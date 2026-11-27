package manager

// Deploying apps on sandbox boxes (T2/T3): public preview URLs for ports,
// T3 snapshots (gateway tarballs) restorable/forkable across sandboxes, and
// clone of a sandbox box via the provider's native fork (E2B) or snapshot →
// new sandbox (providers without fork, e.g. Novita).

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"boxmes/core/internal/model"
	"boxmes/core/internal/proto"
	"boxmes/core/internal/sandbox"
	"boxmes/core/internal/secret"
	"boxmes/core/internal/store"
)

// newShim builds a T3 shim whose snapshots land in core's snapshot dir.
func (m *Manager) newShim(sp sandbox.Provider, c sandbox.Cred, ref string) *sandbox.Shim {
	s := sandbox.NewShim(sp.Driver(), c, ref)
	s.SnapDir = m.snapshotDir()
	return s
}

func (m *Manager) snapshotDir() string {
	if m.Cfg.SnapshotDir != "" {
		return m.Cfg.SnapshotDir
	}
	return filepath.Join(os.TempDir(), "sbx-snapshots")
}

// sandboxPreview is the provider's public URL for a port of a sandbox host
// ("" when the provider has none, e.g. Prized).
func (m *Manager) sandboxPreview(ctx context.Context, h *model.Host, port int) string {
	if h == nil || h.Billing != "sandbox" || h.ProviderRef == "" || port <= 0 {
		return ""
	}
	sp, ok := m.sandboxProv(h.Provider)
	if !ok || !sp.Info().Caps.Ports {
		return ""
	}
	pv, ok := sp.Driver().(sandbox.Previewer)
	if !ok {
		return ""
	}
	tok, err := m.providerToken(ctx, h.Provider)
	if err != nil {
		return ""
	}
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	u, err := pv.PreviewURL(cctx, sandbox.ParseCred(tok), h.ProviderRef, port)
	if err != nil {
		return ""
	}
	return u
}

// PortView is one port of a box and how to reach it from the internet.
type PortView struct {
	Port   int    `json:"port"`
	URL    string `json:"url"`
	Kind   string `json:"kind"`   // provider-preview | ingress | tunnel | none
	Public bool   `json:"public"` // reachable without a Boxmes token
	Note   string `json:"note,omitempty"`
	// Caveats explain the limits of the URL's kind and why earlier steps of
	// the fallback chain (provider preview → ingress → tunnel) were skipped.
	Caveats []string `json:"caveats,omitempty"`
	// v1.7: the stable Boxmes gateway URL of this port (wake on request, URL
	// tokens, WebSocket/SSE pass-through) and its access mode.
	GatewayURL string `json:"gateway_url,omitempty"`
	Access     string `json:"access"` // public | token
	// v1.8: how the provider's own preview host treats this port: "private"
	// (refuses traffic that does not come through the gateway), "public"
	// (reachable directly by anyone who knows the sandbox id) or "" (the
	// provider has no preview host / not a sandbox box).
	ProviderPreview string `json:"provider_preview,omitempty"`
	// v1.9: readable addresses. Label names the port (web, api, db, …);
	// Primary marks the port served at the bare box name; AliasURL is the
	// legacy <key>-<port> gateway URL, which keeps working.
	Label    string `json:"label,omitempty"`
	Primary  bool   `json:"primary,omitempty"`
	AliasURL string `json:"alias_url,omitempty"`
}

const caveatPreviewPublic = "the provider preview URL of this port stays reachable at the provider by anyone who knows the sandbox id (this provider cannot refuse public preview traffic, or the sandbox was created public): the token protects only the Boxmes URL"

// hasPreviewHost reports whether a sandbox provider serves ports on its own
// preview host.
func (m *Manager) hasPreviewHost(provider string) bool {
	sp, ok := m.sandboxProv(provider)
	if !ok || !sp.Info().Caps.Ports {
		return false
	}
	_, ok = sp.Driver().(sandbox.Previewer)
	return ok
}

