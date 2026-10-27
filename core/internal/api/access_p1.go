package api

// v1.11 access P1 endpoints: the box's browser over CDP, the desktop over VNC
// (noVNC served by core) and view-only desktop share links.
//
//   wss://<name>.<domain>/.boxaz/cdp?ticket=…                 browser WebSocket (connectOverCDP / puppeteer.connect)
//   https://<name>.<domain>/.boxaz/cdp/json/version?ticket=…  rewritten so connectOverCDP("https://…/.boxaz/cdp?ticket=…") works
//   wss://<name>.<domain>/.boxaz/vnc?ticket=… | ?share=…      RFB over binary frames (view-only enforced by core)
//   https://<name>.<domain>/.boxaz/desktop?ticket=… | ?share=… noVNC page (vendored, no CDN)
//   /.boxaz/novnc/…                                            noVNC static files
// and the same on core: /v1/boxes/{id}/{cdp,vnc,desktop/view}, /v1/novnc/….

import (
	"bufio"
	"bytes"
	"context"
	"embed"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"

	"boxaz/core/internal/egress"
	"boxaz/core/internal/hostlink"
	"boxaz/core/internal/manager"
	"boxaz/core/internal/model"
	"boxaz/core/internal/wsconn"
)

//go:embed novnc
var novncFS embed.FS

// routesAccessP1 registers the authenticated v1.11 access API.
func (s *Server) routesAccessP1(r chi.Router) {
	r.Get("/boxes/{id}/browser", s.browserStatus)
	r.Post("/boxes/{id}/browser", s.browserOpen)
	r.Delete("/boxes/{id}/browser", s.browserStop)
	r.Get("/boxes/{id}/desktop", s.desktopStatus)
	r.Post("/boxes/{id}/desktop", s.desktopOpen)
	r.Delete("/boxes/{id}/desktop", s.desktopStop)
	r.Get("/boxes/{id}/desktop/shares", s.sharesList)
	r.Post("/boxes/{id}/desktop/shares", s.shareCreate)
	r.Delete("/boxes/{id}/desktop/shares/{sid}", s.shareRevoke)
}

// routesAccessP1Open registers the v1.11 sockets and pages (own auth).
func (s *Server) routesAccessP1Open(r chi.Router) {
	r.Get("/boxes/{id}/cdp", func(w http.ResponseWriter, r *http.Request) {
		s.accessCDP(w, r, nil, "/v1/boxes/"+chi.URLParam(r, "id")+"/cdp")
	})
	r.Get("/boxes/{id}/cdp/*", func(w http.ResponseWriter, r *http.Request) {
		s.accessCDP(w, r, nil, "/v1/boxes/"+chi.URLParam(r, "id")+"/cdp")
	})
	r.Get("/boxes/{id}/vnc", func(w http.ResponseWriter, r *http.Request) { s.accessVNC(w, r, nil) })
	r.Get("/boxes/{id}/desktop/view", func(w http.ResponseWriter, r *http.Request) {
		s.desktopPage(w, r, nil, "/v1/novnc", "/v1/boxes/"+chi.URLParam(r, "id")+"/vnc")
	})
	r.Get("/novnc/*", func(w http.ResponseWriter, r *http.Request) {
		serveNoVNC(w, r, strings.TrimPrefix(r.URL.Path, "/v1/novnc/"))
	})
}

// accessHostP1 serves the v1.11 paths on a box host; false = not one of them.
func (s *Server) accessHostP1(w http.ResponseWriter, r *http.Request, b *model.Box) bool {
	p := r.URL.Path
	switch {
	case p == "/.boxaz/cdp" || strings.HasPrefix(p, "/.boxaz/cdp/"):
		s.accessCDP(w, r, b, "/.boxaz/cdp")
	case p == "/.boxaz/vnc":
		s.accessVNC(w, r, b)
	case p == "/.boxaz/desktop" || p == "/.boxaz/desktop/":
		s.desktopPage(w, r, b, "/.boxaz/novnc", "/.boxaz/vnc")
	case strings.HasPrefix(p, "/.boxaz/novnc/"):
		serveNoVNC(w, r, strings.TrimPrefix(p, "/.boxaz/novnc/"))
	default:
		return false
	}
	return true
}

