package manager

// v1.3 smart provider manager: continuous health checks, circuit breaker +
// failover, rate-limit/quota awareness, per-provider preferences (priority,
// budget caps), token validation for the connect wizard and a merged live
// price + capability catalog with staleness.

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"boxa/core/internal/model"
	"boxa/core/internal/provhealth"
	"boxa/core/internal/provider"
	"boxa/core/internal/selector"
	"boxa/core/internal/store"
)

const kvHealth = "provider_health"

func (m *Manager) initV13(ctx context.Context) {
	var snap provhealth.Snapshot
	if err := m.Store.GetKV(ctx, kvHealth, &snap); err == nil {
		m.Health.Import(snap)
	}
	m.loadWarm(ctx)
}

func (m *Manager) saveHealth(ctx context.Context) {
	_ = m.Store.PutKV(ctx, kvHealth, m.Health.Export())
}

// probeable reports whether core can call the provider API (adapter + token).
func (m *Manager) probeable(ctx context.Context, id string) (provider.Provider, string, bool) {
	prov, ok := m.Providers.Get(id)
	if !ok || !prov.Implemented() || id == "local" {
		return nil, "", false
	}
	tok, err := m.providerToken(ctx, id)
	if err != nil {
		return nil, "", false
	}
	return prov, tok, true
}

