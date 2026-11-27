package manager

// v1.4 T2 "sandbox API" and T3 "exec-only" provider tiers. Sandbox providers
// are ordinary entries of the provider registry (credentials, health,
// breaker, prefs, catalog, chain); this file adds what differs:
//   - a sandbox host holds exactly one box (Billing "sandbox"), created on
//     demand and destroyed when its box ends;
//   - T2: core runs `boxd --dial` inside the sandbox (bootstrap token), so it
//     joins like any host; T3: an in-process shim answers boxd's RPCs;
//   - static per-second offers are seeded so /recommend and /catalog can show
//     sandbox options before a provider is connected.

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"boxmes/core/internal/model"
	"boxmes/core/internal/provider"
	"boxmes/core/internal/sandbox"
	"boxmes/core/internal/secret"
	"boxmes/core/internal/selector"
)

const sandboxSeedSource = "sandbox-pricing"

func isSandboxTier(t string) bool { return t == "T2" || t == "T3" }

// sandboxProv returns the sandbox wrapper for a provider id.
func (m *Manager) sandboxProv(id string) (sandbox.Provider, bool) {
	p, ok := m.Providers.Get(id)
	if !ok {
		return nil, false
	}
	sp, ok := p.(sandbox.Provider)
	return sp, ok
}

func sandboxCaps(info sandbox.Info) map[string]any {
	c := info.Caps
	return map[string]any{"tier": info.Tier, "adapter": info.Adapter, "verification": info.Verification, "docs": info.Docs,
		"background": c.Background, "pause": c.Pause, "snapshot": c.Snapshot, "fork": c.Fork, "ports": c.Ports, "pty": c.PTY,
		"files": c.Files, "gpu": c.GPU, "boot_ms": c.BootMS, "max_lifetime_s": c.MaxLifetimeS, "isolation": c.Isolation,
		"billing": "per_second", "cpu_second_usd": info.Pricing.CPUSecond, "gb_second_usd": info.Pricing.GBSecond,
		"price_confidence": info.Pricing.Confidence, "price_source": info.Pricing.Source}
}

// initV14 seeds sandbox capabilities + static priced offers and re-attaches
// T3 shims after a restart.
func (m *Manager) initV14(ctx context.Context) {
	now := time.Now().UTC()
	for _, p := range m.Providers.All() {
		sp, ok := p.(sandbox.Provider)
		if !ok {
			continue
		}
		info := sp.Info()
		m.Store.PutCapabilities(ctx, info.ID, "adapter", sandboxCaps(info))
		m.Store.ReplaceOffers(ctx, info.ID, sandboxSeedSource, sandbox.StaticOffers(info, sandboxSeedSource, now))
	}
	m.invalidateCatalog()
	hosts, _ := m.Store.ListHosts(ctx)
	for _, h := range hosts {
		if h.Driver == "shim" && h.Status != "deleted" && h.ProviderRef != "" {
			if err := m.attachShim(ctx, h); err != nil {
				m.Emit(ctx, "system", "", "", "host.shim_failed", fmt.Sprintf("host %s: %v", h.ID, err), nil)
			}
		}
	}
}

func (m *Manager) attachShim(ctx context.Context, h *model.Host) error {
	sp, ok := m.sandboxProv(h.Provider)
	if !ok {
		return fmt.Errorf("provider %s is not a sandbox provider", h.Provider)
	}
	tok, _, err := m.hostToken(ctx, h) // v2.2: the host's own account
	if err != nil {
		return err
	}
	m.Hub.RegisterVirtual(h.ID, m.newShim(sp, sandbox.ParseCred(tok), h.ProviderRef))
	return nil
}

// sandboxMode decides T2 (dial) or T3 (shim) for a provider.
func (m *Manager) sandboxMode(ctx context.Context, info sandbox.Info) string {
	mode := strings.ToLower(m.ProviderPrefs(ctx, info.ID).SandboxMode)
	if mode == "" || mode == "auto" {
		mode = strings.ToLower(m.Cfg.SandboxMode)
	}
	if info.DialOnly {
		return "dial" // no exec API: a T3 shim cannot work
	}
	switch mode {
	case "dial", "shim":
		return mode
	}
	if info.Tier == "T2" && info.Caps.Background && m.Cfg.PublicURL != "" {
		return "dial"
	}
	return "shim"
}

