package manager

// v1.11 SFTP that always works. The SSH gateway's "sftp" subsystem runs the
// box's own sftp-server when it has one (fastest, exact semantics); when it
// does not (slim images, distroless, most sandboxes) core serves SFTP itself
// (github.com/pkg/sftp request server) and maps every request onto small
// POSIX shell commands run in the box over proc.open (cat, stat, find, mv,
// mkdir, rm, ln, readlink, chmod, touch, truncate). So `sftp`, `scp` (OpenSSH
// ≥ 9 speaks SFTP) and editors' remote file browsers work through
// `ProxyCommand box ssh-proxy %h` on every tier with a live link.
//
// Reads fetch the file once into a temporary file on core (random access for
// pipelined clients); writes collect into a temporary file and are uploaded
// when the client closes the handle. BOXMES_SFTP=core forces the core server
// (tests), BOXMES_SFTP=box disables it.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"boxmes/core/internal/hostlink"
	"boxmes/core/internal/proto"
)

// sftpServerPaths are where distributions install OpenSSH's sftp-server.
var sftpServerPaths = []string{"/usr/lib/openssh/sftp-server", "/usr/libexec/openssh/sftp-server", "/usr/lib/ssh/sftp-server", "/usr/libexec/sftp-server", "/usr/lib/sftp-server"}

// sftpProbe prints the box's sftp-server (if any) and its home directory.
const sftpProbe = `S=; for p in ` + "/usr/lib/openssh/sftp-server /usr/libexec/openssh/sftp-server /usr/lib/ssh/sftp-server /usr/libexec/sftp-server /usr/lib/sftp-server" + `; do if [ -x "$p" ]; then S=$p; break; fi; done; echo "server=$S"; echo "home=${HOME:-/}"; echo "pwd=$(pwd)"`

// boxRun runs a shell command in the box with pipes and returns its output.
func boxRun(ctx context.Context, peer *hostlink.Peer, boxID, command string, env []string, stdin io.Reader, stdout io.Writer) (stderr string, code int, err error) {
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	pr, err := peer.OpenProc(cctx, proto.ProcOpenParams{BoxID: boxID, Command: command, Env: env})
	cancel()
	if err != nil {
		return "", -1, err
	}
	defer pr.Close()
	go func() {
		if stdin != nil {
			io.Copy(pr.Stdio, stdin)
		}
		pr.CloseStdin()
	}()
	var eb bytes.Buffer
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { io.Copy(&eb, io.LimitReader(pr.Stderr, 64<<10)); io.Copy(io.Discard, pr.Stderr); wg.Done() }()
	if stdout == nil {
		stdout = io.Discard
	}
	_, cerr := io.Copy(stdout, pr.Stdio)
	wg.Wait()
	code = pr.Wait()
	if cerr != nil && code == 0 {
		return eb.String(), code, cerr
	}
	return eb.String(), code, nil
}

// sftpMode is BOXMES_SFTP: auto (default) | core | box.
func (m *Manager) sftpMode() string {
	switch v := strings.ToLower(strings.TrimSpace(m.Cfg.SFTPMode)); v {
	case "core", "box":
		return v
	}
	return "auto"
}

// serveSFTP answers the "sftp" subsystem on ch: the box's sftp-server when
// present, else core's own SFTP server. It returns the exit code.
func (m *Manager) serveSFTP(ctx context.Context, boxID string, peer *hostlink.Peer, ch ssh.Channel) int {
	var out bytes.Buffer
	_, _, err := boxRun(ctx, peer, boxID, sftpProbe, nil, nil, &out)
	if err != nil {
		fmt.Fprintf(ch.Stderr(), "boxmes: sftp: %v\r\n", err)
		return 255
	}
	info := map[string]string{}
	for _, l := range strings.Split(out.String(), "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(l), "="); ok {
			info[k] = v
		}
	}
	mode := m.sftpMode()
	if info["server"] != "" && mode != "core" {
		return m.sftpViaBox(ctx, boxID, peer, ch, info["server"])
	}
	if mode == "box" {
		fmt.Fprintf(ch.Stderr(), "boxmes: this box has no sftp-server and BOXMES_SFTP=box\r\n")
		return 127
	}
	start := info["home"]
	if start == "" || !strings.HasPrefix(start, "/") {
		start = "/"
	}
	fs := &boxFS{ctx: ctx, peer: peer, boxID: boxID}
	// RequestServer.Close closes its connection; keep the channel open so the
	// caller can still send exit-status (OpenSSH 9+ scp, which runs over SFTP,
	// fails without it).
	srv := sftp.NewRequestServer(keepOpen{ch}, sftp.Handlers{FileGet: fs, FilePut: fs, FileCmd: fs, FileList: fs}, sftp.WithStartDirectory(start))
	err = srv.Serve()
	srv.Close()
	if err != nil && !errors.Is(err, io.EOF) {
		return 1
	}
	return 0
}