// PortsResult is GET/POST /v1/boxes/{id}/ports.
type PortsResult struct {
	BoxID string `json:"box_id"`
	URL   string `json:"url"` // the box URL (first port)
	// v1.9: the box's gateway name and the port its bare name serves
	Name        string     `json:"name,omitempty"`
	PrimaryPort int        `json:"primary_port,omitempty"`
	Ports       []PortView `json:"ports"`
}

func (m *Manager) portView(ctx context.Context, b *model.Box, h *model.Host, port int) PortView {
	v := m.portViewChain(ctx, b, h, port)
	v.Access = b.AccessFor(port)
	v.GatewayURL = m.GatewayURL(b, port)
	v.Label, v.Primary = PortLabel(b, port), port == PrimaryPort(b)
	if alias := m.GatewayAliasURL(b, port); alias != v.GatewayURL {
		v.AliasURL = alias
	}
	if h != nil && h.Billing == "sandbox" && m.hasPreviewHost(h.Provider) {
		v.ProviderPreview = "public"
		if h.PrivateTraffic {
			v.ProviderPreview = "private"
		}
	}
	if v.Access == model.AccessToken {
		// token ports are only handed out through the gateway: never the
		// provider preview / tunnel URL, which would bypass the token
		gw := v.GatewayURL
		if gw == "" {
			gw = m.GatewayPath(b, port)
		}
		cav := []string{caveatGateway}
		if v.ProviderPreview == "public" {
			cav = append(cav, caveatPreviewPublic)
		}
		return PortView{Port: port, URL: gw, Kind: KindGateway, Public: false, GatewayURL: v.GatewayURL, Access: v.Access, ProviderPreview: v.ProviderPreview,
			Label: v.Label, Primary: v.Primary, AliasURL: v.AliasURL,
			Note:    "token required: open with ?token=<bxt_…> (exchanged for a cookie), Authorization: Bearer <token> or X-Boxmes-Token; issue one with POST /v1/boxes/{id}/ports/{port}/tokens (box url --token)",
			Caveats: cav}
	}
	return v
}

func (m *Manager) portViewChain(ctx context.Context, b *model.Box, h *model.Host, port int) PortView {
	v := PortView{Port: port, Kind: "none"}
	if h != nil && h.Billing == "sandbox" {
		if h.PrivateTraffic {
			// the provider refuses direct preview traffic: the gateway (which
			// adds the sandbox traffic token upstream) is the only way in
			v.URL, v.Kind, v.Public = m.GatewayURL(b, port), KindGateway, true
			v.Note = h.Provider + " preview host is private (refuses requests without the sandbox traffic token); served through the Boxmes gateway, which wakes a sleeping box"
			v.Caveats = []string{caveatGateway}
			return v
		}
		if u := m.sandboxPreview(ctx, h, port); u != "" {
			v.URL, v.Kind, v.Public = u+"/", KindPreview, true
			v.Note = h.Provider + " preview URL: serve on 0.0.0.0:" + strconv.Itoa(port) + "; works while the box runs (sleeping boxes answer 502 until woken)"
			return v
		}
		// no provider preview: Boxmes ingress, then an in-box tunnel
		v = m.ensurePublic(ctx, b, port, false)
		if v.Kind == "ingress" {
			// same path through core, but the readable gateway URL (v1.9)
			v.URL = m.GatewayURL(b, port)
		}
		if v.Kind == "ingress" && m.GatewayDomain(ctx) != "" {
			for i, c := range v.Caveats {
				if c == caveatIngress {
					v.Caveats[i] = caveatHostGateway
				}
			}
		}
		v.Caveats = append([]string{h.Provider + " has no public preview URLs"}, v.Caveats...)
		return v
	}
	if len(b.Ports) > 0 && b.Ports[0] == port && b.URL != "" {
		v.URL, v.Kind, v.Public = b.URL, "ingress", !m.Settings(ctx).URLTokenProtect
		return v
	}
	v.Note = "Boxmes ingress serves the first declared port; create the box with this port first in ports"
	return v
}

