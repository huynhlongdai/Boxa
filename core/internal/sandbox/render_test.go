package sandbox

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"boxmes/core/internal/model"
	"boxmes/core/internal/provider"
)

// renderMock is a minimal api.render.com/v1 (shapes from the OpenAPI spec).
type renderMock struct {
	t         *testing.T
	mu        sync.Mutex
	created   map[string]any
	ua        string
	deploy    string // newest deploy status
	suspended bool
	deleted   bool
	calls     []string
	pay402    bool
}

func (m *renderMock) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, r.Method+" "+r.URL.Path)
	if r.Header.Get("Authorization") != "Bearer rnd_key" {
		w.WriteHeader(401)
		w.Write([]byte(`{"message":"unauthorized"}`))
		return
	}
	m.ua = r.Header.Get("User-Agent")
	svc := func() string {
		s := "not_suspended"
		if m.suspended {
			s = "suspended"
		}
		return `{"id":"srv-1","name":"mgr-abc","type":"web_service","suspended":"` + s + `","serviceDetails":{"url":"https://mgr-abc.onrender.com","plan":"free","region":"singapore"}}`
	}
	switch {
	case r.Method == "GET" && r.URL.Path == "/owners":
		w.Write([]byte(`[{"owner":{"id":"tea-1","name":"ws","email":"a@b.c","type":"team"},"cursor":"c1"}]`))
	case r.Method == "POST" && r.URL.Path == "/services":
		if m.pay402 {
			w.WriteHeader(402)
			w.Write([]byte(`{"message":"You must enter payment information to perform this request."}`))
			return
		}
		json.NewDecoder(r.Body).Decode(&m.created)
		w.WriteHeader(201)
		w.Write([]byte(`{"service":` + svc() + `,"deployId":"dep-1"}`))
	case r.Method == "GET" && r.URL.Path == "/services/srv-1":
		if m.deleted {
			w.WriteHeader(404)
			w.Write([]byte(`{"message":"not found"}`))
			return
		}
		w.Write([]byte(svc()))
	case r.Method == "GET" && r.URL.Path == "/services/srv-1/deploys":
		if r.URL.Query().Get("limit") != "1" {
			http.Error(w, "limit", 400)
			return
		}
		w.Write([]byte(`[{"deploy":{"id":"dep-1","status":"` + m.deploy + `"},"cursor":"x"}]`))
		if m.deploy == "update_in_progress" {
			m.deploy = "live"
		}
	case r.Method == "POST" && r.URL.Path == "/services/srv-1/suspend":
		m.suspended = true
		w.WriteHeader(202)
	case r.Method == "POST" && r.URL.Path == "/services/srv-1/resume":
		m.suspended = false
		w.WriteHeader(202)
	case r.Method == "DELETE" && r.URL.Path == "/services/srv-1":
		m.deleted = true
		w.WriteHeader(204)
	case r.Method == "GET" && r.URL.Path == "/services":
		if r.URL.Query().Get("type") != "web_service" {
			http.Error(w, "type", 400)
			return
		}
		w.Write([]byte(`[{"service":` + svc() + `,"cursor":"a"},{"service":{"id":"srv-other","name":"my-app","type":"web_service","suspended":"not_suspended"},"cursor":"b"}]`))
	default:
		http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, 404)
	}
}

