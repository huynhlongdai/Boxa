package sandbox

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"boxaz/core/internal/proto"
)

// T3 snapshots: the shim tars the box layout (<workdir>/{work,memory,AGENTS.md},
// the same layout as a boxd local snapshot) inside the sandbox, pulls the
// tarball to the gateway (SnapDir, "local://" location) and, when object
// storage is configured, also uploads it from inside the sandbox to the
// presigned URL. Restore pushes the tarball back and untars it in place;
// with Create=true it first creates the box (fork onto a fresh sandbox).

// shimSnapMaxBytes bounds a snapshot pulled to the gateway (provider file
// APIs answer whole bodies; bigger trees need object storage).
const shimSnapMaxBytes = 64 << 20

var snapHTTP = &http.Client{Timeout: 30 * time.Minute}

func (s *Shim) snapDir() string {
	if s.SnapDir != "" {
		return s.SnapDir
	}
	return filepath.Join(os.TempDir(), "sbx-snapshots")
}

// SnapshotFile is where the gateway keeps a T3 snapshot.
func (s *Shim) SnapshotFile(snapID string) string {
	return filepath.Join(s.snapDir(), filepath.Base(snapID)+".tar.gz")
}

func (s *Shim) snapshot(ctx context.Context, p proto.SnapshotParams) (*proto.SnapshotResult, error) {
	if p.SnapshotID == "" {
		return nil, &proto.Error{Code: "INVALID", Message: "snapshot_id is required"}
	}
	remote := "/tmp/.snap-" + filepath.Base(p.SnapshotID) + ".tgz"
	script := "cd " + shq(s.WorkDir) + " && set -- && for f in work memory AGENTS.md; do [ -e \"$f\" ] && set -- \"$@\" \"$f\"; done; " +
		"tar czf " + shq(remote) + " \"$@\" && stat -c %s " + shq(remote)
	res, err := s.exec(ctx, p.BoxID, script, "/", nil, 900)
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return nil, &proto.Error{Code: "SNAPSHOT_FAILED", Message: "tar: " + trim(res.Stderr+res.Stdout, 300)}
	}
	fields := strings.Fields(res.Stdout)
	size, _ := strconv.ParseInt(fields[len(fields)-1], 10, 64)
	defer s.exec(context.Background(), p.BoxID, "rm -f "+shq(remote), "/", nil, 30)
	out := &proto.SnapshotResult{SizeMB: float64(size) / (1 << 20)}
	if p.UploadURL != "" {
		up := "curl -fsS -X PUT -H 'Content-Type: application/gzip' --upload-file " + shq(remote) + " " + shq(p.UploadURL) + " || wget -q --method=PUT --body-file=" + shq(remote) + " -O- " + shq(p.UploadURL)
		if r, err := s.exec(ctx, p.BoxID, up, "/", nil, 1800); err != nil {
			out.UploadError = err.Error()
		} else if r.ExitCode != 0 {
			out.UploadError = "upload from sandbox exit " + strconv.Itoa(r.ExitCode) + ": " + trim(r.Stderr, 200)
		} else {
			out.Uploaded, out.UploadedBytes = true, size
		}
	}
	if size > shimSnapMaxBytes {
		if out.Uploaded {
			out.Location = "remote-only"
			return out, nil
		}
		return nil, &proto.Error{Code: "SNAPSHOT_TOO_LARGE", Message: fmt.Sprintf("snapshot is %d MB; T3 snapshots above %d MB need object storage (Settings → Storage)", size>>20, shimSnapMaxBytes>>20)}
	}
	data, err := s.pull(ctx, p.BoxID, remote)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) != size {
		return nil, &proto.Error{Code: "SNAPSHOT_FAILED", Message: fmt.Sprintf("pulled %d of %d bytes", len(data), size)}
	}
	if err := os.MkdirAll(s.snapDir(), 0o700); err != nil {
		return nil, err
	}
	file := s.SnapshotFile(p.SnapshotID)
	if err := os.WriteFile(file, data, 0o600); err != nil {
		return nil, err
	}
	out.Location = "local://" + file
	return out, nil
}

