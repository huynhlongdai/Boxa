package manager

// v1.11 desktop: Xvfb + a light window manager + x11vnc in the box (127.0.0.1
// only), RFB streamed over WSS by core (/.boxmes/vnc) and viewed with noVNC
// served by core (/.boxmes/desktop). View-only is enforced by core, which drops
// input messages from the viewer. Share links (InstaBox-style streamUrl) are
// reusable view-only tokens with a TTL (60 s … 24 h); revoking them closes
// every viewer within a second and the link answers 410 from then on.

import (
	"context"
	"crypto/subtle"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"boxmes/core/internal/hostlink"
	"boxmes/core/internal/model"
	"boxmes/core/internal/secret"
)

// ShareTokenPrefix marks desktop share tokens (bxs_<box id>_<40 hex>).
const ShareTokenPrefix = "bxs_"

// DesktopState is GET /v1/boxes/{id}/desktop.
type DesktopState struct {
	BoxID     string `json:"box_id"`
	Running   bool   `json:"running"`
	Display   string `json:"display,omitempty"`
	Width     int    `json:"width,omitempty"`
	Height    int    `json:"height,omitempty"`
	VNC       string `json:"vnc_server,omitempty"` // x11vnc | Xvnc
	WM        string `json:"wm,omitempty"`
	Started   bool   `json:"started"`
	StartMS   int64  `json:"start_ms,omitempty"`
	Viewers   int    `json:"viewers"`
	Shares    int    `json:"shares"` // active share links
	IdleStopS int    `json:"idle_stop_s"`
	Port      int    `json:"port"`
	Browser   bool   `json:"browser_on_desktop"`
	Note      string `json:"note,omitempty"`
}

// EnsureDesktop starts the desktop when its VNC server does not run. When a
// headless browser runs with no CDP client, it is restarted on the desktop so
// it is visible.
func (m *Manager) EnsureDesktop(ctx context.Context, b *model.Box, peer *hostlink.Peer, w, h int) (*DesktopState, error) {
	a := m.addonsOf(b.ID)
	a.mu.Lock()
	defer a.mu.Unlock()
	if w < 320 || w > 3840 || h < 240 || h > 2160 {
		w, h = 1280, 800
	}
	{
		if m.rfbAnswers(ctx, peer, b.ID) {
			st := &DesktopState{BoxID: b.ID, Running: true, Display: a.desktopDisplay, Width: a.desktopW, Height: a.desktopH, VNC: a.desktopVNC, WM: a.desktopWM,
				Port: m.VNCPort(), IdleStopS: int(m.desktopIdle().Seconds()), Browser: a.browserUp && !a.browserHeadless}
			if !a.desktopUp { // a VNC server we did not start (image with its own desktop)
				a.desktopUp, a.desktopVNC = true, "existing"
				st.VNC = "existing"
			}
			a.desktopLast = time.Now()
			m.watchAddons(b.ID)
			return st, nil
		}
	}
	start := time.Now()
	cctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	kv, err := m.addonScript(cctx, peer, b.ID, desktopStart, []string{"VPORT=" + strconv.Itoa(m.VNCPort()), "DISP=" + strconv.Itoa(desktopDisplay), "W=" + strconv.Itoa(w), "H=" + strconv.Itoa(h)})
	if err != nil {
		return nil, err
	}
	st := &DesktopState{BoxID: b.ID, Running: true, Display: kv["display"], VNC: kv["vnc"], WM: kv["wm"], Port: m.VNCPort(), IdleStopS: int(m.desktopIdle().Seconds())}
	st.Width, _ = strconv.Atoi(kv["width"])
	st.Height, _ = strconv.Atoi(kv["height"])
	if st.VNC == "" {
		st.VNC, st.WM = a.desktopVNC, a.desktopWM
	}
	a.desktopUp, a.desktopDisplay, a.desktopW, a.desktopH = true, st.Display, st.Width, st.Height
	a.desktopVNC, a.desktopWM, a.desktopLast = st.VNC, st.WM, time.Now()
	if kv["state"] == "started" {
		st.Started, st.StartMS = true, time.Since(start).Milliseconds()
		a.desktopStarted = time.Now()
		m.Emit(ctx, "system", b.ID, b.AgentID, "box.desktop", fmt.Sprintf("desktop started %dx%d (%s, %d ms)", st.Width, st.Height, st.VNC, st.StartMS), nil)
		// a headless browser nobody is driving moves onto the desktop
		if a.browserUp && a.browserHeadless && m.LiveAccess(b.ID)["cdp"] == 0 && a.browserSource != "existing" {
			if m.stopBrowserLocked(cctx, a, b, peer, "moving onto the desktop") == nil {
				if kv2, err := m.addonScript(cctx, peer, b.ID, browserStart, []string{"PORT=" + strconv.Itoa(m.CDPPort()), "HEADLESS=0", "W=" + strconv.Itoa(st.Width), "H=" + strconv.Itoa(st.Height)}); err == nil {
					a.browserUp, a.browserHeadless, a.browserLast = true, kv2["headless"] == "1", time.Now()
				}
			}
		}
	}
	st.Browser = a.browserUp && !a.browserHeadless
	m.watchAddons(b.ID)
	return st, nil
}

