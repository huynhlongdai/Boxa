package e2e

// v1.4: T2/T3 sandbox provider tier, end to end through REST: catalog +
// recommend show sandbox options, box_up places a box on a T3 shim after a
// failing sandbox provider fails over, a T2 sandbox runs `boxd --dial`
// (simulated in-process) and joins like any host, and both sandboxes are
// destroyed when their box ends.

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"boxa/core/internal/boxd"
	"boxa/core/internal/manager"
	"boxa/core/internal/model"
	"boxa/core/internal/sandbox"
	"boxa/core/internal/selector"
	"boxa/core/internal/store"
)

// fakeSandbox is a sandbox provider whose "sandboxes" are directories on this
// machine. Mode dial: the bootstrap starts an in-process boxd (instead of
// downloading the binary). Mode exec: commands run with bash (T3 shim).
type fakeSandbox struct {
	id, tier string
	price    float64
	fail     bool
	dir      string
	coreWS   string
	ctx      context.Context
	mu       sync.Mutex
	created  []string
	deleted  []string
	boots    int
}

var tokRe = regexp.MustCompile(`BOXD_TOKEN='([^']+)'`)

func (f *fakeSandbox) Info() sandbox.Info {
	return sandbox.Info{ID: f.id, Name: "Fake " + f.id, Tier: f.tier, Adapter: "native", Verification: sandbox.VerifiedDocs, Docs: "https://example.test/" + f.id,
		Initials: "FK", Caps: sandbox.Capabilities{Background: f.tier == "T2", Pause: true, Fork: true, Ports: true, Files: false, BootMS: 200, Isolation: "microvm"},
		Pricing: sandbox.Pricing{CPUSecond: f.price, GBSecond: f.price / 4, Source: "https://example.test/pricing", Confidence: "docs"},
		Sizes:   []sandbox.Size{{SKU: "small", CPU: 1, RAMMB: 2048, DiskGB: 10}}, Regions: []sandbox.Region{{ID: "global", Name: "Global"}},
		WorkDir: filepath.Join(f.dir, f.id), EnvVars: []string{strings.ToUpper(f.id) + "_KEY"}}
}
func (f *fakeSandbox) Validate(ctx context.Context, c sandbox.Cred) error {
	if c.Token() != "good" {
		return &sandbox.HTTPError{Status: 401, Body: "bad key"}
	}
	return nil
}
func (f *fakeSandbox) Create(ctx context.Context, c sandbox.Cred, s sandbox.Spec) (*sandbox.Sandbox, error) {
	if f.fail {
		return nil, &sandbox.HTTPError{Status: 503, Body: "no capacity"}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	id := f.id + "-" + s.Name
	f.created = append(f.created, id)
	return &sandbox.Sandbox{ID: id, State: sandbox.StateRunning}, nil
}
func (f *fakeSandbox) Get(ctx context.Context, c sandbox.Cred, id string) (*sandbox.Sandbox, error) {
	return &sandbox.Sandbox{ID: id, State: sandbox.StateRunning}, nil
}
func (f *fakeSandbox) Delete(ctx context.Context, c sandbox.Cred, id string) error {
	f.mu.Lock()
	f.deleted = append(f.deleted, id)
	f.mu.Unlock()
	return nil
}
func (f *fakeSandbox) Exec(ctx context.Context, c sandbox.Cred, id string, r sandbox.ExecRequest) (*sandbox.ExecResult, error) {
	if strings.Contains(r.Command, "--dial") { // T2 bootstrap → in-process boxd
		m := tokRe.FindStringSubmatch(r.Command)
		if m == nil {
			return &sandbox.ExecResult{ExitCode: 1, Stderr: "no token"}, nil
		}
		f.mu.Lock()
		f.boots++
		n := f.boots
		f.mu.Unlock()
		root := filepath.Join(f.dir, f.id, "boot", strings.Repeat("x", n))
		drv, err := boxd.NewLocalDriver(filepath.Join(root, "root"))
		if err != nil {
			return nil, err
		}
		agent, err := boxd.NewAgent(boxd.Config{CoreURL: f.coreWS, BootstrapToken: m[1], StateDir: filepath.Join(root, "state"), Provider: f.id, Region: "global",
			IP: "127.0.0.1", MetricsEvery: time.Second, Version: "test"}, drv)
		if err != nil {
			return nil, err
		}
		go agent.Run(f.ctx)
		return &sandbox.ExecResult{Stdout: "boxd-started\n"}, nil
	}
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

func (f *fakeSandbox) counts() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.created), len(f.deleted)
}

