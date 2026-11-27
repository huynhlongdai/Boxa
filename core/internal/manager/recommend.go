package manager

// v1.3 need-based box selection: profile + requirements → ranked options
// (warm pool / pool container / Incus VM / new provider server / T2 sandbox)
// with price, ETA and reasons; the same engine places boxes created with a
// profile or requirements.

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"boxmes/core/internal/boxyaml"
	"boxmes/core/internal/egress"
	"boxmes/core/internal/model"
	"boxmes/core/internal/provider"
	"boxmes/core/internal/sandbox"
	"boxmes/core/internal/selector"
)

// RecommendInput is POST /recommend.
type RecommendInput struct {
	Requirements model.Requirements `json:"requirements"`
	Profile      string             `json:"profile,omitempty"`
	Template     string             `json:"template,omitempty"`
	Lifecycle    string             `json:"lifecycle,omitempty"`
	AgentID      string             `json:"agent_id,omitempty"`
	Kind         string             `json:"kind,omitempty"` // agent kind override (default: the agent's kind)
	Limit        int                `json:"limit,omitempty"`
}

// RecommendResult is the ranked answer.
type RecommendResult struct {
	Requirements  model.Requirements   `json:"requirements"` // effective (profile defaults merged)
	Profile       selector.Profile     `json:"profile"`
	Template      string               `json:"template"`
	Lifecycle     string               `json:"lifecycle"`
	ExpectedHours float64              `json:"expected_hours"`
	Options       []selector.Scored    `json:"options"`
	Rejected      []selector.Rejection `json:"rejected"`
	Pick          *selector.Scored     `json:"pick"`
	AutoPick      string               `json:"auto_pick"` // what box_up would do now: the pick, or why it falls back
	Explain       string               `json:"explain"`
	BoxUp         map[string]any       `json:"box_up"` // ready-to-send box_up arguments
}

func (m *Manager) clientLocation(ctx context.Context, req string) string {
	if req != "" {
		return req
	}
	if s := m.Settings(ctx).Location; s != "" {
		return s
	}
	return m.Cfg.ClientLocation
}

// resolveNeeds merges profile defaults, the agent kind and explicit fields.
func (m *Manager) resolveNeeds(ctx context.Context, rq model.Requirements, profile, kind string, agent *model.Agent) (model.Requirements, selector.Profile) {
	if profile != "" && rq.Profile == "" {
		rq.Profile = profile
	}
	if kind == "" && agent != nil && agent.Kind != "" && rq.Profile == "" {
		kind = agent.Kind
	}
	r, p := selector.Resolve(rq, kind)
	r.Location = m.clientLocation(ctx, r.Location)
	return r, p
}

