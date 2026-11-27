package boxd

// Snapshot upload/download through presigned S3 URLs handed out by core.
// boxd never sees the bucket keys.

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"boxmes/core/internal/proto"
)

// Exporter is implemented by drivers whose snapshots are not files (incus):
// it exports a snapshot to a tar.gz for upload.
type Exporter interface {
	ExportSnapshot(ctx context.Context, id, snapID, dst string) error
}

var objHTTP = &http.Client{Timeout: 2 * time.Hour}

// localFile returns the filesystem path of a snapshot location, if any.
func localFile(loc string) string {
	for _, p := range []string{"local://", "file://"} {
		if strings.HasPrefix(loc, p) {
			return strings.TrimPrefix(loc, p)
		}
	}
	return ""
}

func (a *Agent) uploadSnapshot(ctx context.Context, p proto.SnapshotParams, loc string) (int64, error) {
	path := localFile(loc)
	if path == "" {
		ex, ok := a.drv.(Exporter)
		if !ok {
			return 0, fmt.Errorf("driver %s cannot export snapshots", a.drv.Name())
		}
		dir := filepath.Join(a.cfg.StateDir, "snap-export")
		os.MkdirAll(dir, 0o700)
		path = filepath.Join(dir, filepath.Base(p.SnapshotID)+".tar.gz")
		if err := ex.ExportSnapshot(ctx, p.BoxID, p.SnapshotID, path); err != nil {
			return 0, err
		}
		defer os.Remove(path)
	}
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, "PUT", p.UploadURL, f)
	if err != nil {
		return 0, err
	}
	req.ContentLength = st.Size()
	req.Header.Set("Content-Type", "application/gzip")
	resp, err := objHTTP.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return 0, fmt.Errorf("upload HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return st.Size(), nil
}

// downloadSnapshot fetches a snapshot tarball into the state dir and returns
// a driver-specific location for it.
func (a *Agent) downloadSnapshot(ctx context.Context, p proto.RestoreParams) (string, error) {
	dir := filepath.Join(a.cfg.StateDir, "snap-download")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	dst := filepath.Join(dir, filepath.Base(p.SnapshotID)+".tar.gz")
	req, err := http.NewRequestWithContext(ctx, "GET", p.DownloadURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := objHTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return "", boxErr("NOT_FOUND", fmt.Sprintf("snapshot download HTTP %d", resp.StatusCode))
	}
	tmp := dst + ".part"
	out, err := os.Create(tmp)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(out, io.LimitReader(resp.Body, 50<<30)); err != nil {
		out.Close()
		os.Remove(tmp)
		return "", err
	}
	out.Close()
	if err := os.Rename(tmp, dst); err != nil {
		return "", err
	}
	if a.drv.Name() == "local" {
		return "local://" + dst, nil
	}
	return "file://" + dst, nil
}

// needsDownload reports whether the snapshot file is absent on this host.
func needsDownload(p proto.RestoreParams) bool {
	if p.DownloadURL == "" {
		return false
	}
	if f := localFile(p.Location); f != "" {
		if _, err := os.Stat(f); err == nil {
			return false
		}
	}
	return true
}
