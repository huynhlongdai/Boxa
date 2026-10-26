package manager

// v1.1 agent-support features: quote (dry-run placement), exec-batch,
// snapshot restore/fork, stored box.yaml, localized AGENTS.md, snippets.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"boxa/core/internal/agentkit"
	"boxa/core/internal/boxyaml"
	"boxa/core/internal/i18n"
	"boxa/core/internal/model"
	"boxa/core/internal/proto"
	"boxa/core/internal/provider"
	"boxa/core/internal/secret"
	"boxa/core/internal/store"
)

// ToolNames is the MCP tool list advertised in AGENTS.md (kept in sync with
// internal/mcp by a test there).
var ToolNames = []string{"box_up", "box_quote", "box_exec", "box_exec_batch", "box_job", "box_logs", "box_read", "box_write",
	"box_grep", "box_observe", "box_snapshot", "box_restore", "box_fork", "box_sleep", "box_down", "box_list", "box_cost", "box_templates"}

// BaseURL is the public URL agents use to reach core.
func (m *Manager) BaseURL() string {
	if m.Cfg.PublicURL != "" {
		return strings.TrimRight(m.Cfg.PublicURL, "/")
	}
	return "http://localhost:8080"
}

// agentLocale: agent.locale → request locale → settings.locale → en.
func (m *Manager) agentLocale(ctx context.Context, a *model.Agent) string {
	if a != nil && i18n.Supported(a.Locale) {
		return a.Locale
	}
	if l, ok := i18n.Lookup(ctx); ok {
		return l
	}
	if l := m.Settings(ctx).Locale; i18n.Supported(l) {
		return l
	}
	return i18n.Default
}

func (m *Manager) agentsMD(ctx context.Context, b *model.Box, a *model.Agent) string {
	loc := b.Locale
	if !i18n.Supported(loc) {
		loc = m.agentLocale(ctx, a)
	}
	t := func(k string, args ...any) string { return i18n.T(loc, k, args...) }
	var budget []string
	if b.BudgetUSD > 0 {
		budget = append(budget, t("agentsmd.budget_box", b.BudgetUSD))
	}
	if a.BudgetDayUSD > 0 {
		budget = append(budget, t("agentsmd.budget_day", a.BudgetDayUSD))
	}
	if a.BudgetMonthUSD > 0 {
		budget = append(budget, t("agentsmd.budget_month", a.BudgetMonthUSD))
	}
	if len(budget) == 0 {
		budget = append(budget, t("agentsmd.budget_none"))
	}
	var rule string
	switch b.Lifecycle {
	case model.LifecycleTask:
		rule = t("agentsmd.rule_task", b.TTLSeconds)
	case model.LifecycleService:
		rule = t("agentsmd.rule_service")
	default:
		rule = t("agentsmd.rule_session", b.IdleMinutes)
	}
	secrets := t("agentsmd.none")
	if len(b.Secrets) > 0 {
		secrets = "`" + strings.Join(b.Secrets, "`, `") + "`"
	}
	ports := ""
	if len(b.Ports) > 0 {
		ps := make([]string, len(b.Ports))
		for i, p := range b.Ports {
			ps[i] = fmt.Sprint(p)
		}
		ports = strings.Join(ps, ", ")
	}
	return i18n.AgentsMD(loc, i18n.AgentsMDData{
		Slug: b.Slug, BoxID: b.ID, Template: b.Template, Lifecycle: b.Lifecycle, AgentName: a.Name, AgentKind: a.Kind,
		CPU: fmt.Sprintf("%g", b.Spec.CPU), RAMMB: b.Spec.RAMMB, DiskGB: b.Spec.DiskGB, URL: b.URL, Ports: ports,
		ForkedFrom: b.ForkedFrom, Budget: strings.Join(budget, "; "), LifecycleRule: rule, Secrets: secrets,
		Tools: ToolNames, Base: m.BaseURL(),
	})
}

// ---- snippets & kinds ----

