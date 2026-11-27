package manager

// v1.3 agent support: copy-on-write clone, box_ensure (idempotent
// get-or-create), session resume, structured observe diff and right-sizing
// from observed usage.

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"boxmes/core/internal/boxyaml"
	"boxmes/core/internal/model"
	"boxmes/core/internal/proto"
	"boxmes/core/internal/provhealth"
	"boxmes/core/internal/secret"
	"boxmes/core/internal/store"
)

// ---- clone ----

// CloneInput is POST /boxes/:id/clone.
type CloneInput struct {
	Slug string `json:"slug"`
}

// Clone makes a copy-on-write clone of a live box on the same host (Incus
// copy on zfs/btrfs = snapshot clone; local driver = reflink/copy). Much
// faster than snapshot → fork because nothing is tarred or uploaded.
func (m *Manager) Clone(ctx context.Context, p *Principal, id string, in CloneInput) (*model.Box, error) {
	if err := p.Require(model.ScopeBoxCreate); err != nil {
		return nil, err
	}
	src, err := m.GetBox(ctx, p, id)
	if err != nil {
		return nil, err
	}
	if src.Status != model.StatusRunning && src.Status != model.StatusSleeping {
		return nil, model.EH(model.ErrInvalid, "Wake the box first (box_sleep wake=true) or fork a snapshot instead.", "cannot clone a box in status %s", src.Status)
	}
	if !m.Hub.Connected(src.HostID) {
		return nil, model.EH(model.ErrProviderFailed, "The host is offline; use box_fork with a snapshot in object storage.", "host %s is offline", src.HostID)
	}
	agent, err := m.Store.GetAgent(ctx, src.AgentID)
	if err != nil {
		return nil, err
	}
	if err := m.checkBudget(ctx, agent); err != nil {
		return nil, err
	}
	q := m.Quotas(ctx)
	if existing, _ := m.Store.ListBoxes(ctx, store.BoxFilter{AgentID: agent.ID}); len(existing) >= q.MaxBoxesPerAgent {
		return nil, model.EH(model.ErrPolicyDenied, "Terminate unused boxes (box down) first.", "agent already has %d boxes (quota %d)", len(existing), q.MaxBoxesPerAgent)
	}
	if err := m.checkBoxQuota(ctx, src.WorkspaceID, BoxQuotaInput{CPU: src.Spec.CPU, RAMMB: src.Spec.RAMMB, New: true}); err != nil {
		return nil, err
	}
	slug, err := m.uniqueSlug(ctx, in.Slug, src.Slug+"-clone")
	if err != nil {
		return nil, err
	}
	if h, err := m.Store.GetHost(ctx, src.HostID); err == nil && h.Billing == "sandbox" {
		return m.cloneSandbox(ctx, p, src, h, agent, slug)
	}
	now := time.Now().UTC()
	nb := &model.Box{ID: secret.ID("box"), WorkspaceID: src.WorkspaceID, Slug: slug, AgentID: src.AgentID, AgentName: src.AgentName, Status: model.StatusProvisioning,
		Lifecycle: src.Lifecycle, Template: src.Template, Spec: src.Spec, Provider: src.Provider, Region: src.Region, HostID: src.HostID,
		TTLSeconds: src.TTLSeconds, IdleMinutes: src.IdleMinutes, BudgetUSD: src.BudgetUSD, CostPerHour: src.CostPerHour, Billing: src.Billing,
		Ports: nonNilInts(src.Ports), Secrets: nonNilStrs(src.Secrets), CreatedAt: now, LastActiveAt: now, Locale: src.Locale, Egress: src.Egress,
		Profile: src.Profile, Requirements: src.Requirements, Isolation: src.Isolation, ClonedFrom: src.ID,
		PlacementReason: "copy-on-write clone of " + src.ID + " on " + src.HostID}
	inheritV17(nb, src)
	m.ensureNames(ctx, nb)
	if err := m.Store.PutBox(ctx, nb); err != nil {
		return nil, err
	}
	var res proto.CloneResult
	cctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	err = m.Hub.Call(cctx, src.HostID, proto.MClone, proto.CloneParams{SourceID: src.ID, BoxID: nb.ID, AgentID: nb.AgentID, AgentsMD: m.agentsMD(ctx, nb, agent)}, &res)
	cancel()
	if err != nil {
		nb.Error = err.Error()
		m.setStatus(ctx, nb, model.StatusFailed, p.Actor(), "clone failed: "+err.Error())
		return nil, mapHostErr(err)
	}
	if h, err := m.Store.GetHost(ctx, nb.HostID); err == nil {
		nb.URL = m.boxURL(ctx, nb, h)
	}
	if nb.Egress != nil && nb.Egress.Source == "box" {
		_ = m.applyEgress(ctx, nb, false)
	}
	nb.Estimate = &model.BoxEstimate{Kind: "clone", CostPerHour: nb.CostPerHour, ExpectedHours: expectedHours(nb), ExpectedTotalUSD: model.Round4(nb.CostPerHour * expectedHours(nb)),
		ETASeconds: round3(float64(res.DurationMS) / 1000), ActualSeconds: round3(time.Since(now).Seconds())}
	nb.PlacementReason += fmt.Sprintf(" (%s, %d ms)", res.Method, res.DurationMS)
	m.setStatus(ctx, nb, model.StatusRunning, p.Actor(), fmt.Sprintf("cloned from %s via %s in %d ms", src.ID, res.Method, res.DurationMS))
	return m.decorateBox(ctx, nb), nil
}

