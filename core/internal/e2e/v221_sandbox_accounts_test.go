package e2e

// v2.2 M29-S: sandbox providers with several API keys. A fake E2B-like
// provider whose behaviour depends on the key: templates per account
// (credential "templates" field + 404 "template not found"), concurrency
// per account (paused sandboxes do not count; provider 429 "maximum number
// of concurrent sandboxes" is learned), create rate per account, a box keeps
// its account for pause / resume / native fork / destroy, a full account
// refuses a resume with a clear error and makes a clone fall back to
// snapshot → new sandbox on another account, a rotated key reaches the live
// shims, Retry-After drives the account cooldown, a billing group runs out
// of credit together, promo credit is drained to min_balance.

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"boxmes/core/internal/manager"
	"boxmes/core/internal/model"
	"boxmes/core/internal/provider"
	"boxmes/core/internal/sandbox"
	"boxmes/core/internal/store"
)

// acctSandbox is a T3 sandbox provider whose accounts are its keys.
type acctSandbox struct {
	dir       string
	mu        sync.Mutex
	valid     map[string]bool
	templates map[string][]string // key → templates of its account ("base" is public)
	cap       map[string]int      // key → provider-side concurrency (0 = none)
	fail      map[string]*sandbox.HTTPError
	owner     map[string]string // sandbox id → key that created it
	running   map[string]bool   // sandbox id → running (not paused / deleted)
	tries     map[string]int    // "key|template" → create calls
	createdAt map[string][]time.Time
	ops       []string // "op:key:id"
	n         int
}

func newAcctSandbox(dir string) *acctSandbox {
	return &acctSandbox{dir: dir, valid: map[string]bool{"tokA": true, "tokB": true, "tokB2": true}, templates: map[string][]string{"tokA": {"base"}, "tokB": {"base", "agent"}, "tokB2": {"base", "agent"}},
		cap: map[string]int{}, fail: map[string]*sandbox.HTTPError{}, owner: map[string]string{}, running: map[string]bool{}, tries: map[string]int{}, createdAt: map[string][]time.Time{}}
}

func (f *acctSandbox) Info() sandbox.Info {
	return sandbox.Info{ID: "fsb", Name: "Fake sandbox", Tier: "T2", Adapter: "native", Verification: sandbox.VerifiedDocs, Docs: "https://example.test/fsb", Initials: "FS",
		Caps:    sandbox.Capabilities{Pause: true, Fork: true, Ports: false, BootMS: 100, Isolation: "microvm"},
		Pricing: sandbox.Pricing{CPUSecond: 0.000001, GBSecond: 0.0000002, Source: "https://example.test/pricing", Confidence: "docs"},
		Sizes:   []sandbox.Size{{SKU: "base", CPU: 1, RAMMB: 512, DiskGB: 10}}, Regions: []sandbox.Region{{ID: "global", Name: "Global"}},
		WorkDir: filepath.Join(f.dir, "fsb"), EnvVars: []string{"FSB_KEY"}}
}

// SizesFor: like E2B, the credential's "templates" field (id=CPUxMB).
func (f *acctSandbox) SizesFor(c sandbox.Cred) []sandbox.Size {
	out := f.Info().Sizes
	for _, part := range strings.Split(c["templates"], ",") {
		if k, v, ok := strings.Cut(strings.TrimSpace(part), "="); ok {
			var cpu float64
			var ram int
			fmt.Sscanf(v, "%fx%d", &cpu, &ram)
			out = append(out, sandbox.Size{SKU: k, CPU: cpu, RAMMB: ram, DiskGB: 10})
		}
	}
	return out
}

func (f *acctSandbox) ListTemplates(ctx context.Context, c sandbox.Cred) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.templates[c.Token()]...), nil
}

func (f *acctSandbox) Validate(ctx context.Context, c sandbox.Cred) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.valid[c.Token()] {
		return &sandbox.HTTPError{Status: 401, Body: "bad key"}
	}
	return nil
}

func (f *acctSandbox) runningOf(tok string) int {
	n := 0
	for id, r := range f.running {
		if r && f.owner[id] == tok {
			n++
		}
	}
	return n
}