func TestRenderDriver(t *testing.T) {
	m := &renderMock{t: t, deploy: "update_in_progress"}
	srv := httptest.NewServer(m)
	defer srv.Close()
	d := NewRender(srv.Client(), srv.URL)
	c := Cred{"token": "rnd_key"}
	if err := d.Validate(bg, c); err != nil {
		t.Fatal(err)
	}
	if err := d.Validate(bg, Cred{"token": "bad"}); err == nil {
		t.Fatal("bad key must fail")
	}
	if err := d.Validate(bg, Cred{"token": "rnd_key", "owner_id": "tea-x"}); err == nil {
		t.Fatal("unknown workspace must fail")
	}
	if _, err := d.Create(bg, c, Spec{Name: "mgr-abc", SKU: "free"}); err == nil {
		t.Fatal("create without boot command must fail")
	}
	sb, err := d.Create(bg, c, Spec{Name: "mgr-abc", SKU: "free", Region: "singapore", Boot: "set -e; exec boxd"})
	if err != nil || sb.ID != "srv-1" || sb.State != StateStarting {
		t.Fatalf("create %+v %v", sb, err)
	}
	body, _ := json.Marshal(m.created)
	js := string(body)
	for _, want := range []string{`"type":"web_service"`, `"ownerId":"tea-1"`, `"imagePath":"` + RenderDefaultImage + `"`, `"runtime":"image"`, `"plan":"free"`, `"region":"singapore"`,
		`"key":"MGR_BOOT","value":"set -e; exec boxd"`, `"key":"BOXD_LISTEN_PORT","value":"${PORT}"`, `"key":"BOXD_KEEPALIVE_URL","value":"${RENDER_EXTERNAL_URL}/.well-known/mgr-health"`,
		`"key":"BOXD_PREVIEW_PORT","value":"8000"`, `"healthCheckPath":"/.well-known/mgr-health"`, `"dockerCommand":"/bin/sh -c eval \"$MGR_BOOT\""`, `"name":"mgr-abc"`} {
		if !strings.Contains(js, want) {
			t.Fatalf("create body missing %s:\n%s", want, js)
		}
	}
	if strings.Contains(strings.ToLower(js), "boxa") || strings.Contains(m.ua, "oxa") || m.ua != UserAgent {
		t.Fatalf("product name leaked to the provider: ua=%q body=%s", m.ua, js)
	}
	if strings.Contains(js, `"disk"`) {
		t.Fatal("free plan gets no disk")
	}
	// paid plan + disk, no keepalive
	if _, err := d.Create(bg, Cred{"token": "rnd_key", "disk_gb": "5", "image": "ghcr.io/x/agent:1", "preview_port": "3000"}, Spec{Name: "mgr-abd", SKU: "0.5c-512mb", Boot: "x"}); err != nil {
		t.Fatal(err)
	}
	body, _ = json.Marshal(m.created)
	js = string(body)
	for _, want := range []string{`"plan":"0.5c-512mb"`, `"disk":{"mountPath":"/srv/w","name":"data","sizeGB":5}`, `"imagePath":"ghcr.io/x/agent:1"`, `"key":"BOXD_PREVIEW_PORT","value":"3000"`} {
		if !strings.Contains(js, want) {
			t.Fatalf("paid create body missing %s:\n%s", want, js)
		}
	}
	if strings.Contains(js, "BOXD_KEEPALIVE_URL") {
		t.Fatal("paid plans never spin down: no keepalive")
	}
	g, err := d.Get(bg, c, "srv-1")
	if err != nil || g.State != StateStarting || g.Meta["deploy_status"] != "update_in_progress" {
		t.Fatalf("get starting %+v %v", g, err)
	}
	g, _ = d.Get(bg, c, "srv-1")
	if g.State != StateRunning || g.Meta["url"] != "https://mgr-abc.onrender.com" {
		t.Fatalf("get live %+v", g)
	}
	if u, err := d.PreviewURL(bg, c, "srv-1", 8000); err != nil || u != "https://mgr-abc.onrender.com" {
		t.Fatalf("preview %q %v", u, err)
	}
	if _, err := d.PreviewURL(bg, c, "srv-1", 3000); err == nil {
		t.Fatal("only the published port has a provider URL")
	}
	if _, err := d.PreviewURL(bg, Cred{"token": "rnd_key", "preview_port": "0"}, "srv-1", 8000); err == nil {
		t.Fatal("preview_port=0 publishes nothing")
	}
	if _, err := d.Exec(bg, c, "srv-1", ExecRequest{Command: "ls"}); err == nil {
		t.Fatal("no exec API")
	}
	if err := d.Stop(bg, c, "srv-1"); err != nil {
		t.Fatal(err)
	}
	if g, _ := d.Get(bg, c, "srv-1"); g.State != StateStopped {
		t.Fatalf("suspended → stopped, got %s", g.State)
	}
	if err := d.Start(bg, c, "srv-1"); err != nil {
		t.Fatal(err)
	}
	l, err := d.List(bg, c)
	if err != nil || len(l) != 1 || l[0].ID != "srv-1" {
		t.Fatalf("list keeps only mgr- services: %+v %v", l, err)
	}
	m.deploy = "update_failed"
	if g, _ := d.Get(bg, c, "srv-1"); g.State != StateError {
		t.Fatalf("failed deploy → error, got %s", g.State)
	}
	if err := d.Delete(bg, c, "srv-1"); err != nil {
		t.Fatal(err)
	}
	if g, _ := d.Get(bg, c, "srv-1"); g.State != StateDeleted {
		t.Fatalf("deleted %s", g.State)
	}
	m.pay402 = true
	_, err = d.Create(bg, c, Spec{Name: "mgr-x", SKU: "1c-2g", Boot: "x"})
	if ae, ok := err.(*model.APIError); !ok || !strings.Contains(ae.Hint, "payment method") {
		t.Fatalf("402 → payment hint, got %v", err)
	}
}