// rfbAnswers reports whether the in-box VNC port greets with "RFB ".
func (m *Manager) rfbAnswers(ctx context.Context, peer *hostlink.Peer, boxID string) bool {
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	sc, err := peer.OpenStream(cctx, boxID, m.VNCPort())
	if err != nil {
		return false
	}
	defer sc.Close()
	sc.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 12)
	n, _ := io.ReadFull(sc, buf)
	return n >= 4 && string(buf[:4]) == "RFB "
}

func (m *Manager) stopDesktopLocked(ctx context.Context, a *boxAddons, b *model.Box, peer *hostlink.Peer, why string) error {
	m.closeKind(b.ID, "vnc", "[boxmes] desktop stopped")
	_, err := m.addonScript(ctx, peer, b.ID, desktopStop, nil)
	a.desktopUp = false
	if err == nil {
		m.Emit(ctx, "system", b.ID, b.AgentID, "box.desktop", "desktop stopped ("+why+")", nil)
	}
	return err
}

// DesktopInput is POST /v1/boxes/{id}/desktop.
type DesktopInput struct {
	Width      int  `json:"width,omitempty"`
	Height     int  `json:"height,omitempty"`
	TTLSeconds int  `json:"ttl_s,omitempty"`     // viewer ticket: seconds to open it (default 60)
	ViewOnly   bool `json:"view_only,omitempty"` // viewer cannot type or click
}

// DesktopSession is the POST answer.
type DesktopSession struct {
	*DesktopState
	VNCURL    string    `json:"vnc_url"`    // wss://<name>.<domain>/.boxmes/vnc?ticket=… (RFB over binary frames)
	ViewerURL string    `json:"viewer_url"` // https://<name>.<domain>/.boxmes/desktop?ticket=… (noVNC page)
	CoreURL   string    `json:"core_url"`
	TicketID  string    `json:"ticket_id"`
	ViewOnly  bool      `json:"view_only"`
	ExpiresAt time.Time `json:"expires_at"`
}

// OpenDesktop wakes the box, ensures the desktop and issues a viewer ticket.
func (m *Manager) OpenDesktop(ctx context.Context, p *Principal, ref string, in DesktopInput) (*DesktopSession, error) {
	t, err := m.CreateTicket(ctx, p, ref, CreateTicketInput{Purpose: model.PurposeVNC, TTLSeconds: in.TTLSeconds, ViewOnly: in.ViewOnly})
	if err != nil {
		return nil, err
	}
	b, err := m.Store.GetBox(ctx, t.BoxID)
	if err != nil {
		return nil, err
	}
	b, peer, err := m.AccessLink(ctx, b)
	if err != nil {
		return nil, err
	}
	st, err := m.EnsureDesktop(ctx, b, peer, in.Width, in.Height)
	if err != nil {
		m.RevokeTicket(ctx, p, b.ID, t.ID)
		return nil, err
	}
	st.Viewers = m.LiveAccess(b.ID)["vnc"]
	st.Shares = m.activeShares(ctx, b.ID)
	return &DesktopSession{DesktopState: st, VNCURL: t.URL, CoreURL: t.CoreURL, TicketID: t.ID, ViewOnly: t.ViewOnly, ExpiresAt: t.ExpiresAt,
		ViewerURL: m.desktopPageURL(ctx, b, "ticket", t.Ticket) + map[bool]string{true: "&view_only=1", false: ""}[t.ViewOnly]}, nil
}

