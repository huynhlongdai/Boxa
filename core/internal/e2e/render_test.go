package e2e

// Render (dial-only provider): the real Render driver against a mock
// api.render.com. POST /services "runs" the service: the mock reads the boxd
// start command from MGR_BOOT and starts an in-process boxd with its
// bootstrap token, as the container would. Checks: the provider is listed as
// dial-only T2, box up places on a Render service, exec goes through boxd,
// port 8000 is the onrender.com preview, a restart with a fresh filesystem
// rejoins with the same token (and fails the lost box), box down deletes the
// service, and nothing sent to Render names the product.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"boxmes/core/internal/boxd"
	"boxmes/core/internal/manager"
	"boxmes/core/internal/model"
	"boxmes/core/internal/sandbox"
)

type renderAPI struct {
	t       *testing.T
	dir     string
	coreWS  string
	ctx     context.Context
	mu      sync.Mutex
	n       int
	boots   map[string]string // service id → bootstrap token
	stops   map[string]context.CancelFunc
	deleted []string
	bodies  []string
}

func (r *renderAPI) startBoxd(id, token string) {
	r.mu.Lock()
	r.n++
	root := filepath.Join(r.dir, id, strconv.Itoa(r.n)) // fresh filesystem each start
	ctx, cancel := context.WithCancel(r.ctx)
	if old := r.stops[id]; old != nil {
		old()
	}
	r.stops[id] = cancel
	r.mu.Unlock()
	drv, err := boxd.NewLocalDriver(filepath.Join(root, "root"))
	if err != nil {
		r.t.Error(err)
		return
	}
	agent, err := boxd.NewAgent(boxd.Config{CoreURL: r.coreWS, BootstrapToken: token, StateDir: filepath.Join(root, "state"), Provider: "render", Region: "oregon",
		IP: "127.0.0.1", MetricsEvery: time.Second, Version: "test"}, drv)
	if err != nil {
		r.t.Error(err)
		return
	}
	go agent.Run(ctx)
}

func (r *renderAPI) ServeHTTP(w http.ResponseWriter, q *http.Request) {
	if q.Header.Get("Authorization") != "Bearer rnd_key" {
		w.WriteHeader(401)
		return
	}
	raw, _ := io.ReadAll(q.Body)
	r.mu.Lock()
	r.bodies = append(r.bodies, q.Method+" "+q.URL.String()+" "+q.Header.Get("User-Agent")+" "+string(raw))
	r.mu.Unlock()
	path := q.URL.Path
	switch {
	case q.Method == "GET" && path == "/owners":
		w.Write([]byte(`[{"owner":{"id":"tea-1","name":"ws","email":"a@b.c","type":"team"}}]`))
	case q.Method == "POST" && path == "/services":
		var body struct {
			Name    string `json:"name"`
			EnvVars []struct{ Key, Value string }
		}
		json.Unmarshal(raw, &body)
		boot := ""
		for _, e := range body.EnvVars {
			if e.Key == "MGR_BOOT" {
				boot = e.Value
			}
		}
		m := tokRe.FindStringSubmatch(boot)
		if m == nil || !strings.Contains(boot, "exec \"$D/boxd\" --dial") {
			http.Error(w, "no foreground boxd start command", 400)
			return
		}
		r.mu.Lock()
		id := "srv-" + body.Name
		r.boots[id] = m[1]
		r.mu.Unlock()
		r.startBoxd(id, m[1])
		w.WriteHeader(201)
		w.Write([]byte(`{"service":{"id":"` + id + `","name":"` + body.Name + `","suspended":"not_suspended","serviceDetails":{"url":"https://` + body.Name + `.onrender.com","plan":"free","region":"oregon"}},"deployId":"dep-1"}`))
	case q.Method == "GET" && strings.HasSuffix(path, "/deploys"):
		w.Write([]byte(`[{"deploy":{"id":"dep-1","status":"live"}}]`))
	case q.Method == "GET" && strings.HasPrefix(path, "/services/srv-"):
		name := strings.TrimPrefix(path, "/services/srv-")
		r.mu.Lock()
		gone := false
		for _, d := range r.deleted {
			gone = gone || d == "srv-"+name
		}
		r.mu.Unlock()
		if gone {
			w.WriteHeader(404)
			return
		}
		w.Write([]byte(`{"id":"srv-` + name + `","name":"` + name + `","suspended":"not_suspended","serviceDetails":{"url":"https://` + name + `.onrender.com","plan":"free","region":"oregon"}}`))
	case q.Method == "DELETE" && strings.HasPrefix(path, "/services/"):
		id := strings.TrimPrefix(path, "/services/")
		r.mu.Lock()
		r.deleted = append(r.deleted, id)
		if c := r.stops[id]; c != nil {
			c()
		}
		r.mu.Unlock()
		w.WriteHeader(204)
	default:
		http.Error(w, "unexpected "+q.Method+" "+path, 404)
	}
}

