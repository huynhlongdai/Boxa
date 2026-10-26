package manager

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"boxa/core/internal/boxd"
	"boxa/core/internal/boxyaml"
	"boxa/core/internal/hostlink"
	"boxa/core/internal/model"
	"boxa/core/internal/proto"
	"boxa/core/internal/provider"
	"boxa/core/internal/secret"
	"boxa/core/internal/store"
)

const maxProvisionAttempts = 3 // first try + 2 retries (spec M3)

// CreateBox validates, records and starts provisioning a box. When wait is
// true it blocks until the box is running/failed (or CreateWait elapses).
func (m *Manager) CreateBox(ctx context.Context, p *Principal, req boxyaml.Request, idemKey string) (*model.Box, bool, error) {
	if err := p.Require(model.ScopeBoxCreate); err != nil {
		return nil, false, err
	}
	settings := m.Settings(ctx)
	if settings.Maintenance.Enabled {
		msg := settings.Maintenance.Message
		if msg == "" {
			msg = "maintenance mode"
		}
		return nil, false, model.EH(model.ErrPolicyDenied, "Boxa is in maintenance; retry later.", "%s", msg)
	}
	if req.YAML != "" {
		parsed, errs := boxyaml.Parse(req.YAML)
		if errs != nil {
			return nil, false, model.EH(model.ErrInvalid, "Fix box.yaml (validate with POST /templates/validate).", "box.yaml: %s", errs.Error())
		}
		parsed.AgentID = req.AgentID
		parsed.Wait = req.Wait
		req = *parsed
	}
	agent, err := m.agentFor(ctx, p, req.AgentID)
	if err != nil {
		return nil, false, err
	}
	if idemKey != "" {
		if id, err := m.Store.GetIdempotent(ctx, agent.ID+"|"+idemKey); err == nil {
			b, err := m.Store.GetBox(ctx, id)
			if err == nil {
				return m.decorateBox(ctx, b), false, nil
			}
		}
	}
	// Template resolution: built-in or custom (custom YAML provides defaults).
	if req.Template == "" {
		req.Template = "base"
	}
	baseTemplate := req.Template
	tpl, err := m.Store.GetTemplate(ctx, req.Template)
	if err != nil {
		return nil, false, model.EH(model.ErrInvalid, "Use one of GET /templates.", "template %q not found", req.Template)
	}
	if !tpl.Builtin {
		if def, errs := boxyaml.Parse(tpl.YAML); errs == nil {
			baseTemplate = def.Template
			if req.Spec.CPU == 0 {
				req.Spec.CPU = def.Spec.CPU
			}
			if req.Spec.RAMMB == 0 {
				req.Spec.RAMMB = def.Spec.RAMMB
			}
			if req.Spec.DiskGB == 0 {
				req.Spec.DiskGB = def.Spec.DiskGB
			}
			if req.Lifecycle == "" {
				req.Lifecycle = def.Lifecycle
			}
			req.Setup = append(append([]string{}, def.Setup...), req.Setup...)
			if len(req.Secrets) == 0 {
				req.Secrets = def.Secrets
			}
			if len(req.Ports) == 0 {
				req.Ports = def.Ports
			}
		}
		if !boxyaml.IsBuiltin(baseTemplate) {
			baseTemplate = "base"
		}
	}
	tpl.Uses++
	m.Store.PutTemplate(ctx, tpl)
	// Defaults + validation.
	if req.Spec.CPU <= 0 {
		req.Spec.CPU = 1
	}
	if req.Spec.RAMMB <= 0 {
		req.Spec.RAMMB = 1024
	}
	if req.Spec.DiskGB <= 0 {
		req.Spec.DiskGB = 10
	}
	switch req.Lifecycle {
	case "":
		req.Lifecycle = model.LifecycleSession
	case model.LifecycleTask, model.LifecycleSession, model.LifecycleService:
	default:
		return nil, false, model.E(model.ErrInvalid, "lifecycle must be task, session or service")
	}
	q := m.Quotas(ctx)
	if req.Spec.CPU > q.MaxCPUPerBox || req.Spec.RAMMB > q.MaxRAMMBPerBox || req.Spec.DiskGB > q.MaxDiskGBPerBox {
		return nil, false, model.EH(model.ErrPolicyDenied, "Request a smaller spec or ask the operator to raise quotas.",
			"spec exceeds quota (max cpu %.0f, ram %d MB, disk %d GB)", q.MaxCPUPerBox, q.MaxRAMMBPerBox, q.MaxDiskGBPerBox)
	}
	existing, _ := m.Store.ListBoxes(ctx, store.BoxFilter{AgentID: agent.ID})
	if len(existing) >= q.MaxBoxesPerAgent {
		return nil, false, model.EH(model.ErrPolicyDenied, "Terminate unused boxes (box down) first.", "agent already has %d boxes (quota %d)", len(existing), q.MaxBoxesPerAgent)
	}
	for _, port := range req.Ports {
		if port < 1 || port > 65535 {
			return nil, false, model.E(model.ErrInvalid, "invalid port %d", port)
		}
	}
	if len(req.Secrets) > 0 {
		if err := p.Require(model.ScopeSecretUse); err != nil {
			return nil, false, err
		}
		for _, name := range req.Secrets {
			s, err := m.Store.GetSecret(ctx, name)
			if err != nil {
				return nil, false, model.EH(model.ErrInvalid, "Create it first with POST /secrets.", "secret %q not found", name)
			}
			if len(s.Agents) > 0 && !contains(s.Agents, agent.ID) {
				return nil, false, model.E(model.ErrPolicyDenied, "secret %q is not shared with this agent", name)
			}
		}
	}
	if err := m.checkBudget(ctx, agent); err != nil {
		return nil, false, err
	}
	idle := req.IdleMinutes
	if idle <= 0 {
		idle = settings.Reaper.IdleMinutes
	}
	ttl := req.TTLSeconds
	if ttl <= 0 && req.Lifecycle == model.LifecycleTask {
		ttl = settings.Reaper.DefaultTTLSeconds
	}
	slug, err := m.uniqueSlug(ctx, req.Slug, baseTemplate)
	if err != nil {
		return nil, false, err
	}
	now := time.Now().UTC()
	b := &model.Box{
		ID: secret.ID("box"), Slug: slug, AgentID: agent.ID, AgentName: agent.Name, Status: model.StatusRequested,
		Lifecycle: req.Lifecycle, Template: req.Template, Spec: model.Spec{CPU: req.Spec.CPU, RAMMB: req.Spec.RAMMB, DiskGB: req.Spec.DiskGB},
		Region: req.Region, TTLSeconds: ttl, IdleMinutes: idle, BudgetUSD: req.BudgetUSD,
		Ports: nonNilInts(req.Ports), Secrets: nonNilStrs(req.Secrets), Setup: req.Setup, CreatedAt: now, LastActiveAt: now,
	}
	if err := m.Store.PutBox(ctx, b); err != nil {
		return nil, false, err
	}
	if idemKey != "" {
		if err := m.Store.PutIdempotent(ctx, agent.ID+"|"+idemKey, b.ID); err != nil {
			// lost a race with an identical request: return the winner
			if id, err2 := m.Store.GetIdempotent(ctx, agent.ID+"|"+idemKey); err2 == nil && id != b.ID {
				b.Status, b.Error = model.StatusTerminated, "duplicate idempotent request"
				m.Store.PutBox(ctx, b)
				w, _ := m.Store.GetBox(ctx, id)
				return m.decorateBox(ctx, w), false, nil
			}
		}
	}
	m.Emit(ctx, p.Actor(), b.ID, agent.ID, "box.requested", fmt.Sprintf("box %s requested (%s, %s, cpu=%.1f ram=%dMB)", b.Slug, b.Template, b.Lifecycle, b.Spec.CPU, b.Spec.RAMMB), nil)
	m.publishBox(b)
	done := make(chan struct{})
	go func() {
		m.provisionWithTemplate(b.ID, baseTemplate)
		close(done)
	}()
	wait := req.Wait == nil || *req.Wait
	if wait {
		select {
		case <-done:
		case <-time.After(m.Cfg.CreateWait):
		case <-ctx.Done():
		}
	}
	nb, err := m.Store.GetBox(ctx, b.ID)
	if err != nil {
		return nil, false, err
	}
	return m.decorateBox(ctx, nb), true, nil
}

