package api

// v1.1 endpoints: quote, exec-batch, restore/fork, box yaml, agent kinds,
// tool schemas, error catalog, next-call hints.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"

	"boxmes/core/internal/agentkit"
	"boxmes/core/internal/i18n"
	"boxmes/core/internal/manager"
	"boxmes/core/internal/mcp"
	"boxmes/core/internal/model"
)

// publicBase is BOXMES_PUBLIC_URL when set, otherwise the request origin.
func (s *Server) publicBase(r *http.Request) string {
	if s.M.Cfg.PublicURL != "" {
		return strings.TrimRight(s.M.Cfg.PublicURL, "/")
	}
	return baseURL(r)
}

// withNext merges "next" hints into a JSON object response.
func withNext(v any, next []string) any {
	if len(next) == 0 {
		return v
	}
	b, err := json.Marshal(v)
	if err != nil {
		return v
	}
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		return v
	}
	m["next"] = next
	return m
}

func q(s string) string { b, _ := json.Marshal(s); return string(b) }

func nextForBox(b *model.Box) []string {
	id := q(b.ID)
	switch b.Status {
	case model.StatusRunning:
		n := []string{
			fmt.Sprintf(`box_write(box_id=%s, path="/work/main.py", content="...")`, id),
			fmt.Sprintf(`box_exec(box_id=%s, command="ls -la /work")`, id),
		}
		if b.URL != "" {
			n = append(n, fmt.Sprintf(`box_exec(box_id=%s, command="<start server on port %d>", async=true)`, id, b.Ports[0]))
		}
		if b.Lifecycle == model.LifecycleTask {
			n = append(n, fmt.Sprintf(`box_down(box_id=%s)  # when done`, id))
		} else {
			n = append(n, fmt.Sprintf(`box_snapshot(box_id=%s)  # before risky changes`, id))
		}
		return n
	case model.StatusFailed:
		return []string{`box_quote(...)  # check capacity/price`, `box_templates()`}
	case model.StatusTerminated, model.StatusSleeping:
		return nil
	}
	return []string{fmt.Sprintf(`GET /v1/boxes/%s  # poll until status=running (box_list)`, b.ID)}
}

func nextForExec(boxID string, res any) []string {
	switch v := res.(type) {
	case map[string]string:
		if j := v["job_id"]; j != "" {
			return []string{fmt.Sprintf(`box_job(job_id=%s, wait_s=30)`, q(j))}
		}
	case *model.ExecResult:
		var n []string
		id := q(boxID)
		if v.LogID != nil && (v.ExitCode != 0 || v.Truncated) {
			n = append(n, fmt.Sprintf(`box_logs(log_id=%s, tail_lines=200)`, q(*v.LogID)))
		}
		if v.ExitCode != 0 {
			n = append(n, fmt.Sprintf(`box_observe(box_id=%s)`, id))
		}
		if len(v.FilesChanged) > 0 {
			n = append(n, fmt.Sprintf(`box_read(box_id=%s, path=%s)`, id, q(v.FilesChanged[0])))
		}
		return n
	}
	return nil
}

func (s *Server) execBatch(w http.ResponseWriter, r *http.Request) {
	var in manager.BatchInput
	if err := decode(r, &in); err != nil {
		writeErr(w, r, err)
		return
	}
	res, err := s.M.ExecBatch(r.Context(), pr(r), chi.URLParam(r, "id"), in)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	var next []string
	if res.StoppedAt != nil {
		it := res.Results[*res.StoppedAt]
		if it.LogID != nil {
			next = append(next, fmt.Sprintf(`box_logs(log_id=%s, tail_lines=200)`, q(*it.LogID)))
		}
		next = append(next, fmt.Sprintf(`box_exec(box_id=%s, command=%s)  # after fixing`, q(chi.URLParam(r, "id")), q(it.Command)))
	}
	writeJSON(w, 200, withNext(res, next))
}

func (s *Server) quote(w http.ResponseWriter, r *http.Request) {
	var body createBoxBody
	if err := decode(r, &body); err != nil {
		writeErr(w, r, err)
		return
	}
	res, err := s.M.Quote(r.Context(), pr(r), body.Request)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if res.BudgetError != nil {
		res.BudgetError = localizeErr(r, res.BudgetError)
	}
	var next []string
	if res.Available && res.WithinBudget {
		next = []string{"box_up(<same arguments>)"}
	}
	writeJSON(w, 200, withNext(res, next))
}

func (s *Server) restore(w http.ResponseWriter, r *http.Request) {
	var in manager.RestoreInput
	if err := decode(r, &in); err != nil {
		writeErr(w, r, err)
		return
	}
	b, err := s.M.Restore(r.Context(), pr(r), chi.URLParam(r, "id"), in)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, b)
}

func (s *Server) fork(w http.ResponseWriter, r *http.Request) {
	var in manager.RestoreInput
	if err := decode(r, &in); err != nil {
		writeErr(w, r, err)
		return
	}
	b, err := s.M.Fork(r.Context(), pr(r), chi.URLParam(r, "id"), in)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 201, withNext(b, nextForBox(b)))
}

func (s *Server) boxYAML(w http.ResponseWriter, r *http.Request) {
	res, err := s.M.BoxYAML(r.Context(), pr(r), chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if strings.Contains(r.Header.Get("Accept"), "yaml") {
		w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
		w.Write([]byte(res["yaml"].(string)))
		return
	}
	writeJSON(w, 200, res)
}

func (s *Server) agentKinds(w http.ResponseWriter, r *http.Request) {
	list(w, agentkit.Kinds)
}

func (s *Server) tools(w http.ResponseWriter, r *http.Request) {
	f := r.URL.Query().Get("format")
	out, ok := mcp.ToolsAs(f)
	if !ok {
		writeErr(w, r, model.EH(model.ErrInvalid, "Use format=openai, anthropic, gemini or mcp.", "unknown tool format %q", f))
		return
	}
	writeJSON(w, 200, out)
}

type errDoc struct {
	Code        string `json:"code"`
	HTTPStatus  int    `json:"http_status"`
	Hint        string `json:"hint"`
	RetryAfterS *int   `json:"retry_after_s"`
	Retryable   bool   `json:"retryable"`
}

// errorCatalog serves GET /v1/errors (docs_url target), localized.
func (s *Server) errorCatalog(w http.ResponseWriter, r *http.Request) {
	loc := reqLocale(r)
	var out []errDoc
	for _, c := range model.Codes {
		d := errDoc{Code: c, HTTPStatus: model.StatusFor(c), Hint: i18n.Hint(loc, model.DefaultHint(c))}
		if c == model.ErrInternal {
			d.HTTPStatus = 500
		}
		if n := model.DefaultRetryAfter(c); n > 0 {
			d.RetryAfterS, d.Retryable = &n, true
		}
		out = append(out, d)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	writeJSON(w, 200, map[string]any{"locale": loc, "items": out})
}