// ---- box_ensure ----

// EnsureInput is POST /boxes/ensure: a create request whose name (or slug)
// is the idempotency key.
type EnsureInput struct {
	boxyaml.Request
}

// EnsureResult says what happened.
type EnsureResult struct {
	Box     *model.Box  `json:"box"`
	Action  string      `json:"action"` // created | reused | woken | recreated
	Created bool        `json:"created"`
	Resume  *ResumeInfo `json:"resume,omitempty"`
}

var ensureMu sync.Map

// Ensure returns the agent's live box with this name (waking it when
// sleeping) or creates it. Calling it twice never creates two boxes.
func (m *Manager) Ensure(ctx context.Context, p *Principal, in EnsureInput) (*EnsureResult, error) {
	if err := p.Require(model.ScopeBoxCreate); err != nil {
		return nil, err
	}
	name := strings.ToLower(strings.TrimSpace(in.Name))
	if name == "" {
		name = strings.ToLower(strings.TrimSpace(in.Slug))
	}
	if name == "" {
		return nil, model.EH(model.ErrInvalid, "Pass a stable name such as \"repo-main\"; the same name returns the same box.", "name is required")
	}
	for _, c := range name {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return nil, model.E(model.ErrInvalid, "slug must be lowercase letters, digits and '-'")
		}
	}
	agent, err := m.agentFor(ctx, p, in.AgentID)
	if err != nil {
		return nil, err
	}
	l, _ := ensureMu.LoadOrStore(agent.ID+"|"+name, &sync.Mutex{})
	mu := l.(*sync.Mutex)
	mu.Lock()
	defer mu.Unlock()
	boxes, _ := m.Store.ListBoxes(ctx, store.BoxFilter{AgentID: agent.ID})
	var found, failed *model.Box
	for _, b := range boxes {
		if b.Name != name && b.Slug != name {
			continue
		}
		switch b.Status {
		case model.StatusTerminated:
			continue
		case model.StatusFailed:
			failed = b
			continue
		}
		if found == nil || b.CreatedAt.After(found.CreatedAt) {
			found = b
		}
	}
	if found != nil {
		action := "reused"
		if found.Status == model.StatusSleeping {
			if _, err := m.Wake(ctx, p, found.ID); err != nil {
				return nil, err
			}
			action = "woken"
		}
		info, _ := m.Resume(ctx, p, found.ID)
		var b *model.Box
		if info != nil {
			b = info.Box
		} else {
			b, _ = m.GetBox(ctx, p, found.ID)
		}
		return &EnsureResult{Box: b, Action: action, Resume: info}, nil
	}
	req := in.Request
	req.AgentID = agent.ID
	req.Name = name
	if req.Slug == "" {
		req.Slug = name
	}
	if failed != nil {
		// keep the name free for the new box; the failed one stays for its logs
		failed.Name = ""
		failed.Slug = failed.Slug + "-failed-" + secret.RandHex(2)
		m.Store.PutBox(ctx, failed)
	}
	b, _, err := m.CreateBox(ctx, p, req, "")
	if err != nil {
		return nil, err
	}
	action := "created"
	if failed != nil {
		action = "recreated"
	}
	return &EnsureResult{Box: b, Action: action, Created: true}, nil
}

