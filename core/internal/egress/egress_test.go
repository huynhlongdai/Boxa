package egress

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestParseAndNormalize(t *testing.T) {
	ok := map[string]string{
		"API.OpenAI.com:443":     "api.openai.com:443",
		"https://pypi.org/":      "pypi.org",
		"*.github.com":           "*.github.com",
		"1.2.3.4":                "1.2.3.4",
		"10.0.0.0/8":             "10.0.0.0/8",
		"10.1.2.3/8":             "10.0.0.0/8",
		"1.2.3.4:5432":           "1.2.3.4:5432",
		"2001:db8::1":            "2001:db8::1",
		"[2001:db8::1]:443":      "[2001:db8::1]:443",
		"2001:db8::/32":          "2001:db8::/32",
		"registry.npmjs.org:443": "registry.npmjs.org:443",
		"files.pythonhosted.org": "files.pythonhosted.org",
	}
	for in, want := range ok {
		r, err := ParseEntry(in)
		if err != nil || r.String() != want {
			t.Errorf("%q → %q %v, want %q", in, r.String(), err, want)
		}
	}
	for _, bad := range []string{"", "not a domain", "foo", "1.2.3.4:99999", "10.0.0.0/33", "a..b.com", "*.*.com", "[::1", "-x.com"} {
		if _, err := ParseEntry(bad); err == nil {
			t.Errorf("%q should fail", bad)
		}
	}
	n, err := Normalize([]string{"pypi.org", "PyPI.org", "1.1.1.1"})
	if err != nil || strings.Join(n, ",") != "1.1.1.1,pypi.org" {
		t.Fatalf("normalize %v %v", n, err)
	}
}

func TestWithin(t *testing.T) {
	ceiling := []string{"*.github.com", "pypi.org", "10.0.0.0/8", "api.openai.com:443"}
	for _, c := range []struct {
		box  []string
		want bool
	}{
		{[]string{"api.github.com", "github.com", "pypi.org"}, true},
		{[]string{"10.2.0.0/16", "10.9.9.9"}, true},
		{[]string{"api.openai.com:443"}, true},
		{[]string{"api.openai.com"}, false}, // any port is wider than :443
		{[]string{"evil.com"}, false},
		{[]string{"11.0.0.0/8"}, false},
		{[]string{"0.0.0.0/0"}, false},
	} {
		if got, _ := Within(c.box, ceiling); got != c.want {
			t.Errorf("Within(%v) = %v", c.box, got)
		}
	}
	if ok, _ := Within([]string{"anything.com"}, nil); !ok {
		t.Fatal("empty ceiling allows all")
	}
}

func fakeRes(ctx context.Context, host string) ([]string, error) {
	switch host {
	case "pypi.org":
		return []string{"151.101.0.223", "2a04:4e42::223"}, nil
	case "github.com":
		return []string{"140.82.112.3"}, nil
	case "www.github.com":
		return []string{"140.82.112.3", "140.82.112.4"}, nil
	}
	return nil, fmt.Errorf("no such host")
}

func TestResolveAndRules(t *testing.T) {
	p := Policy{Allow: []string{"pypi.org:443", "*.github.com", "10.0.0.0/8", "gone.example"}, DenyByDefault: true}
	targets, hasDomain, warns := Resolve(context.Background(), p, fakeRes)
	if !hasDomain || len(warns) != 1 || !strings.Contains(warns[0], "gone.example") {
		t.Fatalf("hasDomain=%v warns=%v", hasDomain, warns)
	}
	var got []string
	for _, x := range targets {
		got = append(got, fmt.Sprintf("%s:%d", x.CIDR, x.Port))
	}
	want := "10.0.0.0/8:0,140.82.112.3/32:0,140.82.112.4/32:0,151.101.0.223/32:443,2a04:4e42::223/128:443"
	if strings.Join(got, ",") != want {
		t.Fatalf("targets %v", got)
	}

	cmds := IncusCommands("boxmes-box-abc", "", p, targets, hasDomain)
	joined := []string{}
	for _, c := range cmds {
		joined = append(joined, strings.Join(c, " "))
	}
	all := strings.Join(joined, "\n")
	for _, w := range []string{
		"network acl create boxmes-eg-box-abc",
		"network acl rule add boxmes-eg-box-abc egress action=allow protocol=udp destination_port=53",
		"network acl rule add boxmes-eg-box-abc egress action=allow destination=10.0.0.0/8 description=10.0.0.0/8",
		"network acl rule add boxmes-eg-box-abc egress action=allow destination=151.101.0.223/32 protocol=tcp destination_port=443",
		"config device override boxmes-box-abc eth0 security.acls=boxmes-eg-box-abc security.acls.default.egress.action=drop",
	} {
		if !strings.Contains(all, w) {
			t.Errorf("incus commands lack %q:\n%s", w, all)
		}
	}
	if strings.Contains(all, "gone.example") {
		t.Error("unresolved domain must not appear")
	}
	off := IncusCommands("boxmes-box-abc", "eth0", Policy{}, nil, false)
	if strings.Join(off[len(off)-1], " ") != "network acl delete boxmes-eg-box-abc" {
		t.Fatalf("detach %v", off)
	}

	nft := NftScript("boxmes-box-abc", []string{"10.99.0.5", "fd42::5"}, p, targets, hasDomain)
	for _, w := range []string{
		"add table inet boxmes_eg_box_abc\ndelete table inet boxmes_eg_box_abc\n",
		"type filter hook forward priority -10; policy accept;",
		"ip saddr { 10.99.0.5 } ct state established,related accept",
		"ip saddr { 10.99.0.5 } udp dport 53 accept",
		"ip saddr { 10.99.0.5 } ip daddr 151.101.0.223/32 meta l4proto { tcp, udp } th dport 443 accept",
		"ip saddr { 10.99.0.5 } ip daddr 10.0.0.0/8 accept",
		"ip saddr { 10.99.0.5 } counter drop",
		"ip6 saddr { fd42::5 } ip6 daddr 2a04:4e42::223/128 meta l4proto { tcp, udp } th dport 443 accept",
		"ip6 saddr { fd42::5 } counter drop",
	} {
		if !strings.Contains(nft, w) {
			t.Errorf("nft lacks %q:\n%s", w, nft)
		}
	}
	if strings.Contains(nft, "ip6 daddr 10.0.0.0/8") || strings.Contains(nft, "ip daddr 2a04") {
		t.Error("address family mixed")
	}
	// deny-by-default with empty allowlist: no DNS, only drop.
	offline := NftScript("boxmes-x", []string{"10.0.0.2"}, Policy{DenyByDefault: true}, nil, false)
	if strings.Contains(offline, "dport 53") || !strings.Contains(offline, "ip saddr { 10.0.0.2 } counter drop") {
		t.Fatalf("offline script:\n%s", offline)
	}
	if s := NftScript("boxmes-x", nil, Policy{}, nil, false); strings.Contains(s, "chain") {
		t.Fatalf("inactive policy must only delete: %s", s)
	}
}
