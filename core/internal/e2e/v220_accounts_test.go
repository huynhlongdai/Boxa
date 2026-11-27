package e2e

// v2.2 (M29 phase A): provider accounts — several credentials per provider,
// failover by error class, hosts keep their account, drain on low runway,
// spend caps, alerts with dedupe, legacy-token migration, labeled env keys,
// no credential ever in a response or an event.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"boxmes/core/internal/manager"
	"boxmes/core/internal/model"
	"boxmes/core/internal/provider"
	"boxmes/core/internal/secret"
	"boxmes/core/internal/store"
)

// acctCloud is a T1 provider whose behaviour depends on the token: every
// account is a different credential of the same fake cloud.
type acctCloud struct {
	id      string
	mu      sync.Mutex
	valid   map[string]bool
	fail    map[string]string // token → error text returned by Create
	creates map[string]int    // token → successful creates
	tries   map[string]int    // token → create calls
	destroy []string          // "<token>:<ref>"
	n       int
}

func newAcctCloud(id string, tokens ...string) *acctCloud {
	c := &acctCloud{id: id, valid: map[string]bool{}, fail: map[string]string{}, creates: map[string]int{}, tries: map[string]int{}}
	for _, t := range tokens {
		c.valid[t] = true
	}
	return c
}

func (c *acctCloud) ID() string          { return c.id }
func (c *acctCloud) Name() string        { return "Acme " + c.id }
func (c *acctCloud) Tier() string        { return "T1" }
func (c *acctCloud) Implemented() bool   { return true }
func (c *acctCloud) Caps() provider.Caps { return provider.Caps{} }
func (c *acctCloud) setFail(tok, msg string) {
	c.mu.Lock()
	c.fail[tok] = msg
	c.mu.Unlock()
}
func (c *acctCloud) count(m map[string]int, tok string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return m[tok]
}
func (c *acctCloud) Validate(ctx context.Context, token string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.valid[token] {
		return model.EH(model.ErrUnauthorized, "Create a read+write API token in the console and paste it again.", "%s rejected the token (%d)", c.id, 401)
	}
	return nil
}
func (c *acctCloud) Offers(ctx context.Context, token string) ([]model.Offer, error) {
	return []model.Offer{{Provider: c.id, SKU: "s1", Region: "r1", CPU: 2, RAMMB: 4096, DiskGB: 40, PriceHour: 0.05, PriceMonth: 36.5,
		MinBillingMinutes: 60, Source: c.id + "-api", FetchedAt: time.Now().UTC(), Confidence: "verified"}}, nil
}
func (c *acctCloud) Create(ctx context.Context, token string, req provider.CreateRequest) (provider.Handle, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tries[token]++
	if msg := c.fail[token]; msg != "" {
		return provider.Handle{}, fmt.Errorf("%s", msg)
	}
	c.creates[token]++
	c.n++
	return provider.Handle{Ref: fmt.Sprintf("srv-%d", c.n), IP: "127.0.0.9"}, nil
}
func (c *acctCloud) Destroy(ctx context.Context, token, ref string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.valid[token] {
		return fmt.Errorf("HTTP 401: unauthorized")
	}
	c.destroy = append(c.destroy, token+":"+ref)
	return nil
}
func (c *acctCloud) Status(ctx context.Context, token, ref string) (string, error) {
	return "ready", nil
}
func (c *acctCloud) destroyed() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string{}, c.destroy...)
}

