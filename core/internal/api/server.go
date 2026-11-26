// Package api is the REST/SSE/WebSocket/MCP gateway (spec M1) implementing
// docs/api-contract-v1.md.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"boxaz/core/internal/boxyaml"
	"boxaz/core/internal/i18n"
	"boxaz/core/internal/manager"
	"boxaz/core/internal/mcp"
	"boxaz/core/internal/model"
	"boxaz/core/internal/store"
)

type ctxKey int

const principalKey ctxKey = 1

type Server struct {
	M           *manager.Manager
	DistDir     string // serves /dl/<file> (boxd binaries for cloud-init)
	CORSOrigins string // "*" or comma list
	handler     http.Handler
}

func New(m *manager.Manager, distDir, cors string) *Server {
	s := &Server{M: m, DistDir: distDir, CORSOrigins: cors}
	s.handler = s.routes()
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.handler.ServeHTTP(w, r) }

func (s *Server) routes() http.Handler {
	r := chi.NewRouter()
	r.Use(s.recoverer, s.ingressMW, s.cors, s.localeMW)
	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"name": "boxaz-core", "version": manager.Version, "api": "/v1", "mcp": "/mcp", "health": "/v1/health"})
	})
	r.Get("/dl/{file}", s.download)
	mcpHandler := &mcp.HTTPHandler{
		Server: &mcp.Server{Name: "boxaz", Version: manager.Version},
		CallerFor: func(req *http.Request) mcp.Caller {
			return &inProcCaller{h: s, auth: req.Header.Get("Authorization"), lang: reqLocale(req)}
		},
	}
	r.With(s.authMW, s.sessionGuard).Handle("/mcp", mcpHandler)
	r.With(s.authMW, s.sessionGuard).Post("/a2a", s.a2a) // A2A JSON-RPC (message/send, tasks/get, tasks/cancel, message/stream)
	// Discovery (no auth).
	r.Get("/llms.txt", s.llmsTxt)
	r.Get("/llms-full.txt", s.llmsFull)
	r.Get("/.well-known/mcp.json", s.mcpCard)
	r.Get("/.well-known/agent.json", s.agentCard)
	r.Route("/v1", func(r chi.Router) {
		r.Get("/health", s.health)
		r.Get("/openapi.json", s.openapi)
		r.Get("/tools", s.tools)
		r.Get("/agent-kinds", s.agentKinds)
		r.Get("/profiles", s.profiles)
		r.Get("/errors", s.errorCatalog)
		r.Get("/internal/tls-ask", s.tlsAsk)             // Caddy on_demand_tls (bind core to localhost in production)
		r.Get("/hosts/connect", s.M.Hub.ServeHTTP)       // boxd dial-out (own token auth)
		r.Get("/ingress/connect", s.M.ServeIngressAgent) // in-box public-URL agent (own token auth)
		s.routesAccessWS(r)                              // v1.10 terminal / tcp / ssh WebSockets (ticket or Authorization header)
		s.routesTenancyPublic(r)                         // v2: sign-up, login, verify, reset, waitlist
		r.Group(func(r chi.Router) {
			r.Use(s.authMW, s.sessionGuard)
			s.routesTenancy(r)
			r.Get("/overview", s.overview)
			r.Get("/stream", s.stream)

			r.Post("/boxes", s.createBox)
			r.Get("/boxes", s.listBoxes)
			r.Get("/boxes/{id}", s.getBox)
			r.Delete("/boxes/{id}", s.deleteBox)
			r.Post("/boxes/{id}/sleep", s.boxVerb("sleep"))
			r.Post("/boxes/{id}/wake", s.boxVerb("wake"))
			r.Post("/boxes/{id}/snapshot", s.snapshot)
			r.Post("/boxes/{id}/takeover", s.boxVerb("takeover"))
			r.Post("/boxes/{id}/release", s.boxVerb("release"))
			r.Post("/boxes/{id}/exec", s.exec)
			r.Post("/boxes/{id}/exec-batch", s.execBatch)
			r.Post("/boxes/{id}/restore", s.restore)
			r.Post("/boxes/{id}/fork", s.fork)
			r.Get("/boxes/{id}/yaml", s.boxYAML)
			r.Post("/quote", s.quote)
			s.routesV13(r)
			s.routesV14(r)
			s.routesV17(r)
			r.Get("/boxes/{id}/files", s.readFile)
			r.Put("/boxes/{id}/files", s.writeFile)
			r.Post("/boxes/{id}/grep", s.grep)
			r.Get("/boxes/{id}/observe", s.observe)
			r.Get("/boxes/{id}/events", s.boxEvents)
			r.Get("/boxes/{id}/snapshots", s.boxSnapshots)
			s.routesAccess(r)

			r.Get("/jobs/{id}", s.getJob)
			r.Post("/jobs/{id}/cancel", s.cancelJob)
			r.Put("/boxes/{id}/egress", s.putEgress)
			r.Get("/logs/{id}", s.getLog)

			r.Get("/agents", s.listAgents)
			r.Post("/agents", s.createAgent)
			r.Get("/agents/{id}", s.getAgent)
			r.Patch("/agents/{id}", s.patchAgent)
			r.Delete("/agents/{id}", s.deleteAgent)
			r.Get("/agents/{id}/snippet", s.snippet)

			r.Get("/keys", s.listKeys)
			r.Post("/keys", s.createKey)
			r.Delete("/keys/{id}", s.revokeKey)

			r.Get("/costs", s.costs)
			r.Get("/costs.csv", s.costsCSV)
			r.Get("/costs/suggestions", s.suggestions)

			r.Get("/providers", s.providers)
			r.Put("/providers/{id}/token", s.providerToken)
			r.Get("/providers/{id}/regions", s.providerRegions)
			r.Get("/providers/{id}/servers", s.providerServers)
			r.Get("/hosts", s.hosts)
			r.Post("/hosts", s.createHost)
			r.Post("/hosts/{id}/drain", s.drainHost)
			r.Delete("/hosts/{id}", s.deleteHost)
			r.Get("/offers", s.offers)

			r.Get("/templates", s.listTemplates)
			r.Post("/templates", s.createTemplate)
			r.Post("/templates/validate", s.validateTemplate)
			r.Put("/templates/{name}", s.putTemplate)
			r.Delete("/templates/{name}", s.deleteTemplate)

			r.Get("/secrets", s.listSecrets)
			r.Post("/secrets", s.createSecret)
			r.Post("/secrets/{name}/rotate", s.rotateSecret)
			r.Delete("/secrets/{name}", s.deleteSecret)

			r.Get("/approvals", s.listApprovals)
			r.Post("/approvals/{id}/approve", s.decide(true))
			r.Post("/approvals/{id}/reject", s.decide(false))

			r.Post("/emergency-stop", s.emergencyStop)
			r.Get("/settings", s.getSettings)
			r.Put("/settings", s.putSettings)
			r.Get("/storage", s.getStorage)
			r.Put("/storage", s.putStorage)
			r.Post("/storage/test", s.testStorage)
			r.Get("/notify", s.getNotify)
			r.Put("/notify", s.putNotify)
			r.Post("/notify/test", s.testNotify)

			r.Route("/admin", func(r chi.Router) {
				r.Use(s.adminOnly)
				r.Get("/system", s.adminSystem)
				r.Get("/jobs", s.adminJobs)
				r.Get("/audit", s.adminAudit)
				r.Get("/quotas", s.getQuotas)
				r.Put("/quotas", s.putQuotas)
				s.routesTenancyAdmin(r)
				r.Post("/price-feed/refresh", s.refreshPrices)
			})
		})
	})
	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		writeErr(w, r, model.E(model.ErrNotFound, "no route %s %s", r.Method, r.URL.Path))
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		e := model.EH(model.ErrInvalid, "Check the HTTP method in the API contract.", "method not allowed")
		e.Status = 405
		writeErr(w, r, e)
	})
	return r
}