// Recommend ranks placement options for a workload (no side effects).
func (m *Manager) Recommend(ctx context.Context, p *Principal, in RecommendInput) (*RecommendResult, error) {
	if err := p.Require(model.ScopeBoxCreate); err != nil {
		return nil, err
	}
	agent, err := m.agentFor(ctx, p, in.AgentID)
	if err != nil {
		return nil, err
	}
	if in.Profile != "" {
		if _, ok := selector.GetProfile(in.Profile); !ok {
			return nil, model.EH(model.ErrInvalid, "Use one of GET /profiles.", "unknown profile %q", in.Profile)
		}
	}
	req, prof := m.resolveNeeds(ctx, in.Requirements, in.Profile, in.Kind, agent)
	tpl := in.Template
	if tpl == "" {
		tpl = prof.Template
	}
	if _, err := m.Store.GetTemplate(ctx, tpl); err != nil {
		return nil, model.EH(model.ErrInvalid, "Use one of GET /templates.", "template %q not found", tpl)
	}
	lc := in.Lifecycle
	if lc == "" {
		lc = prof.Lifecycle
	}
	b := m.probeBox(req, prof, lc)
	b.Template = tpl
	opts, extraRej := m.buildOptions(ctx, b, agent, req, m.baseTemplateOf(ctx, tpl), nil)
	ranked, rej := selector.Rank(req, prof, opts)
	rej = append(rej, extraRej...)
	res := &RecommendResult{Requirements: req, Profile: prof, Template: tpl, Lifecycle: lc, ExpectedHours: expectedHours(b), Options: ranked, Rejected: rej}
	if res.Rejected == nil {
		res.Rejected = []selector.Rejection{}
	}
	lim := in.Limit
	if lim <= 0 || lim > 20 {
		lim = 8
	}
	if len(res.Options) > lim {
		res.Options = res.Options[:lim]
	}
	if len(ranked) > 0 {
		pk := ranked[0]
		res.Pick = &pk
		res.Explain = strings.Join(pk.Reasons, "; ")
		if auto := m.firstAutoPlaceable(ranked); auto != nil {
			if auto.Rank == 1 {
				res.AutoPick = fmt.Sprintf("box_up places it on option #1 (%s)", auto.Kind)
			} else {
				res.AutoPick = fmt.Sprintf("option #1 needs approval; box_up would use option #%d (%s %s)", auto.Rank, auto.Kind, auto.Provider)
			}
		} else {
			res.AutoPick = "no option can be placed automatically (approval or capacity needed); box_up returns NO_CAPACITY and files a pool_expand approval"
		}
	} else {
		res.Explain = "no option satisfies the requirements; see rejected"
		res.AutoPick = "nothing fits"
	}
	args := map[string]any{"profile": prof.ID, "template": tpl, "lifecycle": lc, "cpu": req.CPU, "ram_mb": req.RAMMB, "disk_gb": req.DiskGB}
	if req.Region != "" {
		args["region"] = req.Region
	}
	if req.MaxPriceHour > 0 {
		args["max_price_hour"] = req.MaxPriceHour
	}
	res.BoxUp = args
	return res, nil
}

func (m *Manager) firstAutoPlaceable(ranked []selector.Scored) *selector.Scored {
	for i := range ranked {
		s := &ranked[i]
		switch s.Kind {
		case selector.KindWarm, selector.KindPool, selector.KindVM:
			return s
		case selector.KindBurst, selector.KindSandbox:
			if !s.RequiresApproval {
				return s
			}
		}
	}
	return nil
}

func (m *Manager) probeBox(req model.Requirements, prof selector.Profile, lifecycle string) *model.Box {
	b := &model.Box{ID: "recommend", Lifecycle: lifecycle, Region: req.Region, Spec: model.Spec{CPU: req.CPU, RAMMB: req.RAMMB, DiskGB: req.DiskGB}}
	if b.Lifecycle == "" {
		b.Lifecycle = model.LifecycleSession
	}
	if req.DurationHours > 0 && b.Lifecycle == model.LifecycleTask {
		b.TTLSeconds = int(req.DurationHours * 3600)
	} else if b.Lifecycle == model.LifecycleTask {
		b.TTLSeconds = prof.TTLSeconds
	}
	return b
}

// hoursFor uses duration_hours when given, else the lifecycle estimate.
func hoursFor(b *model.Box, req model.Requirements) float64 {
	if req.DurationHours > 0 {
		return req.DurationHours
	}
	return expectedHours(b)
}

var defaultProvisionS = map[string]float64{"hetzner": 60, "vultr": 90, "digitalocean": 60, "contabo": 900, "netcup": 1200}

// containerETA estimates a cold container create on a host (seconds).
func (m *Manager) containerETA(h *model.Host, template string) float64 {
	if h != nil {
		m.etaMu.Lock()
		v := m.etas[h.ID+"|"+template]
		m.etaMu.Unlock()
		if len(v) > 0 {
			return math.Max(0.2, avg(v))
		}
		if h.Driver == "local" {
			return 1
		}
	}
	switch template {
	case "python", "node":
		return 45
	case "python-browser":
		return 90
	}
	return 25
}

func (m *Manager) warmClaimETA() float64 {
	m.warm.mu.Lock()
	defer m.warm.mu.Unlock()
	if a := avg(m.warm.claimMS); a > 0 {
		return math.Max(0.05, a/1000)
	}
	return 0.2 // one claim RPC (AGENTS.md + env); measured values replace this
}