// ---- REST ----

func (s *Server) browserOpen(w http.ResponseWriter, r *http.Request) {
	var in manager.BrowserInput
	if r.ContentLength != 0 {
		if err := decode(r, &in); err != nil {
			writeErr(w, r, err)
			return
		}
	}
	out, err := s.M.OpenBrowser(r.Context(), pr(r), chi.URLParam(r, "id"), in)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 201, out)
}

func (s *Server) browserStatus(w http.ResponseWriter, r *http.Request) {
	out, err := s.M.BrowserStatus(r.Context(), pr(r), chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, out)
}

func (s *Server) browserStop(w http.ResponseWriter, r *http.Request) {
	out, err := s.M.StopBrowser(r.Context(), pr(r), chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, out)
}

func (s *Server) desktopOpen(w http.ResponseWriter, r *http.Request) {
	var in manager.DesktopInput
	if r.ContentLength != 0 {
		if err := decode(r, &in); err != nil {
			writeErr(w, r, err)
			return
		}
	}
	out, err := s.M.OpenDesktop(r.Context(), pr(r), chi.URLParam(r, "id"), in)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 201, out)
}

func (s *Server) desktopStatus(w http.ResponseWriter, r *http.Request) {
	out, err := s.M.DesktopStatus(r.Context(), pr(r), chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, out)
}

func (s *Server) desktopStop(w http.ResponseWriter, r *http.Request) {
	out, err := s.M.StopDesktop(r.Context(), pr(r), chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, out)
}

func (s *Server) shareCreate(w http.ResponseWriter, r *http.Request) {
	var in manager.ShareInput
	if r.ContentLength != 0 {
		if err := decode(r, &in); err != nil {
			writeErr(w, r, err)
			return
		}
	}
	out, err := s.M.CreateShare(r.Context(), pr(r), chi.URLParam(r, "id"), in)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 201, out)
}

func (s *Server) sharesList(w http.ResponseWriter, r *http.Request) {
	items, err := s.M.ListShares(r.Context(), pr(r), chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": items})
}

func (s *Server) shareRevoke(w http.ResponseWriter, r *http.Request) {
	n, closed, err := s.M.RevokeShares(r.Context(), pr(r), chi.URLParam(r, "id"), chi.URLParam(r, "sid"))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"revoked": n, "viewers_closed": closed})
}

// ---- CDP ----

func requestIsSecure(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") || r.Header.Get("Cf-Visitor") != "" && strings.Contains(r.Header.Get("Cf-Visitor"), "https")
}

// accessCDP proxies the box browser's DevTools endpoint. base is the path
// prefix the client used (/.boxaz/cdp or /v1/boxes/{id}/cdp).
func (s *Server) accessCDP(w http.ResponseWriter, r *http.Request, hostBox *model.Box, base string) {
	rest := strings.TrimPrefix(r.URL.Path, base)
	if rest == "/" {
		rest = ""
	}
	isWS := websocket.IsWebSocketUpgrade(r)
	if !isWS && (rest == "" || !strings.HasPrefix(rest, "/json")) {
		if rest == "" && r.Method == http.MethodGet { // plain GET on the endpoint: behave like /json/version
			rest = "/json/version"
		} else {
			writeErr(w, r, model.EH(model.ErrNotFound, "Use /json/version, /json/list or a WebSocket.", "no CDP endpoint %s", rest))
			return
		}
	}
	if strings.Contains(rest, "..") {
		writeErr(w, r, model.E(model.ErrInvalid, "bad path"))
		return
	}
	c, ok := s.accessAuth(w, r, hostBox, model.PurposeCDP, 0)
	if !ok {
		return
	}
	ctx := r.Context()
	b, peer, err := s.M.AccessLink(ctx, c.box)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	ver, err := s.M.CDPVersion(ctx, peer, b.ID)
	if err != nil { // not running (idle stop, wake): start it again
		if _, err = s.M.EnsureBrowser(ctx, b, peer, false); err == nil {
			ver, err = s.M.CDPVersion(ctx, peer, b.ID)
		}
		if err != nil {
			writeErr(w, r, err)
			return
		}
	}
	s.M.TouchAddon(b.ID, "cdp")
	ticket := r.URL.Query().Get("ticket")
	scheme := "ws://"
	if requestIsSecure(r) {
		scheme = "wss://"
	}
	pub := scheme + r.Host + base
	if !isWS {
		s.cdpHTTP(w, r, b, peer, rest, pub, ticket)
		return
	}
	target := rest
	if target == "" {
		u, _ := ver["webSocketDebuggerUrl"].(string)
		pu, err := url.Parse(u)
		if err != nil || pu.Path == "" {
			writeErr(w, r, model.E(model.ErrProviderFailed, "browser has no webSocketDebuggerUrl"))
			return
		}
		target = pu.Path
	}
	if !strings.HasPrefix(target, "/devtools/") {
		writeErr(w, r, model.EH(model.ErrNotFound, "Connect to /.boxaz/cdp (the browser) or /.boxaz/cdp/devtools/page/<id>.", "no CDP socket %s", target))
		return
	}
	s.cdpWS(w, r, c, b, peer, target)
}