// ---- middleware & helpers ----

func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					panic(v)
				}
				log.Printf("panic %s %s: %v", r.Method, r.URL.Path, v)
				writeErr(w, r, fmt.Errorf("internal error"))
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func (s *Server) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && s.CORSOrigins != "" {
			allowed := s.CORSOrigins == "*"
			for _, o := range strings.Split(s.CORSOrigins, ",") {
				if strings.TrimSpace(o) == origin {
					allowed = true
				}
			}
			if allowed {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Vary", "Origin")
				w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Idempotency-Key, Mcp-Session-Id, Mcp-Protocol-Version")
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
				w.Header().Set("Access-Control-Expose-Headers", "Mcp-Session-Id")
			}
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func bearer(r *http.Request) string {
	a := r.Header.Get("Authorization")
	if strings.HasPrefix(a, "Bearer ") {
		return strings.TrimSpace(a[7:])
	}
	// EventSource and browser WebSockets cannot set headers.
	if t := r.URL.Query().Get("token"); t != "" {
		return t
	}
	// Dashboard keeps the key in an httpOnly cookie (boxaz_key).
	if c, err := r.Cookie("boxaz_key"); err == nil {
		return c.Value
	}
	if c, err := r.Cookie("boxa_key"); err == nil { // compat: pre-rename cookie
		return c.Value
	}
	return ""
}

func (s *Server) authMW(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, err := s.M.Authenticate(r.Context(), bearer(r))
		if err != nil {
			writeErr(w, r, err)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey, p)))
	})
}

