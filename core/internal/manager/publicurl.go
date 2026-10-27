package manager

// Public URLs for ports of sandbox boxes whose provider has no preview host
// (e.g. Prized). GET/POST /v1/boxes/{id}/ports, `box url` and MCP box_url walk
// one fallback chain:
//
//  1. provider-preview — the provider's own public host (E2B, Novita, …).
//  2. ingress — core publishes the port at <BOXAZ_PUBLIC_URL>/x/<key>-<port>/
//     and forwards each request through its existing ingress Proxy. T2 boxes
//     already have boxd dialled in; T3 (shim) boxes get a small in-box agent
//     (`boxd ingress`, downloaded from core) that dials out to
//     /v1/ingress/connect. Used when BOXAZ_PUBLIC_URL looks internet-facing
//     and the agent actually connects from inside the sandbox.
//  3. tunnel — a Cloudflare quick tunnel (cloudflared, downloaded on demand
//     inside the sandbox, free, no account) when core is not reachable.
//     Disabled by settings.disable_tunnel or BOXAZ_DISABLE_TUNNEL=1.
//
// After a wake the exposures are re-established (agent restarted / tunnel
// re-checked, new URL if it had to restart); box down drops them. Everything
// sent into the sandbox uses neutral names (.boxd dir, `boxd ingress`,
// cloudflared): commands travel through the provider's exec API.

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"boxaz/core/internal/boxd"
	"boxaz/core/internal/hostlink"
	"boxaz/core/internal/model"
	"boxaz/core/internal/proto"
	"boxaz/core/internal/store"
)

// Kinds reported by the ports API.
const (
	KindPreview = "provider-preview"
	KindIngress = "ingress"
	KindTunnel  = "tunnel"
	KindNone    = "none"
	KindGateway = "gateway" // v1.7: token-protected port, only via the Boxaz gateway
)

// Caveats shown with each kind.
var (
	caveatIngress = "path-prefixed URL (/x/<key>-<port>/): the app must use relative links (X-Forwarded-Prefix is sent); streamed through core (WebSocket and SSE pass through); a sleeping box is woken by the first request (held up to settings.wake.hold_s, then 503 + Retry-After)"
	caveatGateway = "Boxaz gateway URL: path-prefixed (/x/<key>-<port>/) unless a gateway domain is set (host-based <key>-<port>.<domain>); WebSocket and SSE pass through; a sleeping box is woken by the first request"
	// caveatHostGateway replaces caveatIngress when a gateway domain is set.
	caveatHostGateway = "served through core at a host-based gateway URL (<key>-<port>.<domain>, no path prefix); WebSocket and SSE pass through; a sleeping box is woken by the first request (held up to settings.wake.hold_s, then 503 + Retry-After)"
	caveatTunnel      = "Cloudflare quick tunnel (trycloudflare.com): free and accountless but without uptime guarantee; the URL changes if the tunnel has to restart (e.g. a wake that lost processes); the first request can take a few seconds"
)

// ingressState holds the live in-box ingress agents (box id → peer).
type ingressState struct {
	mu         sync.Mutex
	peers      map[string]*hostlink.Peer
	locks      sync.Map      // box id → *sync.Mutex (exposure setup)
	agentWait  time.Duration // how long a fresh agent may take to connect (default 25 s)
	tunnelWait time.Duration // how long the tunnel may take to print its URL (default 40 s)
}

func (m *Manager) ingressPeer(boxID string) (*hostlink.Peer, bool) {
	m.ing.mu.Lock()
	defer m.ing.mu.Unlock()
	p, ok := m.ing.peers[boxID]
	return p, ok
}

func (m *Manager) exposeLock(boxID string) *sync.Mutex {
	l, _ := m.ing.locks.LoadOrStore(boxID, &sync.Mutex{})
	return l.(*sync.Mutex)
}

func (m *Manager) hmacHex(domain, id string, n int) string {
	h := hmac.New(sha256.New, []byte(m.Cfg.URLKey))
	h.Write([]byte(domain + ":" + id))
	return hex.EncodeToString(h.Sum(nil))[:n]
}

// IngressAgentToken authenticates the in-box agent of one box.
func (m *Manager) IngressAgentToken(boxID string) string {
	return m.hmacHex("ingress-agent", boxID, 40)
}

