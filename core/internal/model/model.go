// Package model holds the wire/domain objects shared by core, boxd, CLI and MCP.
// Field names follow docs/api-contract-v1.md exactly (snake_case JSON).
package model

import (
	"math"
	"time"
)

// Box statuses.
const (
	StatusRequested    = "requested"
	StatusPlacing      = "placing"
	StatusProvisioning = "provisioning"
	StatusRunning      = "running"
	StatusSleeping     = "sleeping"
	StatusSnapshotting = "snapshotting"
	StatusTerminated   = "terminated"
	StatusFailed       = "failed"
)

// Lifecycles.
const (
	LifecycleTask    = "task"
	LifecycleSession = "session"
	LifecycleService = "service"
)

// Scopes.
const (
	ScopeAdmin     = "admin"
	ScopeBoxCreate = "box:create"
	ScopeBoxExec   = "box:exec"
	ScopeBoxFile   = "box:file"
	ScopeSnapshot  = "snapshot"
	ScopeSecretUse = "secret:use"
)

// AgentScopes is the default scope set for agent keys.
var AgentScopes = []string{ScopeBoxCreate, ScopeBoxExec, ScopeBoxFile, ScopeSnapshot, ScopeSecretUse}

type Spec struct {
	CPU    float64 `json:"cpu"`
	RAMMB  int     `json:"ram_mb"`
	DiskGB int     `json:"disk_gb"`
}

type Box struct {
	ID              string    `json:"id"`
	Slug            string    `json:"slug"`
	AgentID         string    `json:"agent_id"`
	AgentName       string    `json:"agent_name"`
	Status          string    `json:"status"`
	Lifecycle       string    `json:"lifecycle"`
	Template        string    `json:"template"`
	Spec            Spec      `json:"spec"`
	Provider        string    `json:"provider"`
	Region          string    `json:"region"`
	HostID          string    `json:"host_id"`
	URL             string    `json:"url"`
	TTLSeconds      int       `json:"ttl_seconds"`
	IdleMinutes     int       `json:"idle_minutes"`
	BudgetUSD       float64   `json:"budget_usd"`
	SpentUSD        float64   `json:"spent_usd"`
	CostPerHour     float64   `json:"cost_per_hour"`
	PlacementReason string    `json:"placement_reason"`
	Ports           []int     `json:"ports"`
	Secrets         []string  `json:"secrets"`
	CreatedAt       time.Time `json:"created_at"`
	LastActiveAt    time.Time `json:"last_active_at"`
	// Extra fields (not in contract, harmless for clients).
	Error        string     `json:"error,omitempty"`
	Retries      int        `json:"retries,omitempty"`
	TakenOver    bool       `json:"taken_over,omitempty"`
	Setup        []string   `json:"setup,omitempty"`
	TerminatedAt *time.Time `json:"terminated_at,omitempty"`
	Billing      string     `json:"billing,omitempty"`
	Locale       string     `json:"locale,omitempty"`      // agent-facing language (AGENTS.md)
	ForkedFrom   string     `json:"forked_from,omitempty"` // snapshot id this box was forked from
	Egress       *BoxEgress `json:"egress,omitempty"`      // outbound allowlist (nil = unrestricted)
	// v1.3
	Profile      string        `json:"profile,omitempty"`      // workload profile used to size/place the box
	Requirements *Requirements `json:"requirements,omitempty"` // declarative needs (auto-pick)
	Isolation    string        `json:"isolation,omitempty"`    // container (default) | vm
	Estimate     *BoxEstimate  `json:"estimate,omitempty"`     // cost/ETA preview + measured time to running
	ClonedFrom   string        `json:"cloned_from,omitempty"`  // box id this box was copy-on-write cloned from
	Name         string        `json:"name,omitempty"`         // box_ensure key (defaults to slug)
	// Exposures are the fallback public URLs of ports on sandbox boxes whose
	// provider has no preview host (Boxaz ingress or an in-box tunnel).
	Exposures []Exposure `json:"exposures,omitempty"`
	// v1.7
	App           *AppSpec       `json:"app,omitempty"`             // supervised app (nil = none)
	WakeOnRequest *bool          `json:"wake_on_request,omitempty"` // gateway wakes the box on a request (nil = on)
	PortAccess    map[int]string `json:"port_access,omitempty"`     // port → public | token
}

