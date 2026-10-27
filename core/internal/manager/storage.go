package manager

// Snapshot object storage (R2 / B2 / any S3 API). Core holds the keys and
// hands boxd short-lived presigned URLs: PUT after a snapshot, GET before a
// restore/fork on another host. Unconfigured → snapshots stay host-local.

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"boxa/core/internal/model"
	"boxa/core/internal/objstore"
)

const sysSecretS3 = model.SystemSecretPrefix + "S3_SECRET_KEY"

// StorageSettings is the persisted (non-secret) bucket config.
type StorageSettings struct {
	Provider    string `json:"provider"` // r2 | b2 | s3 | minio | local
	Endpoint    string `json:"endpoint"`
	Region      string `json:"region"`
	Bucket      string `json:"bucket"`
	Prefix      string `json:"prefix"`
	AccessKeyID string `json:"access_key_id"`
}

// StorageView is GET /storage.
type StorageView struct {
	StorageSettings
	Configured            bool   `json:"configured"`
	Source                string `json:"source"` // env | settings | none
	HasSecret             bool   `json:"has_secret"`
	KeepSnapshots         int    `json:"keep_snapshots"`
	SnapshotRetentionDays int    `json:"snapshot_retention_days"`
	Fallback              string `json:"fallback"` // what happens when unconfigured/failing
}

// StorageInput is PUT /storage. SecretAccessKey is write-only ("" keeps the stored one).
type StorageInput struct {
	StorageSettings
	SecretAccessKey string `json:"secret_access_key,omitempty"`
}

// StorageTestResult is POST /storage/test.
type StorageTestResult struct {
	OK        bool   `json:"ok"`
	Error     string `json:"error,omitempty"`
	LatencyMS int64  `json:"latency_ms"`
	Bucket    string `json:"bucket"`
}

func (m *Manager) storageSettings(ctx context.Context) StorageSettings {
	var s StorageSettings
	_ = m.Store.GetKV(ctx, "storage", &s)
	return s
}

// objStore returns the snapshot bucket client, or nil when unconfigured.
func (m *Manager) objStore(ctx context.Context) *objstore.Client {
	if m.Cfg.ObjectStore != nil && m.Cfg.ObjectStore.Enabled() {
		return objstore.New(*m.Cfg.ObjectStore, m.objHTTP)
	}
	s := m.storageSettings(ctx)
	if s.Provider == "" || s.Provider == "local" || s.Bucket == "" {
		return nil
	}
	secret, ok := m.systemSecret(ctx, sysSecretS3)
	if !ok {
		return nil
	}
	cfg := objstore.Config{Provider: s.Provider, Endpoint: s.Endpoint, Region: s.Region, Bucket: s.Bucket, Prefix: s.Prefix, AccessKeyID: s.AccessKeyID, SecretAccessKey: secret, PathStyle: true}
	if !cfg.Enabled() {
		return nil
	}
	return objstore.New(cfg, m.objHTTP)
}

func (m *Manager) StorageView(ctx context.Context, p *Principal) (*StorageView, error) {
	if err := p.Require(model.ScopeAdmin); err != nil {
		return nil, err
	}
	st := m.Settings(ctx)
	v := &StorageView{KeepSnapshots: st.Reaper.KeepSnapshots, SnapshotRetentionDays: st.Reaper.SnapshotRetentionDays,
		Fallback: "local: snapshots stay on their host (tar.gz / incus snapshot); restore and fork only on that host"}
	if c := m.Cfg.ObjectStore; c != nil && c.Enabled() {
		cc := *c
		cc.Defaults()
		v.StorageSettings = StorageSettings{Provider: cc.Provider, Endpoint: cc.Endpoint, Region: cc.Region, Bucket: cc.Bucket, Prefix: cc.Prefix, AccessKeyID: cc.AccessKeyID}
		v.Configured, v.Source, v.HasSecret = true, "env", true
		return v, nil
	}
	v.StorageSettings = m.storageSettings(ctx)
	_, v.HasSecret = m.systemSecret(ctx, sysSecretS3)
	v.Configured = m.objStore(ctx) != nil
	v.Source = "none"
	if v.Configured {
		v.Source = "settings"
	}
	if v.Provider == "" {
		v.Provider = "local"
	}
	return v, nil
}

func (m *Manager) PutStorage(ctx context.Context, p *Principal, in StorageInput) (*StorageView, error) {
	if err := p.Require(model.ScopeAdmin); err != nil {
		return nil, err
	}
	if m.Cfg.ObjectStore != nil && m.Cfg.ObjectStore.Enabled() {
		return nil, model.EH(model.ErrInvalid, "Unset the BOXA_S3_* environment variables to manage storage from the dashboard.", "storage is configured by environment variables")
	}
	s := in.StorageSettings
	s.Provider = strings.ToLower(strings.TrimSpace(s.Provider))
	switch s.Provider {
	case "", "local":
		s = StorageSettings{Provider: "local"}
		m.deleteSystemSecret(ctx, sysSecretS3)
	case "r2", "b2", "s3", "minio":
		if strings.TrimSpace(s.Endpoint) == "" || strings.TrimSpace(s.Bucket) == "" || strings.TrimSpace(s.AccessKeyID) == "" {
			return nil, model.E(model.ErrInvalid, "endpoint, bucket and access_key_id are required")
		}
		cfg := objstore.Config{Provider: s.Provider, Endpoint: s.Endpoint, Region: s.Region}
		cfg.Defaults()
		s.Endpoint, s.Region = cfg.Endpoint, cfg.Region
		s.Bucket, s.AccessKeyID, s.Prefix = strings.TrimSpace(s.Bucket), strings.TrimSpace(s.AccessKeyID), strings.Trim(s.Prefix, "/ ")
		if in.SecretAccessKey != "" {
			if err := m.putSystemSecret(ctx, sysSecretS3, in.SecretAccessKey); err != nil {
				return nil, err
			}
		} else if _, ok := m.systemSecret(ctx, sysSecretS3); !ok {
			return nil, model.E(model.ErrInvalid, "secret_access_key is required")
		}
	default:
		return nil, model.E(model.ErrInvalid, "storage provider must be local, r2, b2, s3 or minio")
	}
	if err := m.Store.PutKV(ctx, "storage", s); err != nil {
		return nil, err
	}
	m.Emit(ctx, p.Actor(), "", "", "storage.updated", "snapshot storage: "+s.Provider+" "+s.Bucket, nil)
	return m.StorageView(ctx, p)
}

