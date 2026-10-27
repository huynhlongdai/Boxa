package api

// v1.7 routes: app spec + supervisor, wake settings, scoped URL tokens,
// provider lifecycle capabilities.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"boxaz/core/internal/manager"
	"boxaz/core/internal/model"
)

func (s *Server) routesV17(r chi.Router) {
	r.Patch("/boxes/{id}", s.patchBox)
	r.Get("/boxes/{id}/app", s.getApp)
	r.Put("/boxes/{id}/app", s.putApp)
	r.Delete("/boxes/{id}/app", s.deleteApp)
	r.Post("/boxes/{id}/app/restart", s.restartApp)
	r.Get("/boxes/{id}/app/logs", s.appLogs)
	r.Put("/boxes/{id}/ports/{port}", s.putPortAccess)
	r.Get("/boxes/{id}/ports/{port}/tokens", s.listURLTokens)
	r.Post("/boxes/{id}/ports/{port}/tokens", s.createURLToken)
	r.Delete("/boxes/{id}/ports/{port}/tokens/{token_id}", s.revokeURLToken)
	r.Get("/capabilities", s.capabilities)
}

func portParam(r *http.Request) (int, error) {
	p, err := strconv.Atoi(chi.URLParam(r, "port"))
	if err != nil || p < 1 || p > 65535 {
		return 0, model.EH(model.ErrInvalid, "Use the port number, e.g. /ports/8000/tokens.", "bad port")
	}
	return p, nil
}

func (s *Server) patchBox(w http.ResponseWriter, r *http.Request) {
	var in manager.PatchBoxInput
	if err := decode(r, &in); err != nil {
		writeErr(w, r, err)
		return
	}
	b, err := s.M.PatchBox(r.Context(), pr(r), chi.URLParam(r, "id"), in)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, b)
}

func (s *Server) getApp(w http.ResponseWriter, r *http.Request) {
	v, err := s.M.GetApp(r.Context(), pr(r), chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *Server) putApp(w http.ResponseWriter, r *http.Request) {
	var in model.AppSpec
	if err := decode(r, &in); err != nil {
		writeErr(w, r, err)
		return
	}
	v, err := s.M.SetApp(r.Context(), pr(r), chi.URLParam(r, "id"), in)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *Server) deleteApp(w http.ResponseWriter, r *http.Request) {
	v, err := s.M.DeleteApp(r.Context(), pr(r), chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *Server) restartApp(w http.ResponseWriter, r *http.Request) {
	v, err := s.M.RestartApp(r.Context(), pr(r), chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, v)
}

// appLogs: ?offset=<n> (default -1 = tail), ?max=<bytes>, ?follow=1 → SSE
// events "log" {data, offset, truncated} until the client goes away.
func (s *Server) appLogs(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	off := int64(-1)
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			off = n
		}
	}
	max, _ := strconv.Atoi(r.URL.Query().Get("max"))
	if max <= 0 {
		max = 16 << 10
	}
	follow := r.URL.Query().Get("follow") == "1" || r.URL.Query().Get("follow") == "true"
	res, err := s.M.AppLogs(ctx, pr(r), id, off, max)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if !follow {
		writeJSON(w, 200, res)
		return
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, r, fmt.Errorf("streaming unsupported"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	send := func(v any) {
		b, _ := json.Marshal(v)
		fmt.Fprintf(w, "event: log\ndata: %s\n\n", b)
		fl.Flush()
	}
	send(res)
	next := res.Offset
	poll := time.NewTicker(time.Second)
	defer poll.Stop()
	idle := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-poll.C:
		}
		res, err := s.M.AppLogs(ctx, pr(r), id, next, 64<<10)
		if err != nil {
			fmt.Fprintf(w, "event: error\ndata: %q\n\n", model.AsAPIError(err).Message)
			fl.Flush()
			return
		}
		if res.Data != "" || res.Truncated {
			send(res)
			idle = 0
		} else if idle++; idle%15 == 0 {
			fmt.Fprintf(w, ": ping\n\n")
			fl.Flush()
		}
		next = res.Offset
	}
}

func (s *Server) putPortAccess(w http.ResponseWriter, r *http.Request) {
	port, err := portParam(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	var in manager.PortAccessInput
	if err := decode(r, &in); err != nil {
		writeErr(w, r, err)
		return
	}
	v, err := s.M.SetPortAccess(r.Context(), pr(r), chi.URLParam(r, "id"), port, in)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *Server) listURLTokens(w http.ResponseWriter, r *http.Request) {
	port, err := portParam(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	v, err := s.M.ListURLTokens(r.Context(), pr(r), chi.URLParam(r, "id"), port)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *Server) createURLToken(w http.ResponseWriter, r *http.Request) {
	port, err := portParam(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	var in manager.CreateURLTokenInput
	if r.ContentLength != 0 {
		if err := decode(r, &in); err != nil {
			writeErr(w, r, err)
			return
		}
	}
	t, err := s.M.CreateURLToken(r.Context(), pr(r), chi.URLParam(r, "id"), port, in)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 201, t)
}

func (s *Server) revokeURLToken(w http.ResponseWriter, r *http.Request) {
	port, err := portParam(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	t, err := s.M.RevokeURLToken(r.Context(), pr(r), chi.URLParam(r, "id"), port, chi.URLParam(r, "token_id"))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, t)
}

func (s *Server) capabilities(w http.ResponseWriter, r *http.Request) {
	list(w, s.M.Capabilities(r.Context()))
}