func (m *Manager) sftpViaBox(ctx context.Context, boxID string, peer *hostlink.Peer, ch ssh.Channel, server string) int {
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	pr, err := peer.OpenProc(cctx, proto.ProcOpenParams{BoxID: boxID, Command: "exec " + shQuote(server)})
	cancel()
	if err != nil {
		fmt.Fprintf(ch.Stderr(), "boxmes: sftp: %v\r\n", err)
		return 255
	}
	go func() {
		io.Copy(pr.Stdio, ch)
		pr.CloseStdin()
	}()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { io.Copy(ch, pr.Stdio); wg.Done() }()
	go func() { io.Copy(ch.Stderr(), pr.Stderr); wg.Done() }()
	wg.Wait()
	code := pr.Wait()
	if code < 0 {
		code = 255
	}
	return code
}

// keepOpen hides Close from the SFTP request server (see serveSFTP).
type keepOpen struct{ io.ReadWriter }

func (keepOpen) Close() error { return nil }

func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// ---- core-side SFTP backed by shell commands in the box ----

type boxFS struct {
	ctx   context.Context
	peer  *hostlink.Peer
	boxID string
}

// run executes a command with P (and Q) set to paths; errors map to os errors.
func (f *boxFS) run(command string, env []string, stdin io.Reader, stdout io.Writer) error {
	stderr, code, err := boxRun(f.ctx, f.peer, f.boxID, command, env, stdin, stdout)
	if err != nil {
		return err
	}
	if code != 0 {
		return shErr(stderr, code)
	}
	return nil
}

func shErr(stderr string, code int) error {
	s := strings.ToLower(stderr)
	switch {
	case strings.Contains(s, "no such file") || strings.Contains(s, "not found") || strings.Contains(s, "cannot stat") || strings.Contains(s, "can't stat"):
		return os.ErrNotExist
	case strings.Contains(s, "permission denied") || strings.Contains(s, "operation not permitted") || strings.Contains(s, "read-only"):
		return os.ErrPermission
	case strings.Contains(s, "file exists"):
		return os.ErrExist
	}
	msg := strings.TrimSpace(stderr)
	if msg == "" {
		msg = "exit " + strconv.Itoa(code)
	}
	return errors.New(msg)
}

// tmpFile is a temporary file on core removed on Close.
type tmpFile struct{ *os.File }

func (t *tmpFile) Close() error {
	err := t.File.Close()
	os.Remove(t.File.Name())
	return err
}

func newTmp() (*tmpFile, error) {
	f, err := os.CreateTemp("", "bx-sftp-*")
	if err != nil {
		return nil, err
	}
	return &tmpFile{f}, nil
}

// Fileread downloads the whole file once (random access for the client).
func (f *boxFS) Fileread(r *sftp.Request) (io.ReaderAt, error) {
	t, err := newTmp()
	if err != nil {
		return nil, err
	}
	if err := f.run(`if [ -d "$P" ]; then echo "$P: is a directory" >&2; exit 1; fi; exec cat -- "$P"`, []string{"P=" + r.Filepath}, nil, t.File); err != nil {
		t.Close()
		return nil, err
	}
	return t, nil
}

// boxWriter collects writes and uploads them when the handle closes.
type boxWriter struct {
	*tmpFile
	f      *boxFS
	path   string
	append bool
	once   sync.Once
	err    error
}

func (w *boxWriter) Close() error {
	w.once.Do(func() {
		if _, err := w.tmpFile.Seek(0, io.SeekStart); err != nil {
			w.err = err
		} else {
			cmd := `cat > "$P"`
			if w.append {
				cmd = `cat >> "$P"`
			}
			w.err = w.f.run(cmd, []string{"P=" + w.path}, w.tmpFile.File, nil)
		}
		w.tmpFile.Close()
	})
	return w.err
}