// Exposure is how one port of a box is reachable from the internet when the
// provider has no preview URL.
type Exposure struct {
	Port      int       `json:"port"`
	Kind      string    `json:"kind"` // ingress | tunnel
	URL       string    `json:"url"`
	Since     time.Time `json:"since"`
	CheckedAt time.Time `json:"checked_at"`
}

// ExposureFor returns the recorded exposure of a port (nil when none).
func (b *Box) ExposureFor(port int) *Exposure {
	for i := range b.Exposures {
		if b.Exposures[i].Port == port {
			return &b.Exposures[i]
		}
	}
	return nil
}

// BoxEgress is the effective egress policy of a box and how it is enforced.
type BoxEgress struct {
	Allow         []string   `json:"allow"`
	DenyByDefault bool       `json:"deny_by_default"`
	Source        string     `json:"source"`   // box | agent | settings
	Enforced      bool       `json:"enforced"` // rules applied on the host
	Mode          string     `json:"mode"`     // incus-acl | nftables | none
	Warning       string     `json:"warning,omitempty"`
	Rules         int        `json:"rules"`
	AppliedAt     *time.Time `json:"applied_at,omitempty"`
}

// Active reports whether the box still occupies host capacity.
func (b *Box) Active() bool {
	switch b.Status {
	case StatusTerminated, StatusFailed, StatusRequested:
		return false
	}
	return true
}

type Policy struct {
	Providers       []string `json:"providers"`
	Regions         []string `json:"regions"`
	EgressAllowlist []string `json:"egress_allowlist"`
}

type Agent struct {
	ID             string     `json:"id"`
	Name           string     `json:"name"`
	Kind           string     `json:"kind"`
	BudgetDayUSD   float64    `json:"budget_day_usd"`
	BudgetMonthUSD float64    `json:"budget_month_usd"`
	SpentTodayUSD  float64    `json:"spent_today_usd"`
	SpentMonthUSD  float64    `json:"spent_month_usd"`
	Policy         Policy     `json:"policy"`
	BoxCount       int        `json:"box_count"`
	LastCallAt     *time.Time `json:"last_call_at"`
	MemoryMB       int        `json:"memory_mb"`
	CreatedAt      time.Time  `json:"created_at"`
	Locale         string     `json:"locale,omitempty"` // vi|en|zh|ja; AGENTS.md + snippet language
}

type APIKey struct {
	ID         string     `json:"id"`
	AgentID    *string    `json:"agent_id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	Scopes     []string   `json:"scopes"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	RevokedAt  *time.Time `json:"revoked_at"`
	Hash       string     `json:"-"`
	Key        string     `json:"key,omitempty"` // only on create
}

func (k *APIKey) HasScope(s string) bool {
	for _, x := range k.Scopes {
		if x == s || x == ScopeAdmin {
			return true
		}
	}
	return false
}

type ExecResult struct {
	ExitCode     int      `json:"exit_code"`
	DurationMS   int64    `json:"duration_ms"`
	StdoutTail   string   `json:"stdout_tail"`
	StderrTail   string   `json:"stderr_tail"`
	Truncated    bool     `json:"truncated"`
	LogID        *string  `json:"log_id"`
	FilesChanged []string `json:"files_changed"`
	TokensSaved  int      `json:"tokens_saved"`
}

type Job struct {
	ID         string      `json:"id"`
	BoxID      string      `json:"box_id"`
	Command    string      `json:"command"`
	Status     string      `json:"status"`
	Result     *ExecResult `json:"result"`
	CreatedAt  time.Time   `json:"created_at"`
	FinishedAt *time.Time  `json:"finished_at"`
	AgentID    string      `json:"-"`
}

type Event struct {
	ID      string    `json:"id"`
	TS      time.Time `json:"ts"`
	Actor   string    `json:"actor"`
	BoxID   *string   `json:"box_id"`
	AgentID *string   `json:"agent_id"`
	Type    string    `json:"type"`
	Message string    `json:"message"`
	CostUSD *float64  `json:"cost_usd"`
	// Data carries structured details (e.g. box.exec: exit_code, tokens_saved, log_id).
	Data map[string]any `json:"data,omitempty"`
}