func (s *Server) adminOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := pr(r).Require(model.ScopeAdmin); err != nil {
			writeErr(w, r, err)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func pr(r *http.Request) *manager.Principal {
	p, _ := r.Context().Value(principalKey).(*manager.Principal)
	return p
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.Encode(v)
}

// writeErr writes the contract error envelope, localized to the request's
// locale (?lang= / Accept-Language), with retry_after_s and docs_url.
func writeErr(w http.ResponseWriter, r *http.Request, err error) {
	ae := model.AsAPIError(err)
	if errors.Is(err, model.ErrNoRows) {
		ae = model.E(model.ErrNotFound, "not found")
	}
	le := localizeErr(r, ae)
	if le.RetryAfterS != nil && *le.RetryAfterS > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(*le.RetryAfterS))
	}
	writeJSON(w, ae.Status, map[string]any{"error": le})
}

func localizeErr(r *http.Request, ae *model.APIError) *model.APIError {
	loc := reqLocale(r)
	out := *ae
	if loc != i18n.Default {
		f, args := ae.Format()
		out.Message = i18n.Msg(loc, f, args...)
		out.Hint = i18n.Hint(loc, ae.Hint)
	}
	if out.RetryAfterS == nil {
		if n := model.DefaultRetryAfter(ae.Code); n > 0 {
			out.RetryAfterS = &n
		}
	}
	if len(out.Next) == 0 {
		out.Next = nextForError(ae.Code)
	}
	if out.DocsURL == "" && r != nil {
		out.DocsURL = baseURL(r) + "/v1/errors#" + ae.Code
	}
	return &out
}

func reqLocale(r *http.Request) string {
	if r == nil {
		return i18n.Default
	}
	if l, ok := i18n.Lookup(r.Context()); ok {
		return l
	}
	return i18n.FromRequest(r, i18n.Default)
}

// baseURL is the externally visible origin of the request (honours X-Forwarded-*).
func baseURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if p := r.Header.Get("X-Forwarded-Proto"); p != "" {
		scheme = strings.Split(p, ",")[0]
	}
	host := r.Host
	if h := r.Header.Get("X-Forwarded-Host"); h != "" {
		host = strings.Split(h, ",")[0]
	}
	if host == "" {
		host = "localhost:8080"
	}
	return scheme + "://" + host
}

