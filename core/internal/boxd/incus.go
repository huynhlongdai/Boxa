package boxd

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"boxa/core/internal/egress"
	"boxa/core/internal/proto"
)

// IncusDriver shells out to the `incus` CLI. Each box is an unprivileged
// system container named boxa-<id>. Untested in the Boxa CI sandbox (no Incus);
// every command is built with explicit args (no shell interpolation on the host).
type IncusDriver struct {
	Bin        string // default "incus"
	MemoryRoot string // host dir holding per-agent /memory volumes
	Images     map[string]string
	ExportDir  string // if set, snapshots are also exported as tarballs here
	EgressMode string // "acl" (default: Incus network ACL) | "nft" (nftables table per box)
	Resolver   egress.Resolver
	run        func(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, args ...string) error
	nft        func(ctx context.Context, script string) error // test hook
}

func NewIncusDriver(memRoot, exportDir string) *IncusDriver {
	d := &IncusDriver{
		Bin:        "incus",
		MemoryRoot: memRoot,
		ExportDir:  exportDir,
		Images: map[string]string{
			"base":           "images:ubuntu/24.04/cloud",
			"python":         "images:ubuntu/24.04/cloud",
			"node":           "images:ubuntu/24.04/cloud",
			"python-browser": "images:ubuntu/24.04/cloud",
		},
	}
	d.run = d.execIncus
	return d
}

func (d *IncusDriver) Name() string { return "incus" }

var incusNameRe = regexp.MustCompile(`[^a-z0-9-]`)

// cname converts a box id (box_xxx) to a valid Incus instance name.
func cname(id string) string {
	n := "boxa-" + incusNameRe.ReplaceAllString(strings.ToLower(strings.ReplaceAll(id, "_", "-")), "")
	if len(n) > 63 {
		n = n[:63]
	}
	return n
}

func (d *IncusDriver) execIncus(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, args ...string) error {
	cmd := exec.CommandContext(ctx, d.Bin, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	cmd.WaitDelay = 5 * time.Second
	return cmd.Run()
}

// incus runs a command and returns stdout, folding stderr into the error.
func (d *IncusDriver) incus(ctx context.Context, args ...string) (string, error) {
	var out, errb bytes.Buffer
	err := d.run(ctx, nil, &out, &errb, args...)
	if err != nil {
		msg := strings.TrimSpace(errb.String())
		if strings.Contains(msg, "not found") || strings.Contains(msg, "Instance not found") {
			return "", boxErr("NOT_FOUND", msg)
		}
		return out.String(), fmt.Errorf("incus %s: %v: %s", strings.Join(args, " "), err, msg)
	}
	return out.String(), nil
}

func (d *IncusDriver) Create(ctx context.Context, p proto.CreateParams) error {
	name := cname(p.BoxID)
	image := p.Image
	if image == "" {
		image = d.Images[p.Template]
	}
	if image == "" {
		image = d.Images["base"]
	}
	args := []string{"launch", image, name, "-c", "security.privileged=false", "-c", "security.nesting=false"}
	if p.Spec.CPU >= 1 {
		args = append(args, "-c", fmt.Sprintf("limits.cpu=%d", int(p.Spec.CPU)))
	} else if p.Spec.CPU > 0 {
		args = append(args, "-c", fmt.Sprintf("limits.cpu.allowance=%d%%", int(p.Spec.CPU*100)))
	}
	if p.Spec.RAMMB > 0 {
		args = append(args, "-c", fmt.Sprintf("limits.memory=%dMiB", p.Spec.RAMMB), "-c", "limits.memory.enforce=hard")
	}
	if p.Spec.DiskGB > 0 {
		args = append(args, "-d", fmt.Sprintf("root,size=%dGiB", p.Spec.DiskGB))
	}
	args = append(args, "-c", "limits.processes=4096")
	for k, v := range p.Env {
		args = append(args, "-c", "environment."+k+"="+v)
	}
	if _, err := d.incus(ctx, args...); err != nil {
		return err
	}
	// Per-agent memory volume at /memory.
	if d.MemoryRoot != "" && p.AgentID != "" {
		src := filepath.Join(d.MemoryRoot, filepath.Base(p.AgentID))
		if err := os.MkdirAll(src, 0o777); err == nil {
			_, _ = d.incus(ctx, "config", "device", "add", name, "memory", "disk", "source="+src, "path=/memory", "shift=true")
		}
	}
	// Wait for the container to accept exec.
	deadline := time.Now().Add(90 * time.Second)
	for {
		if _, err := d.incus(ctx, "exec", name, "--", "true"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			return boxErr("PROVIDER_FAILED", "container did not become ready in 90s")
		}
		time.Sleep(time.Second)
	}
	if _, err := d.incus(ctx, "exec", name, "--", "mkdir", "-p", "/work", "/memory"); err != nil {
		return err
	}
	if p.AgentsMD != "" {
		if err := d.Write(ctx, p.BoxID, "/AGENTS.md", []byte(p.AgentsMD)); err != nil {
			return err
		}
	}
	// Template bootstrap then user setup.
	setup := append(templateSetup(p.Template), p.Setup...)
	for _, c := range setup {
		var out bytes.Buffer
		code, err := d.Run(ctx, p.BoxID, proto.ExecParams{Command: c, TimeoutS: 900}, &out, &out)
		if err != nil {
			return err
		}
		if code != 0 {
			o := out.String()
			if len(o) > 2000 {
				o = o[len(o)-2000:]
			}
			return boxErr("SETUP_FAILED", fmt.Sprintf("setup %q exited %d: %s", c, code, o))
		}
	}
	return nil
}

// templateSetup installs runtimes for built-in templates.
func templateSetup(t string) []string {
	apt := "export DEBIAN_FRONTEND=noninteractive; (apt-get update -qq || true) && apt-get install -y -qq "
	switch t {
	case "python":
		return []string{apt + "python3 python3-pip python3-venv git curl ca-certificates >/dev/null"}
	case "node":
		return []string{apt + "nodejs npm git curl ca-certificates >/dev/null"}
	case "python-browser":
		return []string{apt + "python3 python3-pip python3-venv chromium git curl ca-certificates fonts-liberation >/dev/null"}
	case "base", "":
		return []string{apt + "git curl ca-certificates >/dev/null"}
	}
	return nil
}

func (d *IncusDriver) Start(ctx context.Context, id string) error {
	_, err := d.incus(ctx, "start", cname(id))
	if err != nil && strings.Contains(err.Error(), "already running") {
		return nil
	}
	return err
}

func (d *IncusDriver) Stop(ctx context.Context, id string) error {
	if _, err := d.incus(ctx, "stop", cname(id), "--timeout", "30"); err != nil {
		if strings.Contains(err.Error(), "already stopped") || strings.Contains(err.Error(), "not running") {
			return nil
		}
		_, err = d.incus(ctx, "stop", cname(id), "--force")
		return err
	}
	return nil
}

func (d *IncusDriver) Delete(ctx context.Context, id string) error {
	_, err := d.incus(ctx, "delete", cname(id), "--force")
	if pe, ok := err.(*proto.Error); ok && pe.Code == "NOT_FOUND" {
		return nil
	}
	return err
}

func (d *IncusDriver) List(ctx context.Context) ([]string, error) {
	out, err := d.incus(ctx, "list", "--format", "csv", "-c", "n")
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "boxa-box-") {
			ids = append(ids, "box_"+strings.TrimPrefix(l, "boxa-box-"))
		}
	}
	return ids, nil
}