// ValidKind reports whether kind is a supported agent kind.
func ValidKind(kind string) bool { _, ok := agentkit.Get(kind); return ok }

// Snippet renders the connection snippet for an agent (kind defaults to the
// agent's kind, lang to the agent/request locale).
func (m *Manager) Snippet(ctx context.Context, p *Principal, agentID, kind, lang, base string) (*agentkit.Snippet, error) {
	a, err := m.GetAgent(ctx, p, agentID)
	if err != nil {
		return nil, err
	}
	if kind == "" {
		kind = a.Kind
	}
	if kind != "" && !ValidKind(kind) {
		return nil, model.EH(model.ErrInvalid, "List kinds with GET /agent-kinds.", "unknown agent kind %q", kind)
	}
	loc := i18n.Normalize(lang)
	if loc == "" {
		if l, ok := i18n.Lookup(ctx); ok {
			loc = l
		} else {
			loc = m.agentLocale(ctx, a)
		}
	}
	if base == "" {
		base = m.BaseURL()
	}
	s := agentkit.Build(kind, strings.TrimRight(base, "/"), a.Name, loc)
	return &s, nil
}

// ---- box.yaml ----

func boxYAMLKey(id string) string { return "box_yaml:" + id }

type storedYAML struct {
	YAML   string `json:"yaml"`
	Source string `json:"source"` // request|generated
}

// saveBoxYAML stores the yaml the box was created from (or one generated from
// the normalized request) so GET /boxes/:id/yaml can recreate it.
func (m *Manager) saveBoxYAML(ctx context.Context, b *model.Box, orig string) {
	sy := storedYAML{YAML: orig, Source: "request"}
	if strings.TrimSpace(orig) == "" {
		sy = storedYAML{YAML: renderBoxYAML(b), Source: "generated"}
	}
	m.Store.PutKV(ctx, boxYAMLKey(b.ID), sy)
}

func renderBoxYAML(b *model.Box) string {
	type spec struct {
		CPU  float64 `yaml:"cpu"`
		RAM  string  `yaml:"ram"`
		Disk string  `yaml:"disk"`
	}
	doc := struct {
		Name        string   `yaml:"name,omitempty"`
		Template    string   `yaml:"template"`
		Lifecycle   string   `yaml:"lifecycle"`
		Spec        spec     `yaml:"spec"`
		TTLSeconds  int      `yaml:"ttl_seconds,omitempty"`
		IdleMinutes int      `yaml:"idle_minutes,omitempty"`
		BudgetUSD   float64  `yaml:"budget_usd,omitempty"`
		Region      string   `yaml:"region,omitempty"`
		Ports       []int    `yaml:"ports,omitempty"`
		Secrets     []string `yaml:"secrets,omitempty"`
		Setup       []string `yaml:"setup,omitempty"`
		Egress      *struct {
			Allow         []string `yaml:"allow"`
			DenyByDefault bool     `yaml:"deny_by_default"`
		} `yaml:"egress,omitempty"`
	}{
		Name: b.Slug, Template: b.Template, Lifecycle: b.Lifecycle,
		Spec:       spec{CPU: b.Spec.CPU, RAM: fmt.Sprintf("%dM", b.Spec.RAMMB), Disk: fmt.Sprintf("%dG", b.Spec.DiskGB)},
		TTLSeconds: b.TTLSeconds, BudgetUSD: b.BudgetUSD, Region: b.Region,
		Ports: b.Ports, Secrets: b.Secrets, Setup: b.Setup,
	}
	if b.Lifecycle == model.LifecycleSession {
		doc.IdleMinutes = b.IdleMinutes
	}
	if b.Lifecycle != model.LifecycleTask {
		doc.TTLSeconds = 0
	}
	if b.Egress != nil && b.Egress.Source == "box" {
		doc.Egress = &struct {
			Allow         []string `yaml:"allow"`
			DenyByDefault bool     `yaml:"deny_by_default"`
		}{Allow: b.Egress.Allow, DenyByDefault: b.Egress.DenyByDefault}
	}
	out, _ := yaml.Marshal(doc)
	return string(out)
}