func nonNilInts(v []int) []int {
	if v == nil {
		return []int{}
	}
	return v
}
func nonNilStrs(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

func (m *Manager) uniqueSlug(ctx context.Context, want, tmpl string) (string, error) {
	if want != "" {
		want = strings.ToLower(want)
		for _, c := range want {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return "", model.E(model.ErrInvalid, "slug must be lowercase letters, digits and '-'")
			}
		}
	} else {
		want = strings.ReplaceAll(tmpl, "_", "-")
	}
	active, _ := m.Store.ListBoxes(ctx, store.BoxFilter{})
	taken := map[string]bool{}
	for _, b := range active {
		taken[b.Slug] = true
	}
	if !taken[want] && want != tmpl {
		return want, nil
	}
	for i := 0; i < 20; i++ {
		s := want + "-" + secret.ID("x")[2:6]
		if !taken[s] {
			return s, nil
		}
	}
	return want + "-" + secret.RandHex(4), nil
}

func (m *Manager) provision(id string) { m.provisionWithTemplate(id, "") }

// provisionWithTemplate drives requested → placing → provisioning → running,
// retrying up to 2 times on another host (spec M3).
func (m *Manager) provisionWithTemplate(id, baseTemplate string) {
	ctx := context.Background()
	lock := m.boxLock(id)
	lock.Lock()
	defer lock.Unlock()
	exclude := map[string]bool{}
	var lastErr error
	for attempt := 1; attempt <= maxProvisionAttempts; attempt++ {
		b, err := m.Store.GetBox(ctx, id)
		if err != nil || b.Status == model.StatusTerminated {
			return
		}
		if baseTemplate == "" {
			baseTemplate = b.Template
			if !boxyaml.IsBuiltin(baseTemplate) {
				if t, err := m.Store.GetTemplate(ctx, b.Template); err == nil {
					if def, errs := boxyaml.Parse(t.YAML); errs == nil && boxyaml.IsBuiltin(def.Template) {
						baseTemplate = def.Template
					}
				}
				if !boxyaml.IsBuiltin(baseTemplate) {
					baseTemplate = "base"
				}
			}
		}
		b.Retries = attempt - 1
		m.setStatus(ctx, b, model.StatusPlacing, "system", "")
		agent, err := m.Store.GetAgent(ctx, b.AgentID)
		if err != nil {
			lastErr = err
			break
		}
		pl, err := m.Schedule(ctx, b, agent, exclude)
		if err != nil {
			lastErr = err
			var ae *model.APIError
			if errors.As(err, &ae) && ae.Code == model.ErrNoCapacity {
				break // retrying won't create capacity
			}
			continue
		}
		host := pl.Host
		if host == nil && pl.Offer != nil {
			host, err = m.rentBurstHost(ctx, pl.Offer)
			if err != nil {
				lastErr = err
				continue
			}
		}
		b.HostID, b.Provider, b.Region = host.ID, host.Provider, host.Region
		b.CostPerHour, b.Billing, b.PlacementReason = pl.CostPerHour, pl.Billing, pl.Reason
		b.URL = m.boxURL(ctx, b, host)
		m.setStatus(ctx, b, model.StatusProvisioning, "scheduler", pl.Reason)
		cctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
		err = m.Hub.Call(cctx, host.ID, proto.MCreate, proto.CreateParams{
			BoxID: b.ID, Template: baseTemplate, Spec: b.Spec, Setup: b.Setup, AgentID: b.AgentID,
			AgentsMD: m.agentsMD(ctx, b, agent),
		}, nil)
		cancel()
		if err != nil {
			lastErr = err
			exclude[host.ID] = true
			m.Emit(ctx, "system", b.ID, b.AgentID, "box.provision_failed", fmt.Sprintf("attempt %d on %s failed: %v", attempt, host.ID, err), nil)
			dctx, dc := context.WithTimeout(ctx, time.Minute)
			_ = m.Hub.Call(dctx, host.ID, proto.MDelete, proto.BoxRef{BoxID: b.ID}, nil)
			dc()
			continue
		}
		b.LastActiveAt = time.Now().UTC()
		b.Error = ""
		m.setStatus(ctx, b, model.StatusRunning, "system", fmt.Sprintf("running on %s (%s/h)", host.ID, fmtUSD(b.CostPerHour)))
		return
	}
	b, err := m.Store.GetBox(ctx, id)
	if err != nil {
		return
	}
	msg := "provisioning failed"
	if lastErr != nil {
		msg = lastErr.Error()
		var ae *model.APIError
		if errors.As(lastErr, &ae) {
			msg = ae.Code + ": " + ae.Message
		}
	}
	b.Error = msg
	m.setStatus(ctx, b, model.StatusFailed, "system", msg)
}