func (f *acctSandbox) Create(ctx context.Context, c sandbox.Cred, s sandbox.Spec) (*sandbox.Sandbox, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	tok := c.Token()
	tpl := s.SKU
	f.tries[tok+"|"+tpl]++
	if e := f.fail[tok]; e != nil {
		return nil, e
	}
	has := false
	for _, t := range f.templates[tok] {
		has = has || t == tpl
	}
	if !has {
		return nil, &sandbox.HTTPError{Status: 404, Body: `{"code":404,"message":"template '` + tpl + `' not found"}`}
	}
	if n := f.cap[tok]; n > 0 && f.runningOf(tok) >= n {
		return nil, &sandbox.HTTPError{Status: 429, Body: fmt.Sprintf(`{"code":429,"message":"You have reached the maximum number of concurrent sandboxes (%d)"}`, n), RetryAfter: 5}
	}
	f.n++
	id := fmt.Sprintf("sbx-%d", f.n)
	f.owner[id], f.running[id] = tok, true
	f.createdAt[tok] = append(f.createdAt[tok], time.Now())
	f.ops = append(f.ops, "create:"+tok+":"+id)
	return &sandbox.Sandbox{ID: id, State: sandbox.StateRunning, Template: tpl}, nil
}

func (f *acctSandbox) Get(ctx context.Context, c sandbox.Cred, id string) (*sandbox.Sandbox, error) {
	return &sandbox.Sandbox{ID: id, State: sandbox.StateRunning}, nil
}

func (f *acctSandbox) op(name string, c sandbox.Cred, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	tok := c.Token()
	f.ops = append(f.ops, name+":"+tok+":"+id)
	if f.owner[id] != "" && f.owner[id] != tok {
		return &sandbox.HTTPError{Status: 404, Body: "sandbox " + id + " not found"} // another account cannot see it
	}
	switch name {
	case "delete", "pause":
		f.running[id] = false
	case "resume":
		f.running[id] = true
	}
	return nil
}

func (f *acctSandbox) Delete(ctx context.Context, c sandbox.Cred, id string) error {
	return f.op("delete", c, id)
}
func (f *acctSandbox) Pause(ctx context.Context, c sandbox.Cred, id string) error {
	return f.op("pause", c, id)
}
func (f *acctSandbox) Resume(ctx context.Context, c sandbox.Cred, id string) error {
	return f.op("resume", c, id)
}

func (f *acctSandbox) Fork(ctx context.Context, c sandbox.Cred, id string) (*sandbox.Sandbox, error) {
	if err := f.op("fork", c, id); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.n++
	child := fmt.Sprintf("sbx-%d", f.n)
	f.owner[child], f.running[child] = c.Token(), true
	return &sandbox.Sandbox{ID: child, State: sandbox.StateRunning}, nil
}

func (f *acctSandbox) Exec(ctx context.Context, c sandbox.Cred, id string, r sandbox.ExecRequest) (*sandbox.ExecResult, error) {
	f.mu.Lock()
	f.ops = append(f.ops, "exec:"+c.Token()+":"+id)
	f.mu.Unlock()
	cmd := exec.CommandContext(ctx, "bash", "-c", r.Command)
	var so, se strings.Builder
	cmd.Stdout, cmd.Stderr = &so, &se
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		return nil, err
	}
	return &sandbox.ExecResult{ExitCode: code, Stdout: so.String(), Stderr: se.String()}, nil
}

func (f *acctSandbox) count(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, o := range f.ops {
		if strings.HasPrefix(o, prefix) {
			n++
		}
	}
	return n
}

func (f *acctSandbox) triesOf(k string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tries[k]
}

func (f *acctSandbox) set(fn func()) {
	f.mu.Lock()
	fn()
	f.mu.Unlock()
}