// BoxYAML returns the yaml a box was created from.
func (m *Manager) BoxYAML(ctx context.Context, p *Principal, id string) (map[string]any, error) {
	b, err := m.GetBox(ctx, p, id)
	if err != nil {
		return nil, err
	}
	var sy storedYAML
	if err := m.Store.GetKV(ctx, boxYAMLKey(b.ID), &sy); err != nil || sy.YAML == "" {
		sy = storedYAML{YAML: renderBoxYAML(b), Source: "generated"}
	}
	return map[string]any{"box_id": b.ID, "yaml": sy.YAML, "source": sy.Source}, nil
}

// ---- quote ----

type QuoteOption struct {
	Kind             string  `json:"kind"` // pool|burst
	Provider         string  `json:"provider"`
	Region           string  `json:"region"`
	HostID           string  `json:"host_id,omitempty"`
	SKU              string  `json:"sku,omitempty"`
	CostPerHour      float64 `json:"cost_per_hour"`
	ExpectedTotalUSD float64 `json:"expected_total_usd"`
	RequiresApproval bool    `json:"requires_approval"`
	Label            string  `json:"label"`
}

type QuoteResult struct {
	Available        bool            `json:"available"`
	Provider         string          `json:"provider"`
	Region           string          `json:"region"`
	HostID           string          `json:"host_id,omitempty"`
	Billing          string          `json:"billing"`
	CostPerHour      float64         `json:"cost_per_hour"`
	ExpectedHours    float64         `json:"expected_hours"`
	ExpectedTotalUSD float64         `json:"expected_total_usd"`
	PlacementReason  string          `json:"placement_reason"`
	Alternatives     []QuoteOption   `json:"alternatives"`
	Spec             model.Spec      `json:"spec"`
	Lifecycle        string          `json:"lifecycle"`
	Template         string          `json:"template"`
	WithinBudget     bool            `json:"within_budget"`
	BudgetError      *model.APIError `json:"budget_error,omitempty"`
}

