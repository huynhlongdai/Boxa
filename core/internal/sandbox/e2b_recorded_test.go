package sandbox

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// Response bodies recorded from the real E2B API (Hobby) and Novita Sandbox
// (us-phx-1) on 2026-10-27; ids/tokens shortened.
const (
	e2bRecCreate  = `{"alias":"base","clientID":"6532622b","envdAccessToken":"tok-1","envdVersion":"0.10.0","sandboxID":"sbx1","templateID":"rki5dems9wqfm4r03t7g"}`
	e2bRecGet     = `{"alias":"base","clientID":"6532622b","cpuCount":2,"diskSizeMB":23668,"endAt":"2026-10-27T04:41:42Z","envdAccessToken":"tok-1","envdVersion":"0.10.0","lifecycle":{"autoResume":false,"onTimeout":"kill"},"memoryMB":512,"metadata":{"managed":"1"},"sandboxID":"sbx1","startedAt":"2026-10-27T04:39:42Z","state":"%STATE%","templateID":"rki5dems9wqfm4r03t7g","volumeMounts":[]}`
	e2bRecConnect = `{"alias":"base","clientID":"6532622b","envdAccessToken":"tok-2","envdVersion":"0.10.0","sandboxID":"sbx1","templateID":"rki5dems9wqfm4r03t7g"}`
	e2bRecFork    = `[{"sandbox":{"alias":"base","clientID":"6532622b","envdAccessToken":"tok-child","envdVersion":"0.10.0","sandboxID":"sbx2","templateID":"rki5dems9wqfm4r03t7g"}}]`
	e2bRec404     = `{"code":404,"message":"Sandbox \"sbx1\" doesn't exist or you don't have access to it"}`
	// Novita: no envdAccessToken (envd is unauthenticated), domain null.
	novitaRecCreate = `{"alias":"base","clientID":"6532622b","domain":null,"envdVersion":"0.5.12","sandboxID":"nsb1","templateID":"kk065fn4aju6x2v0ghgk","trafficAccessToken":null}`
	novitaRec402    = `{"code":402,"message":"error: code = 400 reason = BALANCE_NOT_ENOUGH message = balance not enough metadata = map[] cause = \u003cnil\u003e"}`
)

// connectFrame encodes one Connect streaming frame.
func connectFrame(flags byte, payload string) []byte {
	var b bytes.Buffer
	b.WriteByte(flags)
	binary.Write(&b, binary.BigEndian, uint32(len(payload)))
	b.WriteString(payload)
	return b.Bytes()
}

// recordedExecStream is envd's real answer shape: start, data (base64),
// end without exitCode when it is 0 (proto3 JSON omits defaults), trailer {}.
func recordedExecStream(stdout string, exit int) []byte {
	var b bytes.Buffer
	b.Write(connectFrame(0, `{"event":{"start":{"pid":1539}}}`))
	for len(stdout) > 0 {
		n := 32 << 10
		if n > len(stdout) {
			n = len(stdout)
		}
		js, _ := json.Marshal(map[string]any{"event": map[string]any{"data": map[string]any{"stdout": []byte(stdout[:n])}}})
		b.Write(connectFrame(0, string(js)))
		stdout = stdout[n:]
	}
	end := `{"event":{"end":{"exited":true,"status":"exit status 0"}}}`
	if exit != 0 {
		end = `{"event":{"end":{"exitCode":` + itoa(exit) + `,"exited":true,"status":"exit status ` + itoa(exit) + `"}}}`
	}
	b.Write(connectFrame(0, end))
	b.Write(connectFrame(2, `{}`))
	return b.Bytes()
}

func itoa(i int) string { b, _ := json.Marshal(i); return string(b) }

// hostRT sends every request to the test server and records the host it was
// meant for, so region routing (api.<domain>, 49983-<id>.<domain>) is checked.
type hostRT struct {
	to   *url.URL
	mu   sync.Mutex
	seen []string
}

func (h *hostRT) RoundTrip(r *http.Request) (*http.Response, error) {
	h.mu.Lock()
	h.seen = append(h.seen, r.Method+" "+r.URL.Host+r.URL.Path+" tok="+r.Header.Get("X-Access-Token"))
	h.mu.Unlock()
	r2 := r.Clone(r.Context())
	r2.URL.Scheme, r2.URL.Host = h.to.Scheme, h.to.Host
	return http.DefaultTransport.RoundTrip(r2)
}

func (h *hostRT) log() string { h.mu.Lock(); defer h.mu.Unlock(); return strings.Join(h.seen, "\n") }