// ---- resume ----

// ExecSummary is one recent exec in ResumeInfo.
type ExecSummary struct {
	TS       time.Time `json:"ts"`
	Command  string    `json:"command"`
	ExitCode any       `json:"exit_code"`
	LogID    any       `json:"log_id"`
}

// ResumeInfo is GET /boxes/:id/resume: everything an agent needs to pick a
// session back up after a restart or context reset.
type ResumeInfo struct {
	Box          *model.Box      `json:"box"`
	Woke         bool            `json:"woke"`
	IdleSeconds  float64         `json:"idle_seconds"`
	RecentExecs  []ExecSummary   `json:"recent_execs"`
	RunningJobs  []*model.Job    `json:"running_jobs"`
	LastSnapshot *model.Snapshot `json:"last_snapshot"`
	FilesChanged []string        `json:"files_changed"`
	Listening    []int           `json:"listening_ports"`
	Next         []string        `json:"next"`
}

// Resume wakes the box if needed and summarises its recent state.
func (m *Manager) Resume(ctx context.Context, p *Principal, id string) (*ResumeInfo, error) {
	b, err := m.GetBox(ctx, p, id)
	if err != nil {
		return nil, err
	}
	info := &ResumeInfo{RecentExecs: []ExecSummary{}, RunningJobs: []*model.Job{}, FilesChanged: []string{}, Listening: []int{}}
	if b.Status == model.StatusSleeping {
		if b, err = m.Wake(ctx, p, b.ID); err != nil {
			return nil, err
		}
		info.Woke = true
	}
	info.Box = b
	info.IdleSeconds = math.Round(time.Since(b.LastActiveAt).Seconds())
	evs, _ := m.Store.ListEvents(ctx, store.EventFilter{BoxID: b.ID, Type: "box.exec", Limit: 10})
	for _, e := range evs {
		s := ExecSummary{TS: e.TS, Command: e.Message}
		if i := strings.Index(e.Message, ": "); i >= 0 {
			s.Command = e.Message[i+2:]
		}
		if e.Data != nil {
			s.ExitCode, s.LogID = e.Data["exit_code"], e.Data["log_id"]
		}
		info.RecentExecs = append(info.RecentExecs, s)
	}
	jobs, _ := m.Store.ListJobs(ctx, b.ID, 20)
	for _, j := range jobs {
		if j.Status == "queued" || j.Status == "running" {
			info.RunningJobs = append(info.RunningJobs, j)
		}
	}
	if snaps, _ := m.Store.ListSnapshots(ctx, b.ID); len(snaps) > 0 {
		last := snaps[0]
		for _, s := range snaps {
			if s.CreatedAt.After(last.CreatedAt) {
				last = s
			}
		}
		info.LastSnapshot = last
	}
	if b.Status == model.StatusRunning && m.Hub.Connected(b.HostID) {
		if ov, err := m.observeRaw(ctx, b); err == nil {
			info.FilesChanged, info.Listening = nonNilStrs(ov.FilesChanged), nonNilInts(ov.Ports)
		}
	}
	q := func(s string) string { return fmt.Sprintf("%q", s) }
	info.Next = []string{fmt.Sprintf("box_read(box_id=%s, path=\"/AGENTS.md\")", q(b.ID)), fmt.Sprintf("box_exec(box_id=%s, command=\"git status || ls -la /work\")", q(b.ID))}
	for _, j := range info.RunningJobs {
		info.Next = append([]string{fmt.Sprintf("box_job(job_id=%s, wait_s=30)", q(j.ID))}, info.Next...)
	}
	return info, nil
}