// localeMW resolves the request locale once and stores it in the context so
// the manager (AGENTS.md, snippets) and error writer can use it.
func (s *Server) localeMW(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		loc := i18n.FromRequest(r, "")
		w.Header().Add("Vary", "Accept-Language")
		if loc == "" { // no explicit language: API default en; agent-facing texts fall back to the agent/settings locale
			w.Header().Set("Content-Language", i18n.Default)
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Language", loc)
		next.ServeHTTP(w, r.WithContext(i18n.WithLocale(r.Context(), loc)))
	})
}

func decode(r *http.Request, v any) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, 32<<20))
	if err != nil {
		return model.E(model.ErrInvalid, "cannot read body")
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return nil
	}
	if err := json.Unmarshal(body, v); err != nil {
		return model.EH(model.ErrInvalid, "Send a JSON body matching the API contract.", "invalid JSON: %v", err)
	}
	return nil
}

func list[T any](w http.ResponseWriter, items []T) { writeJSON(w, 200, model.NewList(items)) }

func qint(r *http.Request, k string) int {
	n, _ := strconv.Atoi(r.URL.Query().Get(k))
	return n
}

func parseTime(s string, end bool) time.Time {
	if s == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		if end {
			return t.AddDate(0, 0, 1)
		}
		return t
	}
	return time.Time{}
}

// ---- health / overview / stream ----

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	comps := map[string]string{}
	ok := true
	for _, c := range s.M.Components(r.Context()) {
		comps[c.Name] = c.Status
		if c.Name == "db" && c.Status != "ok" {
			ok = false
		}
	}
	status := 200
	if !ok {
		status = 503
	}
	writeJSON(w, status, map[string]any{"ok": ok, "version": manager.Version, "components": comps})
}

func (s *Server) overview(w http.ResponseWriter, r *http.Request) {
	o, err := s.M.Overview(r.Context(), pr(r))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, o)
}

func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, r, fmt.Errorf("streaming unsupported"))
		return
	}
	p := pr(r)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	fmt.Fprintf(w, "retry: 3000\n: connected\n\n")
	fl.Flush()
	ch := s.M.Broker.Subscribe()
	defer s.M.Broker.Unsubscribe(ch)
	hb := time.NewTicker(15 * time.Second)
	defer hb.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-hb.C:
			fmt.Fprintf(w, ": ping\n\n")
			fl.Flush()
		case m, ok := <-ch:
			if !ok {
				return
			}
			if !p.Admin && !m.Public && m.AgentID != p.AgentID {
				continue
			}
			b, _ := json.Marshal(m.Data)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", m.Type, b)
			fl.Flush()
		}
	}
}

// ---- boxes ----

type createBoxBody struct {
	boxyaml.Request
	IdemKey  string `json:"_idempotency_key"`
	IdemKey2 string `json:"idempotency_key"`
}

func (s *Server) createBox(w http.ResponseWriter, r *http.Request) {
	var body createBoxBody
	if err := decode(r, &body); err != nil {
		writeErr(w, r, err)
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		key = body.IdemKey
	}
	if key == "" {
		key = body.IdemKey2
	}
	if v := r.URL.Query().Get("wait"); v != "" {
		b := v != "false" && v != "0"
		body.Wait = &b
	}
	box, created, err := s.M.CreateBox(r.Context(), pr(r), body.Request, key)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	st := 201
	if !created {
		st = 200
	}
	writeJSON(w, st, withNext(box, nextForBox(box)))
}

func (s *Server) listBoxes(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.BoxFilter{Status: q.Get("status"), AgentID: q.Get("agent"), Provider: q.Get("provider"), Lifecycle: q.Get("lifecycle")}
	if q.Get("all") == "true" || f.Status == model.StatusTerminated || f.Status == model.StatusFailed {
		f.IncludeTerminated = true
	}
	bs, err := s.M.ListBoxes(r.Context(), pr(r), f)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	list(w, bs)
}

