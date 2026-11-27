package e2e

// Public-URL fallback for sandbox boxes whose provider has no preview host:
// provider preview → Boxmes ingress (in-box `boxd ingress` agent dialling core,
// served at /x/<key>-<port>/) → in-box quick tunnel → none. The fake provider
// runs each sandbox as a local directory (HOME points into it), the agent is
// the real boxd binary downloaded from a fake /dl server, the tunnel is a fake
// cloudflared that prints a trycloudflare URL.

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"boxmes/core/internal/manager"
	"boxmes/core/internal/model"
	"boxmes/core/internal/sandbox"
)

// noPreviewSandbox: a deploySandbox without ports/preview whose commands see
// HOME inside the sandbox directory.
type noPreviewSandbox struct{ *deploySandbox }

func (f noPreviewSandbox) Info() sandbox.Info {
	i := f.deploySandbox.Info()
	i.ID, i.Name = "fakenoprev", "Fake no preview"
	i.Caps.Ports = false
	return i
}
func (f noPreviewSandbox) Exec(ctx context.Context, c sandbox.Cred, id string, r sandbox.ExecRequest) (*sandbox.ExecResult, error) {
	f.mu.Lock()
	st := f.state[id]
	f.mu.Unlock()
	if st != sandbox.StateRunning {
		return nil, &sandbox.HTTPError{Status: 502, Body: "sandbox " + id + " is " + st}
	}
	home := filepath.Join(f.dir, id)
	cmd := exec.CommandContext(ctx, "bash", "-c", strings.ReplaceAll(r.Command, fakeWD, home))
	cmd.Env = append(os.Environ(), "HOME="+home)
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

// buildBoxd compiles the real boxd once and serves it like core's /dl.
func buildBoxd(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain required to build boxd")
	}
	dir := t.TempDir()
	out := filepath.Join(dir, "boxd-linux-"+runtime.GOARCH)
	cmd := exec.Command("go", "build", "-o", out, "../../cmd/boxd")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build boxd: %v %s", err, b)
	}
	dl := httptest.NewServer(http.StripPrefix("/dl/", http.FileServer(http.Dir(dir))))
	t.Cleanup(dl.Close)
	return dl.URL + "/dl/boxd"
}

// helloApp is the "app inside the sandbox" (127.0.0.1:<port>).
func helloApp(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/go" {
			http.Redirect(w, r, "/hello", http.StatusFound)
			return
		}
		fmt.Fprintf(w, "Hello World! path=%s prefix=%s cookie=%s", r.URL.Path, r.Header.Get("X-Forwarded-Prefix"), r.Header.Get("Cookie"))
	})}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return ln.Addr().(*net.TCPAddr).Port
}

func getBody(t *testing.T, u string, cookie string) (int, string, http.Header) {
	t.Helper()
	req, _ := http.NewRequest("GET", u, nil)
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	c := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Do(req)
	if err != nil {
		return 0, err.Error(), nil
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), resp.Header
}

func killPidFile(p string) {
	if b, err := os.ReadFile(p); err == nil {
		if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
			if pr, err := os.FindProcess(pid); err == nil {
				pr.Kill()
			}
		}
	}
}

func TestCoreLooksPublic(t *testing.T) {
	for u, want := range map[string]bool{
		"": false, "http://127.0.0.1:8080": false, "http://localhost:8080": false, "http://10.1.2.3": false, "http://192.168.1.5:80": false,
		"http://core.internal": false, "http://core": false, "ftp://x.example": false,
		"https://abc.trycloudflare.com": true, "https://core-1-2-3-4.sslip.io": true, "http://8.8.8.8:8080": true,
	} {
		if got, _ := manager.CoreLooksPublic(u); got != want {
			t.Errorf("CoreLooksPublic(%q) = %v, want %v", u, got, want)
		}
	}
	s := manager.TunnelScript(8000, 30)
	for _, w := range []string{"cloudflared-linux-$A", "--url http://127.0.0.1:8000", `"$HOME/.boxd"`, "trycloudflare", "URL="} {
		if !strings.Contains(s, w) {
			t.Errorf("tunnel script lacks %q", w)
		}
	}
	a := manager.IngressAgentScript("https://core.example/dl/boxd", "wss://core.example/v1/ingress/connect", "box_1", "tok'x")
	for _, w := range []string{"ingress --dial 'wss://core.example/v1/ingress/connect'", "--box 'box_1'", `'tok'"'"'x'`, "-linux-$A", "ingress.pid"} {
		if !strings.Contains(a, w) {
			t.Errorf("agent script lacks %q", w)
		}
	}
	// nothing provider-visible carries the product name
	for _, s := range []string{s, a} {
		if strings.Contains(strings.ToLower(s), "boxa") {
			t.Errorf("product name in an in-sandbox command: %s", s)
		}
	}
}