// exposureKey is the unguessable path segment of a box's ingress URLs.
func (m *Manager) exposureKey(boxID string) string { return m.hmacHex("expose", boxID, 20) }

// ingressURL is the public ingress URL of a port of a box.
func (m *Manager) ingressURL(b *model.Box, port int) string {
	return strings.TrimRight(m.Cfg.PublicURL, "/") + "/x/" + m.exposureKey(b.ID) + "-" + strconv.Itoa(port) + "/"
}

// CoreLooksPublic reports whether BOXAZ_PUBLIC_URL can be reached from the
// internet (not empty, not localhost / private / link-local). The in-box
// agent connecting is the real proof; this only avoids a pointless attempt.
func CoreLooksPublic(public string) (bool, string) {
	if public == "" {
		return false, "BOXAZ_PUBLIC_URL is not set"
	}
	u, err := url.Parse(public)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return false, "BOXAZ_PUBLIC_URL is not an http(s) URL"
	}
	h := strings.ToLower(u.Hostname())
	if h == "localhost" || strings.HasSuffix(h, ".localhost") || strings.HasSuffix(h, ".local") || strings.HasSuffix(h, ".internal") || strings.HasSuffix(h, ".lan") || !strings.Contains(h, ".") && net.ParseIP(h) == nil {
		return false, "BOXAZ_PUBLIC_URL (" + h + ") is a local name"
	}
	if ip := net.ParseIP(h); ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified()) {
		return false, "BOXAZ_PUBLIC_URL (" + h + ") is a private address"
	}
	return true, ""
}

func (m *Manager) tunnelDisabled(ctx context.Context) bool {
	return m.Cfg.DisableTunnel || m.Settings(ctx).DisableTunnel
}

// ---- in-box agent connection ----

var ingressUpgrader = websocket.Upgrader{ReadBufferSize: 32 << 10, WriteBufferSize: 32 << 10}

// ServeIngressAgent accepts the WebSocket of an in-box ingress agent
// (GET /v1/ingress/connect?box=<id>, Bearer = IngressAgentToken(box)).
func (m *Manager) ServeIngressAgent(w http.ResponseWriter, r *http.Request) {
	boxID := r.URL.Query().Get("box")
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if boxID == "" || !hmac.Equal([]byte(tok), []byte(m.IngressAgentToken(boxID))) {
		http.Error(w, `{"error":{"code":"UNAUTHORIZED","message":"bad ingress token"}}`, http.StatusUnauthorized)
		return
	}
	b, err := m.Store.GetBox(r.Context(), boxID)
	if err != nil || !b.Active() {
		http.Error(w, `{"error":{"code":"NOT_FOUND","message":"box is gone"}}`, http.StatusNotFound)
		return
	}
	ws, err := ingressUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	p := hostlink.NewPeer(ws)
	ws.SetPongHandler(func(string) error { return nil })
	p.OnFrame = func(f proto.Frame) {
		if f.Type == proto.TReq {
			p.Reply(f, nil, &proto.Error{Code: "INVALID", Message: "core accepts no requests"})
		}
	}
	m.ing.mu.Lock()
	if m.ing.peers == nil {
		m.ing.peers = map[string]*hostlink.Peer{}
	}
	if old, ok := m.ing.peers[boxID]; ok {
		old.Close()
	}
	m.ing.peers[boxID] = p
	m.ing.mu.Unlock()
	go p.KeepAlive(25 * time.Second)
	_ = p.ReadLoop()
	m.ing.mu.Lock()
	if m.ing.peers[boxID] == p {
		delete(m.ing.peers, boxID)
	}
	m.ing.mu.Unlock()
}