// Quote is a dry-run of CreateBox + Schedule: same validation and pricing,
// no box, no approvals, no side effects.
func (m *Manager) Quote(ctx context.Context, p *Principal, req boxyaml.Request) (*QuoteResult, error) {
	if err := p.Require(model.ScopeBoxCreate); err != nil {
		return nil, err
	}
	if req.YAML != "" {
		parsed, errs := boxyaml.Parse(req.YAML)
		if errs != nil {
			return nil, model.EH(model.ErrInvalid, "Fix box.yaml (validate with POST /templates/validate).", "box.yaml: %s", errs.Error())
		}
		parsed.AgentID = req.AgentID
		req = *parsed
	}
	agent, err := m.agentFor(ctx, p, req.AgentID)
	if err != nil {
		return nil, err
	}
	if _, _, err := m.normalizeRequest(ctx, p, &req, agent); err != nil {
		return nil, err
	}
	b := &model.Box{ID: "quote", Lifecycle: req.Lifecycle, Template: req.Template, Region: req.Region, TTLSeconds: req.TTLSeconds,
		Spec: model.Spec{CPU: req.Spec.CPU, RAMMB: req.Spec.RAMMB, DiskGB: req.Spec.DiskGB}}
	hours := expectedHours(b)
	cands, offers, rejected, err := m.candidates(ctx, b, agent, nil)
	if err != nil {
		return nil, err
	}
	res := &QuoteResult{ExpectedHours: hours, Spec: b.Spec, Lifecycle: b.Lifecycle, Template: b.Template, Alternatives: []QuoteOption{}, WithinBudget: true}
	if berr := m.checkBudget(ctx, agent); berr != nil {
		res.WithinBudget = false
		res.BudgetError = model.AsAPIError(berr)
	}
	connected := func(pid string) bool {
		mp, err := m.Store.GetProvider(ctx, pid)
		return err == nil && mp.Connected && mp.TokenEnc != ""
	}
	var opts []QuoteOption
	for _, c := range cands {
		opts = append(opts, QuoteOption{Kind: "pool", Provider: c.host.Provider, Region: c.host.Region, HostID: c.host.ID,
			CostPerHour: model.Round6(c.costPerHour), ExpectedTotalUSD: model.Round4(c.costPerHour * hours), Label: c.label})
		if c.host.Billing == "burst" {
			opts[len(opts)-1].Kind = "burst"
		}
	}
	for i, c := range offers {
		if i >= 5 {
			break
		}
		opts = append(opts, QuoteOption{Kind: "burst", Provider: c.offer.Provider, Region: c.offer.Region, SKU: c.offer.SKU,
			CostPerHour: model.Round6(c.offer.PriceHour), ExpectedTotalUSD: model.Round4(provider.Quote(*c.offer, hours, m.Cfg.SetupCostUSD)),
			RequiresApproval: !m.Cfg.AutoBurst || !connected(c.offer.Provider), Label: c.label})
	}
	// Pick exactly like Schedule: cheapest pool host unless an auto-burst offer is cheaper.
	best := -1
	if len(cands) > 0 {
		best = 0
	}
	if len(offers) > 0 && m.Cfg.AutoBurst && connected(offers[0].offer.Provider) && (len(cands) == 0 || offers[0].cost < cands[0].cost) {
		best = len(cands) // first offer option
	}
	if best >= 0 && best < len(opts) {
		o := opts[best]
		res.Available = true
		res.Provider, res.Region, res.HostID, res.CostPerHour, res.ExpectedTotalUSD = o.Provider, o.Region, o.HostID, o.CostPerHour, o.ExpectedTotalUSD
		res.Billing = o.Kind
		if o.HostID != "" {
			if c := cands[best]; c.host != nil {
				res.Billing = c.host.Billing
			}
		}
		res.PlacementReason = fmt.Sprintf("would choose %s | expected %.1fh (%s)", o.Label, hours, b.Lifecycle)
		opts = append(opts[:best], opts[best+1:]...)
	} else {
		res.PlacementReason = fmt.Sprintf("no host fits cpu=%.1f ram=%dMB disk=%dGB", b.Spec.CPU, b.Spec.RAMMB, b.Spec.DiskGB)
		if len(rejected) > 0 {
			res.PlacementReason += " [" + strings.Join(rejected, "; ") + "]"
		}
	}
	if len(opts) > 6 {
		opts = opts[:6]
	}
	res.Alternatives = append(res.Alternatives, opts...)
	return res, nil
}

// ---- exec batch ----

// BatchCommand is a string ("ls") or an object ({"command":"ls","cwd":"/work"}).
type BatchCommand struct {
	Command  string `json:"command"`
	Cwd      string `json:"cwd,omitempty"`
	TimeoutS int    `json:"timeout_s,omitempty"`
}

func (c *BatchCommand) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		c.Command = s
		return nil
	}
	type alias BatchCommand
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*c = BatchCommand(a)
	return nil
}

type BatchInput struct {
	Commands    []BatchCommand `json:"commands"`
	StopOnError *bool          `json:"stop_on_error"` // default true
	Cwd         string         `json:"cwd"`
	TimeoutS    int            `json:"timeout_s"`
	TailLines   int            `json:"tail_lines"`
}

type BatchItem struct {
	Index   int    `json:"index"`
	Command string `json:"command"`
	model.ExecResult
}

type BatchResult struct {
	OK          bool        `json:"ok"`
	Results     []BatchItem `json:"results"`
	Completed   int         `json:"completed"`
	StoppedAt   *int        `json:"stopped_at"`
	DurationMS  int64       `json:"duration_ms"`
	TokensSaved int         `json:"tokens_saved"`
}

const maxBatch = 50