// Ports lists every declared port of a box with its public URL.
func (m *Manager) Ports(ctx context.Context, p *Principal, id string) (*PortsResult, error) {
	b, err := m.GetBox(ctx, p, id)
	if err != nil {
		return nil, err
	}
	b = m.decorateBox(ctx, m.ensureNamesStored(ctx, b))
	h, _ := m.Store.GetHost(ctx, b.HostID)
	out := &PortsResult{BoxID: b.ID, URL: b.URL, Name: b.Subdomain, PrimaryPort: PrimaryPort(b), Ports: []PortView{}}
	for _, port := range b.Ports {
		out.Ports = append(out.Ports, m.portView(ctx, b, h, port))
	}
	return out, nil
}

// ExposeInput is POST /v1/boxes/{id}/ports.
type ExposeInput struct {
	Port int `json:"port"`
	// Label (v1.9) names the port in its gateway URL: <box>-<label>.<domain>
	// (default: a well-known label such as web, api, db, else a word).
	Label string `json:"label,omitempty"`
}

// Expose declares a port on a live box and returns its public URL (sandbox
// boxes: the provider's preview host, e.g. https://<port>-<id>.e2b.app).
func (m *Manager) Expose(ctx context.Context, p *Principal, id string, in ExposeInput) (*PortsResult, error) {
	if err := p.Require(model.ScopeBoxExec); err != nil {
		return nil, err
	}
	if in.Port < 1 || in.Port > 65535 {
		return nil, model.EH(model.ErrInvalid, "Pass the port your server listens on, e.g. 8000.", "port must be 1-65535")
	}
	b, err := m.GetBox(ctx, p, id)
	if err != nil {
		return nil, err
	}
	lock := m.boxLock(b.ID)
	lock.Lock()
	b, err = m.Store.GetBox(ctx, b.ID)
	if err != nil {
		lock.Unlock()
		return nil, err
	}
	h, _ := m.Store.GetHost(ctx, b.HostID)
	found := false
	for _, x := range b.Ports {
		found = found || x == in.Port
	}
	if !found {
		b.Ports = append(nonNilInts(b.Ports), in.Port)
	}
	labelChanged := false
	if in.Label != "" && in.Label != b.PortLabels[in.Port] {
		before := namesCopy(b)
		if err := m.setPortLabel(ctx, b, in.Port, in.Label); err != nil {
			lock.Unlock()
			return nil, err
		}
		m.refreshNamedURLs(b, before)
		labelChanged = true
	}
	m.ensureNames(ctx, b)
	if b.URL == "" && h != nil {
		b.URL = m.boxURL(ctx, b, h)
	}
	m.Store.PutBox(ctx, b)
	lock.Unlock()
	if !found || labelChanged {
		m.Emit(ctx, p.Actor(), b.ID, b.AgentID, "box.port", fmt.Sprintf("port %d exposed as %s", in.Port, m.GatewayURL(b, in.Port)), nil)
		m.publishBox(m.decorateBox(ctx, b))
	}
	res, err := m.Ports(ctx, p, b.ID)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(res.Ports, func(i, j int) bool { return res.Ports[i].Port == in.Port && res.Ports[j].Port != in.Port })
	return res, nil
}

// shimHost reports whether a host is a T3 shim (its snapshots live at core).
func (m *Manager) shimHost(ctx context.Context, id string) (*model.Host, bool) {
	if id == "" {
		return nil, false
	}
	h, err := m.Store.GetHost(ctx, id)
	if err != nil || h == nil {
		return nil, false
	}
	return h, h.Driver == "shim"
}

