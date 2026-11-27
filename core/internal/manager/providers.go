package manager

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"boxmes/core/internal/model"
	"boxmes/core/internal/provhealth"
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
		m.fillProviderMeta(p)
		if p.ID != "local" {
			p.AccountsSummary = m.accountsSummary(ctx, p.ID)
		}
		lc := m.LifecycleFor(p.ID)
		p.Lifecycle = &lc
	}
	sort.Slice(ps, func(i, j int) bool { return order[ps[i].ID] < order[ps[j].ID] })
	return ps, nil
}

// accountsSummary counts accounts from the last computed stats.
func (m *Manager) accountsSummary(ctx context.Context, id string) *model.AccountsSummary {
	s := &model.AccountsSummary{}
	now := time.Now().UTC()
	for _, a := range m.listAccounts(ctx, id) {
		if a.Status == model.AccountArchived {
			continue
		}
		s.Total++
		m.accts.mu.Lock()
		st := m.accts.stats[a.ID]
		m.accts.mu.Unlock()
		if st == nil {
			st = &model.AccountStats{HasCredential: m.hasCredential(ctx, a)}
		}
		if m.ineligible(a, st, "", 0, now) == "" {
			s.Active++
		} else if a.Enabled {
			s.Degraded++
		}
	}
	return s
}

// syncDefaultAccount mirrors PUT /providers/{id}/token onto the default
// account (fingerprint, status, audit).
func (m *Manager) syncDefaultAccount(ctx context.Context, p *Principal, mp *model.Provider, token string) {
	a := m.ensureDefaultAccount(ctx, mp)
	if a == nil {
		return
	}
	l := m.acctLock(a.ID)
	l.Lock()
	defer l.Unlock()
	cur, err := m.Store.GetProviderAccount(ctx, a.ID)
	if err != nil {
		return
	}
	normAccount(cur)
	now := time.Now().UTC()
	old, oldFP := cur.Status, cur.KeyFingerprint
	cur.KeyFingerprint, cur.CredentialSource, cur.UpdatedBy = Fingerprint(token), orStr(mp.TokenSource, "api"), p.Actor()
	if token == "" {
		cur.Enabled, cur.Status, cur.StatusReason = false, model.AccountDisabled, "token removed"
	} else {
		if oldFP != "" && oldFP != cur.KeyFingerprint {
			cur.RotatedAt = &now
		}
		if !cur.Enabled && cur.StatusReason == "token removed" {
			cur.Enabled = true
		}
		if cur.Status == model.AccountAuthFailed || cur.Status == model.AccountDisabled && cur.Enabled {
			cur.Status, cur.StatusReason = model.AccountActive, ""
		}
		cur.LastOKAt, cur.CooldownUntil, cur.ErrorStreak = &now, nil, 0
	}
	m.Health.Reset(acctKey(cur))
	m.setAccountStatus(ctx, cur, old, now)
	m.putAccount(ctx, cur)
	if token != "" && oldFP != cur.KeyFingerprint {
		m.EmitData(ctx, p.Actor(), "", "", "provider_account.key_rotated", fmt.Sprintf("%s default account: key %s → %s", mp.ID, oldFP, cur.KeyFingerprint), nil,
			map[string]any{"provider": mp.ID, "account_id": cur.ID, "old_fingerprint": oldFP, "key_fingerprint": cur.KeyFingerprint})
	}
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
		m.syncDefaultAccount(ctx, p, mp, "")
		m.Emit(ctx, p.Actor(), "", "", "provider.disconnected", id+" token removed", nil)
		m.fillProviderMeta(mp)
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
	m.syncDefaultAccount(ctx, p, mp, token)
	m.Emit(ctx, p.Actor(), "", "", "provider.connected", id+" token validated", nil)
	go func() {
		c, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		m.refreshLiveOffers(c, id)
	}()
	m.fillProviderMeta(mp)
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
