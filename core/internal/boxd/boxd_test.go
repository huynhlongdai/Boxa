package boxd

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"boxaz/core/internal/model"
	"boxaz/core/internal/proto"
)

func TestShapeTail(t *testing.T) {
	var b strings.Builder
	for i := 1; i <= 100; i++ {
		b.WriteString(strings.Repeat("x", 3) + strconv.Itoa(i) + "\n")
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

func TestCompact(t *testing.T) {
	in := "\x1b[32mok\x1b[0m\nDownloading 10%\rDownloading 50%\rDownloading 100%\nsame\nsame\nsame\nsame\nend"
	out, did := Compact(in)
	if !did || out != "ok\nDownloading 100%\nsame  [×4]\nend" {
		t.Fatalf("compact: %q", out)
	}
	if o, d := Compact("a\nb\nb\nc"); d || o != "a\nb\nb\nc" {
		t.Fatalf("no-op compact changed output: %q", o)
	}
	var b strings.Builder
	for i := 0; i < 500; i++ {
		b.WriteString("\x1b[1mprogress line\x1b[0m\n")
	}
	tail, tr := ShapeBytes([]byte(b.String()), 60)
	if !tr || tail != "progress line  [×500]" {
		t.Fatalf("repeated output should collapse: %q", tail)
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
	if n := cname("box_ab3cd"); n != "boxaz-box-ab3cd" {
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
	for _, want := range []string{"launch images:ubuntu/24.04/cloud boxaz-box-x1", "limits.cpu.allowance=50%", "limits.memory=512MiB", "root,size=5GiB", "security.privileged=false"} {
		if !strings.Contains(launch, want) {
			t.Fatalf("launch %q missing %q", launch, want)
		}
	}
	last := calls[len(calls)-1]
	if !strings.Contains(last, "exec boxaz-box-x1 --cwd /work -- timeout -s KILL 900 bash -lc echo hi") {
		t.Fatalf("setup exec: %q", last)
	}
	calls = nil
	d.Run(ctx, "box_x1", proto.ExecParams{Command: "ls", Env: map[string]string{"B": "2", "A": "1"}, TimeoutS: 10}, io.Discard, io.Discard)
	if calls[0] != "exec boxaz-box-x1 --cwd /work --env A=1 --env B=2 -- timeout -s KILL 10 bash -lc ls" {
		t.Fatalf("run argv: %q", calls[0])
	}
}

func TestIncusRestoreFork(t *testing.T) {
	d := NewIncusDriver("", "")
	var calls []string
	d.run = func(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, args ...string) error {
		calls = append(calls, strings.Join(args, " "))
		return nil
	}
	ctx := context.Background()
	if err := d.Restore(ctx, proto.RestoreParams{BoxID: "box_a", SourceBoxID: "box_a", SnapshotID: "snap_X1"}); err != nil {
		t.Fatal(err)
	}
	if calls[0] != "snapshot restore boxaz-box-a snap-x1" {
		t.Fatalf("restore argv %q", calls[0])
	}
	calls = nil
	err := d.Restore(ctx, proto.RestoreParams{BoxID: "box_b", SourceBoxID: "box_a", SnapshotID: "snap_X1", Create: true,
		Box: proto.CreateParams{Spec: model.Spec{CPU: 2, RAMMB: 1024}}})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(calls, "\n")
	for _, want := range []string{"copy boxaz-box-a/snap-x1 boxaz-box-b", "config set boxaz-box-b limits.cpu=2", "config set boxaz-box-b limits.memory=1024MiB", "start boxaz-box-b"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("fork argv missing %q:\n%s", want, joined)
		}
	}
}

func TestLocalRestoreFork(t *testing.T) {
	d, _ := NewLocalDriver(t.TempDir())
	ctx := context.Background()
	if err := d.Create(ctx, proto.CreateParams{BoxID: "box_s", AgentsMD: "src"}); err != nil {
		t.Fatal(err)
	}
	d.Write(ctx, "box_s", "/work/a.txt", []byte("v1"))
	loc, _, err := d.Snapshot(ctx, "box_s", "snap_1")
	if err != nil {
		t.Fatal(err)
	}
	d.Write(ctx, "box_s", "/work/a.txt", []byte("v2"))
	d.Write(ctx, "box_s", "/work/b.txt", []byte("new"))
	if err := d.Restore(ctx, proto.RestoreParams{BoxID: "box_s", SourceBoxID: "box_s", SnapshotID: "snap_1", Location: loc}); err != nil {
		t.Fatal(err)
	}
	if b, _, _, _ := d.Read(ctx, "box_s", "/work/a.txt"); string(b) != "v1" {
		t.Fatalf("restored %q", b)
	}
	if _, _, _, err := d.Read(ctx, "box_s", "/work/b.txt"); err == nil {
		t.Fatal("b.txt survived restore")
	}
	if err := d.Restore(ctx, proto.RestoreParams{BoxID: "box_f", SourceBoxID: "box_s", SnapshotID: "snap_1", Location: loc, Create: true, Box: proto.CreateParams{AgentsMD: "fork"}}); err != nil {
		t.Fatal(err)
	}
	if b, _, _, _ := d.Read(ctx, "box_f", "/AGENTS.md"); string(b) != "fork" {
		t.Fatalf("fork AGENTS.md %q", b)
	}
	if b, _, _, _ := d.Read(ctx, "box_f", "/work/a.txt"); string(b) != "v1" {
		t.Fatalf("fork a.txt %q", b)
	}
	if err := d.Restore(ctx, proto.RestoreParams{BoxID: "box_f", SnapshotID: "snap_1", Location: loc, Create: true}); err == nil {
		t.Fatal("fork onto existing box accepted")
	}
}