func (s *Server) getBox(w http.ResponseWriter, r *http.Request) {
	b, err := s.M.GetBox(r.Context(), pr(r), chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, b)
}

func (s *Server) deleteBox(w http.ResponseWriter, r *http.Request) {
	b, err := s.M.Terminate(r.Context(), pr(r), chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, b)
}

func (s *Server) boxVerb(verb string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		var b *model.Box
		var err error
		switch verb {
		case "sleep":
			b, err = s.M.Sleep(r.Context(), pr(r), id)
		case "wake":
			b, err = s.M.Wake(r.Context(), pr(r), id)
		case "takeover":
			b, err = s.M.Takeover(r.Context(), pr(r), id, true)
		case "release":
			b, err = s.M.Takeover(r.Context(), pr(r), id, false)
		}
		if err != nil {
			writeErr(w, r, err)
			return
		}
		writeJSON(w, 200, b)
	}
}

func (s *Server) snapshot(w http.ResponseWriter, r *http.Request) {
	sn, err := s.M.Snapshot(r.Context(), pr(r), chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 201, sn)
}

func (s *Server) exec(w http.ResponseWriter, r *http.Request) {
	var in manager.ExecInput
	if err := decode(r, &in); err != nil {
		writeErr(w, r, err)
		return
	}
	res, err := s.M.Exec(r.Context(), pr(r), chi.URLParam(r, "id"), in)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	st := 200
	if in.Async {
		st = 202
	}
	writeJSON(w, st, withNext(res, nextForExec(chi.URLParam(r, "id"), res)))
}

func (s *Server) readFile(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	res, err := s.M.ReadFile(r.Context(), pr(r), chi.URLParam(r, "id"), q.Get("path"), qint(r, "from"), qint(r, "to"))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if res.IsDir {
		writeJSON(w, 200, map[string]any{"path": q.Get("path"), "type": "dir", "entries": res.Entries})
		return
	}
	writeJSON(w, 200, map[string]any{"path": q.Get("path"), "type": "file", "content": res.Content, "total_lines": res.TotalLines})
}

func (s *Server) writeFile(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, r, err)
		return
	}
	if err := s.M.WriteFile(r.Context(), pr(r), chi.URLParam(r, "id"), in.Path, in.Content); err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "path": in.Path, "bytes": len(in.Content)})
}

