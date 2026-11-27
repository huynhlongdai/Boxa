package boxd

// v1.3 host-side speed features: warm-pool claim (box id aliases), copy-on-
// write clone, host feature detection (vm, cow, cache, mesh) and the shared
// package-cache environment injected into every box.

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"boxmes/core/internal/mesh"
	"boxmes/core/internal/proto"
)

// Claimer re-targets a pre-started (warm) box to its new owner.
type Claimer interface {
	Claim(ctx context.Context, realID string, p proto.ClaimParams) error
}

// Cloner makes a copy-on-write clone of a box.
type Cloner interface {
	Clone(ctx context.Context, srcID, dstID string, p proto.CloneParams) (method string, err error)
}

// FeatureReporter lists optional host features (vm, cow:<method>).
type FeatureReporter interface {
	Features(ctx context.Context) []string
}

// aliasTable maps an owner box id → the warm container it claimed.
type aliasTable struct {
	mu   sync.Mutex
	path string
	m    map[string]string
}

func loadAliases(stateDir string) *aliasTable {
	t := &aliasTable{path: filepath.Join(stateDir, "aliases.json"), m: map[string]string{}}
	if b, err := os.ReadFile(t.path); err == nil {
		json.Unmarshal(b, &t.m)
	}
	return t
}

func (t *aliasTable) real(id string) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if r, ok := t.m[id]; ok {
		return r
	}
	return id
}

func (t *aliasTable) set(id, real string) {
	t.mu.Lock()
	t.m[id] = real
	b, _ := json.Marshal(t.m)
	t.mu.Unlock()
	os.WriteFile(t.path, b, 0o600)
}

func (t *aliasTable) drop(id string) {
	t.mu.Lock()
	delete(t.m, id)
	b, _ := json.Marshal(t.m)
	t.mu.Unlock()
	os.WriteFile(t.path, b, 0o600)
}

// public maps a container id back to the owner box id.
func (t *aliasTable) public(real string) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	for k, v := range t.m {
		if v == real {
			return k
		}
	}
	return real
}

func (t *aliasTable) empty() bool { t.mu.Lock(); defer t.mu.Unlock(); return len(t.m) == 0 }

// rewriteIDs replaces box_id / source_id in raw RPC params with the real
// container id when the box was claimed from the warm pool.
func (a *Agent) rewriteIDs(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 || a.aliases.empty() {
		return raw
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return raw
	}
	changed := false
	for _, k := range []string{"box_id", "source_id"} {
		if v, ok := m[k].(string); ok {
			if r := a.aliases.real(v); r != v {
				m[k] = r
				changed = true
			}
		}
	}
	if !changed {
		return raw
	}
	b, _ := json.Marshal(m)
	return b
}

// listBoxes returns owner-facing ids.
func (a *Agent) listBoxes(ctx context.Context) ([]string, error) {
	ids, err := a.drv.List(ctx)
	for i, id := range ids {
		ids[i] = a.aliases.public(id)
	}
	return ids, err
}

func (a *Agent) claim(ctx context.Context, p proto.ClaimParams) (any, error) {
	if p.WarmID == "" || p.BoxID == "" {
		return nil, boxErr("INVALID", "warm_id and box_id are required")
	}
	start := time.Now()
	if c, ok := a.drv.(Claimer); ok {
		if err := c.Claim(ctx, p.WarmID, p); err != nil {
			return nil, err
		}
	} else if p.AgentsMD != "" {
		if err := a.drv.Write(ctx, p.WarmID, "/AGENTS.md", []byte(p.AgentsMD)); err != nil {
			return nil, err
		}
	}
	if p.WarmID != p.BoxID {
		a.aliases.set(p.BoxID, p.WarmID)
	}
	return map[string]any{"ok": true, "duration_ms": time.Since(start).Milliseconds()}, nil
}

func (a *Agent) clone(ctx context.Context, p proto.CloneParams) (any, error) {
	c, ok := a.drv.(Cloner)
	if !ok {
		return nil, boxErr("INVALID", "driver "+a.drv.Name()+" cannot clone")
	}
	if p.SourceID == "" || p.BoxID == "" {
		return nil, boxErr("INVALID", "source_id and box_id are required")
	}
	start := time.Now()
	method, err := c.Clone(ctx, p.SourceID, p.BoxID, p)
	if err != nil {
		return nil, err
	}
	return proto.CloneResult{Method: method, DurationMS: time.Since(start).Milliseconds()}, nil
}

