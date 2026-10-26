package api

// v1.3 endpoints: smart provider manager (health, prefs, validate, catalog),
// need-based selection (recommend, profiles), speed (warm pool, clone,
// right-sizing), agent support (ensure, resume) and the headscale mesh.

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"boxa/core/internal/manager"
	"boxa/core/internal/model"
)

func (s *Server) routesV13(r chi.Router) {
	r.Post("/recommend", s.recommend)
	r.Post("/boxes/ensure", s.ensure)
	r.Get("/boxes/{id}/resume", s.resume)
	r.Post("/boxes/{id}/clone", s.clone)
	r.Get("/boxes/{id}/rightsize", s.rightsizeBox)
	r.Get("/rightsize", s.rightsizeAll)
	r.Get("/catalog", s.catalog)
	r.Get("/providers/health", s.providersHealth)
	r.Get("/providers/{id}/health", s.providerHealth)
	r.Post("/providers/{id}/validate", s.providerValidate)
	r.Post("/providers/{id}/check", s.providerCheck)
	r.Post("/providers/{id}/reset", s.providerReset)
	r.Get("/providers/{id}/prefs", s.getProviderPrefs)
	r.Put("/providers/{id}/prefs", s.putProviderPrefs)
	r.Get("/warm-pool", s.getWarmPool)
	r.Put("/warm-pool", s.putWarmPool)
	r.Get("/speed", s.speed)
	r.Get("/mesh", s.meshStatus)
	r.Get("/mesh/config", s.meshConfig)
	r.Get("/mesh/acl", s.meshACL)
	r.Post("/mesh/keys", s.meshKey)
}

// nextForError suggests the tool calls that usually resolve an error code.
func nextForError(code string) []string {
	switch code {
	case model.ErrNoCapacity:
		return []string{"box_recommend(<same needs>)  # see ranked options and why others were rejected", "box_up(<smaller cpu/ram_mb>)"}
	case model.ErrBudgetExceeded:
		return []string{"box_cost()  # see spend vs budget", "box_list(status=\"running\")  # then box_sleep / box_down unused boxes"}
	case model.ErrBoxSleeping:
		return []string{"box_sleep(box_id=<id>, wake=true)", "box_ensure(name=<name>)  # wakes or recreates"}
	case model.ErrNotFound:
		return []string{"box_list()  # find the right id or slug", "box_ensure(name=<name>)  # get-or-create by name"}
	case model.ErrProviderFailed:
		return []string{"box_recommend(<same needs>)  # Boxa already fails over; check other options", "retry after retry_after_s"}
	case model.ErrPolicyDenied:
		return []string{"box_quote(<smaller spec>)", "ask the operator to change the agent policy or quotas"}
	case model.ErrTimeout:
		return []string{"box_exec(..., async=true) then box_job(job_id, wait_s=30)"}
	case model.ErrInvalid:
		return []string{"GET /v1/openapi.json or box_templates() / GET /v1/profiles for valid values"}
	case model.ErrRateLimited:
		return []string{"wait retry_after_s, then retry", "box_exec_batch(...)  # fewer round-trips"}
	}
	return nil
}

func (s *Server) recommend(w http.ResponseWriter, r *http.Request) {
	var in manager.RecommendInput
	if err := decode(r, &in); err != nil {
		writeErr(w, r, err)
		return
	}
	res, err := s.M.Recommend(r.Context(), pr(r), in)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	var next []string
	if res.Pick != nil {
		next = append(next, "box_up(profile="+q(res.Profile.ID)+", template="+q(res.Template)+", ...)  # or box_ensure(name=..., same args)")
	}
	writeJSON(w, 200, withNext(res, next))
}

func (s *Server) profiles(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.M.Profiles())
}

func (s *Server) ensure(w http.ResponseWriter, r *http.Request) {
	var in manager.EnsureInput
	if err := decode(r, &in); err != nil {
		writeErr(w, r, err)
		return
	}
	res, err := s.M.Ensure(r.Context(), pr(r), in)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	st := 200
	if res.Created {
		st = 201
	}
	writeJSON(w, st, withNext(res, nextForBox(res.Box)))
}

func (s *Server) resume(w http.ResponseWriter, r *http.Request) {
	res, err := s.M.Resume(r.Context(), pr(r), chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, res)
}

func (s *Server) clone(w http.ResponseWriter, r *http.Request) {
	var in manager.CloneInput
	if err := decode(r, &in); err != nil {
		writeErr(w, r, err)
		return
	}
	b, err := s.M.Clone(r.Context(), pr(r), chi.URLParam(r, "id"), in)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 201, withNext(b, nextForBox(b)))
}

