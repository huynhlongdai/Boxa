package manager

// v1.10 box access (P0): short-lived single-use access tickets, WebSocket
// origin allowlist, the per-box TCP port allowlist, waking + linking a box for
// an interactive session, and the access summary (forward / ssh / terminal).
//
// Tickets replace the API key in URLs: POST /v1/boxes/{id}/tickets
// {purpose: terminal|tcp|ssh, port?, ttl_s?} returns bxa_… once. A ticket
// must be redeemed before expires_at (default 60 s), works once, and binds the
// connection it opened: revoking it closes that session within a second.
// Values are stored hashed and never logged.

import (
	"context"
	"crypto/subtle"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"boxaz/core/internal/hostlink"
	"boxaz/core/internal/model"
	"boxaz/core/internal/secret"
	"boxaz/core/internal/store"
)

// AccessTicketPrefix marks access tickets (bxa_<box id>_<random>).
const AccessTicketPrefix = "bxa_"

const (
	defaultTicketTTL = 60 * time.Second
	minTicketTTL     = 10 * time.Second
	maxTicketTTL     = 24 * time.Hour
	// SSHInternalPort is always reachable by the TCP stream (a box's own sshd).
	SSHInternalPort = 22
	// accessWakeWait bounds how long an interactive connect waits for a wake.
	accessWakeWait = 90 * time.Second
)

// accessState holds live access connections and terminal sessions.
type accessState struct {
	mu     sync.Mutex
	live   map[string]map[*liveConn]struct{} // ticket id → live connections
	byBox  map[string]map[*liveConn]struct{} // box id → live connections
	terms  termState
	ssh    sshState
	addons addonState // v1.11 browser + desktop
}

type liveConn struct {
	boxID    string
	ticketID string
	kind     string
	close    func(reason string)
}

// CreateTicketInput is POST /v1/boxes/{id}/tickets.
type CreateTicketInput struct {
	Purpose    string `json:"purpose"`              // terminal | tcp | ssh
	Port       int    `json:"port,omitempty"`       // tcp: the in-box port
	TTLSeconds int    `json:"ttl_s,omitempty"`      // seconds to redeem it (default 60, 10 … 86400)
	SessionID  string `json:"session_id,omitempty"` // terminal: only this session
	// Allow adds Port to the box's forward allowlist (tcp) when it is neither
	// exposed nor already allowed.
	Allow bool `json:"allow,omitempty"`
	// ViewOnly (vnc): core drops the viewer's keyboard / mouse input.
	ViewOnly bool `json:"view_only,omitempty"`
}

func ticketKey(boxID string) string { return "tickets:" + boxID }

func (m *Manager) loadTickets(ctx context.Context, boxID string) []*model.AccessTicket {
	var list []*model.AccessTicket
	m.Store.GetKV(ctx, ticketKey(boxID), &list)
	return list
}

// ResolveBox finds a box the principal owns by id, slug or gateway name.
func (m *Manager) ResolveBox(ctx context.Context, p *Principal, ref string) (*model.Box, error) {
	b, err := m.GetBox(ctx, p, ref)
	if err == nil {
		return b, nil
	}
	if d := m.GatewayDomain(ctx); d != "" {
		ref = strings.TrimSuffix(strings.ToLower(ref), "."+d)
	}
	if nb := m.boxByName(ctx, ref); nb != nil && (p.Admin || nb.AgentID == p.AgentID) {
		return m.decorateBox(ctx, nb), nil
	}
	return nil, err
}

// boxByName finds an active box by its gateway name (or a port host label).
func (m *Manager) boxByName(ctx context.Context, label string) *model.Box {
	label = strings.ToLower(strings.TrimSpace(label))
	if label == "" || !IsNameLabel(label) {
		return nil
	}
	boxes, err := m.Store.ListBoxes(ctx, store.BoxFilter{Statuses: []string{model.StatusRunning, model.StatusSleeping}})
	if err != nil {
		return nil
	}
	for _, b := range boxes {
		if b.Subdomain == label {
			return b
		}
	}
	if b, _, err := m.BoxForHostLabel(ctx, label); err == nil {
		return b
	}
	return nil
}

// BoxForAccessHost resolves the box of a gateway host label for /.boxaz/
// endpoints (any label of the box: <name> or <name>-<label>, or the legacy
// <key>-<port> alias).
func (m *Manager) BoxForAccessHost(ctx context.Context, seg string) (*model.Box, error) {
	if b := m.boxByName(ctx, seg); b != nil {
		return b, nil
	}
	if b, _, err := m.BoxForIngressPath(ctx, seg); err == nil {
		return b, nil
	}
	return nil, model.E(model.ErrNotFound, "no box at this address")
}

