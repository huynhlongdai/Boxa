// Package selector turns "what does this box need" (a workload profile plus
// declarative requirements) into ranked, explainable placement options:
// warm-pool container, container on a pool host, Incus VM, a new provider
// server (burst) or a T2 sandbox API. Pure logic; the manager feeds it
// options built from live state.
package selector

import (
	"sort"

	"boxa/core/internal/model"
)

// Weights of the scoring components (normalised to sum 1 at use).
type Weights struct {
	Cost        float64 `json:"cost"`
	Health      float64 `json:"health"`
	Speed       float64 `json:"speed"`
	Latency     float64 `json:"latency"`
	Capacity    float64 `json:"capacity"`
	Reliability float64 `json:"reliability"`
}

// Profile is a built-in workload profile.
type Profile struct {
	ID          string             `json:"id"`
	Name        string             `json:"name"`
	Description string             `json:"description"`
	Template    string             `json:"template"`
	Lifecycle   string             `json:"lifecycle"`
	IdleMinutes int                `json:"idle_minutes"`
	TTLSeconds  int                `json:"ttl_seconds"`
	Defaults    model.Requirements `json:"defaults"`
	Weights     Weights            `json:"weights"`
	// EgressDenyByDefault: profile asks for an offline-by-default box.
	EgressDenyByDefault bool     `json:"egress_deny_by_default"`
	EgressAllow         []string `json:"egress_allow"`
	Warm                bool     `json:"warm"` // worth keeping in the warm pool
	Tips                []string `json:"tips"`
}

var balanced = Weights{Cost: 0.30, Health: 0.15, Speed: 0.20, Latency: 0.15, Capacity: 0.10, Reliability: 0.10}

// Profiles are the built-in workload profiles.
var Profiles = []Profile{
	{ID: "general", Name: "General", Description: "Small all-purpose Linux box for scripts and quick experiments.",
		Template: "base", Lifecycle: model.LifecycleSession, IdleMinutes: 5,
		Defaults: model.Requirements{CPU: 1, RAMMB: 1024, DiskGB: 10, Lifetime: "ephemeral"}, Weights: balanced, Warm: true,
		Tips: []string{"Start here when unsure; right-sizing suggests a better size after a few minutes of use."}},
	{ID: "coding-agent", Name: "Coding agent", Description: "Interactive repo work for coding agents (git, build tools, tests). Fast start and low latency matter most.",
		Template: "python", Lifecycle: model.LifecycleSession, IdleMinutes: 10,
		Defaults: model.Requirements{CPU: 2, RAMMB: 4096, DiskGB: 20, Lifetime: "persistent"},
		Weights:  Weights{Cost: 0.25, Health: 0.15, Speed: 0.25, Latency: 0.20, Capacity: 0.05, Reliability: 0.10}, Warm: true,
		Tips: []string{"Snapshot before risky refactors; fork to try alternatives in parallel."}},
	{ID: "browser-automation", Name: "Browser automation", Description: "Headless Chromium/Playwright scraping and web tasks; needs RAM and a public egress.",
		Template: "python-browser", Lifecycle: model.LifecycleSession, IdleMinutes: 5,
		Defaults: model.Requirements{CPU: 2, RAMMB: 4096, DiskGB: 15, Lifetime: "ephemeral"},
		Weights:  Weights{Cost: 0.30, Health: 0.15, Speed: 0.25, Latency: 0.10, Capacity: 0.10, Reliability: 0.10}, Warm: true,
		Tips: []string{"Chromium needs ≥2 GB RAM per browser; one browser per vCPU is a good rule."}},
	{ID: "build", Name: "Build / CI", Description: "Short CPU-heavy compiles and test runs; cheapest compute that finishes soon, then thrown away.",
		Template: "base", Lifecycle: model.LifecycleTask, TTLSeconds: 3600,
		Defaults: model.Requirements{CPU: 4, RAMMB: 8192, DiskGB: 30, Lifetime: "ephemeral", DurationHours: 1},
		Weights:  Weights{Cost: 0.40, Health: 0.10, Speed: 0.20, Latency: 0.05, Capacity: 0.15, Reliability: 0.10},
		Tips:     []string{"Host caches (apt/pip/npm/go proxies) make repeat builds much faster."}},
	{ID: "data", Name: "Data processing", Description: "Pandas/DuckDB/ETL jobs: RAM and disk heavy, latency-insensitive.",
		Template: "python", Lifecycle: model.LifecycleTask, TTLSeconds: 4 * 3600,
		Defaults: model.Requirements{CPU: 2, RAMMB: 8192, DiskGB: 50, Lifetime: "ephemeral", DurationHours: 4},
		Weights:  Weights{Cost: 0.45, Health: 0.10, Speed: 0.10, Latency: 0.05, Capacity: 0.20, Reliability: 0.10},
		Tips:     []string{"Write results to /memory or snapshot to object storage before the TTL ends."}},
	{ID: "long-running-service", Name: "Long-running service", Description: "Bots, webhooks, APIs that run 24/7 with a public URL; reliability and monthly cost dominate.",
		Template: "node", Lifecycle: model.LifecycleService,
		Defaults: model.Requirements{CPU: 1, RAMMB: 1024, DiskGB: 10, Lifetime: "persistent", DurationHours: 730, PublicURL: true},
		Weights:  Weights{Cost: 0.35, Health: 0.15, Speed: 0.05, Latency: 0.15, Capacity: 0.10, Reliability: 0.20},
		Tips:     []string{"Service boxes are never auto-stopped; Boxa asks you before stopping one over budget."}},
	{ID: "sandbox-untrusted", Name: "Untrusted code sandbox", Description: "Run code you do not trust (user uploads, generated code): strongest isolation, offline by default, thrown away.",
		Template: "python", Lifecycle: model.LifecycleTask, TTLSeconds: 900,
		Defaults:            model.Requirements{CPU: 1, RAMMB: 1024, DiskGB: 5, Lifetime: "ephemeral", Isolation: "vm", Untrusted: true, DurationHours: 0.25},
		Weights:             Weights{Cost: 0.20, Health: 0.15, Speed: 0.20, Latency: 0.05, Capacity: 0.10, Reliability: 0.30},
		EgressDenyByDefault: true, EgressAllow: []string{"pypi.org", "files.pythonhosted.org"}, Warm: true,
		Tips: []string{"Prefers an Incus VM or a T2 sandbox API; containers share the host kernel."}},
}