// cdpHTTP proxies /json/* and rewrites the debugger URLs to this endpoint.
func (s *Server) cdpHTTP(w http.ResponseWriter, r *http.Request, b *model.Box, peer *hostlink.Peer, rest, pub, ticket string) {
	method := r.Method
	if method != http.MethodGet && method != http.MethodPut {
		method = http.MethodGet
	}
	p := strings.TrimSuffix(rest, "/")
	if strings.HasPrefix(rest, "/json/new") {
		// /json/new?<url>: the raw query is the URL to open (minus our ticket)
		raw := r.URL.RawQuery
		if i := strings.Index(raw, "ticket="); i >= 0 {
			j := strings.IndexByte(raw[i:], '&')
			if j < 0 {
				raw = strings.TrimRight(raw[:i], "&")
			} else {
				raw = raw[:i] + raw[i+j+1:]
			}
		}
		dec, _ := url.QueryUnescape(raw)
		if egress.IsMetadataURL(dec) || egress.IsMetadataURL(raw) {
			writeErr(w, r, model.EH(model.ErrPolicyDenied, "Cloud metadata addresses are blocked in box browsers.", "blocked: cloud metadata address"))
			return
		}
		if raw != "" {
			p += "?" + raw
		}
	} else {
		q := r.URL.Query()
		q.Del("ticket")
		if enc := q.Encode(); enc != "" {
			p += "?" + enc
		}
	}
	st, body, err := hostlinkHTTP(r.Context(), peer, b.ID, s.M.CDPPort(), method, p)
	if err != nil {
		writeErr(w, r, model.EH(model.ErrProviderFailed, "Retry; the browser may be restarting.", "CDP %s: %v", rest, err))
		return
	}
	suffix := ""
	if ticket != "" {
		suffix = "?ticket=" + url.QueryEscape(ticket)
	}
	rewrite := func(o map[string]any) {
		for _, k := range []string{"webSocketDebuggerUrl"} {
			if v, ok := o[k].(string); ok {
				if pu, err := url.Parse(v); err == nil && pu.Path != "" {
					o[k] = pub + pu.Path + suffix
				}
			}
		}
		delete(o, "devtoolsFrontendUrl")
		delete(o, "devtoolsFrontendUrlCompat")
	}
	var anyv any
	if json.Unmarshal(body, &anyv) == nil {
		switch v := anyv.(type) {
		case map[string]any:
			rewrite(v)
		case []any:
			for _, x := range v {
				if o, ok := x.(map[string]any); ok {
					rewrite(o)
				}
			}
		}
		body, _ = json.Marshal(anyv)
		w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(st)
	w.Write(body)
}

// hostlinkHTTP: one HTTP request to an in-box loopback port (Host: 127.0.0.1).
func hostlinkHTTP(ctx context.Context, peer *hostlink.Peer, boxID string, port int, method, p string) (int, []byte, error) {
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	sc, err := peer.OpenStream(cctx, boxID, port)
	if err != nil {
		return 0, nil, err
	}
	defer sc.Close()
	sc.SetReadDeadline(time.Now().Add(15 * time.Second))
	req, err := http.NewRequest(method, "http://127.0.0.1:"+strconv.Itoa(port)+p, nil)
	if err != nil {
		return 0, nil, err
	}
	req.Host = "127.0.0.1:" + strconv.Itoa(port)
	req.Header.Set("Connection", "close")
	if err := req.Write(sc); err != nil {
		return 0, nil, err
	}
	resp, err := http.ReadResponse(bufio.NewReader(sc), req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	return resp.StatusCode, body, err
}

// cdpWS splices the client's WebSocket onto the browser's: the upgrade is
// forwarded (Host rewritten to 127.0.0.1, no compression so frames stay
// inspectable), then client → browser frames pass through the metadata
// filter and browser → client bytes are copied as they are.
func (s *Server) cdpWS(w http.ResponseWriter, r *http.Request, c *accessCaller, b *model.Box, peer *hostlink.Peer, target string) {
	key := r.Header.Get("Sec-WebSocket-Key")
	if key == "" {
		writeErr(w, r, model.E(model.ErrInvalid, "missing Sec-WebSocket-Key"))
		return
	}
	cctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	sc, err := peer.OpenStream(cctx, b.ID, s.M.CDPPort())
	cancel()
	if err != nil {
		writeErr(w, r, model.EH(model.ErrProviderFailed, "Retry; the browser may be restarting.", "cannot reach the browser: %v", err))
		return
	}
	port := strconv.Itoa(s.M.CDPPort())
	fmt.Fprintf(sc, "GET %s HTTP/1.1\r\nHost: 127.0.0.1:%s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\n\r\n", target, port, key)
	sc.SetReadDeadline(time.Now().Add(15 * time.Second))
	br := bufio.NewReader(sc)
	resp, err := http.ReadResponse(br, &http.Request{Method: "GET"})
	if err != nil || resp.StatusCode != http.StatusSwitchingProtocols {
		sc.Close()
		st := 0
		if resp != nil {
			st = resp.StatusCode
		}
		e := model.EH(model.ErrNotFound, "The target may be closed; fetch /json/list again.", "browser refused the socket %s (HTTP %d, %v)", target, st, err)
		writeErr(w, r, e)
		return
	}
	sc.SetReadDeadline(time.Time{})
	hj, ok := w.(http.Hijacker)
	if !ok {
		sc.Close()
		writeErr(w, r, model.E(model.ErrInvalid, "cannot hijack"))
		return
	}
	cl, cbuf, err := hj.Hijack()
	if err != nil {
		sc.Close()
		return
	}
	fmt.Fprintf(cl, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n", resp.Header.Get("Sec-WebSocket-Accept"))
	var once sync.Once
	closeAll := func() { once.Do(func() { cl.Close(); sc.Close() }) }
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	unbind := s.M.BindAccess(b.ID, c.ticketID, "cdp", func(string) { closeAll() })
	defer unbind()
	go s.M.KeepActive(ctx, b.ID)
	go func() { // client → browser, filtered
		err := cdpFilter(sc, cbuf.Reader, func() { s.M.TouchAddon(b.ID, "cdp") })
		if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
			s.M.Emit(context.Background(), c.p.Actor(), b.ID, b.AgentID, "box.browser", "CDP client closed: "+err.Error(), nil)
		}
		closeAll()
	}()
	if n := br.Buffered(); n > 0 { // bytes the browser sent right after the 101
		buf, _ := br.Peek(n)
		cl.Write(buf)
		br.Discard(n)
	}
	io.Copy(cl, sc)
	closeAll()
}

// cdpFilter copies client WebSocket frames to the browser and rewrites
// navigations to cloud metadata addresses (Page.navigate, Target.createTarget)
// to an unresolvable URL, so the client gets a normal navigation error.
func cdpFilter(dst io.Writer, src *bufio.Reader, touch func()) error {
	var frag []byte
	fragOp := byte(0)
	n := 0
	for {
		payload, fin, op, err := readClientFrame(src)
		if err != nil {
			return err
		}
		if n++; n%50 == 0 {
			touch()
		}
		if op >= 8 { // control frames pass at once (they may interleave fragments)
			if _, err := dst.Write(encodeClientFrame(op, payload)); err != nil {
				return err
			}
			continue
		}
		if op != 0 && !fin { // first fragment
			frag, fragOp = append(frag[:0], payload...), op
			continue
		}
		if op == 0 { // continuation
			frag = append(frag, payload...)
			if len(frag) > 64<<20 {
				return errors.New("message too large")
			}
			if !fin {
				continue
			}
			payload, op = frag, fragOp
		}
		if op == 1 {
			payload, _ = filterCDPMessage(payload)
		}
		if _, err := dst.Write(encodeClientFrame(op, payload)); err != nil {
			return err
		}
	}
}

// readClientFrame reads one masked client frame; payload is returned unmasked.
func readClientFrame(r *bufio.Reader) (payload []byte, fin bool, op byte, err error) {
	var h [2]byte
	if _, err = io.ReadFull(r, h[:]); err != nil {
		return
	}
	fin, op = h[0]&0x80 != 0, h[0]&0x0f
	masked := h[1]&0x80 != 0
	ln := uint64(h[1] & 0x7f)
	switch ln {
	case 126:
		var e [2]byte
		if _, err = io.ReadFull(r, e[:]); err != nil {
			return
		}
		ln = uint64(binary.BigEndian.Uint16(e[:]))
	case 127:
		var e [8]byte
		if _, err = io.ReadFull(r, e[:]); err != nil {
			return
		}
		ln = binary.BigEndian.Uint64(e[:])
	}
	if ln > 256<<20 {
		err = errors.New("frame too large")
		return
	}
	var mask [4]byte
	if masked {
		if _, err = io.ReadFull(r, mask[:]); err != nil {
			return
		}
	}
	payload = make([]byte, ln)
	if _, err = io.ReadFull(r, payload); err != nil {
		return
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i&3]
		}
	}
	return
}

