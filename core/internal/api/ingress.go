package api

import (
	"io"
	"net"
	"net/http"
	"strings"

	"boxmes/core/internal/model"
)

var hopHeaders = map[string]bool{"Connection": true, "Keep-Alive": true, "Proxy-Authenticate": true, "Proxy-Authorization": true, "Te": true, "Trailer": true, "Transfer-Encoding": true, "Upgrade": true}

func hostOnly(h string) string {
	if host, _, err := net.SplitHostPort(h); err == nil {
		return host
	}
	return h
}

// ingressMW serves box URLs (<slug>-<ip>.sslip.io or <slug>.box.<domain>) before the API router.
func (s *Server) ingressMW(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// path-based ingress (<public url>/x/<key>-<port>/…): the public-URL
		// fallback for sandbox boxes; app traffic, not part of the API.
		if strings.HasPrefix(r.URL.Path, "/x/") {
			s.pathIngress(w, r)
			return
		}
		host := hostOnly(r.Host)
		// host-based gateway URLs: <key>-<port>.<gateway domain>. Only labels
		// of that exact shape reach a box; the public URL host (gw.<domain>)
		// is core itself, reserved and unknown subdomains get a 404.
		switch kind, seg := s.M.GatewayHost(r.Context(), host); kind {
		case "box":
			if rest, ok := legacyAccessPath(r.URL.Path); ok { // compat: /.boxaz/… from before the rename
				r.URL.Path, r.URL.RawPath = "/.boxmes/"+rest, ""
			}
			if strings.HasPrefix(r.URL.Path, "/.boxmes/") { // v1.10 terminal / tcp / ssh on the box host
				s.accessHost(w, r, seg)
				return
			}
			s.gateway(w, r, seg, "")
			return
		case "reserved", "other":
			gatewayPage(w, r, http.StatusNotFound, model.ErrNotFound, "Nothing here", "No box at this address.", 0)
			return
		}
		if !s.M.IsIngressHost(r.Context(), host) {
			next.ServeHTTP(w, r)
			return
		}
		b, err := s.M.BoxForHost(r.Context(), host)
		if err != nil {
			http.Error(w, "boxmes: no running box at this address", http.StatusNotFound)
			return
		}
		if s.M.Settings(r.Context()).URLTokenProtect {
			want := s.M.URLToken(b.ID)
			tokParam := "boxmes_token"
			if r.URL.Query().Get(tokParam) == "" {
				for _, lp := range legacyTokenParams { // compat: links minted before the renames
					if r.URL.Query().Get(lp) != "" {
						tokParam = lp
						break
					}
				}
			}
			if t := r.URL.Query().Get(tokParam); t != "" {
				if t != want {
					http.Error(w, "boxmes: bad token", http.StatusForbidden)
					return
				}
				http.SetCookie(w, &http.Cookie{Name: "boxmes_t", Value: t, Path: "/", HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode})
				q := r.URL.Query()
				q.Del(tokParam)
				r.URL.RawQuery = q.Encode()
				http.Redirect(w, r, r.URL.RequestURI(), http.StatusFound)
				return
			}
			c, err := r.Cookie("boxmes_t")
			for _, lc := range legacyTokenCookies {
				if err == nil {
					break
				}
				c, err = r.Cookie(lc)
			}
			if err != nil || c.Value != want {
				http.Error(w, "boxmes: this box URL is token-protected (open the URL with ?boxmes_token=…)", http.StatusUnauthorized)
				return
			}
		}
		body, _ := io.ReadAll(io.LimitReader(r.Body, 32<<20))
		hdr := map[string][]string{}
		for k, v := range r.Header {
			if !hopHeaders[k] && k != "Cookie" {
				hdr[k] = v
			}
		}
		hdr["X-Forwarded-Host"] = []string{r.Host}
		hdr["X-Forwarded-Proto"] = []string{"https"}
		res, err := s.M.Proxy(r.Context(), b, r.Method, r.URL.RequestURI(), hdr, body)
		if err != nil {
			ae := model.AsAPIError(err)
			http.Error(w, "boxmes: "+ae.Message, http.StatusBadGateway)
			return
		}
		for k, vs := range res.Headers {
			if hopHeaders[k] || strings.EqualFold(k, "Content-Length") {
				continue
			}
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(res.Status)
		w.Write(res.Body)
	})
}

// tlsAsk answers Caddy on_demand_tls: 200 = issue a certificate for ?domain=.
func (s *Server) tlsAsk(w http.ResponseWriter, r *http.Request) {
	if s.M.TLSAllowed(r.Context(), r.URL.Query().Get("domain")) {
		w.WriteHeader(200)
		return
	}
	w.WriteHeader(404)
}

// Pre-rename names (Boxaz, then Boxa) still accepted so existing links,
// sessions and clients keep working. Newest first.
var (
	legacyTokenParams   = []string{"boxaz_token", "boxa_token"} // box URL token query parameter
	legacyTokenCookies  = []string{"boxaz_t", "boxa_t"}         // box URL token cookie
	legacyKeyCookies    = []string{"boxaz_key", "boxa_key"}     // dashboard API key cookie
	legacyGatewayCookie = []string{"boxaz_ut"}                  // gateway URL token cookie (v1.7+, Boxaz era)
	legacyTokenHeaders  = []string{"X-Boxaz-Token"}             // gateway URL token header
)

// legacyAccessPath maps /.boxaz/<rest> (box-host access paths from the Boxaz
// era: terminal, tcp, ssh, cdp, vnc, desktop, novnc) to <rest>.
func legacyAccessPath(p string) (string, bool) {
	if strings.HasPrefix(p, "/.boxaz/") {
		return strings.TrimPrefix(p, "/.boxaz/"), true
	}
	return "", false
}

// isGatewayOwnedCookie reports cookies the gateway / dashboard own (current
// and legacy names); they are never forwarded to a box's app.
func isGatewayOwnedCookie(name string) bool {
	switch name {
	case gatewayCookie, "boxmes_key", "boxmes_t":
		return true
	}
	for _, l := range [][]string{legacyGatewayCookie, legacyKeyCookies, legacyTokenCookies} {
		for _, n := range l {
			if n == name {
				return true
			}
		}
	}
	return false
}

// isGatewayTokenHeader reports the URL token headers (current and legacy).
func isGatewayTokenHeader(k string) bool {
	if http.CanonicalHeaderKey(k) == "X-Boxmes-Token" {
		return true
	}
	for _, h := range legacyTokenHeaders {
		if http.CanonicalHeaderKey(k) == h {
			return true
		}
	}
	return false
}

// pathIngress serves <public url>/x/<key>-<port>/… : the v1.7 Boxmes gateway
// (stable URL per box+port, wake on request, URL tokens, unbuffered proxy).
// The key in the path is unguessable per box; the prefix is stripped before
// forwarding.
func (s *Server) pathIngress(w http.ResponseWriter, r *http.Request) {
	seg, _, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/x/"), "/")
	s.gateway(w, r, seg, "/x/"+seg)
}

func qs(raw string) string {
	if raw == "" {
		return ""
	}
	return "?" + raw
}
