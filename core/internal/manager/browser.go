package manager

// v1.11 on-demand browser (CDP) and desktop (VNC) inside a box.
//
// Core drives both over the box link: a POSIX script (addonscripts.go) run
// with proc.open finds or installs Chromium / Xvfb + x11vnc, starts them bound
// to 127.0.0.1, and core reaches them with OpenStream (the same plumbing as
// box forward). Works on every tier with a live link (boxd or the in-box
// ingress agent). A watcher stops the browser after BOXAZ_BROWSER_IDLE (10
// min) and the desktop after BOXAZ_DESKTOP_IDLE (15 min) without clients;
// sleep and delete end them with the box.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"boxaz/core/internal/hostlink"
	"boxaz/core/internal/model"
)

const (
	defaultCDPPort = 9222
	defaultVNCPort = 5900
	desktopDisplay = 99
)

type addonState struct {
	mu    sync.Mutex
	boxes map[string]*boxAddons
}

type boxAddons struct {
	mu sync.Mutex // serializes start / stop per box

	browserUp       bool
	browserHeadless bool
	browserSource   string // found | installed | existing
	browserBin      string
	browserStarted  time.Time
	browserLast     time.Time
	browserWatch    bool

	desktopUp      bool
	desktopDisplay string
	desktopW       int
	desktopH       int
	desktopVNC     string
	desktopWM      string
	desktopStarted time.Time
	desktopLast    time.Time
	desktopWatch   bool
}

func (m *Manager) addonsOf(boxID string) *boxAddons {
	m.acc.addons.mu.Lock()
	defer m.acc.addons.mu.Unlock()
	if m.acc.addons.boxes == nil {
		m.acc.addons.boxes = map[string]*boxAddons{}
	}
	a := m.acc.addons.boxes[boxID]
	if a == nil {
		a = &boxAddons{}
		m.acc.addons.boxes[boxID] = a
	}
	return a
}

// forgetAddons drops the in-memory state (box slept or was deleted: the
// processes are gone with it).
func (m *Manager) forgetAddons(boxID string) {
	m.acc.addons.mu.Lock()
	delete(m.acc.addons.boxes, boxID)
	m.acc.addons.mu.Unlock()
}

// CDPPort / VNCPort are the in-box loopback ports (tests override).
func (m *Manager) CDPPort() int {
	if m.Cfg.BrowserPort > 0 {
		return m.Cfg.BrowserPort
	}
	return defaultCDPPort
}

func (m *Manager) VNCPort() int {
	if m.Cfg.VNCPort > 0 {
		return m.Cfg.VNCPort
	}
	return defaultVNCPort
}

func (m *Manager) browserIdle() time.Duration {
	if m.Cfg.BrowserIdle > 0 {
		return m.Cfg.BrowserIdle
	}
	return 10 * time.Minute
}

func (m *Manager) desktopIdle() time.Duration {
	if m.Cfg.DesktopIdle > 0 {
		return m.Cfg.DesktopIdle
	}
	return 15 * time.Minute
}

func parseKV(s string) map[string]string {
	out := map[string]string{}
	for _, l := range strings.Split(s, "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(l), "="); ok && k != "" {
			out[k] = v
		}
	}
	return out
}

// addonScript runs one of the in-box scripts and parses its key=value output.
func (m *Manager) addonScript(ctx context.Context, peer *hostlink.Peer, boxID, script string, env []string) (map[string]string, error) {
	var out bytes.Buffer
	env = append(env, "INSTALL="+m.browserInstallMode())
	stderr, code, err := boxRun(ctx, peer, boxID, script, env, nil, &out)
	if err != nil {
		return nil, err
	}
	kv := parseKV(out.String())
	if kv["error"] != "" || code != 0 {
		msg := kv["error"]
		if msg == "" {
			msg = fmt.Sprintf("script exited %d: %s", code, strings.TrimSpace(trunc200(stderr)))
		}
		if d := strings.TrimSpace(kv["detail"]); d != "" {
			msg += " (" + trunc200(d) + ")"
		}
		return kv, model.EH(model.ErrProviderFailed, "Check that the box can install packages (apt with root/sudo, or npx), or install chromium / xvfb x11vnc in the box image.", "%s", msg)
	}
	return kv, nil
}

func trunc200(s string) string {
	if len(s) > 200 {
		return s[:200] + "…"
	}
	return s
}