func (d *IncusDriver) Run(ctx context.Context, id string, p proto.ExecParams, stdout, stderr io.Writer) (int, error) {
	timeout := p.TimeoutS
	if timeout <= 0 {
		timeout = 300
	}
	cwd := p.Cwd
	if cwd == "" {
		cwd = "/work"
	}
	args := []string{"exec", cname(id), "--cwd", cwd}
	keys := make([]string, 0, len(p.Env))
	for k := range p.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		args = append(args, "--env", k+"="+p.Env[k])
	}
	// The in-container `timeout` guarantees the process tree dies even if the
	// incus client is killed.
	args = append(args, "--", "timeout", "-s", "KILL", strconv.Itoa(timeout), "bash", "-lc", p.Command)
	cctx, cancel := context.WithTimeout(ctx, time.Duration(timeout+15)*time.Second)
	defer cancel()
	err := d.run(cctx, nil, stdout, stderr, args...)
	if err == nil {
		return 0, nil
	}
	if ee, ok := err.(*exec.ExitError); ok {
		code := ee.ExitCode()
		if code == 137 && cctx.Err() == nil {
			return 124, boxErr("TIMEOUT", fmt.Sprintf("command exceeded %ds and was killed", timeout))
		}
		return code, nil
	}
	if cctx.Err() != nil {
		return 124, boxErr("TIMEOUT", fmt.Sprintf("command exceeded %ds", timeout))
	}
	return -1, err
}

// sh runs a small helper command in the box and returns stdout.
func (d *IncusDriver) sh(ctx context.Context, id string, script string, args ...string) (string, int, error) {
	var out, errb bytes.Buffer
	full := append([]string{"exec", cname(id), "--", "bash", "-c", script, "boxa"}, args...)
	err := d.run(ctx, nil, &out, &errb, full...)
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return out.String(), ee.ExitCode(), nil
		}
		return "", -1, err
	}
	return out.String(), 0, nil
}

func normPath(p string) string {
	if p == "" {
		return "/work"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/work/" + p
	}
	return filepath.Clean(p)
}

