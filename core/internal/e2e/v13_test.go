package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"boxaz/core/internal/boxd"
	"boxaz/core/internal/client"
	"boxaz/core/internal/manager"
	"boxaz/core/internal/model"
	"boxaz/core/internal/provider"
)

// fakeCloud is an in-process provider: Create either fails like a sold-out
// provider (HTTP 503) or "boots a server" by starting a local boxd that dials
// core with the one-time bootstrap token from cloud-init.
type fakeCloud struct {
	id        string
	price     float64
	failWith  string
	validErr  string
	dir       string
	ctx       context.Context
	mu        sync.Mutex
	creates   int
	destroyed []string
}

func (f *fakeCloud) ID() string           { return f.id }
func (f *fakeCloud) Name() string         { return "Fake " + f.id }
func (f *fakeCloud) Tier() string         { return "T1" }
func (f *fakeCloud) Implemented() bool    { return true }
func (f *fakeCloud) Caps() provider.Caps  { return provider.Caps{} }
func (f *fakeCloud) setValidErr(s string) { f.mu.Lock(); f.validErr = s; f.mu.Unlock() }
func (f *fakeCloud) Validate(ctx context.Context, token string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if token != "good" {
		return model.EH(model.ErrUnauthorized, "Create a read+write API token in the console and paste it again.", "%s rejected the token (%d)", f.id, 401)
	}
	if f.validErr != "" {
		return fmt.Errorf("%s", f.validErr)
	}
	return nil
}
func (f *fakeCloud) Regions(ctx context.Context, token string) ([]provider.Region, error) {
	return []provider.Region{{ID: "sin", Name: "Singapore", Available: true}, {ID: "fsn1", Name: "Falkenstein", Available: true}}, nil
}
func (f *fakeCloud) Offers(ctx context.Context, token string) ([]model.Offer, error) {
	now := time.Now().UTC()
	var out []model.Offer
	for _, r := range []string{"sin", "fsn1"} {
		out = append(out, model.Offer{Provider: f.id, SKU: "s2", Region: r, CPU: 2, RAMMB: 4096, DiskGB: 40, PriceHour: f.price, PriceMonth: f.price * 730, MinBillingMinutes: 60, Source: f.id + "-api", FetchedAt: now, Confidence: "verified"})
	}
	return out, nil
}

var ciRe = regexp.MustCompile(`BOXD_(CORE_URL|TOKEN|REGION)=(\S+)`)

func (f *fakeCloud) Create(ctx context.Context, token string, req provider.CreateRequest) (provider.Handle, error) {
	f.mu.Lock()
	f.creates++
	n := f.creates
	f.mu.Unlock()
	if f.failWith != "" {
		return provider.Handle{}, fmt.Errorf("%s", f.failWith)
	}
	kv := map[string]string{}
	for _, m := range ciRe.FindAllStringSubmatch(req.CloudInit, -1) {
		kv[m[1]] = m[2]
	}
	sfx := fmt.Sprintf("%s-%d", f.id, n)
	drv, err := boxd.NewLocalDriver(filepath.Join(f.dir, "root-"+sfx))
	if err != nil {
		return provider.Handle{}, err
	}
	ag, err := boxd.NewAgent(boxd.Config{CoreURL: kv["CORE_URL"], BootstrapToken: kv["TOKEN"], StateDir: filepath.Join(f.dir, "state-"+sfx),
		Provider: f.id, Region: kv["REGION"], IP: "127.0.0.2", MetricsEvery: time.Second, Version: "fake"}, drv)
	if err != nil {
		return provider.Handle{}, err
	}
	go ag.Run(f.ctx)
	return provider.Handle{Ref: sfx, IP: "127.0.0.2"}, nil
}
func (f *fakeCloud) Destroy(ctx context.Context, token, ref string) error {
	f.mu.Lock()
	f.destroyed = append(f.destroyed, ref)
	f.mu.Unlock()
	return nil
}
func (f *fakeCloud) Status(ctx context.Context, token, ref string) (string, error) {
	return "ready", nil
}