// newSandboxBoxHost creates a fresh sandbox on provider prov for a new box
// (fork/clone targets), with the provider's own size and region.
func (m *Manager) newSandboxBoxHost(ctx context.Context, p *Principal, like *model.Host) (*model.Host, error) {
	sp, ok := m.sandboxProv(like.Provider)
	if !ok {
		return nil, model.E(model.ErrInvalid, "provider %s is not a sandbox provider", like.Provider)
	}
	tok, err := m.providerToken(ctx, like.Provider)
	if err != nil {
		return nil, err
	}
	return m.createSandboxHost(ctx, p, sp, tok, like.SKU, like.Region)
}

// adoptSandbox registers a sandbox the provider already created (a native
// fork) as a single-box T3 host.
func (m *Manager) adoptSandbox(ctx context.Context, p *Principal, like *model.Host, ref string) (*model.Host, error) {
	sp, ok := m.sandboxProv(like.Provider)
	if !ok {
		return nil, model.E(model.ErrInvalid, "provider %s is not a sandbox provider", like.Provider)
	}
	tok, err := m.providerToken(ctx, like.Provider)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	h := &model.Host{ID: secret.ID("host"), Provider: like.Provider, Region: like.Region, SKU: like.SKU, Status: "ready", Billing: "sandbox",
		CreatedAt: now, Driver: "shim", ProviderRef: ref, Connected: true, LastSeenAt: &now,
		CPU: like.CPU, RAMMB: like.RAMMB, DiskGB: like.DiskGB, PriceHour: like.PriceHour, PriceMonth: like.PriceMonth}
	if err := m.Store.PutHost(ctx, h); err != nil {
		return nil, err
	}
	m.Hub.RegisterVirtual(h.ID, m.newShim(sp, sandbox.ParseCred(tok), ref))
	m.Emit(ctx, p.Actor(), "", "", "host.created", fmt.Sprintf("sandbox %s %s forked natively at %s (%s/h, per second) — T3 shim", like.Provider, ref, like.Provider, fmtUSD(h.PriceHour)), nil)
	return h, nil
}

