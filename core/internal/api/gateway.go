package api

// v1.7 Boxaz gateway (HTTP side): stable URL per box+port, scoped URL tokens,
// wake-on-request with a bounded hold, readiness gating, and unbuffered
// pass-through (WebSocket upgrade, SSE, chunked bodies) via
// httputil.ReverseProxy over a box stream or the provider preview host.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"time"

	"boxaz/core/internal/hostlink"
	"boxaz/core/internal/manager"
	"boxaz/core/internal/model"
)

const gatewayCookie = "boxaz_ut"

// tokenQueryParams are the query names a Boxaz URL token may arrive in. Only
// values with the bxt_ prefix are consumed, so an app's own ?token= is kept.
var tokenQueryParams = []string{"token", "boxaz_token", legacyTokenParam}

var previewTransport = func() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.MaxIdleConnsPerHost = 16
	t.IdleConnTimeout = 60 * time.Second
	t.ForceAttemptHTTP2 = false // keep WebSocket upgrades on HTTP/1.1
	return t
}()

// gatewayToken finds a Boxaz URL token in the request: header, query, cookie.
func gatewayToken(r *http.Request) (tok, src string) {
	if a := r.Header.Get("Authorization"); strings.HasPrefix(a, "Bearer "+manager.URLTokenPrefix) {
		return strings.TrimSpace(a[7:]), "header"
	}
	if t := strings.TrimSpace(r.Header.Get("X-Boxaz-Token")); t != "" {
		return t, "header"
	}
	q := r.URL.Query()
	for _, k := range tokenQueryParams {
		if v := q.Get(k); strings.HasPrefix(v, manager.URLTokenPrefix) {
			return v, "query"
		}
	}
	if c, err := r.Cookie(gatewayCookie); err == nil && c.Value != "" {
		return c.Value, "cookie"
	}
	return "", ""
}

func wantsHTML(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept"), "text/html")
}

