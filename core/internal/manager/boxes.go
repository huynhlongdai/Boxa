package manager

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"boxmes/core/internal/boxd"
	"boxmes/core/internal/boxyaml"
	"boxmes/core/internal/hostlink"
	"boxmes/core/internal/i18n"
	"boxmes/core/internal/model"
	"boxmes/core/internal/proto"
	"boxmes/core/internal/provider"
	"boxmes/core/internal/secret"
	"boxmes/core/internal/store"
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
		return nil, false, model.EH(model.ErrPolicyDenied, "Boxmes is in maintenance; retry later.", "%s", msg)
	}
	origYAML := req.YAML
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
	needs, err := m.applyNeeds(ctx, &req, agent)
	if err != nil {
		return nil, false, err
	}
	baseTemplate, tpl, err := m.normalizeRequest(ctx, p, &req, agent)
	if err != nil {
		return nil, false, err
	}
	tpl.Uses++
	m.Store.PutTemplate(ctx, tpl)
	if err := m.checkBudget(ctx, agent); err != nil {
		return nil, false, err
	}
	eg, err := m.effectiveEgress(ctx, agent, req.Egress)
	if err != nil {
		return nil, false, err
	}
	idle, ttl := req.IdleMinutes, req.TTLSeconds
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
		Locale: m.agentLocale(ctx, agent), Egress: eg,
		Requirements: needs, Name: req.Name, WakeOnRequest: req.WakeOnRequest,
	}
	if req.App != nil { // v1.7
		sp := *req.App
		sp.Normalize()
		if msg := sp.Validate(); msg != "" {
			return nil, false, model.EH(model.ErrInvalid, "Example: \"app\":{\"start\":\"npm start\",\"port\":8000}.", "invalid app spec: %s", msg)
		}
		b.App = &sp
		if sp.Port > 0 {
			found := false
			for _, x := range b.Ports {
				found = found || x == sp.Port
			}
			if !found {
				b.Ports = append(b.Ports, sp.Port)
			}
		}
	}
	if needs != nil {
		b.Profile = needs.Profile
	}
	// v1.9 readable gateway name + port labels
	if b.App != nil && b.App.Label != "" && b.App.Port > 0 {
		b.PortLabels = map[int]string{b.App.Port: b.App.Label}
	}
	if b.Subdomain == "" && req.Subdomain == "" {
		// the requested name / slug before it was made unique (a taken one
		// gets a word suffix below, never the slug's random digits)
		for _, s := range []string{req.Name, req.Slug, baseTemplate} {
			if n := model.Slugify(s, model.MaxBoxNameLen); n != "" && hasLetter(n) {
				b.Subdomain = n
				break
			}
		}
	}
	if req.Subdomain != "" {
		if err := m.claimExplicitName(ctx, b, req.Subdomain); err != nil {
			return nil, false, err
		}
	}
	m.ensureNames(ctx, b)
	b.Estimate = m.previewEstimate(ctx, b, agent, baseTemplate)
	if err := m.Store.PutBox(ctx, b); err != nil {
		return nil, false, err
	}
	m.saveBoxYAML(ctx, b, origYAML)
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

