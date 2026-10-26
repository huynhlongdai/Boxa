package selector

import (
	"strings"
	"testing"

	"boxa/core/internal/model"
)

func TestResolveProfilesAndKinds(t *testing.T) {
	r, p := Resolve(model.Requirements{}, "claude-code")
	if p.ID != "coding-agent" || r.CPU != 2 || r.RAMMB != 4096 || r.Optimize != "balanced" {
		t.Fatalf("claude-code → %+v %s", r, p.ID)
	}
	r, p = Resolve(model.Requirements{Profile: "sandbox-untrusted", RAMMB: 2048}, "")
	if p.ID != "sandbox-untrusted" || r.Isolation != "vm" || !r.Untrusted || r.RAMMB != 2048 {
		t.Fatalf("untrusted %+v", r)
	}
	if len(KindProfiles) != 22 {
		t.Fatalf("want 22 agent kinds, have %d", len(KindProfiles))
	}
	for _, k := range KindProfiles {
		if _, ok := GetProfile(k.Profile); !ok {
			t.Fatalf("kind %s → unknown profile %s", k.Kind, k.Profile)
		}
	}
	w := WeightsFor(p, "cost")
	if s := w.Cost + w.Health + w.Speed + w.Latency + w.Capacity + w.Reliability; s < 0.999 || s > 1.001 {
		t.Fatalf("weights not normalised: %v", s)
	}
}

func TestRankExplainsAndFilters(t *testing.T) {
	req, prof := Resolve(model.Requirements{CPU: 1, RAMMB: 1024, Location: "VN", MaxPriceHour: 0.05}, "")
	opts := []Option{
		{Kind: KindWarm, Provider: "local", Region: "local", Isolation: "container", CPU: 4, RAMMB: 8192, ETASeconds: 1, Reliability: 1, Health: "healthy", Headroom: 0.7, LatencyMS: 5},
		{Kind: KindPool, Provider: "local", Region: "local", Isolation: "container", CPU: 4, RAMMB: 8192, ETASeconds: 25, Reliability: 1, Health: "healthy", Headroom: 0.7, LatencyMS: 5},
		{Kind: KindBurst, Provider: "hetzner", Region: "sin", SKU: "cpx11", Isolation: "container", CPU: 2, RAMMB: 2048, CostPerHour: 0.0119, ExpectedTotalUSD: 0.05, ETASeconds: 90, Reliability: 0.95, Health: "healthy", Headroom: 1, LatencyMS: EstimateLatencyMS("VN", "sin")},
		{Kind: KindBurst, Provider: "vultr", Region: "fra", SKU: "big", Isolation: "container", CPU: 8, RAMMB: 16384, CostPerHour: 0.2, ExpectedTotalUSD: 0.8, ETASeconds: 90, Reliability: 1, Health: "healthy", Headroom: 1},
		{Kind: KindBurst, Provider: "contabo", Region: "eu", Isolation: "container", CPU: 4, RAMMB: 8192, CostPerHour: 0.01, ExpectedTotalUSD: 5, MinBillingHours: 730, ETASeconds: 600, Reliability: 0.9, Health: "unknown", Headroom: 1},
		{Kind: KindBurst, Provider: "digitalocean", Region: "sgp1", Isolation: "container", CPU: 1, RAMMB: 1024, CostPerHour: 0.009, ExpectedTotalUSD: 0.04, Blocked: "circuit open (server)"},
	}
	ranked, rej := Rank(req, prof, opts)
	if len(ranked) != 4 || ranked[0].Kind != KindWarm || ranked[0].Rank != 1 {
		t.Fatalf("ranking: %+v", ranked)
	}
	if !strings.Contains(strings.Join(ranked[0].Reasons, " "), "score") || len(ranked[0].Breakdown) != 6 {
		t.Fatalf("not explained: %+v", ranked[0])
	}
	var sawPrice, sawBreaker bool
	for _, r := range rej {
		sawPrice = sawPrice || strings.Contains(r.Reason, "max $")
		sawBreaker = sawBreaker || strings.Contains(r.Reason, "circuit open")
	}
	if !sawPrice || !sawBreaker {
		t.Fatalf("rejections: %+v", rej)
	}
	last := ranked[len(ranked)-1]
	if last.Provider != "contabo" || !strings.Contains(strings.Join(last.Reasons, " "), "ephemeral") {
		t.Fatalf("monthly-billed provider should rank last for ephemeral: %+v", last)
	}
	// untrusted workloads refuse containers
	ureq, uprof := Resolve(model.Requirements{Profile: "sandbox-untrusted"}, "")
	ranked, rej = Rank(ureq, uprof, opts[:2])
	if len(ranked) != 0 || len(rej) != 2 {
		t.Fatalf("untrusted in container must be rejected: %+v %+v", ranked, rej)
	}
	vm := Option{Kind: KindVM, Provider: "local", Region: "local", Isolation: "vm", CPU: 4, RAMMB: 8192, ETASeconds: 40, Reliability: 1, Health: "healthy", Headroom: 0.5, LatencyMS: 5}
	ranked, _ = Rank(ureq, uprof, append(opts[:2:2], vm))
	if len(ranked) != 1 || ranked[0].Kind != KindVM {
		t.Fatalf("vm must win for untrusted: %+v", ranked)
	}
	// GPU is honest
	g, gp := Resolve(model.Requirements{GPU: 1}, "")
	if r, rj := Rank(g, gp, opts[:1]); len(r) != 0 || !strings.Contains(rj[0].Reason, "GPU") {
		t.Fatal("gpu")
	}
}

func TestLatencyEstimate(t *testing.T) {
	sin := EstimateLatencyMS("VN", "sin")
	fra := EstimateLatencyMS("VN", "fsn1")
	if !(sin > 5 && sin < 60) || !(fra > 120 && fra < 260) {
		t.Fatalf("VN→sin %.0f VN→fsn1 %.0f", sin, fra)
	}
	if EstimateLatencyMS("VN", "local") != 5 || EstimateLatencyMS("??", "sin") != -1 {
		t.Fatal("edge")
	}
	if EstimateLatencyMS("10.8,106.6", "sgp1") != sin {
		t.Fatal("lat,lon and region alias")
	}
}