func TestV220ProviderAccounts(t *testing.T) {
	var tgMu sync.Mutex
	var tgMsgs []string
	tg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		tgMu.Lock()
		tgMsgs = append(tgMsgs, fmt.Sprint(b["text"]))
		tgMu.Unlock()
		w.Write([]byte(`{"ok":true}`))
	}))
	defer tg.Close()
	tgCount := func(sub string) int {
		tgMu.Lock()
		defer tgMu.Unlock()
		n := 0
		for _, m := range tgMsgs {
			if strings.Contains(m, sub) {
				n++
			}
		}
		return n
	}

	cloud := newAcctCloud("acme", "tokA", "tokA2", "tokB", "tokC", "tokE")
	reg := provider.NewRegistry(&provider.Local{}, cloud)
	e := setupReg(t, reg, func(c *manager.Config) {
		c.HealthInterval = time.Hour
		c.TelegramAPI = tg.URL
	}, 0)
	u, adm, ctx := e.srv.URL, "bx_admin_e2e", e.ctx
	do := func(method, path string, body any, want int) map[string]any {
		t.Helper()
		st, v := call(t, u, method, path, adm, body)
		if st != want {
			t.Fatalf("%s %s: %d (want %d) %v", method, path, st, want, v)
		}
		return v
	}
	// Telegram for critical infra alerts
	do("PUT", "/v1/notify", map[string]any{"telegram_chat_id": "42", "telegram_bot_token": "bot:tok",
		"events": []string{"infra_alert", "infra_digest"}, "daily_report_hour": 8}, 200)

	// ---- legacy route = default account ----
	do("PUT", "/v1/providers/acme/token", map[string]any{"token": "tokA"}, 200)
	accts := items(t, do("GET", "/v1/admin/providers/acme/accounts", nil, 200))
	if len(accts) != 1 || accts[0]["id"] != model.DefaultAccountID("acme") || accts[0]["is_default"] != true || accts[0]["credential_ref"] != "provider:acme" ||
		accts[0]["key_fingerprint"] == "" || accts[0]["status"] != "active" {
		t.Fatalf("default account %v", accts)
	}
	idA := accts[0]["id"].(string)
	// a second account; validation is enforced, duplicates are warned about
	if st, v := call(t, u, "POST", "/v1/admin/providers/acme/accounts", adm, map[string]any{"label": "bad", "token": "nope"}); st != 400 {
		t.Fatalf("bad key accepted %d %v", st, v)
	}
	vr := do("POST", "/v1/admin/providers/acme/accounts/validate", map[string]any{"token": "tokA"}, 200)
	if vr["ok"] != true || len(vr["duplicates"].([]any)) != 1 {
		t.Fatalf("validate %v", vr)
	}
	b := do("POST", "/v1/admin/providers/acme/accounts", map[string]any{"label": "Acme backup", "token": "tokB", "external_id": "proj-b", "priority": 2,
		"currency": "EUR", "console_url": "https://console.example/b"}, 201)
	idB := b["id"].(string)
	if b["priority"].(float64) != 2 || b["credential_source"] != "api" || b["stats"] == nil || b["currency"] != "EUR" {
		t.Fatalf("account B %v", b)
	}

	// ---- failover: 402 on A → B; A out_of_credit, critical alert once ----
	cloud.setFail("tokA", "HTTP 402: payment required (insufficient balance)")
	h1 := do("POST", "/v1/hosts", map[string]any{"provider": "acme", "sku": "s1", "region": "r1"}, 201)
	if h1["account_id"] != idB || cloud.count(cloud.creates, "tokB") != 1 || cloud.count(cloud.tries, "tokA") != 1 {
		t.Fatalf("failover to B: host %v creates %v tries %v", h1, cloud.creates, cloud.tries)
	}
	a := do("GET", "/v1/admin/providers/acme/accounts/"+idA, nil, 200)
	if a["status"] != "out_of_credit" || a["last_error_class"] != "payment" || a["stats"].(map[string]any)["eligible"] != false {
		t.Fatalf("A after 402 %v", a)
	}
	evs, _ := e.m.Store.ListEvents(ctx, store.EventFilter{Type: "provider_account.failover"})
	if len(evs) != 1 || evs[0].Data["from"] != idA || evs[0].Data["to"] != idB || evs[0].Data["class"] != "payment" {
		t.Fatalf("failover event %+v", evs)
	}
	// account-level errors do not trip the provider breaker when there is another account
	ph := do("GET", "/v1/providers/acme/health", nil, 200)
	if ph["stats"].(map[string]any)["breaker"].(map[string]any)["state"] != "closed" {
		t.Fatalf("provider breaker tripped by an account error: %v", ph["stats"])
	}
	waitFor(t, func() bool { return tgCount("402") >= 1 }, "telegram critical alert")
	// dedupe: same condition again (A is skipped, tick re-evaluates) → no new message
	h2 := do("POST", "/v1/hosts", map[string]any{"provider": "acme", "sku": "s1", "region": "r1"}, 201)
	if h2["account_id"] != idB {
		t.Fatalf("second host %v", h2)
	}
	e.m.AccountsTickNow(ctx)
	e.m.AccountsTickNow(ctx)
	time.Sleep(200 * time.Millisecond)
	if n := tgCount("402"); n != 1 {
		t.Fatalf("critical alert sent %d times (want 1): %v", n, tgMsgs)
	}
	al := items(t, do("GET", "/v1/admin/infra/alerts", nil, 200))
	var credit map[string]any
	for _, x := range al {
		if x["type"] == "out_of_credit" {
			credit = x
		}
	}
	if credit == nil || credit["level"] != "critical" || credit["account_id"] != idA || credit["status"] != "open" {
		t.Fatalf("alerts %v", al)
	}
	ack := do("POST", "/v1/admin/infra/alerts/"+url.PathEscape(credit["id"].(string))+"/ack", nil, 200)
	if ack["status"] != "acked" {
		t.Fatalf("ack %v", ack)
	}
	// manual top-up clears out_of_credit; the alert closes on the next tick
	do("PATCH", "/v1/admin/providers/acme/accounts/"+idA, map[string]any{"balance": 500}, 200)
	cloud.setFail("tokA", "")
	e.m.AccountsTickNow(ctx)
	if al := items(t, do("GET", "/v1/admin/infra/alerts?status=closed", nil, 200)); len(al) == 0 {
		t.Fatal("out_of_credit alert did not close")
	}

	// ---- 401 → auth_failed, 429 → cooldown, capacity → no account failover ----
	cloud.setFail("tokA", "HTTP 401: unauthorized")
	h3 := do("POST", "/v1/hosts", map[string]any{"provider": "acme", "sku": "s1", "region": "r1"}, 201)
	if h3["account_id"] != idB || do("GET", "/v1/admin/providers/acme/accounts/"+idA, nil, 200)["status"] != "auth_failed" {
		t.Fatalf("auth failover %v", h3)
	}
	do("POST", "/v1/admin/providers/acme/accounts/"+idA+"/reset", nil, 200)
	cloud.setFail("tokA", "HTTP 429: too many requests (retry-after: 120)")
	h4 := do("POST", "/v1/hosts", map[string]any{"provider": "acme", "sku": "s1", "region": "r1"}, 201)
	a = do("GET", "/v1/admin/providers/acme/accounts/"+idA, nil, 200)
	cool, _ := time.Parse(time.RFC3339, fmt.Sprint(a["cooldown_until"]))
	if h4["account_id"] != idB || a["status"] != "rate_limited" || time.Until(cool) < 100*time.Second || time.Until(cool) > 130*time.Second {
		t.Fatalf("rate limit: host %v account %v", h4["account_id"], a)
	}
	do("POST", "/v1/admin/providers/acme/accounts/"+idA+"/reset", nil, 200)
	cloud.setFail("tokA", "HTTP 503: sold out in region r1")
	triesB := cloud.count(cloud.tries, "tokB")
	if st, v := call(t, u, "POST", "/v1/hosts", adm, map[string]any{"provider": "acme", "sku": "s1", "region": "r1"}); st < 400 {
		t.Fatalf("capacity error must not fail over to another account: %d %v", st, v)
	}
	if cloud.count(cloud.tries, "tokB") != triesB {
		t.Fatal("capacity error tried the next account of the same provider")
	}
	cloud.setFail("tokA", "")

	// ---- the host keeps its account after a token change ----
	// default account rotated through the legacy route, B disabled: B's
	// hosts are still deleted with B's key; the default's with the new key.
	hA := do("POST", "/v1/hosts", map[string]any{"provider": "acme", "sku": "s1", "region": "r9"}, 201) // A (priority 1) again
	if hA["account_id"] != idA {
		t.Fatalf("host on A %v", hA)
	}
	do("PUT", "/v1/providers/acme/token", map[string]any{"token": "tokA2"}, 200)
	do("POST", "/v1/admin/providers/acme/accounts/"+idB+"/disable", nil, 200)
	if st, v := call(t, u, "POST", "/v1/admin/providers/acme/accounts/"+idB+"/rotate", adm, map[string]any{"token": "tokC", "external_id": "other"}); st != 400 {
		t.Fatalf("rotate to another provider account accepted %d %v", st, v)
	}
	do("DELETE", "/v1/hosts/"+h1["id"].(string), nil, 200)
	do("DELETE", "/v1/hosts/"+hA["id"].(string), nil, 200)
	ds := strings.Join(cloud.destroyed(), ",")
	if !strings.Contains(ds, "tokB:"+h1["provider_ref"].(string)) || !strings.Contains(ds, "tokA2:"+hA["provider_ref"].(string)) {
		t.Fatalf("destroy used the wrong key: %s", ds)
	}
	// rotation of B within the same provider account keeps its hosts
	rb := do("POST", "/v1/admin/providers/acme/accounts/"+idB+"/rotate", map[string]any{"token": "tokC", "external_id": "proj-b"}, 200)
	if rb["key_fingerprint"] == b["key_fingerprint"] || rb["rotated_at"] == nil {
		t.Fatalf("rotate %v", rb)
	}
	do("DELETE", "/v1/hosts/"+h2["id"].(string), nil, 200)
	if !strings.Contains(strings.Join(cloud.destroyed(), ","), "tokC:"+h2["provider_ref"].(string)) {
		t.Fatalf("rotated key not used: %v", cloud.destroyed())
	}
	// an archived account still deletes its hosts; with live hosts archive needs force + reason
	if st, _ := call(t, u, "DELETE", "/v1/admin/providers/acme/accounts/"+idB, adm, nil); st != 400 {
		t.Fatal("archive with live hosts must be refused")
	}
	do("DELETE", "/v1/admin/providers/acme/accounts/"+idB+"?force=1&reason=test", nil, 204)
	do("DELETE", "/v1/hosts/"+h3["id"].(string), nil, 200)
	if !strings.Contains(strings.Join(cloud.destroyed(), ","), "tokC:"+h3["provider_ref"].(string)) {
		t.Fatalf("archived account key not used: %v", cloud.destroyed())
	}
	do("DELETE", "/v1/hosts/"+h4["id"].(string), nil, 200)

	// ---- drain on low runway (never deletes hosts), lifted after a top-up ----
	c := do("POST", "/v1/admin/providers/acme/accounts", map[string]any{"label": "Acme promo", "token": "tokC", "priority": 1}, 201)
	idC := c["id"].(string)
	do("PATCH", "/v1/admin/providers/acme/accounts/"+idA, map[string]any{"priority": 5}, 200)
	hc := do("POST", "/v1/hosts", map[string]any{"provider": "acme", "sku": "s1", "region": "r1"}, 201)
	if hc["account_id"] != idC {
		t.Fatalf("host on C %v", hc)
	}
	do("PATCH", "/v1/admin/providers/acme/accounts/"+idC, map[string]any{"balance": 1.0}, 200) // $0.05/h → 20 h runway
	e.m.AccountsTickNow(ctx)
	c = do("GET", "/v1/admin/providers/acme/accounts/"+idC, nil, 200)
	stc := c["stats"].(map[string]any)
	if c["drain"] != true || c["auto_drained"] != true || c["status"] != "draining" || stc["runway_days"].(float64) >= 2 || stc["hosts_live"].(float64) != 1 {
		t.Fatalf("low runway must drain C: %v", c)
	}
	if h, _ := e.m.Store.GetHost(ctx, hc["id"].(string)); h.Status == "deleted" {
		t.Fatal("drain deleted a running host")
	}
	hn := do("POST", "/v1/hosts", map[string]any{"provider": "acme", "sku": "s1", "region": "r1"}, 201)
	if hn["account_id"] != idA {
		t.Fatalf("drained account still picked: %v", hn)
	}
	lowAlert := false
	for _, x := range items(t, do("GET", "/v1/admin/infra/alerts", nil, 200)) {
		if x["type"] == "low_balance" && x["account_id"] == idC && x["level"] == "critical" {
			lowAlert = true
		}
	}
	if !lowAlert {
		t.Fatal("no critical low_balance alert")
	}
	do("PATCH", "/v1/admin/providers/acme/accounts/"+idC, map[string]any{"balance": 1000}, 200)
	e.m.AccountsTickNow(ctx)
	if c = do("GET", "/v1/admin/providers/acme/accounts/"+idC, nil, 200); c["drain"] != false || c["status"] != "active" {
		t.Fatalf("drain not lifted after top-up: %v", c)
	}

	// ---- spend caps (ledger) ----
	do("PATCH", "/v1/admin/providers/acme/accounts/"+idC, map[string]any{"spend_cap_day": 2.0}, 200)
	e.m.Store.AddLedger(ctx, &model.LedgerEntry{ID: secret.ID("led"), TS: time.Now().UTC(), Provider: "acme", Kind: "compute", AmountUSD: 1.7, AccountID: idC, Note: "test"})
	e.m.AccountsTickNow(ctx)
	capWarn := ""
	for _, x := range items(t, do("GET", "/v1/admin/infra/alerts", nil, 200)) {
		if x["type"] == "spend_cap_near" && x["account_id"] == idC {
			capWarn = fmt.Sprint(x["level"])
		}
	}
	if capWarn != "warn" { // 85 % of the daily cap
		t.Fatalf("spend_cap_near warn missing (%q)", capWarn)
	}
	e.m.Store.AddLedger(ctx, &model.LedgerEntry{ID: secret.ID("led"), TS: time.Now().UTC(), Provider: "acme", Kind: "compute", AmountUSD: 0.4, AccountID: idC, Note: "test"})
	c = do("GET", "/v1/admin/providers/acme/accounts/"+idC, nil, 200)
	if st := c["stats"].(map[string]any); st["eligible"] != false || !strings.Contains(fmt.Sprint(st["ineligible"]), "daily spend cap") {
		t.Fatalf("cap not enforced: %v", st)
	}
	do("POST", "/v1/admin/providers/acme/accounts/"+idA+"/disable", nil, 200)
	st, v := call(t, u, "POST", "/v1/hosts", adm, map[string]any{"provider": "acme", "sku": "s1", "region": "r1"})
	if st < 400 || !strings.Contains(fmt.Sprint(v), "no eligible account") {
		t.Fatalf("create over the cap: %d %v", st, v)
	}
	do("POST", "/v1/admin/providers/acme/accounts/"+idA+"/enable", nil, 200)

	// ---- strategy ----
	sv := do("GET", "/v1/admin/providers/acme/strategy", nil, 200)
	if sv["effective_strategy"] != "priority" || sv["failover_across_accounts"] != true {
		t.Fatalf("strategy %v", sv)
	}
	if st, _ := call(t, u, "PUT", "/v1/admin/providers/acme/strategy", adm, map[string]any{"strategy": "fastest"}); st != 400 {
		t.Fatal("unknown strategy accepted")
	}
	do("PUT", "/v1/admin/providers/acme/strategy", map[string]any{"strategy": "round_robin", "sticky_minutes": 0}, 200)

	// ---- labeled env accounts ----
	provider.EnvTokens["acme"] = []string{"ACME_TOKEN"}
	defer delete(provider.EnvTokens, "acme")
	ids := e.m.ConfigureProviderAccountsFromEnv(ctx, []string{"ACME_TOKEN__EU2=tokE", "OTHER=x"})
	if len(ids) != 1 || ids[0] != "pa_acme_env_eu2" {
		t.Fatalf("env accounts %v", ids)
	}
	ea := do("GET", "/v1/admin/providers/acme/accounts/pa_acme_env_eu2", nil, 200)
	if ea["credential_source"] != "env" || ea["credential_ref"] != "env:ACME_TOKEN__EU2" {
		t.Fatalf("env account %v", ea)
	}
	if st, _ := call(t, u, "POST", "/v1/admin/providers/acme/accounts/pa_acme_env_eu2/rotate", adm, map[string]any{"token": "tokB"}); st != 400 {
		t.Fatal("env account rotated through the API")
	}
	e.m.ConfigureProviderAccountsFromEnv(ctx, nil)
	if ea = do("GET", "/v1/admin/providers/acme/accounts/pa_acme_env_eu2", nil, 200); ea["enabled"] != false {
		t.Fatalf("env account not disabled after its variable went away: %v", ea)
	}

	// ---- overview + legacy list ----
	ov := do("GET", "/v1/admin/providers", nil, 200)
	var row map[string]any
	for _, x := range ov["items"].([]any) {
		if x.(map[string]any)["id"] == "acme" {
			row = x.(map[string]any)
		}
	}
	if row == nil || row["accounts_total"].(float64) < 3 || ov["summary"] == nil {
		t.Fatalf("admin providers %v", ov)
	}
	detail := do("GET", "/v1/admin/providers/acme", nil, 200)
	if len(detail["accounts"].([]any)) < 3 || detail["strategy_config"].(map[string]any)["strategy"] != "round_robin" {
		t.Fatalf("provider detail %v", detail)
	}
	_, pl := call(t, u, "GET", "/v1/providers", adm, nil)
	found := false
	for _, x := range items(t, pl) {
		if x["id"] == "acme" && x["accounts_summary"] != nil {
			found = true
		}
	}
	if !found {
		t.Fatal("GET /providers lacks accounts_summary")
	}
	tl := items(t, do("GET", "/v1/admin/providers/acme/accounts/"+idA+"/timeline", nil, 200))
	if len(tl) < 3 {
		t.Fatalf("timeline %v", tl)
	}
	// non-admin callers are refused
	ag, _ := e.agentClient("acct-agent", 0)
	if st, _, _ := ag.Do(ctx, "GET", "/admin/providers/acme/accounts", nil); st != 401 && st != 403 {
		t.Fatalf("agent reached the accounts API: %d", st)
	}

	// ---- no credential anywhere in responses or events ----
	var dump strings.Builder
	for _, p := range []string{"/v1/admin/providers/acme/accounts?archived=1", "/v1/admin/providers/acme", "/v1/admin/providers", "/v1/admin/infra/alerts?status=all", "/v1/providers"} {
		req, _ := http.NewRequest("GET", u+p, nil)
		req.Header.Set("Authorization", "Bearer "+adm)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		dump.Write(raw)
	}
	all, _ := e.m.Store.ListEvents(ctx, store.EventFilter{Limit: 1000})
	for _, ev := range all {
		raw, _ := json.Marshal(ev)
		dump.Write(raw)
	}
	for _, tok := range []string{"tokA", "tokA2", "tokB", "tokC", "tokE", "bot:tok"} {
		if strings.Contains(dump.String(), `"`+tok+`"`) || strings.Contains(dump.String(), tok+`"`) {
			t.Fatalf("credential %s leaked", tok)
		}
	}
}