func encodeHeader(b0 byte, n int) []byte {
	h := []byte{b0}
	switch {
	case n < 126:
		h = append(h, 0x80|byte(n))
	case n <= 0xffff:
		h = append(h, 0x80|126, byte(n>>8), byte(n))
	default:
		h = append(h, 0x80|127)
		var e [8]byte
		binary.BigEndian.PutUint64(e[:], uint64(n))
		h = append(h, e[:]...)
	}
	return append(h, 0, 0, 0, 0) // mask key 0: payload bytes go unchanged
}

func encodeClientFrame(op byte, payload []byte) []byte {
	return append(encodeHeader(0x80|op, len(payload)), payload...)
}

// metadataBlockedURL replaces a navigation to a metadata address.
const metadataBlockedURL = "http://cloud-metadata.blocked.invalid/"

// filterCDPMessage rewrites Page.navigate / Target.createTarget /
// Page.navigateToHistoryEntry-free navigations whose url is a metadata address.
func filterCDPMessage(p []byte) ([]byte, bool) {
	if !bytes.Contains(p, []byte(`"url"`)) {
		return p, false
	}
	var msg map[string]json.RawMessage
	if json.Unmarshal(p, &msg) != nil {
		return p, false
	}
	var method string
	json.Unmarshal(msg["method"], &method)
	switch method {
	case "Page.navigate", "Target.createTarget", "Page.createIsolatedWorld":
	default:
		return p, false
	}
	var params map[string]any
	if json.Unmarshal(msg["params"], &params) != nil {
		return p, false
	}
	u, _ := params["url"].(string)
	if u == "" || !egress.IsMetadataURL(u) {
		return p, false
	}
	params["url"] = metadataBlockedURL
	msg["params"], _ = json.Marshal(params)
	out, err := json.Marshal(msg)
	if err != nil {
		return p, false
	}
	return out, true
}

