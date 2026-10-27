package e2e

// v1.7: Boxaz gateway (stable URL, wake on request, hold + 503, probes,
// WebSocket/SSE pass-through), app supervisor (boxd + T3 script), scoped URL
// tokens, lifecycle capabilities.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"boxaz/core/internal/manager"
	"boxaz/core/internal/model"
	"boxaz/core/internal/sandbox"
)

func freePortE2E(t *testing.T) int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

type gwResp struct {
	status int
	body   string
	hdr    http.Header
}

func gwDo(t *testing.T, method, u string, hdr map[string]string) gwResp {
	t.Helper()
	req, _ := http.NewRequest(method, u, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	c := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, u, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return gwResp{resp.StatusCode, string(b), resp.Header}
}

func waitAppState(t *testing.T, e *env, id, want string, d time.Duration) model.AppView {
	t.Helper()
	deadline := time.Now().Add(d)
	var v model.AppView
	for {
		e.must(e.admin, "GET", "/boxes/"+id+"/app", nil, &v)
		if v.Status.State == want {
			return v
		}
		if time.Now().After(deadline) {
			t.Fatalf("app of %s never reached %s: %+v", id, want, v.Status)
		}
		time.Sleep(150 * time.Millisecond)
	}
}

func TestGatewayAppTokensLocal(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 required")
	}
	e := setup(t)
	ag, _ := e.agentClient("gw-agent", 0)
	appPort := freePortE2E(t)
	var b model.Box
	e.must(ag, "POST", "/boxes", map[string]any{"template": "base", "slug": "gw", "lifecycle": "session",
		"app": map[string]any{"start": "echo serving; exec python3 -m http.server $PORT --bind 127.0.0.1", "install": "echo hi > index.html", "port": appPort, "workdir": "/work/site", "ready": map[string]any{"http": "/"}}}, &b)
	waitStatus(t, e.m, b.ID, model.StatusRunning, 20*time.Second)
	if b.WakeOnRequest == nil || !*b.WakeOnRequest || b.App == nil || len(b.Ports) != 1 || b.Ports[0] != appPort {
		t.Fatalf("created box %+v", b)
	}
	v := waitAppState(t, e, b.ID, model.AppReadyState, 20*time.Second)
	if v.Status.Supervisor != "boxd" || !v.Status.Ready || v.Spec.Restart != "always" {
		t.Fatalf("app %+v", v)
	}
	// ports: stable gateway URL
	var pr manager.PortsResult
	e.must(ag, "GET", "/boxes/"+b.ID+"/ports", nil, &pr)
	gw := pr.Ports[0].GatewayURL
	if !strings.HasPrefix(gw, e.srv.URL+"/x/") || !strings.HasSuffix(gw, "-"+strconv.Itoa(appPort)+"/") || pr.Ports[0].Access != "public" {
		t.Fatalf("ports %+v", pr)
	}
	if r := gwDo(t, "GET", gw, nil); r.status != 200 || !strings.Contains(r.body, "hi") {
		t.Fatalf("gateway GET: %d %.200s", r.status, r.body)
	}

	// crash → supervisor restarts it
	exec.Command("bash", "-c", fmt.Sprintf("pkill -f 'http.server %d'", appPort)).Run()
	time.Sleep(400 * time.Millisecond)
	v = waitAppState(t, e, b.ID, model.AppReadyState, 20*time.Second)
	if v.Status.Restarts < 1 {
		t.Fatalf("restart not recorded %+v", v.Status)
	}
	var logs struct {
		Data   string `json:"data"`
		Offset int64  `json:"offset"`
	}
	e.must(ag, "GET", "/boxes/"+b.ID+"/app/logs?offset=0&max=65536", nil, &logs)
	if !strings.Contains(logs.Data, "serving") || !strings.Contains(logs.Data, "exited with code") || logs.Offset == 0 {
		t.Fatalf("logs %+v", logs)
	}
	// follow (SSE): first event arrives at once
	req, _ := http.NewRequest("GET", e.srv.URL+"/v1/boxes/"+b.ID+"/app/logs?follow=1", nil)
	req.Header.Set("Authorization", "Bearer bx_admin_e2e")
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("follow: %v %v", err, resp)
	}
	line, _ := bufio.NewReader(resp.Body).ReadString('\n')
	resp.Body.Close()
	if !strings.HasPrefix(line, "event: log") {
		t.Fatalf("follow first line %q", line)
	}

	// sleep → probes do not wake → a real request wakes it within the hold
	e.must(ag, "POST", "/boxes/"+b.ID+"/sleep", map[string]any{}, nil)
	if r := gwDo(t, "HEAD", gw, nil); r.status != 503 || r.hdr.Get("Retry-After") == "" {
		t.Fatalf("HEAD to sleeping box: %d %v", r.status, r.hdr)
	}
	if r := gwDo(t, "GET", gw, map[string]string{"User-Agent": "Googlebot/2.1"}); r.status != 503 {
		t.Fatalf("bot to sleeping box: %d", r.status)
	}
	if r := gwDo(t, "GET", gw+"healthz", nil); r.status != 503 {
		t.Fatalf("health probe to sleeping box: %d", r.status)
	}
	if cur, _ := e.m.Store.GetBox(e.ctx, b.ID); cur.Status != model.StatusSleeping {
		t.Fatalf("a probe woke the box: %s", cur.Status)
	}
	t0 := time.Now()
	r := gwDo(t, "GET", gw, map[string]string{"Accept": "text/html"})
	if r.status != 200 || !strings.Contains(r.body, "hi") {
		t.Fatalf("wake on request: %d %.300s", r.status, r.body)
	}
	t.Logf("sleeping → 200 through the gateway in %s", time.Since(t0).Round(time.Millisecond))

	// wake_on_request=false → 503 and the box stays asleep
	e.must(ag, "POST", "/boxes/"+b.ID+"/sleep", map[string]any{}, nil)
	off := false
	var pb model.Box
	e.must(ag, "PATCH", "/boxes/"+b.ID, map[string]any{"wake_on_request": off}, &pb)
	if pb.WakeOnRequest == nil || *pb.WakeOnRequest {
		t.Fatalf("patch %+v", pb.WakeOnRequest)
	}
	if r := gwDo(t, "GET", gw, nil); r.status != 503 || !strings.Contains(r.body, "BOX_SLEEPING") {
		t.Fatalf("wake off: %d %s", r.status, r.body)
	}
	if cur, _ := e.m.Store.GetBox(e.ctx, b.ID); cur.Status != model.StatusSleeping {
		t.Fatalf("woke with wake_on_request=false")
	}
	on := true
	e.must(ag, "PATCH", "/boxes/"+b.ID, map[string]any{"wake_on_request": on}, nil)

	// hold runs out while the app is still starting → 503 + Retry-After + auto-refresh page
	var s model.Settings
	e.must(e.admin, "GET", "/settings", nil, &s)
	s.Wake.HoldS, s.Wake.RetryAfterS = 1, 3
	e.must(e.admin, "PUT", "/settings", s, nil)
	e.must(ag, "PUT", "/boxes/"+b.ID+"/app", map[string]any{"start": "sleep 4; exec python3 -m http.server $PORT --bind 127.0.0.1", "port": appPort, "workdir": "/work/site"}, nil)
	r = gwDo(t, "GET", gw, map[string]string{"Accept": "text/html,application/xhtml+xml"})
	if r.status != 503 || r.hdr.Get("Retry-After") != "3" || !strings.Contains(r.body, `http-equiv="refresh"`) {
		t.Fatalf("hold timeout: %d %v %.300s", r.status, r.hdr, r.body)
	}
	r = gwDo(t, "GET", gw, nil)
	if r.status != 503 || !strings.Contains(r.body, `"retry_after_s":3`) {
		t.Fatalf("hold timeout json: %d %s", r.status, r.body)
	}
	s.Wake.HoldS = 0
	e.must(e.admin, "PUT", "/settings", s, nil)
	waitAppState(t, e, b.ID, model.AppReadyState, 20*time.Second)

	// URL tokens
	var tok model.URLToken
	e.must(ag, "POST", fmt.Sprintf("/boxes/%s/ports/%d/tokens", b.ID, appPort), map[string]any{"ttl_s": 600, "label": "review"}, &tok)
	if !strings.HasPrefix(tok.Token, "bxt_") || tok.URL != gw+"?token="+tok.Token || tok.Hash != "" {
		t.Fatalf("token %+v", tok)
	}
	if r := gwDo(t, "GET", gw, nil); r.status != 401 || r.hdr.Get("WWW-Authenticate") == "" {
		t.Fatalf("no token → %d", r.status)
	}
	if r := gwDo(t, "GET", gw+"?token=bxt_wrong", nil); r.status != 401 {
		t.Fatalf("bad token → %d", r.status)
	}
	r = gwDo(t, "GET", tok.URL+"&keep=1", nil)
	loc := r.hdr.Get("Location")
	if r.status != 302 || strings.Contains(loc, "bxt_") || !strings.Contains(loc, "keep=1") || !strings.Contains(r.hdr.Get("Set-Cookie"), "boxaz_ut=") || !strings.Contains(r.hdr.Get("Set-Cookie"), "HttpOnly") {
		t.Fatalf("query token exchange: %d %v", r.status, r.hdr)
	}
	if r := gwDo(t, "GET", gw, map[string]string{"Cookie": "boxaz_ut=" + tok.Token}); r.status != 200 {
		t.Fatalf("cookie → %d", r.status)
	}
	if r := gwDo(t, "GET", gw, map[string]string{"Authorization": "Bearer " + tok.Token}); r.status != 200 {
		t.Fatalf("bearer → %d", r.status)
	}
	if r := gwDo(t, "GET", gw, map[string]string{"X-Boxaz-Token": tok.Token}); r.status != 200 {
		t.Fatalf("x-boxaz-token → %d", r.status)
	}
	e.must(ag, "GET", "/boxes/"+b.ID+"/ports", nil, &pr)
	if p := pr.Ports[0]; p.Access != "token" || p.Kind != manager.KindGateway || p.URL != gw || p.Public {
		t.Fatalf("token port view %+v", p)
	}
	var tl manager.URLTokenList
	e.must(ag, "GET", fmt.Sprintf("/boxes/%s/ports/%d/tokens", b.ID, appPort), nil, &tl)
	if len(tl.Items) != 1 || tl.Items[0].Token != "" || tl.Items[0].Label != "review" || tl.Access != "token" {
		t.Fatalf("list %+v", tl)
	}
	e.must(ag, "DELETE", fmt.Sprintf("/boxes/%s/ports/%d/tokens/%s", b.ID, appPort, tok.ID), nil, nil)
	if r := gwDo(t, "GET", gw, map[string]string{"Authorization": "Bearer " + tok.Token}); r.status != 401 {
		t.Fatalf("revoked → %d", r.status)
	}
	if !e.m.CheckURLToken(e.ctx, &b, appPort, "bxt_nope") == false {
		t.Fatal("unreachable")
	}
	e.must(ag, "PUT", fmt.Sprintf("/boxes/%s/ports/%d", b.ID, appPort), map[string]any{"access": "public"}, nil)
	if r := gwDo(t, "GET", gw, nil); r.status != 200 {
		t.Fatalf("public again → %d", r.status)
	}
	// MCP: box_url_token + box_app_status
	mcpCall(t, e, "box_url_token", map[string]any{"box_id": b.ID, "port": appPort, "action": "list"})
	st := mcpCall(t, e, "box_app_status", map[string]any{"box_id": b.ID})
	if !strings.Contains(st, `"ready"`) {
		t.Fatalf("mcp app status %s", st)
	}

	// capabilities
	var caps struct {
		Items []manager.CapabilityRow `json:"items"`
	}
	e.must(ag, "GET", "/capabilities", nil, &caps)
	found := false
	for _, c := range caps.Items {
		if c.Provider == "local" {
			found = c.WakeViaGateway && c.InboundHTTP && !c.MemoryPause
		}
	}
	if !found {
		t.Fatalf("capabilities %+v", caps.Items)
	}
	if c := e.m.LifecycleFor("e2b"); !(c.WakeOnHTTPNative && c.MemoryPause && c.Fork && c.InboundHTTP && c.WakeViaGateway && c.MaxRuntimeS == 86400) {
		t.Fatalf("e2b caps %+v", c)
	}
	if c := e.m.LifecycleFor("prized"); c.InboundHTTP || c.WakeOnHTTPNative || !c.MemoryPause || !c.WakeViaGateway {
		t.Fatalf("prized caps %+v", c)
	}
	e.must(ag, "DELETE", "/boxes/"+b.ID, nil, nil)
}

