package manager

// v1.7 Boxaz gateway: one stable URL per box+port (<public>/x/<key>-<port>/,
// or <key>-<port>.<BOXAZ_GATEWAY_DOMAIN>) for every provider. The HTTP side
// lives in internal/api/gateway.go; this file resolves where a request goes
// and wakes sleeping boxes:
//
//   - stream:  raw TCP stream over the box's live link — boxd (T1/T2) or the
//     in-box ingress agent (T3 without previews, e.g. Prized). Unbuffered, so
//     WebSocket and SSE pass through.
//   - preview: reverse proxy to the provider's preview host (E2B, Novita, …)
//     for T3 boxes that have one.
//   - buffered: the v1.6 http.do RPC, only for agents that predate streams.
//
// Wake: a request to a sleeping box starts one background resume per box
// (singleflight); the API layer holds the request for the hold budget.

import (
	"context"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"boxaz/core/internal/hostlink"
	"boxaz/core/internal/model"
)

type gatewayState struct {
	wakes      sync.Map // box id → *WakeOp
	transports sync.Map // "box:port" → *http.Transport (stream upstream)
	previews   sync.Map // "box:port" → previewEntry
	touched    sync.Map // box id → time.Time
}

type previewEntry struct {
	url   string
	until time.Time
}

// WakeOp is one in-flight wake of a box.
type WakeOp struct {
	Done chan struct{}
	Err  error
}

// Upstream kinds.
const (
	UpstreamStream   = "stream"
	UpstreamPreview  = "preview"
	UpstreamBuffered = "buffered"
)

// Upstream tells the gateway how to reach a box port.
type Upstream struct {
	Kind       string
	PreviewURL string          // preview: https://<port>-<id>.<domain>
	Transport  *http.Transport // stream: dials the box port over the link
}

// WakePolicy is the effective wake configuration.
type WakePolicy struct {
	Hold         time.Duration
	RetryAfterS  int
	WakeOnProbes bool
}

// WakePolicy reads settings (zero = defaults) and BOXAZ_WAKE_HOLD.
func (m *Manager) WakePolicy(ctx context.Context) WakePolicy {
	w := m.Settings(ctx).Wake
	p := WakePolicy{Hold: 10 * time.Second, RetryAfterS: 5, WakeOnProbes: w.WakeOnProbes}
	if m.Cfg.WakeHold > 0 {
		p.Hold = m.Cfg.WakeHold
	}
	if w.HoldS > 0 {
		p.Hold = time.Duration(w.HoldS) * time.Second
	}
	if w.RetryAfterS > 0 {
		p.RetryAfterS = w.RetryAfterS
	}
	return p
}

// GatewayURL is the stable Boxaz URL of a box port. Without BOXAZ_PUBLIC_URL
// or BOXAZ_GATEWAY_DOMAIN it is the core-relative path /x/<key>-<port>/.
func (m *Manager) GatewayURL(b *model.Box, port int) string {
	seg := m.exposureKey(b.ID) + "-" + strconv.Itoa(port)
	if m.Cfg.GatewayDomain != "" {
		scheme := "https://"
		if strings.HasPrefix(m.Cfg.PublicURL, "http://") {
			scheme = "http://"
		}
		return scheme + seg + "." + strings.TrimPrefix(m.Cfg.GatewayDomain, ".") + "/"
	}
	if m.Cfg.PublicURL == "" {
		return "/x/" + seg + "/"
	}
	return strings.TrimRight(m.Cfg.PublicURL, "/") + "/x/" + seg + "/"
}

// GatewayPath is the core-relative path of a box port's gateway URL.
func (m *Manager) GatewayPath(b *model.Box, port int) string {
	return "/x/" + m.exposureKey(b.ID) + "-" + strconv.Itoa(port) + "/"
}

// GatewayWake starts (or joins) the background wake of a box.
func (m *Manager) GatewayWake(boxID string) *WakeOp {
	op := &WakeOp{Done: make(chan struct{})}
	if cur, loaded := m.gw.wakes.LoadOrStore(boxID, op); loaded {
		return cur.(*WakeOp)
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		sys := &Principal{Key: &model.APIKey{Scopes: []string{model.ScopeAdmin}}, Admin: true}
		b, err := m.Store.GetBox(ctx, boxID)
		if err == nil {
			m.Emit(ctx, "gateway", b.ID, b.AgentID, "box.wake_request", "request to a sleeping box: waking it", nil)
		}
		_, op.Err = m.Wake(ctx, sys, boxID)
		if op.Err != nil && b != nil {
			m.Emit(ctx, "gateway", b.ID, b.AgentID, "box.wake_failed", "wake on request failed: "+op.Err.Error(), nil)
		}
		m.gw.wakes.CompareAndDelete(boxID, op) // a later request starts a fresh wake
		close(op.Done)
	}()
	return op
}

