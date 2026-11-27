package sandbox

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"boxmes/core/internal/boxd"
	"boxmes/core/internal/proto"
)

func TestJSONPathAndTemplates(t *testing.T) {
	var doc any
	json.Unmarshal([]byte(`{"a":{"b":[{"c":"x"},{"c":5}]},"s":"ok"}`), &doc)
	if v, _ := JSONPath(doc, "$.a.b[1].c"); asString(v) != "5" {
		t.Fatal(v)
	}
	if v, ok := JSONPath(doc, "$.missing|$.s"); !ok || v != "ok" {
		t.Fatal("alternatives")
	}
	if _, ok := JSONPath(doc, "$.a.b[9]"); ok {
		t.Fatal("out of range")
	}
	v := map[string]string{"spec.cpu": "1.6", "spec.env": `{"A":"1"}`, "path": "a b/c", "empty": ""}
	body := map[string]any{"cpu": "{{int:spec.cpu}}", "env": "{{json:spec.env}}", "q": "/x?p={{q:path}}", "opt?": "{{empty}}", "n": "{{num:spec.cpu}}", "list": []any{"{{p:path}}"}}
	out, _ := renderValue(body, v)
	b, _ := json.Marshal(out)
	if string(b) != `{"cpu":2,"env":{"A":"1"},"list":["a%20b%2Fc"],"n":1.6,"q":"/x?p=a+b%2Fc"}` {
		t.Fatalf("render: %s", b)
	}
	id, vars := splitRef("abc#session=s%201")
	if id != "abc" || vars["session"] != "s 1" {
		t.Fatal("ref")
	}
}

func TestBuiltinYAMLParse(t *testing.T) {
	ys, err := BuiltinYAML(nil)
	if err != nil || len(ys) != 6 {
		t.Fatalf("builtin yaml: %d %v", len(ys), err)
	}
	if _, err := ParseYAML([]byte("id: x\nname: X\n")); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("lint must report missing calls: %v", err)
	}
	for _, y := range ys {
		if y.S.Tier == "T3" && y.S.Capabilities.Background {
			t.Fatalf("%s: T3 adapter claims background processes", y.S.ID)
		}
	}
}

func driverByID(t *testing.T, id, base string) *YAMLDriver {
	ys, _ := BuiltinYAML(nil)
	for _, y := range ys {
		if y.S.ID == id {
			return NewYAMLDriver(y.S, nil, base)
		}
	}
	t.Fatalf("no yaml %s", id)
	return nil
}

