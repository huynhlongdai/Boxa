package sandbox

import (
	"net"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"boxaz/core/internal/model"
	"boxaz/core/internal/proto"
)

func freePort(t *testing.T) int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func waitApp(t *testing.T, s *Shim, want string, d time.Duration) *model.AppStatus {
	t.Helper()
	deadline := time.Now().Add(d)
	var st model.AppStatus
	for time.Now().Before(deadline) {
		if err := s.Call(bg, proto.MAppGet, proto.BoxRef{BoxID: "b1"}, &st); err != nil {
			t.Fatal(err)
		}
		if st.State == want {
			return &st
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("app never reached %s: %+v", want, st)
	return nil
}

// TestShimAppSupervisor runs the T3 supervisor script for real (bash, python3).
func TestShimAppSupervisor(t *testing.T) {
	for _, b := range []string{"bash", "python3", "setsid", "curl"} {
		if _, err := exec.LookPath(b); err != nil {
			t.Skip(b + " not available")
		}
	}
	root := t.TempDir()
	t.Setenv("HOME", root)
	s := NewShim(&localDriver{root: root}, Cred{}, "l1")
	if err := s.Call(bg, proto.MCreate, proto.CreateParams{BoxID: "b1"}, nil); err != nil {
		t.Fatal(err)
	}
	port := freePort(t)
	spec := &model.AppSpec{Start: "echo booting; exec python3 -m http.server $PORT --bind 127.0.0.1", Install: "echo installed > marker", Port: port,
		Ready: &model.AppReady{HTTP: "/"}, Env: map[string]string{"GREETING": "it's"}, BackoffMaxS: 2}
	var st model.AppStatus
	if err := s.Call(bg, proto.MAppSet, proto.AppParams{BoxID: "b1", Spec: spec}, &st); err != nil {
		t.Fatal(err)
	}
	if st.Supervisor != "script" {
		t.Fatalf("supervisor %+v", st)
	}
	ready := waitApp(t, s, model.AppReadyState, 20*time.Second)
	if !ready.Ready || ready.PID == 0 || ready.StartedAt == nil {
		t.Fatalf("ready status %+v", ready)
	}
	// same spec again: no relaunch
	var st2 model.AppStatus
	s.Call(bg, proto.MAppSet, proto.AppParams{BoxID: "b1", Spec: spec}, &st2)
	if st2.PID != ready.PID {
		t.Fatalf("same spec relaunched the app: %d → %d", ready.PID, st2.PID)
	}
	// logs
	var lr proto.AppLogsResult
	if err := s.Call(bg, proto.MAppLogs, proto.AppLogsParams{BoxID: "b1", Offset: 0, Max: 4096}, &lr); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(lr.Data, "booting") || !strings.Contains(lr.Data, "install") || lr.Offset == 0 {
		t.Fatalf("logs %+v", lr)
	}
	// crash → restarted by the supervisor
	syscall.Kill(ready.PID, syscall.SIGKILL)
	time.Sleep(500 * time.Millisecond)
	again := waitApp(t, s, model.AppReadyState, 20*time.Second)
	if again.Restarts < 1 || again.PID == ready.PID || again.LastExit == nil {
		t.Fatalf("after kill %+v", again)
	}
	// supervisor gone (cold boot) → stopped; ensure relaunches it
	exec.Command("bash", "-c", killApp).Run()
	var gone model.AppStatus
	s.Call(bg, proto.MAppGet, proto.BoxRef{BoxID: "b1"}, &gone)
	if gone.State != model.AppStopped || gone.Ready {
		t.Fatalf("after supervisor death %+v", gone)
	}
	if err := s.Call(bg, proto.MAppEnsure, proto.BoxRef{BoxID: "b1"}, nil); err != nil {
		t.Fatal(err)
	}
	r3 := waitApp(t, s, model.AppReadyState, 20*time.Second)
	// explicit restart
	if err := s.Call(bg, proto.MAppRestart, proto.BoxRef{BoxID: "b1"}, nil); err != nil {
		t.Fatal(err)
	}
	r4 := waitApp(t, s, model.AppReadyState, 20*time.Second)
	if r4.PID == r3.PID {
		t.Fatal("restart kept the pid")
	}
	if c, err := net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(port)); err != nil {
		t.Fatal("app not serving", err)
	} else {
		c.Close()
	}
	// remove
	if err := s.Call(bg, proto.MAppSet, proto.AppParams{BoxID: "b1"}, &st); err != nil || st.State != model.AppNone {
		t.Fatalf("remove %+v %v", st, err)
	}
	time.Sleep(300 * time.Millisecond)
	if c, err := net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(port)); err == nil {
		c.Close()
		t.Fatal("app still serving after removal")
	}
}

// Everything the supervisor puts into a sandbox stays neutral (no product name).
func TestSupervisorScriptNeutral(t *testing.T) {
	sp := model.AppSpec{Repo: "https://github.com/heroku/node-js-sample", Install: "npm install", Start: "npm start", Port: 8000, Ready: &model.AppReady{HTTP: "/"}, Restart: model.RestartOnFailure}
	sp.Normalize()
	for _, s := range []string{SupervisorScript(sp, "/home/user/work/node-js-sample"), killApp, launchApp, statusApp} {
		if strings.Contains(strings.ToLower(s), "boxa") {
			t.Fatalf("product name in an in-sandbox script: %s", s)
		}
	}
	if !strings.Contains(SupervisorScript(sp, "/x"), `[ "$code" = 0 ] && { st exited`) {
		t.Fatal("on-failure policy missing")
	}
}
