package manager

import (
	"context"
	"log"
	"time"

	"boxmes/core/internal/model"
	"boxmes/core/internal/provider"
)

// ConfigureProvidersFromEnv connects providers whose tokens are given by
// environment variables (HETZNER_TOKEN, VULTR_API_KEY, DIGITALOCEAN_TOKEN,
// CONTABO_CLIENT_ID/SECRET/API_USER/API_PASSWORD, NETCUP_REFRESH_TOKEN).
// The token is stored encrypted like a dashboard token and validated in the
// background; an env token always wins over a stored one.
func (m *Manager) ConfigureProvidersFromEnv(ctx context.Context, getenv func(string) string) []string {
	var ids []string
	for _, p := range m.Providers.All() {
		tok := provider.TokenFromEnv(p.ID(), getenv)
		if ef, ok := p.(interface {
			TokenFromEnv(func(string) string) string
		}); ok { // v1.4 sandbox providers
			tok = ef.TokenFromEnv(getenv)
		}
		if tok == "" {
			continue
		}
		enc, err := m.Crypt.Seal(tok)
		if err != nil {
			continue
		}
		mp, err := m.Store.GetProvider(ctx, p.ID())
		if err != nil {
			mp = &model.Provider{ID: p.ID(), Name: p.Name(), Tier: p.Tier(), Implemented: p.Implemented()}
		}
		mp.TokenEnc, mp.Connected, mp.Error, mp.TokenSource = enc, true, nil, "env"
		m.Store.PutProvider(ctx, mp)
		ids = append(ids, p.ID())
		go func(id string, prov provider.Provider) {
			c, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			now := time.Now().UTC()
			mp, _ := m.Store.GetProvider(c, id)
			if mp == nil {
				return
			}
			mp.LastCheckAt = &now
			if err := prov.Validate(c, tok); err != nil {
				s := err.Error()
				mp.Error, mp.Connected = &s, false
				log.Printf("provider %s (env token): %v", id, err)
			}
			m.Store.PutProvider(c, mp)
			if mp.Connected {
				m.refreshLiveOffers(c, id)
			}
		}(p.ID(), p)
	}
	return ids
}

// ProviderRegions lists regions of a provider (live API when connected).
func (m *Manager) ProviderRegions(ctx context.Context, p *Principal, id string) ([]provider.Region, error) {
	if err := p.Require(model.ScopeAdmin); err != nil {
		return nil, err
	}
	prov, ok := m.Providers.Get(id)
	if !ok {
		return nil, notFound("provider", id)
	}
	rg, ok := prov.(provider.Regioner)
	if !ok {
		return []provider.Region{}, nil
	}
	tok, _ := m.providerToken(ctx, id)
	rs, err := rg.Regions(ctx, tok)
	if rs == nil {
		rs = []provider.Region{}
	}
	return rs, err
}

// ProviderServers lists servers at the provider (requires a connected token).
func (m *Manager) ProviderServers(ctx context.Context, p *Principal, id string) ([]provider.Server, error) {
	if err := p.Require(model.ScopeAdmin); err != nil {
		return nil, err
	}
	prov, ok := m.Providers.Get(id)
	if !ok {
		return nil, notFound("provider", id)
	}
	ls, ok := prov.(provider.Lister)
	if !ok {
		return []provider.Server{}, nil
	}
	tok, err := m.providerToken(ctx, id)
	if err != nil {
		return nil, err
	}
	ss, err := ls.List(ctx, tok)
	if ss == nil {
		ss = []provider.Server{}
	}
	return ss, err
}

// ProviderChain returns the scheduler burst fallback chain.
func (m *Manager) ProviderChain() []string { return m.chain() }