// rentBurstHost creates an hourly host for an offer and waits for its boxd.
func (m *Manager) rentBurstHost(ctx context.Context, o *model.Offer) (*model.Host, error) {
	sys := &Principal{Key: &model.APIKey{Scopes: []string{model.ScopeAdmin}}, Admin: true}
	h, err := m.CreateHost(ctx, sys, o.Provider, o.SKU, o.Region, "burst")
	if err != nil {
		return nil, err
	}
	if err := m.waitHostReady(ctx, h.ID, 10*time.Minute); err != nil {
		hh, _ := m.Store.GetHost(ctx, h.ID)
		if hh != nil {
			m.destroyHost(ctx, hh, "system", "never came online")
		}
		return nil, err
	}
	return m.Store.GetHost(ctx, h.ID)
}

func (m *Manager) boxURL(ctx context.Context, b *model.Box, h *model.Host) string {
	if len(b.Ports) == 0 {
		return ""
	}
	s := m.Settings(ctx)
	var u string
	if s.BaseDomain == "" || s.BaseDomain == "sslip.io" {
		if h.IP == "" {
			return ""
		}
		u = "https://" + b.Slug + "-" + strings.ReplaceAll(h.IP, ".", "-") + ".sslip.io/"
	} else {
		u = "https://" + b.Slug + ".box." + s.BaseDomain + "/"
	}
	if s.URLTokenProtect {
		u += "?boxa_token=" + m.URLToken(b.ID)
	}
	return u
}

func (m *Manager) agentsMD(ctx context.Context, b *model.Box, a *model.Agent) string {
	budget := "none"
	if b.BudgetUSD > 0 {
		budget = fmt.Sprintf("$%.2f for this box", b.BudgetUSD)
	}
	if a.BudgetDayUSD > 0 {
		budget += fmt.Sprintf(", $%.2f/day for agent %s", a.BudgetDayUSD, a.Name)
	}
	return fmt.Sprintf(`# AGENTS.md — Boxa box %s

- Agent: %s (%s)
- Box: %s, lifecycle=%s, template=%s, spec cpu=%.1f ram=%dMB disk=%dGB
- Work dir: /work (persists across sleep/wake). Agent memory: /memory (shared by all boxes of this agent).
- Budget: %s. Idle %d min → sleep (session). TTL %ds (task).
- Secrets are injected as environment variables on each exec: %s
- Tools: box_exec, box_read, box_write, box_grep, box_observe, box_snapshot, box_sleep, box_down (MCP) or the `+"`box`"+` CLI.
- Output of exec is trimmed to the last 60 lines; full logs via log_id.
`, b.Slug, a.Name, a.Kind, b.ID, b.Lifecycle, b.Template, b.Spec.CPU, b.Spec.RAMMB, b.Spec.DiskGB, budget, b.IdleMinutes, b.TTLSeconds, strings.Join(b.Secrets, ", "))
}