func (d *IncusDriver) Read(ctx context.Context, id, path string) ([]byte, bool, []proto.Entry, error) {
	path = normPath(path)
	out, code, err := d.sh(ctx, id, `if [ -d "$1" ]; then echo dir; elif [ -f "$1" ]; then echo file; else exit 3; fi`, path)
	if err != nil {
		return nil, false, nil, err
	}
	if code == 3 {
		return nil, false, nil, boxErr("NOT_FOUND", "no such file: "+path)
	}
	if strings.TrimSpace(out) == "dir" {
		lst, _, err := d.sh(ctx, id, `find "$1" -mindepth 1 -maxdepth 1 -printf '%f\t%y\t%s\n' | head -n 5000`, path)
		if err != nil {
			return nil, true, nil, err
		}
		ents := []proto.Entry{}
		for _, l := range strings.Split(strings.TrimSpace(lst), "\n") {
			f := strings.Split(l, "\t")
			if len(f) != 3 {
				continue
			}
			t := map[string]string{"d": "dir", "f": "file", "l": "symlink"}[f[1]]
			if t == "" {
				t = "file"
			}
			sz, _ := strconv.ParseInt(f[2], 10, 64)
			ents = append(ents, proto.Entry{Name: f[0], Type: t, Size: sz})
		}
		return nil, true, ents, nil
	}
	var buf, errb bytes.Buffer
	if err := d.run(ctx, nil, &buf, &errb, "file", "pull", cname(id)+path, "-"); err != nil {
		return nil, false, nil, fmt.Errorf("file pull: %v: %s", err, errb.String())
	}
	return buf.Bytes(), false, nil, nil
}

func (d *IncusDriver) Write(ctx context.Context, id, path string, data []byte) error {
	path = normPath(path)
	tmp, err := os.CreateTemp("", "boxa-push-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	tmp.Close()
	_, err = d.incus(ctx, "file", "push", "--create-dirs", tmp.Name(), cname(id)+path)
	return err
}

func (d *IncusDriver) Grep(ctx context.Context, id, pattern, path string) (proto.GrepResult, error) {
	res := proto.GrepResult{Matches: []proto.GrepMatch{}}
	out, code, err := d.sh(ctx, id, `grep -rnIE --exclude-dir=.git --exclude-dir=node_modules -e "$1" -- "$2" 2>/dev/null | cut -c1-500 | head -n 501`, pattern, normPath(path))
	if err != nil {
		return res, err
	}
	if code > 1 {
		return res, boxErr("INVALID", "grep failed (bad pattern?)")
	}
	for _, l := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		parts := strings.SplitN(l, ":", 3)
		if len(parts) < 3 {
			continue
		}
		n, _ := strconv.Atoi(parts[1])
		if len(res.Matches) >= 500 {
			res.Truncated = true
			break
		}
		res.Matches = append(res.Matches, proto.GrepMatch{Path: parts[0], Line: n, Text: parts[2]})
	}
	return res, nil
}

func (d *IncusDriver) ChangedFiles(ctx context.Context, id string, since time.Time) ([]string, error) {
	out, _, err := d.sh(ctx, id, `find /work /memory -xdev -type f -newermt "@$1" -not -path '*/.git/*' -not -path '*/node_modules/*' 2>/dev/null | head -n 200`, strconv.FormatInt(since.Unix()-1, 10))
	if err != nil {
		return nil, err
	}
	var files []string
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if l != "" {
			files = append(files, l)
		}
	}
	sort.Strings(files)
	return files, nil
}

func (d *IncusDriver) Processes(ctx context.Context, id string) ([]proto.Proc, []int, error) {
	out, _, err := d.sh(ctx, id, `ps -eo pid=,args= | head -n 200; echo ---; ss -ltnH 2>/dev/null | awk '{print $4}'`)
	if err != nil {
		return nil, nil, err
	}
	parts := strings.SplitN(out, "---", 2)
	var procs []proto.Proc
	for _, l := range strings.Split(parts[0], "\n") {
		l = strings.TrimSpace(l)
		f := strings.SplitN(l, " ", 2)
		if len(f) != 2 {
			continue
		}
		pid, _ := strconv.Atoi(f[0])
		if strings.Contains(f[1], "ps -eo") {
			continue
		}
		procs = append(procs, proto.Proc{PID: pid, Cmd: strings.TrimSpace(f[1])})
	}
	ports := []int{}
	seen := map[int]bool{}
	if len(parts) == 2 {
		for _, l := range strings.Split(parts[1], "\n") {
			l = strings.TrimSpace(l)
			if i := strings.LastIndex(l, ":"); i >= 0 {
				if p, err := strconv.Atoi(l[i+1:]); err == nil && !seen[p] {
					seen[p] = true
					ports = append(ports, p)
				}
			}
		}
	}
	sort.Ints(ports)
	return procs, ports, nil
}

