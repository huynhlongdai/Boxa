package model

import "time"

// v2.2 (M29 phase A): provider accounts. A provider (adapter, e.g. hetzner)
// has one or more accounts, each an API credential of one real account /
// project / team at that provider. Hosts remember the account that created
// them (Host.AccountID) so every later action uses that account's
// credential, whatever the provider's default is by then.

// Account statuses.
const (
	AccountActive      = "active"
	AccountCooling     = "cooling"      // transient errors / breaker open
	AccountRateLimited = "rate_limited" // 429 → cooldown_until
	AccountQuotaFull   = "quota_full"   // provider account limit reached (learned)
	AccountLowBalance  = "low_balance"  // runway under the warn threshold (still eligible)
	AccountOutOfCredit = "out_of_credit"
	AccountAuthFailed  = "auth_failed"
	AccountExpiring    = "token_expiring"
	AccountDisabled    = "disabled"
	AccountDraining    = "draining" // keeps its hosts, takes no new ones
	AccountArchived    = "archived"
)

// Account selection strategies (ProviderPrefs.Strategy).
const (
	StrategyPriority   = "priority"
	StrategyWeighted   = "weighted"
	StrategyRoundRobin = "round_robin"
	StrategyLeastUsed  = "least_used"
	StrategyDrainFirst = "drain_first"
)

// Strategies lists the valid strategies.
var Strategies = []string{StrategyPriority, StrategyWeighted, StrategyRoundRobin, StrategyLeastUsed, StrategyDrainFirst}

// DefaultAccountID is the account every legacy single-token provider
// becomes (PUT /providers/{id}/token keeps writing it).
func DefaultAccountID(provider string) string { return "pa_" + provider + "_default" }

// AccountLimit is one provider-side account limit (servers, vcpu, …).
type AccountLimit struct {
	Limit  float64    `json:"limit"`
	Used   float64    `json:"used"`             // live usage computed by Boxmes (hosts / vCPU / sandboxes on the account)
	Source string     `json:"source,omitempty"` // manual | learned | api
	At     *time.Time `json:"at,omitempty"`
}

// AccountThresholds override the platform alert thresholds for one account
// (0 = default: runway 7/2 days, limits & caps 80/95 %, expiry 14/3 days).
type AccountThresholds struct {
	RunwayWarnDays     float64 `json:"runway_warn_days,omitempty"`
	RunwayCriticalDays float64 `json:"runway_critical_days,omitempty"`
	LimitWarnPct       float64 `json:"limit_warn_pct,omitempty"`
	LimitCriticalPct   float64 `json:"limit_critical_pct,omitempty"`
	ExpiryWarnDays     float64 `json:"expiry_warn_days,omitempty"`
	ExpiryCriticalDays float64 `json:"expiry_critical_days,omitempty"`
}