// TestStorage writes/reads/deletes a probe object.
func (m *Manager) TestStorage(ctx context.Context, p *Principal) (*StorageTestResult, error) {
	if err := p.Require(model.ScopeAdmin); err != nil {
		return nil, err
	}
	c := m.objStore(ctx)
	if c == nil {
		return &StorageTestResult{OK: false, Error: "object storage not configured (snapshots stay local)"}, nil
	}
	start := time.Now()
	tctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	err := c.Test(tctx)
	r := &StorageTestResult{OK: err == nil, LatencyMS: time.Since(start).Milliseconds(), Bucket: c.Cfg.Bucket}
	if err != nil {
		r.Error = err.Error()
	}
	return r, nil
}

func snapshotKey(c *objstore.Client, boxID, snapID string) string {
	return c.Key("snapshots/" + boxID + "/" + snapID + ".tar.gz")
}

// deleteSnapshot removes the record and its object (best effort).
func (m *Manager) deleteSnapshot(ctx context.Context, sn *model.Snapshot) {
	if sn.RemoteURL != "" {
		if c := m.objStore(ctx); c != nil {
			if key, ok := c.KeyFromURI(sn.RemoteURL); ok {
				if err := c.Delete(ctx, key); err != nil {
					m.Emit(ctx, "system", sn.BoxID, "", "snapshot.delete_failed", fmt.Sprintf("could not delete %s: %v", sn.RemoteURL, err), nil)
				}
			}
		}
	}
	// T3 sandbox snapshots are tarballs kept by core itself
	if f := strings.TrimPrefix(sn.Location, "local://"); f != sn.Location && strings.HasPrefix(f, m.snapshotDir()+string(os.PathSeparator)) {
		os.Remove(f)
	}
	m.Store.DeleteSnapshot(ctx, sn.ID)
}

// applySnapshotRetention enforces keep_snapshots (newest N per box) and
// snapshot_retention_days. Called after each snapshot and by the reaper.
func (m *Manager) applySnapshotRetention(ctx context.Context, boxID string) int {
	st := m.Settings(ctx)
	keep := st.Reaper.KeepSnapshots
	snaps, err := m.Store.ListSnapshots(ctx, boxID)
	if err != nil {
		return 0
	}
	n := 0
	var cutoff time.Time
	if st.Reaper.SnapshotRetentionDays > 0 {
		cutoff = time.Now().UTC().AddDate(0, 0, -st.Reaper.SnapshotRetentionDays)
	}
	perBox := map[string]int{}
	for _, sn := range snaps { // newest first
		perBox[sn.BoxID]++
		if (keep > 0 && perBox[sn.BoxID] > keep) || (!cutoff.IsZero() && sn.CreatedAt.Before(cutoff)) {
			m.deleteSnapshot(ctx, sn)
			n++
		}
	}
	return n
}

// presignSnapshot returns a presigned URL for the snapshot's object.
func (m *Manager) presignSnapshot(ctx context.Context, sn *model.Snapshot, method string) string {
	if sn.RemoteURL == "" {
		return ""
	}
	c := m.objStore(ctx)
	if c == nil {
		return ""
	}
	key, ok := c.KeyFromURI(sn.RemoteURL)
	if !ok {
		return ""
	}
	u, err := c.Presign(method, key, 2*time.Hour)
	if err != nil {
		return ""
	}
	return u
}

// ---- system secrets (encrypted secrets store, never injected into boxes) ----

func (m *Manager) putSystemSecret(ctx context.Context, name, value string) error {
	enc, err := m.Crypt.Seal(value)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	s, err := m.Store.GetSecret(ctx, name)
	if err != nil {
		s = &model.Secret{Name: name, Agents: []string{}, CreatedAt: now}
	} else {
		s.RotatedAt = &now
	}
	s.ValueEnc, s.System = enc, true
	return m.Store.PutSecret(ctx, s)
}

func (m *Manager) systemSecret(ctx context.Context, name string) (string, bool) {
	s, err := m.Store.GetSecret(ctx, name)
	if err != nil || s.ValueEnc == "" {
		return "", false
	}
	v, err := m.Crypt.Open(s.ValueEnc)
	if err != nil || v == "" {
		return "", false
	}
	return v, true
}

func (m *Manager) deleteSystemSecret(ctx context.Context, name string) {
	if _, err := m.Store.GetSecret(ctx, name); err == nil {
		m.Store.DeleteSecret(ctx, name)
	}
}
