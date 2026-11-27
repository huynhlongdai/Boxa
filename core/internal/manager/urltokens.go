package manager

// v1.7 scoped URL tokens: issued by Boxmes for one port of one box, with an
// expiry, revocable, checked by the gateway on every request. Provider-wide
// tokens (sandbox traffic tokens etc.) are never handed out; the gateway is
// the only thing that talks to the provider.

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"boxmes/core/internal/model"
	"boxmes/core/internal/secret"
)

// URLTokenPrefix marks Boxmes URL tokens (lets the gateway tell them apart
// from an app's own Authorization header or ?token= parameter).
const URLTokenPrefix = "bxt_"

const (
	defaultTokenTTL = 24 * time.Hour
	maxTokenTTL     = 30 * 24 * time.Hour
	minTokenTTL     = time.Minute
)

func urlTokKey(boxID string) string { return "urltokens:" + boxID }

func hashToken(t string) string {
	h := sha256.Sum256([]byte(t))
	return hex.EncodeToString(h[:])
}

// CreateURLTokenInput is POST /v1/boxes/{id}/ports/{port}/tokens.
type CreateURLTokenInput struct {
	TTLSeconds int    `json:"ttl_s,omitempty"` // default 86400, 60 … 2592000
	Label      string `json:"label,omitempty"`
	// Private switches the port to access=token (default true): the URL then
	// needs a token. false issues a token without changing the port's access.
	Private *bool `json:"private,omitempty"`
}

// URLTokenList is GET /v1/boxes/{id}/ports/{port}/tokens.
type URLTokenList struct {
	BoxID      string            `json:"box_id"`
	Port       int               `json:"port"`
	Access     string            `json:"access"` // public | token
	GatewayURL string            `json:"gateway_url"`
	Items      []*model.URLToken `json:"items"`
}

// PortAccessInput is PUT /v1/boxes/{id}/ports/{port}.
type PortAccessInput struct {
	Access string `json:"access,omitempty"` // public | token
	Label  string `json:"label,omitempty"`  // v1.9: the port's URL label (<box>-<label>.<domain>)
}

func (m *Manager) loadTokens(ctx context.Context, boxID string) []*model.URLToken {
	var list []*model.URLToken
	m.Store.GetKV(ctx, urlTokKey(boxID), &list)
	return list
}

func (m *Manager) portBox(ctx context.Context, p *Principal, id string, port int) (*model.Box, error) {
	b, err := m.GetBox(ctx, p, id)
	if err != nil {
		return nil, err
	}
	for _, x := range b.Ports {
		if x == port {
			return b, nil
		}
	}
	return nil, model.EH(model.ErrNotFound, "Expose it first: POST /v1/boxes/{id}/ports {port}.", "port %d is not exposed on box %s", port, b.ID)
}

func publicToken(t *model.URLToken, now time.Time) *model.URLToken {
	c := *t
	c.Hash = ""
	c.Expired = now.After(c.ExpiresAt)
	return &c
}

// CreateURLToken issues a token for one port (the value is returned once).
func (m *Manager) CreateURLToken(ctx context.Context, p *Principal, id string, port int, in CreateURLTokenInput) (*model.URLToken, error) {
	if err := p.Require(model.ScopeBoxExec); err != nil {
		return nil, err
	}
	b, err := m.portBox(ctx, p, id, port)
	if err != nil {
		return nil, err
	}
	ttl := time.Duration(in.TTLSeconds) * time.Second
	if in.TTLSeconds == 0 {
		ttl = defaultTokenTTL
	}
	if ttl < minTokenTTL || ttl > maxTokenTTL {
		return nil, model.EH(model.ErrInvalid, "Use a ttl_s between 60 and 2592000 (30 days).", "ttl_s out of range")
	}
	if len(in.Label) > 80 {
		in.Label = in.Label[:80]
	}
	raw := URLTokenPrefix + secret.RandHex(24)
	now := time.Now().UTC()
	t := &model.URLToken{ID: secret.ID("utok"), BoxID: b.ID, Port: port, Label: strings.TrimSpace(in.Label), Prefix: raw[:10],
		ExpiresAt: now.Add(ttl), CreatedAt: now, Hash: hashToken(raw)}
	lock := m.boxLock(b.ID)
	lock.Lock()
	list := m.loadTokens(ctx, b.ID)
	// drop long-dead entries (expired or revoked more than a day ago)
	kept := list[:0]
	for _, x := range list {
		dead := now.After(x.ExpiresAt.Add(24*time.Hour)) || (x.RevokedAt != nil && now.After(x.RevokedAt.Add(24*time.Hour)))
		if !dead {
			kept = append(kept, x)
		}
	}
	kept = append(kept, t)
	err = m.Store.PutKV(ctx, urlTokKey(b.ID), kept)
	if err == nil && (in.Private == nil || *in.Private) {
		if cur, e := m.Store.GetBox(ctx, b.ID); e == nil {
			if cur.PortAccess == nil {
				cur.PortAccess = map[int]string{}
			}
			cur.PortAccess[port] = model.AccessToken
			m.Store.PutBox(ctx, cur)
			b = cur
		}
	}
	lock.Unlock()
	if err != nil {
		return nil, err
	}
	m.Emit(ctx, p.Actor(), b.ID, b.AgentID, "box.url_token", fmt.Sprintf("URL token %s issued for port %d (expires %s)", t.Prefix+"…", port, t.ExpiresAt.Format(time.RFC3339)), nil)
	out := publicToken(t, now)
	out.Token = raw
	if gu := m.GatewayURL(b, port); gu != "" {
		out.URL = gu + "?token=" + raw
	}
	m.publishBox(m.decorateBox(ctx, b))
	return out, nil
}