// normalizeRequest resolves template defaults, applies spec/lifecycle/TTL
// defaults and checks quotas, secrets and ports. No side effects (used by
// CreateBox and Quote).
func (m *Manager) normalizeRequest(ctx context.Context, p *Principal, reqp *boxyaml.Request, agent *model.Agent) (string, *model.Template, error) {
	req := *reqp
	settings := m.Settings(ctx)
	// Template resolution: built-in or custom (custom YAML provides defaults).
	if req.Template == "" {
		req.Template = "base"
	}
	baseTemplate := req.Template
	tpl, err := m.Store.GetTemplate(ctx, req.Template)
	if err != nil {
		return "", nil, model.EH(model.ErrInvalid, "Use one of GET /templates.", "template %q not found", req.Template)
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
		return "", nil, model.E(model.ErrInvalid, "lifecycle must be task, session or service")
	}
	q := m.Quotas(ctx)
	if req.Spec.CPU > q.MaxCPUPerBox || req.Spec.RAMMB > q.MaxRAMMBPerBox || req.Spec.DiskGB > q.MaxDiskGBPerBox {
		return "", nil, model.EH(model.ErrPolicyDenied, "Request a smaller spec or ask the operator to raise quotas.",
			"spec exceeds quota (max cpu %.0f, ram %d MB, disk %d GB)", q.MaxCPUPerBox, q.MaxRAMMBPerBox, q.MaxDiskGBPerBox)
	}
	existing, _ := m.Store.ListBoxes(ctx, store.BoxFilter{AgentID: agent.ID})
	if len(existing) >= q.MaxBoxesPerAgent {
		return "", nil, model.EH(model.ErrPolicyDenied, "Terminate unused boxes (box down) first.", "agent already has %d boxes (quota %d)", len(existing), q.MaxBoxesPerAgent)
	}
	for _, port := range req.Ports {
		if port < 1 || port > 65535 {
			return "", nil, model.E(model.ErrInvalid, "invalid port %d", port)
		}
	}
	if len(req.Secrets) > 0 {
		if err := p.Require(model.ScopeSecretUse); err != nil {
			return "", nil, err
		}
		for _, name := range req.Secrets {
			s, err := m.Store.GetSecret(ctx, name)
			if err != nil {
				return "", nil, model.EH(model.ErrInvalid, "Create it first with POST /secrets.", "secret %q not found", name)
			}
			if len(s.Agents) > 0 && !contains(s.Agents, agent.ID) {
				return "", nil, model.E(model.ErrPolicyDenied, "secret %q is not shared with this agent", name)
			}
		}
	}
	if req.IdleMinutes <= 0 {
		req.IdleMinutes = settings.Reaper.IdleMinutes
	}
	if req.TTLSeconds <= 0 && req.Lifecycle == model.LifecycleTask {
		req.TTLSeconds = settings.Reaper.DefaultTTLSeconds
	}
	*reqp = req
	return baseTemplate, tpl, nil
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
	burstFallbacks := 0
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
		pl, warmE, err := m.place(ctx, b, agent, exclude, baseTemplate)
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
				// fall through to the next provider in the chain
				exclude["provider:"+pl.Offer.Provider] = true
				if burstFallbacks < len(m.chain()) {
					burstFallbacks++
					attempt-- // a provider failure does not use up a host retry
				}
				m.Emit(ctx, "scheduler", b.ID, b.AgentID, "box.burst_failed", fmt.Sprintf("burst host at %s failed: %v; trying the next provider in the chain", pl.Offer.Provider, err), nil)
				continue
			}
		}
		b.HostID, b.Provider, b.Region = host.ID, host.Provider, host.Region
		b.CostPerHour, b.Billing, b.PlacementReason = pl.CostPerHour, pl.Billing, pl.Reason
		if pl.Isolation != "" {
			b.Isolation = pl.Isolation
		}
		hours := expectedHours(b)
		if b.Requirements != nil {
			hours = hoursFor(b, *b.Requirements)
		}
		b.Estimate = &model.BoxEstimate{Kind: pl.Kind, CostPerHour: pl.CostPerHour, ExpectedHours: hours, ExpectedTotalUSD: model.Round4(pl.CostPerHour * hours),
			ETASeconds: round1(pl.ETA), Score: pl.Score, WarmHit: warmE != nil}
		b.URL = m.boxURL(ctx, b, host)
		m.setStatus(ctx, b, model.StatusProvisioning, "scheduler", pl.Reason)
		createStart := time.Now()
		if warmE != nil {
			err = m.claimWarm(ctx, b, agent, warmE)
			if err != nil {
				m.Emit(ctx, "system", b.ID, b.AgentID, "box.warm_claim_failed", fmt.Sprintf("warm entry %s on %s: %v; falling back to a cold create", warmE.ID, host.ID, err), nil)
				attempt-- // a failed claim does not use up a retry
				continue
			}
		} else {
			cctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
			err = m.Hub.Call(cctx, host.ID, proto.MCreate, proto.CreateParams{
				BoxID: b.ID, Template: baseTemplate, Spec: b.Spec, Setup: b.Setup, AgentID: b.AgentID,
				AgentsMD: m.agentsMD(ctx, b, agent), Isolation: b.Isolation,
			}, nil)
			cancel()
			if err == nil && len(b.Setup) == 0 {
				m.recordColdCreate(host.ID, baseTemplate, time.Since(createStart))
			}
			if err == nil && m.warmConfig(ctx).Enabled {
				m.warmRecord(false, 0)
			}
		}
		if err != nil {
			lastErr = err
			exclude[host.ID] = true
			m.Emit(ctx, "system", b.ID, b.AgentID, "box.provision_failed", fmt.Sprintf("attempt %d on %s failed: %v", attempt, host.ID, err), nil)
			dctx, dc := context.WithTimeout(ctx, time.Minute)
			_ = m.Hub.Call(dctx, host.ID, proto.MDelete, proto.BoxRef{BoxID: b.ID}, nil)
			dc()
			continue
		}
		if err := m.applyEgress(ctx, b, false); err != nil {
			// fail closed: never hand out a box whose allowlist could not be applied
			lastErr = err
			exclude[host.ID] = true
			m.Emit(ctx, "system", b.ID, b.AgentID, "box.provision_failed", fmt.Sprintf("attempt %d on %s: egress policy failed: %v", attempt, host.ID, err), nil)
			dctx, dc := context.WithTimeout(ctx, time.Minute)
			_ = m.Hub.Call(dctx, host.ID, proto.MDelete, proto.BoxRef{BoxID: b.ID}, nil)
			dc()
			continue
		}
		b.LastActiveAt = time.Now().UTC()
		b.Error = ""
		if b.Estimate != nil {
			b.Estimate.ActualSeconds = round3(time.Since(b.CreatedAt).Seconds())
		}
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
	if h != nil && h.Billing == "sandbox" {
		// sandbox boxes are reached through the provider's preview host
		// (E2B https://<port>-<id>.e2b.app, Novita …sandbox.novita.ai);
		// a private preview host (v1.8) only through the Boxmes gateway
		if h.PrivateTraffic {
			return m.GatewayURL(b, b.Ports[0])
		}
		if u := m.sandboxPreview(ctx, h, b.Ports[0]); u != "" {
			return u + "/"
		}
		if e := b.ExposureFor(b.Ports[0]); e != nil { // ingress / tunnel fallback
			if e.Kind == "ingress" && m.GatewayDomain(ctx) != "" {
				return m.GatewayURL(b, b.Ports[0])
			}
			return e.URL
		}
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
		u += "?boxmes_token=" + m.URLToken(b.ID)
	}
	return u
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
		if status == model.StatusFailed {
			loc := m.Settings(ctx).Locale
			m.notify("box_failed", i18n.T(loc, "notify.box_failed_title", b.Slug), i18n.T(loc, "notify.box_failed_body", b.ID, b.AgentName, msg))
		}
	}
	m.publishBox(m.decorateBox(ctx, b))
	if prev != status && status == model.StatusRunning && b.App != nil {
		go m.appEnsure(b.ID) // v1.7: (re)start the supervised app (create, wake, clone, fork)
	}
	if prev != status && status != model.StatusRunning {
		m.app.ready.Delete(b.ID)
	}
}