type Host struct {
	ID            string     `json:"id"`
	Provider      string     `json:"provider"`
	Region        string     `json:"region"`
	SKU           string     `json:"sku"`
	IP            string     `json:"ip"`
	Status        string     `json:"status"`
	CPU           float64    `json:"cpu"`
	RAMMB         int        `json:"ram_mb"`
	DiskGB        int        `json:"disk_gb"`
	CPUUsedPct    float64    `json:"cpu_used_pct"`
	RAMUsedPct    float64    `json:"ram_used_pct"`
	DiskUsedPct   float64    `json:"disk_used_pct"`
	BoxCount      int        `json:"box_count"`
	PriceMonth    float64    `json:"price_month"`
	PriceHour     float64    `json:"price_hour"`
	Billing       string     `json:"billing"`
	CreatedAt     time.Time  `json:"created_at"`
	ProviderRef   string     `json:"provider_ref,omitempty"`
	Driver        string     `json:"driver,omitempty"`
	Connected     bool       `json:"connected"`
	LastSeenAt    *time.Time `json:"last_seen_at,omitempty"`
	EmptySince    *time.Time `json:"empty_since,omitempty"`
	TokenHash     string     `json:"-"`
	BootstrapHash string     `json:"-"`
	// v1.3
	Arch     string   `json:"arch,omitempty"`     // amd64 | arm64
	Features []string `json:"features,omitempty"` // vm, cow:zfs|btrfs|reflink|copy, cache, mesh
	MeshIP   string   `json:"mesh_ip,omitempty"`  // tailnet address when joined to headscale
}

type Provider struct {
	ID           string         `json:"id"`
	Name         string         `json:"name"`
	Connected    bool           `json:"connected"`
	Tier         string         `json:"tier"`
	LastCheckAt  *time.Time     `json:"last_check_at"`
	Error        *string        `json:"error"`
	Implemented  bool           `json:"implemented"`
	Capabilities map[string]any `json:"capabilities,omitempty"`
	TokenEnc     string         `json:"-"`
	TokenSource  string         `json:"token_source,omitempty"` // env | api
	ChainRank    int            `json:"chain_rank,omitempty"`   // 1-based position in the burst fallback chain (0 = not in chain)
	// v1.4: how the provider is integrated (filled from the registry, not stored).
	Group        string `json:"group,omitempty"`        // vps | sandbox
	Adapter      string `json:"adapter,omitempty"`      // api | native | yaml
	Verification string `json:"verification,omitempty"` // live-verified | docs-verified | sdk-source
	Docs         string `json:"docs,omitempty"`
	// v1.7: lifecycle capability flags (filled from the registry, not stored).
	Lifecycle *LifecycleCaps `json:"lifecycle,omitempty"`
}

// LifecycleCaps tells clients what sleep / wake / fork really mean on a provider.
type LifecycleCaps struct {
	MemoryPause      bool   `json:"memory_pause"`        // sleep keeps memory + processes (else: processes restart on wake)
	WakeOnHTTPNative bool   `json:"wake_on_http_native"` // the provider's own URL wakes a paused box
	WakeViaGateway   bool   `json:"wake_via_gateway"`    // the Boxaz gateway URL wakes it (always true)
	Fork             bool   `json:"fork"`                // native fork / clone of a live box
	InboundHTTP      bool   `json:"inbound_http"`        // the provider exposes ports to the internet itself
	MaxRuntimeS      int    `json:"max_runtime"`         // max continuous runtime in seconds (0 = unlimited / not documented)
	Note             string `json:"note,omitempty"`
}

type Offer struct {
	Provider          string    `json:"provider"`
	SKU               string    `json:"sku"`
	Region            string    `json:"region"`
	CPU               float64   `json:"cpu"`
	RAMMB             int       `json:"ram_mb"`
	DiskGB            int       `json:"disk_gb"`
	PriceHour         float64   `json:"price_hour"`
	PriceMonth        float64   `json:"price_month"`
	MinBillingMinutes int       `json:"min_billing_minutes"`
	Source            string    `json:"source"`
	FetchedAt         time.Time `json:"fetched_at"`
	Confidence        string    `json:"confidence"`
}

