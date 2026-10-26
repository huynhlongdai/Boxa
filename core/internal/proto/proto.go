// Package proto is the wire protocol between core and boxd. boxd dials OUT to
// core over WebSocket (wss behind Caddy) and the single connection multiplexes
// request/response RPCs, PTY streams and metrics. Frames are JSON text messages.
package proto

import (
	"encoding/json"

	"boxa/core/internal/model"
)

const Version = "boxa-wire/1"

// Frame types.
const (
	TReq     = "req"
	TRes     = "res"
	THello   = "hello"   // boxd → core, first frame
	TWelcome = "welcome" // core → boxd, reply to hello
	TMetrics = "metrics" // boxd → core every 10s
	TPtyData = "pty.data"
	TPtyExit = "pty.exit"
	TPing    = "ping"
)

// RPC methods (core → boxd).
const (
	MCreate     = "box.create"
	MStart      = "box.start"
	MStop       = "box.stop"
	MDelete     = "box.delete"
	MExec       = "box.exec"
	MRead       = "file.read"
	MWrite      = "file.write"
	MGrep       = "file.grep"
	MObserve    = "box.observe"
	MSnapshot   = "box.snapshot"
	MLogRead    = "log.read"
	MPtyOpen    = "pty.open"
	MPtyResize  = "pty.resize"
	MPtyClose   = "pty.close"
	MList       = "box.list"
	MHTTP       = "http.do"
	MRestore    = "box.restore"     // v1.1: restore a snapshot into the same box, or fork it into a new one
	MEgress     = "box.egress"      // v1.2: apply (or clear) the box egress allowlist
	MExecCancel = "box.exec_cancel" // v1.2: kill a running async job (A2A tasks/cancel)
	MClone      = "box.clone"       // v1.3: copy-on-write clone of a live box into a new box
	MClaim      = "box.claim"       // v1.3: hand a warm-pool box to its owner (AGENTS.md, env, agent memory)
)

// CloneParams clones SourceID into a new box (copy-on-write when the storage supports it).
type CloneParams struct {
	SourceID string            `json:"source_id"`
	BoxID    string            `json:"box_id"`
	AgentID  string            `json:"agent_id"`
	AgentsMD string            `json:"agents_md"`
	Env      map[string]string `json:"env"`
}

// CloneResult reports which mechanism produced the clone.
type CloneResult struct {
	Method     string `json:"method"` // incus-zfs | incus-btrfs | incus-copy | reflink | copy
	DurationMS int64  `json:"duration_ms"`
}

// ClaimParams re-targets a pre-started warm box to its new owner.
type ClaimParams struct {
	WarmID   string            `json:"warm_id"` // the pre-started container
	BoxID    string            `json:"box_id"`  // the owner's box id (becomes an alias of warm_id on the host)
	Spec     model.Spec        `json:"spec"`
	AgentID  string            `json:"agent_id"`
	AgentsMD string            `json:"agents_md"`
	Env      map[string]string `json:"env"`
}

// EgressParams applies an egress policy to a box (inactive policy = clear).
type EgressParams struct {
	BoxID         string   `json:"box_id"`
	Allow         []string `json:"allow"`
	DenyByDefault bool     `json:"deny_by_default"`
}

// EgressResult reports how the host enforced the policy.
type EgressResult struct {
	Enforced bool     `json:"enforced"`
	Mode     string   `json:"mode"` // incus-acl | nftables | none
	Warning  string   `json:"warning,omitempty"`
	Rules    int      `json:"rules"`
	Warnings []string `json:"warnings,omitempty"`
}

