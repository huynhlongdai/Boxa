package sandbox

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"boxa/core/internal/model"
	"boxa/core/internal/provider"
)

var bg = context.Background()

// ---- E2B / Novita ----

type e2bMock struct {
	t         *testing.T
	keyHeader string
	idHeader  string
	mu        sync.Mutex
	files     map[string][]byte
	paused    bool
	deleted   bool
	lastCmd   string
}

func (m *e2bMock) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if strings.HasPrefix(r.URL.Path, "/process.") || r.URL.Path == "/files" {
		if r.Header.Get("X-Access-Token") != "envd-tok" || r.Header.Get(m.idHeader) != "sbx1" {
			http.Error(w, "bad envd auth", 401)
			return
		}
	} else if r.Header.Get(m.keyHeader) != "key" {
		http.Error(w, `{"message":"invalid key"}`, 401)
		return
	}
	switch {
	case r.Method == "GET" && r.URL.Path == "/v2/sandboxes":
		w.Write([]byte(`[]`))
	case r.Method == "POST" && r.URL.Path == "/sandboxes":
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if body["templateID"] != "base" || body["timeout"].(float64) <= 0 {
			http.Error(w, "bad create body", 400)
			return
		}
		w.WriteHeader(201)
		w.Write([]byte(`{"sandboxID":"sbx1","templateID":"base","clientID":"c","envdVersion":"0.2","envdAccessToken":"envd-tok","domain":"e2b.test"}`))
	case r.Method == "GET" && r.URL.Path == "/sandboxes/sbx1":
		if m.deleted {
			http.Error(w, "gone", 404)
			return
		}
		st := "running"
		if m.paused {
			st = "paused"
		}
		w.Write([]byte(`{"sandboxID":"sbx1","templateID":"base","state":"` + st + `","envdAccessToken":"envd-tok","domain":"e2b.test"}`))
	case r.Method == "POST" && r.URL.Path == "/sandboxes/sbx1/pause":
		m.paused = true
		w.WriteHeader(204)
	case r.Method == "POST" && r.URL.Path == "/sandboxes/sbx1/connect":
		m.paused = false
		w.Write([]byte(`{"sandboxID":"sbx1"}`))
	case r.Method == "POST" && r.URL.Path == "/sandboxes/sbx1/fork":
		w.WriteHeader(201)
		w.Write([]byte(`[{"sandbox":{"sandboxID":"sbx2","envdVersion":"0.2","templateID":"base","clientID":"c"}}]`))
	case r.Method == "DELETE" && r.URL.Path == "/sandboxes/sbx1":
		m.deleted = true
		w.WriteHeader(204)
	case r.Method == "POST" && r.URL.Path == "/process.Process/Start":
		if r.Header.Get("Content-Type") != "application/connect+json" {
			http.Error(w, "content type", 415)
			return
		}
		b, _ := io.ReadAll(r.Body)
		n := binary.BigEndian.Uint32(b[1:5])
		var req struct {
			Process struct {
				Cmd  string            `json:"cmd"`
				Args []string          `json:"args"`
				Envs map[string]string `json:"envs"`
				Cwd  string            `json:"cwd"`
			} `json:"process"`
		}
		json.Unmarshal(b[5:5+n], &req)
		m.lastCmd = strings.Join(req.Process.Args, " ")
		w.Header().Set("Content-Type", "application/connect+json")
		frame := func(flag byte, v any) {
			p, _ := json.Marshal(v)
			var h [5]byte
			h[0] = flag
			binary.BigEndian.PutUint32(h[1:], uint32(len(p)))
			w.Write(h[:])
			w.Write(p)
		}
		b64 := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
		frame(0, map[string]any{"event": map[string]any{"start": map[string]any{"pid": 7}}})
		frame(0, map[string]any{"event": map[string]any{"data": map[string]any{"stdout": b64("hello ")}}})
		frame(0, map[string]any{"event": map[string]any{"data": map[string]any{"stdout": b64("world\n")}}})
		frame(0, map[string]any{"event": map[string]any{"data": map[string]any{"stderr": b64("warn\n")}}})
		frame(0, map[string]any{"event": map[string]any{"end": map[string]any{"exitCode": 3, "exited": true, "status": "exit status 3"}}})
		frame(2, map[string]any{})
	case r.URL.Path == "/files" && r.Method == "POST":
		mr, err := r.MultipartReader()
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		part, _ := mr.NextPart()
		data, _ := io.ReadAll(part)
		m.files[r.URL.Query().Get("path")] = data
		w.Write([]byte(`[{"path":"x"}]`))
	case r.URL.Path == "/files" && r.Method == "GET":
		d, ok := m.files[r.URL.Query().Get("path")]
		if !ok {
			http.Error(w, "nf", 404)
			return
		}
		w.Write(d)
	default:
		http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, 404)
	}
}