func mcpCall(t *testing.T, e *env, tool string, args map[string]any) string {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": tool, "arguments": args}})
	req, _ := http.NewRequest("POST", e.srv.URL+"/mcp", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer bx_admin_e2e")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(b), `"isError":true`) {
		t.Fatalf("mcp %s: %s", tool, b)
	}
	return string(b)
}

// TestGatewayStreaming: WebSocket and SSE pass through the gateway unbuffered.
func TestGatewayStreaming(t *testing.T) {
	e := setup(t)
	ag, _ := e.agentClient("ws-agent", 0)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	release := make(chan struct{})
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ws":
			c, err := up.Upgrade(w, r, nil)
			if err != nil {
				return
			}
			defer c.Close()
			for {
				mt, m, err := c.ReadMessage()
				if err != nil {
					return
				}
				c.WriteMessage(mt, append([]byte("echo:"), m...))
			}
		case "/sse":
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "data: first\n\n")
			w.(http.Flusher).Flush()
			<-release // the second event only after the client saw the first
			fmt.Fprintf(w, "data: second\n\n")
			w.(http.Flusher).Flush()
		default:
			fmt.Fprintf(w, "auth=%q cookie=%q prefix=%s", r.Header.Get("Authorization"), r.Header.Get("Cookie"), r.Header.Get("X-Forwarded-Prefix"))
		}
	})}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	port := ln.Addr().(*net.TCPAddr).Port
	var b model.Box
	e.must(ag, "POST", "/boxes", map[string]any{"template": "base", "slug": "ws", "lifecycle": "session", "ports": []int{port}}, &b)
	waitStatus(t, e.m, b.ID, model.StatusRunning, 20*time.Second)
	cur, _ := e.m.Store.GetBox(e.ctx, b.ID)
	gw := e.m.GatewayURL(cur, port)

	// headers: the app's own Authorization survives, Boxaz cookies do not
	r := gwDo(t, "GET", gw+"h", map[string]string{"Authorization": "Bearer app-secret", "Cookie": "boxaz_key=K; boxaz_ut=bxt_x; mine=1"})
	if r.status != 200 || !strings.Contains(r.body, `auth="Bearer app-secret"`) || !strings.Contains(r.body, `cookie="mine=1"`) || !strings.Contains(r.body, "prefix=/x/") {
		t.Fatalf("headers: %d %s", r.status, r.body)
	}
	// WebSocket
	wsURL := "ws" + strings.TrimPrefix(gw, "http") + "ws"
	c, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("ws dial via gateway: %v", err)
	}
	for i := 0; i < 3; i++ {
		c.WriteMessage(websocket.TextMessage, []byte("ping"+strconv.Itoa(i)))
		_, m, err := c.ReadMessage()
		if err != nil || string(m) != "echo:ping"+strconv.Itoa(i) {
			t.Fatalf("ws echo %q %v", m, err)
		}
	}
	c.Close()
	// SSE: the first event arrives before the upstream finishes
	resp, err := http.Get(gw + "sse")
	if err != nil {
		t.Fatal(err)
	}
	rd := bufio.NewReader(resp.Body)
	got := make(chan string, 1)
	go func() { l, _ := rd.ReadString('\n'); got <- l }()
	select {
	case l := <-got:
		if l != "data: first\n" {
			t.Fatalf("sse first %q", l)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("SSE was buffered: first event not delivered")
	}
	close(release)
	rd.ReadString('\n')
	if l, _ := rd.ReadString('\n'); l != "data: second\n" {
		t.Fatalf("sse second %q", l)
	}
	resp.Body.Close()
	_ = url.Values{}
}