// desktopPageURL is the noVNC page on the box host (or core without a gateway domain).
func (m *Manager) desktopPageURL(ctx context.Context, b *model.Box, param, token string) string {
	if d := m.GatewayDomain(ctx); d != "" && b.Subdomain != "" {
		scheme := "https://"
		if strings.HasPrefix(m.Cfg.PublicURL, "http://") {
			scheme = "http://"
		}
		return scheme + b.Subdomain + "." + d + "/.boxmes/desktop?" + param + "=" + token
	}
	return strings.TrimRight(m.Cfg.PublicURL, "/") + "/v1/boxes/" + b.ID + "/desktop/view?" + param + "=" + token
}

// DesktopStatus reports without waking the box or starting anything.
func (m *Manager) DesktopStatus(ctx context.Context, p *Principal, ref string) (*DesktopState, error) {
	b, err := m.ResolveBox(ctx, p, ref)
	if err != nil {
		return nil, err
	}
	st := &DesktopState{BoxID: b.ID, Port: m.VNCPort(), IdleStopS: int(m.desktopIdle().Seconds()), Viewers: m.LiveAccess(b.ID)["vnc"], Shares: m.activeShares(ctx, b.ID)}
	peer, ok := m.linkPeer(b)
	if b.Status != model.StatusRunning || !ok {
		st.Note = "box is " + b.Status + "; POST /v1/boxes/{id}/desktop wakes it and starts the desktop"
		return st, nil
	}
	kv, err := m.addonScript(ctx, peer, b.ID, desktopStatus, []string{"VPORT=" + strconv.Itoa(m.VNCPort())})
	if err != nil {
		return nil, err
	}
	st.Running = kv["running"] == "1"
	st.Display = kv["display"]
	st.Width, _ = strconv.Atoi(kv["width"])
	st.Height, _ = strconv.Atoi(kv["height"])
	a := m.addonsOf(b.ID)
	a.mu.Lock()
	st.VNC, st.WM, st.Browser = a.desktopVNC, a.desktopWM, a.browserUp && !a.browserHeadless
	a.mu.Unlock()
	return st, nil
}

// StopDesktop stops the desktop and disconnects viewers.
func (m *Manager) StopDesktop(ctx context.Context, p *Principal, ref string) (map[string]any, error) {
	if err := p.Require(model.ScopeBoxExec); err != nil {
		return nil, err
	}
	b, err := m.ResolveBox(ctx, p, ref)
	if err != nil {
		return nil, err
	}
	peer, ok := m.linkPeer(b)
	if b.Status != model.StatusRunning || !ok {
		m.closeKind(b.ID, "vnc", "[boxmes] desktop stopped")
		return map[string]any{"box_id": b.ID, "stopped": false, "note": "box is not running"}, nil
	}
	a := m.addonsOf(b.ID)
	a.mu.Lock()
	err = m.stopDesktopLocked(ctx, a, b, peer, "requested")
	a.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return map[string]any{"box_id": b.ID, "stopped": true}, nil
}

// ---- share links ----

// ShareInput is POST /v1/boxes/{id}/desktop/shares.
type ShareInput struct {
	TTLSeconds int   `json:"ttl_s,omitempty"`     // 60 … 86400 (default 3600)
	ViewOnly   *bool `json:"view_only,omitempty"` // default true
}

// DesktopShare is one share link (URL only in the create response).
type DesktopShare struct {
	ID        string     `json:"id"`
	BoxID     string     `json:"box_id"`
	URL       string     `json:"url,omitempty"`
	Prefix    string     `json:"prefix"`
	ViewOnly  bool       `json:"view_only"`
	ExpiresAt time.Time  `json:"expires_at"`
	CreatedAt time.Time  `json:"created_at"`
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
	Viewers   int        `json:"viewers"`
	Uses      int        `json:"uses"`
	Actor     string     `json:"actor,omitempty"`
}