// createSandboxHost creates one sandbox and turns it into a single-box host.
// v2.2: the account is picked like for servers (prefer = account to try
// first, e.g. the source box's account for a clone).
func (m *Manager) createSandboxHost(ctx context.Context, p *Principal, sp sandbox.Provider, sku, region, prefer string) (*model.Host, error) {
	info := sp.Info()
	mode := m.sandboxMode(ctx, info)
	if mode == "dial" && m.Cfg.PublicURL == "" {
		return nil, model.EH(model.ErrInvalid, "Set BOXMES_PUBLIC_URL (reachable from the internet) or switch the provider to sandbox_mode=shim.", "T2 sandboxes need BOXMES_PUBLIC_URL so boxd can dial in")
	}
	if region == "" && len(info.Regions) > 0 {
		region = info.Regions[0].ID
	}
	h := &model.Host{ID: secret.ID("host"), Provider: info.ID, Region: region, SKU: sku, Status: "provisioning", Billing: "sandbox", CreatedAt: time.Now().UTC(), Driver: "sandbox"}
	if off := m.findOffer(ctx, info.ID, sku, region); off != nil {
		h.CPU, h.RAMMB, h.DiskGB, h.PriceHour, h.PriceMonth = off.CPU, off.RAMMB, off.DiskGB, off.PriceHour, off.PriceMonth
	} else {
		for _, z := range info.Sizes {
			if z.SKU == sku {
				h.CPU, h.RAMMB, h.DiskGB = z.CPU, z.RAMMB, z.DiskGB
				h.PriceHour = info.SizePrice(z)
				h.PriceMonth = model.Round4(h.PriceHour * 730)
			}
		}
	}
	if err := m.capsAllow(ctx, info.ID, region, h.PriceHour, h.PriceMonth); err != nil {
		return nil, err
	}
	req := provider.CreateRequest{Name: sandbox.NamePrefix + h.ID[5:], SKU: sku, Region: region, Labels: sandbox.ManagedLabels(h.ID[5:])}
	// v1.8: refuse public traffic at the provider's preview host when the
	// gateway is the way in (BOXMES_PROVIDER_PREVIEW / settings.gateway)
	if info.Caps.Ports && m.wantPrivatePreview(ctx) && m.privatePreviewSupported(info.ID) {
		req.PrivateTraffic = true
	}
	if mode == "dial" {
		boot := "bxb_" + secret.RandHex(24)
		h.BootstrapHash = secret.Hash(boot)
		req.Bootstrap = sandbox.BootstrapScript(sandbox.BootstrapParams{CoreURL: wsURL(m.Cfg.PublicURL), BootstrapToken: boot, BoxdURL: m.boxdURL(),
			Provider: info.ID, Region: region, WorkDir: info.WorkDir, Foreground: info.DialOnly})
	} else {
		h.Driver = "shim"
	}
	if err := m.Store.PutHost(ctx, h); err != nil {
		return nil, err
	}
	var hd provider.Handle
	var token string
	_, err := m.acrossAccounts(ctx, info.ID, region, h.PriceHour, prefer, sku+" "+region, func(a *model.ProviderAccount, tok string) error {
		h.AccountID, token = a.ID, tok
		m.Store.PutHost(ctx, h)
		var e error
		hd, e = m.guardedCreate(ctx, sp, a, tok, req, h.PriceMonth)
		return e
	})
	if err != nil {
		h.Status = "down"
		m.Store.PutHost(ctx, h)
		m.Emit(ctx, p.Actor(), "", "", "host.create_failed", fmt.Sprintf("%s %s %s: %v", info.ID, sku, region, err), nil)
		return nil, err
	}
	// In dial mode boxd runs the bootstrap inside Create and may already have
	// dialled in (hello sets status/driver/connected) before Create returns
	// on a fast provider: re-read the record so that update is not lost.
	if cur, err := m.Store.GetHost(ctx, h.ID); err == nil && cur != nil {
		cur.AccountID = h.AccountID
		h = cur
	}
	h.ProviderRef = hd.Ref
	h.PrivateTraffic = hd.Meta[sandbox.MetaPrivateTraffic] == "1"
	how := "T2: boxd dials in from the sandbox"
	if mode == "shim" {
		now := time.Now().UTC()
		h.Status, h.Connected, h.LastSeenAt = "ready", true, &now
		if h.Region == "" {
			h.Region = "global"
		}
		m.Hub.RegisterVirtual(h.ID, m.newShim(sp, sandbox.ParseCred(token), hd.Ref))
		how = "T3: exec-only shim at the gateway"
	}
	m.Store.PutHost(ctx, h)
	if h.PrivateTraffic {
		how += "; provider preview host private (gateway only)"
	}
	m.Emit(ctx, p.Actor(), "", "", "host.created", fmt.Sprintf("sandbox %s %s in %s (%s/h, per second) — %s", info.ID, sku, region, fmtUSD(h.PriceHour), how), nil)
	return h, nil
}