// ---- VNC ----

var vncUpgrader = websocket.Upgrader{ReadBufferSize: 64 << 10, WriteBufferSize: 64 << 10, Subprotocols: []string{"binary"},
	CheckOrigin: func(r *http.Request) bool { return true }}

// sameOrigin: the noVNC page on the box host opens /.boxaz/vnc on its own origin.
func sameOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return false
	}
	u, err := url.Parse(o)
	return err == nil && strings.EqualFold(u.Host, r.Host)
}

// accessVNC streams RFB between the client and the box's VNC server.
func (s *Server) accessVNC(w http.ResponseWriter, r *http.Request, hostBox *model.Box) {
	ctx := r.Context()
	var c *accessCaller
	viewOnly := false
	if share := r.URL.Query().Get("share"); share != "" {
		boxID := ""
		if hostBox != nil {
			boxID = hostBox.ID
		} else if id := chi.URLParam(r, "id"); id != "" {
			if b, err := s.M.BoxForSSH(ctx, id); err == nil {
				boxID = b.ID
			}
		}
		t, b, st := s.M.LookupShare(ctx, share, boxID)
		if st != 200 {
			shareErr(w, r, st)
			return
		}
		if !sameOrigin(r) && !s.M.OriginAllowed(r, s.CORSOrigins) && r.Header.Get("Origin") != "" {
			// share links are iframe-able anywhere, but their socket is opened by the page on the box host
			writeErr(w, r, model.EH(model.ErrPolicyDenied, "Open the share link page.", "origin %s may not open the desktop socket", r.Header.Get("Origin")))
			return
		}
		s.M.UseShare(ctx, t)
		p, _ := s.M.PrincipalForTicket(ctx, t)
		c = &accessCaller{box: b, p: p, ticket: t, ticketID: t.ID}
		viewOnly = t.ViewOnly
	} else {
		if sameOrigin(r) && hostBox != nil {
			r.Header.Del("Origin") // the page on the box host itself (ticket still required)
		}
		var ok bool
		if c, ok = s.accessAuth(w, r, hostBox, model.PurposeVNC, 0); !ok {
			return
		}
		viewOnly = c.ticket != nil && c.ticket.ViewOnly || r.URL.Query().Get("view_only") == "1"
	}
	b, peer, err := s.M.AccessLink(ctx, c.box)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	sc, err := peer.OpenStream(cctx, b.ID, s.M.VNCPort())
	cancel()
	if err != nil { // desktop not running: start it
		if _, err = s.M.EnsureDesktop(ctx, b, peer, 0, 0); err == nil {
			cctx, cancel = context.WithTimeout(ctx, 10*time.Second)
			sc, err = peer.OpenStream(cctx, b.ID, s.M.VNCPort())
			cancel()
		}
		if err != nil {
			writeErr(w, r, err)
			return
		}
	}
	ws, err := vncUpgrader.Upgrade(w, r, nil)
	if err != nil {
		sc.Close()
		return
	}
	conn := wsconn.New(ws)
	var once sync.Once
	closeAll := func(reason string) {
		once.Do(func() {
			conn.CloseWithReason(websocket.CloseGoingAway, reason)
			sc.Close()
		})
	}
	bctx, stop := context.WithCancel(context.Background())
	defer stop()
	unbind := s.M.BindAccess(b.ID, c.ticketID, "vnc", closeAll)
	defer unbind()
	go s.M.KeepActive(bctx, b.ID)
	s.M.TouchAddon(b.ID, "vnc")
	go func() {
		rfbFilter(sc, conn, viewOnly)
		sc.CloseWrite()
	}()
	io.Copy(conn, sc)
	closeAll("")
}