// GetProfile returns a profile by id.
func GetProfile(id string) (Profile, bool) {
	for _, p := range Profiles {
		if p.ID == id {
			return p, true
		}
	}
	return Profile{}, false
}

// KindProfile maps each of the 22 agent kinds to a profile and a tuned spec.
type KindProfile struct {
	Kind    string             `json:"kind"`
	Profile string             `json:"profile"`
	Spec    model.Requirements `json:"spec"`
	Why     string             `json:"why"`
}

// KindProfiles is the built-in per-agent-kind default (GET /profiles → kinds).
var KindProfiles = []KindProfile{
	{"claude-code", "coding-agent", model.Requirements{CPU: 2, RAMMB: 4096, DiskGB: 20}, "repo + tests; node and python toolchains"},
	{"codex-cli", "coding-agent", model.Requirements{CPU: 2, RAMMB: 4096, DiskGB: 20}, "repo + tests"},
	{"gemini-cli", "coding-agent", model.Requirements{CPU: 2, RAMMB: 4096, DiskGB: 20}, "repo + tests"},
	{"cursor", "coding-agent", model.Requirements{CPU: 2, RAMMB: 4096, DiskGB: 20}, "remote workspace for the IDE agent"},
	{"windsurf", "coding-agent", model.Requirements{CPU: 2, RAMMB: 4096, DiskGB: 20}, "remote workspace for the IDE agent"},
	{"cline", "coding-agent", model.Requirements{CPU: 2, RAMMB: 2048, DiskGB: 15}, "lighter IDE agent workspace"},
	{"openai-agents", "general", model.Requirements{CPU: 1, RAMMB: 2048, DiskGB: 10}, "tool-calling runtime; short code runs"},
	{"langchain", "general", model.Requirements{CPU: 1, RAMMB: 2048, DiskGB: 10}, "tool execution"},
	{"langgraph", "general", model.Requirements{CPU: 1, RAMMB: 2048, DiskGB: 10}, "graph nodes run code here"},
	{"crewai", "general", model.Requirements{CPU: 2, RAMMB: 2048, DiskGB: 10}, "several agents share one box"},
	{"autogen", "sandbox-untrusted", model.Requirements{CPU: 1, RAMMB: 2048, DiskGB: 10}, "executes LLM-generated code"},
	{"vercel-ai-sdk", "general", model.Requirements{CPU: 1, RAMMB: 1024, DiskGB: 10}, "code-interpreter style tools"},
	{"mastra", "general", model.Requirements{CPU: 1, RAMMB: 1024, DiskGB: 10}, "workflow steps"},
	{"pydantic-ai", "general", model.Requirements{CPU: 1, RAMMB: 1024, DiskGB: 10}, "typed tool runs"},
	{"llamaindex", "data", model.Requirements{CPU: 2, RAMMB: 4096, DiskGB: 30}, "indexing documents is RAM/disk heavy"},
	{"n8n", "long-running-service", model.Requirements{CPU: 1, RAMMB: 2048, DiskGB: 10}, "always-on workflow server"},
	{"dify", "long-running-service", model.Requirements{CPU: 2, RAMMB: 4096, DiskGB: 20}, "always-on app platform"},
	{"flowise", "long-running-service", model.Requirements{CPU: 1, RAMMB: 2048, DiskGB: 10}, "always-on flow server"},
	{"make", "build", model.Requirements{CPU: 1, RAMMB: 1024, DiskGB: 10}, "short scenario steps"},
	{"zapier", "build", model.Requirements{CPU: 1, RAMMB: 1024, DiskGB: 10}, "short zap steps"},
	{"hark", "browser-automation", model.Requirements{CPU: 2, RAMMB: 4096, DiskGB: 15}, "browses and automates the web"},
	{"custom", "general", model.Requirements{CPU: 1, RAMMB: 1024, DiskGB: 10}, "safe small default"},
}