// ListURLTokens lists a port's tokens (never their values).
func (m *Manager) ListURLTokens(ctx context.Context, p *Principal, id string, port int) (*URLTokenList, error) {
	b, err := m.portBox(ctx, p, id, port)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	out := &URLTokenList{BoxID: b.ID, Port: port, Access: b.AccessFor(port), GatewayURL: m.GatewayURL(b, port), Items: []*model.URLToken{}}
	for _, t := range m.loadTokens(ctx, b.ID) {
		if t.Port == port {
			out.Items = append(out.Items, publicToken(t, now))
		}
	}
	return out, nil
}

// RevokeURLToken revokes one token at once (the gateway checks every request).
func (m *Manager) RevokeURLToken(ctx context.Context, p *Principal, id string, port int, tokID string) (*model.URLToken, error) {
	if err := p.Require(model.ScopeBoxExec); err != nil {
		return nil, err
	}
	b, err := m.portBox(ctx, p, id, port)
	if err != nil {
		return nil, err
	}
	lock := m.boxLock(b.ID)
	lock.Lock()
	defer lock.Unlock()
	list := m.loadTokens(ctx, b.ID)
	now := time.Now().UTC()
	for _, t := range list {
		if t.ID == tokID && t.Port == port {
			if t.RevokedAt == nil {
				t.RevokedAt = &now
				if err := m.Store.PutKV(ctx, urlTokKey(b.ID), list); err != nil {
					return nil, err
				}
				m.Emit(ctx, p.Actor(), b.ID, b.AgentID, "box.url_token", fmt.Sprintf("URL token %s for port %d revoked", t.Prefix+"…", port), nil)
			}
			return publicToken(t, now), nil
		}
	}
	return nil, model.E(model.ErrNotFound, "token %s not found", tokID)
}

// SetPortAccess switches a port between public and token access.
func (m *Manager) SetPortAccess(ctx context.Context, p *Principal, id string, port int, in PortAccessInput) (*PortView, error) {
	if err := p.Require(model.ScopeBoxExec); err != nil {
		return nil, err
	}
	if in.Access == "" && in.Label == "" {
		return nil, model.EH(model.ErrInvalid, "Pass access (public | token) and/or label (e.g. api).", "nothing to change: pass access or label")
	}
	if in.Access != "" && in.Access != model.AccessPublic && in.Access != model.AccessToken {
		return nil, model.EH(model.ErrInvalid, "Use \"public\" or \"token\".", "access must be public or token")
	}
	b, err := m.portBox(ctx, p, id, port)
	if err != nil {
		return nil, err
	}
	if in.Access == model.AccessPublic {
		if q, _ := m.WorkspaceQuota(ctx, b.WorkspaceID); !q.PublicURL {
			return nil, m.limitErr(q, "public_url", false)
		}
	}
	lock := m.boxLock(b.ID)
	lock.Lock()
	cur, err := m.Store.GetBox(ctx, b.ID)
	if err != nil {
		lock.Unlock()
		return nil, err
	}
	if in.Label != "" {
		before := namesCopy(cur)
		if err := m.setPortLabel(ctx, cur, port, in.Label); err != nil {
			lock.Unlock()
			return nil, err
		}
		m.refreshNamedURLs(cur, before)
	}
	if in.Access != "" {
		if cur.PortAccess == nil {
			cur.PortAccess = map[int]string{}
		}
		cur.PortAccess[port] = in.Access
	}
	m.Store.PutBox(ctx, cur)
	lock.Unlock()
	if in.Access != "" {
		m.Emit(ctx, p.Actor(), cur.ID, cur.AgentID, "box.port", fmt.Sprintf("port %d access → %s", port, in.Access), nil)
	}
	if in.Label != "" {
		m.Emit(ctx, p.Actor(), cur.ID, cur.AgentID, "box.port", fmt.Sprintf("port %d label → %s (%s)", port, cur.PortLabels[port], m.GatewayURL(cur, port)), nil)
	}
	m.publishBox(m.decorateBox(ctx, cur))
	h, _ := m.Store.GetHost(ctx, cur.HostID)
	v := m.portView(ctx, cur, h, port)
	return &v, nil
}

// CheckURLToken validates a token for box+port (not expired, not revoked).
func (m *Manager) CheckURLToken(ctx context.Context, b *model.Box, port int, tok string) bool {
	if !strings.HasPrefix(tok, URLTokenPrefix) {
		return false
	}
	want := hashToken(tok)
	now := time.Now()
	for _, t := range m.loadTokens(ctx, b.ID) {
		if t.Port == port && subtle.ConstantTimeCompare([]byte(t.Hash), []byte(want)) == 1 {
			return t.RevokedAt == nil && now.Before(t.ExpiresAt)
		}
	}
	return false
}