func shareOf(t *model.AccessTicket, live int) *DesktopShare {
	return &DesktopShare{ID: t.ID, BoxID: t.BoxID, Prefix: t.Prefix, ViewOnly: t.ViewOnly, ExpiresAt: t.ExpiresAt, CreatedAt: t.CreatedAt,
		RevokedAt: t.RevokedAt, Viewers: live, Uses: t.Uses, Actor: t.Actor}
}

// CreateShare issues a reusable desktop share link (view-only by default).
func (m *Manager) CreateShare(ctx context.Context, p *Principal, ref string, in ShareInput) (*DesktopShare, error) {
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
	ttl := 3600 * time.Second
	if in.TTLSeconds != 0 {
		ttl = time.Duration(in.TTLSeconds) * time.Second
	}
	if ttl < time.Minute || ttl > 24*time.Hour {
		return nil, model.EH(model.ErrInvalid, "Use a ttl_s between 60 and 86400.", "ttl_s out of range")
	}
	viewOnly := in.ViewOnly == nil || *in.ViewOnly
	raw := ShareTokenPrefix + b.ID + "_" + secret.RandHex(20)
	now := time.Now().UTC()
	t := &model.AccessTicket{ID: secret.ID("shr"), BoxID: b.ID, Purpose: model.PurposeShare, Prefix: raw[:len(ShareTokenPrefix)+len(b.ID)+5],
		ExpiresAt: now.Add(ttl), CreatedAt: now, Actor: p.Actor(), Hash: hashToken(raw), ViewOnly: viewOnly, Reusable: true}
	if p.Key != nil {
		t.KeyID = p.Key.ID
	}
	lock := m.boxLock(b.ID)
	lock.Lock()
	list := append(m.pruneTickets(m.loadTickets(ctx, b.ID), now), t)
	err = m.Store.PutKV(ctx, ticketKey(b.ID), list)
	lock.Unlock()
	if err != nil {
		return nil, err
	}
	mode := "view-only"
	if !viewOnly {
		mode = "interactive"
	}
	m.Emit(ctx, p.Actor(), b.ID, b.AgentID, "box.desktop", fmt.Sprintf("%s desktop share link %s… created (until %s)", mode, t.Prefix, t.ExpiresAt.Format(time.RFC3339)), nil)
	out := shareOf(t, 0)
	out.URL = m.desktopPageURL(ctx, b, "share", raw)
	return out, nil
}

// ListShares lists a box's share links (never their tokens).
func (m *Manager) ListShares(ctx context.Context, p *Principal, ref string) ([]*DesktopShare, error) {
	b, err := m.ResolveBox(ctx, p, ref)
	if err != nil {
		return nil, err
	}
	out := []*DesktopShare{}
	for _, t := range m.loadTickets(ctx, b.ID) {
		if t.Purpose == model.PurposeShare {
			out = append(out, shareOf(t, m.ticketLiveCount(t.ID)))
		}
	}
	return out, nil
}

func (m *Manager) activeShares(ctx context.Context, boxID string) int {
	n := 0
	now := time.Now()
	for _, t := range m.loadTickets(ctx, boxID) {
		if t.Purpose == model.PurposeShare && t.RevokedAt == nil && now.Before(t.ExpiresAt) {
			n++
		}
	}
	return n
}