// GatewayTouch records gateway activity (throttled) so idle sleep sees traffic.
func (m *Manager) GatewayTouch(ctx context.Context, boxID string) {
	now := time.Now()
	if t, ok := m.gw.touched.Load(boxID); ok && now.Sub(t.(time.Time)) < 15*time.Second {
		return
	}
	m.gw.touched.Store(boxID, now)
	lock := m.boxLock(boxID)
	lock.Lock()
	defer lock.Unlock()
	if b, err := m.Store.GetBox(ctx, boxID); err == nil && b.Active() {
		b.LastActiveAt = now.UTC()
		m.Store.PutBox(ctx, b)
	}
}

// linkPeer returns the live link that can open streams to a box port.
func (m *Manager) linkPeer(b *model.Box) (*hostlink.Peer, bool) {
	if m.Hub.IsVirtual(b.HostID) {
		return m.ingressPeer(b.ID)
	}
	return m.Hub.Peer(b.HostID)
}

func (m *Manager) streamTransport(boxID string, port int) *http.Transport {
	key := boxID + ":" + strconv.Itoa(port)
	if t, ok := m.gw.transports.Load(key); ok {
		return t.(*http.Transport)
	}
	t := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			b, err := m.Store.GetBox(ctx, boxID)
			if err != nil {
				return nil, err
			}
			p, ok := m.linkPeer(b)
			if !ok {
				return nil, hostlink.ErrClosed
			}
			cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			return p.OpenStream(cctx, b.ID, port)
		},
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       30 * time.Second,
		DisableCompression:    true,
		ExpectContinueTimeout: time.Second,
	}
	actual, _ := m.gw.transports.LoadOrStore(key, t)
	return actual.(*http.Transport)
}

// previewFor returns (cached) the provider preview base URL of a port.
func (m *Manager) previewFor(ctx context.Context, b *model.Box, port int) string {
	key := b.ID + ":" + strconv.Itoa(port)
	if e, ok := m.gw.previews.Load(key); ok && time.Now().Before(e.(previewEntry).until) {
		return e.(previewEntry).url
	}
	h, err := m.Store.GetHost(ctx, b.HostID)
	if err != nil {
		return ""
	}
	u := m.sandboxPreview(ctx, h, port)
	m.gw.previews.Store(key, previewEntry{url: u, until: time.Now().Add(10 * time.Minute)})
	return u
}

// GatewayUpstream picks how to reach port of a running box, waiting (until
// ctx ends) for a link that is reconnecting after a wake.
func (m *Manager) GatewayUpstream(ctx context.Context, b *model.Box, port int) (*Upstream, error) {
	virtual := m.Hub.IsVirtual(b.HostID)
	if virtual {
		if _, ok := m.ingressPeer(b.ID); !ok {
			if u := m.previewFor(ctx, b, port); u != "" {
				return &Upstream{Kind: UpstreamPreview, PreviewURL: strings.TrimRight(u, "/")}, nil
			}
		}
	}
	for {
		if _, ok := m.linkPeer(b); ok {
			return &Upstream{Kind: UpstreamStream, Transport: m.streamTransport(b.ID, port)}, nil
		}
		if virtual {
			// T3 without previews: (re)start the in-box ingress agent
			if ok, why := CoreLooksPublic(m.Cfg.PublicURL); !ok && !(m.Cfg.AssumePublicURL && m.Cfg.PublicURL != "") {
				return nil, model.EH(model.ErrProviderFailed, "Set BOXAZ_PUBLIC_URL to an internet-reachable URL of core so the sandbox can dial in, or use a provider with preview URLs.", "the gateway cannot reach this sandbox: %s", why)
			}
			l := m.exposeLock(b.ID)
			if l.TryLock() {
				err := m.startIngress(ctx, b)
				l.Unlock()
				if err != nil {
					return nil, model.EH(model.ErrProviderFailed, "Retry in a few seconds.", "ingress agent: %v", err)
				}
				continue
			}
		}
		select {
		case <-ctx.Done():
			return nil, model.EH(model.ErrProviderFailed, "Retry in a few seconds; the box link is reconnecting.", "box %s link is not connected", b.ID)
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// probeUA lists user-agent fragments of bots, link previews and health checkers.
var probeUA = []string{"bot", "crawler", "spider", "slurp", "monitor", "uptime", "pingdom", "statuscake", "healthcheck", "health-check", "kube-probe", "elb-healthchecker", "facebookexternalhit", "embedly", "preview", "headless"}

var probePaths = map[string]bool{"/healthz": true, "/health": true, "/_health": true, "/readyz": true, "/livez": true, "/ping": true, "/favicon.ico": true, "/robots.txt": true, "/.well-known/security.txt": true}

// IsProbe reports whether a request looks like a bot / HEAD / health probe,
// which by default does not wake a sleeping box.
func IsProbe(r *http.Request, path string) bool {
	if r.Method == http.MethodHead || r.Method == http.MethodOptions || r.Header.Get("X-Boxaz-No-Wake") != "" {
		return true
	}
	if probePaths[path] {
		return true
	}
	ua := strings.ToLower(r.UserAgent())
	if ua == "" {
		return false
	}
	for _, f := range probeUA {
		if strings.Contains(ua, f) {
			return true
		}
	}
	return false
}