func TestYAMLRunloop(t *testing.T) {
	var created map[string]any
	files := map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer ak_1" {
			w.WriteHeader(401)
			return
		}
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		switch r.Method + " " + r.URL.Path {
		case "GET /v1/devboxes":
			w.Write([]byte(`{"devboxes":[]}`))
		case "POST /v1/devboxes":
			created = b
			w.Write([]byte(`{"id":"dbx_1","status":"provisioning"}`))
		case "GET /v1/devboxes/dbx_1":
			w.Write([]byte(`{"id":"dbx_1","status":"running"}`))
		case "POST /v1/devboxes/dbx_1/execute_sync":
			w.Write([]byte(`{"devbox_id":"dbx_1","exit_status":4,"stdout":"out","stderr":"err"}`))
		case "POST /v1/devboxes/dbx_1/write_file_contents":
			files[b["file_path"].(string)] = b["contents"].(string)
			w.Write([]byte(`{}`))
		case "POST /v1/devboxes/dbx_1/read_file_contents":
			w.Header().Set("Content-Type", "text/plain")
			w.Write([]byte(files[b["file_path"].(string)]))
		case "POST /v1/devboxes/dbx_1/shutdown", "POST /v1/devboxes/dbx_1/suspend", "POST /v1/devboxes/dbx_1/resume":
			w.Write([]byte(`{"id":"dbx_1"}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	y := driverByID(t, "runloop", srv.URL)
	d := y.Wrap()
	c := Cred{"token": "ak_1"}
	if err := d.Validate(bg, c); err != nil {
		t.Fatal(err)
	}
	sb, err := d.Create(bg, c, Spec{Name: "mgr-h1", SKU: "MEDIUM", Env: map[string]string{"K": "v"}})
	if err != nil || sb.ID != "dbx_1" || sb.State != StateStarting {
		t.Fatalf("create %+v %v", sb, err)
	}
	lp := created["launch_parameters"].(map[string]any)
	if lp["resource_size_request"] != "MEDIUM" || created["name"] != "mgr-h1" || created["environment_variables"].(map[string]any)["K"] != "v" {
		t.Fatalf("create body %+v", created)
	}
	if _, has := created["metadata"]; !has { // labels {} rendered as JSON object
		t.Fatalf("metadata missing: %+v", created)
	}
	if g, _ := d.Get(bg, c, "dbx_1"); g.State != StateRunning {
		t.Fatal(g.State)
	}
	r, err := d.Exec(bg, c, "dbx_1", ExecRequest{Command: "ls"})
	if err != nil || r.ExitCode != 4 || r.Stdout != "out" || r.Stderr != "err" {
		t.Fatalf("exec %+v %v", r, err)
	}
	f, ok := d.(Filer)
	if !ok {
		t.Fatal("runloop declares files")
	}
	if err := f.PutFile(bg, c, "dbx_1", "a.txt", []byte("line\n")); err != nil {
		t.Fatal(err)
	}
	if b, _ := f.GetFile(bg, c, "dbx_1", "a.txt"); string(b) != "line\n" {
		t.Fatalf("file %q", b)
	}
	p, ok := d.(Pauser)
	if !ok || p.Pause(bg, c, "dbx_1") != nil || p.Resume(bg, c, "dbx_1") != nil {
		t.Fatal("pause/resume")
	}
	if err := d.Delete(bg, c, "dbx_1"); err != nil {
		t.Fatal(err)
	}
}

func TestYAMLVercelSessionsAndNDJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer vtok" {
			w.WriteHeader(403)
			return
		}
		q := r.URL.Query()
		switch r.Method + " " + r.URL.Path {
		case "POST /v2/sandboxes":
			var b map[string]any
			json.NewDecoder(r.Body).Decode(&b)
			if b["projectId"] != "prj_1" || b["runtime"] != "node24" || b["resources"].(map[string]any)["vcpus"].(float64) != 2 || q.Get("teamId") != "team_1" {
				w.WriteHeader(400)
				return
			}
			w.Write([]byte(`{"sandbox":{"name":"mgr-x","status":"running"},"session":{"id":"sess_9","status":"pending"},"routes":[]}`))
		case "GET /v2/sandboxes/mgr-x":
			w.Write([]byte(`{"sandbox":{"name":"mgr-x"},"session":{"id":"sess_9","status":"running"}}`))
		case "POST /v2/sandboxes/sessions/sess_9/cmd":
			var b map[string]any
			json.NewDecoder(r.Body).Decode(&b)
			args := b["args"].([]any)
			if b["command"] != "bash" || args[0] != "-lc" || !strings.Contains(args[1].(string), "echo hi") {
				w.WriteHeader(400)
				return
			}
			w.Write([]byte(`{"command":{"id":"cmd_1","sessionId":"sess_9"}}`))
		case "GET /v2/sandboxes/sessions/sess_9/cmd/cmd_1/logs":
			w.Header().Set("Content-Type", "application/x-ndjson")
			io.WriteString(w, `{"stream":"stdout","data":"hi\n"}`+"\n"+`{"stream":"stderr","data":"e1"}`+"\n"+`{"stream":"stdout","data":"there\n"}`+"\n")
		case "GET /v2/sandboxes/sessions/sess_9/cmd/cmd_1":
			if q.Get("wait") != "true" {
				w.WriteHeader(400)
				return
			}
			w.Write([]byte(`{"command":{"id":"cmd_1","exitCode":0}}`))
		case "DELETE /v2/sandboxes/mgr-x":
			w.WriteHeader(200)
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	y := driverByID(t, "vercel", srv.URL)
	c := Cred{"token": "vtok", "project_id": "prj_1", "team_id": "team_1"}
	sb, err := y.Create(bg, c, Spec{Name: "mgr-x", CPU: 2, RAMMB: 4096})
	if err != nil || sb.ID != "mgr-x#session=sess_9" || sb.State != StateStarting {
		t.Fatalf("create %+v %v", sb, err)
	}
	// a fresh driver (core restart) still finds the session through the ref
	y2 := driverByID(t, "vercel", srv.URL)
	if g, _ := y2.Get(bg, c, sb.ID); g.State != StateRunning {
		t.Fatalf("get %+v", g)
	}
	r, err := y2.Exec(bg, c, sb.ID, ExecRequest{Command: "echo hi"})
	if err != nil || r.Stdout != "hi\nthere\n" || r.Stderr != "e1" || r.ExitCode != 0 {
		t.Fatalf("exec %+v %v", r, err)
	}
	if err := y2.Delete(bg, c, sb.ID); err != nil {
		t.Fatal(err)
	}
}

// localDriver runs "sandboxes" as directories on this machine (bash), so
// the shim can be tested end to end without a provider.
type localDriver struct{ root string }

func (l *localDriver) Info() Info {
	return Info{ID: "loc", Name: "Local", Tier: "T3", WorkDir: l.root}
}
func (l *localDriver) Validate(ctx context.Context, c Cred) error { return nil }
func (l *localDriver) Create(ctx context.Context, c Cred, s Spec) (*Sandbox, error) {
	return &Sandbox{ID: "l1", State: StateRunning}, nil
}
func (l *localDriver) Get(ctx context.Context, c Cred, id string) (*Sandbox, error) {
	return &Sandbox{ID: id, State: StateRunning}, nil
}
func (l *localDriver) Delete(ctx context.Context, c Cred, id string) error { return nil }
func (l *localDriver) Exec(ctx context.Context, c Cred, id string, r ExecRequest) (*ExecResult, error) {
	cctx, cancel := context.WithTimeout(ctx, time.Duration(r.TimeoutS+5)*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, "bash", "-c", r.Command)
	var so, se strings.Builder
	cmd.Stdout, cmd.Stderr = &so, &se
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		return nil, err
	}
	return &ExecResult{ExitCode: code, Stdout: so.String(), Stderr: se.String()}, nil
}

func TestShimT3(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	root := t.TempDir()
	s := NewShim(&localDriver{root: root}, Cred{}, "l1")
	call := func(method string, params, out any) error { return s.Call(bg, method, params, out) }
	if err := call(proto.MCreate, proto.CreateParams{BoxID: "b1", AgentsMD: "# rules\n", Env: map[string]string{"GREETING": "hey"}, Setup: []string{"echo setup > setup.txt"}}, nil); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "work", "setup.txt")); string(b) != "setup\n" {
		t.Fatalf("setup ran in %s: %q", root, b)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "AGENTS.md")); string(b) != "# rules\n" {
		t.Fatalf("AGENTS.md %q", b)
	}
	var out boxd.ExecOut
	if err := call(proto.MExec, proto.ExecParams{BoxID: "b1", Command: "for i in $(seq 1 10); do echo $GREETING $i; done; pwd; echo oops >&2; exit 3", TailLines: 3}, &out); err != nil {
		t.Fatal(err)
	}
	if out.ExitCode != 3 || !out.Truncated || !strings.HasSuffix(strings.TrimSpace(out.StdoutTail), filepath.Join(root, "work")) || !strings.Contains(out.StdoutTail, "hey 10") || strings.TrimSpace(out.StderrTail) != "oops" {
		t.Fatalf("exec out %+v", out)
	}
	// a truncated exec keeps its full output for box_logs (MLogRead)
	var lr0 proto.LogReadResult
	if out.LogID == "" || call(proto.MLogRead, proto.LogReadParams{LogID: out.LogID}, &lr0) != nil || !strings.Contains(lr0.Content, "hey 1\n") || !strings.Contains(lr0.Content, "oops") {
		t.Fatalf("log %q → %q", out.LogID, lr0.Content)
	}
	if pe, ok := call(proto.MLogRead, proto.LogReadParams{LogID: "log_nope"}, &lr0).(*proto.Error); !ok || pe.Code != "NOT_FOUND" {
		t.Fatal("unknown log id should be NOT_FOUND")
	}
	for i := 0; i < shimLogKeep+2; i++ {
		s.keepLog("x")
	}
	if len(s.logs) != shimLogKeep || s.logs[out.LogID] != "" {
		t.Fatalf("shim keeps %d logs", len(s.logs))
	}
	if err := call(proto.MWrite, proto.WriteParams{BoxID: "b1", Path: "/work/src/a.py", Content: "x = 1\nprint(x)\n"}, nil); err != nil {
		t.Fatal(err)
	}
	var rr proto.ReadResult
	if err := call(proto.MRead, proto.ReadParams{BoxID: "b1", Path: "src/a.py", From: 2, To: 2}, &rr); err != nil || rr.Content != "print(x)\n" || rr.TotalLines != 2 {
		t.Fatalf("read %+v %v", rr, err)
	}
	if err := call(proto.MRead, proto.ReadParams{BoxID: "b1", Path: "/work/src"}, &rr); err != nil || !rr.IsDir || len(rr.Entries) != 1 || rr.Entries[0].Name != "a.py" {
		t.Fatalf("read dir %+v %v", rr, err)
	}
	err := call(proto.MRead, proto.ReadParams{BoxID: "b1", Path: "nope.txt"}, &rr)
	if pe, ok := err.(*proto.Error); !ok || pe.Code != "NOT_FOUND" {
		t.Fatalf("missing file: %v", err)
	}
	var gr proto.GrepResult
	if err := call(proto.MGrep, proto.GrepParams{BoxID: "b1", Pattern: "print", Path: "/work"}, &gr); err != nil || len(gr.Matches) != 1 || gr.Matches[0].Line != 2 {
		t.Fatalf("grep %+v %v", gr, err)
	}
	var ob proto.ObserveResult
	if err := call(proto.MObserve, proto.BoxRef{BoxID: "b1"}, &ob); err != nil {
		t.Fatal(err)
	}
	var lr proto.ListResult
	call(proto.MList, nil, &lr)
	if len(lr.Boxes) != 1 {
		t.Fatal(lr)
	}
	// T3 snapshot: tarball pulled to the gateway; restore in place and fork
	// (Create) onto another sandbox
	s.SnapDir = t.TempDir()
	var sr proto.SnapshotResult
	if err := call(proto.MSnapshot, proto.SnapshotParams{BoxID: "b1", SnapshotID: "snap_1"}, &sr); err != nil || !strings.HasPrefix(sr.Location, "local://") || sr.SizeMB <= 0 {
		t.Fatalf("snapshot %+v %v", sr, err)
	}
	os.WriteFile(filepath.Join(root, "work", "src", "a.py"), []byte("broken\n"), 0o644)
	os.WriteFile(filepath.Join(root, "work", "junk.txt"), []byte("x"), 0o644)
	if err := call(proto.MRestore, proto.RestoreParams{BoxID: "b1", SnapshotID: "snap_1", Location: sr.Location}, nil); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "work", "src", "a.py")); string(b) != "x = 1\nprint(x)\n" {
		t.Fatalf("restored a.py %q", b)
	}
	if _, err := os.Stat(filepath.Join(root, "work", "junk.txt")); err == nil {
		t.Fatal("restore must replace /work")
	}
	root2 := t.TempDir()
	s2 := NewShim(&localDriver{root: root2}, Cred{}, "l2")
	if err := s2.Call(bg, proto.MRestore, proto.RestoreParams{BoxID: "b2", SnapshotID: "snap_1", Location: sr.Location, Create: true,
		Box: proto.CreateParams{AgentsMD: "# fork\n", Setup: []string{"exit 9"}}}, nil); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(root2, "work", "src", "a.py")); string(b) != "x = 1\nprint(x)\n" {
		t.Fatalf("forked a.py %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(root2, "AGENTS.md")); string(b) != "# fork\n" {
		t.Fatalf("a fork keeps its own AGENTS.md: %q", b)
	}
	if pe, ok := call(proto.MClone, proto.CloneParams{SourceID: "b1", BoxID: "b3"}, nil).(*proto.Error); !ok || pe.Code != "UNSUPPORTED" {
		t.Fatal("in-host clone stays unsupported on a T3 shim")
	}
	if err := call(proto.MDelete, proto.BoxRef{BoxID: "b1"}, nil); err != nil {
		t.Fatal(err)
	}
	// the real bootstrap script is valid sh
	script := BootstrapScript(BootstrapParams{CoreURL: "ws://x/v1/hosts/connect", BootstrapToken: "t", BoxdURL: "http://x/dl/boxd", Provider: "p"})
	if out, err := exec.Command("sh", "-n", "-c", script).CombinedOutput(); err != nil {
		t.Fatalf("bootstrap script syntax: %v %s", err, out)
	}
}