func TestSmartProviderManager(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dir := t.TempDir()
	broken := &fakeCloud{id: "fakea", price: 0.004, failWith: "HTTP 503: service unavailable (sold out in region)", dir: dir, ctx: ctx}
	good := &fakeCloud{id: "fakeb", price: 0.01, dir: dir, ctx: ctx}
	reg := provider.NewRegistry(&provider.Local{}, broken, good)
	e := setupReg(t, reg, func(c *manager.Config) {
		c.AutoBurst = true
		c.ProviderChain = []string{"fakea", "fakeb"}
		c.HealthInterval = time.Hour
		c.ClientLocation = "VN"
	}, 0)
	ag, _ := e.agentClient("router", 0)

	// connect wizard step 2: validate without saving
	var vr manager.ValidateResult
	e.must(e.admin, "POST", "/providers/fakeb/validate", map[string]any{"token": "nope"}, &vr)
	if vr.OK || vr.ErrorClass != "auth" {
		t.Fatalf("bad token must be classified auth: %+v", vr)
	}
	e.must(e.admin, "POST", "/providers/fakeb/validate", map[string]any{"token": "good"}, &vr)
	if !vr.OK || len(vr.Regions) != 2 || len(vr.Sizes) == 0 || vr.LatencyMS < 0 {
		t.Fatalf("validate: %+v", vr)
	}
	var ps model.List[model.Provider]
	e.must(e.admin, "GET", "/providers", nil, &ps)
	for _, p := range ps.Items {
		if p.ID == "fakeb" && p.Connected {
			t.Fatal("validate must not save the token")
		}
	}
	// step 3: save token + prefs
	for _, id := range []string{"fakea", "fakeb"} {
		e.must(e.admin, "PUT", "/providers/"+id+"/token", map[string]any{"token": "good"}, nil)
	}
	var cat manager.Catalog
	deadline := time.Now().Add(10 * time.Second)
	for {
		e.must(e.admin, "GET", "/catalog", nil, &cat)
		if cat.Total >= 4 || time.Now().After(deadline) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if cat.Total != 4 || len(cat.Sources) != 2 || cat.Items[0].Provider != "fakea" || cat.Items[0].Stale || cat.Items[0].Arch != "amd64" || !cat.Items[0].Usable {
		t.Fatalf("catalog: %+v", cat)
	}
	var one manager.Catalog
	e.must(e.admin, "GET", "/catalog?provider=fakeb&region=sin", nil, &one)
	if one.Total != 1 || one.Items[0].PriceSource != "fakeb-api" {
		t.Fatalf("catalog filter: %+v", one)
	}

	// recommend ranks the cheap provider first and explains it
	var rec manager.RecommendResult
	e.must(ag, "POST", "/recommend", map[string]any{"profile": "general", "requirements": map[string]any{"max_price_hour": 0.05}}, &rec)
	if rec.Pick == nil || rec.Pick.Kind != "burst" || rec.Pick.Provider != "fakea" || rec.Pick.Region != "sin" || len(rec.Pick.Reasons) == 0 || rec.Pick.Breakdown["latency"].Score <= rec.Options[len(rec.Options)-1].Breakdown["latency"].Score-0.001 && rec.Options[len(rec.Options)-1].Region == "sin" {
		t.Fatalf("recommend: %+v", rec.Pick)
	}

	// failover: fakea's create fails (sold out) → fakeb rents a host whose boxd dials in
	start := time.Now()
	st, raw, err := ag.Do(e.ctx, "POST", "/boxes", map[string]any{"profile": "general", "slug": "failover", "requirements": map[string]any{"cpu": 1, "ram_mb": 512, "disk_gb": 5}})
	if err != nil || st != 201 {
		t.Fatalf("create: %d %s %v", st, raw, err)
	}
	var b model.Box
	json.Unmarshal(raw, &b)
	if b.Status != model.StatusRunning || b.Provider != "fakeb" || b.Profile != "general" || b.Estimate == nil || b.Estimate.Kind != "burst" || b.Estimate.ActualSeconds <= 0 {
		t.Fatalf("failover box: %+v err=%s", b, b.Error)
	}
	t.Logf("failover create (fakea 503 → fakeb new server + boxd) took %s", time.Since(start).Round(time.Millisecond))
	var evs model.List[model.Event]
	e.must(ag, "GET", "/boxes/"+b.ID+"/events", nil, &evs)
	sawFailover := false
	for _, ev := range evs.Items {
		sawFailover = sawFailover || ev.Type == "box.burst_failed"
	}
	if !sawFailover {
		t.Fatal("expected a box.burst_failed failover event")
	}
	var ha manager.ProviderHealth
	e.must(e.admin, "GET", "/providers/fakea/health", nil, &ha)
	if ha.Stats.CreateAttempts != 1 || ha.Stats.CreateSuccessRate != 0 || ha.Stats.ErrorClasses["capacity"] != 1 || len(ha.Recent) == 0 {
		t.Fatalf("fakea health: %+v", ha.Stats)
	}
	var hb manager.ProviderHealth
	e.must(e.admin, "GET", "/providers/fakeb/health", nil, &hb)
	if hb.Stats.CreateSuccessRate != 1 || hb.Stats.ProvisionP50S <= 0 || hb.Hosts != 1 {
		t.Fatalf("fakeb health: %+v hosts=%d", hb.Stats, hb.Hosts)
	}

	// breaker: three failed probes open it; recommend explains the rejection; reset closes it
	broken.setValidErr("HTTP 502: bad gateway")
	for i := 0; i < 3; i++ {
		e.must(e.admin, "POST", "/providers/fakea/check", nil, &ha)
	}
	if ha.Usable || !strings.Contains(ha.Blocked, "circuit open") || ha.Stats.Breaker.State != "open" || ha.Stats.Status != "down" {
		t.Fatalf("breaker should be open: usable=%v blocked=%q %+v", ha.Usable, ha.Blocked, ha.Stats.Breaker)
	}
	rec = manager.RecommendResult{}
	e.must(ag, "POST", "/recommend", map[string]any{"profile": "general"}, &rec)
	found := false
	for _, r := range rec.Rejected {
		found = found || (r.Provider == "fakea" && strings.Contains(r.Reason, "circuit open"))
	}
	if !found || (rec.Pick != nil && rec.Pick.Provider == "fakea") {
		t.Fatalf("open breaker must reject fakea: %+v", rec.Rejected)
	}
	broken.setValidErr("")
	e.must(e.admin, "POST", "/providers/fakea/reset", nil, &ha)
	if !ha.Usable || ha.Stats.Breaker.State != "closed" {
		t.Fatalf("reset: %+v", ha.Stats.Breaker)
	}

	// prefs: priority reorders the chain; caps block offers with a reason
	var pp model.ProviderPrefs
	e.must(e.admin, "PUT", "/providers/fakeb/prefs", map[string]any{"priority": 1, "max_servers": 1, "max_hourly_usd": 0.5}, &pp)
	if pp.Priority != 1 || pp.MaxServers != 1 || !pp.Enabled {
		t.Fatalf("prefs %+v", pp)
	}
	e.must(e.admin, "GET", "/providers", nil, &ps)
	for _, p := range ps.Items {
		if p.ID == "fakeb" && p.ChainRank != 1 {
			t.Fatalf("priority 1 must put fakeb first in the chain: %+v", p)
		}
	}
	rec = manager.RecommendResult{}
	e.must(ag, "POST", "/recommend", map[string]any{"profile": "general"}, &rec)
	capped := false
	for _, r := range rec.Rejected {
		capped = capped || (r.Provider == "fakeb" && strings.Contains(r.Reason, "cap 1"))
	}
	if !capped {
		t.Fatalf("max_servers cap must reject fakeb: %+v", rec.Rejected)
	}
	if st, _, _ := e.admin.Do(e.ctx, "PUT", "/providers/fakeb/prefs", map[string]any{"priority": -1}); st != 400 {
		t.Fatalf("invalid prefs accepted: %d", st)
	}
	var all model.List[manager.ProviderHealth]
	e.must(e.admin, "GET", "/providers/health", nil, &all)
	if len(all.Items) != 3 {
		t.Fatalf("health list: %d", len(all.Items))
	}
	for _, row := range all.Items {
		if row.Provider.ID == "fakea" && (row.Score == nil || row.Score.Score <= 0 || len(row.Score.Breakdown) != 6) {
			t.Fatalf("fakea must have an explainable score: %+v", row.Score)
		}
		if row.Provider.ID == "fakeb" && (row.Score != nil || row.Usable != true) {
			t.Fatalf("fakeb is at its server cap: no score expected: %+v", row.Score)
		}
	}
	// agents cannot read provider internals
	if st, _, _ := ag.Do(e.ctx, "GET", "/providers/health", nil); st != 403 && st != 401 {
		t.Fatalf("agent read provider health: %d", st)
	}
}

func TestRecommendWarmPoolEnsureClone(t *testing.T) {
	t.Setenv("BOXD_LOCAL_CREATE_DELAY_MS", "800") // simulate a real container launch so warm vs cold is measurable
	e := setupWith(t, func(c *manager.Config) { c.WarmInterval = 200 * time.Millisecond; c.ClientLocation = "VN" }, 1)
	ag, _ := e.agentClient("speedy", 0)

	// public profiles
	st, raw, _ := client.New(e.srv.URL, "").Do(e.ctx, "GET", "/profiles", nil)
	var prof struct {
		Items []map[string]any `json:"items"`
		Kinds []map[string]any `json:"kinds"`
	}
	json.Unmarshal(raw, &prof)
	if st != 200 || len(prof.Items) != 7 || len(prof.Kinds) != 22 {
		t.Fatalf("profiles: %d %d %d", st, len(prof.Items), len(prof.Kinds))
	}

	var rec manager.RecommendResult
	e.must(ag, "POST", "/recommend", map[string]any{"profile": "coding-agent"}, &rec)
	if rec.Pick == nil || rec.Pick.Kind != "pool" || rec.Requirements.CPU != 2 || rec.Template != "python" || rec.Pick.LatencyMS != 5 || rec.BoxUp["profile"] != "coding-agent" {
		t.Fatalf("recommend coding-agent: %+v %+v", rec.Pick, rec.Requirements)
	}
	sandboxRejected := false
	for _, r := range rec.Rejected {
		sandboxRejected = sandboxRejected || r.Kind == "sandbox"
	}
	if !sandboxRejected {
		t.Fatal("T2 sandbox must be listed as rejected with a reason")
	}
	// untrusted code refuses containers on a host without VM support
	e.must(ag, "POST", "/recommend", map[string]any{"profile": "sandbox-untrusted"}, &rec)
	if rec.Pick != nil || len(rec.Rejected) == 0 {
		t.Fatalf("untrusted must not land in a plain container: %+v", rec.Pick)
	}

	// cold create (template not in the warm pool)
	cold := mustBox(t, e, ag, map[string]any{"template": "base", "slug": "cold-1", "spec": map[string]any{"cpu": 1, "ram_mb": 256, "disk_gb": 1}})
	if cold.Estimate == nil || cold.Estimate.WarmHit || cold.Estimate.Kind != "pool" {
		t.Fatalf("cold estimate %+v", cold.Estimate)
	}

	// warm pool
	var wv manager.WarmView
	e.must(e.admin, "PUT", "/warm-pool", map[string]any{"enabled": true, "pools": []map[string]any{{"template": "base", "spec": map[string]any{"cpu": 1, "ram_mb": 512, "disk_gb": 2}, "min": 2, "max": 3, "ttl_minutes": 30}}}, &wv)
	deadline := time.Now().Add(15 * time.Second)
	for wv.Ready["base"] < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("warm pool did not fill: %+v", wv)
		}
		time.Sleep(100 * time.Millisecond)
		e.must(e.admin, "GET", "/warm-pool", nil, &wv)
	}
	e.must(ag, "POST", "/recommend", map[string]any{"template": "base", "requirements": map[string]any{"cpu": 1, "ram_mb": 256, "disk_gb": 1}}, &rec)
	if rec.Pick == nil || rec.Pick.Kind != "warm" || rec.Pick.ETASeconds >= rec.Options[1].ETASeconds {
		t.Fatalf("warm option must win: %+v", rec.Options)
	}
	var coldS, warmS []float64
	coldIDs := []string{cold.ID}
	coldS = append(coldS, cold.Estimate.ActualSeconds)
	for i := 0; i < 2; i++ {
		c := mustBox(t, e, ag, map[string]any{"template": "python", "slug": fmt.Sprintf("cold-py-%d", i), "spec": map[string]any{"cpu": 1, "ram_mb": 256, "disk_gb": 1}})
		coldS = append(coldS, c.Estimate.ActualSeconds)
		coldIDs = append(coldIDs, c.ID)
	}
	var warm model.Box
	for i := 0; i < 2; i++ {
		w := mustBox(t, e, ag, map[string]any{"template": "base", "slug": fmt.Sprintf("warm-%d", i), "spec": map[string]any{"cpu": 1, "ram_mb": 256, "disk_gb": 1}})
		if w.Estimate == nil || !w.Estimate.WarmHit || w.Estimate.Kind != "warm" || !strings.Contains(w.PlacementReason, "warm pool hit") {
			t.Fatalf("warm hit expected: %+v %s", w.Estimate, w.PlacementReason)
		}
		warmS = append(warmS, w.Estimate.ActualSeconds)
		warm = w
	}
	t.Logf("BENCH create-to-running: cold %v s (avg %.3f) vs warm-pool %v s (avg %.3f) → %.1fx faster", coldS, avgF(coldS), warmS, avgF(warmS), avgF(coldS)/avgF(warmS))
	if avgF(warmS)*3 > avgF(coldS) {
		t.Fatalf("warm pool should be >3x faster: cold %v warm %v", coldS, warmS)
	}
	// the claimed container behaves like any box (id alias on the host)
	var ex model.ExecResult
	e.must(ag, "POST", "/boxes/"+warm.ID+"/exec", map[string]any{"command": "echo hi > x.txt && cat x.txt && test -f ../AGENTS.md && echo agentsmd"}, &ex)
	if ex.ExitCode != 0 || !strings.Contains(ex.StdoutTail, "hi") || !strings.Contains(ex.StdoutTail, "agentsmd") {
		t.Fatalf("exec in claimed box: %+v", ex)
	}
	e.must(ag, "POST", "/boxes/"+warm.ID+"/sleep", nil, nil)
	e.must(ag, "POST", "/boxes/"+warm.ID+"/wake", nil, nil)
	e.must(ag, "DELETE", "/boxes/"+warm.ID, nil, nil)
	var sp manager.SpeedView
	e.must(e.admin, "GET", "/speed", nil, &sp)
	if sp.Warm.Hits < 2 || sp.Placement != "binpack" || len(sp.Hosts) != 1 || sp.Hosts[0].CoW != "copy" {
		t.Fatalf("speed view: %+v", sp)
	}
	// refill back to min
	deadline = time.Now().Add(15 * time.Second)
	for {
		e.must(e.admin, "GET", "/warm-pool", nil, &wv)
		if wv.Ready["base"] >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("warm pool did not refill: %+v", wv)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if st, _, _ := e.admin.Do(e.ctx, "PUT", "/warm-pool", map[string]any{"enabled": true, "pools": []map[string]any{{"template": "nope", "min": 1}}}); st != 400 {
		t.Fatalf("unknown template accepted: %d", st)
	}

	// box_ensure: create, reuse, wake
	var en manager.EnsureResult
	st, raw, _ = ag.Do(e.ctx, "POST", "/boxes/ensure", map[string]any{"name": "repo-main", "profile": "general", "requirements": map[string]any{"ram_mb": 1024, "disk_gb": 1}})
	json.Unmarshal(raw, &en)
	if st != 201 || en.Action != "created" || !en.Created || en.Box.Slug != "repo-main" || en.Box.Profile != "general" {
		t.Fatalf("ensure create: %d %s", st, raw)
	}
	id := en.Box.ID
	e.must(ag, "POST", "/boxes/"+id+"/exec", map[string]any{"command": "true"}, nil)
	st, raw, _ = ag.Do(e.ctx, "POST", "/boxes/ensure", map[string]any{"name": "repo-main"})
	json.Unmarshal(raw, &en)
	if st != 200 || en.Action != "reused" || en.Box.ID != id || en.Resume == nil || len(en.Resume.RecentExecs) == 0 {
		t.Fatalf("ensure reuse: %d %s", st, raw)
	}
	e.must(ag, "POST", "/boxes/"+id+"/sleep", nil, nil)
	e.must(ag, "POST", "/boxes/ensure", map[string]any{"name": "repo-main"}, &en)
	if en.Action != "woken" || en.Box.Status != model.StatusRunning || en.Box.ID != id {
		t.Fatalf("ensure wake: %+v", en)
	}
	// concurrent ensure never creates two boxes
	var wg sync.WaitGroup
	ids := make([]string, 4)
	for i := range ids {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			var r manager.EnsureResult
			ag.JSON(e.ctx, "POST", "/boxes/ensure", map[string]any{"name": "parallel-one", "template": "base", "spec": map[string]any{"cpu": 0.5, "ram_mb": 128, "disk_gb": 1}}, &r)
			if r.Box != nil {
				ids[i] = r.Box.ID
			}
		}(i)
	}
	wg.Wait()
	for _, x := range ids {
		if x == "" || x != ids[0] {
			t.Fatalf("concurrent ensure created different boxes: %v", ids)
		}
	}

	// exec session: cwd + exports persist
	e.must(ag, "POST", "/boxes/"+id+"/exec", map[string]any{"command": "mkdir -p sub && cd sub && export FOO=bar", "session": true}, &ex)
	e.must(ag, "POST", "/boxes/"+id+"/exec", map[string]any{"command": "pwd; echo FOO=$FOO; exit 3", "session": true}, &ex)
	if ex.ExitCode != 3 || !strings.Contains(ex.StdoutTail, "/sub") || !strings.Contains(ex.StdoutTail, "FOO=bar") {
		t.Fatalf("session exec: %+v", ex)
	}
	for _, f := range ex.FilesChanged {
		if strings.Contains(f, "/.box/") {
			t.Fatalf("session state leaked into files_changed: %v", ex.FilesChanged)
		}
	}

	// structured observe diff
	var ov manager.ObserveView
	e.must(ag, "GET", "/boxes/"+id+"/observe", nil, &ov)
	if !ov.Diff.First || ov.Summary == "" {
		t.Fatalf("observe first: %+v", ov)
	}
	time.Sleep(150 * time.Millisecond) // file mtimes use the kernel's coarse clock
	e.must(ag, "PUT", "/boxes/"+id+"/files", map[string]any{"path": "/work/new.txt", "content": "x"}, nil)
	e.must(ag, "GET", "/boxes/"+id+"/observe", nil, &ov)
	if ov.Diff.First || !strings.Contains(ov.Summary, "file") {
		t.Fatalf("observe diff: %+v", ov)
	}

	// copy-on-write clone
	var cl model.Box
	st, raw, _ = ag.Do(e.ctx, "POST", "/boxes/"+id+"/clone", map[string]any{"slug": "repo-try"})
	json.Unmarshal(raw, &cl)
	if st != 201 || cl.Status != model.StatusRunning || cl.ClonedFrom != id || cl.Estimate == nil || cl.Estimate.Kind != "clone" || cl.HostID != en.Box.HostID {
		t.Fatalf("clone: %d %s", st, raw)
	}
	t.Logf("BENCH clone (copy) took %.3f s vs cold create avg %.3f s", cl.Estimate.ActualSeconds, avgF(coldS))
	var rd map[string]any
	e.must(ag, "GET", "/boxes/"+cl.ID+"/files?path=/work/new.txt", nil, &rd)
	if rd["content"] != "x" {
		t.Fatalf("clone content: %+v", rd)
	}

	// right-sizing from observed usage
	now := time.Now()
	for i := 0; i < 12; i++ {
		e.m.RecordBoxUsage(id, 3+float64(i%3), 60+float64(i), now.Add(time.Duration(i-12)*10*time.Second))
	}
	var rs manager.RightSize
	e.must(ag, "GET", "/boxes/"+id+"/rightsize", nil, &rs)
	if rs.Samples != 12 || rs.Suggestion != "downsize" || rs.SuggestedSpec == nil || rs.SuggestedSpec.RAMMB != 256 || len(rs.Reasons) == 0 {
		t.Fatalf("rightsize: %+v", rs)
	}
	var rsl model.List[manager.RightSize]
	e.must(e.admin, "GET", "/rightsize", nil, &rsl)
	if len(rsl.Items) == 0 {
		t.Fatal("rightsize list empty")
	}

	// agent-friendly errors carry next actions
	err := ag.JSON(e.ctx, "GET", "/boxes/does-not-exist", nil, nil)
	st, raw, _ = ag.Do(e.ctx, "GET", "/boxes/does-not-exist", nil)
	if err == nil || st != 404 || !strings.Contains(string(raw), `"next":[`) || !strings.Contains(string(raw), "box_ensure") {
		t.Fatalf("error next: %d %s", st, raw)
	}

	// free the host (CPU is overcommitted 4x but finite)
	for _, x := range append(coldIDs, cl.ID) {
		e.must(ag, "DELETE", "/boxes/"+x, nil, nil)
	}
	// MCP: box_recommend + box_ensure through /mcp
	call := func(name string, args map[string]any) map[string]any {
		body := map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}}
		bb, _ := json.Marshal(body)
		req, _ := http.NewRequestWithContext(e.ctx, "POST", e.srv.URL+"/mcp", strings.NewReader(string(bb)))
		req.Header.Set("Authorization", "Bearer "+ag.Key)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var r map[string]any
		raw, _ := io.ReadAll(resp.Body)
		json.Unmarshal(raw, &r)
		res, _ := r["result"].(map[string]any)
		if res == nil || res["isError"] == true {
			t.Fatalf("%s: %s", name, raw)
		}
		txt := res["content"].([]any)[0].(map[string]any)["text"].(string)
		var out map[string]any
		json.Unmarshal([]byte(txt), &out)
		return out
	}
	if out := call("box_recommend", map[string]any{"profile": "general", "max_price_hour": 1, "cpu": 0.25, "ram_mb": 128}); out["pick"] == nil {
		t.Fatalf("mcp recommend: %+v", out)
	}
	if out := call("box_ensure", map[string]any{"name": "repo-main"}); out["action"] != "reused" {
		t.Fatalf("mcp ensure: %+v", out)
	}
}