// Filewrite: writes go to a temp file, uploaded on Close. Without TRUNC the
// existing content is fetched first so offset writes (resume) keep it.
func (f *boxFS) Filewrite(r *sftp.Request) (io.WriterAt, error) {
	t, err := newTmp()
	if err != nil {
		return nil, err
	}
	fl := r.Pflags()
	if !fl.Trunc && !fl.Append {
		if err := f.run(`[ -e "$P" ] || exit 0; exec cat -- "$P"`, []string{"P=" + r.Filepath}, nil, t.File); err != nil {
			t.Close()
			return nil, err
		}
	} else if err := f.run(`d=$(dirname -- "$P"); [ -d "$d" ] || { echo "$d: No such file or directory" >&2; exit 1; }; [ -w "$d" ] || [ -w "$P" ] || { echo "$P: Permission denied" >&2; exit 1; }`, []string{"P=" + r.Filepath}, nil, nil); err != nil {
		t.Close()
		return nil, err
	}
	return &boxWriter{tmpFile: t, f: f, path: r.Filepath, append: fl.Append}, nil
}

// Filecmd: Setstat, Rename, Rmdir, Mkdir, Link, Symlink, Remove.
func (f *boxFS) Filecmd(r *sftp.Request) error {
	env := []string{"P=" + r.Filepath, "Q=" + r.Target}
	switch r.Method {
	case "Setstat":
		a := r.Attributes()
		fl := r.AttrFlags()
		var cmds []string
		if fl.Permissions {
			cmds = append(cmds, fmt.Sprintf(`chmod %o "$P"`, a.Mode&0o7777))
		}
		if fl.Size {
			cmds = append(cmds, fmt.Sprintf(`truncate -s %d "$P" 2>/dev/null || { [ %d = 0 ] && : > "$P"; }`, a.Size, a.Size))
		}
		if fl.Acmodtime {
			cmds = append(cmds, fmt.Sprintf(`touch -m -d @%d "$P" 2>/dev/null || touch -m -d "$(date -d @%d '+%%Y-%%m-%%d %%H:%%M:%%S' 2>/dev/null)" "$P" 2>/dev/null || true`, a.Mtime, a.Mtime))
		}
		if fl.UidGid {
			cmds = append(cmds, fmt.Sprintf(`chown %d:%d "$P" 2>/dev/null || true`, a.UID, a.GID))
		}
		if len(cmds) == 0 {
			return nil
		}
		return f.run(strings.Join(cmds, " && "), env, nil, nil)
	case "Rename":
		return f.run(`[ -e "$Q" ] && { echo "$Q: File exists" >&2; exit 1; }; mv -- "$P" "$Q"`, env, nil, nil)
	case "Rmdir":
		return f.run(`rmdir -- "$P"`, env, nil, nil)
	case "Mkdir":
		return f.run(`mkdir -- "$P"`, env, nil, nil)
	case "Link":
		return f.run(`ln -- "$P" "$Q"`, env, nil, nil)
	case "Symlink":
		// SFTP v3 SYMLINK: Filepath = target, Target = link path (pkg/sftp keeps the wire order)
		return f.run(`ln -s -- "$P" "$Q"`, env, nil, nil)
	case "Remove":
		return f.run(`if [ -d "$P" ] && [ ! -L "$P" ]; then echo "$P: is a directory" >&2; exit 1; fi; rm -f -- "$P"; [ ! -e "$P" ]`, env, nil, nil)
	}
	return sftp.ErrSSHFxOpUnsupported
}

// PosixRename (posix-rename@openssh.com) overwrites the target.
func (f *boxFS) PosixRename(r *sftp.Request) error {
	return f.run(`mv -f -- "$P" "$Q"`, []string{"P=" + r.Filepath, "Q=" + r.Target}, nil, nil)
}

// statFmt: size|rawmode(hex)|mtime|uid|gid|name (GNU coreutils and busybox).
const statFmt = `%s|%f|%Y|%u|%g|%n`