// ForwardAllowed reports whether the TCP stream may reach port in the box:
// exposed ports, the forward allowlist, and 22 (a box's own sshd).
func ForwardAllowed(b *model.Box, port int) bool {
	if port == SSHInternalPort {
		return true
	}
	for _, x := range b.Ports {
		if x == port {
			return true
		}
	}
	for _, x := range b.ForwardPorts {
		if x == port {
			return true
		}
	}
	return false
}

// ForwardPortsOf lists the ports the TCP stream may reach.
func ForwardPortsOf(b *model.Box) []int {
	seen := map[int]bool{SSHInternalPort: true}
	out := []int{SSHInternalPort}
	for _, l := range [][]int{b.Ports, b.ForwardPorts} {
		for _, x := range l {
			if !seen[x] {
				seen[x] = true
				out = append(out, x)
			}
		}
	}
	sort.Ints(out)
	return out
}

// AllowForwardPort adds port to the box's forward allowlist.
func (m *Manager) AllowForwardPort(ctx context.Context, p *Principal, ref string, port int) (*model.Box, error) {
	if err := p.Require(model.ScopeBoxExec); err != nil {
		return nil, err
	}
	if port < 1 || port > 65535 {
		return nil, model.EH(model.ErrInvalid, "Use a port between 1 and 65535.", "bad port %d", port)
	}
	b, err := m.ResolveBox(ctx, p, ref)
	if err != nil {
		return nil, err
	}
	if ForwardAllowed(b, port) {
		return b, nil
	}
	lock := m.boxLock(b.ID)
	lock.Lock()
	cur, err := m.Store.GetBox(ctx, b.ID)
	if err == nil && !ForwardAllowed(cur, port) {
		if len(cur.ForwardPorts) >= 32 {
			lock.Unlock()
			return nil, model.EH(model.ErrInvalid, "Remove ports with PATCH /v1/boxes/{id} {forward_ports: [...]}.", "box %s already allows 32 forward ports", cur.ID)
		}
		cur.ForwardPorts = append(cur.ForwardPorts, port)
		sort.Ints(cur.ForwardPorts)
		err = m.Store.PutBox(ctx, cur)
	}
	lock.Unlock()
	if err != nil {
		return nil, err
	}
	m.Emit(ctx, p.Actor(), cur.ID, cur.AgentID, "box.access", fmt.Sprintf("port %d added to the forward allowlist", port), nil)
	return m.decorateBox(ctx, cur), nil
}