func TestV14SandboxProviders(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash required")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dir := t.TempDir()
	failing := &fakeSandbox{id: "failsbx", tier: "T2", price: 0.000001, fail: true, dir: dir, ctx: ctx}
	shim := &fakeSandbox{id: "fakeshim", tier: "T3", price: 0.000004, dir: dir, ctx: ctx}
	dial := &fakeSandbox{id: "fakesbx", tier: "T2", price: 0.000006, dir: dir, ctx: ctx}
	reg := sandbox.DefaultRegistry(nil)
	reg.Add(sandbox.AsProvider(failing), sandbox.AsProvider(shim), sandbox.AsProvider(dial))
	e := setupReg(t, reg, func(c *manager.Config) {
		c.AutoSandbox = true
		c.ProviderChain = []string{"failsbx", "fakeshim", "fakesbx"}
		c.HealthInterval = time.Hour
		c.SandboxEmptyAfter = 300 * time.Millisecond
	}, 0)
	dial.coreWS = "ws" + strings.TrimPrefix(e.srv.URL, "http") + "/v1/hosts/connect"
	ag, _ := e.agentClient("sbx-agent", 0)

	// sandbox provider catalog: built-ins + fakes with verification levels
	var sp model.List[manager.SandboxProviderView]
	e.must(ag, "GET", "/sandbox/providers", nil, &sp)
	ids := map[string]manager.SandboxProviderView{}
	for _, p := range sp.Items {
		ids[p.ID] = p
	}
	for _, id := range []string{"e2b", "novita", "daytona", "prized", "boxdsh", "runloop", "blaxel", "vercel", "boat", "agent37", "upstash-box", "fakeshim"} {
		if _, ok := ids[id]; !ok {
			t.Fatalf("sandbox provider %s missing (%d listed)", id, len(sp.Items))
		}
	}
	if ids["prized"].Verification != sandbox.VerifiedLive || ids["upstash-box"].Verification != sandbox.VerifiedSDK || ids["upstash-box"].Mode != "shim" || ids["e2b"].Connected {
		t.Fatalf("metadata: %+v / %+v", ids["prized"], ids["upstash-box"])
	}
	if ids["fakesbx"].Mode != "dial" || ids["fakeshim"].Mode != "shim" || ids["e2b"].FromUSDH <= 0 {
		t.Fatalf("modes: %+v %+v", ids["fakesbx"], ids["fakeshim"])
	}
	var provs model.List[model.Provider]
	e.must(e.admin, "GET", "/providers", nil, &provs)
	groups := map[string]string{}
	for _, p := range provs.Items {
		groups[p.ID] = p.Group + "/" + p.Adapter
	}
	if groups["hetzner"] != "vps/api" || groups["runloop"] != "sandbox/yaml" || groups["daytona"] != "sandbox/native" {
		t.Fatalf("provider groups %+v", groups)
	}

	// catalog: per-second sandbox sizes appear before connecting
	var cat manager.Catalog
	e.must(e.admin, "GET", "/catalog?provider=e2b", nil, &cat)
	if len(cat.Items) == 0 || cat.Items[0].Tier != "T2" || cat.Items[0].Group != "sandbox" || cat.Items[0].MinBillingMinutes != 0 || cat.Capabilities["e2b"]["fork"] != true {
		t.Fatalf("catalog %+v caps %+v", cat.Items, cat.Capabilities["e2b"])
	}

	// connect: wrong key is refused, good key saved
	if st, _, _ := e.admin.Do(e.ctx, "PUT", "/providers/fakeshim/token", map[string]any{"token": "bad"}); st < 400 {
		t.Fatalf("bad token accepted: %d", st)
	}
	for _, id := range []string{"failsbx", "fakeshim"} {
		e.must(e.admin, "PUT", "/providers/"+id+"/token", map[string]any{"token": "good"}, nil)
	}

	// recommend: untrusted short task → a connected sandbox is the pick
	var rec manager.RecommendResult
	e.must(ag, "POST", "/recommend", map[string]any{"template": "base", "lifecycle": "task",
		"requirements": map[string]any{"cpu": 1, "ram_mb": 1024, "disk_gb": 5, "untrusted": true, "isolation": "sandbox", "lifetime": "ephemeral", "duration_hours": 0.2}}, &rec)
	if rec.Pick == nil || rec.Pick.Kind != selector.KindSandbox || rec.Pick.RequiresApproval || rec.Pick.Provider != "failsbx" {
		t.Fatalf("pick %+v (auto %s)", rec.Pick, rec.AutoPick)
	}
	sawE2B := false
	for _, o := range rec.Options {
		if o.Provider == "e2b" {
			sawE2B = true
		}
		if o.Kind == selector.KindSandbox && o.Provider == "fakeshim" && (o.Tier != "T3" || o.MinBillingHours != 0) {
			t.Fatalf("shim option %+v", o)
		}
	}
	_ = sawE2B // E2B's base template (512 MB) is too small for 1 GB: it shows up under rejected
	if !strings.Contains(strings.Join(rec.Pick.Reasons, " "), "own-kernel sandbox") {
		t.Fatalf("reasons %v", rec.Pick.Reasons)
	}

	// box_up: failsbx fails → failover to the T3 shim
	var b model.Box
	st, raw, err := ag.Do(e.ctx, "POST", "/boxes", map[string]any{"template": "base", "slug": "sbx-one", "lifecycle": "task",
		"requirements": map[string]any{"cpu": 1, "ram_mb": 1024, "disk_gb": 5, "isolation": "sandbox"}})
	if err != nil || st >= 300 {
		t.Fatalf("create %d %s %v", st, raw, err)
	}
	json.Unmarshal(raw, &b)
	got := waitStatus(t, e.m, b.ID, model.StatusRunning, 20*time.Second)
	h, _ := e.m.Store.GetHost(e.ctx, got.HostID)
	if got.Provider != "fakeshim" || h.Billing != "sandbox" || h.Driver != "shim" || got.Estimate == nil || got.Estimate.Kind != "sandbox" {
		t.Fatalf("placed %+v host %+v", got, h)
	}
	evs, _ := e.m.Store.ListEvents(e.ctx, store.EventFilter{BoxID: b.ID, Limit: 100})
	failed := false
	for _, ev := range evs {
		failed = failed || ev.Type == "box.burst_failed" && strings.Contains(ev.Message, "failsbx")
	}
	if !failed {
		t.Fatal("expected a failover event from failsbx")
	}
	var res model.ExecResult
	e.must(ag, "POST", "/boxes/"+b.ID+"/exec", map[string]any{"command": "echo hi from shim; pwd"}, &res)
	if res.ExitCode != 0 || !strings.Contains(res.StdoutTail, "hi from shim") || !strings.Contains(res.StdoutTail, filepath.Join(dir, "fakeshim", "work")) {
		t.Fatalf("shim exec %+v", res)
	}
	e.must(ag, "PUT", "/boxes/"+b.ID+"/files", map[string]any{"path": "notes/a.txt", "content": "one\ntwo\n"}, nil)
	var fr map[string]any
	e.must(ag, "GET", "/boxes/"+b.ID+"/files?path=notes/a.txt&from=2&to=2", nil, &fr)
	if fr["content"] != "two\n" {
		t.Fatalf("read %+v", fr)
	}
	e.must(ag, "DELETE", "/boxes/"+b.ID, nil, nil)
	waitHostStatus(t, e, got.HostID, "deleted")
	if c, d := shim.counts(); c != 1 || d < 1 {
		t.Fatalf("shim sandboxes created %d deleted %d", c, d)
	}

	// T2: disconnect the others, connect the dial-in provider
	for _, id := range []string{"failsbx", "fakeshim"} {
		e.must(e.admin, "PUT", "/providers/"+id+"/token", map[string]any{"token": ""}, nil)
	}
	e.must(e.admin, "PUT", "/providers/fakesbx/token", map[string]any{"token": "good"}, nil)
	st, raw, err = ag.Do(e.ctx, "POST", "/boxes", map[string]any{"template": "base", "slug": "sbx-two", "lifecycle": "task",
		"requirements": map[string]any{"cpu": 1, "ram_mb": 1024, "disk_gb": 5, "isolation": "sandbox"}})
	if err != nil || st >= 300 {
		t.Fatalf("create T2 %d %s %v", st, raw, err)
	}
	var b2 model.Box
	json.Unmarshal(raw, &b2)
	got2 := waitStatus(t, e.m, b2.ID, model.StatusRunning, 30*time.Second)
	h2, _ := e.m.Store.GetHost(e.ctx, got2.HostID)
	if got2.Provider != "fakesbx" || h2.Billing != "sandbox" || h2.Driver != "local" || !e.m.Hub.Connected(h2.ID) || e.m.Hub.IsVirtual(h2.ID) {
		t.Fatalf("T2 placed %+v host %+v", got2, h2)
	}
	e.must(ag, "POST", "/boxes/"+b2.ID+"/exec", map[string]any{"command": "echo hi from boxd"}, &res)
	if res.ExitCode != 0 || !strings.Contains(res.StdoutTail, "hi from boxd") {
		t.Fatalf("T2 exec %+v", res)
	}
	e.must(ag, "DELETE", "/boxes/"+b2.ID, nil, nil)
	waitHostStatus(t, e, got2.HostID, "deleted")
	if c, d := dial.counts(); c != 1 || d != 1 {
		t.Fatalf("dial sandboxes created %d deleted %d", c, d)
	}

	// declarative adapter linter
	var lint map[string]any
	e.must(e.admin, "POST", "/sandbox/adapters/lint", map[string]any{"yaml": "id: x\nname: X\n"}, &lint)
	if lint["ok"] != false || !strings.Contains(lint["error"].(string), "missing") {
		t.Fatalf("lint %+v", lint)
	}
}

func waitHostStatus(t *testing.T, e *env, id, status string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		h, err := e.m.Store.GetHost(e.ctx, id)
		if err == nil && h.Status == status {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("host %s: want %s, have %+v", id, status, h)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