// setStatus persists a transition, emits an event and publishes box.updated.
func (m *Manager) setStatus(ctx context.Context, b *model.Box, status, actor, msg string) {
	prev := b.Status
	b.Status = status
	if status == model.StatusTerminated {
		now := time.Now().UTC()
		b.TerminatedAt = &now
	}
	if err := m.Store.PutBox(ctx, b); err != nil {
		log.Printf("box %s: %v", b.ID, err)
	}
	if prev != status {
		text := fmt.Sprintf("%s → %s", prev, status)
		if msg != "" {
			text += ": " + msg
		}
		var cost *float64
		if status == model.StatusTerminated {
			c := model.Round4(b.SpentUSD)
			cost = &c
		}
		m.Emit(ctx, actor, b.ID, b.AgentID, "box."+status, text, cost)
	}
	m.publishBox(m.decorateBox(ctx, b))
}

func (m *Manager) decorateBox(ctx context.Context, b *model.Box) *model.Box {
	if b.Ports == nil {
		b.Ports = []int{}
	}
	if b.Secrets == nil {
		b.Secrets = []string{}
	}
	b.SpentUSD = model.Round4(b.SpentUSD)
	return b
}

// GetBox enforces ownership.
func (m *Manager) GetBox(ctx context.Context, p *Principal, id string) (*model.Box, error) {
	b, err := m.Store.GetBox(ctx, id)
	if err != nil {
		return nil, notFound("box", id)
	}
	if !p.Admin && b.AgentID != p.AgentID {
		return nil, notFound("box", id)
	}
	return m.decorateBox(ctx, b), nil
}

func (m *Manager) ListBoxes(ctx context.Context, p *Principal, f store.BoxFilter) ([]*model.Box, error) {
	if !p.Admin {
		f.AgentID = p.AgentID
	} else if f.AgentID != "" {
		if a, err := m.Store.GetAgentByName(ctx, f.AgentID); err == nil {
			f.AgentID = a.ID
		}
	}
	bs, err := m.Store.ListBoxes(ctx, f)
	if err != nil {
		return nil, err
	}
	for _, b := range bs {
		m.decorateBox(ctx, b)
	}
	return bs, nil
}

func (m *Manager) touch(ctx context.Context, b *model.Box) {
	b.LastActiveAt = time.Now().UTC()
	m.Store.PutBox(ctx, b)
}

// usable returns the box ready for data-plane calls, auto-waking sleepers.
func (m *Manager) usable(ctx context.Context, p *Principal, id string) (*model.Box, error) {
	b, err := m.GetBox(ctx, p, id)
	if err != nil {
		return nil, err
	}
	if b.TakenOver && !p.Admin {
		return nil, model.EH(model.ErrPolicyDenied, "A human has taken over this box; wait for release.", "box %s is under human takeover", b.Slug)
	}
	switch b.Status {
	case model.StatusRunning:
	case model.StatusSleeping:
		if _, err := m.Wake(ctx, p, b.ID); err != nil {
			return nil, err
		}
		b, _ = m.Store.GetBox(ctx, b.ID)
	case model.StatusRequested, model.StatusPlacing, model.StatusProvisioning:
		return nil, model.EH(model.ErrInvalid, "The box is still starting; poll GET /boxes/:id until status=running.", "box is %s", b.Status)
	case model.StatusSnapshotting:
		return nil, model.EH(model.ErrInvalid, "A snapshot is in progress; retry in a few seconds.", "box is snapshotting")
	default:
		return nil, model.EH(model.ErrNotFound, "This box is gone; create a new one with box_up.", "box is %s", b.Status)
	}
	if !m.Hub.Connected(b.HostID) {
		return nil, model.EH(model.ErrProviderFailed, "The box's host is offline; retry shortly or create a new box.", "host %s is offline", b.HostID)
	}
	return b, nil
}

func mapHostErr(err error) error {
	if err == nil {
		return nil
	}
	var pe *proto.Error
	if errors.As(err, &pe) {
		switch pe.Code {
		case model.ErrNotFound, model.ErrInvalid, model.ErrPolicyDenied, model.ErrTimeout, model.ErrBoxSleeping:
			return model.E(pe.Code, "%s", pe.Message)
		case "SETUP_FAILED":
			return model.EH(model.ErrInvalid, "Fix the setup commands in the template/box.yaml.", "%s", pe.Message)
		}
		return model.E(model.ErrProviderFailed, "%s", pe.Message)
	}
	if errors.Is(err, hostlink.ErrClosed) {
		return model.EH(model.ErrProviderFailed, "The host went offline; retry shortly.", "host connection lost")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return model.E(model.ErrTimeout, "host did not answer in time")
	}
	return err
}