// healthTick probes every connected provider API (Validate = cheapest
// authenticated call) and records latency / errors. A probe is also how an
// open breaker recovers (half-open → closed on success).
func (m *Manager) healthTick(ctx context.Context) error {
	var errs []string
	for _, p := range m.Providers.All() {
		prov, tok, ok := m.probeable(ctx, p.ID())
		if !ok {
			continue
		}
		if allowed, why := m.Health.Peek(p.ID()); !allowed && strings.HasPrefix(why, "rate limited") {
			continue // never add load while the provider asks us to back off
		}
		if err := m.probe(ctx, prov, tok); err != nil {
			errs = append(errs, p.ID()+": "+err.Error())
		}
	}
	m.saveHealth(ctx)
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

func (m *Manager) probe(ctx context.Context, prov provider.Provider, tok string) error {
	c, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	start := time.Now()
	err := prov.Validate(c, tok)
	m.Health.Record(prov.ID(), provhealth.Sample{Kind: provhealth.KindAPI, LatencyMS: float64(time.Since(start).Microseconds()) / 1000}, err, 0)
	mp, gerr := m.Store.GetProvider(ctx, prov.ID())
	if gerr == nil {
		now := time.Now().UTC()
		mp.LastCheckAt = &now
		if err != nil {
			s := err.Error()
			if ae, ok := err.(*model.APIError); ok {
				s = ae.Message
			}
			mp.Error = &s
		} else {
			mp.Error = nil
		}
		m.Store.PutProvider(ctx, mp)
	}
	return err
}

// CheckProviderNow runs one probe (POST /providers/:id/check).
func (m *Manager) CheckProviderNow(ctx context.Context, p *Principal, id string) (*ProviderHealth, error) {
	if err := p.Require(model.ScopeAdmin); err != nil {
		return nil, err
	}
	if _, ok := m.Providers.Get(id); !ok {
		return nil, notFound("provider", id)
	}
	if prov, tok, ok := m.probeable(ctx, id); ok {
		_ = m.probe(ctx, prov, tok)
		m.saveHealth(ctx)
	}
	return m.ProviderHealthOne(ctx, p, id)
}

// ResetProviderBreaker closes the breaker (POST /providers/:id/reset).
func (m *Manager) ResetProviderBreaker(ctx context.Context, p *Principal, id string) (*ProviderHealth, error) {
	if err := p.Require(model.ScopeAdmin); err != nil {
		return nil, err
	}
	if _, ok := m.Providers.Get(id); !ok {
		return nil, notFound("provider", id)
	}
	m.Health.Reset(id)
	m.saveHealth(ctx)
	m.Emit(ctx, p.Actor(), "", "", "provider.breaker_reset", id+" circuit breaker reset", nil)
	return m.ProviderHealthOne(ctx, p, id)
}

// guardedCreate wraps provider Create with the breaker, rate-limit
// awareness, per-provider caps and health recording.
func (m *Manager) guardedCreate(ctx context.Context, prov provider.Provider, tok string, req provider.CreateRequest, priceMonth float64) (provider.Handle, error) {
	id := prov.ID()
	if ok, why := m.Health.Allow(id); !ok {
		return provider.Handle{}, model.EH(model.ErrProviderFailed, "Boxa fails over to the next provider automatically; check GET /providers/health.", "%s skipped: %s", id, why)
	}
	start := time.Now()
	h, err := prov.Create(ctx, tok, req)
	m.Health.Record(id, provhealth.Sample{Kind: provhealth.KindCreate, Region: req.Region, LatencyMS: float64(time.Since(start).Milliseconds())}, err, 0)
	m.saveHealth(ctx)
	if err != nil {
		cls := provhealth.Classify(err)
		m.EmitData(ctx, "system", "", "", "provider.create_failed", fmt.Sprintf("%s %s %s: %v", id, req.SKU, req.Region, err), nil,
			map[string]any{"provider": id, "region": req.Region, "error_class": cls})
	}
	return h, err
}

// recordProvision records the time from host creation to boxd connecting.
func (m *Manager) recordProvision(h *model.Host, err error) {
	if h == nil || h.Provider == "local" {
		return
	}
	m.Health.Record(h.Provider, provhealth.Sample{Kind: provhealth.KindProvision, Region: h.Region, LatencyMS: float64(time.Since(h.CreatedAt).Milliseconds())}, err, 0)
	m.saveHealth(context.Background())
}

// ---- preferences ----

func prefsKey(id string) string { return "provider_prefs:" + id }

// ProviderPrefs returns the stored preferences (defaults when unset).
func (m *Manager) ProviderPrefs(ctx context.Context, id string) model.ProviderPrefs {
	pp := model.DefaultProviderPrefs()
	_ = m.Store.GetKV(ctx, prefsKey(id), &pp)
	if pp.Regions == nil {
		pp.Regions = []string{}
	}
	return pp
}

// PutProviderPrefs validates and stores preferences (PUT /providers/:id/prefs).
func (m *Manager) PutProviderPrefs(ctx context.Context, p *Principal, id string, pp model.ProviderPrefs) (model.ProviderPrefs, error) {
	if err := p.Require(model.ScopeAdmin); err != nil {
		return pp, err
	}
	if _, ok := m.Providers.Get(id); !ok {
		return pp, notFound("provider", id)
	}
	if pp.Priority < 0 || pp.Priority > 99 || pp.MaxHourlyUSD < 0 || pp.MaxMonthlyUSD < 0 || pp.MaxServers < 0 {
		return pp, model.EH(model.ErrInvalid, "priority is 0..99 (0 = chain order); caps are >= 0 (0 = no cap).", "invalid provider preferences")
	}
	if pp.Regions == nil {
		pp.Regions = []string{}
	}
	pp.UpdatedAt = time.Now().UTC()
	if err := m.Store.PutKV(ctx, prefsKey(id), pp); err != nil {
		return pp, err
	}
	m.Emit(ctx, p.Actor(), "", "", "provider.prefs_updated", fmt.Sprintf("%s: enabled=%v priority=%d max $%.4f/h, $%.2f/mo, %d servers", id, pp.Enabled, pp.Priority, pp.MaxHourlyUSD, pp.MaxMonthlyUSD, pp.MaxServers), nil)
	return pp, nil
}

// capsAllow checks a provider's enabled flag, region list and budget caps
// for one more host (priceHour/priceMonth of the new host; 0 = unknown).
func (m *Manager) capsAllow(ctx context.Context, id, region string, priceHour, priceMonth float64) error {
	pp := m.ProviderPrefs(ctx, id)
	deny := func(f string, a ...any) error {
		return model.EH(model.ErrPolicyDenied, "Raise the cap in Infra → Providers or let Boxa use another provider.", f, a...)
	}
	if !pp.Enabled {
		return deny("provider %s is disabled in its preferences", id)
	}
	if region != "" && !allowed(pp.Regions, region) {
		return deny("region %s is not allowed for %s", region, id)
	}
	if pp.MaxHourlyUSD > 0 && priceHour > pp.MaxHourlyUSD+1e-9 {
		return deny("%s price $%.4f/h exceeds the cap $%.4f/h", id, priceHour, pp.MaxHourlyUSD)
	}
	if pp.MaxServers > 0 || pp.MaxMonthlyUSD > 0 {
		hosts, _ := m.Store.ListHosts(ctx)
		n, month := 0, 0.0
		for _, h := range hosts {
			if h.Provider == id && h.Status != "deleted" && h.Status != "down" {
				n++
				month += h.PriceMonth
			}
		}
		if pp.MaxServers > 0 && n+1 > pp.MaxServers {
			return deny("%s already has %d servers (cap %d)", id, n, pp.MaxServers)
		}
		if pp.MaxMonthlyUSD > 0 && month+priceMonth > pp.MaxMonthlyUSD+1e-9 {
			return deny("%s monthly spend would reach $%.2f (cap $%.2f)", id, month+priceMonth, pp.MaxMonthlyUSD)
		}
	}
	return nil
}

// offerBlocked explains why an offer must not be used now ("" = usable).
func (m *Manager) offerBlocked(ctx context.Context, o model.Offer) string {
	if ok, why := m.Health.Peek(o.Provider); !ok {
		return why
	}
	if err := m.capsAllow(ctx, o.Provider, o.Region, o.PriceHour, o.PriceMonth); err != nil {
		return model.AsAPIError(err).Message
	}
	return ""
}

// ---- token validation (connect wizard step 2) ----

// ValidateResult is POST /providers/:id/validate.
type ValidateResult struct {
	Provider   string            `json:"provider"`
	OK         bool              `json:"ok"`
	Error      string            `json:"error,omitempty"`
	ErrorClass string            `json:"error_class,omitempty"`
	LatencyMS  float64           `json:"latency_ms"`
	Regions    []provider.Region `json:"regions"`
	Sizes      []model.Offer     `json:"sizes"`
	SizeCount  int               `json:"size_count"`
	Hint       string            `json:"hint,omitempty"`
}

// ValidateProviderToken checks a token WITHOUT saving it and lists what the
// account can rent (regions + sizes).
func (m *Manager) ValidateProviderToken(ctx context.Context, p *Principal, id, token string) (*ValidateResult, error) {
	if err := p.Require(model.ScopeAdmin); err != nil {
		return nil, err
	}
	prov, ok := m.Providers.Get(id)
	if !ok {
		return nil, notFound("provider", id)
	}
	res := &ValidateResult{Provider: id, Regions: []provider.Region{}, Sizes: []model.Offer{}}
	token = strings.TrimSpace(token)
	if token == "" {
		if t, err := m.providerToken(ctx, id); err == nil {
			token = t // re-validate the stored token
		}
	}
	if token == "" && id != "local" {
		return nil, model.EH(model.ErrInvalid, "Paste the API token from the provider console.", "token is required")
	}
	c, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	start := time.Now()
	err := prov.Validate(c, token)
	res.LatencyMS = float64(time.Since(start).Microseconds()) / 1000
	if err != nil {
		res.Error = err.Error()
		if ae, ok := err.(*model.APIError); ok {
			res.Error, res.Hint = ae.Message, ae.Hint
		}
		res.ErrorClass = provhealth.Classify(err)
		return res, nil
	}
	res.OK = true
	if rg, ok := prov.(provider.Regioner); ok {
		if rs, err := rg.Regions(c, token); err == nil && rs != nil {
			res.Regions = rs
		}
	}
	if offers, err := prov.Offers(c, token); err == nil {
		sort.SliceStable(offers, func(i, j int) bool { return offers[i].PriceHour < offers[j].PriceHour })
		res.SizeCount = len(offers)
		seen := map[string]bool{}
		for _, o := range offers {
			if seen[o.SKU] {
				continue
			}
			seen[o.SKU] = true
			res.Sizes = append(res.Sizes, o)
			if len(res.Sizes) >= 40 {
				break
			}
		}
		if len(res.Regions) == 0 {
			rs := map[string]bool{}
			for _, o := range offers {
				if !rs[o.Region] {
					rs[o.Region] = true
					res.Regions = append(res.Regions, provider.Region{ID: o.Region, Name: o.Region, Available: true})
				}
			}
		}
	}
	return res, nil
}

// ---- health view ----

// ProviderHealth is one row of GET /providers/health.
type ProviderHealth struct {
	Provider *model.Provider     `json:"provider"`
	Stats    provhealth.Stats    `json:"stats"`
	Regions  []provhealth.Stats  `json:"regions"`
	Prefs    model.ProviderPrefs `json:"prefs"`
	Score    *ProviderScore      `json:"score"`
	Recent   []provhealth.Sample `json:"recent,omitempty"`
	Usable   bool                `json:"usable"`
	Blocked  string              `json:"blocked,omitempty"`
	Hosts    int                 `json:"hosts"`
	OfferAge float64             `json:"offer_age_s"` // age of the newest offer (-1 none)
}

// ProviderScore is the explainable provider score for a reference workload
// (1 vCPU / 1 GB general box) — the same engine as POST /recommend.
type ProviderScore struct {
	Score     float64                       `json:"score"`
	Region    string                        `json:"region"`
	SKU       string                        `json:"sku"`
	Breakdown map[string]selector.Component `json:"breakdown"`
	Reasons   []string                      `json:"reasons"`
}

// ProviderHealthAll lists every provider with health, prefs and score.
func (m *Manager) ProviderHealthAll(ctx context.Context, p *Principal) ([]*ProviderHealth, error) {
	if err := p.Require(model.ScopeAdmin); err != nil {
		return nil, err
	}
	ps, err := m.ListProviders(ctx)
	if err != nil {
		return nil, err
	}
	scores := m.referenceScores(ctx)
	hosts, _ := m.Store.ListHosts(ctx)
	offers, _ := m.Store.ListOffers(ctx, "")
	out := make([]*ProviderHealth, 0, len(ps))
	for _, mp := range ps {
		out = append(out, m.healthRow(ctx, mp, scores, hosts, offers, false))
	}
	return out, nil
}

// ProviderHealthOne returns one provider with per-region stats and recent samples.
func (m *Manager) ProviderHealthOne(ctx context.Context, p *Principal, id string) (*ProviderHealth, error) {
	if err := p.Require(model.ScopeAdmin); err != nil {
		return nil, err
	}
	ps, err := m.ListProviders(ctx)
	if err != nil {
		return nil, err
	}
	hosts, _ := m.Store.ListHosts(ctx)
	offers, _ := m.Store.ListOffers(ctx, id)
	for _, mp := range ps {
		if mp.ID == id {
			return m.healthRow(ctx, mp, m.referenceScores(ctx), hosts, offers, true), nil
		}
	}
	return nil, notFound("provider", id)
}

func (m *Manager) healthRow(ctx context.Context, mp *model.Provider, scores map[string]*ProviderScore, hosts []*model.Host, offers []model.Offer, detail bool) *ProviderHealth {
	row := &ProviderHealth{Provider: mp, Stats: m.Health.Stats(mp.ID, ""), Regions: []provhealth.Stats{}, Prefs: m.ProviderPrefs(ctx, mp.ID), Score: scores[mp.ID], OfferAge: -1}
	for _, r := range m.Health.Regions(mp.ID) {
		row.Regions = append(row.Regions, m.Health.Stats(mp.ID, r))
	}
	if detail {
		row.Recent = m.Health.Recent(mp.ID, 30)
	}
	for _, h := range hosts {
		if h.Provider == mp.ID && h.Status != "deleted" {
			row.Hosts++
		}
	}
	var newest time.Time
	for _, o := range offers {
		if o.Provider == mp.ID && o.FetchedAt.After(newest) {
			newest = o.FetchedAt
		}
	}
	if !newest.IsZero() {
		row.OfferAge = time.Since(newest).Round(time.Second).Seconds()
	}
	ok, why := m.Health.Peek(mp.ID)
	row.Usable = mp.Connected && mp.Implemented && ok && row.Prefs.Enabled
	switch {
	case !mp.Implemented:
		row.Blocked = "no adapter"
	case !mp.Connected:
		row.Blocked = "not connected"
	case !row.Prefs.Enabled:
		row.Blocked = "disabled in preferences"
	case !ok:
		row.Blocked = why
	}
	return row
}

// referenceScores scores every provider's best offer for a reference 1 vCPU /
// 1 GB general workload so the Infra page can compare providers at a glance.
func (m *Manager) referenceScores(ctx context.Context) map[string]*ProviderScore {
	req, prof := selector.Resolve(model.Requirements{CPU: 1, RAMMB: 1024, DiskGB: 10, Location: m.clientLocation(ctx, "")}, "")
	b := &model.Box{ID: "ref", Lifecycle: model.LifecycleSession, Spec: model.Spec{CPU: 1, RAMMB: 1024, DiskGB: 10}}
	opts := m.offerOptions(ctx, b, nil, req, true)
	ranked, _ := selector.Rank(req, prof, opts)
	out := map[string]*ProviderScore{}
	for _, s := range ranked {
		if _, ok := out[s.Provider]; ok {
			continue
		}
		out[s.Provider] = &ProviderScore{Score: s.Score, Region: s.Region, SKU: s.SKU, Breakdown: s.Breakdown, Reasons: s.Reasons}
	}
	return out
}

// ---- catalog ----

// CatalogItem merges the same size from the price feed and the live API.
type CatalogItem struct {
	Provider          string    `json:"provider"`
	SKU               string    `json:"sku"`
	Region            string    `json:"region"`
	Arch              string    `json:"arch"`
	CPU               float64   `json:"cpu"`
	RAMMB             int       `json:"ram_mb"`
	DiskGB            int       `json:"disk_gb"`
	PriceHour         float64   `json:"price_hour"`
	PriceMonth        float64   `json:"price_month"`
	MinBillingMinutes int       `json:"min_billing_minutes"`
	PriceSource       string    `json:"price_source"` // the source whose price is used (live API wins)
	Sources           []string  `json:"sources"`
	FeedPriceHour     *float64  `json:"feed_price_hour,omitempty"` // battleships price when the API price differs
	FetchedAt         time.Time `json:"fetched_at"`
	AgeS              float64   `json:"age_s"`
	Stale             bool      `json:"stale"`
	Confidence        string    `json:"confidence"`
	Health            string    `json:"health"`
	Usable            bool      `json:"usable"`
	Blocked           string    `json:"blocked,omitempty"`
}

// CatalogSource summarises one data source.
type CatalogSource struct {
	Source    string    `json:"source"`
	Provider  string    `json:"provider"`
	Count     int       `json:"count"`
	FetchedAt time.Time `json:"fetched_at"`
	AgeS      float64   `json:"age_s"`
	Stale     bool      `json:"stale"`
}

// Catalog is GET /catalog.
type Catalog struct {
	Items        []CatalogItem             `json:"items"`
	Sources      []CatalogSource           `json:"sources"`
	Capabilities map[string]map[string]any `json:"capabilities"`
	GeneratedAt  time.Time                 `json:"generated_at"`
	StaleAfterS  float64                   `json:"stale_after_s"`
	Cached       bool                      `json:"cached"`
	Total        int                       `json:"total"`
}

// CatalogFilter narrows GET /catalog.
type CatalogFilter struct {
	Provider, Region, Arch string
	MinCPU                 float64
	MinRAMMB               int
	MaxPriceHour           float64
	Limit                  int
}

// invalidateCatalog drops the cached catalog (offers changed).
func (m *Manager) invalidateCatalog() { m.catMu.Lock(); m.catalog = nil; m.catVer++; m.catMu.Unlock() }

// archOf infers the CPU architecture from a SKU name.
func archOf(provider, sku string) string {
	s := strings.ToLower(sku)
	switch {
	case provider == "hetzner" && strings.HasPrefix(s, "cax"):
		return "arm64"
	case strings.Contains(s, "arm") || strings.Contains(s, "ampere") || strings.Contains(s, "graviton"):
		return "arm64"
	}
	return "amd64"
}

func (m *Manager) buildCatalog(ctx context.Context) (*Catalog, error) {
	m.catMu.Lock()
	if c := m.catalog; c != nil && time.Since(c.GeneratedAt) < time.Minute {
		m.catMu.Unlock()
		cc := *c
		cc.Cached = true
		return &cc, nil
	}
	ver := m.catVer
	m.catMu.Unlock()
	offers, err := m.Store.ListOffers(ctx, "")
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	stale := m.Cfg.CatalogStaleAfter
	type key struct{ p, sku, r string }
	items := map[key]*CatalogItem{}
	srcs := map[string]*CatalogSource{}
	for _, o := range offers {
		sk := o.Source + "|" + o.Provider
		s := srcs[sk]
		if s == nil {
			s = &CatalogSource{Source: o.Source, Provider: o.Provider}
			srcs[sk] = s
		}
		s.Count++
		if o.FetchedAt.After(s.FetchedAt) {
			s.FetchedAt = o.FetchedAt
		}
		k := key{o.Provider, o.SKU, o.Region}
		it := items[k]
		live := o.Confidence == "verified"
		if it == nil {
			it = &CatalogItem{Provider: o.Provider, SKU: o.SKU, Region: o.Region, Arch: archOf(o.Provider, o.SKU), CPU: o.CPU, RAMMB: o.RAMMB, DiskGB: o.DiskGB,
				PriceHour: o.PriceHour, PriceMonth: o.PriceMonth, MinBillingMinutes: o.MinBillingMinutes, PriceSource: o.Source, FetchedAt: o.FetchedAt, Confidence: o.Confidence}
			items[k] = it
		} else if live && it.Confidence != "verified" {
			fp := it.PriceHour
			it.FeedPriceHour = &fp
			it.PriceHour, it.PriceMonth, it.PriceSource, it.Confidence, it.FetchedAt = o.PriceHour, o.PriceMonth, o.Source, o.Confidence, o.FetchedAt
			if o.CPU > 0 {
				it.CPU, it.RAMMB, it.DiskGB = o.CPU, o.RAMMB, o.DiskGB
			}
		} else if !live && it.Confidence == "verified" {
			fp := o.PriceHour
			it.FeedPriceHour = &fp
		}
		it.Sources = appendUnique(it.Sources, o.Source)
	}
	c := &Catalog{Items: []CatalogItem{}, Sources: []CatalogSource{}, Capabilities: map[string]map[string]any{}, GeneratedAt: now, StaleAfterS: stale.Seconds()}
	health := map[string]string{}
	for _, it := range items {
		it.AgeS = now.Sub(it.FetchedAt).Round(time.Second).Seconds()
		it.Stale = now.Sub(it.FetchedAt) > stale
		if it.FeedPriceHour != nil && model.Round6(*it.FeedPriceHour) == model.Round6(it.PriceHour) {
			it.FeedPriceHour = nil
		}
		h, ok := health[it.Provider]
		if !ok {
			h = m.Health.Stats(it.Provider, "").Status
			health[it.Provider] = h
		}
		it.Health = h
		it.Blocked = m.offerBlocked(ctx, model.Offer{Provider: it.Provider, Region: it.Region, PriceHour: it.PriceHour, PriceMonth: it.PriceMonth})
		mp, err := m.Store.GetProvider(ctx, it.Provider)
		it.Usable = err == nil && mp.Connected && it.Blocked == ""
		c.Items = append(c.Items, *it)
	}
	sort.SliceStable(c.Items, func(i, j int) bool {
		if c.Items[i].PriceHour != c.Items[j].PriceHour {
			return c.Items[i].PriceHour < c.Items[j].PriceHour
		}
		return c.Items[i].Provider+c.Items[i].SKU+c.Items[i].Region < c.Items[j].Provider+c.Items[j].SKU+c.Items[j].Region
	})
	for _, s := range srcs {
		s.AgeS = now.Sub(s.FetchedAt).Round(time.Second).Seconds()
		s.Stale = now.Sub(s.FetchedAt) > stale
		c.Sources = append(c.Sources, *s)
	}
	sort.Slice(c.Sources, func(i, j int) bool {
		return c.Sources[i].Provider+c.Sources[i].Source < c.Sources[j].Provider+c.Sources[j].Source
	})
	for _, p := range m.Providers.All() {
		if caps, err := m.Store.GetCapabilities(ctx, p.ID()); err == nil && len(caps) > 0 {
			c.Capabilities[p.ID()] = caps
		}
	}
	c.Total = len(c.Items)
	m.catMu.Lock()
	if m.catVer == ver { // offers did not change while building
		m.catalog = c
	}
	m.catMu.Unlock()
	return c, nil
}

// GetCatalog returns the merged, filtered catalog (cached for 60 s).
func (m *Manager) GetCatalog(ctx context.Context, f CatalogFilter) (*Catalog, error) {
	full, err := m.buildCatalog(ctx)
	if err != nil {
		return nil, err
	}
	out := *full
	out.Items = []CatalogItem{}
	for _, it := range full.Items {
		if f.Provider != "" && it.Provider != f.Provider || f.Region != "" && it.Region != f.Region || f.Arch != "" && it.Arch != f.Arch {
			continue
		}
		if it.CPU < f.MinCPU || it.RAMMB < f.MinRAMMB || (f.MaxPriceHour > 0 && it.PriceHour > f.MaxPriceHour) {
			continue
		}
		out.Items = append(out.Items, it)
	}
	out.Total = len(out.Items)
	lim := f.Limit
	if lim <= 0 || lim > 1000 {
		lim = 500
	}
	if len(out.Items) > lim {
		out.Items = out.Items[:lim]
	}
	return &out, nil
}

func appendUnique(l []string, v string) []string {
	for _, x := range l {
		if x == v {
			return l
		}
	}
	return append(l, v)
}

// activeHostsAt counts live hosts at a provider.
func (m *Manager) activeHostsAt(ctx context.Context, id string) int {
	hosts, _ := m.Store.ListHosts(ctx)
	n := 0
	for _, h := range hosts {
		if h.Provider == id && h.Status != "deleted" {
			n++
		}
	}
	return n
}

var _ = store.BoxFilter{}
