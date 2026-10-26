package e2e

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"boxa/core/internal/client"
	"boxa/core/internal/manager"
	"boxa/core/internal/model"
	"boxa/core/internal/objstore"
	"boxa/core/internal/store"
)

func mustBox(t *testing.T, e *env, c *client.Client, body map[string]any) model.Box {
	t.Helper()
	var b model.Box
	st, raw, err := c.Do(e.ctx, "POST", "/boxes", body)
	if err != nil || st != 201 {
		t.Fatalf("create box: %d %s %v", st, raw, err)
	}
	json.Unmarshal(raw, &b)
	if b.Status != model.StatusRunning {
		t.Fatalf("box not running: %+v", b)
	}
	return b
}

func TestSnapshotsInObjectStorage(t *testing.T) {
	fake := objstore.NewFake("AKID", "SECRET", "auto")
	s3 := httptest.NewServer(fake)
	defer s3.Close()
	e := setupWith(t, nil, 2)
	ag, _ := e.agentClient("snapper", 0)

	// local fallback before configuration
	var sv manager.StorageView
	e.must(e.admin, "GET", "/storage", nil, &sv)
	if sv.Configured || sv.Provider != "local" {
		t.Fatalf("storage should start local: %+v", sv)
	}
	var tr manager.StorageTestResult
	e.must(e.admin, "POST", "/storage/test", nil, &tr)
	if tr.OK {
		t.Fatal("test must fail when unconfigured")
	}
	if st, _, _ := e.admin.Do(e.ctx, "PUT", "/storage", map[string]any{"provider": "r2", "endpoint": s3.URL, "bucket": "snaps", "access_key_id": "AKID"}); st != 400 {
		t.Fatalf("missing secret should be rejected: %d", st)
	}
	e.must(e.admin, "PUT", "/storage", map[string]any{"provider": "r2", "endpoint": s3.URL, "bucket": "snaps", "prefix": "boxa", "access_key_id": "AKID", "secret_access_key": "SECRET"}, &sv)
	if !sv.Configured || sv.Source != "settings" || !sv.HasSecret || sv.Region != "auto" {
		t.Fatalf("storage view %+v", sv)
	}
	raw, _ := json.Marshal(sv)
	if strings.Contains(string(raw), "SECRET") {
		t.Fatal("secret leaked in GET /storage")
	}
	e.must(e.admin, "POST", "/storage/test", nil, &tr)
	if !tr.OK {
		t.Fatalf("storage test: %+v", tr)
	}
	// system secret is hidden from agents and reserved
	var secs model.List[model.Secret]
	e.must(ag, "GET", "/secrets", nil, &secs)
	for _, s := range secs.Items {
		if strings.HasPrefix(s.Name, "BOXA_SYS_") {
			t.Fatal("agent sees system secret")
		}
	}
	if st, _, _ := e.admin.Do(e.ctx, "POST", "/secrets", map[string]any{"name": "BOXA_SYS_X", "value": "v"}); st != 400 {
		t.Fatalf("reserved secret name accepted: %d", st)
	}

	a := mustBox(t, e, ag, map[string]any{"template": "base", "slug": "snap-a", "spec": map[string]any{"cpu": 1, "ram_mb": 256, "disk_gb": 1}})
	e.must(ag, "PUT", "/boxes/"+a.ID+"/files", map[string]any{"path": "/work/hello.txt", "content": "từ snapshot"}, nil)
	var sn model.Snapshot
	e.must(ag, "POST", "/boxes/"+a.ID+"/snapshot", nil, &sn)
	if sn.Storage != "s3" || !strings.HasPrefix(sn.RemoteURL, "s3://snaps/boxa/snapshots/"+a.ID+"/") || sn.UploadError != "" {
		t.Fatalf("snapshot %+v", sn)
	}
	key := strings.TrimPrefix(sn.RemoteURL, "s3://")
	if b, ok := fake.Get(key); !ok || len(b) < 20 {
		t.Fatalf("object %s not uploaded (%d bytes)", key, len(b))
	}

	// fork onto the OTHER host: drain the snapshot host first
	e.must(e.admin, "POST", "/hosts/"+a.HostID+"/drain", nil, nil)
	var fork model.Box
	st, body, _ := ag.Do(e.ctx, "POST", "/boxes/"+a.ID+"/fork", map[string]any{"snapshot_id": sn.ID, "slug": "snap-fork"})
	json.Unmarshal(body, &fork)
	if st != 201 || fork.HostID == a.HostID || fork.Status != model.StatusRunning {
		t.Fatalf("cross-host fork: %d %s", st, body)
	}
	var fr struct {
		Content string `json:"content"`
	}
	e.must(ag, "GET", "/boxes/"+fork.ID+"/files?path=/work/hello.txt", nil, &fr)
	if !strings.Contains(fr.Content, "từ snapshot") {
		t.Fatalf("forked content %q", fr.Content)
	}
	// restore A's snapshot into the fork (snapshot host differs → download)
	e.must(ag, "PUT", "/boxes/"+fork.ID+"/files", map[string]any{"path": "/work/hello.txt", "content": "changed"}, nil)
	e.must(ag, "POST", "/boxes/"+fork.ID+"/restore", map[string]any{"snapshot_id": sn.ID}, nil)
	e.must(ag, "GET", "/boxes/"+fork.ID+"/files?path=/work/hello.txt", nil, &fr)
	if !strings.Contains(fr.Content, "từ snapshot") {
		t.Fatalf("cross-host restore content %q", fr.Content)
	}

	// retention: keep 1 snapshot per box → the older object is deleted
	var set model.Settings
	e.must(e.admin, "GET", "/settings", nil, &set)
	set.Reaper.KeepSnapshots = 1
	e.must(e.admin, "PUT", "/settings", set, &set)
	var sn2 model.Snapshot
	e.must(ag, "POST", "/boxes/"+fork.ID+"/snapshot", nil, &sn2)
	var sn3 model.Snapshot
	e.must(ag, "POST", "/boxes/"+fork.ID+"/snapshot", nil, &sn3)
	if _, ok := fake.Get(strings.TrimPrefix(sn2.RemoteURL, "s3://")); ok {
		t.Fatal("retention did not delete the old object")
	}
	if _, ok := fake.Get(strings.TrimPrefix(sn3.RemoteURL, "s3://")); !ok {
		t.Fatal("newest object missing")
	}
	var snaps model.List[model.Snapshot]
	e.must(ag, "GET", "/boxes/"+fork.ID+"/snapshots", nil, &snaps)
	if len(snaps.Items) != 1 || snaps.Items[0].ID != sn3.ID {
		t.Fatalf("snapshot records after retention: %+v", snaps.Items)
	}
}