func hasFeature(h *model.Host, f string) bool {
	for _, x := range h.Features {
		if x == f {
			return true
		}
	}
	return false
}

// buildOptions turns live state into selector options for box b.
func (m *Manager) buildOptions(ctx context.Context, b *model.Box, agent *model.Agent, req model.Requirements, baseTemplate string, exclude map[string]bool) ([]selector.Option, []selector.Rejection) {
	var opts []selector.Option
	var rej []selector.Rejection
	if agent == nil {
		agent = &model.Agent{}
	}
	hours := hoursFor(b, req)
	hosts, _ := m.Store.ListHosts(ctx)
	hostByID := map[string]*model.Host{}
	for _, h := range hosts {
		hostByID[h.ID] = h
	}
	// pool hosts (containers, and VMs where supported)
	cands, _, rejected, _ := m.candidates(ctx, b, agent, exclude)
	for _, r := range rejected {
		rej = append(rej, selector.Rejection{Kind: selector.KindPool, HostID: strings.SplitN(r, ":", 2)[0], Reason: r})
	}
	for _, c := range cands {
		h := c.host
		kind := selector.KindPool
		if h.Billing == "burst" {
			kind = selector.KindPool // an already-running burst host is used like a pool host
		}
		o := selector.Option{Kind: kind, Provider: h.Provider, Region: h.Region, HostID: h.ID, Arch: h.Arch, Isolation: "container",
			CPU: h.CPU, RAMMB: h.RAMMB, DiskGB: maxInt(h.DiskGB*2, b.Spec.DiskGB), CostPerHour: model.Round6(c.costPerHour),
			ExpectedTotalUSD: model.Round4(c.costPerHour*hours + c.cost), ETASeconds: round1(m.containerETA(h, baseTemplate)),
			Reliability: 1, Health: "healthy", Headroom: math.Max(0, 1-c.loadAfter), LatencyMS: selector.EstimateLatencyMS(req.Location, h.Region),
			Notes: []string{fmt.Sprintf("host load %.0f%% → %.0f%%", c.loadBefore*100, c.loadAfter*100)}, Tier: "T1", Fork: true, Pause: true, Ports: true}
		opts = append(opts, o)
		if hasFeature(h, "vm") {
			v := o
			v.Kind, v.Isolation = selector.KindVM, "vm"
			v.ETASeconds = round1(o.ETASeconds + 30)
			v.Notes = []string{"Incus VM: own kernel, stronger isolation, slower start"}
			opts = append(opts, v)
		}
	}
	// warm pool
	seenWarmHost := map[string]bool{}
	for _, e := range m.warmReady(baseTemplate, func(e *WarmEntry) bool { return m.warmFits(e, b, agent, exclude) }) {
		if seenWarmHost[e.HostID] {
			continue
		}
		seenWarmHost[e.HostID] = true
		h := hostByID[e.HostID]
		if h == nil {
			continue
		}
		cph := poolShare(h, b.Spec.CPU)
		opts = append(opts, selector.Option{Kind: selector.KindWarm, Provider: h.Provider, Region: h.Region, HostID: h.ID, Arch: h.Arch, Isolation: "container",
			CPU: h.CPU, RAMMB: h.RAMMB, DiskGB: maxInt(h.DiskGB*2, b.Spec.DiskGB), CostPerHour: model.Round6(cph), ExpectedTotalUSD: model.Round4(cph * hours),
			ETASeconds: round1(m.warmClaimETA()), Reliability: 1, Health: "healthy", Headroom: warmHeadroom(cands, h.ID), LatencyMS: selector.EstimateLatencyMS(req.Location, h.Region),
			WarmID: e.ID, Notes: []string{"pre-started container: no image unpack or template setup"}, Tier: "T1", Fork: true, Pause: true, Ports: true})
	}
	if len(hosts) > 0 && req.Isolation == "vm" {
		anyVM := false
		for _, h := range hosts {
			anyVM = anyVM || hasFeature(h, "vm")
		}
		if !anyVM {
			rej = append(rej, selector.Rejection{Kind: selector.KindVM, Reason: "no pool host supports VMs (needs /dev/kvm and the incus driver)"})
		}
	}
	for _, o := range m.offerOptions(ctx, b, agent, req, true) {
		if exclude["provider:"+o.Provider] {
			rej = append(rej, selector.Rejection{Kind: o.Kind, Provider: o.Provider, Region: o.Region, SKU: o.SKU, Reason: "provider failed during this placement; failing over"})
			continue
		}
		opts = append(opts, o)
	}
	anySandbox := false
	for _, o := range opts {
		anySandbox = anySandbox || o.Kind == selector.KindSandbox
	}
	if !anySandbox {
		rej = append(rej, selector.Rejection{Kind: selector.KindSandbox, Reason: "no sandbox provider has a size that fits (or all are excluded by policy/region)"})
	}
	return opts, rej
}