// autoPlace reports whether box_up may rent this kind without approval:
// burst servers need BOXMES_AUTO_BURST; sandboxes also run with
// BOXMES_AUTO_SANDBOX (per-second billing, destroyed with their box).
func (m *Manager) autoPlace(kind string) bool {
	if kind == selector.KindSandbox {
		return m.Cfg.AutoBurst || m.Cfg.AutoSandbox
	}
	return m.Cfg.AutoBurst
}

// sandboxETA estimates create → box usable, in seconds.
func (m *Manager) sandboxETA(ctx context.Context, info sandbox.Info) float64 {
	boot := float64(info.Caps.BootMS) / 1000
	if boot <= 0 {
		boot = 2
	}
	if m.sandboxMode(ctx, info) == "dial" {
		return boot + 6 // download boxd + dial + box create on the local driver
	}
	return boot + 1.5 // shim: mkdir + AGENTS.md via the exec API
}

// decorateSandboxOption turns a burst option into a T2/T3 sandbox option.
func (m *Manager) decorateSandboxOption(ctx context.Context, o *selector.Option, sp sandbox.Provider, measuredS float64) {
	info := sp.Info()
	o.Kind, o.Isolation, o.MinBillingHours = selector.KindSandbox, "sandbox", 0
	eta := m.sandboxETA(ctx, info)
	if measuredS > 0 {
		eta = measuredS
	}
	o.ETASeconds = round1(eta)
	o.Tier = info.Tier
	if m.sandboxMode(ctx, info) == "shim" {
		o.Tier = "T3"
	}
	o.Fork, o.Pause, o.Ports, o.GPU = info.Caps.Fork, info.Caps.Pause, info.Caps.Ports, info.Caps.GPU
	if info.DialOnly && info.Caps.MaxLifetimeS == 0 {
		// a long-lived service running boxd: no lifetime cap, sleep works like
		// on a pool host (boxd stops the box's processes, the service keeps
		// running and billing), so persistent boxes fit
		o.Pause = true
		o.Notes = append(o.Notes, info.Name+": long-lived service; sleep stops the box's processes but the service keeps running (and billing on paid plans)")
	}
	o.Verification, o.BootMS = info.Verification, info.Caps.BootMS
	switch info.Caps.Isolation {
	case "microvm", "vm", "gvisor":
		o.StrongIsolation = true
	}
	if info.Caps.MaxLifetimeS > 0 {
		o.MaxLifetimeH = model.Round4(float64(info.Caps.MaxLifetimeS) / 3600)
	}
	if info.DialOnly && m.Cfg.PublicURL == "" && o.Blocked == "" {
		o.Blocked = "needs BOXMES_PUBLIC_URL: this provider has no exec API, boxd must dial in"
	}
	note := fmt.Sprintf("%s sandbox (%s, %s): per-second billing", info.Name, info.Caps.Isolation, o.Tier)
	if o.Tier == "T3" {
		note += "; exec-only shim: no terminal or egress rules (snapshots are tarballs kept by the gateway)"
	}
	o.Notes = append(o.Notes, note)
	if info.Pricing.Confidence != "docs" {
		o.Notes = append(o.Notes, "price is an estimate (provider does not publish unit rates)")
	}
}