// TestAppSupervisorShimT3: the setsid supervisor script on an exec-only
// sandbox without previews, reached through the in-box ingress agent stream.
func TestAppSupervisorShimT3(t *testing.T) {
	for _, b := range []string{"bash", "curl", "python3", "setsid", "tar"} {
		if _, err := exec.LookPath(b); err != nil {
			t.Skip(b + " required")
		}
	}
	boxdURL := buildBoxd(t)
	dir := t.TempDir()
	fk := noPreviewSandbox{&deploySandbox{dir: dir, state: map[string]string{}}}
	reg := sandbox.DefaultRegistry(nil)
	reg.Add(sandbox.AsProvider(fk))
	e := setupReg(t, reg, func(c *manager.Config) {
		c.AutoSandbox, c.ProviderChain, c.HealthInterval = true, []string{"fakenoprev"}, time.Hour
		c.SandboxEmptyAfter = 300 * time.Millisecond
		c.SnapshotDir = filepath.Join(dir, "snaps")
		c.BoxdDownloadURL = boxdURL
		c.AssumePublicURL = true
	}, 0)
	ag, _ := e.agentClient("t3-agent", 0)
	e.must(e.admin, "PUT", "/providers/fakenoprev/token", map[string]any{"token": "k"}, nil)
	port := freePortE2E(t)
	var b model.Box
	e.must(ag, "POST", "/boxes", map[string]any{"template": "base", "slug": "t3app", "lifecycle": "session",
		"requirements": map[string]any{"cpu": 1, "ram_mb": 1024, "disk_gb": 5, "isolation": "sandbox"}}, &b)
	got := waitStatus(t, e.m, b.ID, model.StatusRunning, 20*time.Second)
	h, _ := e.m.Store.GetHost(e.ctx, got.HostID)
	home := filepath.Join(dir, h.ProviderRef)
	stopAll := func(home string) {
		exec.Command("bash", "-c", "HOME="+home+"; "+`D="$HOME/.boxd/app"; for f in sup.pid child.pid; do P=$(cat "$D/$f" 2>/dev/null); [ -n "$P" ] && kill -KILL -"$P" "$P" 2>/dev/null; done`).Run()
		killPidFile(filepath.Join(home, ".boxd", "ingress.pid"))
	}
	t.Cleanup(func() { stopAll(home) })

	var v model.AppView
	e.must(ag, "PUT", "/boxes/"+b.ID+"/app", map[string]any{"start": "exec python3 -m http.server $PORT --bind 127.0.0.1", "install": "echo t3 > index.html",
		"port": port, "workdir": "/work/site", "backoff_max_s": 1}, &v)
	if v.Status.Supervisor != "script" {
		t.Fatalf("set %+v", v)
	}
	v = waitAppState(t, e, b.ID, model.AppReadyState, 20*time.Second)
	gw := e.m.GatewayURL(got, port)
	t0 := time.Now()
	r := gwDo(t, "GET", gw+"index.html", nil)
	if r.status != 200 || !strings.Contains(r.body, "t3") {
		t.Fatalf("T3 gateway: %d %.200s", r.status, r.body)
	}
	t.Logf("first T3 gateway request (agent start + stream) %s", time.Since(t0).Round(time.Millisecond))
	// crash → restarted by the script
	syscall.Kill(v.Status.PID, syscall.SIGKILL)
	time.Sleep(700 * time.Millisecond)
	v2 := waitAppState(t, e, b.ID, model.AppReadyState, 20*time.Second)
	if v2.Status.Restarts < 1 || v2.Status.PID == v.Status.PID {
		t.Fatalf("T3 restart %+v", v2.Status)
	}
	// sleep; supervisor + agent die (cold resume); a request wakes, relaunches and serves
	e.must(ag, "POST", "/boxes/"+b.ID+"/sleep", map[string]any{}, nil)
	stopAll(home)
	time.Sleep(300 * time.Millisecond)
	t0 = time.Now()
	r = gwDo(t, "GET", gw+"index.html", nil)
	if r.status != 200 || !strings.Contains(r.body, "t3") {
		t.Fatalf("T3 wake on request: %d %.300s", r.status, r.body)
	}
	t.Logf("T3 cold wake → 200 in %s", time.Since(t0).Round(time.Millisecond))
	// restore → app restarted
	before := waitAppState(t, e, b.ID, model.AppReadyState, 20*time.Second)
	var sn model.Snapshot
	e.must(ag, "POST", "/boxes/"+b.ID+"/snapshot", map[string]any{}, &sn)
	time.Sleep(1100 * time.Millisecond) // started_at has 1 s resolution
	e.must(ag, "POST", "/boxes/"+b.ID+"/restore", map[string]any{"snapshot_id": sn.ID}, nil)
	deadline := time.Now().Add(20 * time.Second)
	for {
		var cur model.AppView
		e.must(ag, "GET", "/boxes/"+b.ID+"/app", nil, &cur)
		if cur.Status.Ready && cur.Status.StartedAt != nil && cur.Status.StartedAt.After(*before.Status.StartedAt) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("app not restarted after restore: %+v", cur.Status)
		}
		time.Sleep(200 * time.Millisecond)
	}
	// clone (snapshot → new sandbox): the clone's app starts once the port is free
	var cl model.Box
	e.must(ag, "POST", "/boxes/"+b.ID+"/clone", map[string]any{"slug": "t3clone"}, &cl)
	if cl.App == nil {
		t.Fatalf("clone lost the app spec")
	}
	ch, _ := e.m.Store.GetHost(e.ctx, cl.HostID)
	t.Cleanup(func() { stopAll(filepath.Join(dir, ch.ProviderRef)) })
	stopAll(home) // frees the shared localhost port (sandboxes share this host's network here)
	e.must(ag, "DELETE", "/boxes/"+b.ID, nil, nil)
	waitAppState(t, e, cl.ID, model.AppReadyState, 30*time.Second)
	if r := gwDo(t, "GET", e.m.GatewayURL(&cl, port)+"index.html", nil); r.status != 200 || !strings.Contains(r.body, "t3") {
		t.Fatalf("clone gateway: %d %.200s", r.status, r.body)
	}
	e.must(ag, "DELETE", "/boxes/"+cl.ID, nil, nil)
	waitNoSandboxes(t, fk.deploySandbox)
}
