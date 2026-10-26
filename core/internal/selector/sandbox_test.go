package selector

import (
	"strings"
	"testing"

	"boxa/core/internal/model"
)

// v1.4: sandbox options are filtered by their capabilities, not by kind.
func TestRankSandboxCapabilities(t *testing.T) {
	base := func(p string) Option {
		return Option{Kind: KindSandbox, Provider: p, Region: "global", Isolation: "sandbox", CPU: 2, RAMMB: 4096, CostPerHour: 0.1, ExpectedTotalUSD: 0.02,
			ETASeconds: 2, Reliability: 1, Health: "healthy", Headroom: 1, Tier: "T2", StrongIsolation: true}
	}
	pausing, short, noPorts, forky := base("pausing"), base("short"), base("noports"), base("forky")
	pausing.Pause, pausing.Ports = true, true
	short.MaxLifetimeH, short.Pause, short.Ports = 1, true, true
	noPorts.Pause = true
	forky.Fork, forky.Ports = true, true
	req, prof := Resolve(model.Requirements{CPU: 1, RAMMB: 1024, Lifetime: "persistent", DurationHours: 4, PublicURL: true}, "")
	ranked, rej := Rank(req, prof, []Option{pausing, short, noPorts, forky})
	if len(ranked) != 1 || ranked[0].Provider != "pausing" {
		t.Fatalf("ranked %+v rejected %+v", ranked, rej)
	}
	why := map[string]string{}
	for _, r := range rej {
		why[r.Provider] = r.Reason
	}
	if !strings.Contains(why["short"], "at most 1.0 h") || !strings.Contains(why["noports"], "public URL") || !strings.Contains(why["forky"], "cannot pause") {
		t.Fatalf("reasons %+v", why)
	}
	// untrusted ephemeral fork workload: sandbox with fork wins and is explained
	req, prof = Resolve(model.Requirements{CPU: 1, RAMMB: 1024, Untrusted: true, Lifetime: "ephemeral", Fork: true}, "")
	container := base("dockerish")
	container.Fork, container.StrongIsolation = true, false
	ranked, rej = Rank(req, prof, []Option{forky, pausing, container})
	if len(rej) != 2 || !strings.Contains(rej[0].Reason+rej[1].Reason, "shared kernel") {
		t.Fatalf("container sandbox must be rejected for untrusted code: %+v", rej)
	}
	if len(ranked) != 1 || ranked[0].Provider != "forky" || !strings.Contains(strings.Join(ranked[0].Reasons, " "), "native fork") {
		t.Fatalf("fork ranking %+v", ranked)
	}
	// explicit isolation=sandbox does not let a shared-kernel sandbox through for untrusted code
	req, prof = Resolve(model.Requirements{CPU: 1, RAMMB: 1024, Untrusted: true, Isolation: "sandbox", Lifetime: "ephemeral"}, "")
	ranked, rej = Rank(req, prof, []Option{forky, container})
	if len(ranked) != 1 || ranked[0].Provider != "forky" || len(rej) != 1 || !strings.Contains(rej[0].Reason, "shared kernel") {
		t.Fatalf("isolation=sandbox + untrusted: ranked %+v rej %+v", ranked, rej)
	}
}