func (m *Manager) waitIngressPeer(ctx context.Context, boxID string, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for {
		if _, ok := m.ingressPeer(boxID); ok {
			return true
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return false
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// dropExposures forgets a box's public URLs and closes its agent (box down).
func (m *Manager) dropExposures(boxID string) {
	m.ing.mu.Lock()
	if p, ok := m.ing.peers[boxID]; ok {
		p.Close()
		delete(m.ing.peers, boxID)
	}
	m.ing.mu.Unlock()
	m.ing.locks.Delete(boxID)
}

// ---- proxy ----

// BoxForIngressPath resolves the /x/<key>-<port> path segment.
func (m *Manager) BoxForIngressPath(ctx context.Context, seg string) (*model.Box, int, error) {
	i := strings.LastIndexByte(seg, '-')
	if i <= 0 {
		return nil, 0, model.E(model.ErrNotFound, "no box at this address")
	}
	key, ps := seg[:i], seg[i+1:]
	port, err := strconv.Atoi(ps)
	if err != nil || port < 1 || port > 65535 {
		return nil, 0, model.E(model.ErrNotFound, "no box at this address")
	}
	boxes, err := m.Store.ListBoxes(ctx, store.BoxFilter{Statuses: []string{model.StatusRunning, model.StatusSleeping}})
	if err != nil {
		return nil, 0, err
	}
	for _, b := range boxes {
		if hmac.Equal([]byte(m.exposureKey(b.ID)), []byte(key)) {
			for _, p := range b.Ports {
				if p == port {
					return b, port, nil
				}
			}
			return nil, 0, model.E(model.ErrNotFound, "port %d is not exposed", port)
		}
	}
	return nil, 0, model.E(model.ErrNotFound, "no box at this address")
}

// ProxyPort forwards one HTTP request to a port of a box: through the box's
// in-box ingress agent when it has one, otherwise through its host (boxd).
func (m *Manager) ProxyPort(ctx context.Context, b *model.Box, port int, method, path string, hdr map[string][]string, body []byte) (*proto.HTTPResult, error) {
	if b.Status == model.StatusSleeping {
		sys := &Principal{Key: &model.APIKey{Scopes: []string{model.ScopeAdmin}}, Admin: true}
		if _, err := m.Wake(ctx, sys, b.ID); err != nil {
			return nil, err
		}
	}
	m.touch(ctx, b)
	var res proto.HTTPResult
	cctx, cancel := context.WithTimeout(ctx, 70*time.Second)
	defer cancel()
	params := proto.HTTPParams{BoxID: b.ID, Port: port, Method: method, Path: path, Headers: hdr, Body: body}
	if m.Hub.IsVirtual(b.HostID) {
		// T3 shim: only the in-box agent can reach the port; after a wake it
		// reconnects within a few seconds.
		p, ok := m.ingressPeer(b.ID)
		if !ok && m.waitIngressPeer(ctx, b.ID, 15*time.Second) {
			p, ok = m.ingressPeer(b.ID)
		}
		if !ok {
			go m.reexpose(b.ID)
			return nil, model.EH(model.ErrProviderFailed, "Retry in a few seconds; the in-box ingress agent is (re)connecting.", "ingress agent of %s is not connected", b.ID)
		}
		if err := p.Call(cctx, proto.MHTTP, params, &res); err != nil {
			return nil, mapHostErr(err)
		}
		return &res, nil
	}
	if err := m.Hub.Call(cctx, b.HostID, proto.MHTTP, params, &res); err != nil {
		return nil, mapHostErr(err)
	}
	return &res, nil
}

// ---- establishing ----

func (m *Manager) boxExec(ctx context.Context, b *model.Box, cmd string, timeoutS int) (*boxd.ExecOut, error) {
	var out boxd.ExecOut
	cctx, cancel := context.WithTimeout(ctx, time.Duration(timeoutS+30)*time.Second)
	defer cancel()
	if err := m.Hub.Call(cctx, b.HostID, proto.MExec, proto.ExecParams{BoxID: b.ID, Command: cmd, Cwd: "/", TimeoutS: timeoutS, TailLines: 40}, &out); err != nil {
		return nil, mapHostErr(err)
	}
	return &out, nil
}

// archDetect is shared shell: $A = amd64|arm64.
const archDetect = `A=$(uname -m); case "$A" in x86_64|amd64) A=amd64;; aarch64|arm64) A=arm64;; esac; `

// fetchSh downloads $U to $F (curl or wget) atomically.
const fetchSh = `if command -v curl >/dev/null 2>&1; then curl -fsSL "$U" -o "$F.tmp"; else wget -qO "$F.tmp" "$U"; fi && chmod +x "$F.tmp" && mv "$F.tmp" "$F"; `

// IngressAgentScript starts (idempotently) the in-box ingress agent.
func IngressAgentScript(boxdURL, coreWS, boxID, token string) string {
	var b strings.Builder
	b.WriteString(`D="$HOME/.boxd"; mkdir -p "$D"; ` + archDetect)
	b.WriteString(`F="$D/boxd"; U=` + shq(boxdURL) + `-linux-$A; [ -x "$F" ] || { ` + fetchSh + `} || { echo download-failed; exit 4; }; `)
	b.WriteString(`if [ -f "$D/ingress.pid" ] && kill -0 "$(cat "$D/ingress.pid")" 2>/dev/null; then echo agent-running; exit 0; fi; `)
	b.WriteString(`BOXD_INGRESS_TOKEN=` + shq(token) + ` nohup setsid "$F" ingress --dial ` + shq(coreWS) + ` --box ` + shq(boxID) + ` >"$D/ingress.log" 2>&1 </dev/null & echo $! >"$D/ingress.pid"; echo agent-started`)
	return b.String()
}

// TunnelScript starts (idempotently) a cloudflared quick tunnel to a port and
// prints URL=<https://…trycloudflare.com> once it is up.
func TunnelScript(port int, waitS int) string {
	p := strconv.Itoa(port)
	var b strings.Builder
	b.WriteString(`D="$HOME/.boxd"; mkdir -p "$D"; ` + archDetect)
	b.WriteString(`F="$D/cloudflared"; U="https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-$A"; [ -x "$F" ] || { ` + fetchSh + `} || { echo download-failed; exit 4; }; `)
	b.WriteString(`L="$D/tunnel-` + p + `.log"; PF="$D/tunnel-` + p + `.pid"; `)
	b.WriteString(`if [ -f "$PF" ] && kill -0 "$(cat "$PF")" 2>/dev/null && grep -q 'trycloudflare.com' "$L" 2>/dev/null; then echo tunnel-running; else `)
	b.WriteString(`: >"$L"; nohup setsid "$F" tunnel --no-autoupdate --url http://127.0.0.1:` + p + ` >"$L" 2>&1 </dev/null & echo $! >"$PF"; echo tunnel-started; fi; `)
	b.WriteString(`i=0; while [ $i -lt ` + strconv.Itoa(waitS*2) + ` ]; do T=$(grep -o 'https://[a-z0-9-]*\.trycloudflare\.com' "$L" | head -1); if [ -n "$T" ]; then echo "URL=$T"; exit 0; fi; sleep 0.5; i=$((i+1)); done; echo tunnel-timeout; tail -5 "$L"; exit 3`)
	return b.String()
}

var tunnelURLRe = regexp.MustCompile(`URL=(https://[a-z0-9-]+\.trycloudflare\.com)`)

// startIngress makes port reachable through core. T2 boxes already have
// boxd connected, so nothing has to run in the box.
func (m *Manager) startIngress(ctx context.Context, b *model.Box) error {
	if !m.Hub.IsVirtual(b.HostID) {
		if !m.Hub.Connected(b.HostID) {
			return fmt.Errorf("host %s is not connected", b.HostID)
		}
		return nil
	}
	if _, ok := m.ingressPeer(b.ID); ok {
		return nil
	}
	script := IngressAgentScript(m.boxdURL(), ingressWS(m.Cfg.PublicURL), b.ID, m.IngressAgentToken(b.ID))
	out, err := m.boxExec(ctx, b, script, 90)
	if err != nil {
		return err
	}
	if out.ExitCode != 0 {
		return fmt.Errorf("agent start exit %d: %s", out.ExitCode, trunc(strings.TrimSpace(out.StdoutTail+" "+out.StderrTail), 200))
	}
	wait := m.ing.agentWait
	if wait == 0 {
		wait = 25 * time.Second
	}
	if !m.waitIngressPeer(ctx, b.ID, wait) {
		// not reachable from the sandbox: stop the agent so it does not retry forever
		m.boxExec(context.Background(), b, `D="$HOME/.boxd"; [ -f "$D/ingress.pid" ] && kill "$(cat "$D/ingress.pid")" 2>/dev/null; rm -f "$D/ingress.pid"; tail -3 "$D/ingress.log" 2>/dev/null`, 20)
		return fmt.Errorf("the in-box agent could not connect to %s within %s", m.Cfg.PublicURL, wait)
	}
	return nil
}

func ingressWS(public string) string {
	u := strings.TrimRight(public, "/")
	switch {
	case strings.HasPrefix(u, "https://"):
		u = "wss://" + u[8:]
	case strings.HasPrefix(u, "http://"):
		u = "ws://" + u[7:]
	}
	return u + "/v1/ingress/connect"
}

// startTunnel runs the quick tunnel in the box and returns its URL.
func (m *Manager) startTunnel(ctx context.Context, b *model.Box, port int) (string, error) {
	wait := m.ing.tunnelWait
	if wait == 0 {
		wait = 40 * time.Second
	}
	out, err := m.boxExec(ctx, b, TunnelScript(port, int(wait/time.Second)), int(wait/time.Second)+90)
	if err != nil {
		return "", err
	}
	if mm := tunnelURLRe.FindStringSubmatch(out.StdoutTail); mm != nil {
		return mm[1] + "/", nil
	}
	return "", fmt.Errorf("tunnel did not come up (exit %d): %s", out.ExitCode, trunc(strings.TrimSpace(out.StdoutTail+" "+out.StderrTail), 200))
}

var probeClient = &http.Client{Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

// probeURL waits until the public URL answers through the edge (any status
// except Cloudflare's 530 "tunnel not found" / DNS errors).
func (m *Manager) probeURL(ctx context.Context, u string, d time.Duration) bool {
	if m.Cfg.PublicURLProbe != nil {
		return m.Cfg.PublicURLProbe(ctx, u)
	}
	deadline := time.Now().Add(d)
	for {
		req, _ := http.NewRequestWithContext(ctx, "GET", u, nil)
		if resp, err := probeClient.Do(req); err == nil {
			resp.Body.Close()
			if resp.StatusCode != 530 {
				return true
			}
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return false
		}
		time.Sleep(time.Second)
	}
}

// ensurePublic returns the public URL of a port on a sandbox box without a
// provider preview, establishing it when needed (fallback steps 2 and 3).
func (m *Manager) ensurePublic(ctx context.Context, b *model.Box, port int, force bool) PortView {
	l := m.exposeLock(b.ID)
	l.Lock()
	defer l.Unlock()
	if cur, err := m.Store.GetBox(ctx, b.ID); err == nil {
		b = cur
	}
	v := PortView{Port: port, Kind: KindNone}
	if b.Status == model.StatusSleeping {
		if e := b.ExposureFor(port); e != nil {
			v.URL, v.Kind, v.Public = e.URL, e.Kind, true
			v.Caveats = []string{caveatFor(e.Kind)}
			v.Note = "box is sleeping: the URL is re-checked when it wakes"
			return v
		}
		v.Note = "wake the box to get a public URL"
		return v
	}
	if b.Status != model.StatusRunning {
		v.Note = "box is " + b.Status
		return v
	}
	now := time.Now().UTC()
	// existing exposure still healthy?
	if e := b.ExposureFor(port); e != nil && !force {
		switch e.Kind {
		case KindIngress:
			ok := m.Hub.Connected(b.HostID)
			if m.Hub.IsVirtual(b.HostID) {
				_, ok = m.ingressPeer(b.ID)
			}
			if ok {
				return m.exposedView(e)
			}
		case KindTunnel:
			if now.Sub(e.CheckedAt) < time.Minute {
				return m.exposedView(e)
			}
		}
	}
	var caveats []string
	// 2. ingress through core
	if ok, why := CoreLooksPublic(m.Cfg.PublicURL); !ok && !(m.Cfg.AssumePublicURL && m.Cfg.PublicURL != "") && m.Hub.IsVirtual(b.HostID) {
		caveats = append(caveats, "ingress skipped: "+why)
	} else if err := m.startIngress(ctx, b); err != nil {
		caveats = append(caveats, "ingress failed: "+err.Error())
	} else {
		e := m.recordExposure(ctx, b, port, KindIngress, m.ingressURL(b, port), now)
		v = m.exposedView(e)
		v.Caveats = append(v.Caveats, caveats...)
		return v
	}
	// 3. in-box tunnel
	if m.tunnelDisabled(ctx) {
		caveats = append(caveats, "tunnel fallback is disabled (settings.disable_tunnel / BOXAZ_DISABLE_TUNNEL)")
	} else if u, err := m.startTunnel(ctx, b, port); err != nil {
		caveats = append(caveats, "tunnel failed: "+err.Error())
	} else {
		verified := m.probeURL(ctx, u, 20*time.Second)
		e := m.recordExposure(ctx, b, port, KindTunnel, u, now)
		v = m.exposedView(e)
		if !verified {
			v.Caveats = append(v.Caveats, "the tunnel URL did not answer yet from core; retry in a few seconds")
		}
		v.Caveats = append(v.Caveats, caveats...)
		return v
	}
	m.clearExposure(ctx, b, port)
	v.Caveats = caveats
	v.Note = "no public URL: the provider has no previews, core is not reachable from the sandbox and the tunnel fallback is unavailable; use a provider with ports (E2B, Novita, Daytona) or a pool host"
	return v
}

func caveatFor(kind string) string {
	if kind == KindTunnel {
		return caveatTunnel
	}
	return caveatIngress
}

func (m *Manager) exposedView(e *model.Exposure) PortView {
	v := PortView{Port: e.Port, URL: e.URL, Kind: e.Kind, Public: true, Caveats: []string{caveatFor(e.Kind)}}
	if e.Kind == KindIngress {
		v.Note = "served by Boxaz ingress through core; serve on 0.0.0.0 or 127.0.0.1:" + strconv.Itoa(e.Port)
	} else {
		v.Note = "served by an in-box Cloudflare quick tunnel to 127.0.0.1:" + strconv.Itoa(e.Port)
	}
	return v
}

// recordExposure persists an exposure (and the box URL for the first port).
func (m *Manager) recordExposure(ctx context.Context, b *model.Box, port int, kind, u string, now time.Time) *model.Exposure {
	lock := m.boxLock(b.ID)
	lock.Lock()
	defer lock.Unlock()
	if cur, err := m.Store.GetBox(ctx, b.ID); err == nil {
		*b = *cur
	}
	changed := true
	if e := b.ExposureFor(port); e != nil {
		changed = e.URL != u || e.Kind != kind
		if changed {
			e.Since = now
		}
		e.Kind, e.URL, e.CheckedAt = kind, u, now
	} else {
		b.Exposures = append(b.Exposures, model.Exposure{Port: port, Kind: kind, URL: u, Since: now, CheckedAt: now})
	}
	if len(b.Ports) > 0 && b.Ports[0] == port {
		b.URL = u
	}
	m.Store.PutBox(ctx, b)
	if changed {
		m.Emit(ctx, "system", b.ID, b.AgentID, "box.port", fmt.Sprintf("port %d public via %s: %s", port, kind, u), nil)
		m.publishBox(m.decorateBox(ctx, b))
	}
	return b.ExposureFor(port)
}

func (m *Manager) clearExposure(ctx context.Context, b *model.Box, port int) {
	lock := m.boxLock(b.ID)
	lock.Lock()
	defer lock.Unlock()
	cur, err := m.Store.GetBox(ctx, b.ID)
	if err != nil || cur.ExposureFor(port) == nil {
		return
	}
	kept := cur.Exposures[:0]
	for _, e := range cur.Exposures {
		if e.Port != port {
			kept = append(kept, e)
		}
	}
	cur.Exposures = kept
	if len(cur.Ports) > 0 && cur.Ports[0] == port {
		cur.URL = ""
	}
	m.Store.PutBox(ctx, cur)
	*b = *cur
}

// reexpose re-establishes every exposure of a box (after a wake): the agent
// is restarted if it died, the tunnel re-checked (new URL if it restarted).
func (m *Manager) reexpose(boxID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	b, err := m.Store.GetBox(ctx, boxID)
	if err != nil || b.Status != model.StatusRunning || len(b.Exposures) == 0 {
		return
	}
	for _, e := range append([]model.Exposure(nil), b.Exposures...) {
		m.ensurePublic(ctx, b, e.Port, true)
	}
}

// shq single-quotes s for sh.
func shq(s string) string { return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'" }
