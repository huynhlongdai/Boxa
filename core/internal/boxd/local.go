package boxd

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"boxa/core/internal/proto"
)

// LocalDriver runs each box as a directory under Root. Commands run via bash
// with the box's work dir as cwd, in their own process group, with ulimits
// and timeouts. It provides NO real isolation: dev and tests only.
//
// Layout: <root>/boxes/<id>/{work,memory,AGENTS.md,.boxa.json}
// Path mapping for the file API: "/work/x" or "x" → work/x, "/memory/x" → memory/x,
// "/AGENTS.md" → AGENTS.md. Paths cannot escape the box dir.
type LocalDriver struct {
	Root string
	mu   sync.Mutex
}

type localMeta struct {
	ID      string            `json:"id"`
	Stopped bool              `json:"stopped"`
	CPU     float64           `json:"cpu"`
	RAMMB   int               `json:"ram_mb"`
	DiskGB  int               `json:"disk_gb"`
	Env     map[string]string `json:"env"`
}

func NewLocalDriver(root string) (*LocalDriver, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(abs, "boxes"), 0o755); err != nil {
		return nil, err
	}
	os.MkdirAll(filepath.Join(abs, "snapshots"), 0o755)
	return &LocalDriver{Root: abs}, nil
}

func (d *LocalDriver) Name() string { return "local" }

func (d *LocalDriver) dir(id string) string { return filepath.Join(d.Root, "boxes", filepath.Base(id)) }

func (d *LocalDriver) meta(id string) (*localMeta, error) {
	b, err := os.ReadFile(filepath.Join(d.dir(id), ".boxa.json"))
	if err != nil {
		return nil, boxErr("NOT_FOUND", "box not on this host: "+id)
	}
	var m localMeta
	return &m, json.Unmarshal(b, &m)
}

func (d *LocalDriver) saveMeta(m *localMeta) error {
	b, _ := json.MarshalIndent(m, "", "  ")
	return os.WriteFile(filepath.Join(d.dir(m.ID), ".boxa.json"), b, 0o600)
}

func (d *LocalDriver) Create(ctx context.Context, p proto.CreateParams) error {
	dir := d.dir(p.BoxID)
	for _, sub := range []string{"work", "memory"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			return err
		}
	}
	if p.AgentsMD != "" {
		os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte(p.AgentsMD), 0o644)
	}
	m := &localMeta{ID: p.BoxID, CPU: p.Spec.CPU, RAMMB: p.Spec.RAMMB, DiskGB: p.Spec.DiskGB, Env: p.Env}
	if err := d.saveMeta(m); err != nil {
		return err
	}
	for _, cmd := range p.Setup {
		var out strings.Builder
		code, err := d.Run(ctx, p.BoxID, proto.ExecParams{BoxID: p.BoxID, Command: cmd, TimeoutS: 600}, &out, &out)
		if err != nil {
			return err
		}
		if code != 0 {
			o := out.String()
			if len(o) > 2000 {
				o = o[len(o)-2000:]
			}
			return boxErr("SETUP_FAILED", fmt.Sprintf("setup %q exited %d: %s", cmd, code, o))
		}
	}
	return nil
}

func (d *LocalDriver) setStopped(id string, v bool) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	m, err := d.meta(id)
	if err != nil {
		return err
	}
	m.Stopped = v
	return d.saveMeta(m)
}

func (d *LocalDriver) Start(ctx context.Context, id string) error { return d.setStopped(id, false) }

func (d *LocalDriver) Stop(ctx context.Context, id string) error {
	d.killBoxProcs(id)
	return d.setStopped(id, true)
}

func (d *LocalDriver) Delete(ctx context.Context, id string) error {
	d.killBoxProcs(id)
	return os.RemoveAll(d.dir(id))
}