// SetForwardPorts replaces the forward allowlist (PATCH /v1/boxes/{id}).
func setForwardPorts(b *model.Box, ports []int) error {
	seen := map[int]bool{}
	out := []int{}
	for _, x := range ports {
		if x < 1 || x > 65535 {
			return model.EH(model.ErrInvalid, "Use ports between 1 and 65535.", "bad port %d", x)
		}
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	if len(out) > 32 {
		return model.EH(model.ErrInvalid, "Keep at most 32 forward ports.", "too many forward ports (%d)", len(out))
	}
	sort.Ints(out)
	b.ForwardPorts = out
	return nil
}

func publicTicket(t *model.AccessTicket) *model.AccessTicket {
	c := *t
	c.Hash = ""
	c.Ticket = ""
	return &c
}

// CreateTicket issues a single-use access ticket (the value is returned once).
func (m *Manager) CreateTicket(ctx context.Context, p *Principal, ref string, in CreateTicketInput) (*model.AccessTicket, error) {
	if err := p.Require(model.ScopeBoxExec); err != nil {
		return nil, err
	}
	b, err := m.ResolveBox(ctx, p, ref)
	if err != nil {
		return nil, err
	}
	if !b.Active() {
		return nil, model.EH(model.ErrNotFound, "This box is gone; create a new one with box_up.", "box is %s", b.Status)
	}
	if b.TakenOver && !p.Admin {
		return nil, model.EH(model.ErrPolicyDenied, "A human has taken over this box; wait for release.", "box %s is under human takeover", b.Slug)
	}
	in.Purpose = strings.ToLower(strings.TrimSpace(in.Purpose))
	switch in.Purpose {
	case model.PurposeTerminal, model.PurposeSSH, model.PurposeVNC:
		in.Port = 0
	case model.PurposeCDP:
		in.Port = 0
		if in.TTLSeconds == 0 {
			in.TTLSeconds = 3600 // reusable until expiry: connectOverCDP(http) needs two requests
		}
	case model.PurposeTCP:
		if in.Port < 1 || in.Port > 65535 {
			return nil, model.EH(model.ErrInvalid, "Pass the in-box port, e.g. {\"purpose\":\"tcp\",\"port\":5432}.", "purpose tcp needs a port")
		}
		if !ForwardAllowed(b, in.Port) {
			if !in.Allow {
				return nil, model.EH(model.ErrPolicyDenied, "Expose the port, or pass allow=true (box forward does) to add it to the box's forward allowlist.", "port %d is not in the forward allowlist of box %s", in.Port, b.ID)
			}
			if b, err = m.AllowForwardPort(ctx, p, b.ID, in.Port); err != nil {
				return nil, err
			}
		}
	default:
		return nil, model.EH(model.ErrInvalid, "Use purpose terminal, tcp, ssh, cdp or vnc (share links: POST /v1/boxes/{id}/desktop/shares; certificates: POST /v1/ssh/certs).", "unknown ticket purpose %q", in.Purpose)
	}
	if in.SessionID != "" && in.Purpose != model.PurposeTerminal {
		in.SessionID = ""
	}
	ttl := defaultTicketTTL
	if in.TTLSeconds != 0 {
		ttl = time.Duration(in.TTLSeconds) * time.Second
	}
	if ttl < minTicketTTL || ttl > maxTicketTTL {
		return nil, model.EH(model.ErrInvalid, "Use a ttl_s between 10 and 86400.", "ttl_s out of range")
	}
	raw := AccessTicketPrefix + b.ID + "_" + secret.RandHex(20)
	now := time.Now().UTC()
	t := &model.AccessTicket{ID: secret.ID("tkt"), BoxID: b.ID, Purpose: in.Purpose, Port: in.Port, SessionID: in.SessionID,
		Prefix: raw[:len(AccessTicketPrefix)+len(b.ID)+5], ExpiresAt: now.Add(ttl), CreatedAt: now, Actor: p.Actor(), Hash: hashToken(raw),
		Reusable: in.Purpose == model.PurposeCDP, ViewOnly: in.Purpose == model.PurposeVNC && in.ViewOnly}
	if p.Key != nil {
		t.KeyID = p.Key.ID
	}
	lock := m.boxLock(b.ID)
	lock.Lock()
	list := m.loadTickets(ctx, b.ID)
	kept := list[:0]
	for _, x := range list {
		dead := now.After(x.ExpiresAt.Add(time.Hour)) && !m.ticketLive(x.ID)
		if !dead && len(kept) < 200 {
			kept = append(kept, x)
		}
	}
	kept = append(kept, t)
	err = m.Store.PutKV(ctx, ticketKey(b.ID), kept)
	lock.Unlock()
	if err != nil {
		return nil, err
	}
	m.Emit(ctx, p.Actor(), b.ID, b.AgentID, "box.ticket", fmt.Sprintf("%s ticket %s… issued (redeem before %s)", t.Purpose, t.Prefix, t.ExpiresAt.Format(time.RFC3339)), nil)
	out := publicTicket(t)
	out.Ticket = raw
	out.URL, out.CoreURL = m.accessURLs(ctx, b, t.Purpose, t.Port, t.SessionID)
	if out.URL != "" {
		out.URL = addQuery(out.URL, "ticket", raw)
	}
	if out.CoreURL != "" {
		out.CoreURL = addQuery(out.CoreURL, "ticket", raw)
	}
	if t.Purpose == model.PurposeSSH {
		if d := m.SSHDirect(); d != "" {
			out.Command = sshDirectCommand(d, raw)
		}
	}
	return out, nil
}

func addQuery(u, k, v string) string {
	sep := "?"
	if strings.Contains(u, "?") {
		sep = "&"
	}
	return u + sep + k + "=" + url.QueryEscape(v)
}

// ListTickets lists a box's tickets (never their values).
func (m *Manager) ListTickets(ctx context.Context, p *Principal, ref string) ([]*model.AccessTicket, error) {
	b, err := m.ResolveBox(ctx, p, ref)
	if err != nil {
		return nil, err
	}
	out := []*model.AccessTicket{}
	for _, t := range m.loadTickets(ctx, b.ID) {
		c := publicTicket(t)
		c.Live = m.ticketLiveCount(t.ID)
		out = append(out, c)
	}
	return out, nil
}

// RevokeTicket revokes one ticket ("all" = every ticket of the box) and
// closes the sessions bound to it.
func (m *Manager) RevokeTicket(ctx context.Context, p *Principal, ref, id string) (int, error) {
	if err := p.Require(model.ScopeBoxExec); err != nil {
		return 0, err
	}
	b, err := m.ResolveBox(ctx, p, ref)
	if err != nil {
		return 0, err
	}
	lock := m.boxLock(b.ID)
	lock.Lock()
	list := m.loadTickets(ctx, b.ID)
	now := time.Now().UTC()
	var hit []*model.AccessTicket
	for _, t := range list {
		if (id == "all" || t.ID == id) && t.RevokedAt == nil {
			t.RevokedAt = &now
			hit = append(hit, t)
		}
	}
	if len(hit) > 0 {
		err = m.Store.PutKV(ctx, ticketKey(b.ID), list)
	}
	lock.Unlock()
	if err != nil {
		return 0, err
	}
	if len(hit) == 0 && id != "all" {
		found := false
		for _, t := range list {
			found = found || t.ID == id
		}
		if !found {
			return 0, model.E(model.ErrNotFound, "ticket %s not found", id)
		}
	}
	closed := 0
	for _, t := range hit {
		closed += m.closeTicketConns(t.ID, "[boxaz] access revoked")
	}
	if len(hit) > 0 {
		m.Emit(ctx, p.Actor(), b.ID, b.AgentID, "box.ticket", fmt.Sprintf("%d ticket(s) revoked, %d session(s) closed", len(hit), closed), nil)
	}
	return len(hit), nil
}

// TicketBoxID extracts the box id from a ticket value ("" if malformed).
func TicketBoxID(raw string) string {
	if !strings.HasPrefix(raw, AccessTicketPrefix) {
		return ""
	}
	rest := raw[len(AccessTicketPrefix):]
	i := strings.LastIndexByte(rest, '_')
	if i <= 0 || len(rest)-i-1 != 40 {
		return ""
	}
	return rest[:i]
}

// RedeemTicket validates and consumes a ticket for purpose (and port for
// tcp; boxID when the endpoint already names the box). It returns the box and
// the principal that issued it (re-checked: a revoked key fails).
func (m *Manager) RedeemTicket(ctx context.Context, raw, purpose, boxID string, port int) (*model.AccessTicket, *Principal, *model.Box, error) {
	bad := model.EH(model.ErrUnauthorized, "Get a fresh ticket: POST /v1/boxes/{id}/tickets.", "access ticket is invalid, expired, revoked or already used")
	tb := TicketBoxID(raw)
	if tb == "" || (boxID != "" && tb != boxID) {
		return nil, nil, nil, bad
	}
	want := hashToken(raw)
	lock := m.boxLock(tb)
	lock.Lock()
	list := m.loadTickets(ctx, tb)
	now := time.Now().UTC()
	var t *model.AccessTicket
	for _, x := range list {
		if subtle.ConstantTimeCompare([]byte(x.Hash), []byte(want)) == 1 {
			t = x
		}
	}
	if t == nil || t.RevokedAt != nil || (t.RedeemedAt != nil && !t.Reusable) || now.After(t.ExpiresAt) || t.Purpose != purpose || (purpose == model.PurposeTCP && t.Port != port) {
		lock.Unlock()
		return nil, nil, nil, bad
	}
	if t.RedeemedAt == nil {
		t.RedeemedAt = &now
	}
	t.Uses++
	err := m.Store.PutKV(ctx, ticketKey(tb), list)
	lock.Unlock()
	if err != nil {
		return nil, nil, nil, err
	}
	p, err := m.principalForKey(ctx, t.KeyID)
	if err != nil {
		return nil, nil, nil, err
	}
	b, err := m.Store.GetBox(ctx, tb)
	if err != nil || !b.Active() {
		return nil, nil, nil, model.E(model.ErrNotFound, "no box at this address")
	}
	return t, p, b, nil
}

// principalForKey rebuilds the principal of an API key id (fails if revoked).
func (m *Manager) principalForKey(ctx context.Context, keyID string) (*Principal, error) {
	k, err := m.Store.GetKey(ctx, keyID)
	if err != nil || k.RevokedAt != nil {
		return nil, model.EH(model.ErrUnauthorized, "This key was revoked; ask the operator for a new key.", "API key revoked")
	}
	p := &Principal{Key: k, Admin: k.HasScope(model.ScopeAdmin)}
	if k.AgentID != nil {
		p.AgentID = *k.AgentID
	}
	return p, nil
}

// BindAccess registers a live connection (bound to ticketID when it came from
// a ticket); close is called on revoke, box sleep or delete. Call the returned
// func when the connection ends.
func (m *Manager) BindAccess(boxID, ticketID, kind string, close func(reason string)) func() {
	c := &liveConn{boxID: boxID, ticketID: ticketID, kind: kind, close: close}
	m.acc.mu.Lock()
	if m.acc.live == nil {
		m.acc.live = map[string]map[*liveConn]struct{}{}
		m.acc.byBox = map[string]map[*liveConn]struct{}{}
	}
	if ticketID != "" {
		if m.acc.live[ticketID] == nil {
			m.acc.live[ticketID] = map[*liveConn]struct{}{}
		}
		m.acc.live[ticketID][c] = struct{}{}
	}
	if m.acc.byBox[boxID] == nil {
		m.acc.byBox[boxID] = map[*liveConn]struct{}{}
	}
	m.acc.byBox[boxID][c] = struct{}{}
	m.acc.mu.Unlock()
	return func() {
		m.acc.mu.Lock()
		delete(m.acc.live[ticketID], c)
		if len(m.acc.live[ticketID]) == 0 {
			delete(m.acc.live, ticketID)
		}
		delete(m.acc.byBox[boxID], c)
		if len(m.acc.byBox[boxID]) == 0 {
			delete(m.acc.byBox, boxID)
		}
		m.acc.mu.Unlock()
	}
}

func (m *Manager) ticketLive(id string) bool { return m.ticketLiveCount(id) > 0 }

func (m *Manager) ticketLiveCount(id string) int {
	m.acc.mu.Lock()
	defer m.acc.mu.Unlock()
	return len(m.acc.live[id])
}

func (m *Manager) closeTicketConns(id, reason string) int {
	m.acc.mu.Lock()
	var cs []*liveConn
	for c := range m.acc.live[id] {
		cs = append(cs, c)
	}
	m.acc.mu.Unlock()
	for _, c := range cs {
		c.close(reason)
	}
	return len(cs)
}

// closeAccess ends every interactive connection and terminal session of a box
// (sleep, delete).
func (m *Manager) closeAccess(boxID, reason string) {
	m.acc.mu.Lock()
	var cs []*liveConn
	for c := range m.acc.byBox[boxID] {
		cs = append(cs, c)
	}
	m.acc.mu.Unlock()
	for _, c := range cs {
		c.close(reason)
	}
	m.killBoxTerminals(boxID, reason)
	m.forgetAddons(boxID)
}

// LiveAccess counts open interactive connections of a box by kind.
func (m *Manager) LiveAccess(boxID string) map[string]int {
	m.acc.mu.Lock()
	defer m.acc.mu.Unlock()
	out := map[string]int{}
	for c := range m.acc.byBox[boxID] {
		out[c.kind]++
	}
	return out
}

// ---- origin allowlist ----

func originOf(u string) string {
	pu, err := url.Parse(strings.TrimSpace(u))
	if err != nil || pu.Scheme == "" || pu.Host == "" {
		return ""
	}
	return strings.ToLower(pu.Scheme + "://" + pu.Host)
}

// AccessOrigins lists the browser origins allowed to open access WebSockets:
// the dashboard (BOXAZ_DASHBOARD_URL), core's public URL, BOXAZ_ACCESS_ORIGINS
// and explicit (non-*) CORS origins.
func (m *Manager) AccessOrigins(cors string) []string {
	var out []string
	add := func(u string) {
		if o := originOf(u); o != "" {
			for _, x := range out {
				if x == o {
					return
				}
			}
			out = append(out, o)
		}
	}
	add(m.Cfg.DashboardURL)
	add(m.Cfg.PublicURL)
	for _, o := range m.Cfg.AccessOrigins {
		add(o)
	}
	if cors != "*" {
		for _, o := range strings.Split(cors, ",") {
			add(o)
		}
	}
	return out
}

func isLoopbackHost(h string) bool {
	if hh, _, err := net.SplitHostPort(h); err == nil {
		h = hh
	}
	h = strings.Trim(h, "[]")
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// OriginAllowed checks the Origin of an access WebSocket. Native clients send
// none. Browsers must come from an allowed origin; a loopback origin is
// accepted when core itself is reached on loopback (local development).
func (m *Manager) OriginAllowed(r *http.Request, cors string) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return true
	}
	oo := originOf(o)
	if oo == "" {
		return false
	}
	for _, a := range m.AccessOrigins(cors) {
		if a == oo {
			return true
		}
	}
	pu, _ := url.Parse(oo)
	if d := m.GatewayDomain(r.Context()); d != "" && pu != nil && strings.HasSuffix(strings.ToLower(pu.Hostname()), "."+d) {
		return false // v1.11: box hosts (app JS) never open access sockets
	}
	return pu != nil && isLoopbackHost(pu.Host) && isLoopbackHost(r.Host)
}