// TestE2BRecorded replays the live E2B shapes: template alias (not the build
// id) is reported, connect (resume) refreshes the envd token, fork children
// are remembered with their own token, exit codes survive the stream, and a
// killed sandbox reads as deleted.
func TestE2BRecorded(t *testing.T) {
	state, killed := "running", map[string]bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Method + " " + r.URL.Path
		switch {
		case key == "POST /sandboxes":
			var b map[string]any
			json.NewDecoder(r.Body).Decode(&b)
			if b["templateID"] != "base" || b["metadata"].(map[string]any)[LabelManaged] != "1" {
				t.Errorf("create body %v", b)
			}
			w.WriteHeader(201)
			io.WriteString(w, e2bRecCreate)
		case key == "GET /sandboxes/sbx1":
			if killed["sbx1"] {
				w.WriteHeader(404)
				io.WriteString(w, e2bRec404)
				return
			}
			io.WriteString(w, strings.Replace(e2bRecGet, "%STATE%", state, 1))
		case key == "POST /sandboxes/sbx1/pause":
			state = "paused"
			w.WriteHeader(204)
		case key == "POST /sandboxes/sbx1/connect":
			state = "running"
			w.WriteHeader(201)
			io.WriteString(w, e2bRecConnect)
		case key == "POST /sandboxes/sbx1/fork":
			w.WriteHeader(201)
			io.WriteString(w, e2bRecFork)
		case strings.HasPrefix(key, "DELETE /sandboxes/"):
			killed[strings.TrimPrefix(r.URL.Path, "/sandboxes/")] = true
			w.WriteHeader(204)
		case key == "POST /process.Process/Start":
			body, _ := io.ReadAll(r.Body)
			w.Header().Set("Content-Type", "application/connect+json")
			if bytes.Contains(body, []byte("seq")) {
				var sb strings.Builder
				for i := 1; i <= 200000; i++ {
					sb.WriteString(itoa(i) + "\n")
				}
				w.Write(recordedExecStream(sb.String(), 7))
				return
			}
			w.Write(recordedExecStream("ok\n", 0))
		default:
			t.Errorf("unexpected %s", key)
			w.WriteHeader(500)
		}
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	rt := &hostRT{to: u}
	d := NewE2B(&http.Client{Transport: rt}, "")
	c := Cred{"token": "proxy"}

	sb, err := d.Create(bg, c, Spec{SKU: "base", Labels: ManagedLabels("h1")})
	if err != nil || sb.ID != "sbx1" || sb.Template != "base" {
		t.Fatalf("create %+v %v", sb, err)
	}
	r, err := d.Exec(bg, c, "sbx1", ExecRequest{Command: "true"})
	if err != nil || r.ExitCode != 0 || r.Stdout != "ok\n" {
		t.Fatalf("exec %+v %v", r, err)
	}
	r, err = d.Exec(bg, c, "sbx1", ExecRequest{Command: "seq 1 200000; exit 7"})
	if err != nil || r.ExitCode != 7 || strings.Count(r.Stdout, "\n") != 200000 || !strings.HasSuffix(r.Stdout, "200000\n") {
		t.Fatalf("long exec: exit %d, %d lines, %v", r.ExitCode, strings.Count(r.Stdout, "\n"), err)
	}
	if g, err := d.Get(bg, c, "sbx1"); err != nil || g.Template != "base" || g.State != StateRunning || g.Meta["domain"] != "e2b.app" {
		t.Fatalf("get %+v %v", g, err)
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
	if _, err := d.Exec(bg, c, "sbx1", ExecRequest{Command: "true"}); err != nil {
		t.Fatal(err)
	}
	ch, err := d.Fork(bg, c, "sbx1")
	if err != nil || ch.ID != "sbx2" || ch.Template != "base" {
		t.Fatalf("fork %+v %v", ch, err)
	}
	// preview host as served live (2026-10-27: https://8000-<id>.e2b.app → 200, public)
	if u, err := d.PreviewURL(bg, c, "sbx2", 8000); err != nil || u != "https://8000-sbx2.e2b.app" {
		t.Fatalf("preview %q %v", u, err)
	}
	if _, err := d.Exec(bg, c, "sbx2", ExecRequest{Command: "true"}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"sbx1", "sbx2"} {
		if err := d.Delete(bg, c, id); err != nil {
			t.Fatal(err)
		}
	}
	if g, err := d.Get(bg, c, "sbx1"); err != nil || g.State != StateDeleted {
		t.Fatalf("after delete %+v %v", g, err)
	}
	log := rt.log()
	for _, want := range []string{
		"POST api.e2b.app/sandboxes tok=",
		"POST 49983-sbx1.e2b.app/process.Process/Start tok=tok-1",
		"POST 49983-sbx1.e2b.app/process.Process/Start tok=tok-2",     // token from /connect
		"POST 49983-sbx2.e2b.app/process.Process/Start tok=tok-child", // fork child remembered
	} {
		if !strings.Contains(log, want) {
			t.Errorf("missing %q in\n%s", want, log)
		}
	}
	if strings.Contains(log, "GET api.e2b.app/sandboxes/sbx2") {
		t.Errorf("fork child looked up again although fork answered its token:\n%s", log)
	}
}

// TestNovitaRecorded: Novita answers no envd token, so the driver must not
// re-GET the sandbox before every exec/file call; a sandbox created in
// us-virginia-1 is paused/resumed/deleted on that region's API; 402
// BALANCE_NOT_ENOUGH is a quota/balance error.
func TestNovitaRecorded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch key := r.Method + " " + r.URL.Path; {
		case key == "POST /sandboxes" && r.Host == "api.us-phx-1.sandbox.novita.ai" && r.Header.Get("X-API-KEY") == "broke":
			w.WriteHeader(402)
			io.WriteString(w, novitaRec402)
		case key == "POST /sandboxes":
			w.WriteHeader(201)
			io.WriteString(w, novitaRecCreate)
		case key == "POST /process.Process/Start":
			w.Header().Set("Content-Type", "application/connect+json")
			w.Write(recordedExecStream("hi\n", 0))
		case key == "POST /files":
			io.WriteString(w, `[{"name":"a.txt","path":"/home/user/a.txt","type":"file"}]`)
		case key == "GET /files":
			io.WriteString(w, "data")
		case key == "POST /sandboxes/nsb1/pause", key == "DELETE /sandboxes/nsb1":
			w.WriteHeader(204)
		case key == "POST /sandboxes/nsb1/connect":
			w.WriteHeader(201)
			io.WriteString(w, novitaRecCreate)
		default:
			t.Errorf("unexpected %s %s", r.Host, key)
			w.WriteHeader(500)
		}
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	rt := &hostRT{to: u}
	d := NewNovita(&http.Client{Transport: rt}, "")
	c := Cred{"token": "proxy"}
	if _, err := d.Create(bg, c, Spec{SKU: "base", Region: "us-virginia-1"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if r, err := d.Exec(bg, c, "nsb1", ExecRequest{Command: "echo hi"}); err != nil || r.Stdout != "hi\n" || r.ExitCode != 0 {
			t.Fatalf("exec %+v %v", r, err)
		}
	}
	if err := d.PutFile(bg, c, "nsb1", "/home/user/a.txt", []byte("data")); err != nil {
		t.Fatal(err)
	}
	if b, err := d.GetFile(bg, c, "nsb1", "/home/user/a.txt"); err != nil || string(b) != "data" {
		t.Fatalf("getfile %q %v", b, err)
	}
	// preview host on the sandbox's region domain (live: us-phx-1 → https://8000-<id>.us-phx-1.sandbox.novita.ai, no traffic token)
	if u, err := d.PreviewURL(bg, c, "nsb1", 8000); err != nil || u != "https://8000-nsb1.sandbox.novita.ai" {
		t.Fatalf("preview %q %v", u, err)
	}
	if err := d.Pause(bg, c, "nsb1"); err != nil {
		t.Fatal(err)
	}
	if err := d.Resume(bg, c, "nsb1"); err != nil {
		t.Fatal(err)
	}
	if err := d.Delete(bg, c, "nsb1"); err != nil {
		t.Fatal(err)
	}
	log := rt.log()
	if strings.Contains(log, "GET api.") {
		t.Errorf("sandbox re-fetched before envd calls (no token on Novita):\n%s", log)
	}
	for _, want := range []string{"POST api.sandbox.novita.ai/sandboxes", "POST 49983-nsb1.sandbox.novita.ai/process.Process/Start",
		"POST api.sandbox.novita.ai/sandboxes/nsb1/pause", "POST api.sandbox.novita.ai/sandboxes/nsb1/connect", "DELETE api.sandbox.novita.ai/sandboxes/nsb1"} {
		if !strings.Contains(log, want) {
			t.Errorf("missing %q in\n%s", want, log)
		}
	}
	_, err := d.Create(bg, Cred{"token": "broke"}, Spec{SKU: "base"})
	if he, ok := err.(*HTTPError); !ok || he.Status != 402 {
		t.Fatalf("402 not surfaced: %v", err)
	}
	if e := wrapErr("Novita Sandbox", err); !strings.Contains(e.Error(), "quota/balance") {
		t.Fatalf("402 not classified as balance: %v", e)
	}
}
