package boxd

import (
	"context"
	"encoding/json"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"boxaz/core/internal/model"
	"boxaz/core/internal/proto"
)

func freeTCPPort(t *testing.T) int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func appCall(t *testing.T, a *Agent, method string, params any) model.AppStatus {
	t.Helper()
	raw, _ := json.Marshal(params)
	res, err := a.handleApp(context.Background(), method, raw)
	if err != nil {
		t.Fatalf("%s: %v", method, err)
	}
	return *res.(*model.AppStatus)
}

func waitState(t *testing.T, a *Agent, want string, d time.Duration) model.AppStatus {
	t.Helper()
	deadline := time.Now().Add(d)
	var st model.AppStatus
	for time.Now().Before(deadline) {
		st = appCall(t, a, proto.MAppGet, proto.BoxRef{BoxID: "b1"})
		if st.State == want {
			return st
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("never reached %s: %+v", want, st)
	return st
}

func serving(port int) bool {
	c, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), time.Second)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

func TestSupervisorLocal(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}
	ctx := context.Background()
	dir := t.TempDir()
	drv, err := NewLocalDriver(dir + "/root")
	if err != nil {
		t.Fatal(err)
	}
	if err := drv.Create(ctx, proto.CreateParams{BoxID: "b1", Spec: model.Spec{CPU: 1, RAMMB: 512, DiskGB: 1}}); err != nil {
		t.Fatal(err)
	}
	a, err := NewAgent(Config{StateDir: dir + "/state"}, drv)
	if err != nil {
		t.Fatal(err)
	}
	port := freeTCPPort(t)
	spec := &model.AppSpec{Install: "echo once >> installs.txt", Start: "echo up; exec python3 -m http.server $PORT --bind 127.0.0.1",
		Port: port, Ready: &model.AppReady{HTTP: "/"}, Workdir: "/work/site", BackoffMaxS: 1}
	st := appCall(t, a, proto.MAppSet, proto.AppParams{BoxID: "b1", Spec: spec})
	if st.Supervisor != "boxd" {
		t.Fatalf("%+v", st)
	}
	r := waitState(t, a, model.AppReadyState, 15*time.Second)
	if !r.Ready || !serving(port) {
		t.Fatalf("not serving %+v", r)
	}
	// crash: kill whatever listens → supervisor restarts it
	exec.Command("bash", "-c", "pkill -f 'http.server "+strconv.Itoa(port)+"'").Run()
	time.Sleep(300 * time.Millisecond)
	r2 := waitState(t, a, model.AppReadyState, 15*time.Second)
	if r2.Restarts < 1 || r2.LastExit == nil {
		t.Fatalf("no restart recorded %+v", r2)
	}
	// logs: ring buffer from offset 0 and tail
	raw, _ := json.Marshal(proto.AppLogsParams{BoxID: "b1", Offset: 0, Max: 1 << 16})
	lr, _ := a.handleApp(ctx, proto.MAppLogs, raw)
	logs := lr.(proto.AppLogsResult)
	if !strings.Contains(logs.Data, "up") || !strings.Contains(logs.Data, "exited with code") || logs.Offset == 0 {
		t.Fatalf("logs %+v", logs)
	}
	// sleep: app stops; wake: it comes back
	a.appBoxStopped("b1")
	if serving(port) {
		t.Fatal("still serving while sleeping")
	}
	a.appBoxStarted("b1")
	waitState(t, a, model.AppReadyState, 15*time.Second)
	// boxd restart (cold resume): a new agent on the same state dir restarts the app
	a.stopAllApps()
	time.Sleep(200 * time.Millisecond)
	if serving(port) {
		t.Fatal("still serving after boxd stop")
	}
	a2, _ := NewAgent(Config{StateDir: dir + "/state"}, drv)
	a2.loadApps(ctx)
	waitState(t, a2, model.AppReadyState, 15*time.Second)
	// install ran once despite restarts
	b, _, _, _ := drv.Read(ctx, "b1", "/work/site/installs.txt")
	if strings.Count(string(b), "once") != 1 {
		t.Fatalf("install ran %q", b)
	}
	// restore → restart (install re-runs), explicit restart
	a2.appBoxRestored("b1")
	waitState(t, a2, model.AppReadyState, 15*time.Second)
	appCall(t, a2, proto.MAppRestart, proto.BoxRef{BoxID: "b1"})
	waitState(t, a2, model.AppReadyState, 15*time.Second)
	// restart=on-failure with a clean exit stays exited
	spec2 := &model.AppSpec{Start: "echo done", Restart: model.RestartOnFailure}
	appCall(t, a2, proto.MAppSet, proto.AppParams{BoxID: "b1", Spec: spec2})
	ex := waitState(t, a2, model.AppExited, 10*time.Second)
	if ex.LastExit == nil || *ex.LastExit != 0 || serving(port) {
		t.Fatalf("on-failure %+v", ex)
	}
	// delete removes the app
	a2.appBoxGone("b1")
	if s := appCall(t, a2, proto.MAppGet, proto.BoxRef{BoxID: "b1"}); s.State != model.AppNone {
		t.Fatalf("after delete %+v", s)
	}
}

func TestRing(t *testing.T) {
	r := ring{size: 8}
	r.write([]byte("abcdef"))
	r.write([]byte("ghij"))
	b, next, trunc := r.read(0, 100)
	if string(b) != "cdefghij" || next != 10 || !trunc {
		t.Fatalf("%q %d %v", b, next, trunc)
	}
	b, next, _ = r.read(-1, 3)
	if string(b) != "hij" || next != 10 {
		t.Fatalf("tail %q %d", b, next)
	}
	b, next, _ = r.read(10, 3)
	if string(b) != "" || next != 10 {
		t.Fatalf("eof %q", b)
	}
}