type Frame struct {
	ID     string          `json:"id,omitempty"`
	Type   string          `json:"type"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *Error          `json:"error,omitempty"`
	Stream string          `json:"stream,omitempty"`
	Data   []byte          `json:"data,omitempty"`
}

type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

type Hello struct {
	HostID   string   `json:"host_id"`
	Hostname string   `json:"hostname"`
	Driver   string   `json:"driver"`
	Version  string   `json:"version"`
	Provider string   `json:"provider"`
	Region   string   `json:"region"`
	IP       string   `json:"ip"`
	CPU      float64  `json:"cpu"`
	RAMMB    int      `json:"ram_mb"`
	DiskGB   int      `json:"disk_gb"`
	Boxes    []string `json:"boxes"`
	// v1.3
	Arch     string   `json:"arch,omitempty"`
	Features []string `json:"features,omitempty"`
	MeshIP   string   `json:"mesh_ip,omitempty"`
}

type Welcome struct {
	HostID    string `json:"host_id"`
	HostToken string `json:"host_token,omitempty"`
	// v1.3: join the headscale mesh (only when enabled on core).
	Mesh *MeshJoin `json:"mesh,omitempty"`
}

// MeshJoin tells boxd how to join the tailnet with tailscale.
type MeshJoin struct {
	LoginServer string   `json:"login_server"`
	AuthKey     string   `json:"auth_key"`
	Hostname    string   `json:"hostname"`
	Tags        []string `json:"tags"`
}

type Metrics struct {
	CPUUsedPct  float64            `json:"cpu_used_pct"`
	RAMUsedPct  float64            `json:"ram_used_pct"`
	DiskUsedPct float64            `json:"disk_used_pct"`
	BoxCount    int                `json:"box_count"`
	BoxCPU      map[string]float64 `json:"box_cpu,omitempty"`
	// v1.3: per-box usage for right-sizing (cpu_pct of one vCPU, ram_mb).
	Boxes map[string]BoxUsage `json:"boxes,omitempty"`
}

// BoxUsage is one box's resource usage sample.
type BoxUsage struct {
	CPUPct float64 `json:"cpu_pct"`
	RAMMB  float64 `json:"ram_mb"`
}

type CreateParams struct {
	BoxID    string            `json:"box_id"`
	Template string            `json:"template"`
	Image    string            `json:"image"`
	Spec     model.Spec        `json:"spec"`
	Env      map[string]string `json:"env"`
	Setup    []string          `json:"setup"`
	AgentsMD string            `json:"agents_md"`
	AgentID  string            `json:"agent_id"`
	// v1.3
	Isolation string `json:"isolation,omitempty"` // container (default) | vm
}

type BoxRef struct {
	BoxID string `json:"box_id"`
}

type ExecParams struct {
	BoxID     string            `json:"box_id"`
	Command   string            `json:"command"`
	Cwd       string            `json:"cwd"`
	Env       map[string]string `json:"env"`
	TimeoutS  int               `json:"timeout_s"`
	TailLines int               `json:"tail_lines"`
	JobID     string            `json:"job_id,omitempty"` // async job: cancellable with MExecCancel
}

// ExecCancelParams kills the running command of an async job.
type ExecCancelParams struct {
	JobID string `json:"job_id"`
}

type ReadParams struct {
	BoxID string `json:"box_id"`
	Path  string `json:"path"`
	From  int    `json:"from"`
	To    int    `json:"to"`
}

type Entry struct {
	Name string `json:"name"`
	Type string `json:"type"` // file|dir|symlink
	Size int64  `json:"size"`
}

type ReadResult struct {
	IsDir      bool    `json:"is_dir"`
	Entries    []Entry `json:"entries,omitempty"`
	Content    string  `json:"content,omitempty"`
	TotalLines int     `json:"total_lines"`
}

type WriteParams struct {
	BoxID   string `json:"box_id"`
	Path    string `json:"path"`
	Content string `json:"content"`
}

type GrepParams struct {
	BoxID   string `json:"box_id"`
	Pattern string `json:"pattern"`
	Path    string `json:"path"`
}

type GrepMatch struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Text string `json:"text"`
}

type GrepResult struct {
	Matches   []GrepMatch `json:"matches"`
	Truncated bool        `json:"truncated"`
}

type Proc struct {
	PID int    `json:"pid"`
	Cmd string `json:"cmd"`
}

type ObserveResult struct {
	FilesChanged []string `json:"files_changed"`
	Processes    []Proc   `json:"processes"`
	Ports        []int    `json:"ports"`
	Since        string   `json:"since"`
}

type SnapshotParams struct {
	BoxID      string `json:"box_id"`
	SnapshotID string `json:"snapshot_id"`
	// UploadURL is a presigned S3 PUT URL; when set boxd uploads the snapshot
	// tarball there (incus: exported first). Failure keeps the local copy.
	UploadURL string `json:"upload_url,omitempty"`
}

type SnapshotResult struct {
	Location      string  `json:"location"`
	SizeMB        float64 `json:"size_mb"`
	Uploaded      bool    `json:"uploaded,omitempty"`
	UploadedBytes int64   `json:"uploaded_bytes,omitempty"`
	UploadError   string  `json:"upload_error,omitempty"`
}

type LogReadParams struct {
	LogID string `json:"log_id"`
}

type LogReadResult struct {
	Content string `json:"content"`
}

type PtyOpenParams struct {
	BoxID  string `json:"box_id"`
	Stream string `json:"stream"`
	Cols   int    `json:"cols"`
	Rows   int    `json:"rows"`
}

type PtyResizeParams struct {
	Stream string `json:"stream"`
	Cols   int    `json:"cols"`
	Rows   int    `json:"rows"`
}

type ListResult struct {
	Boxes []string `json:"boxes"`
}

// HTTPParams proxies one HTTP request to a port inside a box (ingress).
type HTTPParams struct {
	BoxID   string              `json:"box_id"`
	Port    int                 `json:"port"`
	Method  string              `json:"method"`
	Path    string              `json:"path"`
	Headers map[string][]string `json:"headers"`
	Body    []byte              `json:"body"`
}

type HTTPResult struct {
	Status  int                 `json:"status"`
	Headers map[string][]string `json:"headers"`
	Body    []byte              `json:"body"`
}

// RestoreParams restores snapshot SnapshotID (taken from SourceBoxID) into
// BoxID. Create=true forks: BoxID does not exist yet and is created from the
// snapshot using Box (spec/agents_md/env); otherwise BoxID's /work and
// /memory are replaced in place.
type RestoreParams struct {
	BoxID       string       `json:"box_id"`
	SourceBoxID string       `json:"source_box_id"`
	SnapshotID  string       `json:"snapshot_id"`
	Location    string       `json:"location"`
	Create      bool         `json:"create"`
	Box         CreateParams `json:"box"`
	// DownloadURL is a presigned S3 GET URL for the snapshot tarball, used
	// when the snapshot is not on this host (restore/fork across hosts).
	DownloadURL string `json:"download_url,omitempty"`
}