// ---- observe diff ----

// ObserveDiff is what changed since the previous observe call.
type ObserveDiff struct {
	ProcessesStarted []proto.Proc `json:"processes_started"`
	ProcessesExited  []proto.Proc `json:"processes_exited"`
	PortsOpened      []int        `json:"ports_opened"`
	PortsClosed      []int        `json:"ports_closed"`
	First            bool         `json:"first"`
}

// ObserveView extends the observe result with a structured diff and a summary line.
type ObserveView struct {
	proto.ObserveResult
	Diff    ObserveDiff `json:"diff"`
	Summary string      `json:"summary"`
}

func (m *Manager) observeRaw(ctx context.Context, b *model.Box) (*proto.ObserveResult, error) {
	var out proto.ObserveResult
	if err := m.Hub.Call(ctx, b.HostID, proto.MObserve, proto.BoxRef{BoxID: b.ID}, &out); err != nil {
		return nil, mapHostErr(err)
	}
	return &out, nil
}

func diffObserve(prev, cur *proto.ObserveResult) ObserveDiff {
	d := ObserveDiff{ProcessesStarted: []proto.Proc{}, ProcessesExited: []proto.Proc{}, PortsOpened: []int{}, PortsClosed: []int{}}
	if prev == nil {
		d.First = true
		d.ProcessesStarted = append(d.ProcessesStarted, cur.Processes...)
		d.PortsOpened = append(d.PortsOpened, cur.Ports...)
		return d
	}
	pp := map[int]proto.Proc{}
	for _, x := range prev.Processes {
		pp[x.PID] = x
	}
	cp := map[int]bool{}
	for _, x := range cur.Processes {
		cp[x.PID] = true
		if _, ok := pp[x.PID]; !ok {
			d.ProcessesStarted = append(d.ProcessesStarted, x)
		}
	}
	for _, x := range prev.Processes {
		if !cp[x.PID] {
			d.ProcessesExited = append(d.ProcessesExited, x)
		}
	}
	po := map[int]bool{}
	for _, x := range prev.Ports {
		po[x] = true
	}
	co := map[int]bool{}
	for _, x := range cur.Ports {
		co[x] = true
		if !po[x] {
			d.PortsOpened = append(d.PortsOpened, x)
		}
	}
	for _, x := range prev.Ports {
		if !co[x] {
			d.PortsClosed = append(d.PortsClosed, x)
		}
	}
	return d
}

func summarizeObserve(v *ObserveView) string {
	var parts []string
	if n := len(v.FilesChanged); n > 0 {
		f := v.FilesChanged
		if n > 3 {
			f = f[:3]
		}
		s := fmt.Sprintf("%d file(s) changed (%s", n, strings.Join(f, ", "))
		if n > 3 {
			s += ", …"
		}
		parts = append(parts, s+")")
	}
	if n := len(v.Diff.ProcessesStarted); n > 0 {
		parts = append(parts, fmt.Sprintf("%d process(es) started", n))
	}
	if n := len(v.Diff.ProcessesExited); n > 0 {
		parts = append(parts, fmt.Sprintf("%d exited", n))
	}
	if len(v.Diff.PortsOpened) > 0 {
		parts = append(parts, fmt.Sprintf("now listening on %v", v.Diff.PortsOpened))
	}
	if len(v.Diff.PortsClosed) > 0 {
		parts = append(parts, fmt.Sprintf("stopped listening on %v", v.Diff.PortsClosed))
	}
	if len(parts) == 0 {
		return "no changes since the last observe"
	}
	return strings.Join(parts, "; ")
}

// ---- right-sizing ----

type usageSample struct {
	TS    time.Time
	CPU   float64 // percent of one vCPU (100 = one core busy)
	RAMMB float64
}

type usageState struct {
	mu sync.Mutex
	m  map[string][]usageSample
}