func (s *Server) rightsizeBox(w http.ResponseWriter, r *http.Request) {
	res, err := s.M.RightSizeBox(r.Context(), pr(r), chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, res)
}

func (s *Server) rightsizeAll(w http.ResponseWriter, r *http.Request) {
	res, err := s.M.RightSizeAll(r.Context(), pr(r))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	list(w, res)
}

func qf(r *http.Request, k string) float64 {
	f, _ := strconv.ParseFloat(r.URL.Query().Get(k), 64)
	return f
}

func (s *Server) catalog(w http.ResponseWriter, r *http.Request) {
	qs := r.URL.Query()
	c, err := s.M.GetCatalog(r.Context(), manager.CatalogFilter{Provider: qs.Get("provider"), Region: qs.Get("region"), Arch: qs.Get("arch"),
		MinCPU: qf(r, "min_cpu"), MinRAMMB: qint(r, "min_ram_mb"), MaxPriceHour: qf(r, "max_price_hour"), Limit: qint(r, "limit")})
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, c)
}

func (s *Server) providersHealth(w http.ResponseWriter, r *http.Request) {
	res, err := s.M.ProviderHealthAll(r.Context(), pr(r))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	list(w, res)
}

func (s *Server) providerHealth(w http.ResponseWriter, r *http.Request) {
	res, err := s.M.ProviderHealthOne(r.Context(), pr(r), chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, res)
}

func (s *Server) providerValidate(w http.ResponseWriter, r *http.Request) {
	var body tokenReq
	if err := decode(r, &body); err != nil {
		writeErr(w, r, err)
		return
	}
	res, err := s.M.ValidateProviderToken(r.Context(), pr(r), chi.URLParam(r, "id"), body.Token)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, res)
}

func (s *Server) providerCheck(w http.ResponseWriter, r *http.Request) {
	res, err := s.M.CheckProviderNow(r.Context(), pr(r), chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, res)
}

func (s *Server) providerReset(w http.ResponseWriter, r *http.Request) {
	res, err := s.M.ResetProviderBreaker(r.Context(), pr(r), chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, res)
}

func (s *Server) getProviderPrefs(w http.ResponseWriter, r *http.Request) {
	if err := pr(r).Require(model.ScopeAdmin); err != nil {
		writeErr(w, r, err)
		return
	}
	id := chi.URLParam(r, "id")
	if _, ok := s.M.Providers.Get(id); !ok {
		writeErr(w, r, model.E(model.ErrNotFound, "%s %q not found", "provider", id))
		return
	}
	writeJSON(w, 200, s.M.ProviderPrefs(r.Context(), id))
}

func (s *Server) putProviderPrefs(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	cur := s.M.ProviderPrefs(r.Context(), id)
	if err := decode(r, &cur); err != nil { // partial update over the stored prefs
		writeErr(w, r, err)
		return
	}
	res, err := s.M.PutProviderPrefs(r.Context(), pr(r), id, cur)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, res)
}

func (s *Server) getWarmPool(w http.ResponseWriter, r *http.Request) {
	res, err := s.M.GetWarmPool(r.Context(), pr(r))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, res)
}

func (s *Server) putWarmPool(w http.ResponseWriter, r *http.Request) {
	var in manager.WarmConfig
	if err := decode(r, &in); err != nil {
		writeErr(w, r, err)
		return
	}
	res, err := s.M.PutWarmPool(r.Context(), pr(r), in)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, res)
}

func (s *Server) speed(w http.ResponseWriter, r *http.Request) {
	res, err := s.M.Speed(r.Context(), pr(r))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, res)
}

func (s *Server) meshStatus(w http.ResponseWriter, r *http.Request) {
	res, err := s.M.MeshStatus(r.Context(), pr(r))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, res)
}

func (s *Server) meshConfig(w http.ResponseWriter, r *http.Request) {
	res, err := s.M.MeshConfigYAML(r.Context(), pr(r))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if r.URL.Query().Get("format") == "yaml" {
		w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
		fmt.Fprint(w, res["config"])
		return
	}
	writeJSON(w, 200, res)
}

func (s *Server) meshACL(w http.ResponseWriter, r *http.Request) {
	res, err := s.M.MeshACL(r.Context(), pr(r))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, res)
}

func (s *Server) meshKey(w http.ResponseWriter, r *http.Request) {
	var in manager.MeshKeyInput
	if err := decode(r, &in); err != nil {
		writeErr(w, r, err)
		return
	}
	res, err := s.M.MeshKey(r.Context(), pr(r), in)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 201, res)
}