func TestRenderDialOnlyProvider(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	api := &renderAPI{t: t, dir: t.TempDir(), ctx: ctx, boots: map[string]string{}, stops: map[string]context.CancelFunc{}}
	rs := httptest.NewServer(api)
	defer rs.Close()
	reg := sandbox.DefaultRegistry(nil)
	e := setupReg(t, reg, func(c *manager.Config) {
		c.AutoSandbox = true
		c.ProviderChain = []string{"render"}
		c.HealthInterval = time.Hour
		c.SandboxEmptyAfter = 300 * time.Millisecond
	}, 0)
	api.coreWS = "ws" + strings.TrimPrefix(e.srv.URL, "http") + "/v1/hosts/connect"
	ag, _ := e.agentClient("render-agent", 0)

	var sp model.List[manager.SandboxProviderView]
	e.must(ag, "GET", "/sandbox/providers", nil, &sp)
	var rv *manager.SandboxProviderView
	for i := range sp.Items {
		if sp.Items[i].ID == "render" {
			rv = &sp.Items[i]
		}
	}
	if rv == nil || rv.Mode != "dial" || !rv.DialOnly || rv.Tier != "T2" || rv.Connected || rv.Caps.Pause || rv.FromUSDH != 0 {
		t.Fatalf("render view %+v", rv)
	}
	// without an internet-reachable core URL the provider cannot work at all
	pub := e.m.Cfg.PublicURL
	e.m.Cfg.PublicURL = ""
	var sp2 model.List[manager.SandboxProviderView]
	e.must(ag, "GET", "/sandbox/providers", nil, &sp2)
	for _, v := range sp2.Items {
		if v.ID == "render" && (v.Mode != "dial" || !strings.Contains(v.Blocked, "BOXMES_PUBLIC_URL")) {
			t.Fatalf("no public URL: %+v", v)
		}
	}
	e.m.Cfg.PublicURL = pub
	// even with sandbox_mode=shim the provider stays dial (no exec API)
	cred, _ := json.Marshal(map[string]string{"token": "rnd_key", "base_url": rs.URL})
	e.must(e.admin, "PUT", "/providers/render/token", map[string]any{"token": string(cred)}, nil)
	e.must(e.admin, "PUT", "/providers/render/prefs", map[string]any{"enabled": true, "sandbox_mode": "shim"}, nil)

	var b model.Box
	e.must(ag, "POST", "/boxes", map[string]any{"template": "base", "slug": "rweb", "lifecycle": "task", "ports": []int{8000},
		"requirements": map[string]any{"cpu": 0.5, "ram_mb": 512, "disk_gb": 5, "isolation": "sandbox"}}, &b)
	got := waitStatus(t, e.m, b.ID, model.StatusRunning, 30*time.Second)
	var h *model.Host
	for i := 0; i < 100; i++ {
		if h, _ = e.m.Store.GetHost(e.ctx, got.HostID); h != nil && h.Driver == "local" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got.Provider != "render" || h.Billing != "sandbox" || h.Driver != "local" || e.m.Hub.IsVirtual(h.ID) || !strings.HasPrefix(h.ProviderRef, "srv-mgr-") {
		t.Fatalf("placed %+v on %+v", got, h)
	}
	var res struct {
		ExitCode   int    `json:"exit_code"`
		StdoutTail string `json:"stdout_tail"`
	}
	e.must(ag, "POST", "/boxes/"+b.ID+"/exec", map[string]any{"command": "echo hi from render"}, &res)
	if res.ExitCode != 0 || !strings.Contains(res.StdoutTail, "hi from render") {
		t.Fatalf("exec %+v", res)
	}
	var pr manager.PortsResult
	e.must(ag, "GET", "/boxes/"+b.ID+"/ports", nil, &pr)
	name := strings.TrimPrefix(h.ProviderRef, "srv-")
	if len(pr.Ports) == 0 || pr.Ports[0].Kind != "provider-preview" || pr.Ports[0].URL != "https://"+name+".onrender.com/" || !pr.Ports[0].Public {
		t.Fatalf("ports %+v", pr)
	}

	// platform restart with a fresh filesystem: boxd rejoins with the same
	// bootstrap token; the box's files are gone, so the box is failed
	api.startBoxd(h.ProviderRef, api.boots[h.ProviderRef])
	deadline := time.Now().Add(15 * time.Second)
	for {
		cur, _ := e.m.Store.GetBox(e.ctx, b.ID)
		if cur.Status == model.StatusFailed && strings.Contains(cur.Error, "missing on host") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("after restart: %+v", cur)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if hh, _ := e.m.Store.GetHost(e.ctx, h.ID); hh.Status == "deleted" || hh.BootstrapHash == "" {
		t.Fatalf("rejoin keeps the host and its token: %+v", hh)
	}

	// box down → service deleted
	e.must(ag, "DELETE", "/boxes/"+b.ID, nil, nil)
	waitHostStatus(t, e, got.HostID, "deleted")
	api.mu.Lock()
	defer api.mu.Unlock()
	if len(api.deleted) != 1 || api.deleted[0] != h.ProviderRef {
		t.Fatalf("deleted %v", api.deleted)
	}
	for _, s := range api.bodies {
		if l := strings.ToLower(s); strings.Contains(l, "boxa") {
			t.Fatalf("product name sent to the provider: %s", s)
		}
	}
}