// RecordBoxUsage stores a usage sample (from boxd metrics; tests call it directly).
func (m *Manager) RecordBoxUsage(boxID string, cpuPct, ramMB float64, ts time.Time) {
	m.usage.mu.Lock()
	defer m.usage.mu.Unlock()
	if m.usage.m == nil {
		m.usage.m = map[string][]usageSample{}
	}
	l := append(m.usage.m[boxID], usageSample{ts, cpuPct, ramMB})
	if len(l) > 720 { // ~2h at 10s
		l = l[len(l)-720:]
	}
	m.usage.m[boxID] = l
}

// RightSize is GET /boxes/:id/rightsize.
type RightSize struct {
	BoxID           string      `json:"box_id"`
	Slug            string      `json:"slug"`
	Spec            model.Spec  `json:"spec"`
	Samples         int         `json:"samples"`
	WindowS         float64     `json:"window_s"`
	CPUP95Pct       float64     `json:"cpu_p95_pct"` // of one vCPU
	RAMP95MB        float64     `json:"ram_p95_mb"`
	RAMMaxMB        float64     `json:"ram_max_mb"`
	IdleS           float64     `json:"idle_s"`
	Suggestion      string      `json:"suggestion"` // keep | downsize | upsize | sleep | stop | insufficient_data
	SuggestedSpec   *model.Spec `json:"suggested_spec,omitempty"`
	SavingsPerHour  float64     `json:"savings_per_hour"`
	SavingsPerMonth float64     `json:"savings_per_month"`
	Reasons         []string    `json:"reasons"`
}

// RightSizeBox analyses observed usage of one box.
func (m *Manager) RightSizeBox(ctx context.Context, p *Principal, id string) (*RightSize, error) {
	b, err := m.GetBox(ctx, p, id)
	if err != nil {
		return nil, err
	}
	return m.rightSize(b), nil
}

// RightSizeAll analyses every running/sleeping box visible to the caller.
func (m *Manager) RightSizeAll(ctx context.Context, p *Principal) ([]*RightSize, error) {
	boxes, err := m.ListBoxes(ctx, p, store.BoxFilter{})
	if err != nil {
		return nil, err
	}
	out := []*RightSize{}
	for _, b := range boxes {
		if b.Status == model.StatusRunning || b.Status == model.StatusSleeping {
			out = append(out, m.rightSize(b))
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].SavingsPerMonth > out[j].SavingsPerMonth })
	return out, nil
}