// warmFits: entry on an allowed, non-excluded host, big enough for the box.
func (m *Manager) warmFits(e *WarmEntry, b *model.Box, agent *model.Agent, exclude map[string]bool) bool {
	if exclude[e.HostID] || b.Spec.CPU > e.Spec.CPU || b.Spec.RAMMB > e.Spec.RAMMB || b.Spec.DiskGB > e.Spec.DiskGB {
		return false
	}
	h, err := m.Store.GetHost(context.Background(), e.HostID)
	if err != nil || h.Status != "ready" {
		return false
	}
	if agent != nil && (!allowed(agent.Policy.Providers, h.Provider) || !allowed(agent.Policy.Regions, h.Region)) {
		return false
	}
	return b.Region == "" || b.Region == h.Region
}

func poolShare(h *model.Host, cpu float64) float64 {
	if h.CPU <= 0 {
		return 0
	}
	return h.PriceHour * math.Min(1, cpu/h.CPU)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// offerOptions: cheapest fitting offer per provider+region (≤3 regions per
// provider, sorted by expected cost), blocked ones included with a reason.
func (m *Manager) offerOptions(ctx context.Context, b *model.Box, agent *model.Agent, req model.Requirements, withBlocked bool) []selector.Option {
	offers, _ := m.Store.ListOffers(ctx, "")
	verified := map[string]bool{}
	for _, o := range offers {
		if o.Confidence == "verified" {
			verified[o.Provider] = true
		}
	}
	hours := hoursFor(b, req)
	best := map[string]selector.Option{}
	connectedCache := map[string]bool{}
	connected := func(pid string) bool {
		if v, ok := connectedCache[pid]; ok {
			return v
		}
		mp, err := m.Store.GetProvider(ctx, pid)
		v := err == nil && mp.Connected && mp.TokenEnc != ""
		connectedCache[pid] = v
		return v
	}
	stats := map[string]struct {
		rel, prov float64
		status    string
	}{}
	now := time.Now()
	for _, o := range offers {
		if verified[o.Provider] && o.Confidence != "verified" {
			continue
		}
		if o.CPU < b.Spec.CPU || o.RAMMB < b.Spec.RAMMB || (o.DiskGB > 0 && o.DiskGB < b.Spec.DiskGB) {
			continue
		}
		if agent != nil && (!allowed(agent.Policy.Providers, o.Provider) || !allowed(agent.Policy.Regions, o.Region)) {
			continue
		}
		if b.Region != "" && b.Region != o.Region {
			continue
		}
		prov, ok := m.Providers.Get(o.Provider)
		if !ok || !prov.Implemented() || o.Provider == "local" {
			continue
		}
		st, ok := stats[o.Provider]
		if !ok {
			s := m.Health.Stats(o.Provider, "")
			st.rel, st.status, st.prov = s.Reliability, s.Status, s.ProvisionP50S
			if s.Samples == 0 {
				st.rel = 0.9 // unknown history: slightly below proven providers
			}
			if st.prov == 0 && !isSandboxTier(prov.Tier()) {
				st.prov = defaultProvisionS[o.Provider]
				if st.prov == 0 {
					st.prov = 120
				}
			}
			stats[o.Provider] = st
		}
		blocked := m.offerBlocked(ctx, o)
		if blocked != "" && !withBlocked {
			continue
		}
		total := provider.Quote(o, hours, m.Cfg.SetupCostUSD)
		key := o.Provider + "|" + o.Region
		if cur, ok := best[key]; ok && cur.ExpectedTotalUSD <= total {
			continue
		}
		opt := selector.Option{Kind: selector.KindBurst, Provider: o.Provider, Region: o.Region, SKU: o.SKU, Arch: archOf(o.Provider, o.SKU), Isolation: "container",
			CPU: o.CPU, RAMMB: o.RAMMB, DiskGB: o.DiskGB, CostPerHour: model.Round6(o.PriceHour), ExpectedTotalUSD: model.Round4(total),
			ETASeconds: round1(st.prov + m.containerETA(nil, b.Template)), Reliability: st.rel, Health: st.status, Headroom: 1,
			LatencyMS: selector.EstimateLatencyMS(req.Location, o.Region), MinBillingHours: float64(o.MinBillingMinutes) / 60,
			RequiresApproval: !m.Cfg.AutoBurst || !connected(o.Provider), PriceStale: now.Sub(o.FetchedAt) > m.Cfg.CatalogStaleAfter, Blocked: blocked}
		if o.Confidence == "stock_limited" {
			opt.Headroom = 0.5
			opt.Notes = append(opt.Notes, "stock limited at the provider")
		}
		opt.Tier, opt.Fork, opt.Pause, opt.Ports = "T1", true, true, true
		if sp, ok := prov.(sandbox.Provider); ok {
			m.decorateSandboxOption(ctx, &opt, sp, st.prov)
			opt.RequiresApproval = !m.autoPlace(opt.Kind) || !connected(o.Provider)
		}
		best[key] = opt
	}
	out := make([]selector.Option, 0, len(best))
	for _, o := range best {
		out = append(out, o)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ExpectedTotalUSD != out[j].ExpectedTotalUSD {
			return out[i].ExpectedTotalUSD < out[j].ExpectedTotalUSD
		}
		return out[i].Provider+out[i].Region < out[j].Provider+out[j].Region
	})
	perProv := map[string]int{}
	var trimmed []selector.Option
	for _, o := range out {
		if perProv[o.Provider] >= 3 {
			continue
		}
		perProv[o.Provider]++
		trimmed = append(trimmed, o)
	}
	return trimmed
}