func TestEgressPolicy(t *testing.T) {
	e := setup(t)
	ag, agentID := e.agentClient("egress", 0)
	b := mustBox(t, e, ag, map[string]any{"template": "base", "slug": "eg", "spec": map[string]any{"cpu": 1, "ram_mb": 256, "disk_gb": 1},
		"egress": map[string]any{"allow": []string{"PyPI.org", "*.github.com", "10.0.0.0/8"}, "deny_by_default": true}})
	if b.Egress == nil || b.Egress.Enforced || b.Egress.Mode != "none" || !strings.Contains(b.Egress.Warning, "NOT enforced") || strings.Join(b.Egress.Allow, ",") != "*.github.com,10.0.0.0/8,pypi.org" {
		t.Fatalf("egress %+v", b.Egress)
	}
	var res model.ExecResult
	e.must(ag, "POST", "/boxes/"+b.ID+"/exec", map[string]any{"command": "echo $BOXA_EGRESS_ALLOW"}, &res)
	if !strings.Contains(res.StdoutTail, "pypi.org") {
		t.Fatalf("env not exported: %q", res.StdoutTail)
	}
	var y struct {
		YAML string `json:"yaml"`
	}
	// invalid entries are rejected with a hint
	st, raw, _ := ag.Do(e.ctx, "POST", "/boxes", map[string]any{"template": "base", "egress": map[string]any{"allow": []string{"not a host"}}})
	if st != 400 || !strings.Contains(string(raw), "INVALID") {
		t.Fatalf("bad egress: %d %s", st, raw)
	}
	// box.yaml egress (list form)
	yb := mustBox(t, e, ag, map[string]any{"yaml": "name: eg-yaml\ntemplate: base\nspec: { cpu: 1, ram: 256M, disk: 1G }\negress:\n  - api.openai.com:443\n"})
	if yb.Egress == nil || yb.Egress.Allow[0] != "api.openai.com:443" || yb.Egress.Source != "box" {
		t.Fatalf("yaml egress %+v", yb.Egress)
	}
	e.must(ag, "GET", "/boxes/"+yb.ID+"/yaml", nil, &y)
	if !strings.Contains(y.YAML, "api.openai.com:443") {
		t.Fatalf("yaml %s", y.YAML)
	}
	// clear → unrestricted
	var cleared model.Box
	e.must(ag, "PUT", "/boxes/"+b.ID+"/egress", map[string]any{"allow": []string{}}, &cleared)
	if cleared.Egress != nil {
		t.Fatalf("cleared egress %+v", cleared.Egress)
	}
	// agent ceiling: box entries must stay inside it; default = agent list
	e.must(e.admin, "PATCH", "/agents/"+agentID, map[string]any{"policy": map[string]any{"egress_allowlist": []string{"*.github.com", "pypi.org"}}}, nil)
	st, raw, _ = ag.Do(e.ctx, "POST", "/boxes", map[string]any{"template": "base", "egress": map[string]any{"allow": []string{"evil.example.com"}}})
	if st != 403 || !strings.Contains(string(raw), "POLICY_DENIED") {
		t.Fatalf("ceiling: %d %s", st, raw)
	}
	d := mustBox(t, e, ag, map[string]any{"template": "base", "slug": "eg-default", "spec": map[string]any{"cpu": 1, "ram_mb": 256, "disk_gb": 1}})
	if d.Egress == nil || d.Egress.Source != "agent" || !d.Egress.DenyByDefault || len(d.Egress.Allow) != 2 {
		t.Fatalf("agent default egress %+v", d.Egress)
	}
}