// Filelist: List, Stat, Readlink.
func (f *boxFS) Filelist(r *sftp.Request) (sftp.ListerAt, error) {
	env := []string{"P=" + r.Filepath}
	switch r.Method {
	case "List":
		var out bytes.Buffer
		err := f.run(`[ -d "$P" ] || { echo "$P: No such file or directory" >&2; exit 1; }; cd -- "$P" || exit 1; for e in .* *; do [ "$e" = . ] || [ "$e" = .. ] && continue; [ -e "$e" ] || [ -L "$e" ] || continue; stat -c '`+statFmt+`' -- "$e" 2>/dev/null; done; true`, env, nil, &out)
		if err != nil {
			return nil, err
		}
		return listerAt(parseStats(out.String())), nil
	case "Stat":
		fi, err := f.stat(r.Filepath, true)
		if err != nil {
			return nil, err
		}
		return listerAt{fi}, nil
	case "Readlink":
		var out bytes.Buffer
		if err := f.run(`readlink -- "$P"`, env, nil, &out); err != nil {
			return nil, err
		}
		return listerAt{&boxFileInfo{name: strings.TrimRight(out.String(), "\n")}}, nil
	}
	return nil, sftp.ErrSSHFxOpUnsupported
}

// Lstat does not follow symlinks.
func (f *boxFS) Lstat(r *sftp.Request) (sftp.ListerAt, error) {
	fi, err := f.stat(r.Filepath, false)
	if err != nil {
		return nil, err
	}
	return listerAt{fi}, nil
}

// Readlink (ReadlinkFileLister).
func (f *boxFS) Readlink(p string) (string, error) {
	var out bytes.Buffer
	if err := f.run(`readlink -- "$P"`, []string{"P=" + p}, nil, &out); err != nil {
		return "", err
	}
	return strings.TrimRight(out.String(), "\n"), nil
}

func (f *boxFS) stat(p string, follow bool) (os.FileInfo, error) {
	flag := ""
	if follow {
		flag = "-L "
	}
	var out bytes.Buffer
	if err := f.run(`stat `+flag+`-c '`+statFmt+`' -- "$P"`, []string{"P=" + p}, nil, &out); err != nil {
		return nil, err
	}
	l := parseStats(out.String())
	if len(l) == 0 {
		return nil, os.ErrNotExist
	}
	l[0].(*boxFileInfo).name = path.Base(p)
	return l[0], nil
}

func parseStats(s string) []os.FileInfo {
	var out []os.FileInfo
	for _, line := range strings.Split(s, "\n") {
		parts := strings.SplitN(line, "|", 6)
		if len(parts) != 6 {
			continue
		}
		size, _ := strconv.ParseInt(parts[0], 10, 64)
		raw, err := strconv.ParseUint(parts[1], 16, 32)
		if err != nil {
			continue
		}
		mt, _ := strconv.ParseInt(parts[2], 10, 64)
		uid, _ := strconv.ParseUint(parts[3], 10, 32)
		gid, _ := strconv.ParseUint(parts[4], 10, 32)
		st := sftp.FileStat{Mode: uint32(raw)}
		out = append(out, &boxFileInfo{name: path.Base(parts[5]), size: size, mode: st.FileMode(), mtime: time.Unix(mt, 0), uid: uint32(uid), gid: uint32(gid)})
	}
	return out
}

type boxFileInfo struct {
	name     string
	size     int64
	mode     os.FileMode
	mtime    time.Time
	uid, gid uint32
}

func (i *boxFileInfo) Name() string       { return i.name }
func (i *boxFileInfo) Size() int64        { return i.size }
func (i *boxFileInfo) Mode() os.FileMode  { return i.mode }
func (i *boxFileInfo) ModTime() time.Time { return i.mtime }
func (i *boxFileInfo) IsDir() bool        { return i.mode.IsDir() }
func (i *boxFileInfo) Sys() any           { return nil }
func (i *boxFileInfo) Uid() uint32        { return i.uid }
func (i *boxFileInfo) Gid() uint32        { return i.gid }

type listerAt []os.FileInfo

func (l listerAt) ListAt(dst []os.FileInfo, off int64) (int, error) {
	if off >= int64(len(l)) {
		return 0, io.EOF
	}
	n := copy(dst, l[off:])
	if n < len(dst) || off+int64(n) >= int64(len(l)) {
		return n, io.EOF
	}
	return n, nil
}