// cloneSandbox clones a box that lives on a sandbox host onto a new sandbox:
// provider-native fork when the provider has one (E2B: memory + disk, running
// processes included), otherwise snapshot tarball → fresh sandbox → restore.
func (m *Manager) cloneSandbox(ctx context.Context, p *Principal, src *model.Box, srcHost *model.Host, agent *model.Agent, slug string) (*model.Box, error) {
	if srcHost.Driver != "shim" {
		return nil, model.EH(model.ErrInvalid, "Clone of a T2 (boxd dial-in) sandbox is not supported; switch the provider to sandbox_mode=shim or use box_fork with object storage.", "cannot clone a dial-in sandbox box")
	}
	sp, _ := m.sandboxProv(srcHost.Provider)
	now := time.Now().UTC()
	nb := &model.Box{ID: secret.ID("box"), Slug: slug, AgentID: src.AgentID, AgentName: src.AgentName, Status: model.StatusProvisioning,
		Lifecycle: src.Lifecycle, Template: src.Template, Spec: src.Spec, Provider: src.Provider, Region: src.Region,
		TTLSeconds: src.TTLSeconds, IdleMinutes: src.IdleMinutes, BudgetUSD: src.BudgetUSD, CostPerHour: src.CostPerHour, Billing: "sandbox",
		Ports: nonNilInts(src.Ports), Secrets: nonNilStrs(src.Secrets), CreatedAt: now, LastActiveAt: now, Locale: src.Locale,
		Profile: src.Profile, Requirements: src.Requirements, Isolation: src.Isolation, ClonedFrom: src.ID}
	inheritV17(nb, src)
	m.ensureNames(ctx, nb)
	if err := m.Store.PutBox(ctx, nb); err != nil {
		return nil, err
	}
	fail := func(err error) (*model.Box, error) {
		nb.Error = err.Error()
		m.setStatus(ctx, nb, model.StatusFailed, p.Actor(), "clone failed: "+err.Error())
		if nb.HostID != "" {
			if h, e := m.Store.GetHost(ctx, nb.HostID); e == nil && h.Status != "deleted" {
				go m.destroyHost(context.Background(), h, p.Actor(), "clone failed")
			}
		}
		return nil, err
	}
	method := ""
	var h *model.Host
	if fk, ok := sp.Driver().(sandbox.Forker); ok && sp.Info().Caps.Fork {
		if src.Status == model.StatusSleeping {
			return fail(model.EH(model.ErrInvalid, "Wake the box first (box_sleep wake=true).", "native fork needs a running sandbox"))
		}
		tok, err := m.providerToken(ctx, srcHost.Provider)
		if err != nil {
			return fail(err)
		}
		cctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
		child, err := fk.Fork(cctx, sandbox.ParseCred(tok), srcHost.ProviderRef)
		cancel()
		if err != nil {
			return fail(model.EH(model.ErrProviderFailed, "Retry, or box_fork (snapshot based) instead.", "%s fork: %v", srcHost.Provider, err))
		}
		if h, err = m.adoptSandbox(ctx, p, srcHost, child.ID); err != nil {
			return fail(err)
		}
		if child.Meta[sandbox.MetaPrivateTraffic] == "1" || srcHost.PrivateTraffic {
			h.PrivateTraffic = true // a fork keeps the source's network config
			m.Store.PutHost(ctx, h)
		}
		nb.HostID = h.ID
		m.Store.PutBox(ctx, nb)
		// registers the box with the shim and writes its own AGENTS.md
		if err := m.Hub.Call(ctx, h.ID, proto.MCreate, proto.CreateParams{BoxID: nb.ID, AgentID: nb.AgentID, AgentsMD: m.agentsMD(ctx, nb, agent)}, nil); err != nil {
			return fail(mapHostErr(err))
		}
		// the fork copied the source's memory: its in-box ingress agent and
		// quick tunnels would dial in as the source box (and steal its
		// traffic); stop them in the child, the clone gets its own on demand
		m.Hub.Call(ctx, h.ID, proto.MExec, proto.ExecParams{BoxID: nb.ID, Cwd: "/", TimeoutS: 30, TailLines: 5,
			Command: `D="$HOME/.boxd"; for f in "$D"/ingress.pid "$D"/tunnel-*.pid; do [ -f "$f" ] && kill "$(cat "$f")" 2>/dev/null; rm -f "$f"; done; true`}, nil)
		method = srcHost.Provider + " native fork"
	} else {
		sn, err := m.snapshotBox(ctx, src, p.Actor())
		if err != nil {
			return fail(err)
		}
		if h, err = m.newSandboxBoxHost(ctx, p, srcHost); err != nil {
			return fail(err)
		}
		nb.HostID = h.ID
		m.Store.PutBox(ctx, nb)
		cctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
		err = m.Hub.Call(cctx, h.ID, proto.MRestore, proto.RestoreParams{BoxID: nb.ID, SourceBoxID: src.ID, SnapshotID: sn.ID, Location: sn.Location, Create: true,
			DownloadURL: m.presignSnapshot(ctx, sn, "GET"),
			Box:         proto.CreateParams{BoxID: nb.ID, Template: nb.Template, Spec: nb.Spec, AgentID: nb.AgentID, AgentsMD: m.agentsMD(ctx, nb, agent)}}, nil)
		cancel()
		if err != nil {
			return fail(mapHostErr(err))
		}
		method = "snapshot " + sn.ID + " → new " + h.Provider + " sandbox"
	}
	nb.Provider, nb.Region, nb.CostPerHour = h.Provider, h.Region, model.Round6(h.PriceHour)
	nb.URL = m.boxURL(ctx, nb, h)
	ms := time.Since(now).Milliseconds()
	nb.PlacementReason = fmt.Sprintf("clone of %s via %s (%d ms)", src.ID, method, ms)
	nb.Estimate = &model.BoxEstimate{Kind: "clone", CostPerHour: nb.CostPerHour, ExpectedHours: expectedHours(nb), ExpectedTotalUSD: model.Round4(nb.CostPerHour * expectedHours(nb)),
		ETASeconds: round3(float64(ms) / 1000), ActualSeconds: round3(float64(ms) / 1000)}
	m.setStatus(ctx, nb, model.StatusRunning, p.Actor(), fmt.Sprintf("cloned from %s via %s in %d ms", src.ID, method, ms))
	return m.decorateBox(ctx, nb), nil
}