func TestPublicURLIngressFallback(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash required")
	}
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl required")
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
		c.AssumePublicURL = true // the httptest core is on 127.0.0.1
	}, 0)
	ag, _ := e.agentClient("pub-agent", 0)
	e.must(e.admin, "PUT", "/providers/fakenoprev/token", map[string]any{"token": "k"}, nil)
	port := helloApp(t)

	var b model.Box
	e.must(ag, "POST", "/boxes", map[string]any{"template": "base", "slug": "pub", "lifecycle": "session",
		"requirements": map[string]any{"cpu": 1, "ram_mb": 1024, "disk_gb": 5, "isolation": "sandbox"}}, &b)
	got := waitStatus(t, e.m, b.ID, model.StatusRunning, 20*time.Second)
	h, _ := e.m.Store.GetHost(e.ctx, got.HostID)
	home := filepath.Join(dir, h.ProviderRef)
	t.Cleanup(func() { killPidFile(filepath.Join(home, ".boxd", "ingress.pid")) })

	var pr manager.PortsResult
	start := time.Now()
	e.must(ag, "POST", "/boxes/"+b.ID+"/ports", map[string]any{"port": port}, &pr)
	v := pr.Ports[0]
	if v.Kind != manager.KindIngress || !v.Public || !strings.HasPrefix(v.URL, e.srv.URL+"/x/") || len(v.Caveats) < 2 {
		t.Fatalf("expose → %+v", pr)
	}
	t.Logf("ingress established in %s: %s", time.Since(start).Round(time.Millisecond), v.URL)
	if !strings.Contains(v.Caveats[0], "no public preview") {
		t.Fatalf("caveats %v", v.Caveats)
	}
	st, body, _ := getBody(t, v.URL+"hello?x=1", "boxmes_key=SECRET; app=1")
	if st != 200 || !strings.Contains(body, "Hello World! path=/hello") || !strings.Contains(body, "prefix=/x/") || strings.Contains(body, "SECRET") || !strings.Contains(body, "app=1") {
		t.Fatalf("GET via ingress: %d %q", st, body)
	}
	// redirects stay inside the prefix; the bare prefix gets its slash
	if st, _, hd := getBody(t, v.URL+"go", ""); st != 302 || !strings.HasPrefix(hd.Get("Location"), "/x/") {
		t.Fatalf("redirect %d %v", st, hd)
	}
	if st, _, _ := getBody(t, strings.TrimSuffix(v.URL, "/"), ""); st != 301 {
		t.Fatalf("bare prefix → %d", st)
	}
	if st, _, _ := getBody(t, e.srv.URL+"/x/deadbeefdeadbeefdead-"+strconv.Itoa(port)+"/", ""); st != 404 {
		t.Fatalf("unknown key → %d", st)
	}
	// the box URL is the first port's exposure; GET /ports is stable
	cur, _ := e.m.Store.GetBox(e.ctx, b.ID)
	if cur.URL != v.URL {
		t.Fatalf("box url %q", cur.URL)
	}
	e.must(ag, "GET", "/boxes/"+b.ID+"/ports", nil, &pr)
	if pr.Ports[0].URL != v.URL || pr.Ports[0].Kind != manager.KindIngress {
		t.Fatalf("GET ports %+v", pr)
	}

	// sleep → (agent dies, as if the sandbox lost its processes) → wake → URL works again
	e.must(ag, "POST", "/boxes/"+b.ID+"/sleep", map[string]any{}, nil)
	killPidFile(filepath.Join(home, ".boxd", "ingress.pid"))
	time.Sleep(300 * time.Millisecond)
	e.must(ag, "POST", "/boxes/"+b.ID+"/wake", map[string]any{}, nil)
	deadline := time.Now().Add(20 * time.Second)
	for {
		st, body, _ = getBody(t, v.URL+"again", "")
		if st == 200 && strings.Contains(body, "Hello World!") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("after wake: %d %q", st, body)
		}
		time.Sleep(300 * time.Millisecond)
	}
	// a request to a sleeping box wakes it
	e.must(ag, "POST", "/boxes/"+b.ID+"/sleep", map[string]any{}, nil)
	if st, body, _ = getBody(t, v.URL+"wake-me", ""); st != 200 {
		t.Fatalf("request to sleeping box: %d %q", st, body)
	}

	// box down → agent dropped, sandbox deleted
	e.must(ag, "DELETE", "/boxes/"+b.ID, nil, nil)
	if st, _, _ := getBody(t, v.URL, ""); st != 404 {
		t.Fatalf("after down → %d", st)
	}
	waitNoSandboxes(t, fk.deploySandbox)
}