// ---- exec / jobs ----

type ExecInput struct {
	Command   string `json:"command"`
	Cwd       string `json:"cwd"`
	TimeoutS  int    `json:"timeout_s"`
	Async     bool   `json:"async"`
	TailLines int    `json:"tail_lines"`
}

func (m *Manager) secretEnv(ctx context.Context, b *model.Box) (map[string]string, error) {
	env := map[string]string{}
	for _, name := range b.Secrets {
		s, err := m.Store.GetSecret(ctx, name)
		if err != nil {
			continue
		}
		if len(s.Agents) > 0 && !contains(s.Agents, b.AgentID) {
			continue
		}
		v, err := m.Crypt.Open(s.ValueEnc)
		if err != nil {
			return nil, err
		}
		env[name] = v
	}
	return env, nil
}

// Exec runs a command (sync) or starts a job (async). Returns *model.ExecResult or *model.Job.
func (m *Manager) Exec(ctx context.Context, p *Principal, id string, in ExecInput) (any, error) {
	if err := p.Require(model.ScopeBoxExec); err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.Command) == "" {
		return nil, model.E(model.ErrInvalid, "command is required")
	}
	b, err := m.usable(ctx, p, id)
	if err != nil {
		return nil, err
	}
	m.touch(ctx, b)
	if in.TimeoutS <= 0 {
		if in.Async {
			in.TimeoutS = 6 * 3600
		} else {
			in.TimeoutS = 300
		}
	}
	if !in.Async && in.TimeoutS > 3600 {
		in.TimeoutS = 3600
	}
	if in.Async {
		j := &model.Job{ID: secret.ID("job"), BoxID: b.ID, Command: in.Command, Status: "queued", CreatedAt: time.Now().UTC(), AgentID: b.AgentID}
		if err := m.Store.PutJob(ctx, j); err != nil {
			return nil, err
		}
		m.Emit(ctx, p.Actor(), b.ID, b.AgentID, "job.queued", "job "+j.ID+": "+trunc(in.Command, 160), nil)
		go m.runJob(j, b, in)
		return map[string]string{"job_id": j.ID}, nil
	}
	return m.execSync(ctx, p.Actor(), b, in)
}

func (m *Manager) execSync(ctx context.Context, actor string, b *model.Box, in ExecInput) (*model.ExecResult, error) {
	env, err := m.secretEnv(ctx, b)
	if err != nil {
		return nil, err
	}
	var out boxd.ExecOut
	cctx, cancel := context.WithTimeout(ctx, time.Duration(in.TimeoutS+60)*time.Second)
	defer cancel()
	err = m.Hub.Call(cctx, b.HostID, proto.MExec, proto.ExecParams{
		BoxID: b.ID, Command: in.Command, Cwd: in.Cwd, Env: env, TimeoutS: in.TimeoutS, TailLines: in.TailLines,
	}, &out)
	if err != nil {
		return nil, mapHostErr(err)
	}
	res := &model.ExecResult{
		ExitCode: out.ExitCode, DurationMS: out.DurationMS, StdoutTail: out.StdoutTail, StderrTail: out.StderrTail,
		Truncated: out.Truncated, FilesChanged: out.FilesChanged, TokensSaved: out.TokensSaved,
	}
	if res.FilesChanged == nil {
		res.FilesChanged = []string{}
	}
	if out.LogID != "" {
		lid := out.LogID
		res.LogID = &lid
		m.Store.PutLog(context.Background(), lid, b.ID, b.HostID)
	}
	m.touch(context.Background(), b)
	msg := fmt.Sprintf("exit=%d %dms: %s", out.ExitCode, out.DurationMS, trunc(in.Command, 160))
	if out.TimedOut {
		msg = "TIMEOUT " + msg
	}
	m.Emit(context.Background(), actor, b.ID, b.AgentID, "box.exec", msg, nil)
	return res, nil
}

func (m *Manager) runJob(j *model.Job, b *model.Box, in ExecInput) {
	ctx := context.Background()
	j.Status = "running"
	m.Store.PutJob(ctx, j)
	res, err := m.execSync(ctx, "job", b, in)
	now := time.Now().UTC()
	j.FinishedAt = &now
	if err != nil {
		j.Status = "failed"
		ae := model.AsAPIError(err)
		j.Result = &model.ExecResult{ExitCode: -1, StderrTail: ae.Code + ": " + ae.Message, FilesChanged: []string{}}
	} else {
		j.Result = res
		if res.ExitCode == 0 {
			j.Status = "done"
		} else {
			j.Status = "failed"
		}
	}
	m.Store.PutJob(ctx, j)
	m.Emit(ctx, "job", b.ID, b.AgentID, "job."+j.Status, "job "+j.ID+" "+j.Status, nil)
}

