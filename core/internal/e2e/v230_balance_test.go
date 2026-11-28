package e2e

// v2.3 (M29 phase B): real balance reading (Runpod GraphQL, mocked) +
// balance history. A reading sets balance_source=api and feeds runway; the
// runway thresholds raise the existing alerts and drain under 2 days; a read
// failure only marks the balance stale (status, eligibility and failover
// untouched); every poll writes a snapshot served by the history routes.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"boxmes/core/internal/manager"
	"boxmes/core/internal/provider"
	"boxmes/core/internal/store"
)

type fakeRunpodAPI struct {
	mu   sync.Mutex
	down bool
	seen map[string]int // bearer key → calls
	ans  map[string]string
}

func (f *fakeRunpodAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	f.seen[key]++
	if f.down {
		w.WriteHeader(503)
		w.Write([]byte("service unavailable"))
		return
	}
	if a, ok := f.ans[key]; ok {
		w.Write([]byte(a))
		return
	}
	w.WriteHeader(401)
	w.Write([]byte(`{"errors":[{"message":"Unauthorized"}]}`))
}

func (f *fakeRunpodAPI) set(down bool) { f.mu.Lock(); f.down = down; f.mu.Unlock() }
func (f *fakeRunpodAPI) calls(k string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.seen[k]
}

