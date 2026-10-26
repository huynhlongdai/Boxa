package manager

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"boxa/core/internal/events"
	"boxa/core/internal/model"
	"boxa/core/internal/proto"
	"boxa/core/internal/provider"
	"boxa/core/internal/secret"
	"boxa/core/internal/store"
)

type hostAuth struct {
	global    bool
	hostID    string
	bootstrap bool
}

// AuthHost implements hostlink.Registrar.
func (m *Manager) AuthHost(ctx context.Context, token string) (any, error) {
	if token == "" {
		return nil, errors.New("no token")
	}
	if m.Cfg.BootstrapToken != "" && token == m.Cfg.BootstrapToken {
		return hostAuth{global: true}, nil
	}
	h := secret.Hash(token)
	if host, err := m.Store.GetHostByToken(ctx, h); err == nil {
		return hostAuth{hostID: host.ID}, nil
	}
	if host, err := m.Store.GetHostByBootstrap(ctx, h); err == nil {
		return hostAuth{hostID: host.ID, bootstrap: true}, nil
	}
	return nil, errors.New("unknown host token")
}

// RegisterHost implements hostlink.Registrar.
func (m *Manager) RegisterHost(ctx context.Context, a any, hello proto.Hello) (proto.Welcome, error) {
	auth := a.(hostAuth)
	var host *model.Host
	issue := false
	now := time.Now().UTC()
	switch {
	case auth.global:
		if hello.HostID != "" {
			if h, err := m.Store.GetHost(ctx, hello.HostID); err == nil {
				host = h
			}
		}
		if host == nil {
			prov := hello.Provider
			if prov == "" {
				prov = "local"
			}
			host = &model.Host{ID: secret.ID("host"), Provider: prov, Region: hello.Region, SKU: "self", Billing: "pool", CreatedAt: now}
			if prov == "local" {
				host.PriceMonth = m.Cfg.LocalPriceMonth
				host.PriceHour = model.Round6(m.Cfg.LocalPriceMonth / 730)
			}
			m.Emit(ctx, "boxd", "", "", "host.registered", fmt.Sprintf("host %s (%s) registered via bootstrap token", host.ID, hello.Hostname), nil)
		}
		issue = true
	case auth.bootstrap:
		h, err := m.Store.GetHost(ctx, auth.hostID)
		if err != nil {
			return proto.Welcome{}, err
		}
		host = h
		host.BootstrapHash = "" // one-time
		issue = true
	default:
		h, err := m.Store.GetHost(ctx, auth.hostID)
		if err != nil {
			return proto.Welcome{}, err
		}
		host = h
	}
	w := proto.Welcome{HostID: host.ID}
	if issue {
		tok := "bxh_" + secret.RandHex(24)
		host.TokenHash = secret.Hash(tok)
		w.HostToken = tok
	}
	if hello.CPU > 0 {
		host.CPU, host.RAMMB, host.DiskGB = hello.CPU, hello.RAMMB, hello.DiskGB
	}
	if hello.IP != "" {
		host.IP = hello.IP
	}
	if hello.Region != "" && host.Region == "" {
		host.Region = hello.Region
	}
	if host.Region == "" {
		host.Region = "local"
	}
	host.Driver = hello.Driver
	wasDown := host.Status == "down"
	if host.Status != "draining" {
		host.Status = "ready"
	}
	host.Connected = true
	host.LastSeenAt = &now
	if err := m.Store.PutHost(ctx, host); err != nil {
		return proto.Welcome{}, err
	}
	if wasDown {
		m.Emit(ctx, "boxd", "", "", "host.up", "host "+host.ID+" is back", nil)
	}
	go m.reconcileHost(host.ID, hello.Boxes)
	return w, nil
}

// reconcileHost compares boxes reported by boxd with the DB.
func (m *Manager) reconcileHost(hostID string, present []string) {
	ctx := context.Background()
	have := map[string]bool{}
	for _, id := range present {
		have[id] = true
	}
	boxes, _ := m.Store.ListBoxes(ctx, store.BoxFilter{HostID: hostID, Statuses: []string{model.StatusRunning, model.StatusSleeping}})
	for _, b := range boxes {
		if !have[b.ID] {
			b.Status, b.Error = model.StatusFailed, "box missing on host after reconnect"
			m.Store.PutBox(ctx, b)
			m.publishBox(b)
			m.Emit(ctx, "system", b.ID, b.AgentID, "box.failed", "box missing on host "+hostID+" after reconnect", nil)
		}
	}
	for id := range have {
		b, err := m.Store.GetBox(ctx, id)
		if err == nil && b.Status == model.StatusTerminated {
			_ = m.Hub.Call(ctx, hostID, proto.MDelete, proto.BoxRef{BoxID: id}, nil)
		}
	}
}

