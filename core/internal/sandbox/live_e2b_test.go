package sandbox

import (
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// TestE2BLiveLifecycle creates ONE real E2B sandbox (template base, short
// timeout), exercises exec/files/pause/resume/fork, then deletes everything.
// Spends well under a cent of credit. Opt-in: BOXAZ_LIVE_E2B_CREATE=1 and
// E2B_API_KEY. BOXAZ_LIVE_E2B_PAUSE=0 / BOXAZ_LIVE_E2B_FORK=0 skip those steps.
func TestE2BLiveLifecycle(t *testing.T) {
	if os.Getenv("BOXAZ_LIVE_E2B_CREATE") != "1" {
		t.Skip("set BOXAZ_LIVE_E2B_CREATE=1 and E2B_API_KEY to run (creates a sandbox)")
	}
	liveE2BLike(t, func(c *http.Client) *E2B { return NewE2B(c, "") }, Cred{"token": os.Getenv("E2B_API_KEY")}, "")
}

// TestNovitaLiveLifecycle is the same for Novita Sandbox (E2B-compatible).
// Opt-in: BOXAZ_LIVE_NOVITA_CREATE=1 and NOVITA_API_KEY (NOVITA_REGION picks
// the region, default us-phx-1).
func TestNovitaLiveLifecycle(t *testing.T) {
	if os.Getenv("BOXAZ_LIVE_NOVITA_CREATE") != "1" {
		t.Skip("set BOXAZ_LIVE_NOVITA_CREATE=1 and NOVITA_API_KEY to run (creates a sandbox)")
	}
	liveE2BLike(t, func(c *http.Client) *E2B { return NewNovita(c, "") }, Cred{"token": os.Getenv("NOVITA_API_KEY")}, os.Getenv("NOVITA_REGION"))
}

func liveE2BLike(t *testing.T, mk func(*http.Client) *E2B, c Cred, region string) {
	var w io.Writer = io.Discard
	if p := os.Getenv("BOXAZ_LIVE_LOG"); p != "" {
		f, err := os.Create(p)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		w = f
	}
	d := mk(&http.Client{Timeout: 120 * time.Second, Transport: logRT{w}})
	ctx := context.Background()
	tm := func(name string, t0 time.Time) { t.Logf("TIMING %-18s %6d ms", name, time.Since(t0).Milliseconds()) }
	var created []string
	defer func() {
		for _, id := range created {
			if err := d.Delete(context.Background(), c, id); err != nil && !strings.Contains(err.Error(), "404") {
				t.Logf("cleanup delete %s: %v", id, err)
			}
		}
		if l, err := d.List(context.Background(), c); err == nil {
			t.Logf("left after cleanup: %d sandbox(es)", len(l))
		}
	}()

	t0 := time.Now()
	sb, err := d.Create(ctx, c, Spec{SKU: "base", Region: region, TimeoutS: 120, Labels: map[string]string{LabelManaged: "live-test"}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	created = append(created, sb.ID)
	tm("create", t0)
	t0 = time.Now()
	run := func(cmd string) *ExecResult {
		r, err := d.Exec(ctx, c, sb.ID, ExecRequest{Command: cmd, TimeoutS: 60})
		if err != nil {
			t.Fatalf("exec %q: %v", cmd, err)
		}
		return r
	}
	r := run("true")
	tm("create→first exec", t0)
	for _, cmd := range []string{"uname -a", "python3 --version", "nproc; free -m | sed -n 2p; df -BG --output=size,avail / | tail -1", "echo hi", "echo err >&2; exit 3"} {
		t0 = time.Now()
		r = run(cmd)
		t.Logf("exec %-22q %4d ms exit=%d out=%q err=%q", cmd, time.Since(t0).Milliseconds(), r.ExitCode, trim(r.Stdout, 120), trim(r.Stderr, 80))
	}
	if r.ExitCode != 3 || strings.TrimSpace(r.Stderr) != "err" {
		t.Fatalf("exit code/stderr lost: %+v", r)
	}
	t0 = time.Now()
	r = run("seq 1 200000")
	tm("exec seq 200k", t0)
	if n := strings.Count(r.Stdout, "\n"); n != 200000 || r.ExitCode != 0 {
		t.Fatalf("long output: %d lines exit %d", n, r.ExitCode)
	}
	r = run("pwd; whoami")
	t.Logf("pwd/whoami: %q", r.Stdout)

	data := []byte("hello from the live test\n" + strings.Repeat("x", 4096))
	t0 = time.Now()
	if err := d.PutFile(ctx, c, sb.ID, "/home/user/work/a.txt", data); err != nil {
		t.Fatalf("put: %v", err)
	}
	tm("file put", t0)
	t0 = time.Now()
	got, err := d.GetFile(ctx, c, sb.ID, "/home/user/work/a.txt")
	if err != nil || string(got) != string(data) {
		t.Fatalf("get: %v (%d bytes, want %d)", err, len(got), len(data))
	}
	tm("file get", t0)
	if r := run("head -1 /home/user/work/a.txt; stat -c %U /home/user/work/a.txt"); !strings.HasPrefix(r.Stdout, "hello from the live test") {
		t.Fatalf("file not visible to exec: %q", r.Stdout)
	}
	if g, err := d.Get(ctx, c, sb.ID); err != nil || g.State != StateRunning || g.Template != "base" {
		t.Fatalf("get state: %+v %v", g, err)
	}

	if os.Getenv("BOXAZ_LIVE_E2B_PAUSE") != "0" {
		t0 = time.Now()
		if err := d.Pause(ctx, c, sb.ID); err != nil {
			t.Logf("pause: %v (skipping resume)", err)
		} else {
			tm("pause", t0)
			g, _ := d.Get(ctx, c, sb.ID)
			t.Logf("state after pause: %+v", g)
			t0 = time.Now()
			if err := d.Resume(ctx, c, sb.ID); err != nil {
				t.Fatalf("resume: %v", err)
			}
			tm("resume", t0)
			t0 = time.Now()
			if r := run("head -1 /home/user/work/a.txt"); strings.TrimSpace(r.Stdout) != "hello from the live test" {
				t.Fatalf("file lost across pause/resume: %q", r.Stdout)
			}
			tm("resume→exec", t0)
		}
	}
	if os.Getenv("BOXAZ_LIVE_E2B_FORK") != "0" && d.Info().Caps.Fork {
		t0 = time.Now()
		ch, err := d.Fork(ctx, c, sb.ID)
		if err != nil {
			t.Logf("fork: %v", err)
		} else {
			created = append(created, ch.ID)
			tm("fork", t0)
			t0 = time.Now()
			r, err := d.Exec(ctx, c, ch.ID, ExecRequest{Command: "head -1 /home/user/work/a.txt", TimeoutS: 60})
			if err != nil || strings.TrimSpace(r.Stdout) != "hello from the live test" {
				t.Fatalf("fork exec: %v %+v", err, r)
			}
			tm("fork→exec", t0)
		}
	}
	for _, id := range created {
		t0 = time.Now()
		if err := d.Delete(ctx, c, id); err != nil {
			t.Fatalf("delete %s: %v", id, err)
		}
		tm("delete", t0)
		if g, err := d.Get(ctx, c, id); err != nil || g.State != StateDeleted {
			t.Fatalf("after delete: %+v %v", g, err)
		}
	}
	created = nil
}
