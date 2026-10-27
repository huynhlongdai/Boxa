package selector

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"boxaz/core/internal/model"
)

// Option kinds.
const (
	KindWarm    = "warm"    // pre-started container from the warm pool (claim ≈ 1 s)
	KindPool    = "pool"    // new container on a connected pool host
	KindVM      = "vm"      // Incus VM on a pool host that supports it
	KindBurst   = "burst"   // new server rented from a provider (hourly)
	KindSandbox = "sandbox" // T2/T3 sandbox API (E2B, Daytona, Prized, … v1.4)
)

// Option is one way to satisfy the requirements, built by the manager from
// live state (pool hosts, warm pool, offers, provider health).
type Option struct {
	Kind             string   `json:"kind"`
	Provider         string   `json:"provider"`
	Region           string   `json:"region"`
	HostID           string   `json:"host_id,omitempty"`
	SKU              string   `json:"sku,omitempty"`
	Arch             string   `json:"arch,omitempty"`
	Isolation        string   `json:"isolation"` // container | vm | sandbox
	CPU              float64  `json:"cpu"`
	RAMMB            int      `json:"ram_mb"`
	DiskGB           int      `json:"disk_gb"`
	CostPerHour      float64  `json:"cost_per_hour"`
	ExpectedTotalUSD float64  `json:"expected_total_usd"`
	ETASeconds       float64  `json:"eta_seconds"`
	Reliability      float64  `json:"reliability"`   // 0..1 create success history
	Health           string   `json:"health"`        // healthy | degraded | down | unknown
	Headroom         float64  `json:"headroom"`      // 0..1 capacity left after placement
	LatencyMS        float64  `json:"latency_ms"`    // estimated RTT agent→region (-1 unknown)
	MinBillingHours  float64  `json:"min_billing_h"` // billing granularity (monthly providers ≈ 730)
	RequiresApproval bool     `json:"requires_approval"`
	PriceStale       bool     `json:"price_stale"`
	Blocked          string   `json:"blocked,omitempty"` // breaker open / rate limited / budget cap
	Notes            []string `json:"notes,omitempty"`
	WarmID           string   `json:"warm_id,omitempty"`
	// v1.4 capability flags (hosts: fork = clone, pause = sleep, ports = public URL).
	Tier         string  `json:"tier,omitempty"` // T1 | T2 | T3
	Fork         bool    `json:"fork"`
	Pause        bool    `json:"pause"`
	Ports        bool    `json:"ports"`
	GPU          bool    `json:"gpu,omitempty"`
	MaxLifetimeH float64 `json:"max_lifetime_h,omitempty"` // 0 = unlimited
	BootMS       int     `json:"boot_ms,omitempty"`
	Verification string  `json:"verification,omitempty"` // adapter verification level (sandbox providers)
	// StrongIsolation: the sandbox has its own kernel (microVM / VM / gVisor);
	// false for container-based sandbox APIs.
	StrongIsolation bool `json:"strong_isolation,omitempty"`
}

// Component is one explained part of a score.
type Component struct {
	Score  float64 `json:"score"`  // 0..100
	Weight float64 `json:"weight"` // 0..1
	Value  string  `json:"value"`  // human readable input
}

// Scored is a ranked option.
type Scored struct {
	Option
	Rank      int                  `json:"rank"`
	Score     float64              `json:"score"` // 0..100
	Breakdown map[string]Component `json:"breakdown"`
	Reasons   []string             `json:"reasons"`
}

// Rejection explains why an option was not offered.
type Rejection struct {
	Kind     string `json:"kind"`
	Provider string `json:"provider,omitempty"`
	Region   string `json:"region,omitempty"`
	HostID   string `json:"host_id,omitempty"`
	SKU      string `json:"sku,omitempty"`
	Reason   string `json:"reason"`
}