func TestV221SandboxAccounts(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash required")
	}
	dir := t.TempDir()
	fake := newAcctSandbox(dir)
	reg := provider.NewRegistry(&provider.Local{})
	reg.Add(sandbox.AsProvider(fake))
	e := setupReg(t, reg, func(c *manager.Config) {
		c.AutoSandbox = true
		c.ProviderChain = []string{"fsb"}
		c.HealthInterval = time.Hour
		c.SandboxEmptyAfter = 200 * time.Millisecond
		c.SandboxMode = "shim"
	}, 0)
	u, adm, ctx := e.srv.URL, "bx_admin_e2e", e.ctx
	ag, _ := e.agentClient("sbx-accts", 0)
	do := func(method, path string, body any, want int) map[string]any {
		t.Helper()
		st, v := call(t, u, method, path, adm, body)
		if st != want {
			t.Fatalf("%s %s: %d (want %d) %v", method, path, st, want, v)
		}
		return v
	}
	box := func(slug string, cpu float64, ram int) (*model.Box, string) {
		t.Helper()
		st, raw, err := ag.Do(ctx, "POST", "/boxes", map[string]any{"template": "base", "slug": slug, "lifecycle": "task",
			"requirements": map[string]any{"cpu": cpu, "ram_mb": ram, "disk_gb": 5, "isolation": "sandbox"}})
		if err != nil {
			t.Fatal(err)
		}
		if st >= 300 {
			return nil, string(raw)
		}
		var b model.Box
		json.Unmarshal(raw, &b)
		deadline := time.Now().Add(20 * time.Second)
		for {
			cur, _ := e.m.Store.GetBox(ctx, b.ID)
			if cur != nil && (cur.Status == model.StatusRunning || cur.Status == model.StatusFailed) {
				if cur.Status == model.StatusFailed {
					return cur, cur.Error
				}
				return cur, ""
			}
			if time.Now().After(deadline) {
				t.Fatalf("box %s never ran: %+v", slug, cur)
			}
			time.Sleep(30 * time.Millisecond)
		}
	}
	hostAcct := func(b *model.Box) string {
		h, err := e.m.Store.GetHost(ctx, b.HostID)
		if err != nil {
			t.Fatal(err)
		}
		return h.AccountID
	}
	down := func(b *model.Box) {
		t.Helper()
		if st, _, err := ag.Do(ctx, "DELETE", "/boxes/"+b.ID, nil); err != nil || st >= 300 {
			t.Fatalf("delete %s: %d %v", b.ID, st, err)
		}
		waitHostStatus(t, e, b.HostID, "deleted")
	}

	// ---- two accounts: A (priority 1, public templates only), B (its own "agent" template) ----
	do("PUT", "/v1/admin/providers/fsb/strategy", map[string]any{"strategy": "priority"}, 200)
	a := do("POST", "/v1/admin/providers/fsb/accounts", map[string]any{"label": "Project A", "token": "tokA", "priority": 1, "external_id": "prj-a"}, 201)
	b := do("POST", "/v1/admin/providers/fsb/accounts", map[string]any{"label": "Project B", "token": `{"token":"tokB","templates":"agent=2x2048"}`, "priority": 2, "external_id": "prj-b"}, 201)
	idA, idB := a["id"].(string), b["id"].(string)
	if err := e.m.RefreshPricesNow(ctx); err != nil && !strings.Contains(err.Error(), "battleships") {
		t.Logf("refresh: %v", err)
	}
	// the catalog has B's template although A is the primary key
	offers, _ := e.m.Store.ListOffers(ctx, "fsb")
	seen := false
	for _, o := range offers {
		seen = seen || o.SKU == "agent"
	}
	if !seen {
		t.Fatalf("agent template of account B missing from the catalog: %+v", offers)
	}
	sv := do("GET", "/v1/admin/providers/fsb/sandbox", nil, 200)
	rows := map[string]map[string]any{}
	for _, r := range sv["accounts"].([]any) {
		m := r.(map[string]any)
		rows[m["id"].(string)] = m
	}
	if rows[idA]["templates"].(map[string]any)["agent"] != "unknown" || rows[idB]["templates"].(map[string]any)["agent"] != "available" ||
		rows[idA]["templates"].(map[string]any)["base"] != "available" {
		t.Fatalf("template matrix %v", sv)
	}
	if raw, _ := json.Marshal(sv); strings.Contains(string(raw), "tokB") || strings.Contains(string(raw), "tokA") {
		t.Fatal("credential leaked in the sandbox view")
	}

	// ---- template availability: A answers 404 → learned, B creates; next time A is not tried ----
	bt, errT := box("tpl-one", 2, 2048)
	if errT != "" || hostAcct(bt) != idB || fake.triesOf("tokA|agent") != 1 || fake.triesOf("tokB|agent") != 1 {
		t.Fatalf("template failover: %s acct %v tries %v", errT, bt, fake.tries)
	}
	ga := do("GET", "/v1/admin/providers/fsb/accounts/"+idA, nil, 200)
	if fmt.Sprint(ga["templates_missing"]) != "[agent]" || ga["status"] != "active" {
		t.Fatalf("A after 404: %v", ga)
	}
	if evs, _ := e.m.Store.ListEvents(ctx, store.EventFilter{Type: "provider_account.template_missing"}); len(evs) != 1 || evs[0].Data["template"] != "agent" {
		t.Fatalf("template_missing event %+v", evs)
	}
	bt2, errT := box("tpl-two", 2, 2048)
	if errT != "" || hostAcct(bt2) != idB || fake.triesOf("tokA|agent") != 1 {
		t.Fatalf("A tried again for a missing template: %s tries %v", errT, fake.tries)
	}
	down(bt)
	down(bt2)
	// template sync with A's own key: A lists base only, the learned entry stays
	sy := do("POST", "/v1/admin/providers/fsb/accounts/"+idA+"/templates/sync", nil, 200)
	if fmt.Sprint(sy["templates"]) != "[base]" || fmt.Sprint(sy["account"].(map[string]any)["templates_missing"]) != "[agent]" {
		t.Fatalf("sync %v", sy)
	}

	// ---- concurrency per account: A limit 1, live counts; paused sandboxes free the slot ----
	do("PATCH", "/v1/admin/providers/fsb/accounts/"+idA, map[string]any{"limits": map[string]any{"concurrent_sandboxes": 1}}, 200)
	b1, err1 := box("conc-one", 1, 512)
	if err1 != "" || hostAcct(b1) != idA {
		t.Fatalf("box 1 on A: %s %v", err1, b1)
	}
	triesA := fake.triesOf("tokA|base")
	b2, err2 := box("conc-two", 1, 512)
	if err2 != "" || hostAcct(b2) != idB || fake.triesOf("tokA|base") != triesA {
		t.Fatalf("A full must be skipped without calling it: %s acct %s tries %v", err2, hostAcct(b2), fake.tries)
	}
	sv = do("GET", "/v1/admin/providers/fsb/sandbox", nil, 200)
	for _, r := range sv["accounts"].([]any) {
		m := r.(map[string]any)
		if m["id"] == idA && (m["running"].(float64) != 1 || m["concurrent_limit"].(float64) != 1 || m["concurrency_pct"].(float64) != 100) {
			t.Fatalf("A gauge %v", m)
		}
	}
	// pause box 1 with A's key → A has a free slot again
	if st, raw, _ := ag.Do(ctx, "POST", "/boxes/"+b1.ID+"/sleep", nil); st >= 300 {
		t.Fatalf("sleep %d %s", st, raw)
	}
	if fake.count("pause:tokA:") != 1 {
		t.Fatalf("pause not with A's key: %v", fake.ops)
	}
	b3, err3 := box("conc-three", 1, 512)
	if err3 != "" || hostAcct(b3) != idA {
		t.Fatalf("paused sandbox must not count: %s acct %s", err3, hostAcct(b3))
	}
	// A is full again: resuming box 1 is refused clearly (it can only resume in A)
	st, raw, _ := ag.Do(ctx, "POST", "/boxes/"+b1.ID+"/wake", nil)
	if st < 400 || !strings.Contains(string(raw), "cannot resume this box") || fake.count("resume:") != 0 {
		t.Fatalf("wake on a full account: %d %s", st, raw)
	}
	down(b3)
	if st, raw, _ := ag.Do(ctx, "POST", "/boxes/"+b1.ID+"/wake", nil); st >= 300 || fake.count("resume:tokA:") != 1 {
		t.Fatalf("wake after a slot freed: %d %s ops %v", st, raw, fake.ops)
	}

	// ---- clone: native fork stays in A; A full → snapshot → new sandbox on B ----
	var cl model.Box
	if st, raw, _ := ag.Do(ctx, "POST", "/boxes/"+b1.ID+"/clone", map[string]any{"slug": "clone-full"}); st >= 300 {
		t.Fatalf("clone %d %s", st, raw)
	} else {
		json.Unmarshal(raw, &cl)
	}
	if fake.count("fork:") != 0 || hostAcct(&cl) != idB || !strings.Contains(cl.PlacementReason, "native fork skipped") {
		t.Fatalf("clone on a full account: %q acct %s ops %v", cl.PlacementReason, hostAcct(&cl), fake.ops)
	}
	down(&cl)
	do("PATCH", "/v1/admin/providers/fsb/accounts/"+idA, map[string]any{"limits": map[string]any{"concurrent_sandboxes": 5}}, 200)
	var cl2 model.Box
	if st, raw, _ := ag.Do(ctx, "POST", "/boxes/"+b1.ID+"/clone", map[string]any{"slug": "clone-native"}); st >= 300 {
		t.Fatalf("clone 2 %d %s", st, raw)
	} else {
		json.Unmarshal(raw, &cl2)
	}
	if fake.count("fork:tokA:") != 1 || hostAcct(&cl2) != idA {
		t.Fatalf("native fork must use and stay in A: acct %s ops %v", hostAcct(&cl2), fake.ops)
	}
	down(&cl2)
	down(b1)
	if n := fake.count("delete:tokA:"); n < 3 {
		t.Fatalf("A's sandboxes must be deleted with A's key: %v", fake.ops)
	}

	// ---- rotation reaches the live shim: b2 (on B) pauses with the new key ----
	do("POST", "/v1/admin/providers/fsb/accounts/"+idB+"/rotate", map[string]any{"token": `{"token":"tokB2","templates":"agent=2x2048"}`, "external_id": "prj-b"}, 200)
	h2, _ := e.m.Store.GetHost(ctx, b2.HostID)
	fake.set(func() { fake.owner[h2.ProviderRef] = "tokB2" }) // a new key of the same project sees its sandboxes
	if st, raw, _ := ag.Do(ctx, "POST", "/boxes/"+b2.ID+"/sleep", nil); st >= 300 || fake.count("pause:tokB2:") != 1 {
		t.Fatalf("rotated key not used by the live shim: %d %s ops %v", st, raw, fake.ops)
	}
	down(b2)

	// ---- provider 429 "maximum number of concurrent" on B → learned limit, failover to A ----
	do("PATCH", "/v1/admin/providers/fsb/accounts/"+idA, map[string]any{"priority": 3}, 200) // B first now
	fake.set(func() { fake.cap["tokB2"] = 1 })
	k1, errK := box("cap-one", 1, 512)
	if errK != "" || hostAcct(k1) != idB {
		t.Fatalf("cap box 1 %s", errK)
	}
	k2, errK := box("cap-two", 1, 512)
	if errK != "" || hostAcct(k2) != idA {
		t.Fatalf("429 concurrency on B must fail over to A: %s", errK)
	}
	gb := do("GET", "/v1/admin/providers/fsb/accounts/"+idB, nil, 200)
	lim := gb["limits"].(map[string]any)["concurrent_sandboxes"].(map[string]any)
	if gb["status"] != "quota_full" || lim["limit"].(float64) != 1 || lim["source"] != "learned" {
		t.Fatalf("B after a concurrency 429: %v", gb)
	}
	if cu, _ := time.Parse(time.RFC3339, gb["cooldown_until"].(string)); time.Until(cu) > 3*time.Minute || time.Until(cu) < time.Minute {
		t.Fatalf("sandbox quota cooldown %v", gb["cooldown_until"])
	}
	down(k1)
	down(k2)
	do("POST", "/v1/admin/providers/fsb/accounts/"+idB+"/reset", nil, 200)
	fake.set(func() { fake.cap["tokB2"] = 0 })

	// ---- Retry-After of a plain 429 drives the account cooldown ----
	fake.set(func() {
		fake.fail["tokB2"] = &sandbox.HTTPError{Status: 429, Body: `{"code":"RATE_LIMITED","message":"request rate limit exceeded"}`, RetryAfter: 40}
	})
	r1, errR := box("rl-one", 1, 512)
	if errR != "" || hostAcct(r1) != idA {
		t.Fatalf("rate limited B must fail over to A: %s", errR)
	}
	gb = do("GET", "/v1/admin/providers/fsb/accounts/"+idB, nil, 200)
	cu, _ := time.Parse(time.RFC3339, gb["cooldown_until"].(string))
	if gb["status"] != "rate_limited" || time.Until(cu) < 30*time.Second || time.Until(cu) > 50*time.Second {
		t.Fatalf("Retry-After cooldown: %v (%v)", gb["status"], gb["cooldown_until"])
	}
	down(r1)
	fake.set(func() { delete(fake.fail, "tokB2") })
	do("POST", "/v1/admin/providers/fsb/accounts/"+idB+"/reset", nil, 200)

	// ---- create rate per account: B create_per_sec 1, A drained → the 2nd create waits for the slot ----
	do("POST", "/v1/admin/providers/fsb/accounts/"+idA+"/drain", nil, 200)
	do("PATCH", "/v1/admin/providers/fsb/accounts/"+idB, map[string]any{"limits": map[string]any{"create_per_sec": 1}}, 200)
	var wg sync.WaitGroup
	got := make([]*model.Box, 2)
	errs := make([]string, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			got[i], errs[i] = box(fmt.Sprintf("rate-%d", i), 1, 512)
		}(i)
	}
	wg.Wait()
	fake.mu.Lock()
	ts := append([]time.Time{}, fake.createdAt["tokB2"]...)
	fake.mu.Unlock()
	if errs[0] != "" || errs[1] != "" || len(ts) < 2 || ts[len(ts)-1].Sub(ts[len(ts)-2]) < 900*time.Millisecond {
		t.Fatalf("create rate: errs %v gaps %v", errs, ts)
	}
	for _, x := range got {
		down(x)
	}
	do("POST", "/v1/admin/providers/fsb/accounts/"+idA+"/undrain", nil, 200)
	do("PATCH", "/v1/admin/providers/fsb/accounts/"+idB, map[string]any{"limits": map[string]any{"create_per_sec": 0}}, 200)

	// ---- no account has the template: clear error ----
	fake.set(func() { fake.templates["tokB2"] = []string{"base"} })
	_, errNone := box("tpl-none-1", 2, 2048) // B answers 404 now → learned
	if !strings.Contains(errNone, "template") {
		t.Fatalf("expected a template error, got %q", errNone)
	}
	_, errNone = box("tpl-none-2", 2, 2048)
	if !strings.Contains(errNone, "template agent is not available in any account") {
		t.Fatalf("clear template error: %q", errNone)
	}
	do("PATCH", "/v1/admin/providers/fsb/accounts/"+idB, map[string]any{"clear_templates_missing": true}, 200)

	// ---- billing group: one wallet → both out of credit; a top-up lifts both ----
	do("PATCH", "/v1/admin/providers/fsb/accounts/"+idA, map[string]any{"billing_group": "w1"}, 200)
	do("PATCH", "/v1/admin/providers/fsb/accounts/"+idB, map[string]any{"billing_group": "w1"}, 200)
	fake.set(func() {
		fake.fail["tokB2"] = &sandbox.HTTPError{Status: 402, Body: "credits exhausted: add a payment method"}
		fake.fail["tokA"] = &sandbox.HTTPError{Status: 402, Body: "credits exhausted: add a payment method"}
	})
	_, errP := box("pay-one", 1, 512)
	if errP == "" {
		t.Fatal("create must fail when the wallet is empty")
	}
	ga, gb = do("GET", "/v1/admin/providers/fsb/accounts/"+idA, nil, 200), do("GET", "/v1/admin/providers/fsb/accounts/"+idB, nil, 200)
	if ga["status"] != "out_of_credit" || gb["status"] != "out_of_credit" {
		t.Fatalf("billing group: A %v B %v", ga["status"], gb["status"])
	}
	fake.set(func() { delete(fake.fail, "tokA"); delete(fake.fail, "tokB2") })
	do("PATCH", "/v1/admin/providers/fsb/accounts/"+idB, map[string]any{"credit_remaining": 80}, 200)
	ga = do("GET", "/v1/admin/providers/fsb/accounts/"+idA, nil, 200)
	if ga["status"] != "active" {
		t.Fatalf("top-up of the wallet must lift the group: A %v (%v)", ga["status"], ga["status_reason"])
	}

	// ---- promo credit: effective credit below min_balance → not eligible (drain at threshold) ----
	pa := do("PATCH", "/v1/admin/providers/fsb/accounts/"+idA, map[string]any{"credit_remaining": 0.5, "min_balance": 1}, 200)
	st0 := pa["stats"].(map[string]any)
	if st0["eligible"] != false || !strings.Contains(fmt.Sprint(st0["ineligible"]), "below the minimum") || st0["balance_effective"].(float64) != 0.5 {
		t.Fatalf("promo credit floor: %v", st0)
	}

	// ---- limit group validation, non-sandbox provider, no credential in events ----
	pb := do("PATCH", "/v1/admin/providers/fsb/accounts/"+idB, map[string]any{"limit_group": "org-1"}, 200)
	if pb["limit_group"] != "org-1" {
		t.Fatalf("limit group %v", pb)
	}
	if st, _ := call(t, u, "PATCH", "/v1/admin/providers/fsb/accounts/"+idB, adm, map[string]any{"limit_group": "bad group"}); st != 400 {
		t.Fatalf("bad limit_group accepted: %d", st)
	}
	if st, _ := call(t, u, "GET", "/v1/admin/providers/local/sandbox", adm, nil); st != 400 {
		t.Fatalf("sandbox view of a VPS provider: %d", st)
	}
	evs, _ := e.m.Store.ListEvents(ctx, store.EventFilter{Limit: 1000})
	for _, ev := range evs {
		raw, _ := json.Marshal(ev)
		if strings.Contains(string(raw), "tokB2") || strings.Contains(string(raw), `"tokA"`) {
			t.Fatalf("credential in event %s", raw)
		}
	}
}