func testE2BLike(t *testing.T, d *E2B, keyHeader, idHeader string) {
	mock := &e2bMock{t: t, keyHeader: keyHeader, idHeader: idHeader, files: map[string][]byte{}}
	srv := httptest.NewServer(mock)
	defer srv.Close()
	d.EnvdBase = srv.URL
	c := Cred{"token": "key", "base_url": srv.URL}
	if err := d.Validate(bg, Cred{"token": "nope", "base_url": srv.URL}); err == nil {
		t.Fatal("bad key must fail")
	}
	if err := d.Validate(bg, c); err != nil {
		t.Fatal(err)
	}
	sb, err := d.Create(bg, c, Spec{SKU: "base", Labels: map[string]string{"boxa": "1"}})
	if err != nil || sb.ID != "sbx1" || sb.State != StateRunning {
		t.Fatalf("create %+v %v", sb, err)
	}
	res, err := d.Exec(bg, c, "sbx1", ExecRequest{Command: "echo hello world", Cwd: "/home/user"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Stdout != "hello world\n" || res.Stderr != "warn\n" || res.ExitCode != 3 {
		t.Fatalf("exec %+v", res)
	}
	if !strings.Contains(mock.lastCmd, "-c echo hello world") {
		t.Fatalf("cmd %q", mock.lastCmd)
	}
	if err := d.PutFile(bg, c, "sbx1", "/home/user/a.txt", []byte("data")); err != nil {
		t.Fatal(err)
	}
	if b, err := d.GetFile(bg, c, "sbx1", "/home/user/a.txt"); err != nil || string(b) != "data" {
		t.Fatalf("get file %q %v", b, err)
	}
	if err := d.Pause(bg, c, "sbx1"); err != nil {
		t.Fatal(err)
	}
	if g, _ := d.Get(bg, c, "sbx1"); g.State != StatePaused {
		t.Fatalf("paused state %+v", g)
	}
	if err := d.Resume(bg, c, "sbx1"); err != nil {
		t.Fatal(err)
	}
	if err := d.Delete(bg, c, "sbx1"); err != nil {
		t.Fatal(err)
	}
	if g, _ := d.Get(bg, c, "sbx1"); g.State != StateDeleted {
		t.Fatalf("deleted state %+v", g)
	}
}

func TestE2BDriver(t *testing.T) {
	d := NewE2B(nil, "")
	testE2BLike(t, d, "X-API-Key", "E2b-Sandbox-Id")
	mock := &e2bMock{t: t, keyHeader: "X-API-Key", idHeader: "E2b-Sandbox-Id", files: map[string][]byte{}}
	srv := httptest.NewServer(mock)
	defer srv.Close()
	fk, err := d.Fork(bg, Cred{"token": "key", "base_url": srv.URL}, "sbx1")
	if err != nil || fk.ID != "sbx2" {
		t.Fatalf("fork %+v %v", fk, err)
	}
	if u, _ := d.PreviewURL(bg, Cred{"token": "key", "base_url": srv.URL}, "sbx1", 3000); u != "https://3000-sbx1.e2b.test" {
		t.Fatalf("preview %q", u)
	}
	// custom templates become sizes
	if z := d.SizesFor(Cred{"templates": "base=2x512, big=4x4096"}); len(z) != 2 || z[1].SKU != "big" || z[1].CPU != 4 || z[1].RAMMB != 4096 {
		t.Fatalf("sizes %+v", z)
	}
}

func TestNovitaDriver(t *testing.T) {
	d := NewNovita(nil, "")
	if d.api(Cred{}, "us-virginia-1") != "https://api.sandbox.novita.ai" || d.api(Cred{}, "") != "https://api.us-phx-1.sandbox.novita.ai" {
		t.Fatalf("novita api urls %q %q", d.api(Cred{}, "us-virginia-1"), d.api(Cred{}, ""))
	}
	testE2BLike(t, d, "X-API-KEY", "Novita-Sandbox-Id")
	if _, err := d.Fork(bg, Cred{"token": "key"}, "x"); err == nil {
		t.Fatal("novita has no fork")
	}
}

// ---- Prized ----

func TestPrizedDriver(t *testing.T) {
	var srv *httptest.Server
	state := "running"
	var execBody map[string]any
	files := map[string]string{}
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer dcp_ok" {
			w.WriteHeader(401)
			w.Write([]byte(`{"error":"unauthorized"}`))
			return
		}
		sbx := func() string {
			return `{"id":"sbx_1","state":"` + state + `","template":"prized-base-v1","toolboxBase":"` + srv.URL + `/edge/sbx_1"}`
		}
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/v1/sandbox-templates":
			w.Write([]byte(`{"templates":[{"id":"prized-base-v1"}]}`))
		case r.Method == "POST" && r.URL.Path == "/api/v1/sandboxes":
			var b map[string]any
			json.NewDecoder(r.Body).Decode(&b)
			res := b["resources"].(map[string]any)
			if r.URL.Query().Get("wait") != "running" || r.Header.Get("Idempotency-Key") != "boxa-1" || b["template"] != "prized-base-v1" || res["vcpu"].(float64) != 2 || res["memoryMb"].(float64) != 4096 {
				w.WriteHeader(422)
				w.Write([]byte(`{"error":"unknown_field"}`))
				return
			}
			w.WriteHeader(201)
			w.Write([]byte(`{"sandbox":` + sbx() + `}`))
		case r.Method == "GET" && r.URL.Path == "/api/v1/sandboxes/sbx_1":
			if state == "deleted" {
				w.WriteHeader(404)
				w.Write([]byte(`{"error":"not_found"}`))
				return
			}
			w.Write([]byte(`{"sandbox":` + sbx() + `}`))
		case r.Method == "GET" && r.URL.Path == "/api/v1/sandboxes":
			w.Write([]byte(`{"sandboxes":[` + sbx() + `]}`))
		case r.Method == "POST" && r.URL.Path == "/api/v1/sandboxes/sbx_1/pause":
			state = "paused"
			w.WriteHeader(202)
			w.Write([]byte(`{"sandbox":` + sbx() + `}`))
		case r.Method == "POST" && r.URL.Path == "/api/v1/sandboxes/sbx_1/resume":
			state = "running"
			w.Write([]byte(`{"sandbox":` + sbx() + `}`))
		case r.Method == "POST" && r.URL.Path == "/api/v1/sandboxes/sbx_1/fork":
			w.WriteHeader(202)
			w.Write([]byte(`{"fork":{"id":"frk_1"},"sandboxes":[{"id":"sbx_2","state":"starting","toolboxBase":"x"}]}`))
		case r.Method == "DELETE" && r.URL.Path == "/api/v1/sandboxes/sbx_1":
			if state == "deleted" {
				w.WriteHeader(404)
				return
			}
			state = "deleted"
			w.WriteHeader(202)
			w.Write([]byte(`{"sandbox":{"id":"sbx_1","state":"deleting"}}`))
		case r.Method == "POST" && r.URL.Path == "/edge/sbx_1/process/execute":
			json.NewDecoder(r.Body).Decode(&execBody)
			w.Write([]byte(`{"stdout":"ok\n","stderr":"","exitCode":0,"durationMs":12}`))
		case r.Method == "POST" && r.URL.Path == "/edge/sbx_1/files/upload":
			b, _ := io.ReadAll(r.Body)
			files[r.URL.Query().Get("path")] = string(b)
			w.Write([]byte(`{"path":"p","size":1}`))
		case r.Method == "GET" && r.URL.Path == "/edge/sbx_1/files/download":
			w.Write([]byte(files[r.URL.Query().Get("path")]))
		default:
			w.WriteHeader(404)
			w.Write([]byte(`{"error":"not_found"}`))
		}
	}))
	defer srv.Close()
	d := NewPrized(nil, srv.URL+"/api/v1")
	c := Cred{"token": "dcp_ok"}
	if err := d.Validate(bg, Cred{"token": "bad"}); err == nil {
		t.Fatal("bad key")
	}
	if err := d.Validate(bg, c); err != nil {
		t.Fatal(err)
	}
	sb, err := d.Create(bg, c, Spec{Name: "boxa-1", CPU: 2, RAMMB: 4096, DiskGB: 10, Template: "prized-base-v1"})
	if err != nil || sb.ID != "sbx_1" || sb.State != StateRunning {
		t.Fatalf("create %+v %v", sb, err)
	}
	r, err := d.Exec(bg, c, "sbx_1", ExecRequest{Command: "make", Cwd: "/home/prized/work", TimeoutS: 9999})
	if err != nil || r.Stdout != "ok\n" || r.DurationMS != 12 {
		t.Fatalf("exec %+v %v", r, err)
	}
	if execBody["timeout"].(float64) != 3600 || execBody["cwd"] != "/home/prized/work" {
		t.Fatalf("exec body %+v", execBody)
	}
	if err := d.PutFile(bg, c, "sbx_1", "/home/prized/x", []byte("hi")); err != nil {
		t.Fatal(err)
	}
	if b, _ := d.GetFile(bg, c, "sbx_1", "/home/prized/x"); string(b) != "hi" {
		t.Fatalf("file %q", b)
	}
	if err := d.Pause(bg, c, "sbx_1"); err != nil {
		t.Fatal(err)
	}
	if g, _ := d.Get(bg, c, "sbx_1"); g.State != StatePaused {
		t.Fatal(g.State)
	}
	d.Resume(bg, c, "sbx_1")
	if f, err := d.Fork(bg, c, "sbx_1"); err != nil || f.ID != "sbx_2" {
		t.Fatalf("fork %+v %v", f, err)
	}
	if l, _ := d.List(bg, c); len(l) != 1 {
		t.Fatal("list")
	}
	p := AsProvider(d)
	if err := p.Destroy(bg, "dcp_ok", "sbx_1"); err != nil {
		t.Fatal(err)
	}
	if err := p.Destroy(bg, "dcp_ok", "sbx_1"); err != nil { // 404 = already gone
		t.Fatalf("second destroy: %v", err)
	}
	if st, _ := p.Status(bg, "dcp_ok", "sbx_1"); st != "down" {
		t.Fatal(st)
	}
	// errors are classified for the breaker
	err = p.Validate(bg, "bad")
	if ae, ok := err.(*model.APIError); !ok || ae.Code != model.ErrUnauthorized {
		t.Fatalf("want UNAUTHORIZED, got %v", err)
	}
}