// ExecBatch runs commands sequentially in one call (stop_on_error default true).
func (m *Manager) ExecBatch(ctx context.Context, p *Principal, id string, in BatchInput) (*BatchResult, error) {
	if err := p.Require(model.ScopeBoxExec); err != nil {
		return nil, err
	}
	if len(in.Commands) == 0 {
		return nil, model.E(model.ErrInvalid, "commands is required")
	}
	if len(in.Commands) > maxBatch {
		return nil, model.E(model.ErrInvalid, "at most %d commands per batch", maxBatch)
	}
	for i, c := range in.Commands {
		if strings.TrimSpace(c.Command) == "" {
			return nil, model.E(model.ErrInvalid, "command %d is empty", i)
		}
	}
	stop := in.StopOnError == nil || *in.StopOnError
	b, err := m.usable(ctx, p, id)
	if err != nil {
		return nil, err
	}
	m.touch(ctx, b)
	start := time.Now()
	res := &BatchResult{OK: true, Results: []BatchItem{}}
	for i, c := range in.Commands {
		ei := ExecInput{Command: c.Command, Cwd: c.Cwd, TimeoutS: c.TimeoutS, TailLines: in.TailLines}
		if ei.Cwd == "" {
			ei.Cwd = in.Cwd
		}
		if ei.TimeoutS <= 0 {
			ei.TimeoutS = in.TimeoutS
		}
		if ei.TimeoutS <= 0 {
			ei.TimeoutS = 300
		}
		if ei.TimeoutS > 3600 {
			ei.TimeoutS = 3600
		}
		r, err := m.execSync(ctx, p.Actor(), b, ei)
		if err != nil {
			if len(res.Results) == 0 {
				return nil, err
			}
			ae := model.AsAPIError(err)
			r = &model.ExecResult{ExitCode: -1, StderrTail: ae.Code + ": " + ae.Message, FilesChanged: []string{}}
		}
		res.Results = append(res.Results, BatchItem{Index: i, Command: c.Command, ExecResult: *r})
		res.Completed++
		res.TokensSaved += r.TokensSaved
		if r.ExitCode != 0 {
			res.OK = false
			if stop {
				idx := i
				res.StoppedAt = &idx
				break
			}
		}
	}
	res.DurationMS = time.Since(start).Milliseconds()
	return res, nil
}

// ---- restore / fork ----

func (m *Manager) snapshotFor(ctx context.Context, p *Principal, b *model.Box, snapID string) (*model.Snapshot, *model.Box, error) {
	if snapID == "" {
		snaps, err := m.Store.ListSnapshots(ctx, b.ID)
		if err != nil || len(snaps) == 0 {
			return nil, nil, model.EH(model.ErrNotFound, "Take one first with box_snapshot, or pass snapshot_id.", "box %s has no snapshots", b.Slug)
		}
		return snaps[0], b, nil
	}
	sn, err := m.Store.GetSnapshot(ctx, snapID)
	if err != nil {
		return nil, nil, notFound("snapshot", snapID)
	}
	src, err := m.Store.GetBox(ctx, sn.BoxID)
	if err != nil || (!p.Admin && src.AgentID != p.AgentID) {
		return nil, nil, notFound("snapshot", snapID)
	}
	return sn, src, nil
}

type RestoreInput struct {
	SnapshotID string `json:"snapshot_id"`
	Slug       string `json:"slug"`
}

