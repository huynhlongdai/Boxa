package model

// v2.0 plans and per-workspace quotas (spec v1.0 §5 / M16). A workspace has a
// plan id; its effective quota is the plan's quota with the workspace's
// overrides applied on top. 0 in a limit means "no limit".

const (
	PlanFree      = "free"
	PlanPro       = "pro"
	PlanTeam      = "team"
	PlanUnlimited = "unlimited" // self-hosted operator workspace (ws_default)

	EgressRestricted = "restricted" // package registries / git hosts only (EgressRestrictedAllow)
	EgressOpen       = "open"

	ErrQuotaExceeded = "QUOTA_EXCEEDED"
)

// Quota is the effective set of limits of a workspace.
type Quota struct {
	Plan              string  `json:"plan"`
	MaxBoxes          int     `json:"max_boxes"`            // concurrent non-sleeping boxes (0 = unlimited)
	MaxCPUPerBox      float64 `json:"max_cpu_per_box"`      // vCPU (0 = unlimited)
	MaxRAMMBPerBox    int     `json:"max_ram_mb_per_box"`   // MB (0 = unlimited)
	MaxTTLHours       int     `json:"max_ttl_hours"`        // box TTL cap (0 = unlimited)
	PublicURL         bool    `json:"public_url"`           // ports may be public (no URL token)
	Egress            string  `json:"egress"`               // restricted | open
	MaxMembers        int     `json:"max_members"`          // 0 = unlimited
	MaxAgents         int     `json:"max_agents"`           // 0 = unlimited
	MaxKeys           int     `json:"max_keys"`             // active workspace + agent keys (0 = unlimited)
	MaxSecrets        int     `json:"max_secrets"`          // 0 = unlimited
	MaxBoxesPerMinute int     `json:"max_boxes_per_minute"` // create rate (0 = unlimited)
}

// QuotaOverrides replace single plan limits for one workspace (nil = plan value).
type QuotaOverrides struct {
	MaxBoxes          *int     `json:"max_boxes,omitempty"`
	MaxCPUPerBox      *float64 `json:"max_cpu_per_box,omitempty"`
	MaxRAMMBPerBox    *int     `json:"max_ram_mb_per_box,omitempty"`
	MaxTTLHours       *int     `json:"max_ttl_hours,omitempty"`
	PublicURL         *bool    `json:"public_url,omitempty"`
	Egress            *string  `json:"egress,omitempty"`
	MaxMembers        *int     `json:"max_members,omitempty"`
	MaxAgents         *int     `json:"max_agents,omitempty"`
	MaxKeys           *int     `json:"max_keys,omitempty"`
	MaxSecrets        *int     `json:"max_secrets,omitempty"`
	MaxBoxesPerMinute *int     `json:"max_boxes_per_minute,omitempty"`
}

// Plans are the built-in plan quotas (spec §5: Free / Pro / Team).
var Plans = map[string]Quota{
	PlanFree: {Plan: PlanFree, MaxBoxes: 1, MaxCPUPerBox: 1, MaxRAMMBPerBox: 2048, MaxTTLHours: 2, PublicURL: false, Egress: EgressRestricted,
		MaxMembers: 1, MaxAgents: 3, MaxKeys: 5, MaxSecrets: 10, MaxBoxesPerMinute: 5},
	PlanPro: {Plan: PlanPro, MaxBoxes: 10, MaxCPUPerBox: 8, MaxRAMMBPerBox: 16384, MaxTTLHours: 24, PublicURL: true, Egress: EgressOpen,
		MaxMembers: 3, MaxAgents: 25, MaxKeys: 50, MaxSecrets: 100, MaxBoxesPerMinute: 30},
	PlanTeam: {Plan: PlanTeam, MaxBoxes: 50, MaxCPUPerBox: 16, MaxRAMMBPerBox: 32768, MaxTTLHours: 168, PublicURL: true, Egress: EgressOpen,
		MaxMembers: 0, MaxAgents: 0, MaxKeys: 0, MaxSecrets: 0, MaxBoxesPerMinute: 120},
	PlanUnlimited: {Plan: PlanUnlimited, PublicURL: true, Egress: EgressOpen},
}

// PlanIDs is the stable order of plans for listings.
var PlanIDs = []string{PlanFree, PlanPro, PlanTeam, PlanUnlimited}

// EgressRestrictedAllow is the outbound allowlist of a restricted workspace:
// package registries, git hosts and OS mirrors.
var EgressRestrictedAllow = []string{
	"pypi.org", "files.pythonhosted.org", "registry.npmjs.org", "registry.yarnpkg.com",
	"github.com", "codeload.github.com", "objects.githubusercontent.com", "raw.githubusercontent.com",
	"gitlab.com", "proxy.golang.org", "sum.golang.org", "crates.io", "static.crates.io", "index.crates.io",
	"rubygems.org", "repo.maven.apache.org", "deb.debian.org", "security.debian.org",
	"archive.ubuntu.com", "security.ubuntu.com", "ports.ubuntu.com", "dl-cdn.alpinelinux.org",
}

// Apply returns q with the overrides applied.
func (o *QuotaOverrides) Apply(q Quota) Quota {
	if o == nil {
		return q
	}
	if o.MaxBoxes != nil {
		q.MaxBoxes = *o.MaxBoxes
	}
	if o.MaxCPUPerBox != nil {
		q.MaxCPUPerBox = *o.MaxCPUPerBox
	}
	if o.MaxRAMMBPerBox != nil {
		q.MaxRAMMBPerBox = *o.MaxRAMMBPerBox
	}
	if o.MaxTTLHours != nil {
		q.MaxTTLHours = *o.MaxTTLHours
	}
	if o.PublicURL != nil {
		q.PublicURL = *o.PublicURL
	}
	if o.Egress != nil && (*o.Egress == EgressOpen || *o.Egress == EgressRestricted) {
		q.Egress = *o.Egress
	}
	if o.MaxMembers != nil {
		q.MaxMembers = *o.MaxMembers
	}
	if o.MaxAgents != nil {
		q.MaxAgents = *o.MaxAgents
	}
	if o.MaxKeys != nil {
		q.MaxKeys = *o.MaxKeys
	}
	if o.MaxSecrets != nil {
		q.MaxSecrets = *o.MaxSecrets
	}
	if o.MaxBoxesPerMinute != nil {
		q.MaxBoxesPerMinute = *o.MaxBoxesPerMinute
	}
	return q
}

// BoxLocation is where a box runs, derived from its placement (v2.0).
// Region is eu | us | apac | sandbox (sandbox API providers) | local (the
// operator's own machine) | other; Site is "<provider>-<site code>" such as
// hetzner-fsn and is what ?location= filters on.
type BoxLocation struct {
	Region   string `json:"region"`
	Country  string `json:"country,omitempty"` // ISO 3166-1 alpha-2
	City     string `json:"city,omitempty"`
	Provider string `json:"provider"`
	Site     string `json:"site"`
	Host     string `json:"host,omitempty"` // host id
}
