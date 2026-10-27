package e2e

// Deploying an app on a sandbox box, end to end through REST with a fake
// T3 provider whose sandboxes are separate directories: preview URLs for
// ports (box url + /ports), sleep → provider pause, wake → resume, T3
// snapshot (gateway tarball) + restore, fork onto a fresh sandbox, clone via
// the provider's native fork, and every sandbox deleted at the end.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"boxaz/core/internal/manager"
	"boxaz/core/internal/model"
	"boxaz/core/internal/sandbox"
)

const fakeWD = "/fake-sandbox-home"

// deploySandbox: one directory per sandbox; commands see it as fakeWD.
type deploySandbox struct {
	dir   string
	fork  bool
	mu    sync.Mutex
	n     int
	state map[string]string
	calls []string
}

func (f *deploySandbox) id() string {
	if f.fork {
		return "fakedeploy"
	}
	return "fakenofork"
}

func (f *deploySandbox) Info() sandbox.Info {
	return sandbox.Info{ID: f.id(), Name: "Fake deploy", Tier: "T3", Adapter: "native", Verification: sandbox.VerifiedDocs, Docs: "https://example.test",
		Caps:    sandbox.Capabilities{Pause: true, Fork: f.fork, Ports: true, BootMS: 100, Isolation: "microvm"},
		Pricing: sandbox.Pricing{CPUSecond: 0.000002, GBSecond: 0.000001, Source: "https://example.test/pricing", Confidence: "docs"},
		Sizes:   []sandbox.Size{{SKU: "base", CPU: 1, RAMMB: 2048, DiskGB: 10}}, Regions: []sandbox.Region{{ID: "global", Name: "Global"}},
		WorkDir: fakeWD, EnvVars: []string{"FAKEDEPLOY_KEY"}}
}
func (f *deploySandbox) Validate(ctx context.Context, c sandbox.Cred) error { return nil }
func (f *deploySandbox) note(s string) {
	f.mu.Lock()
	f.calls = append(f.calls, s)
	f.mu.Unlock()
}
func (f *deploySandbox) Create(ctx context.Context, c sandbox.Cred, s sandbox.Spec) (*sandbox.Sandbox, error) {
	f.mu.Lock()
	f.n++
	id := fmt.Sprintf("sb%d", f.n)
	f.state[id] = sandbox.StateRunning
	f.mu.Unlock()
	f.note("create " + id)
	return &sandbox.Sandbox{ID: id, State: sandbox.StateRunning}, os.MkdirAll(filepath.Join(f.dir, id), 0o755)
}
func (f *deploySandbox) Get(ctx context.Context, c sandbox.Cred, id string) (*sandbox.Sandbox, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st, ok := f.state[id]
	if !ok {
		st = sandbox.StateDeleted
	}
	return &sandbox.Sandbox{ID: id, State: st}, nil
}
func (f *deploySandbox) Delete(ctx context.Context, c sandbox.Cred, id string) error {
	f.mu.Lock()
	delete(f.state, id)
	f.mu.Unlock()
	f.note("delete " + id)
	return os.RemoveAll(filepath.Join(f.dir, id))
}
func (f *deploySandbox) Exec(ctx context.Context, c sandbox.Cred, id string, r sandbox.ExecRequest) (*sandbox.ExecResult, error) {
	f.mu.Lock()
	st := f.state[id]
	f.mu.Unlock()
	if st != sandbox.StateRunning {
		return nil, &sandbox.HTTPError{Status: 502, Body: "sandbox " + id + " is " + st}
	}
	cmd := exec.CommandContext(ctx, "bash", "-c", strings.ReplaceAll(r.Command, fakeWD, filepath.Join(f.dir, id)))
	var so, se strings.Builder
	cmd.Stdout, cmd.Stderr = &so, &se
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		return nil, err
	}
	return &sandbox.ExecResult{ExitCode: code, Stdout: strings.ReplaceAll(so.String(), filepath.Join(f.dir, id), fakeWD), Stderr: se.String()}, nil
}
func (f *deploySandbox) setState(id, st string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.state[id]; !ok {
		return &sandbox.HTTPError{Status: 404, Body: "no sandbox"}
	}
	f.state[id] = st
	return nil
}
func (f *deploySandbox) Pause(ctx context.Context, c sandbox.Cred, id string) error {
	f.note("pause " + id)
	return f.setState(id, sandbox.StatePaused)
}
func (f *deploySandbox) Resume(ctx context.Context, c sandbox.Cred, id string) error {
	f.note("resume " + id)
	return f.setState(id, sandbox.StateRunning)
}
func (f *deploySandbox) PreviewURL(ctx context.Context, c sandbox.Cred, id string, port int) (string, error) {
	return fmt.Sprintf("https://%d-%s.fake.test", port, id), nil
}