func TestV230RunpodBalance(t *testing.T) {
	t.Setenv("BOXMES_ACCOUNT_POLL_BALANCE", "15m")
	fake := &fakeRunpodAPI{seen: map[string]int{}, ans: map[string]string{
		// $10 left burning $1/h → ~0.4 days: critical, auto drain
		"rp-low": `{"data":{"myself":{"id":"user_low","clientBalance":10,"currentSpendPerHr":1,"spendLimit":80}}}`,
		// main key of the rich account (validation); balance read with its billing key
		"rp-main":    `{"data":{"myself":{"id":"user_rich","clientBalance":1000,"currentSpendPerHr":1,"spendLimit":80}}}`,
		"rp-billing": `{"data":{"myself":{"id":"user_rich","clientBalance":960,"currentSpendPerHr":1,"spendLimit":80}}}`,
	}}
	api := httptest.NewServer(fake)
	defer api.Close()
	rp := provider.NewRunpod(api.Client())
	rp.URL = api.URL
	reg := provider.NewRegistry(&provider.Local{}, rp)
	e := setupReg(t, reg, nil, 0)
	u, adm, ctx := e.srv.URL, "bx_admin_e2e", e.ctx
	do := func(method, path string, body any, want int) map[string]any {
		t.Helper()
		st, v := call(t, u, method, path, adm, body)
		if st != want {
			t.Fatalf("%s %s: %d (want %d) %v", method, path, st, want, v)
		}
		return v
	}

	// the provider shows up (balance-only, not implemented)
	pv := do("GET", "/v1/admin/providers/runpod", nil, 200)
	if pv["implemented"] != false {
		t.Fatalf("runpod %v", pv)
	}
	low := do("POST", "/v1/admin/providers/runpod/accounts", map[string]any{"label": "RP low", "token": "rp-low"}, 201)
	rich := do("POST", "/v1/admin/providers/runpod/accounts", map[string]any{"label": "RP rich", "token": "rp-main", "billing_token": "rp-billing", "priority": 2}, 201)
	idLow, idRich := low["id"].(string), rich["id"].(string)
	if s := rich["stats"].(map[string]any); s["balance_api"] != true {
		t.Fatalf("balance_api %v", s)
	}

	e.m.AccountsTickNow(ctx)
	gl := do("GET", "/v1/admin/providers/runpod/accounts/"+idLow, nil, 200)
	st := gl["stats"].(map[string]any)
	if gl["balance_source"] != "api" || gl["balance"].(float64) != 10 || gl["spend_per_hour"].(float64) != 1 || gl["balance_stale"] != false ||
		gl["currency"] != "USD" || gl["billing_model"] != "prepaid" {
		t.Fatalf("low after poll %v", gl)
	}
	if lim := gl["limits"].(map[string]any)["spend_per_hour"].(map[string]any); lim["limit"].(float64) != 80 || lim["source"] != "api" {
		t.Fatalf("spend limit %v", lim)
	}
	if rd := st["runway_days"].(float64); rd > 0.45 || rd < 0.35 || st["burn_per_hour_usd"].(float64) != 1 {
		t.Fatalf("runway %v", st)
	}
	// < 2 days → drain (never delete), critical low_balance alert
	if gl["drain"] != true || gl["auto_drained"] != true || gl["status"] != "draining" {
		t.Fatalf("auto drain %v", gl)
	}
	var lowAlert map[string]any
	for _, al := range items(t, do("GET", "/v1/admin/infra/alerts", nil, 200)) {
		if al["type"] == "low_balance" && al["account_id"] == idLow {
			lowAlert = al
		}
	}
	if lowAlert == nil || lowAlert["level"] != "critical" {
		t.Fatalf("low_balance alert missing")
	}
	// the rich account read its balance with the billing key, not the main key
	gr := do("GET", "/v1/admin/providers/runpod/accounts/"+idRich, nil, 200)
	if gr["balance"].(float64) != 960 || fake.calls("rp-billing") != 1 || gr["stats"].(map[string]any)["eligible"] != true || gr["status"] != "active" {
		t.Fatalf("rich %v billing calls %d", gr, fake.calls("rp-billing"))
	}
	// within the poll interval nothing is read again
	before := fake.calls("rp-billing")
	e.m.AccountsTickNow(ctx)
	if fake.calls("rp-billing") != before {
		t.Fatal("polled again inside the interval")
	}

	// ---- fail-soft: API down → stale only ----
	fake.set(true)
	tr := do("POST", "/v1/admin/providers/runpod/accounts/"+idRich+"/test", nil, 200)
	if tr["balance_read"] != true || tr["balance_error"] == nil || tr["ok"] != false {
		t.Fatalf("test while down %v", tr)
	}
	gr = do("GET", "/v1/admin/providers/runpod/accounts/"+idRich, nil, 200)
	if gr["balance_stale"] != true || gr["balance"].(float64) != 960 || !strings.Contains(gr["balance_error"].(string), "503") {
		t.Fatalf("stale %v", gr)
	}
	// the probe failure is the normal account outcome; the balance read
	// itself did not change anything else: reset and check again
	do("POST", "/v1/admin/providers/runpod/accounts/"+idRich+"/reset", nil, 200)
	if err := e.m.PollAccountBalanceNow(ctx, "runpod", idRich); err == nil {
		t.Fatal("poll while down succeeded")
	}
	gr = do("GET", "/v1/admin/providers/runpod/accounts/"+idRich, nil, 200)
	if gr["status"] != "active" || gr["stats"].(map[string]any)["eligible"] != true || gr["error_streak"].(float64) != 0 {
		t.Fatalf("balance read failure touched the account: %v", gr)
	}
	evs, _ := e.m.Store.ListEvents(ctx, store.EventFilter{Type: "provider_account.balance_read_failed"})
	if len(evs) != 1 {
		t.Fatalf("balance_read_failed events %d", len(evs))
	}

	// ---- history ----
	h := do("GET", "/v1/admin/providers/runpod/accounts/"+idRich+"/balance/history?days=3", nil, 200)
	pts := h["points"].([]any)
	if h["days"].(float64) != 3 || h["balance_api"] != true || h["stale"] != true || h["interval_s"].(float64) != 900 || len(pts) < 2 {
		t.Fatalf("history %v", h)
	}
	first, last := pts[0].(map[string]any), pts[len(pts)-1].(map[string]any)
	if first["source"] != "api" || first["balance"].(float64) != 960 || first["stale"] != false || last["stale"] != true || last["error"] == nil {
		t.Fatalf("points %v … %v", first, last)
	}
	if st, _ := call(t, u, "GET", "/v1/admin/providers/runpod/accounts/pa_nope/balance/history", adm, nil); st != 404 {
		t.Fatalf("unknown account %d", st)
	}
	ph := do("GET", "/v1/admin/providers/runpod/balance/history?days=1", nil, 200)
	ppts := ph["points"].([]any)
	if len(ph["accounts"].([]any)) != 2 || len(ppts) == 0 || ph["bucket_s"].(float64) != 3600 {
		t.Fatalf("provider history %v", ph)
	}
	lp := ppts[len(ppts)-1].(map[string]any)
	if lp["accounts"].(float64) != 2 || lp["balance_usd"].(float64) < 960 || lp["burn_per_hour_usd"].(float64) != 2 || lp["stale_accounts"].(float64) != 1 {
		t.Fatalf("provider bucket %v", lp)
	}
	// recovery clears stale
	fake.set(false)
	if err := e.m.PollAccountBalanceNow(ctx, "runpod", idRich); err != nil {
		t.Fatal(err)
	}
	if gr = do("GET", "/v1/admin/providers/runpod/accounts/"+idRich, nil, 200); gr["balance_stale"] != false || gr["balance_error"] != nil {
		t.Fatalf("recovered %v", gr)
	}
	// no credential in any response or event
	all, _ := e.m.Store.ListEvents(ctx, store.EventFilter{})
	evJSON, _ := json.Marshal(all)
	_, hist := call(t, u, "GET", "/v1/admin/providers/runpod/accounts/"+idRich+"/balance/history", adm, nil)
	_, acct := call(t, u, "GET", "/v1/admin/providers/runpod/accounts/"+idRich, adm, nil)
	blob, _ := json.Marshal([]any{hist, acct})
	for _, sec := range []string{"rp-low", "rp-main", "rp-billing"} {
		if strings.Contains(string(evJSON), sec) || strings.Contains(string(blob), sec) {
			t.Fatalf("credential %s leaked", sec)
		}
	}
	var _ = manager.BalanceHistory{}
}
