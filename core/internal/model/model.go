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
	ID              string     `json:"id"`
	Slug            string     `json:"slug"`
	AgentID         string     `json:"agent_id"`
	AgentName       string     `json:"agent_name"`
	Status          string     `json:"status"`
	Lifecycle       string     `json:"lifecycle"`
	Template        string     `json:"template"`
	Spec            Spec       `json:"spec"`
	Provider        string     `json:"provider"`
	Region          string     `json:"region"`
	HostID          string     `json:"host_id"`
	URL             string     `json:"url"`
	TTLSeconds      int        `json:"ttl_seconds"`
	IdleMinutes     int        `json:"idle_minutes"`
	BudgetUSD       float64    `json:"budget_usd"`
	SpentUSD        float64    `json:"spent_usd"`
	CostPerHour     float64    `json:"cost_per_hour"`
	PlacementReason string     `json:"placement_reason"`
	Ports           []int      `json:"ports"`
	Secrets         []string   `json:"secrets"`
	CreatedAt       time.Time  `json:"created_at"`
	LastActiveAt    time.Time  `json:"last_active_at"`
	// Extra fields (not in contract, harmless for clients).
	Error        string     `json:"error,omitempty"`
	Retries      int        `json:"retries,omitempty"`
	TakenOver    bool       `json:"taken_over,omitempty"`
	Setup        []string   `json:"setup,omitempty"`
	TerminatedAt *time.Time `json:"terminated_at,omitempty"`
	Billing      string     `json:"billing,omitempty"`
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
}

type Host struct {
	ID          string     `json:"id"`
	Provider    string     `json:"provider"`
	Region      string     `json:"region"`
	SKU         string     `json:"sku"`
	IP          string     `json:"ip"`
	Status      string     `json:"status"`
	CPU         float64    `json:"cpu"`
	RAMMB       int        `json:"ram_mb"`
	DiskGB      int        `json:"disk_gb"`
	CPUUsedPct  float64    `json:"cpu_used_pct"`
	RAMUsedPct  float64    `json:"ram_used_pct"`
	DiskUsedPct float64    `json:"disk_used_pct"`
	BoxCount    int        `json:"box_count"`
	PriceMonth  float64    `json:"price_month"`
	PriceHour   float64    `json:"price_hour"`
	Billing     string     `json:"billing"`
	CreatedAt   time.Time  `json:"created_at"`
	ProviderRef string     `json:"provider_ref,omitempty"`
	Driver      string     `json:"driver,omitempty"`
	Connected   bool       `json:"connected"`
	LastSeenAt  *time.Time `json:"last_seen_at,omitempty"`
	EmptySince  *time.Time `json:"empty_since,omitempty"`
	TokenHash     string `json:"-"`
	BootstrapHash string `json:"-"`
}

type Provider struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Connected   bool       `json:"connected"`
	Tier        string     `json:"tier"`
	LastCheckAt *time.Time `json:"last_check_at"`
	Error       *string    `json:"error"`
	Implemented bool       `json:"implemented"`
	Capabilities map[string]any `json:"capabilities,omitempty"`
	TokenEnc    string     `json:"-"`
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
}

type Snapshot struct {
	ID        string    `json:"id"`
	BoxID     string    `json:"box_id"`
	SizeMB    float64   `json:"size_mb"`
	CreatedAt time.Time `json:"created_at"`
	Location  string    `json:"location"`
	HostID    string    `json:"host_id,omitempty"`
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

type Settings struct {
	Notify struct {
		TelegramChatID  string   `json:"telegram_chat_id"`
		Email           string   `json:"email"`
		Events          []string `json:"events"`
		DailyReportHour int      `json:"daily_report_hour"`
	} `json:"notify"`
	Reaper struct {
		IdleMinutes       int `json:"idle_minutes"`
		DefaultTTLSeconds int `json:"default_ttl_seconds"`
		KeepSnapshots     int `json:"keep_snapshots"`
	} `json:"reaper"`
	BaseDomain     string  `json:"base_domain"`
	URLTokenProtect bool   `json:"url_token_protect"`
	BudgetMonthUSD float64 `json:"budget_month_usd"`
	Maintenance    struct {
		Enabled bool   `json:"enabled"`
		Message string `json:"message"`
	} `json:"maintenance"`
	Features struct {
		GPU bool `json:"gpu"`
	} `json:"features"`
}

func DefaultSettings() Settings {
	var s Settings
	s.Notify.Events = []string{"budget", "host_down", "box_failed"}
	s.Notify.DailyReportHour = 8
	s.Reaper.IdleMinutes = 5
	s.Reaper.DefaultTTLSeconds = 3600
	s.Reaper.KeepSnapshots = 4
	s.BaseDomain = "sslip.io"
	s.URLTokenProtect = true
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