// forkSandboxSnapshot forks a T3 snapshot (a tarball at core) onto a fresh
// sandbox of the snapshot's provider.
func (m *Manager) forkSandboxSnapshot(ctx context.Context, p *Principal, src *model.Box, sn *model.Snapshot, snapHost *model.Host, agent *model.Agent, slug string) (*model.Box, error) {
	q := m.Quotas(ctx)
	if existing, _ := m.Store.ListBoxes(ctx, store.BoxFilter{AgentID: agent.ID}); len(existing) >= q.MaxBoxesPerAgent {
		return nil, model.EH(model.ErrPolicyDenied, "Terminate unused boxes (box down) first.", "agent already has %d boxes (quota %d)", len(existing), q.MaxBoxesPerAgent)
	}
	now := time.Now().UTC()
	b := &model.Box{ID: secret.ID("box"), Slug: slug, AgentID: agent.ID, AgentName: agent.Name, Status: model.StatusProvisioning,
		Lifecycle: src.Lifecycle, Template: src.Template, Spec: src.Spec, Region: src.Region, TTLSeconds: src.TTLSeconds,
		IdleMinutes: src.IdleMinutes, BudgetUSD: src.BudgetUSD, Ports: nonNilInts(src.Ports), Secrets: nonNilStrs(src.Secrets),
		Setup: src.Setup, CreatedAt: now, LastActiveAt: now, Locale: src.Locale, ForkedFrom: sn.ID, Billing: "sandbox", Provider: snapHost.Provider}
	inheritV17(b, src)
	m.ensureNames(ctx, b)
	if err := m.Store.PutBox(ctx, b); err != nil {
		return nil, err
	}
	m.Emit(ctx, p.Actor(), b.ID, agent.ID, "box.requested", fmt.Sprintf("box %s forked from %s (snapshot %s) onto a new %s sandbox", b.Slug, src.Slug, sn.ID, snapHost.Provider), nil)
	h, err := m.newSandboxBoxHost(ctx, p, snapHost)
	if err != nil {
		b.Error = err.Error()
		m.setStatus(ctx, b, model.StatusFailed, "system", b.Error)
		return nil, err
	}
	b.HostID, b.Provider, b.Region, b.CostPerHour = h.ID, h.Provider, h.Region, model.Round6(h.PriceHour)
	b.PlacementReason = "fork of " + src.ID + " from snapshot " + sn.ID + " onto a new " + h.Provider + " sandbox"
	m.Store.PutBox(ctx, b)
	cctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	err = m.Hub.Call(cctx, h.ID, proto.MRestore, proto.RestoreParams{BoxID: b.ID, SourceBoxID: src.ID, SnapshotID: sn.ID, Location: sn.Location, Create: true,
		DownloadURL: m.presignSnapshot(ctx, sn, "GET"),
		Box:         proto.CreateParams{BoxID: b.ID, Template: b.Template, Spec: b.Spec, AgentID: b.AgentID, AgentsMD: m.agentsMD(ctx, b, agent)}}, nil)
	cancel()
	if err != nil {
		b.Error = err.Error()
		m.setStatus(ctx, b, model.StatusFailed, "system", b.Error)
		go m.destroyHost(context.Background(), h, p.Actor(), "fork failed")
		return nil, mapHostErr(err)
	}
	b.URL = m.boxURL(ctx, b, h)
	b.LastActiveAt = time.Now().UTC()
	m.setStatus(ctx, b, model.StatusRunning, "system", fmt.Sprintf("forked from snapshot %s on %s in %d ms", sn.ID, h.ID, time.Since(now).Milliseconds()))
	return m.decorateBox(ctx, b), nil
}