func requestIsHTTPS(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// gatewayPage answers a gateway error: an auto-refreshing HTML page for
// browsers, the usual JSON error envelope for everything else.
func gatewayPage(w http.ResponseWriter, r *http.Request, status int, code, title, msg string, retryAfter int) {
	if retryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
	}
	w.Header().Set("Cache-Control", "no-store")
	if wantsHTML(r) && r.Method == http.MethodGet {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(status)
		refresh := ""
		if retryAfter > 0 {
			refresh = fmt.Sprintf(`<meta http-equiv="refresh" content="%d">`, retryAfter)
		}
		fmt.Fprintf(w, `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">%s<title>%s</title>`+
			`<style>body{font:16px/1.5 system-ui,sans-serif;display:grid;place-items:center;min-height:100vh;margin:0;background:#0b0d12;color:#e6e8ee}`+
			`main{max-width:28rem;padding:2rem;text-align:center}.d{width:12px;height:12px;border-radius:50%%;background:#7c9cff;display:inline-block;animation:p 1s infinite alternate}`+
			`@keyframes p{to{opacity:.2}}small{color:#8a90a2}</style></head><body><main><p><span class="d"></span></p><h1>%s</h1><p>%s</p>%s</main></body></html>`,
			refresh, html.EscapeString(title), html.EscapeString(title), html.EscapeString(msg),
			map[bool]string{true: "<p><small>This page reloads by itself.</small></p>", false: ""}[retryAfter > 0])
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	body := map[string]any{"error": map[string]any{"code": code, "message": msg}}
	if retryAfter > 0 {
		body["error"].(map[string]any)["retry_after_s"] = retryAfter
	}
	json.NewEncoder(w).Encode(body)
}

// gateway serves one request for box port seg ("<key>-<port>"); prefix is the
// path prefix stripped before forwarding ("" for host-based gateway URLs).
func (s *Server) gateway(w http.ResponseWriter, r *http.Request, seg, prefix string) {
	ctx := r.Context()
	b, port, err := s.M.BoxForIngressPath(ctx, seg)
	if err != nil {
		gatewayPage(w, r, http.StatusNotFound, model.ErrNotFound, "Nothing here", "No running box at this address.", 0)
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, prefix)
	if rest == "" {
		// relative links need the trailing slash
		http.Redirect(w, r, prefix+"/"+qs(r.URL.RawQuery), http.StatusMovedPermanently)
		return
	}
	// 1. access: token ports need a valid Boxaz URL token
	tok, src := gatewayToken(r)
	valid := tok != "" && s.M.CheckURLToken(ctx, b, port, tok)
	if b.AccessFor(port) == model.AccessToken && !valid {
		w.Header().Set("WWW-Authenticate", `Bearer realm="boxaz"`)
		if src == "cookie" {
			http.SetCookie(w, &http.Cookie{Name: gatewayCookie, Value: "", Path: prefix + "/", MaxAge: -1})
		}
		msg := "This URL needs a Boxaz URL token (Authorization: Bearer, X-Boxaz-Token, or ?token=)."
		if tok != "" {
			msg = "The Boxaz URL token is invalid, expired or revoked."
		}
		gatewayPage(w, r, http.StatusUnauthorized, model.ErrUnauthorized, "Access token required", msg, 0)
		return
	}
	q := r.URL.Query()
	stripped := false
	for _, k := range tokenQueryParams {
		if strings.HasPrefix(q.Get(k), manager.URLTokenPrefix) {
			q.Del(k)
			stripped = true
		}
	}
	if src == "query" && valid && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
		// exchange the query token for a cookie, then drop it from the URL
		http.SetCookie(w, &http.Cookie{Name: gatewayCookie, Value: tok, Path: prefix + "/", HttpOnly: true, Secure: requestIsHTTPS(r), SameSite: http.SameSiteLaxMode})
		u := prefix + rest
		if enc := q.Encode(); enc != "" {
			u += "?" + enc
		}
		http.Redirect(w, r, u, http.StatusFound)
		return
	}
	// 2. wake: hold the request while a sleeping box resumes
	pol := s.M.WakePolicy(ctx)
	deadline := time.Now().Add(pol.Hold)
	if b.Status == model.StatusSleeping {
		if !b.WakeOn() {
			gatewayPage(w, r, http.StatusServiceUnavailable, model.ErrBoxSleeping, "This app is asleep", "The box is sleeping and wake on request is off for it.", 0)
			return
		}
		if !pol.WakeOnProbes && manager.IsProbe(r, rest) {
			gatewayPage(w, r, http.StatusServiceUnavailable, model.ErrBoxSleeping, "This app is asleep", "The box is sleeping; probes and bots do not wake it.", pol.RetryAfterS)
			return
		}
		op := s.M.GatewayWake(b.ID)
		tm := time.NewTimer(time.Until(deadline))
		select {
		case <-op.Done:
			tm.Stop()
			if op.Err != nil {
				gatewayPage(w, r, http.StatusServiceUnavailable, model.ErrBoxSleeping, "Could not wake this app", "Wake failed: "+model.AsAPIError(op.Err).Message, pol.RetryAfterS)
				return
			}
		case <-tm.C:
			gatewayPage(w, r, http.StatusServiceUnavailable, model.ErrBoxSleeping, "Waking up this app…", "The box was asleep and is starting; try again in a few seconds.", pol.RetryAfterS)
			return
		case <-ctx.Done():
			return
		}
		if b, err = s.M.Store.GetBox(ctx, b.ID); err != nil {
			gatewayPage(w, r, http.StatusNotFound, model.ErrNotFound, "Nothing here", "No running box at this address.", 0)
			return
		}
	}
	if b.Status != model.StatusRunning {
		gatewayPage(w, r, http.StatusServiceUnavailable, model.ErrBoxSleeping, "This app is not running", "The box is "+b.Status+".", pol.RetryAfterS)
		return
	}
	s.M.GatewayTouch(ctx, b.ID)
	// 3. readiness: hold until the supervised app answers its check
	for {
		ok, why := s.M.AppGate(ctx, b, port)
		if ok {
			break
		}
		if time.Now().After(deadline) {
			gatewayPage(w, r, http.StatusServiceUnavailable, model.ErrBoxSleeping, "Starting this app…", why+"; try again in a few seconds.", pol.RetryAfterS)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(300 * time.Millisecond):
		}
	}
	// 4. upstream
	uctx, cancel := context.WithDeadline(ctx, maxTime(deadline, time.Now().Add(5*time.Second)))
	up, err := s.M.GatewayUpstream(uctx, b, port)
	cancel()
	if err != nil {
		gatewayPage(w, r, http.StatusServiceUnavailable, model.ErrProviderFailed, "This app is unreachable", model.AsAPIError(err).Message, pol.RetryAfterS)
		return
	}
	s.proxyGateway(w, r, b, port, up, prefix, rest, q, stripped)
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

// proxyGateway forwards the request without buffering.
func (s *Server) proxyGateway(w http.ResponseWriter, r *http.Request, b *model.Box, port int, up *manager.Upstream, prefix, rest string, q url.Values, stripped bool) {
	rawQuery := r.URL.RawQuery
	if stripped {
		rawQuery = q.Encode()
	}
	inHost := r.Host
	proto := "http"
	if requestIsHTTPS(r) {
		proto = "https"
	}
	var target *url.URL
	var tr http.RoundTripper
	switch up.Kind {
	case manager.UpstreamPreview:
		target, _ = url.Parse(up.PreviewURL)
		tr = previewTransport
	default:
		target = &url.URL{Scheme: "http", Host: "127.0.0.1:" + strconv.Itoa(port)}
		tr = up.Transport
	}
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme = target.Scheme
			pr.Out.URL.Host = target.Host
			pr.Out.URL.Path = rest
			pr.Out.URL.RawPath = ""
			pr.Out.URL.RawQuery = rawQuery
			if up.Kind == manager.UpstreamPreview {
				pr.Out.Host = target.Host
			} else {
				pr.Out.Host = inHost
			}
			h := pr.Out.Header
			h.Del("X-Boxaz-Token")
			h.Del("X-Boxaz-No-Wake")
			if strings.HasPrefix(h.Get("Authorization"), "Bearer "+manager.URLTokenPrefix) {
				h.Del("Authorization")
			}
			// the app gets its own cookies, never the gateway / dashboard ones
			var keep []string
			for _, c := range pr.In.Cookies() {
				switch c.Name {
				case gatewayCookie, "boxaz_key", "boxa_key", "boxaz_t", legacyTokenCookie:
				default:
					keep = append(keep, c.Name+"="+c.Value)
				}
			}
			h.Del("Cookie")
			if len(keep) > 0 {
				h.Set("Cookie", strings.Join(keep, "; "))
			}
			h.Set("X-Forwarded-Host", inHost)
			h.Set("X-Forwarded-Proto", proto)
			if prefix != "" {
				h.Set("X-Forwarded-Prefix", prefix)
			}
			if ip := clientIP(pr.In); ip != "" {
				h.Set("X-Forwarded-For", ip)
			}
		},
		Transport:     tr,
		FlushInterval: -1, // SSE / streaming: flush every write
		ModifyResponse: func(res *http.Response) error {
			if loc := res.Header.Get("Location"); loc != "" {
				switch {
				case prefix != "" && strings.HasPrefix(loc, "/") && !strings.HasPrefix(loc, "//"):
					res.Header.Set("Location", prefix+loc) // keep redirects inside the prefix
				case up.Kind == manager.UpstreamPreview && strings.HasPrefix(loc, up.PreviewURL):
					res.Header.Set("Location", prefix+strings.TrimPrefix(loc, up.PreviewURL))
				}
			}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, req *http.Request, err error) {
			if errors.Is(err, hostlink.ErrStreamUnsupported) {
				s.bufferedGateway(w, req, b, port, prefix, rest, rawQuery)
				return
			}
			if errors.Is(err, context.Canceled) {
				return
			}
			gatewayPage(w, req, http.StatusBadGateway, model.ErrProviderFailed, "The app did not answer", "Upstream error: "+err.Error(), 0)
		},
	}
	rp.ServeHTTP(w, r)
}