// Rank filters options by hard requirements and scores the rest.
func Rank(req model.Requirements, prof Profile, opts []Option) ([]Scored, []Rejection) {
	w := WeightsFor(prof, req.Optimize)
	var keep []Option
	var rej []Rejection
	reject := func(o Option, why string) {
		rej = append(rej, Rejection{Kind: o.Kind, Provider: o.Provider, Region: o.Region, HostID: o.HostID, SKU: o.SKU, Reason: why})
	}
	for _, o := range opts {
		switch {
		case req.GPU > 0:
			reject(o, "GPU boxes are not supported yet")
			continue
		case o.Blocked != "":
			reject(o, o.Blocked)
			continue
		case o.CPU > 0 && o.CPU < req.CPU, o.RAMMB > 0 && o.RAMMB < req.RAMMB, o.DiskGB > 0 && o.DiskGB < req.DiskGB:
			reject(o, fmt.Sprintf("too small (%.0f vCPU / %d MB / %d GB)", o.CPU, o.RAMMB, o.DiskGB))
			continue
		case req.Arch != "" && req.Arch != "any" && o.Arch != "" && o.Arch != req.Arch:
			reject(o, "arch "+o.Arch+" ≠ "+req.Arch)
			continue
		case req.Region != "" && o.Region != "" && o.Region != req.Region:
			reject(o, "region "+o.Region+" ≠ "+req.Region)
			continue
		case req.MaxPriceHour > 0 && o.CostPerHour > req.MaxPriceHour+1e-9:
			reject(o, fmt.Sprintf("$%.4f/h > max $%.4f/h", o.CostPerHour, req.MaxPriceHour))
			continue
		case req.MaxLatencyMS > 0 && o.LatencyMS > req.MaxLatencyMS:
			reject(o, fmt.Sprintf("~%.0f ms > max %.0f ms", o.LatencyMS, req.MaxLatencyMS))
			continue
		case req.Isolation == "vm" && req.Untrusted && o.Isolation == "container":
			// untrusted code may still run in a container, but only with an explicit opt-in
			reject(o, "untrusted workload needs a VM or sandbox (containers share the host kernel)")
			continue
		case req.Untrusted && req.Isolation != "container" && o.Kind == KindSandbox && !o.StrongIsolation:
			// also with isolation=sandbox: a container-based sandbox API shares its kernel
			reject(o, "untrusted workload: this sandbox API runs containers on a shared kernel")
			continue
		case req.Isolation == "vm" && !req.Untrusted && o.Isolation == "container":
			reject(o, "isolation=vm requested")
			continue
		case req.Isolation == "container" && o.Isolation != "container":
			reject(o, "isolation=container requested")
			continue
		case req.Isolation == "sandbox" && o.Isolation != "sandbox":
			reject(o, "isolation=sandbox requested")
			continue
		case req.Lifetime == "persistent" && o.Kind == KindSandbox && !o.Pause:
			reject(o, "this sandbox cannot pause; persistent boxes need pause/resume or a host")
			continue
		case o.Kind == KindSandbox && o.MaxLifetimeH > 0 && req.DurationHours > o.MaxLifetimeH:
			reject(o, fmt.Sprintf("sandbox lives at most %.1f h < %.1f h needed", o.MaxLifetimeH, req.DurationHours))
			continue
		case req.PublicURL && o.Kind == KindSandbox && !o.Ports:
			reject(o, "this sandbox provider does not expose a public URL")
			continue
		case req.Fork && !o.Fork:
			reject(o, "fork/clone requested but not supported here")
			continue
		}
		keep = append(keep, o)
	}
	if len(keep) == 0 {
		return []Scored{}, rej
	}
	minCost := math.Inf(1)
	for _, o := range keep {
		if o.ExpectedTotalUSD < minCost {
			minCost = o.ExpectedTotalUSD
		}
	}
	out := make([]Scored, 0, len(keep))
	for _, o := range keep {
		s := Scored{Option: o, Breakdown: map[string]Component{}}
		// cost: relative to the cheapest option; free (pool spare capacity) = 100
		cost := 100.0
		if o.ExpectedTotalUSD > 0 {
			floor := math.Max(minCost, 0.0005)
			cost = 100 * math.Pow(floor/math.Max(o.ExpectedTotalUSD, floor), 0.5)
		}
		health := map[string]float64{"healthy": 100, "unknown": 70, "degraded": 40, "down": 0}[o.Health]
		if o.Health == "" {
			health = 70
		}
		speed := 100 * math.Exp(-o.ETASeconds/180)
		lat := 70.0
		latVal := "unknown"
		if o.LatencyMS >= 0 {
			lat = 100 * math.Exp(-o.LatencyMS/200)
			latVal = fmt.Sprintf("~%.0f ms", o.LatencyMS)
		}
		capacity := 100 * clamp(o.Headroom, 0, 1)
		rel := 100 * clamp(o.Reliability, 0, 1)
		s.Breakdown["cost"] = Component{round1(cost), round3(w.Cost), fmt.Sprintf("$%.4f total ($%.4f/h)", o.ExpectedTotalUSD, o.CostPerHour)}
		s.Breakdown["health"] = Component{round1(health), round3(w.Health), orStr(o.Health, "unknown")}
		s.Breakdown["speed"] = Component{round1(speed), round3(w.Speed), fmt.Sprintf("ETA %.0f s", o.ETASeconds)}
		s.Breakdown["latency"] = Component{round1(lat), round3(w.Latency), latVal}
		s.Breakdown["capacity"] = Component{round1(capacity), round3(w.Capacity), fmt.Sprintf("%.0f%% headroom", o.Headroom*100)}
		s.Breakdown["reliability"] = Component{round1(rel), round3(w.Reliability), fmt.Sprintf("%.0f%% create success", o.Reliability*100)}
		total := w.Cost*cost + w.Health*health + w.Speed*speed + w.Latency*lat + w.Capacity*capacity + w.Reliability*rel
		// soft penalties (explained)
		if req.Lifetime == "ephemeral" && o.MinBillingHours >= 24 {
			total -= 15
			s.Reasons = append(s.Reasons, fmt.Sprintf("−15: billed per %.0f h minimum, wasteful for an ephemeral box", o.MinBillingHours))
		}
		if req.Untrusted && o.Isolation == "container" {
			total -= 20
			s.Reasons = append(s.Reasons, "−20: container isolation for untrusted code")
		}
		if o.RequiresApproval {
			total -= 10
			s.Reasons = append(s.Reasons, "−10: needs operator approval (auto-burst off or provider not connected)")
		}
		if o.PriceStale {
			total -= 5
			s.Reasons = append(s.Reasons, "−5: price data is stale")
		}
		if o.Kind == KindSandbox && req.Untrusted && o.StrongIsolation {
			total += 8
			s.Reasons = append(s.Reasons, "+8: own-kernel sandbox (microVM/gVisor) for untrusted code")
		}
		if o.Kind == KindSandbox && req.Lifetime == "ephemeral" && o.MinBillingHours == 0 {
			total += 4
			s.Reasons = append(s.Reasons, "+4: per-second billing, nothing left running after the task")
		}
		if o.Kind == KindSandbox && req.Fork && o.Fork {
			total += 3
			s.Reasons = append(s.Reasons, "+3: native fork")
		}
		s.Score = round1(clamp(total, 0, 100))
		s.Reasons = append(explain(s, w), s.Reasons...)
		s.Reasons = append(s.Reasons, o.Notes...)
		out = append(out, s)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].ExpectedTotalUSD < out[j].ExpectedTotalUSD
	})
	for i := range out {
		out[i].Rank = i + 1
	}
	return out, rej
}

