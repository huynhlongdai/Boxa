package egress

import (
	"strings"
	"testing"
)

func TestMetadataHosts(t *testing.T) {
	for _, h := range []string{"169.254.169.254", "http://169.254.169.254/latest/meta-data/", "http://2852039166/", "http://0xA9FEA9FE/", "http://0251.0376.0251.0376/", "http://169.254.43518/",
		"http://[fd00:ec2::254]/", "http://metadata.google.internal/computeMetadata/v1/", "http://100.100.100.200/latest", "https://169.254.170.2/v2", "http://[fe80::1]/"} {
		ok := IsMetadataURL(h)
		if !strings.Contains(h, "/") {
			ok = IsMetadataHost(h)
		}
		if !ok {
			t.Errorf("%s not detected", h)
		}
	}
	for _, h := range []string{"https://example.com/", "http://127.0.0.1:3000/", "http://10.0.0.1/", "http://[::1]/", "about:blank", "data:text/html,hi", "http://1.2.3.4/"} {
		if IsMetadataURL(h) {
			t.Errorf("%s flagged", h)
		}
	}
}

func TestMetadataRules(t *testing.T) {
	// no allowlist: the ACL still exists with metadata drops and default allow
	cmds := IncusCommands("c-1", "eth0", Policy{BlockMetadata: true}, nil, false)
	j := ""
	for _, c := range cmds {
		j += strings.Join(c, " ") + "\n"
	}
	for _, want := range []string{"action=drop destination=169.254.169.254/32", "action=drop destination=fd00:ec2::254/128", "security.acls.default.egress.action=allow"} {
		if !strings.Contains(j, want) {
			t.Fatalf("missing %q in\n%s", want, j)
		}
	}
	// with an allowlist: drops + default drop
	j = ""
	for _, c := range IncusCommands("c-1", "eth0", Policy{Allow: []string{"1.2.3.4"}, DenyByDefault: true, BlockMetadata: true}, []Target{{CIDR: "1.2.3.4/32"}}, false) {
		j += strings.Join(c, " ") + "\n"
	}
	if !strings.Contains(j, "action=drop destination=169.254.169.254/32") || !strings.Contains(j, "default.egress.action=drop") {
		t.Fatal(j)
	}
	s := NftScript("c-1", []string{"10.0.0.5", "fd42::5"}, Policy{BlockMetadata: true}, nil, false)
	if !strings.Contains(s, "ip daddr { 169.254.169.254/32") || !strings.Contains(s, "ip6 daddr { fd00:ec2::254/128 } counter drop") || strings.Contains(s, "saddr { 10.0.0.5 } counter drop\n") {
		t.Fatal(s)
	}
	// opt-out: nothing at all
	if c := IncusCommands("c-1", "eth0", Policy{}, nil, false); !strings.Contains(strings.Join(c[len(c)-1], " "), "acl delete") {
		t.Fatal(c)
	}
}