func (d *LocalDriver) List(ctx context.Context) ([]string, error) {
	ents, err := os.ReadDir(filepath.Join(d.Root, "boxes"))
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range ents {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

func (d *LocalDriver) env(id string, m *localMeta, extra map[string]string) []string {
	dir := d.dir(id)
	env := []string{
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"HOME=" + filepath.Join(dir, "work"),
		"BOX_ID=" + id,
		"BOX_WORK=" + filepath.Join(dir, "work"),
		"BOX_MEMORY=" + filepath.Join(dir, "memory"),
		"LANG=C.UTF-8",
		"TERM=xterm-256color",
	}
	for k, v := range m.Env {
		env = append(env, k+"="+v)
	}
	for k, v := range extra {
		env = append(env, k+"="+v)
	}
	return env
}

func (d *LocalDriver) Run(ctx context.Context, id string, p proto.ExecParams, stdout, stderr io.Writer) (int, error) {
	m, err := d.meta(id)
	if err != nil {
		return -1, err
	}
	if m.Stopped {
		return -1, boxErr("BOX_SLEEPING", "box is sleeping")
	}
	cwd := filepath.Join(d.dir(id), "work")
	if p.Cwd != "" {
		if c, err := d.resolve(id, p.Cwd); err == nil {
			cwd = c
		}
	}
	timeout := time.Duration(p.TimeoutS) * time.Second
	if timeout <= 0 {
		timeout = 300 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	limits := "ulimit -n 4096 2>/dev/null; "
	if m.DiskGB > 0 {
		// file size limit in 1K blocks
		limits += fmt.Sprintf("ulimit -f %d 2>/dev/null; ", int64(m.DiskGB)*1024*1024)
	}
	if os.Getenv("BOXD_LOCAL_ULIMIT_MEM") == "1" && m.RAMMB > 0 {
		limits += fmt.Sprintf("ulimit -v %d 2>/dev/null; ", int64(m.RAMMB)*1024*2)
	}
	cmd := exec.Command("bash", "-c", limits+p.Command)
	cmd.Dir = cwd
	cmd.Env = d.env(id, m, p.Env)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return -1, err
	}
	d.trackPID(id, cmd.Process.Pid)
	defer d.untrackPID(id, cmd.Process.Pid)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			if ee, ok := err.(*exec.ExitError); ok {
				return ee.ExitCode(), nil
			}
			return -1, err
		}
		return 0, nil
	case <-ctx.Done():
		syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-done
		return 124, boxErr("TIMEOUT", fmt.Sprintf("command exceeded %s and was killed", timeout))
	}
}

var pidMu sync.Mutex
var boxPIDs = map[string]map[int]bool{}

func (d *LocalDriver) trackPID(id string, pid int) {
	pidMu.Lock()
	defer pidMu.Unlock()
	if boxPIDs[id] == nil {
		boxPIDs[id] = map[int]bool{}
	}
	boxPIDs[id][pid] = true
}

func (d *LocalDriver) untrackPID(id string, pid int) {
	pidMu.Lock()
	defer pidMu.Unlock()
	delete(boxPIDs[id], pid)
}

func (d *LocalDriver) killBoxProcs(id string) {
	pidMu.Lock()
	pids := boxPIDs[id]
	pidMu.Unlock()
	for pid := range pids {
		syscall.Kill(-pid, syscall.SIGKILL)
	}
	// also processes whose cwd is inside the box (background daemons)
	for _, p := range d.procsInBox(id) {
		syscall.Kill(p.PID, syscall.SIGKILL)
	}
}

// resolve maps a box path to a host path, refusing escapes.
func (d *LocalDriver) resolve(id, p string) (string, error) {
	base := d.dir(id)
	clean := filepath.Clean("/" + p)
	var rel string
	switch {
	case clean == "/work" || strings.HasPrefix(clean, "/work/"):
		rel = clean
	case clean == "/memory" || strings.HasPrefix(clean, "/memory/"):
		rel = clean
	case clean == "/AGENTS.md":
		rel = clean
	case strings.HasPrefix(p, "/"):
		rel = "/work" + clean // treat other absolute paths as relative to /work
	default:
		rel = "/work" + clean
	}
	full := filepath.Join(base, rel)
	if full != base && !strings.HasPrefix(full, base+string(os.PathSeparator)) {
		return "", boxErr("POLICY_DENIED", "path escapes box")
	}
	return full, nil
}

func (d *LocalDriver) unresolve(id, full string) string {
	r, err := filepath.Rel(d.dir(id), full)
	if err != nil {
		return full
	}
	return "/" + filepath.ToSlash(r)
}

func (d *LocalDriver) Read(ctx context.Context, id, path string) ([]byte, bool, []proto.Entry, error) {
	if _, err := d.meta(id); err != nil {
		return nil, false, nil, err
	}
	if path == "" {
		path = "/work"
	}
	full, err := d.resolve(id, path)
	if err != nil {
		return nil, false, nil, err
	}
	st, err := os.Stat(full)
	if err != nil {
		return nil, false, nil, boxErr("NOT_FOUND", "no such file: "+path)
	}
	if st.IsDir() {
		ents, err := os.ReadDir(full)
		if err != nil {
			return nil, true, nil, err
		}
		out := []proto.Entry{}
		for _, e := range ents {
			if e.Name() == ".boxa.json" {
				continue
			}
			info, _ := e.Info()
			t := "file"
			if e.IsDir() {
				t = "dir"
			} else if e.Type()&fs.ModeSymlink != 0 {
				t = "symlink"
			}
			var size int64
			if info != nil {
				size = info.Size()
			}
			out = append(out, proto.Entry{Name: e.Name(), Type: t, Size: size})
		}
		return nil, true, out, nil
	}
	if st.Size() > 50<<20 {
		return nil, false, nil, boxErr("INVALID", "file larger than 50MB; read a line range or use exec")
	}
	b, err := os.ReadFile(full)
	return b, false, nil, err
}