func (m *Manager) browserInstallMode() string {
	if strings.EqualFold(strings.TrimSpace(m.Cfg.BrowserInstall), "off") {
		return "off"
	}
	return "auto"
}

// boxHTTP does one HTTP/1.1 request to a loopback port in the box over a stream.
func boxHTTP(ctx context.Context, peer *hostlink.Peer, boxID string, port int, method, path string) (int, []byte, error) {
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	sc, err := peer.OpenStream(cctx, boxID, port)
	if err != nil {
		return 0, nil, err
	}
	defer sc.Close()
	sc.SetReadDeadline(time.Now().Add(10 * time.Second))
	req, _ := http.NewRequest(method, "http://127.0.0.1:"+strconv.Itoa(port)+path, nil)
	req.Host = "127.0.0.1:" + strconv.Itoa(port) // Chrome only answers Host: localhost / an IP
	req.Header.Set("Connection", "close")
	if err := req.Write(sc); err != nil {
		return 0, nil, err
	}
	resp, err := http.ReadResponse(bufio.NewReader(sc), req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	return resp.StatusCode, b, err
}

// CDPVersion reads /json/version of the box's browser.
func (m *Manager) CDPVersion(ctx context.Context, peer *hostlink.Peer, boxID string) (map[string]any, error) {
	st, body, err := boxHTTP(ctx, peer, boxID, m.CDPPort(), "GET", "/json/version")
	if err != nil {
		return nil, err
	}
	if st != 200 {
		return nil, fmt.Errorf("/json/version: HTTP %d", st)
	}
	var v map[string]any
	if err := json.Unmarshal(body, &v); err != nil {
		return nil, err
	}
	return v, nil
}

// BrowserState is GET /v1/boxes/{id}/browser (and part of the POST answer).
type BrowserState struct {
	BoxID     string         `json:"box_id"`
	Running   bool           `json:"running"`
	Headless  bool           `json:"headless"`
	Source    string         `json:"source,omitempty"` // found | installed | existing
	Binary    string         `json:"binary,omitempty"`
	Version   map[string]any `json:"version,omitempty"`
	Started   bool           `json:"started"`            // this call started it
	StartMS   int64          `json:"start_ms,omitempty"` // time to start (and install) when started
	Clients   int            `json:"clients"`
	IdleStopS int            `json:"idle_stop_s"`
	Port      int            `json:"port"` // in-box loopback port
	Note      string         `json:"note,omitempty"`
}

// EnsureBrowser starts the browser when CDP does not answer. headless=false
// puts it on the desktop's display when the desktop runs.
func (m *Manager) EnsureBrowser(ctx context.Context, b *model.Box, peer *hostlink.Peer, headless bool) (*BrowserState, error) {
	a := m.addonsOf(b.ID)
	a.mu.Lock()
	defer a.mu.Unlock()
	st := &BrowserState{BoxID: b.ID, Port: m.CDPPort(), IdleStopS: int(m.browserIdle().Seconds())}
	if v, err := m.CDPVersion(ctx, peer, b.ID); err == nil {
		st.Running, st.Version = true, v
		if !a.browserUp { // started outside core (user's own Chrome on that port, or before a core restart)
			a.browserUp, a.browserSource, a.browserHeadless = true, "existing", true
			if ua, _ := v["User-Agent"].(string); ua != "" && !strings.Contains(ua, "Headless") {
				a.browserHeadless = false
			}
		}
		st.Headless, st.Source, st.Binary = a.browserHeadless, a.browserSource, a.browserBin
		a.browserLast = time.Now()
		m.watchAddons(b.ID)
		return st, nil
	}
	start := time.Now()
	hl := "1"
	if !headless && a.desktopUp {
		hl = "0"
	}
	w, h := a.desktopW, a.desktopH
	if w == 0 {
		w, h = 1280, 800
	}
	cctx, cancel := context.WithTimeout(ctx, 6*time.Minute)
	defer cancel()
	kv, err := m.addonScript(cctx, peer, b.ID, browserStart, []string{"PORT=" + strconv.Itoa(m.CDPPort()), "HEADLESS=" + hl, "W=" + strconv.Itoa(w), "H=" + strconv.Itoa(h)})
	if err != nil {
		return nil, err
	}
	var v map[string]any
	for i := 0; i < 60; i++ {
		if v, err = m.CDPVersion(cctx, peer, b.ID); err == nil {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if err != nil {
		return nil, model.EH(model.ErrProviderFailed, "Retry; see $TMPDIR/.bx-access/browser.log in the box.", "browser started but CDP does not answer: %v", err)
	}
	a.browserUp, a.browserHeadless, a.browserBin = true, kv["headless"] != "0", kv["browser"]
	a.browserSource = kv["source"]
	if a.browserSource == "" {
		a.browserSource = "existing"
	}
	a.browserStarted, a.browserLast = time.Now(), time.Now()
	st.Running, st.Version, st.Headless, st.Source, st.Binary = true, v, a.browserHeadless, a.browserSource, a.browserBin
	if kv["state"] == "started" {
		st.Started, st.StartMS = true, time.Since(start).Milliseconds()
		m.Emit(ctx, "system", b.ID, b.AgentID, "box.browser", fmt.Sprintf("browser started (%s, %s, %d ms)", map[bool]string{true: "headless", false: "on the desktop"}[a.browserHeadless], a.browserSource, st.StartMS), nil)
	}
	m.watchAddons(b.ID)
	return st, nil
}

// stopBrowserLocked runs the stop script (caller holds a.mu).
func (m *Manager) stopBrowserLocked(ctx context.Context, a *boxAddons, b *model.Box, peer *hostlink.Peer, why string) error {
	_, err := m.addonScript(ctx, peer, b.ID, browserStop, nil)
	a.browserUp = false
	if err == nil {
		m.Emit(ctx, "system", b.ID, b.AgentID, "box.browser", "browser stopped ("+why+")", nil)
	}
	return err
}

// BrowserInput is POST /v1/boxes/{id}/browser.
type BrowserInput struct {
	TTLSeconds int  `json:"ttl_s,omitempty"`    // how long the CDP URL works (default 3600, ≤ 86400)
	Headless   bool `json:"headless,omitempty"` // force headless even when the desktop runs
}

// BrowserSession is the POST answer: a CDP endpoint (Browserbase-shaped).
type BrowserSession struct {
	*BrowserState
	CDPURL     string    `json:"cdp_url"`            // wss://<name>.<domain>/.boxaz/cdp?ticket=… (connectOverCDP / puppeteer.connect)
	HTTPURL    string    `json:"http_url,omitempty"` // https://…/.boxaz/cdp?ticket=… (connectOverCDP fetches /json/version)
	CoreURL    string    `json:"core_url"`
	TicketID   string    `json:"ticket_id"`
	ExpiresAt  time.Time `json:"expires_at"`
	LiveViewIn string    `json:"live_view,omitempty"` // the desktop shows it when not headless
}

// OpenBrowser wakes the box, ensures the browser and returns a CDP endpoint.
func (m *Manager) OpenBrowser(ctx context.Context, p *Principal, ref string, in BrowserInput) (*BrowserSession, error) {
	ttl := in.TTLSeconds
	if ttl == 0 {
		ttl = 3600
	}
	t, err := m.CreateTicket(ctx, p, ref, CreateTicketInput{Purpose: model.PurposeCDP, TTLSeconds: ttl})
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
	st, err := m.EnsureBrowser(ctx, b, peer, in.Headless)
	if err != nil {
		m.RevokeTicket(ctx, p, b.ID, t.ID)
		return nil, err
	}
	st.Clients = m.LiveAccess(b.ID)["cdp"]
	out := &BrowserSession{BrowserState: st, CDPURL: t.URL, CoreURL: t.CoreURL, TicketID: t.ID, ExpiresAt: t.ExpiresAt}
	if strings.HasPrefix(t.URL, "wss://") {
		out.HTTPURL = "https://" + strings.TrimPrefix(t.URL, "wss://")
	} else if strings.HasPrefix(t.URL, "ws://") {
		out.HTTPURL = "http://" + strings.TrimPrefix(t.URL, "ws://")
	}
	if !st.Headless {
		out.LiveViewIn = "desktop"
	}
	return out, nil
}

// BrowserStatus reports without waking the box or starting anything.
func (m *Manager) BrowserStatus(ctx context.Context, p *Principal, ref string) (*BrowserState, error) {
	b, err := m.ResolveBox(ctx, p, ref)
	if err != nil {
		return nil, err
	}
	a := m.addonsOf(b.ID)
	st := &BrowserState{BoxID: b.ID, Port: m.CDPPort(), IdleStopS: int(m.browserIdle().Seconds()), Clients: m.LiveAccess(b.ID)["cdp"]}
	peer, ok := m.linkPeer(b)
	if b.Status != model.StatusRunning || !ok {
		st.Note = "box is " + b.Status + "; POST /v1/boxes/{id}/browser wakes it and starts the browser"
		return st, nil
	}
	if v, err := m.CDPVersion(ctx, peer, b.ID); err == nil {
		st.Running, st.Version = true, v
	}
	a.mu.Lock()
	st.Headless, st.Source, st.Binary = a.browserHeadless, a.browserSource, a.browserBin
	a.mu.Unlock()
	return st, nil
}

// StopBrowser stops the box's browser and closes CDP clients.
func (m *Manager) StopBrowser(ctx context.Context, p *Principal, ref string) (map[string]any, error) {
	if err := p.Require(model.ScopeBoxExec); err != nil {
		return nil, err
	}
	b, err := m.ResolveBox(ctx, p, ref)
	if err != nil {
		return nil, err
	}
	m.closeKind(b.ID, "cdp", "[boxaz] browser stopped")
	peer, ok := m.linkPeer(b)
	if b.Status != model.StatusRunning || !ok {
		return map[string]any{"box_id": b.ID, "stopped": false, "note": "box is not running"}, nil
	}
	a := m.addonsOf(b.ID)
	a.mu.Lock()
	err = m.stopBrowserLocked(ctx, a, b, peer, "requested")
	a.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return map[string]any{"box_id": b.ID, "stopped": true}, nil
}

// closeKind closes the live connections of one kind on a box.
func (m *Manager) closeKind(boxID, kind, reason string) int {
	m.acc.mu.Lock()
	var cs []*liveConn
	for c := range m.acc.byBox[boxID] {
		if c.kind == kind {
			cs = append(cs, c)
		}
	}
	m.acc.mu.Unlock()
	for _, c := range cs {
		c.close(reason)
	}
	return len(cs)
}

// watchAddons starts (once per box) the idle watcher for browser + desktop.
func (m *Manager) watchAddons(boxID string) {
	a := m.addonsOf(boxID)
	m.acc.addons.mu.Lock()
	if a.browserWatch {
		m.acc.addons.mu.Unlock()
		return
	}
	a.browserWatch = true
	m.acc.addons.mu.Unlock()
	go func() {
		tick := 30 * time.Second
		for _, d := range []time.Duration{m.browserIdle(), m.desktopIdle()} {
			if d/4 < tick {
				tick = d / 4
			}
		}
		if tick < 200*time.Millisecond {
			tick = 200 * time.Millisecond
		}
		defer func() {
			m.acc.addons.mu.Lock()
			a.browserWatch = false
			m.acc.addons.mu.Unlock()
		}()
		for {
			time.Sleep(tick)
			b, err := m.Store.GetBox(context.Background(), boxID)
			if err != nil || !b.Active() || b.Status != model.StatusRunning {
				m.forgetAddons(boxID)
				return
			}
			live := m.LiveAccess(boxID)
			now := time.Now()
			a.mu.Lock()
			if live["cdp"] > 0 {
				a.browserLast = now
			}
			if live["vnc"] > 0 {
				a.desktopLast = now
			}
			peer, ok := m.linkPeer(b)
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			if ok && a.browserUp && now.Sub(a.browserLast) > m.browserIdle() {
				m.stopBrowserLocked(ctx, a, b, peer, fmt.Sprintf("idle %s", m.browserIdle()))
			}
			if ok && a.desktopUp && now.Sub(a.desktopLast) > m.desktopIdle() {
				m.stopDesktopLocked(ctx, a, b, peer, fmt.Sprintf("idle %s", m.desktopIdle()))
			}
			cancel()
			done := !a.browserUp && !a.desktopUp
			a.mu.Unlock()
			if done {
				return
			}
		}
	}()
}

// TouchAddon marks browser / desktop use (CDP or VNC traffic) now.
func (m *Manager) TouchAddon(boxID, kind string) {
	a := m.addonsOf(boxID)
	a.mu.Lock()
	if kind == "cdp" {
		a.browserLast = time.Now()
	} else {
		a.desktopLast = time.Now()
	}
	a.mu.Unlock()
}