func TestPublicURLTunnelFallback(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash required")
	}
	dir := t.TempDir()
	fk := noPreviewSandbox{&deploySandbox{dir: dir, state: map[string]string{}}}
	reg := sandbox.DefaultRegistry(nil)
	reg.Add(sandbox.AsProvider(fk))
	probed := make(chan string, 4)
	e := setupReg(t, reg, func(c *manager.Config) {
		c.AutoSandbox, c.ProviderChain, c.HealthInterval = true, []string{"fakenoprev"}, time.Hour
		c.SandboxEmptyAfter = 300 * time.Millisecond
		c.SnapshotDir = filepath.Join(dir, "snaps")
		c.PublicURLProbe = func(ctx context.Context, u string) bool { probed <- u; return true }
	}, 0)
	ag, _ := e.agentClient("tun-agent", 0)
	e.must(e.admin, "PUT", "/providers/fakenoprev/token", map[string]any{"token": "k"}, nil)
	var b model.Box
	e.must(ag, "POST", "/boxes", map[string]any{"template": "base", "slug": "tun", "lifecycle": "session",
		"requirements": map[string]any{"cpu": 1, "ram_mb": 1024, "disk_gb": 5, "isolation": "sandbox"}}, &b)
	got := waitStatus(t, e.m, b.ID, model.StatusRunning, 20*time.Second)
	h, _ := e.m.Store.GetHost(e.ctx, got.HostID)
	home := filepath.Join(dir, h.ProviderRef)
	// a fake cloudflared already in place (the script only downloads when missing)
	os.MkdirAll(filepath.Join(home, ".boxd"), 0o755)
	fake := "#!/usr/bin/env bash\necho \"INF |  https://fake-$RANDOM-tunnel.trycloudflare.com  |\"\nexec sleep 300\n"
	os.WriteFile(filepath.Join(home, ".boxd", "cloudflared"), []byte(fake), 0o755)
	t.Cleanup(func() {
		for _, p := range []string{"8000", "8001"} {
			killPidFile(filepath.Join(home, ".boxd", "tunnel-"+p+".pid"))
		}
	})

	// core is on 127.0.0.1 → ingress skipped → tunnel
	var pr manager.PortsResult
	e.must(ag, "POST", "/boxes/"+b.ID+"/ports", map[string]any{"port": 8000}, &pr)
	v := pr.Ports[0]
	if v.Kind != manager.KindTunnel || !v.Public || !strings.HasSuffix(v.URL, ".trycloudflare.com/") || !strings.Contains(strings.Join(v.Caveats, " "), "ingress skipped") {
		t.Fatalf("tunnel expose %+v", pr)
	}
	if u := <-probed; u != v.URL {
		t.Fatalf("probed %q", u)
	}
	// idempotent: the running tunnel is reused (same URL)
	e.must(ag, "POST", "/boxes/"+b.ID+"/ports", map[string]any{"port": 8000}, &pr)
	if pr.Ports[0].URL != v.URL {
		t.Fatalf("tunnel restarted: %q → %q", v.URL, pr.Ports[0].URL)
	}
	// tunnel died while asleep → wake re-creates it (new quick-tunnel URL)
	e.must(ag, "POST", "/boxes/"+b.ID+"/sleep", map[string]any{}, nil)
	killPidFile(filepath.Join(home, ".boxd", "tunnel-8000.pid"))
	e.must(ag, "POST", "/boxes/"+b.ID+"/wake", map[string]any{}, nil)
	deadline := time.Now().Add(15 * time.Second)
	for {
		cur, _ := e.m.Store.GetBox(e.ctx, b.ID)
		if ex := cur.ExposureFor(8000); ex != nil && ex.URL != v.URL && ex.Kind == manager.KindTunnel {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("tunnel not re-established after wake: %+v", cur.Exposures)
		}
		time.Sleep(200 * time.Millisecond)
	}

	// tunnel mode disabled by setting → none, with the reasons
	var s model.Settings
	e.must(e.admin, "GET", "/settings", nil, &s)
	s.DisableTunnel = true
	e.must(e.admin, "PUT", "/settings", s, nil)
	e.must(ag, "POST", "/boxes/"+b.ID+"/ports", map[string]any{"port": 8001}, &pr)
	v = pr.Ports[0]
	if v.Kind != manager.KindNone || v.URL != "" || v.Public || !strings.Contains(strings.Join(v.Caveats, " "), "disabled") {
		t.Fatalf("disabled tunnel → %+v", pr)
	}
	e.must(ag, "DELETE", "/boxes/"+b.ID, nil, nil)
	waitNoSandboxes(t, fk.deploySandbox)
}

func waitNoSandboxes(t *testing.T, fk *deploySandbox) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		fk.mu.Lock()
		left := len(fk.state)
		fk.mu.Unlock()
		if left == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d sandboxes left: %v", left, fk.calls)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