// Key uniquely identifies an offer within a source.
func (o Offer) Key() string { return o.Provider + "|" + o.SKU + "|" + o.Region + "|" + o.Source }

type LedgerEntry struct {
	ID        string    `json:"id"`
	TS        time.Time `json:"ts"`
	AgentID   string    `json:"agent_id"`
	BoxID     string    `json:"box_id"`
	Provider  string    `json:"provider"`
	Kind      string    `json:"kind"`
	AmountUSD float64   `json:"amount_usd"`
	Note      string    `json:"note"`
	Billing   string    `json:"-"`
}

type Template struct {
	Name        string    `json:"name"`
	Description string    `json:"description"`
	YAML        string    `json:"yaml"`
	Uses        int       `json:"uses"`
	CreatedAt   time.Time `json:"created_at"`
	Builtin     bool      `json:"builtin,omitempty"`
}

type Secret struct {
	Name      string     `json:"name"`
	Agents    []string   `json:"agents"`
	CreatedAt time.Time  `json:"created_at"`
	RotatedAt *time.Time `json:"rotated_at"`
	ValueEnc  string     `json:"-"`
	// System secrets (BOXAZ_SYS_*) hold Boxaz's own credentials (Telegram bot
	// token, SMTP password, S3 secret key). They are never injected into boxes
	// and only admins see them listed.
	System bool `json:"system,omitempty"`
}

// SystemSecretPrefix marks secrets reserved for Boxaz itself.
const SystemSecretPrefix = "BOXAZ_SYS_"

// LegacySystemSecretPrefix is the pre-rename prefix; secrets stored under it
// are still read (and stay reserved) for backward compatibility.
const LegacySystemSecretPrefix = "BOXA_SYS_"

// LegacySystemSecretName maps a BOXAZ_SYS_* name to its pre-rename name.
func LegacySystemSecretName(name string) string {
	if len(name) > len(SystemSecretPrefix) && name[:len(SystemSecretPrefix)] == SystemSecretPrefix {
		return LegacySystemSecretPrefix + name[len(SystemSecretPrefix):]
	}
	return ""
}

type Snapshot struct {
	ID        string    `json:"id"`
	BoxID     string    `json:"box_id"`
	SizeMB    float64   `json:"size_mb"`
	CreatedAt time.Time `json:"created_at"`
	Location  string    `json:"location"`
	HostID    string    `json:"host_id,omitempty"`
	// Storage is where the snapshot can be restored from: "local" (only on
	// HostID) or "s3" (object storage, restorable/forkable on any host).
	Storage     string `json:"storage"`
	RemoteURL   string `json:"remote_url,omitempty"` // s3://bucket/key
	UploadError string `json:"upload_error,omitempty"`
}

type Approval struct {
	ID        string    `json:"id"`
	Type      string    `json:"type"`
	Title     string    `json:"title"`
	Detail    string    `json:"detail"`
	AmountUSD *float64  `json:"amount_usd"`
	CreatedAt time.Time `json:"created_at"`
	Status    string    `json:"status"`
	DedupeKey string    `json:"-"`
	BoxID     string    `json:"box_id,omitempty"`
	AgentID   string    `json:"agent_id,omitempty"`
}

// NotifySettings configures reports/alerts. Secrets (bot token, SMTP
// password) are stored separately in the encrypted secrets store.
type NotifySettings struct {
	TelegramChatID  string   `json:"telegram_chat_id"`
	Email           string   `json:"email"` // comma separated recipients
	Events          []string `json:"events"`
	DailyReportHour int      `json:"daily_report_hour"`
	Timezone        string   `json:"timezone"` // IANA zone for daily_report_hour (default UTC)
	SMTP            struct {
		Host     string `json:"host"`
		Port     int    `json:"port"`
		Username string `json:"username"`
		From     string `json:"from"`
		TLS      string `json:"tls"` // starttls | tls | none
	} `json:"smtp"`
}