// ---- API: GET /v1/sandbox/providers ----

// SandboxProviderView is one sandbox provider with its live state.
type SandboxProviderView struct {
	sandbox.Info
	Mode      string  `json:"mode"` // dial (T2) | shim (T3)
	Connected bool    `json:"connected"`
	Usable    bool    `json:"usable"`
	Blocked   string  `json:"blocked,omitempty"`
	Health    string  `json:"health"`
	FromUSDH  float64 `json:"from_usd_h"` // cheapest catalog size per hour
	ChainRank int     `json:"chain_rank,omitempty"`
	// v1.7 lifecycle capability flags
	Lifecycle model.LifecycleCaps `json:"lifecycle"`
}

// SandboxProviders lists every sandbox provider (adapter metadata + state).
func (m *Manager) SandboxProviders(ctx context.Context, p *Principal) ([]SandboxProviderView, error) {
	if err := p.Require(model.ScopeBoxCreate); err != nil {
		return nil, err
	}
	out := []SandboxProviderView{}
	for _, pr := range m.Providers.All() {
		sp, ok := pr.(sandbox.Provider)
		if !ok {
			continue
		}
		info := sp.Info()
		v := SandboxProviderView{Info: info, Mode: m.sandboxMode(ctx, info), Health: m.Health.Stats(info.ID, "").Status, Lifecycle: m.LifecycleFor(info.ID)}
		if mp, err := m.Store.GetProvider(ctx, info.ID); err == nil {
			v.Connected = m.provConnected(ctx, mp)
		}
		okH, why := m.Health.Peek(info.ID)
		pp := m.ProviderPrefs(ctx, info.ID)
		switch {
		case !v.Connected:
			v.Blocked = "not connected"
		case !pp.Enabled:
			v.Blocked = "disabled in preferences"
		case !okH:
			v.Blocked = why
		}
		if info.DialOnly && m.Cfg.PublicURL == "" {
			v.Blocked = "needs BOXMES_PUBLIC_URL (no exec API: boxd must dial in)"
		}
		v.Usable = v.Blocked == ""
		for i, z := range info.Sizes {
			ph := info.SizePrice(z)
			if i == 0 || ph < v.FromUSDH {
				v.FromUSDH = ph
			}
		}
		if r := m.chainRank(info.ID); r < len(m.chain()) {
			v.ChainRank = r + 1
		}
		if v.Sizes == nil {
			v.Sizes = []sandbox.Size{}
		}
		if v.Regions == nil {
			v.Regions = []sandbox.Region{}
		}
		if v.Templates == nil {
			v.Templates = []string{}
		}
		if v.Notes == nil {
			v.Notes = []string{}
		}
		out = append(out, v)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Adapter != out[j].Adapter {
			return out[i].Adapter == "native"
		}
		return out[i].FromUSDH < out[j].FromUSDH
	})
	return out, nil
}

// fillProviderMeta adds integration metadata to a provider row.
func (m *Manager) fillProviderMeta(mp *model.Provider) {
	pr, ok := m.Providers.Get(mp.ID)
	if !ok {
		return
	}
	if sp, ok := pr.(sandbox.Provider); ok {
		info := sp.Info()
		mp.Group, mp.Adapter, mp.Verification, mp.Docs = "sandbox", info.Adapter, info.Verification, info.Docs
		mp.Tier = info.Tier
		return
	}
	mp.Group, mp.Adapter = "vps", "api"
	if mp.ID == "local" {
		mp.Adapter = "self"
	}
}