// TestRenderAdapter: through AsProvider the bootstrap becomes the service's
// start command (no exec), prices are the flat plan rates, and a missing
// bootstrap (shim mode) is refused.
func TestRenderAdapter(t *testing.T) {
	m := &renderMock{t: t, deploy: "live"}
	srv := httptest.NewServer(m)
	defer srv.Close()
	p := AsProvider(NewRender(srv.Client(), srv.URL))
	info := p.Info()
	if !info.DialOnly || !info.EphemeralRestart || info.Caps.Pause || info.Caps.Snapshot || info.Caps.Fork || !info.Caps.Ports || info.Tier != "T2" {
		t.Fatalf("caps %+v", info)
	}
	if p.Caps().Sleep || p.Caps().Snapshot || p.Caps().Fork {
		t.Fatalf("provider caps %+v", p.Caps())
	}
	offers, _ := p.Offers(bg, "")
	byKey := map[string]model.Offer{}
	for _, o := range offers {
		byKey[o.SKU+"@"+o.Region] = o
	}
	if len(offers) != 9*5 || byKey["free@oregon"].PriceHour != 0 || byKey["0.5c-512mb@singapore"].PriceHour != model.Round6(7.0/730) ||
		byKey["1c-2g@frankfurt"].PriceMonth != model.Round4(model.Round6(25.0/730)*730) || byKey["free@oregon"].Confidence != "verified" {
		t.Fatalf("offers %d %+v", len(offers), byKey["0.5c-512mb@singapore"])
	}
	if _, err := p.Create(bg, "rnd_key", provider.CreateRequest{Name: "mgr-abc", SKU: "free", Region: "oregon"}); err == nil {
		t.Fatal("dial-only provider without bootstrap must refuse")
	}
	h, err := p.Create(bg, "rnd_key", provider.CreateRequest{Name: "mgr-abc", SKU: "free", Region: "oregon", Bootstrap: "echo boot"})
	if err != nil || h.Ref != "srv-1" {
		t.Fatalf("create %+v %v", h, err)
	}
	b, _ := json.Marshal(m.created)
	if !strings.Contains(string(b), `"key":"MGR_BOOT","value":"echo boot"`) {
		t.Fatalf("bootstrap must be the start command: %s", b)
	}
	for _, c := range m.calls {
		if strings.Contains(c, "exec") {
			t.Fatalf("no exec call expected: %v", m.calls)
		}
	}
	if st, _ := p.Status(bg, "rnd_key", "srv-1"); st != "ready" {
		t.Fatalf("status %s", st)
	}
	tok := p.TokenFromEnv(func(k string) string {
		return map[string]string{"RENDER_API_KEY": "rnd_key", "RENDER_OWNER_ID": "tea-1", "RENDER_AGENT_IMAGE": "img:1"}[k]
	})
	if c := ParseCred(tok); c.Token() != "rnd_key" || c["owner_id"] != "tea-1" || c["image"] != "img:1" {
		t.Fatalf("env cred %q", tok)
	}
	if err := p.Validate(bg, "rnd_key"); err != nil {
		t.Fatal(err)
	}
	if err := p.Destroy(bg, "rnd_key", "srv-1"); err != nil {
		t.Fatal(err)
	}
	if err := p.Destroy(bg, "rnd_key", "srv-1"); err != nil {
		t.Fatalf("destroy twice is fine: %v", err)
	}
}

// TestRenderBootCommand runs the start command the way Render does (`/bin/sh
// -c <rest as one argument>`) with a foreground bootstrap whose download is
// a local file: the script must reach `exec boxd --dial`.
func TestRenderBootCommand(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	dir := t.TempDir()
	boot := BootstrapScript(BootstrapParams{CoreURL: "wss://core.example/v1/hosts/connect", BootstrapToken: "bxb_t", BoxdURL: "file://" + dir + "/boxd", Provider: "render", Region: "oregon", WorkDir: dir + "/w", Foreground: true})
	if strings.Contains(boot, "nohup") || !strings.Contains(boot, `exec "$D/boxd" --dial 'wss://core.example/v1/hosts/connect'`) || !strings.Contains(boot, "export BOXD_TOKEN='bxb_t'") {
		t.Fatalf("foreground bootstrap:\n%s", boot)
	}
	// fake boxd binary for both arches: prints its args and env
	fake := "#!/bin/sh\necho \"ARGS=$*\"; echo \"TOKEN=$BOXD_TOKEN DRIVER=$BOXD_DRIVER PROVIDER=$BOXD_PROVIDER\"\n"
	for _, a := range []string{"amd64", "arm64"} {
		if err := writeExec(dir+"/boxd-linux-"+a, fake); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("no curl")
	}
	cmd := exec.Command("/bin/sh", "-c", `eval "$MGR_BOOT"`)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "MGR_BOOT=" + boot, "HOME=" + dir}
	done := make(chan struct{})
	var out []byte
	var err error
	go func() { out, err = cmd.CombinedOutput(); close(done) }()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		cmd.Process.Kill()
		t.Fatal("boot command hung")
	}
	if err != nil || !strings.Contains(string(out), "ARGS=--dial wss://core.example/v1/hosts/connect") || !strings.Contains(string(out), "TOKEN=bxb_t DRIVER=sandbox PROVIDER=render") {
		t.Fatalf("boot run: %v\n%s", err, out)
	}
}

func writeExec(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o755)
}