// ---- placement for boxes created with a profile/requirements ----

// place decides where box b goes: the smart (scored) path when it carries
// requirements, else a warm-pool hit for its template, else Scheduler v1.
func (m *Manager) place(ctx context.Context, b *model.Box, agent *model.Agent, exclude map[string]bool, baseTemplate string) (*Placement, *WarmEntry, error) {
	if b.Requirements != nil {
		if pl, e, ok := m.scheduleSmart(ctx, b, agent, exclude, baseTemplate); ok {
			return pl, e, nil
		}
	} else if e := m.takeWarm(ctx, baseTemplate, "", func(e *WarmEntry) bool { return m.warmFits(e, b, agent, exclude) }); e != nil {
		h, err := m.Store.GetHost(ctx, e.HostID)
		if err == nil {
			return &Placement{Host: h, Billing: h.Billing, CostPerHour: model.Round6(poolShare(h, b.Spec.CPU)), Kind: "warm", ETA: m.warmClaimETA(),
				Reason: fmt.Sprintf("warm pool hit: pre-started %s container on %s (%s/%s)", baseTemplate, h.ID, h.Provider, h.Region)}, e, nil
		}
	}
	pl, err := m.Schedule(ctx, b, agent, exclude)
	if err != nil {
		return nil, nil, err
	}
	if pl.Host != nil {
		pl.Kind, pl.ETA = "pool", m.containerETA(pl.Host, baseTemplate)
	} else if pl.Offer != nil {
		pl.Kind, pl.ETA = "burst", defaultProvisionS[pl.Offer.Provider]+m.containerETA(nil, baseTemplate)
		if sp, ok := m.sandboxProv(pl.Offer.Provider); ok {
			pl.Kind, pl.Billing, pl.ETA = "sandbox", "sandbox", m.sandboxETA(ctx, sp.Info())
		}
	}
	return pl, nil, nil
}