func (s *Server) grep(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Pattern string `json:"pattern"`
		Path    string `json:"path"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, r, err)
		return
	}
	res, err := s.M.Grep(r.Context(), pr(r), chi.URLParam(r, "id"), in.Pattern, in.Path)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, res)
}

func (s *Server) observe(w http.ResponseWriter, r *http.Request) {
	res, err := s.M.Observe(r.Context(), pr(r), chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, res)
}

func (s *Server) boxEvents(w http.ResponseWriter, r *http.Request) {
	es, err := s.M.BoxEvents(r.Context(), pr(r), chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	list(w, es)
}

func (s *Server) boxSnapshots(w http.ResponseWriter, r *http.Request) {
	ss, err := s.M.ListSnapshots(r.Context(), pr(r), chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	list(w, ss)
}

func (s *Server) getJob(w http.ResponseWriter, r *http.Request) {
	wait := qint(r, "wait")
	if wait > 60 {
		wait = 60
	}
	j, err := s.M.WaitJob(r.Context(), pr(r), chi.URLParam(r, "id"), time.Duration(wait)*time.Second)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, j)
}

func (s *Server) getLog(w http.ResponseWriter, r *http.Request) {
	txt, err := s.M.GetLog(r.Context(), pr(r), chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	io.WriteString(w, txt)
}

// ---- agents & keys ----

func (s *Server) listAgents(w http.ResponseWriter, r *http.Request) {
	as, err := s.M.ListAgents(r.Context(), pr(r))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	list(w, as)
}

func (s *Server) createAgent(w http.ResponseWriter, r *http.Request) {
	var in manager.AgentInput
	if err := decode(r, &in); err != nil {
		writeErr(w, r, err)
		return
	}
	a, err := s.M.CreateAgent(r.Context(), pr(r), in)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 201, a)
}

func (s *Server) getAgent(w http.ResponseWriter, r *http.Request) {
	a, err := s.M.GetAgent(r.Context(), pr(r), chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, a)
}

func (s *Server) patchAgent(w http.ResponseWriter, r *http.Request) {
	var in manager.AgentInput
	if err := decode(r, &in); err != nil {
		writeErr(w, r, err)
		return
	}
	a, err := s.M.UpdateAgent(r.Context(), pr(r), chi.URLParam(r, "id"), in)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, a)
}

func (s *Server) deleteAgent(w http.ResponseWriter, r *http.Request) {
	if err := s.M.DeleteAgent(r.Context(), pr(r), chi.URLParam(r, "id")); err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) snippet(w http.ResponseWriter, r *http.Request) {
	sn, err := s.M.Snippet(r.Context(), pr(r), chi.URLParam(r, "id"), r.URL.Query().Get("kind"), r.URL.Query().Get("lang"), s.publicBase(r))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, sn)
}

func (s *Server) listKeys(w http.ResponseWriter, r *http.Request) {
	ks, err := s.M.ListKeys(r.Context(), pr(r))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	list(w, ks)
}

func (s *Server) createKey(w http.ResponseWriter, r *http.Request) {
	var in manager.KeyInput
	if err := decode(r, &in); err != nil {
		writeErr(w, r, err)
		return
	}
	k, err := s.M.CreateKey(r.Context(), pr(r), in)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 201, k)
}

func (s *Server) revokeKey(w http.ResponseWriter, r *http.Request) {
	if err := s.M.RevokeKey(r.Context(), pr(r), chi.URLParam(r, "id")); err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// ---- costs ----

func ledgerFilter(r *http.Request) store.LedgerFilter {
	q := r.URL.Query()
	return store.LedgerFilter{AgentID: q.Get("agent"), Provider: q.Get("provider"), BoxID: q.Get("box"),
		From: parseTime(q.Get("from"), false), To: parseTime(q.Get("to"), true), Limit: qint(r, "limit")}
}

func (s *Server) costs(w http.ResponseWriter, r *http.Request) {
	res, err := s.M.Costs(r.Context(), pr(r), ledgerFilter(r))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, res)
}

func (s *Server) costsCSV(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="boxaz-costs.csv"`)
	if err := s.M.CostsCSV(r.Context(), pr(r), ledgerFilter(r), w); err != nil {
		log.Printf("csv: %v", err)
	}
}

func (s *Server) suggestions(w http.ResponseWriter, r *http.Request) {
	ss, err := s.M.Suggestions(r.Context(), pr(r))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	list(w, ss)
}

// ---- infra ----

func (s *Server) providers(w http.ResponseWriter, r *http.Request) {
	ps, err := s.M.ListProviders(r.Context())
	if err != nil {
		writeErr(w, r, err)
		return
	}
	list(w, ps)
}

