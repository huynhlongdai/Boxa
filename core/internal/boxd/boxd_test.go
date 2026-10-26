package boxd

import (
	"bytes"
	"io"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"boxa/core/internal/model"
	"boxa/core/internal/proto"
)

func TestShapeTail(t *testing.T) {
	var b strings.Builder
	for i := 1; i <= 100; i++ {
		b.WriteString(strings.Repeat("x", 3) + "\n")
	}
	tail, tr := ShapeBytes([]byte(b.String()), 60)
	if !tr || strings.Count(tail, "\n") != 59 {
		t.Fatalf("tail lines=%d truncated=%v", strings.Count(tail, "\n")+1, tr)
	}
	tail, tr = ShapeBytes([]byte("a\nb\n"), 60)
	if tr || tail != "a\nb" {
		t.Fatalf("%q %v", tail, tr)
	}
	if tokensSaved(4000, 400) != 900 {
		t.Fatal("tokens")
	}
	// Huge single line is capped and flagged.
	tail, tr = ShapeBytes(bytes.Repeat([]byte("y"), 100<<10), 60)
	if !tr || len(tail) > MaxTailBytes {
		t.Fatalf("cap: %d %v", len(tail), tr)
	}
}

func TestLocalDriver(t *testing.T) {
	d, err := NewLocalDriver(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := d.Create(ctx, proto.CreateParams{BoxID: "box_t1", AgentsMD: "# hi", Setup: []string{"echo ok > setup.txt"}}); err != nil {
		t.Fatal(err)
	}
	data, _, _, err := d.Read(ctx, "box_t1", "/work/setup.txt")
	if err != nil || string(data) != "ok\n" {
		t.Fatalf("setup: %q %v", data, err)
	}
	if _, err := d.resolve("box_t1", "../../../../etc/passwd"); err != nil {
		t.Fatal(err) // mapped inside /work, not an error
	}
	full, _ := d.resolve("box_t1", "../../../../etc/passwd")
	if !strings.HasPrefix(full, d.dir("box_t1")) {
		t.Fatalf("escaped: %s", full)
	}
	var out, errb bytes.Buffer
	code, err := d.Run(ctx, "box_t1", proto.ExecParams{Command: "sleep 5", TimeoutS: 1}, &out, &errb)
	if err == nil || code != 124 {
		t.Fatalf("timeout: code=%d err=%v", code, err)
	}
	start := time.Now()
	time.Sleep(20 * time.Millisecond)
	d.Write(ctx, "box_t1", "new.txt", []byte("n"))
	files, _ := d.ChangedFiles(ctx, "box_t1", start)
	if len(files) != 1 || files[0] != "/work/new.txt" {
		t.Fatalf("changed: %v", files)
	}
	if err := d.Stop(ctx, "box_t1"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Run(ctx, "box_t1", proto.ExecParams{Command: "true"}, &out, &errb); err == nil {
		t.Fatal("exec on stopped box should fail")
	}
	d.Start(ctx, "box_t1")
	loc, size, err := d.Snapshot(ctx, "box_t1", "snap_1")
	if err != nil || size == 0 || !strings.HasPrefix(loc, "local://") {
		t.Fatalf("snapshot %s %d %v", loc, size, err)
	}
	if _, err := os.Stat(filepath.Join(d.Root, "snapshots", "snap_1.tar.gz")); err != nil {
		t.Fatal(err)
	}
	if err := d.Delete(ctx, "box_t1"); err != nil {
		t.Fatal(err)
	}
}

func TestIncusArgv(t *testing.T) {
	if n := cname("box_ab3cd"); n != "boxa-box-ab3cd" {
		t.Fatal(n)
	}
	d := NewIncusDriver("", "")
	var calls []string
	d.run = func(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, args ...string) error {
		calls = append(calls, strings.Join(args, " "))
		return nil
	}
	ctx := context.Background()
	err := d.Create(ctx, proto.CreateParams{BoxID: "box_x1", Template: "python", Spec: model.Spec{CPU: 0.5, RAMMB: 512, DiskGB: 5}, Setup: []string{"echo hi"}})
	if err != nil {
		t.Fatal(err)
	}
	launch := calls[0]
	for _, want := range []string{"launch images:ubuntu/24.04/cloud boxa-box-x1", "limits.cpu.allowance=50%", "limits.memory=512MiB", "root,size=5GiB", "security.privileged=false"} {
		if !strings.Contains(launch, want) {
			t.Fatalf("launch %q missing %q", launch, want)
		}
	}
	last := calls[len(calls)-1]
	if !strings.Contains(last, "exec boxa-box-x1 --cwd /work -- timeout -s KILL 900 bash -lc echo hi") {
		t.Fatalf("setup exec: %q", last)
	}
	calls = nil
	d.Run(ctx, "box_x1", proto.ExecParams{Command: "ls", Env: map[string]string{"B": "2", "A": "1"}, TimeoutS: 10}, io.Discard, io.Discard)
	if calls[0] != "exec boxa-box-x1 --cwd /work --env A=1 --env B=2 -- timeout -s KILL 10 bash -lc ls" {
		t.Fatalf("run argv: %q", calls[0])
	}
}