func (m *Manager) GetJob(ctx context.Context, p *Principal, id string) (*model.Job, error) {
	j, err := m.Store.GetJob(ctx, id)
	if err != nil {
		return nil, notFound("job", id)
	}
	if !p.Admin && j.AgentID != p.AgentID {
		return nil, notFound("job", id)
	}
	return j, nil
}

// WaitJob polls until the job finishes or the timeout passes.
func (m *Manager) WaitJob(ctx context.Context, p *Principal, id string, wait time.Duration) (*model.Job, error) {
	deadline := time.Now().Add(wait)
	for {
		j, err := m.GetJob(ctx, p, id)
		if err != nil {
			return nil, err
		}
		if j.Status == "done" || j.Status == "failed" || time.Now().After(deadline) {
			return j, nil
		}
		select {
		case <-ctx.Done():
			return j, nil
		case <-time.After(300 * time.Millisecond):
		}
	}
}

func (m *Manager) GetLog(ctx context.Context, p *Principal, id string) (string, error) {
	boxID, hostID, err := m.Store.GetLogHost(ctx, id)
	if err != nil {
		return "", notFound("log", id)
	}
	if _, err := m.GetBox(ctx, p, boxID); err != nil {
		return "", err
	}
	var out proto.LogReadResult
	if err := m.Hub.Call(ctx, hostID, proto.MLogRead, proto.LogReadParams{LogID: id}, &out); err != nil {
		return "", mapHostErr(err)
	}
	return out.Content, nil
}

// ---- files ----

func (m *Manager) ReadFile(ctx context.Context, p *Principal, id, path string, from, to int) (*proto.ReadResult, error) {
	if err := p.Require(model.ScopeBoxFile); err != nil {
		return nil, err
	}
	b, err := m.usable(ctx, p, id)
	if err != nil {
		return nil, err
	}
	m.touch(ctx, b)
	var out proto.ReadResult
	if err := m.Hub.Call(ctx, b.HostID, proto.MRead, proto.ReadParams{BoxID: b.ID, Path: path, From: from, To: to}, &out); err != nil {
		return nil, mapHostErr(err)
	}
	if out.IsDir && out.Entries == nil {
		out.Entries = []proto.Entry{}
	}
	return &out, nil
}

func (m *Manager) WriteFile(ctx context.Context, p *Principal, id, path, content string) error {
	if err := p.Require(model.ScopeBoxFile); err != nil {
		return err
	}
	if path == "" {
		return model.E(model.ErrInvalid, "path is required")
	}
	if len(content) > 20<<20 {
		return model.E(model.ErrInvalid, "content larger than 20MB")
	}
	b, err := m.usable(ctx, p, id)
	if err != nil {
		return err
	}
	m.touch(ctx, b)
	if err := m.Hub.Call(ctx, b.HostID, proto.MWrite, proto.WriteParams{BoxID: b.ID, Path: path, Content: content}, nil); err != nil {
		return mapHostErr(err)
	}
	m.Emit(ctx, p.Actor(), b.ID, b.AgentID, "box.file_write", fmt.Sprintf("wrote %s (%d bytes)", path, len(content)), nil)
	return nil
}

func (m *Manager) Grep(ctx context.Context, p *Principal, id, pattern, path string) (*proto.GrepResult, error) {
	if err := p.Require(model.ScopeBoxFile); err != nil {
		return nil, err
	}
	if pattern == "" {
		return nil, model.E(model.ErrInvalid, "pattern is required")
	}
	b, err := m.usable(ctx, p, id)
	if err != nil {
		return nil, err
	}
	m.touch(ctx, b)
	var out proto.GrepResult
	if err := m.Hub.Call(ctx, b.HostID, proto.MGrep, proto.GrepParams{BoxID: b.ID, Pattern: pattern, Path: path}, &out); err != nil {
		return nil, mapHostErr(err)
	}
	if out.Matches == nil {
		out.Matches = []proto.GrepMatch{}
	}
	return &out, nil
}

func (m *Manager) Observe(ctx context.Context, p *Principal, id string) (*proto.ObserveResult, error) {
	if err := p.Require(model.ScopeBoxFile); err != nil {
		return nil, err
	}
	b, err := m.usable(ctx, p, id)
	if err != nil {
		return nil, err
	}
	var out proto.ObserveResult
	if err := m.Hub.Call(ctx, b.HostID, proto.MObserve, proto.BoxRef{BoxID: b.ID}, &out); err != nil {
		return nil, mapHostErr(err)
	}
	return &out, nil
}

// ---- lifecycle verbs ----

func (m *Manager) Sleep(ctx context.Context, p *Principal, id string) (*model.Box, error) {
	b, err := m.GetBox(ctx, p, id)
	if err != nil {
		return nil, err
	}
	return m.sleepBox(ctx, b, p.Actor(), "requested")
}

