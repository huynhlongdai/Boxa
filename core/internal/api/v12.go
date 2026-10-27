package api

// v1.2 endpoints: object storage, notifications, egress, provider
// regions/servers, job cancel.

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"boxaz/core/internal/manager"
)

func (s *Server) getStorage(w http.ResponseWriter, r *http.Request) {
	v, err := s.M.StorageView(r.Context(), pr(r))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *Server) putStorage(w http.ResponseWriter, r *http.Request) {
	var in manager.StorageInput
	if err := decode(r, &in); err != nil {
		writeErr(w, r, err)
		return
	}
	v, err := s.M.PutStorage(r.Context(), pr(r), in)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *Server) testStorage(w http.ResponseWriter, r *http.Request) {
	v, err := s.M.TestStorage(r.Context(), pr(r))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *Server) getNotify(w http.ResponseWriter, r *http.Request) {
	v, err := s.M.NotifyView(r.Context(), pr(r))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *Server) putNotify(w http.ResponseWriter, r *http.Request) {
	var in manager.NotifyInput
	if err := decode(r, &in); err != nil {
		writeErr(w, r, err)
		return
	}
	v, err := s.M.PutNotify(r.Context(), pr(r), in)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *Server) testNotify(w http.ResponseWriter, r *http.Request) {
	var in manager.NotifyTestInput
	if err := decode(r, &in); err != nil {
		writeErr(w, r, err)
		return
	}
	v, err := s.M.TestNotify(r.Context(), pr(r), in)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *Server) putEgress(w http.ResponseWriter, r *http.Request) {
	var in manager.EgressInput
	if err := decode(r, &in); err != nil {
		writeErr(w, r, err)
		return
	}
	b, err := s.M.SetEgress(r.Context(), pr(r), chi.URLParam(r, "id"), in)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, b)
}

func (s *Server) providerRegions(w http.ResponseWriter, r *http.Request) {
	rs, err := s.M.ProviderRegions(r.Context(), pr(r), chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	list(w, rs)
}

func (s *Server) providerServers(w http.ResponseWriter, r *http.Request) {
	ss, err := s.M.ProviderServers(r.Context(), pr(r), chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	list(w, ss)
}

func (s *Server) cancelJob(w http.ResponseWriter, r *http.Request) {
	j, err := s.M.CancelJob(r.Context(), pr(r), chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, j)
}