// ProviderAccount is one row of provider_accounts. Credentials are never
// serialized to API responses: only key_fingerprint.
type ProviderAccount struct {
	ID               string   `json:"id"`
	Provider         string   `json:"provider"`
	Label            string   `json:"label"`
	ExternalID       string   `json:"external_id,omitempty"` // project / org / team id at the provider (rotation must keep it)
	OwnerWorkspaceID string   `json:"owner_workspace_id,omitempty"`
	Tags             []string `json:"tags"`
	Owner            string   `json:"owner,omitempty"`
	Notes            string   `json:"notes,omitempty"`
	ConsoleURL       string   `json:"console_url,omitempty"`
	IsDefault        bool     `json:"is_default"`

	// credential
	CredentialRef         string     `json:"credential_ref"`    // provider:<id> (legacy token) | account (encrypted in this row) | env:<VAR>
	CredentialSource      string     `json:"credential_source"` // api | env
	KeyFingerprint        string     `json:"key_fingerprint,omitempty"`
	BillingKeyFingerprint string     `json:"billing_key_fingerprint,omitempty"` // optional read-only billing key
	Scopes                []string   `json:"scopes"`
	TokenExpiresAt        *time.Time `json:"token_expires_at,omitempty"`
	RefreshExpiresAt      *time.Time `json:"refresh_expires_at,omitempty"`
	RotatedAt             *time.Time `json:"rotated_at,omitempty"`
	RotateEveryDays       int        `json:"rotate_every_days,omitempty"`

	// scheduling
	Enabled     bool     `json:"enabled"`
	Priority    int      `json:"priority"` // 1 = first
	Weight      int      `json:"weight"`   // weighted strategy (default 1)
	Regions     []string `json:"regions"`  // allowed regions (empty = every region the provider prefs allow)
	Pool        string   `json:"pool,omitempty"`
	Drain       bool     `json:"drain"`        // no new hosts; existing hosts stay
	AutoDrained bool     `json:"auto_drained"` // drain set by Boxmes (runway < critical); cleared when runway recovers

	// state
	Status         string     `json:"status"`
	StatusReason   string     `json:"status_reason,omitempty"`
	StatusSince    *time.Time `json:"status_since,omitempty"`
	CooldownUntil  *time.Time `json:"cooldown_until,omitempty"`
	ErrorStreak    int        `json:"error_streak"`
	LastErrorClass string     `json:"last_error_class,omitempty"`
	LastError      string     `json:"last_error,omitempty"`
	LastOKAt       *time.Time `json:"last_ok_at,omitempty"`
	LastCheckAt    *time.Time `json:"last_check_at,omitempty"`

	// money: amounts are in Currency (the provider's billing currency);
	// spend caps too. Stats carries USD conversions.
	BillingModel    string     `json:"billing_model,omitempty"`  // prepaid | postpaid | contract | free_tier
	Currency        string     `json:"currency"`                 // USD | EUR | …
	FXRateUSD       float64    `json:"fx_rate_usd,omitempty"`    // USD per 1 unit of Currency (0 = platform default)
	Balance         *float64   `json:"balance,omitempty"`        // positive = money left
	BalanceSource   string     `json:"balance_source,omitempty"` // manual | api | estimated
	BalanceAt       *time.Time `json:"balance_at,omitempty"`
	CreditTotal     *float64   `json:"credit_total,omitempty"`
	CreditRemaining *float64   `json:"credit_remaining,omitempty"`
	CreditExpiresAt *time.Time `json:"credit_expires_at,omitempty"`
	PendingCharges  *float64   `json:"pending_charges,omitempty"`
	MonthToDate     *float64   `json:"month_to_date,omitempty"`
	// v2.3 (M29 phase B) balance reading through the provider API
	// (provider.BalanceReader). A failed read never touches status or the
	// breaker: it only marks the balance stale.
	SpendPerHour    *float64   `json:"spend_per_hour,omitempty"`    // provider-reported burn rate (Runpod currentSpendPerHr), in Currency/h
	BalancePolledAt *time.Time `json:"balance_polled_at,omitempty"` // last read attempt (ok or not)
	BalanceStale    bool       `json:"balance_stale"`               // the last read failed: balance is the last good one
	BalanceError    string     `json:"balance_error,omitempty"`
	BalanceErrorAt  *time.Time `json:"balance_error_at,omitempty"`
	SpendCapDay     float64    `json:"spend_cap_day"`   // 0 = no cap (Boxmes ledger spend)
	SpendCapMonth   float64    `json:"spend_cap_month"` // 0 = no cap
	MinBalance      float64    `json:"min_balance"`     // below this effective balance: no new hosts
	LowBalanceWarn  float64    `json:"low_balance_warn"`

	// billing cycle
	BillingCycle   string     `json:"billing_cycle,omitempty"` // monthly | hourly-prepaid | contract
	InvoiceDay     int        `json:"invoice_day,omitempty"`
	NextInvoiceAt  *time.Time `json:"next_invoice_at,omitempty"`
	TrialEndsAt    *time.Time `json:"trial_ends_at,omitempty"`
	ContractEndsAt *time.Time `json:"contract_ends_at,omitempty"`

	Limits     map[string]AccountLimit `json:"limits"` // servers | vcpu | ram_gb | concurrent_sandboxes | create_per_sec | create_per_min | …
	Thresholds AccountThresholds       `json:"thresholds"`

	// M29-S sandbox accounts. Templates / snapshots are scoped to the
	// provider account (E2B project, Daytona project, Novita team): a box
	// created from template X can only land on an account that has X.
	Templates         []string   `json:"templates"`                     // templates known to exist in this account (manual or synced; empty = unknown)
	TemplatesMissing  []string   `json:"templates_missing"`             // learned: create answered "template not found" with this account's key
	TemplatesSyncedAt *time.Time `json:"templates_synced_at,omitempty"` // last POST …/templates/sync
	// BillingGroup names accounts that pay from one wallet (E2B workspace,
	// Daytona organization, Novita account): out of credit on one is out of
	// credit on all of them.
	BillingGroup string `json:"billing_group,omitempty"`
	// LimitGroup names accounts that share provider-side limits (several
	// keys of one E2B project, projects of one Daytona organization):
	// concurrent sandboxes and create rate are counted over the group.
	LimitGroup string     `json:"limit_group,omitempty"`
	CreditAt   *time.Time `json:"credit_at,omitempty"` // when credit_remaining was entered (effective credit = credit − ledger since then)

	CreatedAt time.Time `json:"created_at"`
	CreatedBy string    `json:"created_by,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
	UpdatedBy string    `json:"updated_by,omitempty"`

	// Stats is computed on read (never stored).
	Stats *AccountStats `json:"stats,omitempty"`

	CredentialEnc        string `json:"-"`
	BillingCredentialEnc string `json:"-"`
}

// AccountStats are the computed figures of an account (GET responses).
type AccountStats struct {
	HostsLive           int      `json:"hosts_live"`
	BoxesLive           int      `json:"boxes_live"`
	VCPULive            float64  `json:"vcpu_live"`
	SpendToday          float64  `json:"spend_today"`     // ledger, in Currency
	SpendMonth          float64  `json:"spend_month"`     // ledger, in Currency
	SpendTodayUSD       float64  `json:"spend_today_usd"` // ledger (USD)
	SpendMonthUSD       float64  `json:"spend_month_usd"`
	BurnPerHourUSD      float64  `json:"burn_per_hour_usd"`
	BalanceEffective    *float64 `json:"balance_effective,omitempty"` // balance − ledger spend since balance_at, in Currency
	BalanceEffectiveUSD *float64 `json:"balance_effective_usd,omitempty"`
	RunwayHours         *float64 `json:"runway_hours,omitempty"`
	RunwayDays          *float64 `json:"runway_days,omitempty"`
	ForecastMonthEnd    float64  `json:"forecast_month_end"` // in Currency
	Eligible            bool     `json:"eligible"`
	Ineligible          string   `json:"ineligible,omitempty"` // why the scheduler skips it now
	Breaker             string   `json:"breaker"`              // closed | open | half_open (account-level)
	HasCredential       bool     `json:"has_credential"`
	BalanceAPI          bool     `json:"balance_api"` // v2.3: the provider reports the balance (polled), else manual / estimated
	// M29-S sandbox figures (sandbox providers only)
	SandboxesRunning int  `json:"sandboxes_running,omitempty"` // live sandboxes of this account that count toward concurrency (paused ones do not)
	GroupRunning     int  `json:"group_running,omitempty"`     // the same over the account's limit_group
	InFlight         int  `json:"in_flight,omitempty"`         // creates / forks / resumes in progress (limit group)
	CreatesLastSec   int  `json:"creates_last_sec,omitempty"`  // creates started in the last second (limit group)
	CreatesLastMin   int  `json:"creates_last_min,omitempty"`  // creates started in the last minute (limit group)
	SandboxStats     bool `json:"-"`                           // the sandbox figures above were computed
}

// AccountSnapshot is one row of provider_account_snapshots (v2.3, M29
// phase B): the account's money at one poll, for the balance / spend chart
// and runway trend. Amounts are in Currency unless suffixed _usd.
type AccountSnapshot struct {
	AccountID         string    `json:"account_id"`
	Provider          string    `json:"provider"`
	TS                time.Time `json:"ts"`
	Source            string    `json:"source"` // api | manual | estimated | none
	Currency          string    `json:"currency"`
	Balance           *float64  `json:"balance,omitempty"`           // as read (api) or entered (manual)
	BalanceEffective  *float64  `json:"balance_effective,omitempty"` // balance − ledger since it was read
	BalanceUSD        *float64  `json:"balance_usd,omitempty"`       // effective balance in USD
	SpendPerHour      *float64  `json:"spend_per_hour,omitempty"`    // provider-reported burn (Currency/h)
	SpendLimitPerHour *float64  `json:"spend_limit_per_hour,omitempty"`
	BurnPerHourUSD    float64   `json:"burn_per_hour_usd"` // what runway uses: max(provider rate, ledger 24h, live hosts)
	RunwayDays        *float64  `json:"runway_days,omitempty"`
	SpendTodayUSD     float64   `json:"spend_today_usd"`
	SpendMonthUSD     float64   `json:"spend_month_usd"`
	Stale             bool      `json:"stale"`           // the read at this point failed (balance carried from the last good one)
	Error             string    `json:"error,omitempty"` // the read error (never a credential)
}