func (m *Manager) sleepBox(ctx context.Context, b *model.Box, actor, why string) (*model.Box, error) {
	lock := m.boxLock(b.ID)
	lock.Lock()
	defer lock.Unlock()
	b, err := m.Store.GetBox(ctx, b.ID)
	if err != nil {
		return nil, err
	}
	if b.Status == model.StatusSleeping {
		return m.decorateBox(ctx, b), nil
	}
	if b.Status != model.StatusRunning {
		return nil, model.E(model.ErrInvalid, "cannot sleep a box in status %s", b.Status)
	}
	if err := m.Hub.Call(ctx, b.HostID, proto.MStop, proto.BoxRef{BoxID: b.ID}, nil); err != nil {
		return nil, mapHostErr(err)
	}
	m.setStatus(ctx, b, model.StatusSleeping, actor, why)
	return m.decorateBox(ctx, b), nil
}

func (m *Manager) Wake(ctx context.Context, p *Principal, id string) (*model.Box, error) {
	b, err := m.GetBox(ctx, p, id)
	if err != nil {
		return nil, err
	}
	lock := m.boxLock(b.ID)
	lock.Lock()
	defer lock.Unlock()
	b, _ = m.Store.GetBox(ctx, b.ID)
	if b.Status == model.StatusRunning {
		return m.decorateBox(ctx, b), nil
	}
	if b.Status != model.StatusSleeping {
		return nil, model.E(model.ErrInvalid, "cannot wake a box in status %s", b.Status)
	}
	agent, err := m.Store.GetAgent(ctx, b.AgentID)
	if err == nil {
		if err := m.checkBudget(ctx, agent); err != nil {
			return nil, err
		}
	}
	if b.BudgetUSD > 0 && b.SpentUSD >= b.BudgetUSD && !m.hasOverride(ctx, "box:"+b.ID) {
		return nil, model.EH(model.ErrBudgetExceeded, "This box used its budget_usd; create a new box or ask the operator.", "box budget $%.2f used", b.BudgetUSD)
	}
	if !m.Hub.Connected(b.HostID) {
		return nil, model.EH(model.ErrProviderFailed, "The box's host is offline; retry shortly.", "host %s offline", b.HostID)
	}
	if err := m.Hub.Call(ctx, b.HostID, proto.MStart, proto.BoxRef{BoxID: b.ID}, nil); err != nil {
		return nil, mapHostErr(err)
	}
	b.LastActiveAt = time.Now().UTC()
	m.setStatus(ctx, b, model.StatusRunning, p.Actor(), "woken")
	return m.decorateBox(ctx, b), nil
}

func (m *Manager) Snapshot(ctx context.Context, p *Principal, id string) (*model.Snapshot, error) {
	if err := p.Require(model.ScopeSnapshot); err != nil {
		return nil, err
	}
	b, err := m.GetBox(ctx, p, id)
	if err != nil {
		return nil, err
	}
	return m.snapshotBox(ctx, b, p.Actor())
}

func (m *Manager) snapshotBox(ctx context.Context, b *model.Box, actor string) (*model.Snapshot, error) {
	lock := m.boxLock(b.ID)
	lock.Lock()
	defer lock.Unlock()
	b, err := m.Store.GetBox(ctx, b.ID)
	if err != nil {
		return nil, err
	}
	prev := b.Status
	if prev != model.StatusRunning && prev != model.StatusSleeping {
		return nil, model.E(model.ErrInvalid, "cannot snapshot a box in status %s", prev)
	}
	if !m.Hub.Connected(b.HostID) {
		return nil, model.EH(model.ErrProviderFailed, "The host is offline; retry later.", "host %s offline", b.HostID)
	}
	m.setStatus(ctx, b, model.StatusSnapshotting, actor, "")
	snapID := secret.ID("snap")
	var res proto.SnapshotResult
	cctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	err = m.Hub.Call(cctx, b.HostID, proto.MSnapshot, proto.SnapshotParams{BoxID: b.ID, SnapshotID: snapID}, &res)
	cancel()
	m.setStatus(ctx, b, prev, actor, "snapshot finished")
	if err != nil {
		return nil, mapHostErr(err)
	}
	sn := &model.Snapshot{ID: snapID, BoxID: b.ID, SizeMB: float64(int(res.SizeMB*100)) / 100, CreatedAt: time.Now().UTC(), Location: res.Location, HostID: b.HostID}
	if err := m.Store.PutSnapshot(ctx, sn); err != nil {
		return nil, err
	}
	m.Emit(ctx, actor, b.ID, b.AgentID, "box.snapshot", fmt.Sprintf("snapshot %s (%.2f MB) at %s", sn.ID, sn.SizeMB, sn.Location), nil)
	// Retention: keep newest N records.
	keep := m.Settings(ctx).Reaper.KeepSnapshots
	if snaps, err := m.Store.ListSnapshots(ctx, b.ID); err == nil && len(snaps) > keep {
		for _, old := range snaps[keep:] {
			m.Store.DeleteSnapshot(ctx, old.ID)
		}
	}
	return sn, nil
}

func (m *Manager) ListSnapshots(ctx context.Context, p *Principal, id string) ([]*model.Snapshot, error) {
	b, err := m.GetBox(ctx, p, id)
	if err != nil {
		return nil, err
	}
	return m.Store.ListSnapshots(ctx, b.ID)
}