func avgF(v []float64) float64 {
	s := 0.0
	for _, x := range v {
		s += x
	}
	return s / float64(len(v))
}

func TestMesh(t *testing.T) {
	// disabled: graceful no-op
	e := setup(t)
	var mv manager.MeshView
	e.must(e.admin, "GET", "/mesh", nil, &mv)
	if mv.Enabled || mv.Hint == "" || mv.Hosts != 1 {
		t.Fatalf("disabled mesh: %+v", mv)
	}
	if st, _, _ := e.admin.Do(e.ctx, "POST", "/mesh/keys", map[string]any{}); st != 400 {
		t.Fatalf("keys while disabled: %d", st)
	}
	var cfg map[string]any
	e.must(e.admin, "GET", "/mesh/config", nil, &cfg)
	if !strings.Contains(cfg["config"].(string), "server_url: https://mesh.example.com") {
		t.Fatalf("config preview: %v", cfg["config"])
	}

	// enabled against a fake headscale
	var mu sync.Mutex
	var keyReqs []map[string]any
	users := map[string]bool{}
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer hs-key" {
			w.WriteHeader(401)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.URL.Path == "/api/v1/user" && r.Method == "GET":
			var l []map[string]string
			for u := range users {
				l = append(l, map[string]string{"name": u})
			}
			json.NewEncoder(w).Encode(map[string]any{"users": l})
		case r.URL.Path == "/api/v1/user" && r.Method == "POST":
			var b map[string]string
			json.NewDecoder(r.Body).Decode(&b)
			users[b["name"]] = true
			w.Write([]byte(`{"user":{}}`))
		case r.URL.Path == "/api/v1/preauthkey":
			var b map[string]any
			json.NewDecoder(r.Body).Decode(&b)
			keyReqs = append(keyReqs, b)
			json.NewEncoder(w).Encode(map[string]any{"preAuthKey": map[string]any{"id": "1", "key": fmt.Sprintf("hskey-%d", len(keyReqs)), "aclTags": b["aclTags"], "expiration": b["expiration"]}})
		case r.URL.Path == "/api/v1/node":
			w.Write([]byte(`{"nodes":[{"id":"1","name":"host-a","ipAddresses":["100.64.0.1"],"online":true,"user":{"name":"boxaz"}},{"id":"2","name":"host-b","online":false}]}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer hs.Close()
	e2 := setupWith(t, func(c *manager.Config) {
		c.Mesh = manager.MeshConfig{Enabled: true, HeadscaleURL: hs.URL, APIKey: "hs-key"}
	}, 1)
	ag, _ := e2.agentClient("meshy", 0)
	b := mustBox(t, e2, ag, map[string]any{"template": "base", "slug": "mesh-box", "ports": []int{8080}, "spec": map[string]any{"cpu": 1, "ram_mb": 256, "disk_gb": 1}, "egress": map[string]any{"allow": []string{"10.0.0.0/8:5432", "pypi.org"}}})
	mu.Lock()
	n := len(keyReqs)
	var first map[string]any
	if n > 0 {
		first = keyReqs[0]
	}
	mu.Unlock()
	if n == 0 || !users["boxaz"] || fmt.Sprint(first["aclTags"]) != "[tag:boxaz-host]" || first["reusable"] != false {
		t.Fatalf("host join key: %d %+v", n, first)
	}
	e2.must(e2.admin, "GET", "/mesh", nil, &mv)
	if !mv.Enabled || len(mv.Nodes) != 2 || mv.Online != 1 || mv.ACLRules < 4 || mv.Error != "" {
		t.Fatalf("mesh status: %+v", mv)
	}
	var acl struct {
		Policy struct {
			TagOwners map[string][]string `json:"tagOwners"`
			ACLs      []struct {
				Src []string `json:"src"`
				Dst []string `json:"dst"`
			} `json:"acls"`
		} `json:"policy"`
		Notes []string `json:"notes"`
	}
	e2.must(e2.admin, "GET", "/mesh/acl", nil, &acl)
	raw, _ := json.Marshal(acl)
	if !strings.Contains(string(raw), "10.0.0.0/8:5432") || !strings.Contains(string(raw), ":8080") || len(acl.Notes) != 1 || !strings.Contains(acl.Notes[0], "pypi.org") {
		t.Fatalf("acl: %s", raw)
	}
	var key manager.MeshKeyResult
	st, kraw, _ := e2.admin.Do(e2.ctx, "POST", "/mesh/keys", map[string]any{"box_id": b.ID, "ttl_minutes": 10})
	json.Unmarshal(kraw, &key)
	if st != 201 || !key.Ephemeral || len(key.Tags) != 2 || !strings.HasPrefix(key.Tags[1], "tag:box-") || !strings.Contains(key.Command, "--login-server="+hs.URL) {
		t.Fatalf("box key: %d %s", st, kraw)
	}
}