// forkingSandbox adds the provider-native fork (copies the directory).
type forkingSandbox struct{ *deploySandbox }

func (f forkingSandbox) Fork(ctx context.Context, c sandbox.Cred, id string) (*sandbox.Sandbox, error) {
	child, _ := f.Create(ctx, c, sandbox.Spec{})
	f.note("fork " + id + " → " + child.ID)
	out, err := exec.Command("cp", "-a", filepath.Join(f.dir, id)+"/.", filepath.Join(f.dir, child.ID)).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("cp: %v %s", err, out)
	}
	return child, nil
}

func TestSandboxDeploy(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash required")
	}
	if _, err := exec.LookPath("tar"); err != nil {
		t.Skip("tar required")
	}
	for _, withFork := range []bool{true, false} {
		withFork := withFork
		t.Run(map[bool]string{true: "native-fork", false: "no-fork"}[withFork], func(t *testing.T) { testSandboxDeploy(t, withFork) })
	}
}

func testSandboxDeploy(t *testing.T, withFork bool) {
	dir := t.TempDir()
	fk := &deploySandbox{dir: dir, fork: withFork, state: map[string]string{}}
	var drv sandbox.Driver = fk
	if withFork {
		drv = forkingSandbox{fk}
	}
	reg := sandbox.DefaultRegistry(nil)
	reg.Add(sandbox.AsProvider(drv))
	pid := fk.id()
	e := setupReg(t, reg, func(c *manager.Config) {
		c.AutoSandbox = true
		c.ProviderChain = []string{pid}
		c.HealthInterval = time.Hour
		c.SandboxEmptyAfter = 300 * time.Millisecond
		c.SnapshotDir = filepath.Join(dir, "core-snapshots")
	}, 0)
	ag, _ := e.agentClient("deploy-agent", 0)
	e.must(e.admin, "PUT", "/providers/"+pid+"/token", map[string]any{"token": "k"}, nil)

	// box up with a declared port → the box URL is the provider preview host
	var b model.Box
	e.must(ag, "POST", "/boxes", map[string]any{"template": "base", "slug": "web", "lifecycle": "session", "ports": []int{8000},
		"requirements": map[string]any{"cpu": 1, "ram_mb": 1024, "disk_gb": 5, "isolation": "sandbox"}}, &b)
	got := waitStatus(t, e.m, b.ID, model.StatusRunning, 20*time.Second)
	h, _ := e.m.Store.GetHost(e.ctx, got.HostID)
	if got.Provider != pid || h.Driver != "shim" {
		t.Fatalf("placed %+v on %+v", got, h)
	}
	if want := "https://8000-" + h.ProviderRef + ".fake.test/"; got.URL != want {
		t.Fatalf("box url %q, want %q", got.URL, want)
	}
	// expose another port
	var pr manager.PortsResult
	e.must(ag, "POST", "/boxes/"+b.ID+"/ports", map[string]any{"port": 9000}, &pr)
	if len(pr.Ports) != 2 || pr.Ports[0].Port != 9000 || pr.Ports[0].URL != "https://9000-"+h.ProviderRef+".fake.test/" || pr.Ports[0].Kind != "provider-preview" || !pr.Ports[0].Public {
		t.Fatalf("expose %+v", pr)
	}
	if st, _, _ := ag.Do(e.ctx, "POST", "/boxes/"+b.ID+"/ports", map[string]any{"port": 0}); st != 400 {
		t.Fatalf("port 0 → %d", st)
	}
	e.must(ag, "PUT", "/boxes/"+b.ID+"/files", map[string]any{"path": "app/index.html", "content": "v1\n"}, nil)

	// sleep → provider pause; wake → resume; files survive
	e.must(ag, "POST", "/boxes/"+b.ID+"/sleep", map[string]any{}, nil)
	if s, _ := fk.Get(e.ctx, nil, h.ProviderRef); s.State != sandbox.StatePaused {
		t.Fatalf("sleep did not pause the sandbox: %s", s.State)
	}
	e.must(ag, "POST", "/boxes/"+b.ID+"/wake", map[string]any{}, nil)
	var fr map[string]any
	e.must(ag, "GET", "/boxes/"+b.ID+"/files?path=app/index.html", nil, &fr)
	if fr["content"] != "v1\n" {
		t.Fatalf("after wake %+v", fr)
	}

	// T3 snapshot → change → restore
	var sn model.Snapshot
	e.must(ag, "POST", "/boxes/"+b.ID+"/snapshot", map[string]any{}, &sn)
	if !strings.HasPrefix(sn.Location, "local://"+filepath.Join(dir, "core-snapshots")) || sn.SizeMB <= 0 {
		t.Fatalf("snapshot %+v", sn)
	}
	e.must(ag, "PUT", "/boxes/"+b.ID+"/files", map[string]any{"path": "app/index.html", "content": "broken\n"}, nil)
	e.must(ag, "POST", "/boxes/"+b.ID+"/restore", map[string]any{"snapshot_id": sn.ID}, nil)
	e.must(ag, "GET", "/boxes/"+b.ID+"/files?path=app/index.html", nil, &fr)
	if fr["content"] != "v1\n" {
		t.Fatalf("after restore %+v", fr)
	}

	// fork the snapshot onto a fresh sandbox
	var fb model.Box
	e.must(ag, "POST", "/boxes/"+b.ID+"/fork", map[string]any{"snapshot_id": sn.ID, "slug": "web-fork"}, &fb)
	if fb.Status != model.StatusRunning || fb.HostID == got.HostID || fb.ForkedFrom != sn.ID {
		t.Fatalf("fork %+v", fb)
	}
	e.must(ag, "GET", "/boxes/"+fb.ID+"/files?path=app/index.html", nil, &fr)
	if fr["content"] != "v1\n" {
		t.Fatalf("fork files %+v", fr)
	}
	fh, _ := e.m.Store.GetHost(e.ctx, fb.HostID)
	if fh.ProviderRef == h.ProviderRef || !strings.Contains(fb.URL, fh.ProviderRef) {
		t.Fatalf("fork host %+v url %s", fh, fb.URL)
	}

	// clone: native fork when the provider has one, else snapshot → new sandbox
	var cb model.Box
	e.must(ag, "POST", "/boxes/"+b.ID+"/clone", map[string]any{"slug": "web-clone"}, &cb)
	if cb.Status != model.StatusRunning || cb.HostID == got.HostID || cb.ClonedFrom != b.ID {
		t.Fatalf("clone %+v", cb)
	}
	wantMethod := "native fork"
	if !withFork {
		wantMethod = "new " + pid + " sandbox"
	}
	if !strings.Contains(cb.PlacementReason, wantMethod) {
		t.Fatalf("clone method %q", cb.PlacementReason)
	}
	e.must(ag, "GET", "/boxes/"+cb.ID+"/files?path=app/index.html", nil, &fr)
	if fr["content"] != "v1\n" {
		t.Fatalf("clone files %+v", fr)
	}
	e.must(ag, "GET", "/boxes/"+cb.ID+"/files?path=/AGENTS.md", nil, &fr)
	if !strings.Contains(fr["content"].(string), cb.ID) {
		t.Fatal("clone has its own AGENTS.md")
	}

	// down everything → every sandbox deleted at the provider
	for _, id := range []string{b.ID, fb.ID, cb.ID} {
		e.must(ag, "DELETE", "/boxes/"+id, nil, nil)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		fk.mu.Lock()
		left := len(fk.state)
		fk.mu.Unlock()
		if left == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d sandboxes left: %v", left, fk.calls)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