// RevokeShares revokes one share link (or "all") and disconnects its viewers.
func (m *Manager) RevokeShares(ctx context.Context, p *Principal, ref, id string) (int, int, error) {
	if err := p.Require(model.ScopeBoxExec); err != nil {
		return 0, 0, err
	}
	b, err := m.ResolveBox(ctx, p, ref)
	if err != nil {
		return 0, 0, err
	}
	lock := m.boxLock(b.ID)
	lock.Lock()
	list := m.loadTickets(ctx, b.ID)
	now := time.Now().UTC()
	var hit []*model.AccessTicket
	found := false
	for _, t := range list {
		if t.Purpose != model.PurposeShare {
			continue
		}
		if id == "all" || t.ID == id {
			found = true
			if t.RevokedAt == nil {
				t.RevokedAt = &now
				hit = append(hit, t)
			}
		}
	}
	if len(hit) > 0 {
		err = m.Store.PutKV(ctx, ticketKey(b.ID), list)
	}
	lock.Unlock()
	if err != nil {
		return 0, 0, err
	}
	if !found && id != "all" {
		return 0, 0, model.E(model.ErrNotFound, "share link %s not found", id)
	}
	closed := 0
	for _, t := range hit {
		closed += m.closeTicketConns(t.ID, "[boxmes] share link revoked")
	}
	if len(hit) > 0 {
		m.Emit(ctx, p.Actor(), b.ID, b.AgentID, "box.desktop", fmt.Sprintf("%d share link(s) revoked, %d viewer(s) disconnected", len(hit), closed), nil)
	}
	return len(hit), closed, nil
}

// ShareStatus classifies a share token for the page / socket: 200 valid,
// 401 unknown, 410 revoked or expired.
func (m *Manager) LookupShare(ctx context.Context, raw, boxID string) (*model.AccessTicket, *model.Box, int) {
	tb := shareBoxID(raw)
	if tb == "" || (boxID != "" && tb != boxID) {
		return nil, nil, 401
	}
	want := hashToken(raw)
	var t *model.AccessTicket
	for _, x := range m.loadTickets(ctx, tb) {
		if x.Purpose == model.PurposeShare && subtle.ConstantTimeCompare([]byte(x.Hash), []byte(want)) == 1 {
			t = x
		}
	}
	if t == nil {
		return nil, nil, 401
	}
	if t.RevokedAt != nil || time.Now().After(t.ExpiresAt) {
		return t, nil, 410
	}
	b, err := m.Store.GetBox(ctx, tb)
	if err != nil || !b.Active() {
		return t, nil, 410
	}
	if _, err := m.principalForKey(ctx, t.KeyID); err != nil {
		return t, nil, 410
	}
	return t, b, 200
}

// UseShare counts one viewer connection.
func (m *Manager) UseShare(ctx context.Context, t *model.AccessTicket) {
	lock := m.boxLock(t.BoxID)
	lock.Lock()
	defer lock.Unlock()
	list := m.loadTickets(ctx, t.BoxID)
	now := time.Now().UTC()
	for _, x := range list {
		if x.ID == t.ID {
			x.Uses++
			if x.RedeemedAt == nil {
				x.RedeemedAt = &now
			}
		}
	}
	m.Store.PutKV(ctx, ticketKey(t.BoxID), list)
}

func shareBoxID(raw string) string {
	if !strings.HasPrefix(raw, ShareTokenPrefix) {
		return ""
	}
	rest := raw[len(ShareTokenPrefix):]
	i := strings.LastIndexByte(rest, '_')
	if i <= 0 || len(rest)-i-1 != 40 {
		return ""
	}
	return rest[:i]
}

// PrincipalForTicket rebuilds the principal that issued a ticket / share.
func (m *Manager) PrincipalForTicket(ctx context.Context, t *model.AccessTicket) (*Principal, error) {
	return m.principalForKey(ctx, t.KeyID)
}

// PeekTicket checks a ticket without consuming it (the noVNC page loads with
// the ticket its socket then redeems).
func (m *Manager) PeekTicket(ctx context.Context, raw, purpose, boxID string) bool {
	tb := TicketBoxID(raw)
	if tb == "" || (boxID != "" && tb != boxID) {
		return false
	}
	want := hashToken(raw)
	for _, x := range m.loadTickets(ctx, tb) {
		if subtle.ConstantTimeCompare([]byte(x.Hash), []byte(want)) == 1 {
			return x.Purpose == purpose && x.RevokedAt == nil && time.Now().Before(x.ExpiresAt) && (x.RedeemedAt == nil || x.Reusable)
		}
	}
	return false
}