// ---- link: wake + a peer that opens streams / processes ----

// AccessLink wakes the box when needed and returns a live link able to open
// streams and processes: boxd (T1/T2) or the in-box ingress agent (T3, which
// is started on demand and needs a core URL the sandbox can reach).
func (m *Manager) AccessLink(ctx context.Context, b *model.Box) (*model.Box, *hostlink.Peer, error) {
	ctx, cancel := context.WithTimeout(ctx, accessWakeWait)
	defer cancel()
	if b.Status == model.StatusSleeping {
		op := m.GatewayWake(b.ID)
		select {
		case <-op.Done:
			if op.Err != nil {
				return nil, nil, op.Err
			}
		case <-ctx.Done():
			return nil, nil, model.EH(model.ErrBoxSleeping, "Retry in a few seconds.", "box %s is still waking", b.ID)
		}
		nb, err := m.Store.GetBox(ctx, b.ID)
		if err != nil {
			return nil, nil, err
		}
		b = nb
	}
	if b.Status != model.StatusRunning {
		return nil, nil, model.EH(model.ErrInvalid, "Wait until the box is running.", "box is %s", b.Status)
	}
	m.GatewayTouch(ctx, b.ID)
	for {
		if p, ok := m.linkPeer(b); ok {
			return b, p, nil
		}
		if m.Hub.IsVirtual(b.HostID) {
			if ok, why := CoreLooksPublic(m.Cfg.PublicURL); !ok && !(m.Cfg.AssumePublicURL && m.Cfg.PublicURL != "") {
				return nil, nil, model.EH(model.ErrProviderFailed, "Set BOXAZ_PUBLIC_URL to an internet-reachable URL of core so the sandbox's agent can dial in.", "this exec-only sandbox has no live link for interactive access: %s", why)
			}
			l := m.exposeLock(b.ID)
			if l.TryLock() {
				err := m.startIngress(ctx, b)
				l.Unlock()
				if err != nil {
					return nil, nil, model.EH(model.ErrProviderFailed, "Retry in a few seconds; the sandbox must allow a background process and outbound WebSocket.", "in-box agent for interactive access: %v", err)
				}
				continue
			}
		}
		select {
		case <-ctx.Done():
			return nil, nil, model.EH(model.ErrProviderFailed, "Retry in a few seconds; the box link is reconnecting.", "box %s link is not connected", b.ID)
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// KeepActive touches a box every 30 s while an interactive session is open,
// so idle sleep does not fire under a user. Stop by cancelling ctx.
func (m *Manager) KeepActive(ctx context.Context, boxID string) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.gw.touched.Delete(boxID)
			m.GatewayTouch(context.Background(), boxID)
		}
	}
}