func (m *Manager) HostMetrics(hostID string, mt proto.Metrics) {
	ctx := context.Background()
	h, err := m.Store.GetHost(ctx, hostID)
	if err != nil {
		return
	}
	now := time.Now().UTC()
	h.CPUUsedPct, h.RAMUsedPct, h.DiskUsedPct = mt.CPUUsedPct, mt.RAMUsedPct, mt.DiskUsedPct
	h.LastSeenAt = &now
	h.Connected = true
	m.Store.PutHost(ctx, h)
}

func (m *Manager) HostDisconnected(hostID string) {
	ctx := context.Background()
	if h, err := m.Store.GetHost(ctx, hostID); err == nil {
		h.Connected = false
		m.Store.PutHost(ctx, h)
		m.Emit(ctx, "system", "", "", "host.disconnected", "host "+hostID+" tunnel closed", nil)
	}
}

// ListHosts decorates hosts with live box counts.
func (m *Manager) ListHosts(ctx context.Context) ([]*model.Host, error) {
	hosts, err := m.Store.ListHosts(ctx)
	if err != nil {
		return nil, err
	}
	boxes, _ := m.Store.ListBoxes(ctx, store.BoxFilter{})
	count := map[string]int{}
	for _, b := range boxes {
		if b.Active() {
			count[b.HostID]++
		}
	}
	out := []*model.Host{}
	for _, h := range hosts {
		if h.Status == "deleted" {
			continue
		}
		h.BoxCount = count[h.ID]
		h.Connected = m.Hub.Connected(h.ID)
		out = append(out, h)
	}
	return out, nil
}

// CreateHost rents a new T1 host from a provider (spends money; admin only).
func (m *Manager) CreateHost(ctx context.Context, p *Principal, provID, sku, region, billing string) (*model.Host, error) {
	if err := p.Require(model.ScopeAdmin); err != nil {
		return nil, err
	}
	prov, ok := m.Providers.Get(provID)
	if !ok {
		return nil, notFound("provider", provID)
	}
	if provID == "local" {
		return m.newLocalHostToken(ctx, p, region)
	}
	if !prov.Implemented() {
		return nil, model.EH(model.ErrProviderFailed, "Use hetzner or vultr; this adapter is planned for P3.", "%s adapter not implemented", provID)
	}
	token, err := m.providerToken(ctx, provID)
	if err != nil {
		return nil, err
	}
	if m.Cfg.PublicURL == "" {
		return nil, model.EH(model.ErrInvalid, "Set BOXA_PUBLIC_URL to the https URL where boxd can reach core.", "BOXA_PUBLIC_URL is not configured")
	}
	offers, _ := m.Store.ListOffers(ctx, provID)
	var off *model.Offer
	for i := range offers {
		o := offers[i]
		if o.SKU == sku && o.Region == region {
			if off == nil || o.Confidence == "verified" {
				off = &o
			}
		}
	}
	if billing == "" {
		billing = "pool"
	}
	boot := "bxb_" + secret.RandHex(24)
	h := &model.Host{ID: secret.ID("host"), Provider: provID, Region: region, SKU: sku, Status: "provisioning", Billing: billing, CreatedAt: time.Now().UTC(), BootstrapHash: secret.Hash(boot)}
	if off != nil {
		h.CPU, h.RAMMB, h.DiskGB, h.PriceHour, h.PriceMonth = off.CPU, off.RAMMB, off.DiskGB, off.PriceHour, off.PriceMonth
	}
	ci, err := provider.RenderCloudInit(provider.CloudInitParams{
		CoreURL: wsURL(m.Cfg.PublicURL), BootstrapToken: boot, Provider: provID, Region: region, BoxdURL: m.boxdURL(),
	})
	if err != nil {
		return nil, err
	}
	if err := m.Store.PutHost(ctx, h); err != nil {
		return nil, err
	}
	hd, err := prov.Create(ctx, token, provider.CreateRequest{Name: "boxa-" + h.ID[5:], SKU: sku, Region: region, CloudInit: ci, Labels: map[string]string{"boxa": "1", "boxa-host": h.ID[5:]}})
	if err != nil {
		h.Status = "down"
		m.Store.PutHost(ctx, h)
		m.Emit(ctx, p.Actor(), "", "", "host.create_failed", fmt.Sprintf("%s %s %s: %v", provID, sku, region, err), nil)
		return nil, err
	}
	h.ProviderRef, h.IP = hd.Ref, hd.IP
	m.Store.PutHost(ctx, h)
	m.Emit(ctx, p.Actor(), "", "", "host.created", fmt.Sprintf("rented %s %s in %s (%s/h), waiting for boxd", provID, sku, region, fmtUSD(h.PriceHour)), nil)
	return h, nil
}