// TestV220AccountsMigration: a pre-v2.2 database (one provider token, a
// live host without account, ledger rows) gets its default account, the
// host's account_id and this month's ledger stamped at startup.
func TestV220AccountsMigration(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "boxmes.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	crypt, _ := secret.New("test-master")
	ctx := context.Background()
	enc, _ := crypt.Seal("tokA")
	st.PutProvider(ctx, &model.Provider{ID: "acme", Name: "Acme", Tier: "T1", Implemented: true, Connected: true, TokenEnc: enc, TokenSource: "api"})
	now := time.Now().UTC()
	st.PutHost(ctx, &model.Host{ID: "host_old", Provider: "acme", Region: "r1", SKU: "s1", Status: "ready", Billing: "burst", CreatedAt: now, ProviderRef: "srv-old", PriceHour: 0.05})
	st.PutHost(ctx, &model.Host{ID: "host_gone", Provider: "acme", Status: "deleted", CreatedAt: now})
	st.PutBox(ctx, &model.Box{ID: "box_old", HostID: "host_old", Provider: "acme", Status: model.StatusRunning, CreatedAt: now, LastActiveAt: now})
	st.AddLedger(ctx, &model.LedgerEntry{ID: "led_1", TS: now, BoxID: "box_old", Provider: "acme", Kind: "compute", AmountUSD: 0.25})
	st.Close()

	st, err = store.Open(path) // re-open: migrations are idempotent
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cloud := newAcctCloud("acme", "tokA")
	m := manager.New(st, provider.NewRegistry(&provider.Local{}, cloud), crypt, manager.Config{DisablePriceFeed: true})
	if _, err := m.Init(ctx, "bx_admin_mig"); err != nil {
		t.Fatal(err)
	}
	a, err := st.GetProviderAccount(ctx, model.DefaultAccountID("acme"))
	if err != nil || !a.IsDefault || a.CredentialRef != "provider:acme" || a.KeyFingerprint == "" || a.Status != model.AccountActive {
		t.Fatalf("default account %+v %v", a, err)
	}
	if h, _ := st.GetHost(ctx, "host_old"); h.AccountID != a.ID {
		t.Fatalf("live host account %q", h.AccountID)
	}
	if h, _ := st.GetHost(ctx, "host_gone"); h.AccountID != "" {
		t.Fatal("deleted host got an account")
	}
	if sum, _ := st.SumLedger(ctx, store.LedgerFilter{AccountID: a.ID}); sum != 0.25 {
		t.Fatalf("ledger backfill %v", sum)
	}
	by, err := st.SumLedgerBy(ctx, store.LedgerFilter{}, "account_id")
	if err != nil || by[a.ID] != 0.25 {
		t.Fatalf("sum by account %v %v", by, err)
	}
	// the migrated host is deleted with the default account's key
	b, _ := st.GetBox(ctx, "box_old")
	b.Status = model.StatusTerminated
	st.PutBox(ctx, b)
	p, err := m.Authenticate(ctx, "bx_admin_mig")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.DeleteHost(ctx, p, "host_old", false); err != nil {
		t.Fatal(err)
	}
	if d := cloud.destroyed(); len(d) != 1 || d[0] != "tokA:srv-old" {
		t.Fatalf("destroy %v", d)
	}
}

func waitFor(t *testing.T, ok func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
