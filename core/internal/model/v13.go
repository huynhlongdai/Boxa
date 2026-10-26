package model

import "time"

// v1.3 objects: need-based selection, create estimates, provider prefs,
// warm pool and mesh.

// Requirements is the declarative "what does this box need" spec
// (POST /recommend, POST /boxes {requirements}). Zero values mean "no
// constraint / use the profile default".
type Requirements struct {
	CPU    float64 `json:"cpu,omitempty" yaml:"cpu,omitempty"`
	RAMMB  int     `json:"ram_mb,omitempty" yaml:"ram_mb,omitempty"`
	DiskGB int     `json:"disk_gb,omitempty" yaml:"disk_gb,omitempty"`
	// GPU is reserved for a later release; >0 is rejected with a clear reason.
	GPU  int    `json:"gpu,omitempty" yaml:"gpu,omitempty"`
	Arch string `json:"arch,omitempty" yaml:"arch,omitempty"` // amd64 | arm64 | any (default any)
	// Region pins a region; Location is where the agent/user runs (region id,
	// ISO country code such as "VN", or "lat,lon") for latency estimates.
	Region       string  `json:"region,omitempty" yaml:"region,omitempty"`
	Location     string  `json:"location,omitempty" yaml:"location,omitempty"`
	MaxLatencyMS float64 `json:"max_latency_ms,omitempty" yaml:"max_latency_ms,omitempty"`
	MaxPriceHour float64 `json:"max_price_hour,omitempty" yaml:"max_price_hour,omitempty"`
	// Lifetime: ephemeral (minutes–hours, thrown away) | persistent (kept, sleeps when idle).
	Lifetime      string  `json:"lifetime,omitempty" yaml:"lifetime,omitempty"`
	DurationHours float64 `json:"duration_hours,omitempty" yaml:"duration_hours,omitempty"`
	// Isolation: any | container | vm | sandbox (T2 sandbox API).
	Isolation string `json:"isolation,omitempty" yaml:"isolation,omitempty"`
	// Workload profile id (browser-automation, coding-agent, build, data,
	// long-running-service, sandbox-untrusted, general).
	Profile string `json:"profile,omitempty" yaml:"profile,omitempty"`
	// Optimize: balanced (default) | cost | speed | reliability | latency.
	Optimize  string `json:"optimize,omitempty" yaml:"optimize,omitempty"`
	PublicURL bool   `json:"public_url,omitempty" yaml:"public_url,omitempty"`
	Untrusted bool   `json:"untrusted,omitempty" yaml:"untrusted,omitempty"`
	// Fork (v1.4): the workload forks/clones the box (parallel attempts).
	Fork bool `json:"fork,omitempty" yaml:"fork,omitempty"`
}

// BoxEstimate is the cost/ETA preview attached to every create response.
type BoxEstimate struct {
	Kind             string  `json:"kind"` // warm | pool | vm | burst | clone
	CostPerHour      float64 `json:"cost_per_hour"`
	ExpectedHours    float64 `json:"expected_hours"`
	ExpectedTotalUSD float64 `json:"expected_total_usd"`
	ETASeconds       float64 `json:"eta_seconds"`
	ActualSeconds    float64 `json:"actual_seconds,omitempty"` // measured once running
	Score            float64 `json:"score,omitempty"`
	WarmHit          bool    `json:"warm_hit"`
}

// ProviderPrefs are operator preferences per provider (connect wizard).
type ProviderPrefs struct {
	Enabled       bool     `json:"enabled"`
	Priority      int      `json:"priority"`        // 1 = first; 0 = use the chain order
	MaxHourlyUSD  float64  `json:"max_hourly_usd"`  // refuse offers above this price (0 = no cap)
	MaxMonthlyUSD float64  `json:"max_monthly_usd"` // cap on the sum of monthly prices of live hosts (0 = no cap)
	MaxServers    int      `json:"max_servers"`     // cap on live hosts at this provider (0 = no cap)
	Regions       []string `json:"regions"`         // allowed regions (empty = all)
	DefaultRegion string   `json:"default_region"`
	// v1.4 (sandbox providers): auto (T2 dial-in when the sandbox can keep a
	// background process and BOXA_PUBLIC_URL is set, else T3 shim) | dial | shim.
	SandboxMode string    `json:"sandbox_mode,omitempty"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// DefaultProviderPrefs: enabled, no caps.
func DefaultProviderPrefs() ProviderPrefs { return ProviderPrefs{Enabled: true, Regions: []string{}} }
