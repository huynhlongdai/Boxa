package api

// v2.2 (M29 phase A) platform-admin infra API: provider accounts, account
// strategy, provider overview and infra alerts under /v1/admin. The legacy
// /v1/providers/* routes stay as they are (PUT /providers/{id}/token
// writes the provider's default account).

import (
	"net/http"
	"net/url"

	"github.com/go-chi/chi/v5"

	"boxmes/core/internal/manager"
	"boxmes/core/internal/model"
)

// routesInfraAdmin is mounted inside /v1/admin (adminOnly).
func (s *Server) routesInfraAdmin(r chi.Router) {
	r.Get("/providers", s.adminProviders)
	r.Get("/providers/{p}", s.adminProvider)
	r.Get("/providers/{p}/strategy", s.getStrategy)
	r.Put("/providers/{p}/strategy", s.putStrategy)
	r.Get("/providers/{p}/accounts", s.listAccounts)
	r.Post("/providers/{p}/accounts", s.createAccount)
	r.Post("/providers/{p}/accounts/validate", s.validateAccount)
	r.Get("/providers/{p}/accounts/{a}", s.getAccount)
	r.Patch("/providers/{p}/accounts/{a}", s.patchAccount)
	r.Delete("/providers/{p}/accounts/{a}", s.deleteAccount)
	r.Post("/providers/{p}/accounts/{a}/rotate", s.rotateAccount)
	r.Post("/providers/{p}/accounts/{a}/test", s.testAccount)
	r.Get("/providers/{p}/accounts/{a}/timeline", s.accountTimeline)
	for _, act := range []string{"enable", "disable", "drain", "undrain", "reset"} {
		r.Post("/providers/{p}/accounts/{a}/"+act, s.accountState(act))
	}
	r.Get("/providers/{p}/balance/history", s.providerBalanceHistory)
	r.Get("/providers/{p}/accounts/{a}/balance/history", s.accountBalanceHistory)
	r.Get("/providers/{p}/sandbox", s.sandboxAccounts)
	r.Post("/providers/{p}/accounts/{a}/templates/sync", s.syncTemplates)
	r.Get("/infra/alerts", s.infraAlerts)
	r.Post("/infra/alerts/{id}/ack", s.ackInfraAlert)
}

func pa(r *http.Request) (string, string) { return chi.URLParam(r, "p"), chi.URLParam(r, "a") }

func reply(w http.ResponseWriter, r *http.Request, status int, v any, err error) {
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, status, v)
}

func (s *Server) adminProviders(w http.ResponseWriter, r *http.Request) {
	v, err := s.M.AdminProviders(r.Context(), pr(r))
	reply(w, r, 200, v, err)
}

func (s *Server) adminProvider(w http.ResponseWriter, r *http.Request) {
	p, _ := pa(r)
	v, err := s.M.AdminProvider(r.Context(), pr(r), p)
	reply(w, r, 200, v, err)
}

func (s *Server) getStrategy(w http.ResponseWriter, r *http.Request) {
	p, _ := pa(r)
	v, err := s.M.GetProviderStrategy(r.Context(), pr(r), p)
	reply(w, r, 200, v, err)
}

func (s *Server) putStrategy(w http.ResponseWriter, r *http.Request) {
	var in manager.StrategyInput
	if err := decode(r, &in); err != nil {
		writeErr(w, r, err)
		return
	}
	p, _ := pa(r)
	v, err := s.M.PutProviderStrategy(r.Context(), pr(r), p, in)
	reply(w, r, 200, v, err)
}

func (s *Server) listAccounts(w http.ResponseWriter, r *http.Request) {
	p, _ := pa(r)
	v, err := s.M.ListProviderAccounts(r.Context(), pr(r), p, r.URL.Query().Get("archived") == "1" || r.URL.Query().Get("archived") == "true")
	if err != nil {
		writeErr(w, r, err)
		return
	}
	list(w, v)
}

func (s *Server) createAccount(w http.ResponseWriter, r *http.Request) {
	var in manager.AccountInput
	if err := decode(r, &in); err != nil {
		writeErr(w, r, err)
		return
	}
	p, _ := pa(r)
	v, err := s.M.CreateProviderAccount(r.Context(), pr(r), p, in)
	reply(w, r, 201, v, err)
}