// rfbFilter copies the client → server half of RFB. With viewOnly it parses
// the message stream and drops KeyEvent, PointerEvent, ClientCutText,
// SetDesktopSize and QEMU key events; the ClientInit always asks for a
// shared session so a viewer never disconnects others.
func rfbFilter(dst io.Writer, src io.Reader, viewOnly bool) error {
	r := bufio.NewReaderSize(src, 64<<10)
	read := func(n int) ([]byte, error) {
		b := make([]byte, n)
		_, err := io.ReadFull(r, b)
		return b, err
	}
	ver, err := read(12) // "RFB 003.008\n"
	if err != nil {
		return err
	}
	if _, err := dst.Write(ver); err != nil {
		return err
	}
	minor := 8
	if len(ver) == 12 && bytes.HasPrefix(ver, []byte("RFB 003.")) {
		minor, _ = strconv.Atoi(strings.TrimSpace(string(ver[8:11])))
	}
	if minor >= 7 {
		sec, err := read(1)
		if err != nil {
			return err
		}
		dst.Write(sec)
		if sec[0] == 2 { // VNC authentication response
			resp, err := read(16)
			if err != nil {
				return err
			}
			dst.Write(resp)
		}
	}
	if _, err := read(1); err != nil { // ClientInit: force shared
		return err
	}
	if _, err := dst.Write([]byte{1}); err != nil {
		return err
	}
	if !viewOnly {
		_, err := io.Copy(dst, r)
		return err
	}
	for {
		t, err := r.ReadByte()
		if err != nil {
			return err
		}
		var msg []byte
		drop := false
		switch t {
		case 0: // SetPixelFormat
			msg, err = read(19)
		case 2: // SetEncodings
			var h []byte
			if h, err = read(3); err == nil {
				var tail []byte
				tail, err = read(4 * int(binary.BigEndian.Uint16(h[1:3])))
				msg = append(h, tail...)
			}
		case 3: // FramebufferUpdateRequest
			msg, err = read(9)
		case 4: // KeyEvent
			msg, err, drop = nil, discard(r, 7), true
		case 5: // PointerEvent
			msg, err, drop = nil, discard(r, 5), true
		case 6: // ClientCutText
			var h []byte
			if h, err = read(7); err == nil {
				err, drop = discard(r, int(binary.BigEndian.Uint32(h[3:7]))), true
			}
		case 150: // EnableContinuousUpdates
			msg, err = read(9)
		case 248: // ClientFence
			var h []byte
			if h, err = read(8); err == nil {
				var tail []byte
				tail, err = read(int(h[7]))
				msg = append(h, tail...)
			}
		case 251: // SetDesktopSize
			var h []byte
			if h, err = read(7); err == nil {
				err, drop = discard(r, 16*int(h[5])), true
			}
		case 255: // QEMU client message
			var sub []byte
			if sub, err = read(1); err == nil {
				switch sub[0] {
				case 0: // extended key event
					err, drop = discard(r, 10), true
				case 1: // audio
					var op []byte
					if op, err = read(2); err == nil {
						msg = append(sub, op...)
						if binary.BigEndian.Uint16(op) == 2 {
							var tail []byte
							tail, err = read(6)
							msg = append(msg, tail...)
						}
					}
				default:
					return fmt.Errorf("unsupported QEMU message %d", sub[0])
				}
			}
		default:
			return fmt.Errorf("unsupported RFB client message %d", t)
		}
		if err != nil {
			return err
		}
		if drop {
			continue
		}
		if _, err := dst.Write(append([]byte{t}, msg...)); err != nil {
			return err
		}
	}
}