// ---- Daytona ----

func TestDaytonaDriver(t *testing.T) {
	var srv *httptest.Server
	var created map[string]any
	uploaded := ""
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer dtn" || r.Header.Get("X-Daytona-Organization-ID") != "org1" {
			w.WriteHeader(401)
			return
		}
		sb := `{"id":"d1","state":"started","target":"eu","toolboxProxyUrl":"` + srv.URL + `/toolbox"}`
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/sandbox":
			w.Write([]byte(`{"items":[` + sb + `],"nextCursor":null}`))
		case r.Method == "POST" && r.URL.Path == "/api/sandbox":
			json.NewDecoder(r.Body).Decode(&created)
			w.Write([]byte(strings.Replace(sb, "started", "creating", 1)))
		case r.Method == "GET" && r.URL.Path == "/api/sandbox/d1":
			w.Write([]byte(sb))
		case r.Method == "DELETE" && r.URL.Path == "/api/sandbox/d1":
			w.WriteHeader(200)
		case r.Method == "POST" && (r.URL.Path == "/api/sandbox/d1/pause" || r.URL.Path == "/api/sandbox/d1/start" || r.URL.Path == "/api/sandbox/d1/stop"):
			w.Write([]byte(sb))
		case r.Method == "GET" && r.URL.Path == "/api/sandbox/d1/ports/8080/preview-url":
			w.Write([]byte(`{"sandboxId":"d1","url":"https://8080-d1.proxy.test","token":"t"}`))
		case r.Method == "POST" && r.URL.Path == "/toolbox/d1/process/execute":
			var b map[string]any
			json.NewDecoder(r.Body).Decode(&b)
			if !strings.HasPrefix(b["command"].(string), "bash -lc ") {
				w.WriteHeader(400)
				return
			}
			w.Write([]byte(`{"exitCode":1,"result":"boom\n"}`))
		case r.Method == "POST" && r.URL.Path == "/toolbox/d1/files/upload-v2":
			f, _, err := r.FormFile("file")
			if err != nil {
				w.WriteHeader(400)
				return
			}
			b, _ := io.ReadAll(f)
			uploaded = string(b)
		case r.Method == "GET" && r.URL.Path == "/toolbox/d1/files/download":
			w.Write([]byte(uploaded))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	d := NewDaytona(nil, srv.URL+"/api")
	c := Cred{"token": "dtn", "organization_id": "org1"}
	if err := d.Validate(bg, c); err != nil {
		t.Fatal(err)
	}
	sb, err := d.Create(bg, c, Spec{CPU: 2, RAMMB: 3000, DiskGB: 10, Region: "eu"})
	if err != nil || sb.State != StateStarting {
		t.Fatalf("create %+v %v", sb, err)
	}
	if created["memory"].(float64) != 3 || created["target"] != "eu" || created["cpu"].(float64) != 2 {
		t.Fatalf("create body %+v", created)
	}
	ws, err := WaitRunning(bg, d, c, "d1", 5*time.Second)
	if err != nil || ws.State != StateRunning {
		t.Fatalf("wait %+v %v", ws, err)
	}
	r, err := d.Exec(bg, c, "d1", ExecRequest{Command: "false"})
	if err != nil || r.ExitCode != 1 || r.Stdout != "boom\n" {
		t.Fatalf("exec %+v %v", r, err)
	}
	if err := d.PutFile(bg, c, "d1", "/home/daytona/f", []byte("abc")); err != nil {
		t.Fatal(err)
	}
	if b, _ := d.GetFile(bg, c, "d1", "/home/daytona/f"); string(b) != "abc" {
		t.Fatalf("file %q", b)
	}
	if u, _ := d.PreviewURL(bg, c, "d1", 8080); u != "https://8080-d1.proxy.test" {
		t.Fatal(u)
	}
	for _, f := range []func(context.Context, Cred, string) error{d.Pause, d.Resume, d.Stop, d.Start, d.Delete} {
		if err := f(bg, c, "d1"); err != nil {
			t.Fatal(err)
		}
	}
}