type Settings struct {
	Notify NotifySettings `json:"notify"`
	Reaper struct {
		IdleMinutes       int `json:"idle_minutes"`
		DefaultTTLSeconds int `json:"default_ttl_seconds"`
		KeepSnapshots     int `json:"keep_snapshots"`
		// SnapshotRetentionDays deletes snapshots (and their objects) older than N days; 0 = count-based only.
		SnapshotRetentionDays int `json:"snapshot_retention_days"`
	} `json:"reaper"`
	BaseDomain      string `json:"base_domain"`
	URLTokenProtect bool   `json:"url_token_protect"`
	// DisableTunnel turns off the in-box tunnel fallback for public URLs
	// (cloudflared quick tunnel); ingress through core is still used.
	DisableTunnel bool `json:"disable_tunnel"`
	// EgressDenyByDefault: boxes without an explicit egress policy get
	// deny-by-default (only their agent's egress_allowlist is reachable).
	EgressDenyByDefault bool    `json:"egress_deny_by_default"`
	BudgetMonthUSD      float64 `json:"budget_month_usd"`
	Maintenance         struct {
		Enabled bool   `json:"enabled"`
		Message string `json:"message"`
	} `json:"maintenance"`
	Features struct {
		GPU bool `json:"gpu"`
	} `json:"features"`
	// Locale is the operator default language (vi|en|zh|ja): approvals, AGENTS.md
	// fallback. API responses follow each request's Accept-Language.
	Locale string `json:"locale"`
	// Location (v1.3) is where agents/users usually run (region id, ISO
	// country code such as VN, or "lat,lon"); used for latency estimates.
	Location string `json:"location"`
	// Wake (v1.7) tunes wake-on-request at the Boxaz gateway.
	Wake WakeSettings `json:"wake"`
}

// WakeSettings: zero values mean the defaults (hold 10 s, Retry-After 5 s,
// bots / HEAD / health probes do not wake a sleeping box).
type WakeSettings struct {
	HoldS        int  `json:"hold_s"`         // how long a request to a sleeping box is held while it resumes (0 = 10)
	RetryAfterS  int  `json:"retry_after_s"`  // Retry-After of the 503 when the hold runs out (0 = 5)
	WakeOnProbes bool `json:"wake_on_probes"` // true = HEAD, bots and health probes wake boxes too
}

func DefaultSettings() Settings {
	var s Settings
	s.Notify.Events = []string{"daily_report", "budget", "approval", "emergency_stop", "box_failed", "host_down"}
	s.Notify.DailyReportHour = 8
	s.Reaper.IdleMinutes = 5
	s.Reaper.DefaultTTLSeconds = 3600
	s.Reaper.KeepSnapshots = 4
	s.BaseDomain = "sslip.io"
	s.URLTokenProtect = true
	s.Locale = "vi"
	return s
}

type Quotas struct {
	MaxBoxesPerAgent   int     `json:"max_boxes_per_agent"`
	MaxCPUPerBox       float64 `json:"max_cpu_per_box"`
	MaxRAMMBPerBox     int     `json:"max_ram_mb_per_box"`
	MaxDiskGBPerBox    int     `json:"max_disk_gb_per_box"`
	RateLimitPerMinute int     `json:"rate_limit_per_minute"`
}

func DefaultQuotas() Quotas {
	return Quotas{MaxBoxesPerAgent: 20, MaxCPUPerBox: 8, MaxRAMMBPerBox: 16384, MaxDiskGBPerBox: 100, RateLimitPerMinute: 600}
}

// Round4 rounds money to 4 decimals.
func Round4(v float64) float64 { return math.Round(v*10000) / 10000 }

// Round6 rounds money to 6 decimals (ledger granularity).
func Round6(v float64) float64 { return math.Round(v*1e6) / 1e6 }

// List is the standard list envelope.
type List[T any] struct {
	Items      []T     `json:"items"`
	NextCursor *string `json:"next_cursor"`
}

func NewList[T any](items []T) List[T] {
	if items == nil {
		items = []T{}
	}
	return List[T]{Items: items}
}
