package sandbox

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// logRT records every request/response (bodies trimmed) for BOXAZ_LIVE_LOG.
type logRT struct {
	w io.Writer
}

func (l logRT) RoundTrip(r *http.Request) (*http.Response, error) {
	var rb []byte
	if r.Body != nil {
		rb, _ = io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(rb))
	}
	t0 := time.Now()
	resp, err := http.DefaultTransport.RoundTrip(r)
	ms := time.Since(t0).Milliseconds()
	if err != nil {
		fmt.Fprintf(l.w, "%s %s -> ERR %v (%dms)\n", r.Method, r.URL.Path, err, ms)
		return nil, err
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body = io.NopCloser(bytes.NewReader(b))
	fmt.Fprintf(l.w, "%s %s?%s req=%s -> %d (%dms) %s\n\n", r.Method, r.URL.Path, r.URL.RawQuery, trim(string(rb), 300), resp.StatusCode, ms, trim(string(b), 1500))
	return resp, nil
}

// TestPrizedLiveLifecycle creates ONE real sandbox (free tier, 1 vCPU / 2 GB,
// maxRuntime 15 min), exercises exec/files/pause/resume, then deletes it.
// Spends a few cents of credit. Opt-in: BOXAZ_LIVE_PRIZED_CREATE=1.
func TestPrizedLiveLifecycle(t *testing.T) {
	if os.Getenv("BOXAZ_LIVE_PRIZED_CREATE") != "1" {
		t.Skip("set BOXAZ_LIVE_PRIZED_CREATE=1 and PRIZED_API_KEY to run (creates a sandbox)")
	}
	var w io.Writer = io.Discard
	if p := os.Getenv("BOXAZ_LIVE_LOG"); p != "" {
		f, err := os.Create(p)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		w = f
	}
	d := NewPrized(&http.Client{Timeout: 120 * time.Second, Transport: logRT{w}}, "")
	c := Cred{"token": os.Getenv("PRIZED_API_KEY")}
	ctx := context.Background()
	tm := func(name string, t0 time.Time) { t.Logf("TIMING %-18s %6d ms", name, time.Since(t0).Milliseconds()) }

	name := fmt.Sprintf("mgr-live-%d", time.Now().Unix())
	t0 := time.Now()
	sb, err := d.Create(ctx, c, Spec{Name: name, Template: "prized-base-v1", CPU: 1, RAMMB: 2048,
		Labels: map[string]string{LabelManaged: "live-test"}, TimeoutS: 15 * 60})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	tm("create(wait=run)", t0)
	defer func() {
		t1 := time.Now()
		if err := d.Delete(ctx, c, sb.ID); err != nil {
			t.Errorf("delete: %v", err)
		}
		tm("delete(202)", t1)
		for i := 0; i < 60; i++ {
			g, err := d.Get(ctx, c, sb.ID)
			if err == nil && g.State == StateDeleted {
				tm("delete->gone", t1)
				return
			}
			time.Sleep(time.Second)
		}
		t.Errorf("sandbox %s still not deleted after 60s", sb.ID)
	}()
	t.Logf("created state=%s", sb.State)
	if sb.State != StateRunning {
		t0 = time.Now()
		if sb, err = WaitRunning(ctx, d, c, sb.ID, 3*time.Minute); err != nil {
			t.Fatalf("wait running: %v", err)
		}
		tm("wait running", t0)
	}

	run := func(cmd string) *ExecResult {
		t0 := time.Now()
		r, err := d.Exec(ctx, c, sb.ID, ExecRequest{Command: cmd, TimeoutS: 60})
		if err != nil {
			t.Errorf("exec %q: %v", cmd, err)
			return nil
		}
		tm("exec "+trim(cmd, 12), t0)
		t.Logf("  exit=%d dur=%dms stdout=%q stderr=%q", r.ExitCode, r.DurationMS, trim(r.Stdout, 200), trim(r.Stderr, 200))
		return r
	}
	run("uname -a")
	if r := run("echo hi"); r != nil && strings.TrimSpace(r.Stdout) != "hi" {
		t.Errorf("echo: %q", r.Stdout)
	}
	run(`python3 -c 'import sys,platform; print(sys.version.split()[0], platform.machine())'`)
	if r := run("seq 1 200000"); r != nil {
		t.Logf("  long output: %d bytes, %d lines", len(r.Stdout), strings.Count(r.Stdout, "\n"))
	}
	run("pwd; id; nproc; free -m | head -2; df -h /home/prized | tail -1")
	if r := run("exit 3"); r != nil && r.ExitCode != 3 {
		t.Errorf("exit code: %d", r.ExitCode)
	}

	t0 = time.Now()
	data := []byte("hello from sandbox\n" + strings.Repeat("x", 4096))
	if err := d.PutFile(ctx, c, sb.ID, "/home/prized/work/probe.txt", data); err != nil {
		t.Errorf("put: %v", err)
	}
	tm("put file", t0)
	t0 = time.Now()
	got, err := d.GetFile(ctx, c, sb.ID, "/home/prized/work/probe.txt")
	if err != nil || !bytes.Equal(got, data) {
		t.Errorf("get: err=%v len=%d", err, len(got))
	}
	tm("get file", t0)

	t0 = time.Now()
	if err := d.Pause(ctx, c, sb.ID); err != nil {
		t.Errorf("pause: %v", err)
	} else {
		tm("pause(202)", t0)
		for i := 0; i < 60; i++ {
			g, _ := d.Get(ctx, c, sb.ID)
			if g != nil && g.State == StatePaused {
				raw := ""
				if g.Meta != nil {
					raw = g.Meta["raw_state"]
				}
				t.Logf("  paused state seen (%s)", raw)
				break
			}
			time.Sleep(time.Second)
		}
		tm("pause->paused", t0)
		t0 = time.Now()
		if err := d.Resume(ctx, c, sb.ID); err != nil {
			t.Errorf("resume: %v", err)
		}
		tm("resume", t0)
		if r := run("cat /home/prized/work/probe.txt | head -1"); r != nil && strings.TrimSpace(r.Stdout) != "hello from sandbox" {
			t.Errorf("file after resume: %q", r.Stdout)
		}
	}
	// fork (free tier: the child keeps the parent's expiry)
	t0 = time.Now()
	if ch, err := d.Fork(ctx, c, sb.ID); err != nil {
		t.Errorf("fork: %v", err)
	} else {
		tm("fork(202)", t0)
		if _, err := WaitRunning(ctx, d, c, ch.ID, 2*time.Minute); err != nil {
			t.Errorf("fork wait: %v", err)
		}
		tm("fork->running", t0)
		if r, err := d.Exec(ctx, c, ch.ID, ExecRequest{Command: "head -1 /home/prized/work/probe.txt"}); err != nil || strings.TrimSpace(r.Stdout) != "hello from sandbox" {
			t.Errorf("fork exec: %v %+v", err, r)
		}
		if err := d.Delete(ctx, c, ch.ID); err != nil {
			t.Errorf("fork delete: %v", err)
		}
	}
	l, err := d.List(ctx, c)
	if err != nil {
		t.Errorf("list: %v", err)
	}
	t.Logf("list: %d sandbox(es)", len(l))
}