// ---- boxd.sh (grpc-web) ----

func TestBoxdShDriver(t *testing.T) {
	var gotCreate map[int][]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/auth/token" {
			var b map[string]string
			json.NewDecoder(r.Body).Decode(&b)
			if b["api_key"] != "bxd_ok" {
				w.WriteHeader(401)
				return
			}
			w.Write([]byte(`{"token":"jwt1","expires_at":` + jsonInt(time.Now().Add(time.Hour).Unix()) + `}`))
			return
		}
		if r.Header.Get("Authorization") != "Bearer jwt1" || r.Header.Get("Content-Type") != "application/grpc-web+proto" {
			w.WriteHeader(401)
			return
		}
		body, _ := io.ReadAll(r.Body)
		req, _ := pbFields(body[5:])
		w.Header().Set("Content-Type", "application/grpc-web+proto")
		send := func(m pbMsg) {
			var h [5]byte
			binary.BigEndian.PutUint32(h[1:], uint32(len(m)))
			w.Write(h[:])
			w.Write(m)
		}
		trailer := func(status, msg string) {
			t := []byte("grpc-status: " + status + "\r\ngrpc-message: " + msg + "\r\n")
			var h [5]byte
			h[0] = 0x80
			binary.BigEndian.PutUint32(h[1:], uint32(len(t)))
			w.Write(h[:])
			w.Write(t)
		}
		switch strings.TrimPrefix(r.URL.Path, "/boxd.api.v1.BoxdApi/") {
		case "Whoami":
			send(pbMsg{}.str(1, "usr_1"))
		case "CreateVm":
			gotCreate = req
			send(pbMsg{}.str(1, "vm_1").str(2, pbStr(req, 1)).str(4, "https://x.boxd.sh").str(6, "running").uint(7, 31))
		case "GetVm":
			if pbStr(req, 1) == "gone" {
				trailer("5", "machine%20not%20found")
				return
			}
			send(pbMsg{}.str(1, "vm_1").str(5, "running"))
		case "Exec":
			if pbStr(req, 5) != "vm_1" || !strings.Contains(pbStr(req, 4), "uname") {
				trailer("3", "bad exec")
				return
			}
			send(pbMsg{}.bytes(1, []byte("Linux\n")))
			send(pbMsg{}.bytes(1, []byte("err\n")).boolean(10, true))
			send(pbMsg{}.uint(6, 2))
		case "UploadFile":
			send(pbMsg{})
		case "DownloadFile":
			send(pbMsg{}.bytes(1, []byte("content")))
		case "DestroyVm", "SuspendVm", "ResumeVm":
			send(pbMsg{})
		case "ForkVm":
			send(pbMsg{}.str(1, "vm_2"))
		case "CreateSnapshot":
			send(pbMsg{}.str(1, "snap_1"))
		default:
			trailer("12", "unimplemented")
		}
		trailer("0", "")
	}))
	defer srv.Close()
	d := NewBoxdSh(nil, srv.URL)
	c := Cred{"token": "bxd_ok"}
	if err := d.Validate(bg, Cred{"token": "bxd_bad"}); err == nil {
		t.Fatal("bad key")
	}
	if err := d.Validate(bg, c); err != nil {
		t.Fatal(err)
	}
	sb, err := d.Create(bg, c, Spec{Name: "Boxa-Host", CPU: 2, RAMMB: 8192, Env: map[string]string{"A": "1"}})
	if err != nil || sb.ID != "vm_1" || sb.State != StateRunning {
		t.Fatalf("create %+v %v", sb, err)
	}
	cfg, _ := pbFields(pbBytes(gotCreate, 8))
	if pbStr(gotCreate, 1) != "boxa-host" || pbUint(cfg, 1) != 2 || pbUint(cfg, 2) != 8192<<20 || pbUint(gotCreate, 12) != 1 {
		t.Fatalf("CreateVm fields %+v cfg %+v", gotCreate, cfg)
	}
	r, err := d.Exec(bg, c, "vm_1", ExecRequest{Command: "uname"})
	if err != nil || r.Stdout != "Linux\n" || r.Stderr != "err\n" || r.ExitCode != 2 {
		t.Fatalf("exec %+v %v", r, err)
	}
	if g, _ := d.Get(bg, c, "gone"); g.State != StateDeleted {
		t.Fatalf("missing vm must read as deleted: %+v", g)
	}
	if b, _ := d.GetFile(bg, c, "vm_1", "/x"); string(b) != "content" {
		t.Fatal(string(b))
	}
	if err := d.PutFile(bg, c, "vm_1", "/x", []byte("1")); err != nil {
		t.Fatal(err)
	}
	if f, _ := d.Fork(bg, c, "vm_1"); f.ID != "vm_2" {
		t.Fatal("fork")
	}
	if s, _ := d.Snapshot(bg, c, "vm_1", "s"); s != "snap_1" {
		t.Fatal("snapshot")
	}
	for _, f := range []func(context.Context, Cred, string) error{d.Pause, d.Resume, d.Delete} {
		if err := f(bg, c, "vm_1"); err != nil {
			t.Fatal(err)
		}
	}
}