func (m *Manager) decorateBox(ctx context.Context, b *model.Box) *model.Box {
	if b.Ports == nil {
		b.Ports = []int{}
	}
	if b.Secrets == nil {
		b.Secrets = []string{}
	}
	b.SpentUSD = model.Round4(b.SpentUSD)
	if b.WakeOnRequest == nil { // v1.7: report the default explicitly
		on := true
		b.WakeOnRequest = &on
	}
	if pp := PrimaryPort(b); pp > 0 && b.Subdomain != "" && b.URL != "" && b.URL == m.GatewayAliasURL(b, pp) {
		b.URL = m.GatewayURL(b, pp) // v1.9: the readable URL first
	}
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
	// Session (v1.3 exec fast path): cwd and exported variables persist
	// across calls, like one long-lived shell (state lives in the box).
	Session bool   `json:"session,omitempty"`
	jobID   string // set for async jobs (cancellable)
}

// sessionWrap makes a command behave like the next line of a persistent
// shell: restore the previous cwd + exports, run, save them again. The exit
// code of the user command is preserved.
func sessionWrap(cmd string) string {
	const st = "${BOX_WORK:-/work}/.box"
	return `mkdir -p "` + st + `" 2>/dev/null; if [ -f "` + st + `/env" ]; then . "` + st + `/env" 2>/dev/null; fi; ` +
		`if [ -f "` + st + `/cwd" ]; then cd "$(cat "` + st + `/cwd")" 2>/dev/null; fi; ` +
		"{ " + cmd + "\n}; __box_rc=$?; pwd > \"" + st + "/cwd\"; export -p | grep -v -E ' (PWD|OLDPWD|SHLVL|_|__box_rc)=' > \"" + st + "/env\"; exit $__box_rc"
}