func (s *Server) validateAccount(w http.ResponseWriter, r *http.Request) {
	var in manager.ValidateAccountInput
	if err := decode(r, &in); err != nil {
		writeErr(w, r, err)
		return
	}
	p, _ := pa(r)
	v, err := s.M.ValidateProviderAccount(r.Context(), pr(r), p, in)
	reply(w, r, 200, v, err)
}

func (s *Server) getAccount(w http.ResponseWriter, r *http.Request) {
	p, a := pa(r)
	v, err := s.M.GetProviderAccount(r.Context(), pr(r), p, a)
	reply(w, r, 200, v, err)
}

func (s *Server) patchAccount(w http.ResponseWriter, r *http.Request) {
	var in manager.AccountInput
	if err := decode(r, &in); err != nil {
		writeErr(w, r, err)
		return
	}
	p, a := pa(r)
	v, err := s.M.PatchProviderAccount(r.Context(), pr(r), p, a, in)
	reply(w, r, 200, v, err)
}

func (s *Server) deleteAccount(w http.ResponseWriter, r *http.Request) {
	p, a := pa(r)
	q := r.URL.Query()
	yes := func(k string) bool { return q.Get(k) == "1" || q.Get(k) == "true" }
	if err := s.M.DeleteProviderAccount(r.Context(), pr(r), p, a, yes("force"), yes("purge"), q.Get("reason")); err != nil {
		writeErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) rotateAccount(w http.ResponseWriter, r *http.Request) {
	var in manager.RotateInput
	if err := decode(r, &in); err != nil {
		writeErr(w, r, err)
		return
	}
	p, a := pa(r)
	v, err := s.M.RotateProviderAccount(r.Context(), pr(r), p, a, in)
	reply(w, r, 200, v, err)
}

func (s *Server) testAccount(w http.ResponseWriter, r *http.Request) {
	p, a := pa(r)
	v, err := s.M.TestProviderAccount(r.Context(), pr(r), p, a)
	reply(w, r, 200, v, err)
}

func (s *Server) accountTimeline(w http.ResponseWriter, r *http.Request) {
	p, a := pa(r)
	v, err := s.M.AccountTimeline(r.Context(), pr(r), p, a, qint(r, "limit"))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	list(w, v)
}

func (s *Server) accountState(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, a := pa(r)
		v, err := s.M.SetAccountState(r.Context(), pr(r), p, a, action)
		reply(w, r, 200, v, err)
	}
}

// v2.3 (M29 phase B): balance history (snapshots written at each poll).
func (s *Server) accountBalanceHistory(w http.ResponseWriter, r *http.Request) {
	p, a := pa(r)
	v, err := s.M.AccountBalanceHistory(r.Context(), pr(r), p, a, qint(r, "days"))
	reply(w, r, 200, v, err)
}

func (s *Server) providerBalanceHistory(w http.ResponseWriter, r *http.Request) {
	p, _ := pa(r)
	v, err := s.M.ProviderBalanceHistoryView(r.Context(), pr(r), p, qint(r, "days"))
	reply(w, r, 200, v, err)
}

// M29-S: sandbox accounts (concurrency gauges, template availability).
func (s *Server) sandboxAccounts(w http.ResponseWriter, r *http.Request) {
	p, _ := pa(r)
	v, err := s.M.SandboxAccounts(r.Context(), pr(r), p)
	reply(w, r, 200, v, err)
}

func (s *Server) syncTemplates(w http.ResponseWriter, r *http.Request) {
	p, a := pa(r)
	v, err := s.M.SyncAccountTemplates(r.Context(), pr(r), p, a)
	reply(w, r, 200, v, err)
}

func (s *Server) infraAlerts(w http.ResponseWriter, r *http.Request) {
	v, err := s.M.ListInfraAlerts(r.Context(), pr(r), r.URL.Query().Get("status"))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	list(w, v)
}

func (s *Server) ackInfraAlert(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if u, err := url.PathUnescape(id); err == nil {
		id = u
	}
	v, err := s.M.AckInfraAlert(r.Context(), pr(r), id)
	reply(w, r, 200, v, err)
}

// ---- OpenAPI ----

var (
	provP  = param{"p", "path", "string", "Provider id (hetzner, vultr, e2b, …)", true}
	acctP  = param{"a", "path", "string", "Provider account id (pa_…)", true}
	alertP = param{"id", "path", "string", "Alert id (<type>|<subject>, URL-encoded)", true}
	daysP  = param{"days", "query", "integer", "Window in days (default 7, max 90)", false}
)