// Restore replaces the box's /work and /memory with a snapshot (latest when
// snapshot_id is empty). The snapshot must live on the box's host.
func (m *Manager) Restore(ctx context.Context, p *Principal, id string, in RestoreInput) (*model.Box, error) {
	if err := p.Require(model.ScopeSnapshot); err != nil {
		return nil, err
	}
	b, err := m.GetBox(ctx, p, id)
	if err != nil {
		return nil, err
	}
	sn, src, err := m.snapshotFor(ctx, p, b, in.SnapshotID)
	if err != nil {
		return nil, err
	}
	if b.Status != model.StatusRunning && b.Status != model.StatusSleeping {
		return nil, model.EH(model.ErrInvalid, "Use box_fork to start a new box from this snapshot.", "cannot restore a box in status %s", b.Status)
	}
	download := ""
	if sn.HostID != "" && sn.HostID != b.HostID {
		if download = m.presignSnapshot(ctx, sn, "GET"); download == "" {
			return nil, model.EH(model.ErrInvalid, "Use box_fork to start a new box from this snapshot.", "snapshot %s is on another host", sn.ID)
		}
	}
	if !m.Hub.Connected(b.HostID) {
		return nil, model.EH(model.ErrProviderFailed, "The host is offline; retry later.", "host %s offline", b.HostID)
	}
	lock := m.boxLock(b.ID)
	lock.Lock()
	defer lock.Unlock()
	cctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	if err := m.Hub.Call(cctx, b.HostID, proto.MRestore, proto.RestoreParams{BoxID: b.ID, SourceBoxID: src.ID, SnapshotID: sn.ID, Location: sn.Location, DownloadURL: download}, nil); err != nil {
		return nil, mapHostErr(err)
	}
	b, _ = m.Store.GetBox(ctx, b.ID)
	b.LastActiveAt = time.Now().UTC()
	m.Store.PutBox(ctx, b)
	m.EmitData(ctx, p.Actor(), b.ID, b.AgentID, "box.restored", fmt.Sprintf("restored snapshot %s", sn.ID), nil, map[string]any{"snapshot_id": sn.ID, "source_box_id": src.ID})
	m.publishBox(b)
	return m.decorateBox(ctx, b), nil
}