func (s *Server) providerToken(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Token string `json:"token"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, r, err)
		return
	}
	p, err := s.M.SetProviderToken(r.Context(), pr(r), chi.URLParam(r, "id"), in.Token)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, p)
}

func (s *Server) hosts(w http.ResponseWriter, r *http.Request) {
	if err := pr(r).Require(model.ScopeAdmin); err != nil {
		writeErr(w, r, err)
		return
	}
	hs, err := s.M.ListHosts(r.Context())
	if err != nil {
		writeErr(w, r, err)
		return
	}
	list(w, hs)
}

func (s *Server) createHost(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Provider, SKU, Region, Billing string
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, r, err)
		return
	}
	h, err := s.M.CreateHost(r.Context(), pr(r), in.Provider, in.SKU, in.Region, in.Billing)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 201, h)
}

func (s *Server) drainHost(w http.ResponseWriter, r *http.Request) {
	h, err := s.M.DrainHost(r.Context(), pr(r), chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, h)
}

func (s *Server) deleteHost(w http.ResponseWriter, r *http.Request) {
	if err := s.M.DeleteHost(r.Context(), pr(r), chi.URLParam(r, "id"), r.URL.Query().Get("force") == "true"); err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) offers(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	minCPU, _ := strconv.ParseFloat(q.Get("min_cpu"), 64)
	os, err := s.M.ListOffers(r.Context(), manager.OfferFilter{Provider: q.Get("provider"), Region: q.Get("region"), MinCPU: minCPU, MinRAMMB: qint(r, "min_ram_mb")})
	if err != nil {
		writeErr(w, r, err)
		return
	}
	list(w, os)
}

// ---- templates ----

func (s *Server) listTemplates(w http.ResponseWriter, r *http.Request) {
	ts, err := s.M.ListTemplates(r.Context())
	if err != nil {
		writeErr(w, r, err)
		return
	}
	list(w, ts)
}

func (s *Server) createTemplate(w http.ResponseWriter, r *http.Request) {
	var in manager.TemplateInput
	if err := decode(r, &in); err != nil {
		writeErr(w, r, err)
		return
	}
	t, err := s.M.PutTemplate(r.Context(), pr(r), in, true)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 201, t)
}

func (s *Server) putTemplate(w http.ResponseWriter, r *http.Request) {
	var in manager.TemplateInput
	if err := decode(r, &in); err != nil {
		writeErr(w, r, err)
		return
	}
	in.Name = chi.URLParam(r, "name")
	t, err := s.M.PutTemplate(r.Context(), pr(r), in, false)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, t)
}

func (s *Server) deleteTemplate(w http.ResponseWriter, r *http.Request) {
	if err := s.M.DeleteTemplate(r.Context(), pr(r), chi.URLParam(r, "name")); err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) validateTemplate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		YAML string `json:"yaml"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, r, err)
		return
	}
	_, errs := boxyaml.Parse(in.YAML)
	if errs == nil {
		errs = boxyaml.Errors{}
	}
	writeJSON(w, 200, map[string]any{"ok": len(errs) == 0, "errors": errs})
}

// ---- secrets ----

func (s *Server) listSecrets(w http.ResponseWriter, r *http.Request) {
	ss, err := s.M.ListSecrets(r.Context(), pr(r))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	list(w, ss)
}

func (s *Server) createSecret(w http.ResponseWriter, r *http.Request) {
	var in manager.SecretInput
	if err := decode(r, &in); err != nil {
		writeErr(w, r, err)
		return
	}
	sec, err := s.M.CreateSecret(r.Context(), pr(r), in)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 201, sec)
}

func (s *Server) rotateSecret(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Value  string   `json:"value"`
		Agents []string `json:"agents"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, r, err)
		return
	}
	sec, err := s.M.RotateSecret(r.Context(), pr(r), chi.URLParam(r, "name"), in.Value, in.Agents)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, sec)
}

func (s *Server) deleteSecret(w http.ResponseWriter, r *http.Request) {
	if err := s.M.DeleteSecret(r.Context(), pr(r), chi.URLParam(r, "name")); err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// ---- approvals / settings / emergency ----

func (s *Server) listApprovals(w http.ResponseWriter, r *http.Request) {
	as, err := s.M.ListApprovals(r.Context(), pr(r), r.URL.Query().Get("status"))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	list(w, as)
}

func (s *Server) decide(approve bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		a, err := s.M.Decide(r.Context(), pr(r), chi.URLParam(r, "id"), approve)
		if err != nil {
			writeErr(w, r, err)
			return
		}
		writeJSON(w, 200, a)
	}
}

func (s *Server) emergencyStop(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Confirm string `json:"confirm"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, r, err)
		return
	}
	n, err := s.M.EmergencyStop(r.Context(), pr(r), in.Confirm)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "slept": n})
}