const acctDesc = "Platform admin only. Tokens are write-only: responses carry key_fingerprint (sha256 prefix + last 4) and never the key. Money fields are in the account currency; stats.*_usd are USD conversions."

func init() {
	ops = append(ops,
		op{Method: "GET", Path: "/v1/admin/providers", Summary: "Providers with account totals, balance, spend, runway and alerts (KPI summary + rows)", Tag: "admin-infra", Resp: manager.AdminProvidersView{}, Desc: acctDesc},
		op{Method: "GET", Path: "/v1/admin/providers/{p}", Summary: "One provider: prefs, strategy, accounts with stats, open alerts", Tag: "admin-infra", Resp: manager.AdminProviderDetail{}, Params: []param{provP}},
		op{Method: "GET", Path: "/v1/admin/providers/{p}/strategy", Summary: "Account selection strategy of a provider", Tag: "admin-infra", Resp: manager.StrategyView{}, Params: []param{provP}},
		op{Method: "PUT", Path: "/v1/admin/providers/{p}/strategy", Summary: "Set strategy (priority | weighted | round_robin | least_used | drain_first), sticky minutes, failover across accounts", Tag: "admin-infra",
			Req: manager.StrategyInput{}, Resp: manager.StrategyView{}, Params: []param{provP},
			Desc: "Empty strategy = default: priority for VPS providers, drain_first for sandbox providers."},
		op{Method: "GET", Path: "/v1/admin/providers/{p}/accounts", Summary: "Accounts of a provider with stats (no credentials)", Tag: "admin-infra", Resp: manager.AccountView{}, List: true,
			Params: []param{provP, qp("archived", "boolean", "include archived accounts")}, Desc: acctDesc},
		op{Method: "POST", Path: "/v1/admin/providers/{p}/accounts", Summary: "Add an account (validates the key unless skip_validate)", Tag: "admin-infra", Req: manager.AccountInput{}, Resp: manager.AccountView{}, Status: 201,
			Params: []param{provP}, Desc: "label and token are required. warnings lists accounts with the same external_id or key (not a real backup)."},
		op{Method: "POST", Path: "/v1/admin/providers/{p}/accounts/validate", Summary: "Check a key without saving it; reports duplicates", Tag: "admin-infra", Req: manager.ValidateAccountInput{}, Resp: manager.AccountTestResult{}, Params: []param{provP}},
		op{Method: "GET", Path: "/v1/admin/providers/{p}/accounts/{a}", Summary: "One account with stats", Tag: "admin-infra", Resp: manager.AccountView{}, Params: []param{provP, acctP}},
		op{Method: "PATCH", Path: "/v1/admin/providers/{p}/accounts/{a}", Summary: "Merge-patch an account: scheduling, manual balance / credit / limits / dates, caps, thresholds", Tag: "admin-infra",
			Req: manager.AccountInput{}, Resp: manager.AccountView{}, Params: []param{provP, acctP},
			Desc: "balance sets balance_source=manual and balance_at=now; dates accept RFC 3339 or YYYY-MM-DD, \"\" clears; limits {name: value}, 0 removes. The key changes only through /rotate."},
		op{Method: "DELETE", Path: "/v1/admin/providers/{p}/accounts/{a}", Summary: "Archive an account (refused while hosts live on it unless force + reason; purge deletes an unreferenced one)", Tag: "admin-infra", Status: 204,
			Params: []param{provP, acctP, qp("force", "boolean", ""), qp("reason", "string", "required with force"), qp("purge", "boolean", "hard delete when no host references it")}},
		op{Method: "POST", Path: "/v1/admin/providers/{p}/accounts/{a}/rotate", Summary: "Rotate the key (same provider account: external_id must match); hosts keep the account", Tag: "admin-infra",
			Req: manager.RotateInput{}, Resp: manager.AccountView{}, Params: []param{provP, acctP}},
		op{Method: "POST", Path: "/v1/admin/providers/{p}/accounts/{a}/test", Summary: "Probe the stored key now (updates status and the account breaker)", Tag: "admin-infra", Resp: manager.AccountTestResult{}, Params: []param{provP, acctP}},
		op{Method: "GET", Path: "/v1/admin/providers/{p}/accounts/{a}/timeline", Summary: "Audit events of the account (status changes, failovers, rotations, alerts)", Tag: "admin-infra", Resp: model.Event{}, List: true,
			Params: []param{provP, acctP, qp("limit", "integer", "default 100")}},
		op{Method: "POST", Path: "/v1/admin/providers/{p}/accounts/{a}/enable", Summary: "Enable the account", Tag: "admin-infra", Resp: manager.AccountView{}, Params: []param{provP, acctP}},
		op{Method: "POST", Path: "/v1/admin/providers/{p}/accounts/{a}/disable", Summary: "Disable the account (no new hosts; existing hosts still use its key)", Tag: "admin-infra", Resp: manager.AccountView{}, Params: []param{provP, acctP}},
		op{Method: "POST", Path: "/v1/admin/providers/{p}/accounts/{a}/drain", Summary: "Drain: keep hosts, take no new ones", Tag: "admin-infra", Resp: manager.AccountView{}, Params: []param{provP, acctP}},
		op{Method: "POST", Path: "/v1/admin/providers/{p}/accounts/{a}/undrain", Summary: "Lift a drain", Tag: "admin-infra", Resp: manager.AccountView{}, Params: []param{provP, acctP}},
		op{Method: "POST", Path: "/v1/admin/providers/{p}/accounts/{a}/reset", Summary: "Reset breaker, cooldown, error status and learned limits", Tag: "admin-infra", Resp: manager.AccountView{}, Params: []param{provP, acctP}},
		op{Method: "GET", Path: "/v1/admin/providers/{p}/accounts/{a}/balance/history", Summary: "Balance / spend history of an account (one snapshot per poll) with runway trend", Tag: "admin-infra",
			Resp: manager.BalanceHistory{}, Params: []param{provP, acctP, daysP},
			Desc: "v2.3 (M29 phase B). Providers with a balance API (Runpod) are polled every BOXMES_ACCOUNT_POLL_BALANCE (15m) with the account's billing key if set, else its key; other accounts get one snapshot per interval from their manual / estimated balance and ledger spend. stale = the last read failed (the last good balance is kept; status, breaker and failover are not touched). points are oldest first."},
		op{Method: "GET", Path: "/v1/admin/providers/{p}/balance/history", Summary: "Provider-level balance history: accounts' snapshots summed per bucket (USD) with burn, runway and trend", Tag: "admin-infra",
			Resp: manager.ProviderBalanceHistory{}, Params: []param{provP, daysP},
			Desc: "Buckets of 1 h (≤ 7 days), 3 h (≤ 30) or 6 h; each account counts with its last point carried forward. Archived accounts are left out."},
		op{Method: "GET", Path: "/v1/admin/providers/{p}/sandbox", Summary: "Sandbox API tab: accounts with concurrency gauges (running / in flight / limit, create rate) and the template availability matrix", Tag: "admin-infra",
			Resp: manager.SandboxAccountsView{}, Params: []param{provP},
			Desc: "Sandbox providers only (M29-S). running counts live sandboxes that hold a concurrency slot (paused ones do not); limits are the tightest over the account's limit_group. templates[t] is available | missing (learned from a \"template not found\" answer) | not_listed (the account lists its templates and t is not one) | unknown (no list: Boxmes tries and learns)."},
		op{Method: "POST", Path: "/v1/admin/providers/{p}/accounts/{a}/templates/sync", Summary: "Read the account's templates / snapshots from the provider with its own key and store them", Tag: "admin-infra",
			Resp: manager.TemplateSyncResult{}, Params: []param{provP, acctP},
			Desc: "E2B / Novita: GET /templates; Daytona: GET /snapshots (ready ones). Clears learned \"missing\" entries the provider now lists. Providers without a template API answer 400: set templates with PATCH."},
		op{Method: "GET", Path: "/v1/admin/infra/alerts", Summary: "Infra alerts (low balance, expiring token/credit/contract, limits, spend caps, failover, orphan hosts)", Tag: "admin-infra", Resp: manager.InfraAlert{}, List: true,
			Params: []param{qp("status", "string", "open+acked (default) | open | acked | closed | all")}},
		op{Method: "POST", Path: "/v1/admin/infra/alerts/{id}/ack", Summary: "Acknowledge an alert (no repeats until it escalates or clears)", Tag: "admin-infra", Resp: manager.InfraAlert{}, Params: []param{alertP}},
	)
}