// hostFeatures reports optional capabilities in the hello frame.
func (a *Agent) hostFeatures(ctx context.Context) []string {
	var f []string
	if fr, ok := a.drv.(FeatureReporter); ok {
		f = append(f, fr.Features(ctx)...)
	}
	if CacheHost() != "" {
		f = append(f, "cache")
	}
	if a.meshIP != "" {
		f = append(f, "mesh")
	}
	return f
}

// hostArch is the Go arch name (amd64 | arm64).
func hostArch() string { return runtime.GOARCH }

// CacheHost is the host address of the shared package caches
// (BOXD_CACHE_HOST, e.g. the incus bridge IP 10.10.0.1). "" = disabled.
func CacheHost() string { return strings.TrimSpace(os.Getenv("BOXD_CACHE_HOST")) }

// CacheEnv is injected into every box when host caches are enabled:
// apt-cacher-ng :3142, devpi (pip) :3141, verdaccio (npm) :4873, athens (Go) :3000.
func CacheEnv(host string) map[string]string {
	if host == "" {
		return nil
	}
	return map[string]string{
		"PIP_INDEX_URL":       "http://" + host + ":3141/root/pypi/+simple/",
		"PIP_TRUSTED_HOST":    host,
		"UV_INDEX_URL":        "http://" + host + ":3141/root/pypi/+simple/",
		"npm_config_registry": "http://" + host + ":4873/",
		"YARN_REGISTRY":       "http://" + host + ":4873/",
		"GOPROXY":             "http://" + host + ":3000,https://proxy.golang.org,direct",
		"BOXMES_APT_PROXY":    "http://" + host + ":3142",
		"BOXMES_CACHE_HOST":   host,
	}
}

// AptProxyConf is written to /etc/apt/apt.conf.d/01boxmes-cache in new boxes.
func AptProxyConf(host string) string {
	return "Acquire::http::Proxy \"http://" + host + ":3142\";\nAcquire::https::Proxy \"DIRECT\";\n"
}

// cpReflink copies a directory tree, trying a reflink (copy-on-write on
// btrfs/xfs) first and a plain copy otherwise.
func cpReflink(ctx context.Context, src, dst string) (string, error) {
	if err := exec.CommandContext(ctx, "cp", "-a", "--reflink=always", src, dst).Run(); err == nil {
		return "reflink", nil
	}
	os.RemoveAll(dst)
	if out, err := exec.CommandContext(ctx, "cp", "-a", src, dst).CombinedOutput(); err != nil {
		return "", boxErr("CLONE_FAILED", strings.TrimSpace(string(out)))
	}
	return "copy", nil
}

// TailscaleBin is the tailscale CLI used to join the mesh (BOXD_TAILSCALE).
func TailscaleBin() string {
	if b := os.Getenv("BOXD_TAILSCALE"); b != "" {
		return b
	}
	return "tailscale"
}

// joinMesh runs `tailscale up` with the pre-auth key from core. Without a
// tailscale binary it logs and does nothing (graceful no-op).
func (a *Agent) joinMesh(ctx context.Context, j proto.MeshJoin) {
	bin, err := exec.LookPath(TailscaleBin())
	if err != nil {
		log.Printf("mesh: tailscale not installed, skipping join (%v)", err)
		return
	}
	args := append([]string{}, mesh.TailscaleUpArgs(j.LoginServer, j.AuthKey, j.Hostname, j.Tags)...)
	c, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if out, err := exec.CommandContext(c, bin, args...).CombinedOutput(); err != nil {
		log.Printf("mesh: tailscale up failed: %v: %s", err, strings.TrimSpace(string(out)))
		return
	}
	out, err := exec.CommandContext(c, bin, "ip", "-4").Output()
	if err == nil {
		a.mu.Lock()
		a.meshIP = strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
		a.mu.Unlock()
		log.Printf("mesh: joined %s as %s (%s)", j.LoginServer, j.Hostname, a.meshIP)
	}
}