// ForKind returns the kind profile (custom when unknown).
func ForKind(kind string) KindProfile {
	for _, k := range KindProfiles {
		if k.Kind == kind {
			return k
		}
	}
	return KindProfiles[len(KindProfiles)-1]
}

// Resolve merges requirements over the profile defaults (and an optional
// agent-kind spec) and returns the effective requirements and profile.
func Resolve(req model.Requirements, kind string) (model.Requirements, Profile) {
	pid := req.Profile
	var ks *KindProfile
	if kind != "" {
		k := ForKind(kind)
		ks = &k
		if pid == "" {
			pid = k.Profile
		}
	}
	if pid == "" {
		pid = "general"
	}
	prof, ok := GetProfile(pid)
	if !ok {
		prof, _ = GetProfile("general")
	}
	out := prof.Defaults
	if ks != nil && ks.Profile == prof.ID {
		out.CPU, out.RAMMB, out.DiskGB = ks.Spec.CPU, ks.Spec.RAMMB, ks.Spec.DiskGB
	}
	if req.CPU > 0 {
		out.CPU = req.CPU
	}
	if req.RAMMB > 0 {
		out.RAMMB = req.RAMMB
	}
	if req.DiskGB > 0 {
		out.DiskGB = req.DiskGB
	}
	if req.GPU > 0 {
		out.GPU = req.GPU
	}
	for _, kv := range []struct {
		dst *string
		v   string
	}{
		{&out.Arch, req.Arch}, {&out.Region, req.Region}, {&out.Location, req.Location},
		{&out.Lifetime, req.Lifetime}, {&out.Isolation, req.Isolation}, {&out.Optimize, req.Optimize},
	} {
		if kv.v != "" {
			*kv.dst = kv.v
		}
	}
	if req.MaxLatencyMS > 0 {
		out.MaxLatencyMS = req.MaxLatencyMS
	}
	if req.MaxPriceHour > 0 {
		out.MaxPriceHour = req.MaxPriceHour
	}
	if req.DurationHours > 0 {
		out.DurationHours = req.DurationHours
	}
	out.PublicURL = out.PublicURL || req.PublicURL
	out.Untrusted = out.Untrusted || req.Untrusted
	out.Fork = out.Fork || req.Fork
	if out.Untrusted && out.Isolation == "" {
		out.Isolation = "vm"
	}
	if out.Arch == "" {
		out.Arch = "any"
	}
	if out.Isolation == "" {
		out.Isolation = "any"
	}
	if out.Optimize == "" {
		out.Optimize = "balanced"
	}
	if out.Lifetime == "" {
		out.Lifetime = "ephemeral"
	}
	out.Profile = prof.ID
	return out, prof
}

// WeightsFor returns the scoring weights for a profile and optimize goal.
func WeightsFor(p Profile, optimize string) Weights {
	w := p.Weights
	if w == (Weights{}) {
		w = balanced
	}
	switch optimize {
	case "cost":
		w.Cost += 0.35
	case "speed":
		w.Speed += 0.35
	case "reliability":
		w.Reliability += 0.2
		w.Health += 0.15
	case "latency":
		w.Latency += 0.35
	}
	sum := w.Cost + w.Health + w.Speed + w.Latency + w.Capacity + w.Reliability
	if sum > 0 {
		w = Weights{w.Cost / sum, w.Health / sum, w.Speed / sum, w.Latency / sum, w.Capacity / sum, w.Reliability / sum}
	}
	return w
}

// ProfileIDs lists profile ids sorted.
func ProfileIDs() []string {
	out := make([]string, len(Profiles))
	for i, p := range Profiles {
		out[i] = p.ID
	}
	sort.Strings(out)
	return out
}