func clientIP(r *http.Request) string {
	if f := r.Header.Get("X-Forwarded-For"); f != "" {
		return f
	}
	if c := r.Header.Get("Cf-Connecting-Ip"); c != "" {
		return c
	}
	return hostOnly(r.RemoteAddr)
}

// bufferedGateway is the v1.6 path for agents without stream support.
func (s *Server) bufferedGateway(w http.ResponseWriter, r *http.Request, b *model.Box, port int, prefix, rest, rawQuery string) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 32<<20))
	hdr := map[string][]string{}
	for k, v := range r.Header {
		if !hopHeaders[k] && k != "Cookie" && k != "X-Boxaz-Token" && !(k == "Authorization" && strings.HasPrefix(r.Header.Get(k), "Bearer "+manager.URLTokenPrefix)) {
			hdr[k] = v
		}
	}
	var keep []string
	for _, c := range r.Cookies() {
		if c.Name != gatewayCookie && c.Name != "boxaz_key" && c.Name != "boxa_key" {
			keep = append(keep, c.Name+"="+c.Value)
		}
	}
	if len(keep) > 0 {
		hdr["Cookie"] = []string{strings.Join(keep, "; ")}
	}
	hdr["X-Forwarded-Host"] = []string{r.Host}
	if prefix != "" {
		hdr["X-Forwarded-Prefix"] = []string{prefix}
	}
	res, err := s.M.ProxyPort(r.Context(), b, port, r.Method, rest+qs(rawQuery), hdr, body)
	if err != nil {
		gatewayPage(w, r, http.StatusBadGateway, model.ErrProviderFailed, "The app did not answer", model.AsAPIError(err).Message, 0)
		return
	}
	for k, vs := range res.Headers {
		if hopHeaders[k] || strings.EqualFold(k, "Content-Length") {
			continue
		}
		for _, v := range vs {
			if strings.EqualFold(k, "Location") && prefix != "" && strings.HasPrefix(v, "/") && !strings.HasPrefix(v, "//") {
				v = prefix + v
			}
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(res.Status)
	w.Write(res.Body)
}