// ---- URLs and the access summary ----

func wsScheme(u string) string {
	switch {
	case strings.HasPrefix(u, "https://"):
		return "wss://" + u[8:]
	case strings.HasPrefix(u, "http://"):
		return "ws://" + u[7:]
	}
	return u
}

// accessURLs returns the WebSocket endpoint of a purpose on the box's own host
// (wss://<name>.<domain>/.boxaz/…, "" without a gateway domain) and on core.
func (m *Manager) accessURLs(ctx context.Context, b *model.Box, purpose string, port int, session string) (hostURL, coreURL string) {
	kind := purpose
	q := ""
	switch purpose {
	case model.PurposeTCP:
		q = "?port=" + strconv.Itoa(port)
	case model.PurposeTerminal:
		if session != "" {
			q = "?session=" + url.QueryEscape(session)
		}
	}
	if d := m.GatewayDomain(ctx); d != "" && b.Subdomain != "" {
		scheme := "wss://"
		if strings.HasPrefix(m.Cfg.PublicURL, "http://") {
			scheme = "ws://"
		}
		hostURL = scheme + b.Subdomain + "." + d + "/.boxaz/" + kind + q
	}
	base := strings.TrimRight(wsScheme(m.Cfg.PublicURL), "/")
	coreURL = base + "/v1/boxes/" + b.ID + "/" + kind + q
	if hostURL == "" {
		hostURL = coreURL
	}
	return
}

