package manager

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"boxa/core/internal/model"
	"boxa/core/internal/provhealth"
)

func (m *Manager) ListProviders(ctx context.Context) ([]*model.Provider, error) {
	ps, err := m.Store.ListProviders(ctx)
	if err != nil {
		return nil, err
	}
	order := map[string]int{}
	for i, p := range m.Providers.All() {
		order[p.ID()] = i
	}
	for _, p := range ps {
		if caps, err := m.Store.GetCapabilities(ctx, p.ID); err == nil {
			p.Capabilities = caps
		}
		if r := m.chainRank(p.ID); r < len(m.chain()) {
			p.ChainRank = r + 1
		}
		if pr, ok := m.Providers.Get(p.ID); ok {
			p.Implemented = pr.Implemented() // stubs from older DBs became real adapters
		}
		if p.TokenEnc != "" && p.TokenSource == "" {
			p.TokenSource = "api"
		}
	}
	sort.Slice(ps, func(i, j int) bool { return order[ps[i].ID] < order[ps[j].ID] })
	return ps, nil
}

func (m *Manager) providerToken(ctx context.Context, id string) (string, error) {
	p, err := m.Store.GetProvider(ctx, id)
	if err != nil || p.TokenEnc == "" {
		return "", model.EH(model.ErrProviderFailed, "Connect the provider first: PUT /providers/"+id+"/token.", "provider %s not connected", id)
	}
	return m.Crypt.Open(p.TokenEnc)
}

// SetProviderToken validates the token against the provider API, stores it
// encrypted and refreshes live offers.
func (m *Manager) SetProviderToken(ctx context.Context, p *Principal, id, token string) (*model.Provider, error) {
	if err := p.Require(model.ScopeAdmin); err != nil {
		return nil, err
	}
	prov, ok := m.Providers.Get(id)
	if !ok {
		return nil, notFound("provider", id)
	}
	mp, err := m.Store.GetProvider(ctx, id)
	if err != nil {
		mp = &model.Provider{ID: id, Name: prov.Name(), Tier: prov.Tier(), Implemented: prov.Implemented()}
	}
	now := time.Now().UTC()
	mp.LastCheckAt = &now
	token = strings.TrimSpace(token)
	if token == "" {
		// empty token = disconnect
		mp.TokenEnc, mp.Connected, mp.Error, mp.TokenSource = "", id == "local", nil, ""
		m.Store.PutProvider(ctx, mp)
		m.Emit(ctx, p.Actor(), "", "", "provider.disconnected", id+" token removed", nil)
		return mp, nil
	}
	if err := prov.Validate(ctx, token); err != nil {
		msg := err.Error()
		if ae, ok := err.(*model.APIError); ok {
			msg = ae.Message
		}
		mp.Error = &msg
		mp.Connected = false
		m.Store.PutProvider(ctx, mp)
		m.Emit(ctx, p.Actor(), "", "", "provider.token_invalid", id+": "+msg, nil)
		return nil, err
	}
	enc, err := m.Crypt.Seal(token)
	if err != nil {
		return nil, err
	}
	mp.TokenEnc, mp.Connected, mp.Error, mp.TokenSource = enc, true, nil, "api"
	if err := m.Store.PutProvider(ctx, mp); err != nil {
		return nil, err
	}
	m.Health.Reset(id)
	m.Emit(ctx, p.Actor(), "", "", "provider.connected", id+" token validated", nil)
	go func() {
		c, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		m.refreshLiveOffers(c, id)
	}()
	return mp, nil
}

// refreshLiveOffers pulls prices from a connected provider API.
func (m *Manager) refreshLiveOffers(ctx context.Context, id string) error {
	prov, ok := m.Providers.Get(id)
	if !ok || !prov.Implemented() || id == "local" {
		return nil
	}
	tok, err := m.providerToken(ctx, id)
	if err != nil {
		return nil // not connected: nothing to do
	}
	start := time.Now()
	offers, err := prov.Offers(ctx, tok)
	m.Health.Record(id, provhealth.Sample{Kind: provhealth.KindAPI, LatencyMS: float64(time.Since(start).Microseconds()) / 1000}, err, 0)
	defer m.invalidateCatalog() // after the offers are stored
	mp, _ := m.Store.GetProvider(ctx, id)
	now := time.Now().UTC()
	if mp != nil {
		mp.LastCheckAt = &now
	}
	if err != nil {
		if mp != nil {
			s := err.Error()
			mp.Error = &s
			m.Store.PutProvider(ctx, mp)
		}
		return fmt.Errorf("%s offers: %w", id, err)
	}
	if mp != nil {
		mp.Error = nil
		m.Store.PutProvider(ctx, mp)
	}
	return m.Store.ReplaceOffers(ctx, id, id+"-api", offers)
}

// refreshPrices is the price-feed job: battleships + live provider APIs.
func (m *Manager) refreshPrices(ctx context.Context) error {
	var errs []string
	defer m.invalidateCatalog()
	results, err := m.Feed.Fetch(ctx)
	if err != nil {
		errs = append(errs, err.Error())
	}
	for _, r := range results {
		if r.Err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", r.BattleshipsID, r.Err))
			continue
		}
		if err := m.Store.ReplaceOffers(ctx, r.Provider, "battleships", r.Offers); err != nil {
			errs = append(errs, err.Error())
		}
		if len(r.Capabilities) > 0 {
			m.Store.PutCapabilities(ctx, r.Provider, "battleships", r.Capabilities)
		}
	}
	for _, p := range m.Providers.All() {
		if err := m.refreshLiveOffers(ctx, p.ID()); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

// RefreshPricesNow is exposed for admin/testing.
func (m *Manager) RefreshPricesNow(ctx context.Context) error { return m.refreshPrices(ctx) }

type OfferFilter struct {
	Provider, Region string
	MinCPU           float64
	MinRAMMB         int
}

func (m *Manager) ListOffers(ctx context.Context, f OfferFilter) ([]model.Offer, error) {
	all, err := m.Store.ListOffers(ctx, f.Provider)
	if err != nil {
		return nil, err
	}
	out := []model.Offer{}
	for _, o := range all {
		if f.Region != "" && o.Region != f.Region {
			continue
		}
		if o.CPU < f.MinCPU || o.RAMMB < f.MinRAMMB {
			continue
		}
		out = append(out, o)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].PriceHour < out[j].PriceHour })
	return out, nil
}