func discard(r *bufio.Reader, n int) error {
	_, err := io.CopyN(io.Discard, r, int64(n))
	return err
}

func shareErr(w http.ResponseWriter, r *http.Request, st int) {
	switch st {
	case 410:
		e := model.EH(model.ErrNotFound, "Ask the owner for a new link.", "this share link was revoked or has expired")
		e.Status = http.StatusGone
		writeErr(w, r, e)
	default:
		writeErr(w, r, model.EH(model.ErrUnauthorized, "Ask the owner for a new link.", "unknown share link"))
	}
}

// ---- noVNC page + static files ----

func serveNoVNC(w http.ResponseWriter, r *http.Request, p string) {
	p = path.Clean("/" + p)[1:]
	if p == "" || strings.HasSuffix(p, ".md") {
		http.NotFound(w, r)
		return
	}
	b, err := fs.ReadFile(novncFS, "novnc/"+p)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	ct := mime.TypeByExtension(path.Ext(p))
	if strings.HasSuffix(p, ".js") {
		ct = "text/javascript; charset=utf-8"
	}
	if ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Write(b)
}

var desktopTpl = template.Must(template.New("d").Parse(`<!doctype html>
<html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta name="robots" content="noindex">
<title>{{.Title}}</title>
<style nonce="{{.Nonce}}">html,body{margin:0;height:100%;background:#111827;color:#e5e7eb;font:14px system-ui,sans-serif}#s{position:absolute;inset:0}#bar{position:fixed;top:8px;right:8px;z-index:9;background:#1f2937cc;padding:4px 10px;border-radius:6px;font-size:12px}</style>
</head><body><div id="bar">{{.Label}} · <span id="st">…</span></div><div id="s"></div>
<script type="module" nonce="{{.Nonce}}">
import RFB from "{{.Lib}}/core/rfb.js";
const st = document.getElementById("st");
const proto = location.protocol === "https:" ? "wss://" : "ws://";
const url = proto + location.host + {{.Socket}};
const rfb = new RFB(document.getElementById("s"), url, {shared: true});
rfb.viewOnly = {{.ViewOnly}};
rfb.scaleViewport = true;
rfb.background = "#111827";
rfb.addEventListener("connect", () => { st.textContent = {{.Connected}}; });
rfb.addEventListener("disconnect", (e) => { st.textContent = e.detail.clean ? {{.Closed}} : {{.Lost}}; });
</script></body></html>`))