func (d *IncusDriver) Snapshot(ctx context.Context, id, snapID string) (string, int64, error) {
	name := cname(id)
	snap := incusNameRe.ReplaceAllString(strings.ReplaceAll(strings.ToLower(snapID), "_", "-"), "")
	if _, err := d.incus(ctx, "snapshot", "create", name, snap); err != nil {
		return "", 0, err
	}
	loc := "incus://" + name + "/" + snap
	var size int64
	if d.ExportDir != "" {
		os.MkdirAll(d.ExportDir, 0o755)
		dst := filepath.Join(d.ExportDir, snap+".tar.gz")
		if _, err := d.incus(ctx, "export", name, dst, "--instance-only", "--compression", "gzip"); err == nil {
			if st, err := os.Stat(dst); err == nil {
				size = st.Size()
				loc = "file://" + dst
			}
		}
	}
	return loc, size, nil
}

// ExportSnapshot exports the instance (with its snapshot) to a tar.gz for
// object storage upload. When ExportDir already produced a tarball it is reused.
func (d *IncusDriver) ExportSnapshot(ctx context.Context, id, snapID, dst string) error {
	if d.ExportDir != "" {
		src := filepath.Join(d.ExportDir, incusSnapName(snapID)+".tar.gz")
		if _, err := os.Stat(src); err == nil {
			if err := os.Link(src, dst); err == nil {
				return nil
			}
		}
	}
	_, err := d.incus(ctx, "export", cname(id), dst, "--instance-only", "--compression", "gzip")
	return err
}

func (d *IncusDriver) ShellCmd(ctx context.Context, id string) (*exec.Cmd, error) {
	return exec.Command(d.Bin, "exec", cname(id), "--cwd", "/work", "-t", "--", "bash", "-l"), nil
}

// Addr returns the container's IPv4 on the incus bridge.
func (d *IncusDriver) Addr(ctx context.Context, id string, port int) (string, error) {
	out, err := d.incus(ctx, "list", cname(id), "--format", "csv", "-c", "4")
	if err != nil {
		return "", err
	}
	for _, f := range strings.Fields(strings.ReplaceAll(out, ",", " ")) {
		if strings.Count(f, ".") == 3 {
			return fmt.Sprintf("%s:%d", f, port), nil
		}
	}
	return "", boxErr("NOT_FOUND", "container has no IPv4 (is it running?)")
}

func incusSnapName(snapID string) string {
	return incusNameRe.ReplaceAllString(strings.ReplaceAll(strings.ToLower(snapID), "_", "-"), "")
}

// Restore: in place → `incus snapshot restore`; fork → `incus copy src/snap dst`
// (falls back to `incus import` of the exported tarball when the source
// instance is gone), then apply the new box's limits and AGENTS.md.
func (d *IncusDriver) Restore(ctx context.Context, p proto.RestoreParams) error {
	dst, src, snap := cname(p.BoxID), cname(p.SourceBoxID), incusSnapName(p.SnapshotID)
	if !p.Create && p.SourceBoxID == p.BoxID {
		_, err := d.incus(ctx, "snapshot", "restore", dst, snap)
		return err
	}
	if !p.Create {
		// Restore another box's snapshot over this box: replace the instance.
		d.Stop(ctx, p.BoxID)
		if _, err := d.incus(ctx, "delete", dst, "--force"); err != nil {
			return err
		}
	}
	if _, err := d.incus(ctx, "copy", src+"/"+snap, dst); err != nil {
		file := strings.TrimPrefix(p.Location, "file://")
		if !strings.HasPrefix(p.Location, "file://") {
			return err
		}
		if _, err2 := d.incus(ctx, "import", file, dst); err2 != nil {
			return err
		}
	}
	if p.Create {
		if p.Box.Spec.CPU >= 1 {
			d.incus(ctx, "config", "set", dst, fmt.Sprintf("limits.cpu=%d", int(p.Box.Spec.CPU)))
		}
		if p.Box.Spec.RAMMB > 0 {
			d.incus(ctx, "config", "set", dst, fmt.Sprintf("limits.memory=%dMiB", p.Box.Spec.RAMMB))
		}
	}
	if err := d.Start(ctx, p.BoxID); err != nil {
		return err
	}
	deadline := time.Now().Add(90 * time.Second)
	for {
		if _, err := d.incus(ctx, "exec", dst, "--", "true"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			return boxErr("PROVIDER_FAILED", "restored container did not become ready in 90s")
		}
		time.Sleep(time.Second)
	}
	if p.Create && p.Box.AgentsMD != "" {
		return d.Write(ctx, p.BoxID, "/AGENTS.md", []byte(p.Box.AgentsMD))
	}
	return nil
}