// pull reads a (binary) file from the sandbox.
func (s *Shim) pull(ctx context.Context, box, path string) ([]byte, error) {
	if f, ok := s.D.(Filer); ok {
		if b, err := f.GetFile(ctx, s.C, s.Ref, path); err == nil {
			return b, nil
		}
	}
	res, err := s.exec(ctx, box, "base64 -w0 "+shq(path), "/", nil, 600)
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return nil, &proto.Error{Code: "SNAPSHOT_FAILED", Message: "read: " + trim(res.Stderr, 200)}
	}
	return base64.StdEncoding.DecodeString(strings.TrimSpace(res.Stdout))
}

// push writes a (binary) file into the sandbox.
func (s *Shim) push(ctx context.Context, box, path string, data []byte) error {
	if f, ok := s.D.(Filer); ok {
		if err := f.PutFile(ctx, s.C, s.Ref, path, data); err == nil {
			return nil
		}
	}
	b64 := base64.StdEncoding.EncodeToString(data)
	if _, err := s.exec(ctx, box, ": > "+shq(path+".b64"), "/", nil, 30); err != nil {
		return err
	}
	const chunk = 96 << 10
	for i := 0; i < len(b64); i += chunk {
		j := i + chunk
		if j > len(b64) {
			j = len(b64)
		}
		res, err := s.exec(ctx, box, "printf %s "+shq(b64[i:j])+" >> "+shq(path+".b64"), "/", nil, 60)
		if err != nil {
			return err
		}
		if res.ExitCode != 0 {
			return &proto.Error{Code: "RESTORE_FAILED", Message: "upload: " + trim(res.Stderr, 200)}
		}
	}
	res, err := s.exec(ctx, box, "base64 -d "+shq(path+".b64")+" > "+shq(path)+" && rm -f "+shq(path+".b64"), "/", nil, 120)
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return &proto.Error{Code: "RESTORE_FAILED", Message: "decode: " + trim(res.Stderr, 200)}
	}
	return nil
}

func (s *Shim) restore(ctx context.Context, p proto.RestoreParams) error {
	if p.Create {
		cp := p.Box
		cp.BoxID = p.BoxID
		cp.Setup = nil // the snapshot already holds the result of setup
		if err := s.Call(ctx, proto.MCreate, cp, nil); err != nil {
			return err
		}
	}
	remote := "/tmp/.restore-" + filepath.Base(orStr(p.SnapshotID, "snap")) + ".tgz"
	var data []byte
	if f := localSnapFile(p.Location); f != "" {
		if b, err := os.ReadFile(f); err == nil {
			data = b
		}
	}
	if data == nil && p.DownloadURL != "" {
		// fetch inside the sandbox: the tarball never transits the gateway
		dl := "curl -fsSL -o " + shq(remote) + " " + shq(p.DownloadURL) + " || wget -qO " + shq(remote) + " " + shq(p.DownloadURL)
		res, err := s.exec(ctx, p.BoxID, dl, "/", nil, 1800)
		if err != nil {
			return err
		}
		if res.ExitCode != 0 {
			return &proto.Error{Code: "RESTORE_FAILED", Message: "download: " + trim(res.Stderr, 200)}
		}
	} else if data == nil {
		return &proto.Error{Code: "NOT_FOUND", Message: "snapshot tarball not found at the gateway (" + p.Location + ") and no object storage copy"}
	} else if err := s.push(ctx, p.BoxID, remote, data); err != nil {
		return err
	}
	script := "cd " + shq(s.WorkDir) + " && rm -rf work memory && tar xzf " + shq(remote) + " --exclude=AGENTS.md && mkdir -p work memory; R=$?; rm -f " + shq(remote) + "; exit $R"
	res, err := s.exec(ctx, p.BoxID, script, "/", nil, 900)
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return &proto.Error{Code: "RESTORE_FAILED", Message: "untar: " + trim(res.Stderr+res.Stdout, 300)}
	}
	return nil
}

func localSnapFile(loc string) string {
	for _, p := range []string{"local://", "file://"} {
		if strings.HasPrefix(loc, p) {
			return strings.TrimPrefix(loc, p)
		}
	}
	return ""
}