func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	if err := pr(r).Require(model.ScopeAdmin); err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, s.M.Settings(r.Context()))
}

func (s *Server) putSettings(w http.ResponseWriter, r *http.Request) {
	if err := pr(r).Require(model.ScopeAdmin); err != nil {
		writeErr(w, r, err)
		return
	}
	cur := s.M.Settings(r.Context())
	if err := decode(r, &cur); err != nil { // partial update: unknown fields keep current values
		writeErr(w, r, err)
		return
	}
	out, err := s.M.PutSettings(r.Context(), cur)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, out)
}

// ---- admin ----

func (s *Server) adminSystem(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{
		"version": manager.Version, "uptime_s": int(s.M.Uptime().Seconds()), "db": s.M.Store.Dialect(),
		"auto_burst": s.M.Cfg.AutoBurst, "public_url": s.M.Cfg.PublicURL,
		"components": s.M.Components(r.Context()),
	})
}

func (s *Server) adminJobs(w http.ResponseWriter, r *http.Request) { list(w, s.M.SysJobs()) }

func (s *Server) adminAudit(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	es, err := s.M.Audit(r.Context(), pr(r), store.EventFilter{Actor: q.Get("actor"), Type: q.Get("type"), BoxID: q.Get("box"), AgentID: q.Get("agent"), Limit: qint(r, "limit")})
	if err != nil {
		writeErr(w, r, err)
		return
	}
	list(w, es)
}

func (s *Server) getQuotas(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.M.Quotas(r.Context()))
}

func (s *Server) putQuotas(w http.ResponseWriter, r *http.Request) {
	q := s.M.Quotas(r.Context())
	if err := decode(r, &q); err != nil {
		writeErr(w, r, err)
		return
	}
	out, err := s.M.PutQuotas(r.Context(), q)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, out)
}


func (s *Server) refreshPrices(w http.ResponseWriter, r *http.Request) {
	err := s.M.RefreshPricesNow(r.Context())
	res := map[string]any{"ok": err == nil}
	if err != nil {
		res["error"] = err.Error()
	}
	writeJSON(w, 200, res)
}

// ---- downloads ----

func (s *Server) download(w http.ResponseWriter, r *http.Request) {
	f := filepath.Base(chi.URLParam(r, "file"))
	if s.DistDir == "" || !strings.HasPrefix(f, "boxd") {
		writeErr(w, r, model.E(model.ErrNotFound, "no such download"))
		return
	}
	p := filepath.Join(s.DistDir, f)
	if _, err := os.Stat(p); err != nil {
		writeErr(w, r, model.E(model.ErrNotFound, "no such download"))
		return
	}
	http.ServeFile(w, r, p)
}

// ---- in-process caller for MCP tools ----

type inProcCaller struct {
	h    http.Handler
	auth string
	lang string
}

func (c *inProcCaller) Do(ctx context.Context, method, path string, body any) (int, []byte, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		rd = bytes.NewReader(b)
	}
	// Drop the outer request's chi routing context: chi would otherwise treat
	// this as a sub-route of POST /mcp (wrong method/path → 405 for GETs).
	ctx = context.WithValue(ctx, chi.RouteCtxKey, nil)
	req := httptest.NewRequest(method, "/v1"+path, rd).WithContext(ctx)
	req.Header.Set("Authorization", c.auth)
	req.Header.Set("Content-Type", "application/json")
	if c.lang != "" {
		req.Header.Set("Accept-Language", c.lang)
	}
	rec := httptest.NewRecorder()
	c.h.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes(), nil
}