// explain lists the strongest weighted contributions as sentences.
func explain(s Scored, w Weights) []string {
	type kv struct {
		k string
		c Component
	}
	var l []kv
	for k, c := range s.Breakdown {
		l = append(l, kv{k, c})
	}
	sort.Slice(l, func(i, j int) bool {
		ci, cj := l[i].c.Score*l[i].c.Weight, l[j].c.Score*l[j].c.Weight
		if ci != cj {
			return ci > cj
		}
		return l[i].k < l[j].k
	})
	var out []string
	label := map[string]string{KindWarm: "warm container", KindPool: "pool host container", KindVM: "Incus VM", KindBurst: "new " + s.Provider + " server", KindSandbox: s.Provider + " sandbox"}[s.Kind]
	out = append(out, fmt.Sprintf("%s in %s: score %.0f/100", label, orStr(s.Region, "?"), s.Score))
	for i := 0; i < len(l) && i < 2; i++ {
		out = append(out, fmt.Sprintf("+ %s %.0f (%s, weight %.0f%%)", l[i].k, l[i].c.Score, l[i].c.Value, l[i].c.Weight*100))
	}
	if n := len(l); n > 2 && l[n-1].c.Score < 50 {
		out = append(out, fmt.Sprintf("− %s %.0f (%s)", l[n-1].k, l[n-1].c.Score, l[n-1].c.Value))
	}
	return out
}

func clamp(v, lo, hi float64) float64 { return math.Max(lo, math.Min(hi, v)) }
func round1(v float64) float64        { return math.Round(v*10) / 10 }
func round3(v float64) float64        { return math.Round(v*1000) / 1000 }
func orStr(s, d string) string {
	if strings.TrimSpace(s) == "" {
		return d
	}
	return s
}