func TestNotifications(t *testing.T) {
	var mu sync.Mutex
	var msgs []string
	tg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		if r.URL.Path != "/botBOT123/sendMessage" || b["chat_id"] != "-100" {
			w.WriteHeader(401)
			w.Write([]byte(`{"ok":false,"description":"Unauthorized"}`))
			return
		}
		mu.Lock()
		msgs = append(msgs, b["text"].(string))
		mu.Unlock()
		w.Write([]byte(`{"ok":true}`))
	}))
	defer tg.Close()
	e := setupWith(t, func(c *manager.Config) { c.TelegramAPI = tg.URL }, 1)
	var v manager.NotifyView
	e.must(e.admin, "GET", "/notify", nil, &v)
	if v.TelegramConfigured || len(v.EventsAvailable) != 6 {
		t.Fatalf("initial notify %+v", v)
	}
	if st, _, _ := e.admin.Do(e.ctx, "PUT", "/notify", map[string]any{"events": []string{"nope"}}); st != 400 {
		t.Fatal("unknown event accepted")
	}
	e.must(e.admin, "PUT", "/notify", map[string]any{"telegram_chat_id": "-100", "telegram_bot_token": "BOT123", "email": "anh@example.com",
		"events": []string{"daily_report", "emergency_stop", "budget"}, "daily_report_hour": 8, "timezone": "Asia/Ho_Chi_Minh",
		"smtp": map[string]any{"host": "", "tls": "starttls"}}, &v)
	if !v.TelegramConfigured || !v.HasTelegramToken || v.EmailConfigured || v.Timezone != "Asia/Ho_Chi_Minh" {
		t.Fatalf("notify view %+v", v)
	}
	raw, _ := json.Marshal(v)
	if strings.Contains(string(raw), "BOT123") {
		t.Fatal("bot token leaked")
	}
	var tr manager.NotifyTestResult
	e.must(e.admin, "POST", "/notify/test", map[string]any{"channel": "all"}, &tr)
	if len(tr.Results) != 2 || !tr.Results[0].OK || tr.Results[1].OK || !strings.Contains(tr.Results[1].Error, "not configured") {
		t.Fatalf("test results %+v", tr.Results)
	}
	e.must(e.admin, "POST", "/notify/test", map[string]any{"channel": "telegram", "kind": "daily_report"}, &tr)
	if !tr.Results[0].OK {
		t.Fatalf("daily report %+v", tr)
	}
	// settings PUT without smtp/timezone keeps them
	var set model.Settings
	e.must(e.admin, "GET", "/settings", nil, &set)
	set.Notify.Timezone = ""
	e.must(e.admin, "PUT", "/settings", set, &set)
	if set.Notify.Timezone != "Asia/Ho_Chi_Minh" {
		t.Fatalf("timezone lost: %+v", set.Notify)
	}
	// emergency stop alert (async)
	e.must(e.admin, "POST", "/emergency-stop", map[string]any{"confirm": "DUNG"}, nil)
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		n := len(msgs)
		last := ""
		if n > 0 {
			last = msgs[n-1]
		}
		mu.Unlock()
		if n >= 3 && strings.Contains(last, "Dừng khẩn cấp") {
			if !strings.Contains(msgs[1], "Báo cáo chi phí ngày") {
				t.Fatalf("daily report text %q", msgs[1])
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("telegram messages %v", msgs)
		}
		time.Sleep(50 * time.Millisecond)
	}
	e.must(e.admin, "GET", "/notify", nil, &v)
	if len(v.Recent) < 3 {
		t.Fatalf("recent deliveries %+v", v.Recent)
	}
}