// Fork creates a new box from a snapshot of box id (latest, or a fresh one
// taken now when the box has none and is running). The fork is placed on the
// snapshot's host.
func (m *Manager) Fork(ctx context.Context, p *Principal, id string, in RestoreInput) (*model.Box, error) {
	if err := p.Require(model.ScopeBoxCreate); err != nil {
		return nil, err
	}
	src, err := m.GetBox(ctx, p, id)
	if err != nil {
		return nil, err
	}
	var sn *model.Snapshot
	if in.SnapshotID == "" {
		if snaps, _ := m.Store.ListSnapshots(ctx, src.ID); len(snaps) > 0 {
			sn = snaps[0]
		} else if src.Status == model.StatusRunning || src.Status == model.StatusSleeping {
			if err := p.Require(model.ScopeSnapshot); err != nil {
				return nil, err
			}
			if sn, err = m.snapshotBox(ctx, src, p.Actor()); err != nil {
				return nil, err
			}
		}
	}
	if sn == nil {
		if sn, src, err = m.snapshotFor(ctx, p, src, in.SnapshotID); err != nil {
			return nil, err
		}
	}
	agent, err := m.Store.GetAgent(ctx, src.AgentID)
	if err != nil {
		return nil, notFound("agent", src.AgentID)
	}
	if err := m.checkBudget(ctx, agent); err != nil {
		return nil, err
	}
	q := m.Quotas(ctx)
	if existing, _ := m.Store.ListBoxes(ctx, store.BoxFilter{AgentID: agent.ID}); len(existing) >= q.MaxBoxesPerAgent {
		return nil, model.EH(model.ErrPolicyDenied, "Terminate unused boxes (box down) first.", "agent already has %d boxes (quota %d)", len(existing), q.MaxBoxesPerAgent)
	}
	hostID := sn.HostID
	if hostID == "" {
		hostID = src.HostID
	}
	// Snapshots in object storage can be forked onto any host; local ones
	// only on the host that holds them.
	download := m.presignSnapshot(ctx, sn, "GET")
	if download == "" && !m.Hub.Connected(hostID) {
		return nil, model.EH(model.ErrProviderFailed, "The host is offline; retry later.", "host %s offline", hostID)
	}
	slug, err := m.uniqueSlug(ctx, in.Slug, src.Slug+"-fork")
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	b := &model.Box{
		ID: secret.ID("box"), Slug: slug, AgentID: agent.ID, AgentName: agent.Name, Status: model.StatusPlacing,
		Lifecycle: src.Lifecycle, Template: src.Template, Spec: src.Spec, Region: src.Region, TTLSeconds: src.TTLSeconds,
		IdleMinutes: src.IdleMinutes, BudgetUSD: src.BudgetUSD, Ports: nonNilInts(src.Ports), Secrets: nonNilStrs(src.Secrets),
		Setup: src.Setup, CreatedAt: now, LastActiveAt: now, Locale: src.Locale, ForkedFrom: sn.ID,
	}
	if src.Egress != nil {
		eg := *src.Egress
		eg.Allow = append([]string{}, src.Egress.Allow...)
		eg.Enforced, eg.AppliedAt = false, nil
		b.Egress = &eg
	}
	// Local snapshot: place on its host. Remote (s3): any host, preferring the snapshot host on ties.
	exclude := map[string]bool{}
	if download == "" {
		hosts, _ := m.Store.ListHosts(ctx)
		for _, h := range hosts {
			if h.ID != hostID {
				exclude[h.ID] = true
			}
		}
	}
	cands, _, _, err := m.candidates(ctx, b, agent, exclude)
	if err != nil {
		return nil, err
	}
	if len(cands) == 0 {
		return nil, model.EH(model.ErrNoCapacity, "Free capacity on that host (terminate boxes) or restore into an existing box.", "snapshot host %s has no room for the fork", hostID)
	}
	c := cands[0]
	for _, x := range cands {
		if x.host.ID == hostID && x.cost <= c.cost {
			c = x // same host: no download needed
			break
		}
	}
	if c.host.ID == hostID {
		download = ""
	}
	b.HostID, b.Provider, b.Region = c.host.ID, c.host.Provider, c.host.Region
	b.CostPerHour, b.Billing = model.Round6(c.costPerHour), c.host.Billing
	b.PlacementReason = "fork of " + src.ID + " from snapshot " + sn.ID + ": " + c.label
	b.URL = m.boxURL(ctx, b, c.host)
	if err := m.Store.PutBox(ctx, b); err != nil {
		return nil, err
	}
	var sy storedYAML
	if m.Store.GetKV(ctx, boxYAMLKey(src.ID), &sy) == nil && sy.YAML != "" {
		m.Store.PutKV(ctx, boxYAMLKey(b.ID), sy)
	} else {
		m.saveBoxYAML(ctx, b, "")
	}
	m.Emit(ctx, p.Actor(), b.ID, agent.ID, "box.requested", fmt.Sprintf("box %s forked from %s (snapshot %s)", b.Slug, src.Slug, sn.ID), nil)
	lock := m.boxLock(b.ID)
	lock.Lock()
	defer lock.Unlock()
	m.setStatus(ctx, b, model.StatusProvisioning, "scheduler", b.PlacementReason)
	cctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	err = m.Hub.Call(cctx, b.HostID, proto.MRestore, proto.RestoreParams{
		BoxID: b.ID, SourceBoxID: src.ID, SnapshotID: sn.ID, Location: sn.Location, Create: true, DownloadURL: download,
		Box: proto.CreateParams{BoxID: b.ID, Template: b.Template, Spec: b.Spec, AgentID: b.AgentID, AgentsMD: m.agentsMD(ctx, b, agent)},
	}, nil)
	if err != nil {
		b.Error = err.Error()
		m.setStatus(ctx, b, model.StatusFailed, "system", b.Error)
		return nil, mapHostErr(err)
	}
	if err := m.applyEgress(ctx, b, false); err != nil {
		b.Error = err.Error()
		m.Hub.Call(ctx, b.HostID, proto.MDelete, proto.BoxRef{BoxID: b.ID}, nil)
		m.setStatus(ctx, b, model.StatusFailed, "system", "egress policy failed: "+b.Error)
		return nil, err
	}
	b.LastActiveAt = time.Now().UTC()
	m.setStatus(ctx, b, model.StatusRunning, "system", fmt.Sprintf("forked from snapshot %s on %s", sn.ID, b.HostID))
	return m.decorateBox(ctx, b), nil
}
