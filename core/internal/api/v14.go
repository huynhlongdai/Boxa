package api

// v1.4 endpoints: T2/T3 sandbox providers (adapter metadata, capabilities,
// per-second prices, verification level) and the declarative adapter linter.

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"boxmes/core/internal/model"
	"boxmes/core/internal/sandbox"
)

func (s *Server) routesV14(r chi.Router) {
	r.Get("/sandbox/providers", s.sandboxProviders)
	r.Post("/sandbox/adapters/lint", s.sandboxLint)
}

func (s *Server) sandboxProviders(w http.ResponseWriter, r *http.Request) {
	res, err := s.M.SandboxProviders(r.Context(), pr(r))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	list(w, res)
}

// LintRequest is POST /v1/sandbox/adapters/lint.
type LintRequest struct {
	YAML string `json:"yaml"`
}

// LintResult reports whether a declarative adapter parses.
type LintResult struct {
	OK    bool          `json:"ok"`
	Error string        `json:"error,omitempty"`
	Info  *sandbox.Info `json:"info,omitempty"`
}

func (s *Server) sandboxLint(w http.ResponseWriter, r *http.Request) {
	if err := pr(r).Require(model.ScopeAdmin); err != nil {
		writeErr(w, r, err)
		return
	}
	var body LintRequest
	if err := decode(r, &body); err != nil {
		writeErr(w, r, err)
		return
	}
	spec, err := sandbox.ParseYAML([]byte(body.YAML))
	if err != nil {
		writeJSON(w, 200, LintResult{Error: err.Error()})
		return
	}
	info := sandbox.NewYAMLDriver(*spec, nil, "").Info()
	writeJSON(w, 200, LintResult{OK: true, Info: &info})
}