func (d *LocalDriver) Write(ctx context.Context, id, path string, data []byte) error {
	if _, err := d.meta(id); err != nil {
		return err
	}
	full, err := d.resolve(id, path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	return os.WriteFile(full, data, 0o644)
}

func (d *LocalDriver) Grep(ctx context.Context, id, pattern, path string) (proto.GrepResult, error) {
	res := proto.GrepResult{Matches: []proto.GrepMatch{}}
	if _, err := d.meta(id); err != nil {
		return res, err
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return res, boxErr("INVALID", "bad regexp: "+err.Error())
	}
	if path == "" {
		path = "/work"
	}
	root, err := d.resolve(id, path)
	if err != nil {
		return res, err
	}
	const max = 500
	filepath.WalkDir(root, func(p string, e fs.DirEntry, err error) error {
		if err != nil || ctx.Err() != nil {
			return nil
		}
		if e.IsDir() {
			n := e.Name()
			if n == ".git" || n == "node_modules" || n == "__pycache__" {
				return filepath.SkipDir
			}
			return nil
		}
		info, _ := e.Info()
		if info == nil || info.Size() > 5<<20 || e.Name() == ".boxa.json" {
			return nil
		}
		f, err := os.Open(p)
		if err != nil {
			return nil
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		ln := 0
		for sc.Scan() {
			ln++
			t := sc.Text()
			if strings.IndexByte(t, 0) >= 0 {
				return nil // binary
			}
			if re.MatchString(t) {
				if len(res.Matches) >= max {
					res.Truncated = true
					return filepath.SkipAll
				}
				if len(t) > 400 {
					t = t[:400]
				}
				res.Matches = append(res.Matches, proto.GrepMatch{Path: d.unresolve(id, p), Line: ln, Text: t})
			}
		}
		return nil
	})
	return res, nil
}

func (d *LocalDriver) ChangedFiles(ctx context.Context, id string, since time.Time) ([]string, error) {
	var out []string
	base := d.dir(id)
	for _, sub := range []string{"work", "memory"} {
		filepath.WalkDir(filepath.Join(base, sub), func(p string, e fs.DirEntry, err error) error {
			if err != nil || len(out) >= 200 {
				return nil
			}
			if e.IsDir() {
				if e.Name() == ".git" || e.Name() == "node_modules" {
					return filepath.SkipDir
				}
				return nil
			}
			if info, err := e.Info(); err == nil && !info.ModTime().Before(since) {
				out = append(out, d.unresolve(id, p))
			}
			return nil
		})
	}
	sort.Strings(out)
	return out, nil
}

func (d *LocalDriver) procsInBox(id string) []proto.Proc {
	base := d.dir(id)
	var out []proto.Proc
	ents, _ := os.ReadDir("/proc")
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		cwd, err := os.Readlink(filepath.Join("/proc", e.Name(), "cwd"))
		if err != nil || !(cwd == base || strings.HasPrefix(cwd, base+"/")) {
			continue
		}
		cmdline, _ := os.ReadFile(filepath.Join("/proc", e.Name(), "cmdline"))
		c := strings.TrimSpace(strings.ReplaceAll(string(cmdline), "\x00", " "))
		out = append(out, proto.Proc{PID: pid, Cmd: c})
	}
	return out
}

func (d *LocalDriver) Processes(ctx context.Context, id string) ([]proto.Proc, []int, error) {
	if _, err := d.meta(id); err != nil {
		return nil, nil, err
	}
	return d.procsInBox(id), []int{}, nil
}

// Snapshot writes a tar.gz of the box dir.
func (d *LocalDriver) Snapshot(ctx context.Context, id, snapID string) (string, int64, error) {
	if _, err := d.meta(id); err != nil {
		return "", 0, err
	}
	dst := filepath.Join(d.Root, "snapshots", filepath.Base(snapID)+".tar.gz")
	f, err := os.Create(dst)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	base := d.dir(id)
	err = filepath.Walk(base, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(base, p)
		if rel == "." {
			return nil
		}
		link := ""
		if info.Mode()&os.ModeSymlink != 0 {
			link, _ = os.Readlink(p)
		}
		hdr, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return nil
		}
		hdr.Name = filepath.ToSlash(rel)
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			src, err := os.Open(p)
			if err != nil {
				return nil
			}
			_, err = io.Copy(tw, src)
			src.Close()
			return err
		}
		return nil
	})
	if err != nil {
		return "", 0, err
	}
	tw.Close()
	gz.Close()
	st, _ := f.Stat()
	return "local://" + dst, st.Size(), nil
}

func (d *LocalDriver) ShellCmd(ctx context.Context, id string) (*exec.Cmd, error) {
	m, err := d.meta(id)
	if err != nil {
		return nil, err
	}
	if m.Stopped {
		return nil, boxErr("BOX_SLEEPING", "box is sleeping")
	}
	cmd := exec.Command("bash", "--noprofile", "--norc", "-i")
	cmd.Dir = filepath.Join(d.dir(id), "work")
	cmd.Env = append(d.env(id, m, nil), "PS1=box:\\w$ ")
	return cmd, nil
}

// Addr: local boxes share the host network namespace.
func (d *LocalDriver) Addr(ctx context.Context, id string, port int) (string, error) {
	if _, err := d.meta(id); err != nil {
		return "", err
	}
	return fmt.Sprintf("127.0.0.1:%d", port), nil
}
