package sandbox

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// Response bodies recorded from the real Prized API on 2026-10-27 (free
// tier); ids shortened, timestamps as the API returned them.
const prizedRecordedSandbox = `{"id":"sbx_aaaa","state":"%STATE%","template":"prized-base-v1","templateBuild":"prized-base-v1-r2","resources":{"vcpu":1,"memoryMb":2048,"diskGb":20},"durability":"ephemeral","expiresAt":"2026-10-27T04:12:00Z","maxRuntimeMin":15,"errorReason":null,"labels":{"managed":"1"},"autoPauseMin":null,"autoDeleteMin":null,"toolboxBase":"%TB%","createdAt":"2026-10-27T03:57:00Z","lastActiveAt":"2026-10-27T03:57:00Z"}`

const prizedRecordedFork = `{"fork":{"id":"frk_bbbb","parentId":"sbx_aaaa","count":1,"status":"active","sandboxIds":["sbx_cccc"],"createdAt":"2026-10-27T04:00:00Z"},"sandboxes":[{"id":"sbx_cccc","state":"starting","template":"prized-base-v1","templateBuild":"prized-base-v1-r2","resources":{"vcpu":1,"memoryMb":2048,"diskGb":20},"durability":"ephemeral","expiresAt":"2026-10-27T04:14:00Z","maxRuntimeMin":15,"errorReason":null,"labels":{"managed":"1"},"autoPauseMin":null,"autoDeleteMin":null,"toolboxBase":"%TB%","createdAt":"2026-10-27T04:00:00Z","lastActiveAt":"2026-10-27T04:00:00Z"}]}`

const prizedRecordedExec = `{"stdout":"Linux box-5208 6.1.119 #1 SMP PREEMPT_DYNAMIC Sat Aug 15 04:06:25 UTC 2026 x86_64 x86_64 x86_64 GNU/Linux\n","stderr":"","exitCode":0,"durationMs":5}`

// TestPrizedRecorded replays real response shapes and checks the fixes from
// the live test: zero resources are omitted, a name (Idempotency-Key) reused
// after delete gets a fresh key instead of the dead sandbox, pause waits for
// "paused", and fork/exec parse the real bodies.
func TestPrizedRecorded(t *testing.T) {
	var mu sync.Mutex
	keys := map[string]string{} // idempotency key → sandbox state it replays
	var bodies []map[string]any
	var paths []string
	var srv *httptest.Server
	sbx := func(state string) string {
		return strings.NewReplacer("%STATE%", state, "%TB%", srv.URL+"/v1/sandbox/sbx_aaaa").Replace(prizedRecordedSandbox)
	}
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		paths = append(paths, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
		b, _ := io.ReadAll(r.Body)
		switch {
		case r.Method == "POST" && r.URL.Path == "/api/v1/sandboxes":
			var m map[string]any
			json.Unmarshal(b, &m)
			bodies = append(bodies, m)
			k := r.Header.Get("Idempotency-Key")
			if st, ok := keys[k]; ok { // replay of the first result, as the real API does
				w.WriteHeader(200)
				w.Write([]byte(`{"sandbox":` + sbx(st) + `}`))
				return
			}
			keys[k] = "running"
			w.WriteHeader(201)
			w.Write([]byte(`{"sandbox":` + sbx("running") + `}`))
		case r.Method == "DELETE":
			for k := range keys {
				keys[k] = "deleted"
			}
			w.WriteHeader(202)
			w.Write([]byte(`{"sandbox":` + sbx("deleting") + `}`))
		case strings.HasSuffix(r.URL.Path, "/pause"):
			w.Write([]byte(`{"sandbox":` + sbx("paused") + `}`))
		case strings.HasSuffix(r.URL.Path, "/fork"):
			w.WriteHeader(202)
			w.Write([]byte(strings.ReplaceAll(prizedRecordedFork, "%TB%", srv.URL+"/v1/sandbox/sbx_cccc")))
		case strings.HasSuffix(r.URL.Path, "/process/execute"):
			w.Write([]byte(prizedRecordedExec))
		default:
			w.WriteHeader(404)
			w.Write([]byte(`{"error":"not_found"}`))
		}
	}))
	defer srv.Close()
	d := NewPrized(nil, srv.URL+"/api/v1")
	c := Cred{"token": "dcp_test"}

	a, err := d.Create(bg, c, Spec{Name: "mgr-h1", Template: "prized-base-v1"})
	if err != nil || a.ID != "sbx_aaaa" || a.State != StateRunning || a.Meta["raw_state"] != "running" {
		t.Fatalf("create: %+v %v", a, err)
	}
	if _, ok := bodies[0]["resources"]; ok {
		t.Fatalf("zero cpu/ram must not be sent: %v", bodies[0])
	}
	if err := d.Delete(bg, c, a.ID); err != nil {
		t.Fatal(err)
	}
	b, err := d.Create(bg, c, Spec{Name: "mgr-h1", CPU: 1, RAMMB: 2048, DiskGB: 20})
	if err != nil || b.State != StateRunning {
		t.Fatalf("name reuse after delete: %+v %v (paths %v)", b, err, paths)
	}
	if res, _ := bodies[len(bodies)-1]["resources"].(map[string]any); res["vcpu"] != 1.0 || res["memoryMb"] != 2048.0 || res["diskGb"] != 20.0 {
		t.Fatalf("resources %v", bodies[len(bodies)-1])
	}
	if err := d.Pause(bg, c, b.ID); err != nil {
		t.Fatal(err)
	}
	ch, err := d.Fork(bg, c, b.ID)
	if err != nil || ch.ID != "sbx_cccc" || ch.State != StateStarting {
		t.Fatalf("fork %+v %v", ch, err)
	}
	r, err := d.Exec(bg, c, b.ID, ExecRequest{Command: "uname -a"})
	if err != nil || r.ExitCode != 0 || !strings.Contains(r.Stdout, "x86_64") || r.DurationMS != 5 {
		t.Fatalf("exec %+v %v", r, err)
	}
	joined := strings.Join(paths, "\n")
	if !strings.Contains(joined, "/pause?wait=paused") || strings.Count(joined, "POST /api/v1/sandboxes?wait=running") != 3 {
		t.Fatalf("calls:\n%s", joined)
	}
	for _, z := range d.Info().Sizes {
		if z.DiskGB < 20 {
			t.Fatalf("size %s disk %d: must fit the coding-agent profile (20 GB)", z.SKU, z.DiskGB)
		}
	}
}