// scheduleSmart ranks options and takes the best one that can be placed
// without approval. ok=false → fall back to Scheduler v1 (NO_CAPACITY etc).
func (m *Manager) scheduleSmart(ctx context.Context, b *model.Box, agent *model.Agent, exclude map[string]bool, baseTemplate string) (*Placement, *WarmEntry, bool) {
	req := *b.Requirements
	prof, ok := selector.GetProfile(req.Profile)
	if !ok {
		prof, _ = selector.GetProfile("general")
	}
	opts, _ := m.buildOptions(ctx, b, agent, req, baseTemplate, exclude)
	ranked, _ := selector.Rank(req, prof, opts)
	connected := func(pid string) bool {
		mp, err := m.Store.GetProvider(ctx, pid)
		return err == nil && mp.Connected && mp.TokenEnc != ""
	}
	for i := range ranked {
		s := ranked[i]
		reason := fmt.Sprintf("smart pick #%d of %d (%s, profile %s, optimize %s): %s", s.Rank, len(ranked), s.Kind, prof.ID, req.Optimize, strings.Join(s.Reasons, "; "))
		switch s.Kind {
		case selector.KindWarm:
			e := m.takeWarm(ctx, baseTemplate, s.WarmID, nil)
			if e == nil {
				continue
			}
			h, err := m.Store.GetHost(ctx, e.HostID)
			if err != nil {
				continue
			}
			return &Placement{Host: h, Billing: h.Billing, CostPerHour: s.CostPerHour, Reason: reason, Kind: "warm", ETA: s.ETASeconds, Score: s.Score}, e, true
		case selector.KindPool, selector.KindVM:
			h, err := m.Store.GetHost(ctx, s.HostID)
			if err != nil {
				continue
			}
			pl := &Placement{Host: h, Billing: h.Billing, CostPerHour: s.CostPerHour, Reason: reason, Kind: s.Kind, ETA: s.ETASeconds, Score: s.Score}
			if s.Kind == selector.KindVM {
				pl.Isolation = "vm"
			}
			return pl, nil, true
		case selector.KindBurst, selector.KindSandbox:
			if !m.autoPlace(s.Kind) || !connected(s.Provider) {
				continue
			}
			o := m.findOffer(ctx, s.Provider, s.SKU, s.Region)
			if o == nil {
				continue
			}
			billing := "burst"
			if s.Kind == selector.KindSandbox {
				billing = "sandbox"
			}
			return &Placement{Offer: o, Billing: billing, CostPerHour: s.CostPerHour, Reason: reason, Kind: s.Kind, ETA: s.ETASeconds, Score: s.Score}, nil, true
		}
	}
	return nil, nil, false
}

func (m *Manager) findOffer(ctx context.Context, prov, sku, region string) *model.Offer {
	offers, _ := m.Store.ListOffers(ctx, prov)
	var best *model.Offer
	for i := range offers {
		o := offers[i]
		if o.SKU == sku && o.Region == region && (best == nil || o.Confidence == "verified") {
			best = &o
		}
	}
	return best
}