func jsonInt(v int64) string { b, _ := json.Marshal(v); return string(b) }

// ---- adapter (provider.Provider wrapper) ----

type recDriver struct {
	info    Info
	execs   []string
	deleted []string
	failRun bool
}

func (d *recDriver) Info() Info                                 { return d.info }
func (d *recDriver) Validate(ctx context.Context, c Cred) error { return nil }
func (d *recDriver) Create(ctx context.Context, c Cred, s Spec) (*Sandbox, error) {
	return &Sandbox{ID: "s-" + s.SKU, State: StateStarting}, nil
}
func (d *recDriver) Get(ctx context.Context, c Cred, id string) (*Sandbox, error) {
	return &Sandbox{ID: id, State: StateRunning}, nil
}
func (d *recDriver) Delete(ctx context.Context, c Cred, id string) error {
	d.deleted = append(d.deleted, id)
	return nil
}
func (d *recDriver) Exec(ctx context.Context, c Cred, id string, r ExecRequest) (*ExecResult, error) {
	d.execs = append(d.execs, r.Command)
	if d.failRun {
		return &ExecResult{ExitCode: 7, Stderr: "curl: (6) could not resolve"}, nil
	}
	return &ExecResult{Stdout: "boxd-started"}, nil
}

func TestAdapter(t *testing.T) {
	d := &recDriver{info: Info{ID: "rec", Name: "Rec", Tier: "T2", Caps: Capabilities{Pause: false, Fork: true},
		Pricing: Pricing{CPUSecond: 0.00001, GBSecond: 0.000002, Confidence: "docs"},
		Sizes:   []Size{{SKU: "s1", CPU: 1, RAMMB: 1024}, {SKU: "s2", CPU: 2, RAMMB: 4096}}, Regions: []Region{{ID: "r1"}, {ID: "r2"}},
		Credentials: []CredField{{Name: "token", Label: "Key"}, {Name: "project", Label: "Project"}}, EnvVars: []string{"REC_KEY", "REC_PROJECT"}}}
	var p provider.Provider = AsProvider(d)
	if p.Tier() != "T2" || !p.Implemented() || !p.Caps().Fork {
		t.Fatal("meta")
	}
	if err := p.Validate(bg, `{"token":"k"}`); err == nil {
		t.Fatal("missing project must fail")
	}
	if err := p.Validate(bg, `{"token":"k","project":"p"}`); err != nil {
		t.Fatal(err)
	}
	offers, _ := p.Offers(bg, "")
	if len(offers) != 4 || offers[0].MinBillingMinutes != 0 || offers[0].Confidence != "verified" {
		t.Fatalf("offers %+v", offers)
	}
	if want := model.Round6((0.00001*1 + 0.000002*1) * 3600); offers[0].PriceHour != want {
		t.Fatalf("price %v want %v", offers[0].PriceHour, want)
	}
	h, err := p.Create(bg, "k", provider.CreateRequest{SKU: "s2", Region: "r1", Bootstrap: "echo start"})
	if err != nil || h.Ref != "s-s2" || len(d.execs) != 1 || d.execs[0] != "echo start" {
		t.Fatalf("create %+v %v %v", h, err, d.execs)
	}
	d.failRun = true
	if _, err := p.Create(bg, "k", provider.CreateRequest{SKU: "s1", Bootstrap: "echo start"}); err == nil || len(d.deleted) != 1 {
		t.Fatalf("failed bootstrap must delete the sandbox: %v %v", err, d.deleted)
	}
	if err := p.(provider.Sleeper).Sleep(bg, "k", "x"); err == nil {
		t.Fatal("no pause support → error")
	}
	tok := AsProvider(d).TokenFromEnv(func(k string) string { return map[string]string{"REC_KEY": "a", "REC_PROJECT": "b"}[k] })
	if c := ParseCred(tok); c.Token() != "a" || c["project"] != "b" {
		t.Fatalf("env token %q", tok)
	}
	if AsProvider(d).TokenFromEnv(func(string) string { return "" }) != "" {
		t.Fatal("no env → empty")
	}
	if ParseCred("plain").Token() != "plain" || ParseCred(Cred{"token": "x"}.String()).Token() != "x" {
		t.Fatal("cred roundtrip")
	}
}