// TestPrizedLiveNameReuse: a name (Idempotency-Key) reused after its sandbox
// was deleted must still create a new running sandbox. Opt-in as above.
func TestPrizedLiveNameReuse(t *testing.T) {
	if os.Getenv("BOXAZ_LIVE_PRIZED_CREATE") != "1" {
		t.Skip("set BOXAZ_LIVE_PRIZED_CREATE=1 and PRIZED_API_KEY to run (creates a sandbox)")
	}
	d := NewPrized(nil, "")
	c := Cred{"token": os.Getenv("PRIZED_API_KEY")}
	ctx := context.Background()
	spec := Spec{Name: fmt.Sprintf("mgr-reuse-%d", time.Now().Unix()), CPU: 1, RAMMB: 2048, TimeoutS: 600, Labels: map[string]string{LabelManaged: "live-test"}}
	a, err := d.Create(ctx, c, spec)
	if err != nil {
		t.Fatalf("create 1: %v", err)
	}
	_ = d.Delete(ctx, c, a.ID)
	time.Sleep(3 * time.Second)
	b, err := d.Create(ctx, c, spec)
	if err != nil {
		t.Fatalf("create 2: %v", err)
	}
	defer d.Delete(ctx, c, b.ID)
	if b.ID == a.ID || b.State != StateRunning {
		t.Fatalf("name reuse returned %s (%s), first was %s", b.ID, b.State, a.ID)
	}
}