// AccessInfo is GET /v1/boxes/{id}/access: how to reach the box beyond HTTP.
type AccessInfo struct {
	BoxID    string            `json:"box_id"`
	Name     string            `json:"name,omitempty"`
	Host     string            `json:"host,omitempty"` // <name>.<domain>
	Link     string            `json:"link"`           // boxd | agent | agent-on-demand | none
	Live     map[string]int    `json:"live"`           // open connections by kind
	TCP      AccessTCP         `json:"tcp"`
	Terminal AccessTerminal    `json:"terminal"`
	SSH      AccessSSH         `json:"ssh"`
	CDP      AccessAddon       `json:"cdp"`     // v1.11 browser endpoint
	Desktop  AccessAddon       `json:"desktop"` // v1.11 VNC desktop
	Commands map[string]string `json:"commands"`
	Notes    []string          `json:"notes,omitempty"`
	Tickets  string            `json:"tickets"` // POST endpoint
}

// AccessTCP describes the TCP stream endpoint.
type AccessTCP struct {
	URL          string `json:"url"`      // wss://…/.boxaz/tcp?port={port}
	CoreURL      string `json:"core_url"` // wss://core/v1/boxes/{id}/tcp?port={port}
	AllowedPorts []int  `json:"allowed_ports"`
	Auth         string `json:"auth"`
}