func (m *Manager) rightSize(b *model.Box) *RightSize {
	m.usage.mu.Lock()
	samples := append([]usageSample(nil), m.usage.m[b.ID]...)
	m.usage.mu.Unlock()
	r := &RightSize{BoxID: b.ID, Slug: b.Slug, Spec: b.Spec, Samples: len(samples), Reasons: []string{}, Suggestion: "keep"}
	r.IdleS = math.Round(time.Since(b.LastActiveAt).Seconds())
	idleLimit := time.Duration(b.IdleMinutes) * time.Minute
	if idleLimit <= 0 {
		idleLimit = 30 * time.Minute
	}
	if b.Status == model.StatusSleeping {
		r.Suggestion = "keep"
		r.Reasons = append(r.Reasons, "box is sleeping: compute is not billed on pool hosts")
		return r
	}
	if len(samples) < 6 {
		r.Suggestion = "insufficient_data"
		r.Reasons = append(r.Reasons, "need at least 6 usage samples (~1 minute of metrics)")
		if time.Duration(r.IdleS)*time.Second > idleLimit {
			r.Suggestion = idleAction(b)
			r.Reasons = append(r.Reasons, fmt.Sprintf("no agent activity for %.0f min", r.IdleS/60))
		}
		return r
	}
	cpu := make([]float64, len(samples))
	ram := make([]float64, len(samples))
	for i, s := range samples {
		cpu[i], ram[i] = s.CPU, s.RAMMB
		r.RAMMaxMB = math.Max(r.RAMMaxMB, s.RAMMB)
	}
	r.WindowS = math.Round(samples[len(samples)-1].TS.Sub(samples[0].TS).Seconds())
	r.CPUP95Pct = math.Round(provhealth.Percentile(cpu, 95)*10) / 10
	r.RAMP95MB = math.Round(provhealth.Percentile(ram, 95))
	capCPU := b.Spec.CPU * 100
	capRAM := float64(b.Spec.RAMMB)
	switch {
	case time.Duration(r.IdleS)*time.Second > idleLimit && r.CPUP95Pct < 5:
		r.Suggestion = idleAction(b)
		r.Reasons = append(r.Reasons, fmt.Sprintf("idle: no agent call for %.0f min and CPU p95 %.1f%%", r.IdleS/60, r.CPUP95Pct))
	case capRAM > 0 && (r.RAMP95MB > 0.85*capRAM || r.RAMMaxMB > 0.95*capRAM):
		ns := b.Spec
		ns.RAMMB = int(math.Max(float64(b.Spec.RAMMB)*2, roundUpPow2(r.RAMMaxMB*1.3)))
		if r.CPUP95Pct > 0.9*capCPU {
			ns.CPU = b.Spec.CPU * 2
		}
		r.Suggestion, r.SuggestedSpec = "upsize", &ns
		r.Reasons = append(r.Reasons, fmt.Sprintf("RAM p95 %.0f MB / max %.0f MB of %d MB: risk of OOM", r.RAMP95MB, r.RAMMaxMB, b.Spec.RAMMB))
	case capCPU > 0 && r.CPUP95Pct > 0.9*capCPU:
		ns := b.Spec
		ns.CPU = b.Spec.CPU * 2
		r.Suggestion, r.SuggestedSpec = "upsize", &ns
		r.Reasons = append(r.Reasons, fmt.Sprintf("CPU p95 %.0f%% of %.0f%%: CPU bound", r.CPUP95Pct, capCPU))
	case capRAM > 256 && r.RAMP95MB < 0.35*capRAM && r.CPUP95Pct < 0.25*capCPU:
		ns := b.Spec
		ns.RAMMB = int(math.Max(256, roundUpPow2(r.RAMMaxMB*1.5)))
		ns.CPU = math.Max(0.5, math.Ceil(r.CPUP95Pct*1.5/100*2)/2)
		if ns.RAMMB >= b.Spec.RAMMB && ns.CPU >= b.Spec.CPU {
			break
		}
		if ns.CPU > b.Spec.CPU {
			ns.CPU = b.Spec.CPU
		}
		if ns.RAMMB > b.Spec.RAMMB {
			ns.RAMMB = b.Spec.RAMMB
		}
		r.Suggestion, r.SuggestedSpec = "downsize", &ns
		r.Reasons = append(r.Reasons, fmt.Sprintf("uses RAM p95 %.0f MB of %d MB and CPU p95 %.0f%% of %.0f%%", r.RAMP95MB, b.Spec.RAMMB, r.CPUP95Pct, capCPU))
		if b.Spec.CPU > 0 && b.CostPerHour > 0 && b.Billing != "burst" {
			r.SavingsPerHour = model.Round6(b.CostPerHour * (1 - ns.CPU/b.Spec.CPU))
			r.SavingsPerMonth = model.Round4(r.SavingsPerHour * 730)
		}
	default:
		r.Reasons = append(r.Reasons, fmt.Sprintf("sized well: RAM p95 %.0f/%d MB, CPU p95 %.0f/%.0f%%", r.RAMP95MB, b.Spec.RAMMB, r.CPUP95Pct, capCPU))
	}
	if r.Suggestion == "sleep" || r.Suggestion == "stop" {
		r.SavingsPerHour = model.Round6(b.CostPerHour)
		r.SavingsPerMonth = model.Round4(b.CostPerHour * 730)
	}
	return r
}

func idleAction(b *model.Box) string {
	if b.Lifecycle == model.LifecycleService {
		return "stop" // services are never auto-stopped: Boxmes only suggests it
	}
	return "sleep"
}

func roundUpPow2(v float64) float64 {
	if v <= 256 {
		return 256
	}
	return math.Pow(2, math.Ceil(math.Log2(v)))
}