// applyNeeds resolves profile/requirements on a create request: template,
// lifecycle, spec, TTL/idle and the profile's egress default.
func (m *Manager) applyNeeds(ctx context.Context, req *boxyaml.Request, agent *model.Agent) (*model.Requirements, error) {
	if req.Profile == "" && req.Requirements == nil {
		return nil, nil
	}
	if req.Profile != "" {
		if _, ok := selector.GetProfile(req.Profile); !ok {
			return nil, model.EH(model.ErrInvalid, "Use one of GET /profiles.", "unknown profile %q", req.Profile)
		}
	}
	var rq model.Requirements
	if req.Requirements != nil {
		rq = *req.Requirements
	}
	if req.Spec.CPU > 0 && rq.CPU == 0 {
		rq.CPU = req.Spec.CPU
	}
	if req.Spec.RAMMB > 0 && rq.RAMMB == 0 {
		rq.RAMMB = req.Spec.RAMMB
	}
	if req.Spec.DiskGB > 0 && rq.DiskGB == 0 {
		rq.DiskGB = req.Spec.DiskGB
	}
	if req.Region != "" && rq.Region == "" {
		rq.Region = req.Region
	}
	r, prof := m.resolveNeeds(ctx, rq, req.Profile, "", agent)
	if r.GPU > 0 {
		return nil, model.EH(model.ErrInvalid, "Remove gpu from the requirements; GPU boxes are planned.", "GPU boxes are not supported yet")
	}
	if req.Template == "" {
		req.Template = prof.Template
	}
	if req.Lifecycle == "" {
		req.Lifecycle = prof.Lifecycle
	}
	req.Spec.CPU, req.Spec.RAMMB, req.Spec.DiskGB = r.CPU, r.RAMMB, r.DiskGB
	if req.Region == "" {
		req.Region = r.Region
	}
	if req.IdleMinutes == 0 && prof.IdleMinutes > 0 {
		req.IdleMinutes = prof.IdleMinutes
	}
	if req.TTLSeconds == 0 && req.Lifecycle == model.LifecycleTask {
		if r.DurationHours > 0 {
			req.TTLSeconds = int(r.DurationHours * 3600)
		} else if prof.TTLSeconds > 0 {
			req.TTLSeconds = prof.TTLSeconds
		}
	}
	if req.Egress == nil && prof.EgressDenyByDefault {
		req.Egress = egressPolicy(prof.EgressAllow, true)
	}
	return &r, nil
}

// previewEstimate is the cost/ETA preview returned by POST /boxes before
// placement finishes (placement replaces it with the real numbers).
func (m *Manager) previewEstimate(ctx context.Context, b *model.Box, agent *model.Agent, baseTemplate string) *model.BoxEstimate {
	req := model.Requirements{}
	if b.Requirements != nil {
		req = *b.Requirements
	}
	hours := hoursFor(b, req)
	if len(m.warmReady(baseTemplate, func(e *WarmEntry) bool { return m.warmFits(e, b, agent, nil) })) > 0 {
		if cands, _, _, err := m.candidates(ctx, b, agent, nil); err == nil && len(cands) > 0 {
			cph := cands[0].costPerHour
			return &model.BoxEstimate{Kind: "warm", CostPerHour: model.Round6(cph), ExpectedHours: hours, ExpectedTotalUSD: model.Round4(cph * hours), ETASeconds: round1(m.warmClaimETA()), WarmHit: true}
		}
	}
	cands, offers, _, err := m.candidates(ctx, b, agent, nil)
	if err == nil && len(cands) > 0 {
		c := cands[0]
		return &model.BoxEstimate{Kind: "pool", CostPerHour: model.Round6(c.costPerHour), ExpectedHours: hours, ExpectedTotalUSD: model.Round4(c.costPerHour * hours), ETASeconds: round1(m.containerETA(c.host, baseTemplate))}
	}
	if len(offers) > 0 {
		o := offers[0].offer
		return &model.BoxEstimate{Kind: "burst", CostPerHour: model.Round6(o.PriceHour), ExpectedHours: hours, ExpectedTotalUSD: model.Round4(provider.Quote(*o, hours, m.Cfg.SetupCostUSD)), ETASeconds: round1(defaultProvisionS[o.Provider] + m.containerETA(nil, baseTemplate))}
	}
	return nil
}

// Profiles is GET /profiles.
func (m *Manager) Profiles() map[string]any {
	return map[string]any{"items": selector.Profiles, "kinds": selector.KindProfiles,
		"optimize":  []string{"balanced", "cost", "speed", "reliability", "latency"},
		"isolation": []string{"any", "container", "vm", "sandbox"}, "lifetime": []string{"ephemeral", "persistent"}}
}

func egressPolicy(allow []string, deny bool) *egress.Policy {
	return &egress.Policy{Allow: append([]string{}, allow...), DenyByDefault: deny}
}

// warmHeadroom: a warm container's capacity is already reserved, so the
// host's headroom does not shrink when it is claimed.
func warmHeadroom(cands []candidate, hostID string) float64 {
	for _, c := range cands {
		if c.host.ID == hostID {
			return math.Max(0, 1-c.loadBefore)
		}
	}
	return 0.7
}