// AccessAddon describes the on-demand browser or desktop of a box (v1.11).
type AccessAddon struct {
	URL     string `json:"url"`      // wss://<name>.<domain>/.boxaz/{cdp,vnc}?ticket=…
	CoreURL string `json:"core_url"` // same on core
	Page    string `json:"page,omitempty"`
	Start   string `json:"start"` // REST call that starts it and returns a ticketed URL
	Running bool   `json:"running"`
	Clients int    `json:"clients"`
}

// AccessTerminal describes the durable web terminal.
type AccessTerminal struct {
	URL        string                   `json:"url"`
	CoreURL    string                   `json:"core_url"`
	DetachTTLS int                      `json:"detach_ttl_s"`
	Sessions   []*model.TerminalSession `json:"sessions"`
}

// AccessSSH describes SSH access.
type AccessSSH struct {
	URL          string `json:"url"` // WebSocket the ProxyCommand tunnels through
	CoreURL      string `json:"core_url"`
	Host         string `json:"host"` // alias used in ssh-config
	ProxyCommand string `json:"proxy_command"`
	Command      string `json:"command"`
	Config       string `json:"config"`
	KnownHosts   string `json:"known_hosts"`
	Fingerprint  string `json:"host_key_fingerprint"`
	Direct       string `json:"direct,omitempty"` // host:port of the direct listener (ssh <ticket>@host)
	Features     string `json:"features"`
}

