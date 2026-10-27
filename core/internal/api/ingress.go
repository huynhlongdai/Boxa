package api

import (
	"io"
	"net"
	"net/http"
	"strings"

	"boxaz/core/internal/model"
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
		host := hostOnly(r.Host)
		if !s.M.IsIngressHost(r.Context(), host) {
			next.ServeHTTP(w, r)
			return
		}
		b, err := s.M.BoxForHost(r.Context(), host)
		if err != nil {
			http.Error(w, "boxaz: no running box at this address", http.StatusNotFound)
			return
		}
		if s.M.Settings(r.Context()).URLTokenProtect {
			want := s.M.URLToken(b.ID)
			tokParam := "boxaz_token"
			if r.URL.Query().Get(tokParam) == "" && r.URL.Query().Get(legacyTokenParam) != "" {
				tokParam = legacyTokenParam // compat: links minted before the rename
			}
			if t := r.URL.Query().Get(tokParam); t != "" {
				if t != want {
					http.Error(w, "boxaz: bad token", http.StatusForbidden)
					return
				}
				http.SetCookie(w, &http.Cookie{Name: "boxaz_t", Value: t, Path: "/", HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode})
				q := r.URL.Query()
				q.Del(tokParam)
				r.URL.RawQuery = q.Encode()
				http.Redirect(w, r, r.URL.RequestURI(), http.StatusFound)
				return
			}
			c, err := r.Cookie("boxaz_t")
			if err != nil {
				c, err = r.Cookie(legacyTokenCookie)
			}
			if err != nil || c.Value != want {
				http.Error(w, "boxaz: this box URL is token-protected (open the URL with ?boxaz_token=…)", http.StatusUnauthorized)
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
			http.Error(w, "boxaz: "+ae.Message, http.StatusBadGateway)
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

// Pre-rename names of the box URL token (query parameter and cookie), still
// accepted so existing links keep working.
const (
	legacyTokenParam  = "boxa_token"
	legacyTokenCookie = "boxa_t"
)