func (m *Manager) secretEnv(ctx context.Context, b *model.Box) (map[string]string, error) {
	env := map[string]string{}
	for _, name := range b.Secrets {
		s, err := m.Store.GetSecret(ctx, name)
		if err != nil {
			continue
		}
		if s.System || (len(s.Agents) > 0 && !contains(s.Agents, b.AgentID)) {
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
		in.jobID = j.ID
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
	command := in.Command
	if in.Session {
		command = sessionWrap(command)
	}
	err = m.Hub.Call(cctx, b.HostID, proto.MExec, proto.ExecParams{
		BoxID: b.ID, Command: command, Cwd: in.Cwd, Env: env, TimeoutS: in.TimeoutS, TailLines: in.TailLines, JobID: in.jobID,
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
	if in.Session {
		kept := res.FilesChanged[:0]
		for _, f := range res.FilesChanged {
			if !strings.Contains(f, "/.box/") {
				kept = append(kept, f)
			}
		}
		res.FilesChanged = kept
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
	data := map[string]any{"exit_code": out.ExitCode, "duration_ms": out.DurationMS, "tokens_saved": out.TokensSaved, "truncated": out.Truncated, "log_id": nil}
	if res.LogID != nil {
		data["log_id"] = *res.LogID
	}
	if out.TimedOut {
		data["timed_out"] = true
	}
	m.EmitData(context.Background(), actor, b.ID, b.AgentID, "box.exec", msg, nil, data)
	return res, nil
}

func (m *Manager) runJob(j *model.Job, b *model.Box, in ExecInput) {
	ctx := context.Background()
	j.Status = "running"
	m.Store.PutJob(ctx, j)
	jctx, cancel := context.WithCancel(ctx)
	m.jobCancel.Store(j.ID, cancel)
	res, err := m.execSync(jctx, "job", b, in)
	m.jobCancel.Delete(j.ID)
	cancel()
	now := time.Now().UTC()
	j.FinishedAt = &now
	if cur, _ := m.Store.GetJob(ctx, j.ID); cur != nil && cur.Status == "canceled" {
		j.Status = "canceled"
		if err == nil && res != nil {
			j.Result = res
		}
		m.Store.PutJob(ctx, j)
		return
	}
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

// CancelJob stops a queued/running async job: the command is killed on the
// host and the job ends with status "canceled".
func (m *Manager) CancelJob(ctx context.Context, p *Principal, id string) (*model.Job, error) {
	j, err := m.GetJob(ctx, p, id)
	if err != nil {
		return nil, err
	}
	if j.Status != "queued" && j.Status != "running" {
		return j, nil
	}
	now := time.Now().UTC()
	j.Status, j.FinishedAt = "canceled", &now
	if err := m.Store.PutJob(ctx, j); err != nil {
		return nil, err
	}
	if b, err := m.Store.GetBox(ctx, j.BoxID); err == nil && m.Hub.Connected(b.HostID) {
		cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		m.Hub.Call(cctx, b.HostID, proto.MExecCancel, proto.ExecCancelParams{JobID: j.ID}, nil)
		cancel()
	}
	if c, ok := m.jobCancel.Load(j.ID); ok {
		c.(context.CancelFunc)()
	}
	m.Emit(ctx, p.Actor(), j.BoxID, j.AgentID, "job.canceled", "job "+j.ID+" canceled", nil)
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
		if j.Status == "done" || j.Status == "failed" || j.Status == "canceled" || time.Now().After(deadline) {
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

func (m *Manager) Observe(ctx context.Context, p *Principal, id string) (*ObserveView, error) {
	if err := p.Require(model.ScopeBoxFile); err != nil {
		return nil, err
	}
	b, err := m.usable(ctx, p, id)
	if err != nil {
		return nil, err
	}
	out, err := m.observeRaw(ctx, b)
	if err != nil {
		return nil, err
	}
	if out.FilesChanged == nil {
		out.FilesChanged = []string{}
	}
	if out.Processes == nil {
		out.Processes = []proto.Proc{}
	}
	if out.Ports == nil {
		out.Ports = []int{}
	}
	m.obsMu.Lock()
	prev := m.lastObs[b.ID]
	m.lastObs[b.ID] = out
	m.obsMu.Unlock()
	v := &ObserveView{ObserveResult: *out, Diff: diffObserve(prev, out)}
	v.Summary = summarizeObserve(v)
	return v, nil
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
	if b.Status == model.StatusRunning {
		m.closeAccess(b.ID, "[boxmes] box is going to sleep") // v1.10: terminals, ssh, tcp streams
	}
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
	if b.Egress != nil {
		// re-resolve allowlisted domains after a wake (addresses may have changed)
		if err := m.applyEgress(ctx, b, false); err != nil {
			m.Hub.Call(ctx, b.HostID, proto.MStop, proto.BoxRef{BoxID: b.ID}, nil)
			return nil, err
		}
	}
	b.LastActiveAt = time.Now().UTC()
	m.setStatus(ctx, b, model.StatusRunning, p.Actor(), "woken")
	if len(b.Exposures) > 0 { // public-URL fallback: restart agent / re-check tunnel
		go m.reexpose(b.ID)
	}
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
	params := proto.SnapshotParams{BoxID: b.ID, SnapshotID: snapID}
	ostore := m.objStore(ctx)
	var key string
	if ostore != nil {
		key = snapshotKey(ostore, b.ID, snapID)
		if u, err := ostore.Presign("PUT", key, 2*time.Hour); err == nil {
			params.UploadURL = u
		}
	}
	var res proto.SnapshotResult
	cctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	err = m.Hub.Call(cctx, b.HostID, proto.MSnapshot, params, &res)
	cancel()
	m.setStatus(ctx, b, prev, actor, "snapshot finished")
	if err != nil {
		return nil, mapHostErr(err)
	}
	sn := &model.Snapshot{ID: snapID, BoxID: b.ID, SizeMB: float64(int(res.SizeMB*1000)) / 1000, CreatedAt: time.Now().UTC(), Location: res.Location, HostID: b.HostID, Storage: "local"}
	if res.Uploaded && ostore != nil {
		sn.Storage, sn.RemoteURL = "s3", ostore.URI(key)
		if res.UploadedBytes > 0 && sn.SizeMB == 0 {
			sn.SizeMB = float64(int(float64(res.UploadedBytes)/(1<<20)*1000)) / 1000
		}
	} else if params.UploadURL != "" {
		sn.UploadError = res.UploadError
		if sn.UploadError == "" {
			sn.UploadError = "host did not upload the snapshot"
		}
	}
	if err := m.Store.PutSnapshot(ctx, sn); err != nil {
		return nil, err
	}
	where := sn.Location
	if sn.RemoteURL != "" {
		where += " + " + sn.RemoteURL
	}
	m.EmitData(ctx, actor, b.ID, b.AgentID, "box.snapshot", fmt.Sprintf("snapshot %s (%.2f MB) at %s", sn.ID, sn.SizeMB, where), nil, map[string]any{"snapshot_id": sn.ID, "storage": sn.Storage})
	if sn.UploadError != "" {
		m.Emit(ctx, "system", b.ID, b.AgentID, "snapshot.upload_failed", fmt.Sprintf("snapshot %s kept locally; upload failed: %s", sn.ID, sn.UploadError), nil)
	}
	m.applySnapshotRetention(ctx, b.ID)
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
	m.closeAccess(b.ID, "[boxmes] box was deleted")
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
	m.dropExposures(b.ID)
	if b.HostID != "" { // v1.4: a sandbox host exists only for its one box
		if h, err := m.Store.GetHost(ctx, b.HostID); err == nil && h.Billing == "sandbox" && h.Status != "deleted" {
			go m.destroyHost(context.Background(), h, actor, "its box ended (sandbox billed per second)")
		}
	}
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
	loc := m.Settings(ctx).Locale
	m.notify("emergency_stop", i18n.T(loc, "notify.emergency_title"), i18n.T(loc, "notify.emergency_body", n, p.Actor()))
	return n, nil
}

var _ = provider.Quote