// Terminate deletes the box everywhere.
func (m *Manager) Terminate(ctx context.Context, p *Principal, id string) (*model.Box, error) {
	b, err := m.GetBox(ctx, p, id)
	if err != nil {
		return nil, err
	}
	return m.terminateBox(ctx, b, p.Actor(), "requested")
}

func (m *Manager) terminateBox(ctx context.Context, b *model.Box, actor, why string) (*model.Box, error) {
	if b.Status == model.StatusTerminated {
		return m.decorateBox(ctx, b), nil
	}
	lock := m.boxLock(b.ID)
	if !lock.TryLock() {
		// provisioning holds the lock; mark terminated and let provision see it
		m.setStatus(ctx, b, model.StatusTerminated, actor, why+" (during provisioning)")
		go func() {
			lock.Lock()
			defer lock.Unlock()
			if b.HostID != "" {
				m.Hub.Call(context.Background(), b.HostID, proto.MDelete, proto.BoxRef{BoxID: b.ID}, nil)
			}
		}()
		return m.decorateBox(ctx, b), nil
	}
	defer lock.Unlock()
	b, err := m.Store.GetBox(ctx, b.ID)
	if err != nil {
		return nil, err
	}
	note := why
	if b.HostID != "" {
		if m.Hub.Connected(b.HostID) {
			cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
			err := m.Hub.Call(cctx, b.HostID, proto.MDelete, proto.BoxRef{BoxID: b.ID}, nil)
			cancel()
			if err != nil {
				note += fmt.Sprintf(" (host delete error: %v; will clean up on reconnect)", err)
			}
		} else {
			note += " (host offline; cleaned up on reconnect)"
		}
	}
	m.setStatus(ctx, b, model.StatusTerminated, actor, note)
	return m.decorateBox(ctx, b), nil
}

func (m *Manager) BoxEvents(ctx context.Context, p *Principal, id string) ([]*model.Event, error) {
	b, err := m.GetBox(ctx, p, id)
	if err != nil {
		return nil, err
	}
	return m.Store.ListEvents(ctx, store.EventFilter{BoxID: b.ID, Limit: 500})
}

func (m *Manager) Takeover(ctx context.Context, p *Principal, id string, on bool) (*model.Box, error) {
	if err := p.Require(model.ScopeAdmin); err != nil {
		return nil, err
	}
	b, err := m.GetBox(ctx, p, id)
	if err != nil {
		return nil, err
	}
	b.TakenOver = on
	m.Store.PutBox(ctx, b)
	typ, msg := "box.takeover", "human took over the box; agent calls are paused"
	if !on {
		typ, msg = "box.release", "box released back to the agent"
	}
	m.Emit(ctx, p.Actor(), b.ID, b.AgentID, typ, msg, nil)
	m.publishBox(b)
	return b, nil
}

// OpenTerminal starts a PTY on the box's host; returns host id and stream id.
func (m *Manager) OpenTerminal(ctx context.Context, p *Principal, id string, cols, rows int, sink hostlink.StreamSink) (hostID, stream string, err error) {
	if err := p.Require(model.ScopeBoxExec); err != nil {
		return "", "", err
	}
	b, err := m.usable(ctx, p, id)
	if err != nil {
		return "", "", err
	}
	stream = secret.ID("pty")
	m.Hub.AddStream(stream, sink)
	if err := m.Hub.Call(ctx, b.HostID, proto.MPtyOpen, proto.PtyOpenParams{BoxID: b.ID, Stream: stream, Cols: cols, Rows: rows}, nil); err != nil {
		m.Hub.RemoveStream(stream)
		return "", "", mapHostErr(err)
	}
	m.touch(ctx, b)
	m.Emit(ctx, p.Actor(), b.ID, b.AgentID, "box.terminal", "terminal opened", nil)
	return b.HostID, stream, nil
}

func (m *Manager) ResizeTerminal(hostID, stream string, cols, rows int) {
	m.Hub.Call(context.Background(), hostID, proto.MPtyResize, proto.PtyResizeParams{Stream: stream, Cols: cols, Rows: rows}, nil)
}

func (m *Manager) CloseTerminal(hostID, stream string) {
	m.Hub.RemoveStream(stream)
	m.Hub.Call(context.Background(), hostID, proto.MPtyClose, proto.PtyResizeParams{Stream: stream}, nil)
}

// EmergencyStop sleeps every running box.
func (m *Manager) EmergencyStop(ctx context.Context, p *Principal, confirm string) (int, error) {
	if err := p.Require(model.ScopeAdmin); err != nil {
		return 0, err
	}
	if confirm != "DUNG" {
		return 0, model.EH(model.ErrInvalid, `Send {"confirm":"DUNG"} to confirm.`, "confirmation text mismatch")
	}
	boxes, _ := m.Store.ListBoxes(ctx, store.BoxFilter{Status: model.StatusRunning})
	n := 0
	for _, b := range boxes {
		if _, err := m.sleepBox(ctx, b, p.Actor(), "emergency stop"); err == nil {
			n++
		}
	}
	m.Emit(ctx, p.Actor(), "", "", "emergency_stop", fmt.Sprintf("emergency stop: %d boxes put to sleep", n), nil)
	return n, nil
}

var _ = provider.Quote