func rpc(t *testing.T, e *env, key string, method string, params any) map[string]any {
	t.Helper()
	var out map[string]any
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 7, "method": method, "params": params})
	req, _ := http.NewRequestWithContext(e.ctx, "POST", e.srv.URL+"/a2a", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("a2a %s: %v", method, err)
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || resp.StatusCode != 200 {
		t.Fatalf("a2a %s: %d %v", method, resp.StatusCode, err)
	}
	return out
}

func TestA2ATasks(t *testing.T) {
	e := setup(t)
	ag, _ := e.agentClient("a2a", 0)
	b := mustBox(t, e, ag, map[string]any{"template": "base", "slug": "a2a", "spec": map[string]any{"cpu": 1, "ram_mb": 256, "disk_gb": 1}})
	key := ag.Key

	out := rpc(t, e, key, "message/send", map[string]any{"message": map[string]any{"role": "user", "messageId": "m1",
		"parts": []any{map[string]any{"kind": "text", "text": "echo xin-chao-a2a"}}}, "metadata": map[string]any{"box_id": b.ID}})
	task := out["result"].(map[string]any)
	if task["kind"] != "task" || task["status"].(map[string]any)["state"] != "completed" || !strings.HasPrefix(task["id"].(string), "job_") {
		t.Fatalf("send: %+v", out)
	}
	arts, _ := json.Marshal(task["artifacts"])
	if !strings.Contains(string(arts), "xin-chao-a2a") {
		t.Fatalf("artifacts %s", arts)
	}
	got := rpc(t, e, key, "tasks/get", map[string]any{"id": task["id"]})["result"].(map[string]any)
	if got["id"] != task["id"] || got["status"].(map[string]any)["state"] != "completed" {
		t.Fatalf("get %+v", got)
	}
	// legacy name + tool call via data part
	out = rpc(t, e, key, "tasks/send", map[string]any{"id": "x", "message": map[string]any{"role": "user", "parts": []any{map[string]any{"type": "data", "data": map[string]any{"tool": "box_list", "arguments": map[string]any{}}}}}})
	task = out["result"].(map[string]any)
	if task["status"].(map[string]any)["state"] != "completed" || !strings.Contains(string(mustJSON(task["artifacts"])), b.ID) {
		t.Fatalf("tool task %+v", out)
	}
	// cancel a long command
	out = rpc(t, e, key, "message/send", map[string]any{"configuration": map[string]any{"blocking": false}, "message": map[string]any{"role": "user",
		"parts": []any{map[string]any{"kind": "data", "data": map[string]any{"box_id": b.ID, "command": "sleep 30"}}}}})
	long := out["result"].(map[string]any)
	if st := long["status"].(map[string]any)["state"]; st != "submitted" && st != "working" {
		t.Fatalf("long task state %v", st)
	}
	time.Sleep(300 * time.Millisecond)
	start := time.Now()
	can := rpc(t, e, key, "tasks/cancel", map[string]any{"id": long["id"]})["result"].(map[string]any)
	if can["status"].(map[string]any)["state"] != "canceled" {
		t.Fatalf("cancel %+v", can)
	}
	var j model.Job
	e.must(ag, "GET", "/jobs/"+long["id"].(string)+"?wait=10", nil, &j)
	if j.Status != "canceled" || time.Since(start) > 8*time.Second {
		t.Fatalf("job after cancel %+v (%s)", j, time.Since(start))
	}
	if e2 := rpc(t, e, key, "tasks/cancel", map[string]any{"id": long["id"]})["error"]; e2 == nil {
		t.Fatal("second cancel should be not cancelable")
	}
	// streaming
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": "s1", "method": "message/stream", "params": map[string]any{
		"message": map[string]any{"role": "user", "parts": []any{map[string]any{"kind": "text", "text": "sleep 0.5; echo streamed"}}, "metadata": map[string]any{"box_id": b.ID}}}})
	req, _ := http.NewRequestWithContext(e.ctx, "POST", e.srv.URL+"/a2a", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("stream %v %v", resp, err)
	}
	var events []map[string]any
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		if line := sc.Text(); strings.HasPrefix(line, "data: ") {
			var ev map[string]any
			json.Unmarshal([]byte(line[6:]), &ev)
			events = append(events, ev["result"].(map[string]any))
		}
	}
	resp.Body.Close()
	if len(events) < 3 || events[0]["kind"] != "task" {
		t.Fatalf("stream events %+v", events)
	}
	last := events[len(events)-1]
	if last["kind"] != "status-update" || last["final"] != true || last["status"].(map[string]any)["state"] != "completed" {
		t.Fatalf("last event %+v", last)
	}
	if !strings.Contains(string(mustJSON(events)), "streamed") {
		t.Fatal("artifact-update with output missing")
	}
	// errors
	if rpc(t, e, key, "nope/x", nil)["error"].(map[string]any)["code"].(float64) != -32601 {
		t.Fatal("method not found")
	}
	if rpc(t, e, key, "tasks/get", map[string]any{"id": "job_missing"})["error"].(map[string]any)["code"].(float64) != -32001 {
		t.Fatal("task not found")
	}
	other, _ := e.agentClient("other", 0)
	if rpc(t, e, other.Key, "tasks/get", map[string]any{"id": task["id"]})["error"] == nil {
		t.Fatal("other agent must not see the task")
	}
	if rpc(t, e, key, "message/send", map[string]any{"message": map[string]any{"role": "user", "parts": []any{map[string]any{"kind": "text", "text": "ls"}}}})["error"] == nil {
		t.Fatal("command without box_id must fail")
	}
}

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }

func TestProvidersAndChain(t *testing.T) {
	e := setup(t)
	var ps model.List[model.Provider]
	e.must(e.admin, "GET", "/providers", nil, &ps)
	seen := map[string]model.Provider{}
	for _, p := range ps.Items {
		seen[p.ID] = p
	}
	for i, id := range []string{"hetzner", "vultr", "digitalocean", "contabo", "netcup"} {
		p, ok := seen[id]
		if !ok || !p.Implemented || p.ChainRank != i+1 {
			t.Fatalf("provider %s: %+v", id, p)
		}
	}
	var rs model.List[map[string]any]
	e.must(e.admin, "GET", "/providers/contabo/regions", nil, &rs)
	if len(rs.Items) != 9 {
		t.Fatalf("contabo regions %d", len(rs.Items))
	}
	if st, _, _ := e.admin.Do(e.ctx, "GET", "/providers/digitalocean/servers", nil); st < 400 {
		t.Fatal("servers without token must fail")
	}
	// env token config (no network: validation runs in the background and fails harmlessly)
	ids := e.m.ConfigureProvidersFromEnv(e.ctx, func(k string) string { return map[string]string{"NETCUP_REFRESH_TOKEN": "rt"}[k] })
	if len(ids) != 1 || ids[0] != "netcup" {
		t.Fatalf("env providers %v", ids)
	}

	// Fallback chain: with vultr + digitalocean connected, vultr wins even
	// though DO is cheaper; excluding vultr (burst failure) falls to DO.
	ctx := context.Background()
	now := time.Now().UTC()
	big := model.Offer{CPU: 16, RAMMB: 262144, DiskGB: 200, MinBillingMinutes: 60, Source: "test", FetchedAt: now, Confidence: "verified"}
	for _, o := range []struct {
		prov  string
		price float64
	}{{"hetzner", 0.10}, {"vultr", 0.30}, {"digitalocean", 0.20}, {"contabo", 0.05}} {
		x := big
		x.Provider, x.SKU, x.Region, x.PriceHour, x.PriceMonth = o.prov, o.prov+"-xl", "r1", o.price, o.price*730
		e.m.Store.ReplaceOffers(ctx, o.prov, "test", []model.Offer{x})
	}
	for _, id := range []string{"vultr", "digitalocean"} {
		enc, _ := e.m.Crypt.Seal("tok")
		p, _ := e.m.Store.GetProvider(ctx, id)
		p.TokenEnc, p.Connected = enc, true
		e.m.Store.PutProvider(ctx, p)
	}
	e.m.Cfg.AutoBurst = true
	agent := &model.Agent{ID: "ag"}
	box := &model.Box{ID: "box_chain", Lifecycle: model.LifecycleTask, TTLSeconds: 3600, Spec: model.Spec{CPU: 12, RAMMB: 250000, DiskGB: 50}}
	pl, err := e.m.Schedule(ctx, box, agent, map[string]bool{})
	if err != nil || pl.Offer == nil || pl.Offer.Provider != "vultr" || !strings.Contains(pl.Reason, "chain #2") {
		t.Fatalf("chain pick: %+v %v", pl, err)
	}
	pl, err = e.m.Schedule(ctx, box, agent, map[string]bool{"provider:vultr": true})
	if err != nil || pl.Offer.Provider != "digitalocean" {
		t.Fatalf("fallback pick: %+v %v", pl, err)
	}
	_, err = e.m.Schedule(ctx, box, agent, map[string]bool{"provider:vultr": true, "provider:digitalocean": true})
	if err == nil || !strings.Contains(err.Error(), "contabo") { // nothing connected left → NO_CAPACITY naming the cheapest
		t.Fatalf("exhausted chain: %v", err)
	}
	e.m.Cfg.AutoBurst = false
	_ = store.BoxFilter{}
}
