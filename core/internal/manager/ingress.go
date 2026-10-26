package manager

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"strings"
	"time"

	"boxa/core/internal/model"
	"boxa/core/internal/proto"
	"boxa/core/internal/store"
)

// Ingress (spec M9 / v0.3 §4): Caddy terminates TLS for <slug>-<ip>.sslip.io
// (or <slug>.box.<BASE_DOMAIN>) and forwards to core, which reverse-tunnels the
// request through boxd to the box port. Buffered (no streaming/WebSocket) in v1.

// URLToken is a stable per-box token derived from the URL key (HMAC).
func (m *Manager) URLToken(boxID string) string {
	h := hmac.New(sha256.New, []byte(m.Cfg.URLKey))
	h.Write([]byte("boxa-url:" + boxID))
	return hex.EncodeToString(h.Sum(nil))[:24]
}

// IsIngressHost reports whether host looks like a box hostname.
func (m *Manager) IsIngressHost(ctx context.Context, host string) bool {
	host = strings.ToLower(host)
	if pu, err := url.Parse(m.Cfg.PublicURL); err == nil && pu.Hostname() == host {
		return false
	}
	if strings.HasSuffix(host, ".sslip.io") {
		return true
	}
	bd := m.Settings(ctx).BaseDomain
	return bd != "" && bd != "sslip.io" && strings.HasSuffix(host, ".box."+bd)
}

// BoxForHost finds the active box whose URL host matches.
func (m *Manager) BoxForHost(ctx context.Context, host string) (*model.Box, error) {
	host = strings.ToLower(host)
	boxes, err := m.Store.ListBoxes(ctx, store.BoxFilter{Statuses: []string{model.StatusRunning, model.StatusSleeping}})
	if err != nil {
		return nil, err
	}
	for _, b := range boxes {
		if b.URL == "" {
			continue
		}
		if u, err := url.Parse(b.URL); err == nil && strings.EqualFold(u.Hostname(), host) {
			return b, nil
		}
	}
	return nil, model.E(model.ErrNotFound, "no box for host %s", host)
}

// TLSAllowed answers Caddy's on_demand_tls "ask".
func (m *Manager) TLSAllowed(ctx context.Context, host string) bool {
	if pu, err := url.Parse(m.Cfg.PublicURL); err == nil && strings.EqualFold(pu.Hostname(), host) {
		return true
	}
	_, err := m.BoxForHost(ctx, host)
	return err == nil
}

// Proxy forwards one HTTP request to the box's first exposed port.
func (m *Manager) Proxy(ctx context.Context, b *model.Box, method, path string, hdr map[string][]string, body []byte) (*proto.HTTPResult, error) {
	if b.Status == model.StatusSleeping {
		sys := &Principal{Key: &model.APIKey{Scopes: []string{model.ScopeAdmin}}, Admin: true}
		if _, err := m.Wake(ctx, sys, b.ID); err != nil {
			return nil, err
		}
	}
	if len(b.Ports) == 0 {
		return nil, model.E(model.ErrNotFound, "box exposes no ports")
	}
	m.touch(ctx, b)
	var res proto.HTTPResult
	cctx, cancel := context.WithTimeout(ctx, 70*time.Second)
	defer cancel()
	if err := m.Hub.Call(cctx, b.HostID, proto.MHTTP, proto.HTTPParams{BoxID: b.ID, Port: b.Ports[0], Method: method, Path: path, Headers: hdr, Body: body}, &res); err != nil {
		return nil, mapHostErr(err)
	}
	return &res, nil
}