func TestBootstrapScript(t *testing.T) {
	s := BootstrapScript(BootstrapParams{CoreURL: "wss://core.example/v1/hosts/connect", BootstrapToken: "bxb_it's", BoxdURL: "https://core.example/dl/boxd", Provider: "e2b", Region: "global", WorkDir: "/home/user"})
	for _, want := range []string{"--dial 'wss://core.example/v1/hosts/connect'", "nohup setsid", "BOXD_DRIVER=sandbox", `'bxb_it'"'"'s'`, "-linux-$A", "/home/user/.boxa"} {
		if !strings.Contains(s, want) {
			t.Fatalf("bootstrap missing %q:\n%s", want, s)
		}
	}
	if !bytes.Contains([]byte(ShellCommand(ExecRequest{Command: "ls", Cwd: "/w", Env: map[string]string{"B": "2", "A": "x y", "bad-name": "z"}})), []byte("mkdir -p '/w' && cd '/w' && export A='x y'; export B='2'; ls")) {
		t.Fatalf("shell command: %s", ShellCommand(ExecRequest{Command: "ls", Cwd: "/w", Env: map[string]string{"B": "2", "A": "x y"}}))
	}
}

func TestAllDriversMetadata(t *testing.T) {
	ds := Drivers(nil)
	if len(ds) != 11 {
		t.Fatalf("want 5 native + 6 yaml drivers, have %d", len(ds))
	}
	seen := map[string]bool{}
	for _, d := range ds {
		in := d.Info()
		if seen[in.ID] {
			t.Fatalf("duplicate id %s", in.ID)
		}
		seen[in.ID] = true
		if in.Name == "" || in.Docs == "" || in.Verification == "" || in.Pricing.Source == "" || len(in.Sizes) == 0 || in.Initials == "" || len(in.EnvVars) == 0 {
			t.Fatalf("%s: incomplete metadata %+v", in.ID, in)
		}
		if in.Tier != "T2" && in.Tier != "T3" {
			t.Fatalf("%s tier %s", in.ID, in.Tier)
		}
		if in.Pricing.Confidence != "docs" && in.Pricing.Confidence != "estimated" {
			t.Fatalf("%s confidence %q", in.ID, in.Pricing.Confidence)
		}
	}
	for _, id := range DefaultChain {
		if !seen[id] {
			t.Fatalf("chain id %s has no driver", id)
		}
	}
	reg := DefaultRegistry(nil)
	if _, ok := reg.Get("hetzner"); !ok {
		t.Fatal("T1 providers kept")
	}
	if p, ok := reg.Get("prized"); !ok || p.Tier() != "T2" {
		t.Fatal("prized registered")
	}
}