// newLocalHostToken pre-registers a self-hosted host and returns a one-time
// bootstrap token in the host's provider_ref field (shown once).
func (m *Manager) newLocalHostToken(ctx context.Context, p *Principal, region string) (*model.Host, error) {
	boot := "bxb_" + secret.RandHex(24)
	if region == "" {
		region = "local"
	}
	h := &model.Host{ID: secret.ID("host"), Provider: "local", Region: region, SKU: "self", Status: "provisioning", Billing: "pool", CreatedAt: time.Now().UTC(), BootstrapHash: secret.Hash(boot),
		PriceMonth: m.Cfg.LocalPriceMonth, PriceHour: model.Round6(m.Cfg.LocalPriceMonth / 730)}
	if err := m.Store.PutHost(ctx, h); err != nil {
		return nil, err
	}
	m.Emit(ctx, p.Actor(), "", "", "host.created", "local host "+h.ID+" pre-registered; run boxd with the bootstrap token", nil)
	c := *h
	c.ProviderRef = "BOXD_TOKEN=" + boot
	return &c, nil
}

func wsURL(public string) string {
	u := public
	switch {
	case len(u) > 8 && u[:8] == "https://":
		u = "wss://" + u[8:]
	case len(u) > 7 && u[:7] == "http://":
		u = "ws://" + u[7:]
	}
	return u + "/v1/hosts/connect"
}

func (m *Manager) boxdURL() string {
	if m.Cfg.BoxdDownloadURL != "" {
		return m.Cfg.BoxdDownloadURL
	}
	return m.Cfg.PublicURL + "/dl/boxd"
}

func (m *Manager) DrainHost(ctx context.Context, p *Principal, id string) (*model.Host, error) {
	if err := p.Require(model.ScopeAdmin); err != nil {
		return nil, err
	}
	h, err := m.Store.GetHost(ctx, id)
	if err != nil {
		return nil, notFound("host", id)
	}
	h.Status = "draining"
	if err := m.Store.PutHost(ctx, h); err != nil {
		return nil, err
	}
	m.Emit(ctx, p.Actor(), "", "", "host.draining", "host "+id+" draining: no new boxes", nil)
	return h, nil
}

// DeleteHost destroys the server at the provider. Refuses while boxes are active unless force.
func (m *Manager) DeleteHost(ctx context.Context, p *Principal, id string, force bool) error {
	if err := p.Require(model.ScopeAdmin); err != nil {
		return err
	}
	h, err := m.Store.GetHost(ctx, id)
	if err != nil {
		return notFound("host", id)
	}
	boxes, _ := m.Store.ListBoxes(ctx, store.BoxFilter{HostID: id})
	active := 0
	for _, b := range boxes {
		if b.Active() {
			active++
		}
	}
	if active > 0 && !force {
		return model.EH(model.ErrInvalid, "Drain the host and terminate its boxes first, or pass ?force=true.", "host has %d active boxes", active)
	}
	for _, b := range boxes {
		if b.Active() {
			m.Terminate(ctx, p, b.ID)
		}
	}
	return m.destroyHost(ctx, h, p.Actor(), "deleted by operator")
}

func (m *Manager) destroyHost(ctx context.Context, h *model.Host, actor, why string) error {
	if h.Provider != "local" && h.ProviderRef != "" {
		prov, ok := m.Providers.Get(h.Provider)
		if ok {
			tok, err := m.providerToken(ctx, h.Provider)
			if err != nil {
				return err
			}
			if err := prov.Destroy(ctx, tok, h.ProviderRef); err != nil {
				m.Emit(ctx, actor, "", "", "host.destroy_failed", fmt.Sprintf("host %s: %v", h.ID, err), nil)
				return err
			}
		}
	}
	if p, ok := m.Hub.Peer(h.ID); ok {
		p.Close()
	}
	h.Status, h.Connected, h.TokenHash, h.BootstrapHash = "deleted", false, "", ""
	if err := m.Store.PutHost(ctx, h); err != nil {
		return err
	}
	m.Emit(ctx, actor, "", "", "host.deleted", "host "+h.ID+" destroyed: "+why, nil)
	m.Broker.Publish(events.Msg{Type: "overview.updated", Data: map[string]any{"reason": "host.deleted"}, Public: true})
	return nil
}

// waitHostReady blocks until a new host's boxd connects.
func (m *Manager) waitHostReady(ctx context.Context, id string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if m.Hub.Connected(id) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
	log.Printf("host %s did not connect within %s", id, timeout)
	return model.EH(model.ErrProviderFailed, "The new host did not come online; retry or check the provider console.", "host %s did not connect in %s", id, timeout)
}