// Access builds the access summary of a box.
func (m *Manager) Access(ctx context.Context, p *Principal, ref string) (*AccessInfo, error) {
	b, err := m.ResolveBox(ctx, p, ref)
	if err != nil {
		return nil, err
	}
	name := b.Subdomain
	if name == "" {
		name = b.Slug
	}
	out := &AccessInfo{BoxID: b.ID, Name: name, Live: m.LiveAccess(b.ID), Commands: map[string]string{},
		Tickets: "POST /v1/boxes/" + b.ID + "/tickets {purpose: terminal|tcp|ssh|cdp|vnc, port?, ttl_s?, view_only?}"}
	if d := m.GatewayDomain(ctx); d != "" && b.Subdomain != "" {
		out.Host = b.Subdomain + "." + d
	}
	switch {
	case !m.Hub.IsVirtual(b.HostID):
		out.Link = "boxd"
	case func() bool { _, ok := m.ingressPeer(b.ID); return ok }():
		out.Link = "agent"
	default:
		if ok, _ := CoreLooksPublic(m.Cfg.PublicURL); ok || (m.Cfg.AssumePublicURL && m.Cfg.PublicURL != "") {
			out.Link = "agent-on-demand"
		} else {
			out.Link = "none"
			out.Notes = append(out.Notes, "exec-only sandbox and core is not reachable from it (BOXAZ_PUBLIC_URL): terminal, forward and ssh need the in-box agent to dial in")
		}
	}
	h, c := m.accessURLs(ctx, b, model.PurposeTCP, 0, "")
	out.TCP = AccessTCP{URL: strings.Replace(h, "port=0", "port={port}", 1), CoreURL: strings.Replace(c, "port=0", "port={port}", 1),
		AllowedPorts: ForwardPortsOf(b), Auth: "?ticket=bxa_… (purpose tcp, same port) or Authorization: Bearer <API key>"}
	h, c = m.accessURLs(ctx, b, model.PurposeTerminal, 0, "")
	out.Terminal = AccessTerminal{URL: h, CoreURL: c, DetachTTLS: int(m.termDetachTTL().Seconds()), Sessions: m.listTerms(b.ID)}
	h, c = m.accessURLs(ctx, b, model.PurposeSSH, 0, "")
	alias := out.Host
	if alias == "" {
		alias = name + ".boxaz"
	}
	known, fp := m.SSHKnownHosts()
	out.SSH = AccessSSH{URL: h, CoreURL: c, Host: alias, ProxyCommand: "box ssh-proxy %h", Command: "box ssh " + name,
		KnownHosts: known, Fingerprint: fp, Direct: m.SSHDirect(),
		Features: "shell, exec, pty, env, window-change, signals, local port forwarding (ssh -L, VS Code / Cursor Remote-SSH), sftp / scp always (the box's sftp-server, else served by core), short-lived user certificates (POST /v1/ssh/certs); no -R / agent / X11 forwarding",
		Config:   SSHConfigBlock([]string{alias}, "box ssh-proxy %h")}
	out.Commands["forward"] = "box forward " + name + " <remote-port> [local-port]"
	out.Commands["ssh"] = "box ssh " + name
	out.Commands["ssh_exec"] = "box ssh " + name + " -- uname -a"
	out.Commands["ssh_config"] = "box ssh-config --write"
	out.Commands["ssh_cert"] = "box ssh-cert " + name + " --ttl 12h"
	out.Commands["sftp"] = "box sftp " + name + " put ./file.txt /tmp/"
	out.Commands["scp"] = "scp -o ProxyCommand='box ssh-proxy %h' -o HostKeyAlias=" + SSHHostKeyAlias + " ./file.txt boxaz@" + alias + ":/tmp/"
	out.Commands["browser"] = "box browser " + name
	out.Commands["desktop"] = "box desktop " + name
	out.Commands["desktop_share"] = "box desktop " + name + " --share --ttl 1h"
	live := m.LiveAccess(b.ID)
	a := m.addonsOf(b.ID)
	a.mu.Lock()
	bUp, dUp := a.browserUp, a.desktopUp
	a.mu.Unlock()
	h, c = m.accessURLs(ctx, b, model.PurposeCDP, 0, "")
	out.CDP = AccessAddon{URL: h, CoreURL: c, Start: "POST /v1/boxes/" + b.ID + "/browser", Running: bUp, Clients: live["cdp"]}
	h, c = m.accessURLs(ctx, b, model.PurposeVNC, 0, "")
	out.Desktop = AccessAddon{URL: h, CoreURL: c, Page: strings.TrimSuffix(m.desktopPageURL(ctx, b, "ticket", ""), "?ticket="), Start: "POST /v1/boxes/" + b.ID + "/desktop", Running: dUp, Clients: live["vnc"]}
	out.Commands["ssh_proxy"] = "ssh -o ProxyCommand='box ssh-proxy %h' -o HostKeyAlias=" + SSHHostKeyAlias + " boxaz@" + alias
	if out.SSH.Direct != "" {
		host, port, _ := net.SplitHostPort(out.SSH.Direct)
		out.Commands["ssh_direct"] = "ssh -p " + port + " $(box ticket " + name + " ssh)@" + host
	}
	return out, nil
}

// SSHHostKeyAlias is the known_hosts name of the gateway's host key.
const SSHHostKeyAlias = "boxaz-ssh"

// SSHConfigBlock renders ~/.ssh/config entries routed through the ProxyCommand.
func SSHConfigBlock(hosts []string, proxy string) string {
	var b strings.Builder
	for _, h := range hosts {
		fmt.Fprintf(&b, "Host %s\n  HostName %s\n  User boxaz\n  ProxyCommand %s\n  HostKeyAlias %s\n  UserKnownHostsFile ~/.boxaz/known_hosts\n  ServerAliveInterval 25\n\n", h, h, proxy, SSHHostKeyAlias)
	}
	return b.String()
}

func sshDirectCommand(direct, ticket string) string {
	host, port, err := net.SplitHostPort(direct)
	if err != nil {
		return "ssh " + ticket + "@" + direct
	}
	if port == "22" {
		return "ssh " + ticket + "@" + host
	}
	return "ssh -p " + port + " " + ticket + "@" + host
}