// desktopPage serves the noVNC viewer for a ticket (single use, redeemed by
// its socket) or a share link (reusable until revoked / expired).
func (s *Server) desktopPage(w http.ResponseWriter, r *http.Request, hostBox *model.Box, lib, socket string) {
	ctx := r.Context()
	q := r.URL.Query()
	boxID := ""
	if hostBox != nil {
		boxID = hostBox.ID
	} else if id := chi.URLParam(r, "id"); id != "" {
		if b, err := s.M.BoxForSSH(ctx, id); err == nil {
			boxID = b.ID
		} else {
			boxID = id
		}
	}
	viewOnly := true
	frame := "'self'"
	sock := socket + "?"
	switch {
	case q.Get("share") != "":
		t, _, st := s.M.LookupShare(ctx, q.Get("share"), boxID)
		if st != 200 {
			sharePage(w, st)
			return
		}
		viewOnly = t.ViewOnly
		frame = "*" // share links are meant to be embedded
		sock += "share=" + url.QueryEscape(q.Get("share"))
	case q.Get("ticket") != "":
		if !s.M.PeekTicket(ctx, q.Get("ticket"), model.PurposeVNC, boxID) {
			sharePage(w, 401)
			return
		}
		viewOnly = q.Get("view_only") == "1"
		for _, o := range s.M.AccessOrigins(s.CORSOrigins) {
			frame += " " + o
		}
		sock += "ticket=" + url.QueryEscape(q.Get("ticket"))
		if viewOnly {
			sock += "&view_only=1"
		}
	default:
		sharePage(w, 401)
		return
	}
	nonce := strconv.FormatInt(time.Now().UnixNano(), 36)
	label := "Desktop · view only"
	if !viewOnly {
		label = "Desktop"
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Robots-Tag", "noindex")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self' 'nonce-"+nonce+"'; style-src 'nonce-"+nonce+"' 'unsafe-inline'; img-src 'self' data: blob:; connect-src 'self'; worker-src 'self' blob:; frame-ancestors "+frame+"; base-uri 'none'; form-action 'none'")
	desktopTpl.Execute(w, map[string]any{"Title": "Desktop", "Nonce": nonce, "Lib": lib, "Socket": sock, "ViewOnly": viewOnly, "Label": label,
		"Connected": "connected", "Closed": "disconnected", "Lost": "connection lost"})
}

func sharePage(w http.ResponseWriter, st int) {
	msg := "This link is not valid."
	if st == 410 {
		msg = "This share link was revoked or has expired."
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.WriteHeader(st)
	fmt.Fprintf(w, "<!doctype html><meta charset=utf-8><meta name=robots content=noindex><title>Desktop</title><body style=\"font:15px system-ui;background:#111827;color:#e5e7eb;display:grid;place-items:center;height:100vh;margin:0\"><p>%s</p>", template.HTMLEscapeString(msg))
}
