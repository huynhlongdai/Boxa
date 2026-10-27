// Package egress validates per-box outbound allowlists and generates the
// enforcement rules for boxd: Incus network ACL commands (default) or an
// nftables script (BOXD_EGRESS_MODE=nft). Generation is pure so it is unit
// tested; applying happens in the Incus driver.
//
// Entries: "example.com", "*.example.com", "1.2.3.4", "10.0.0.0/8",
// "2001:db8::/32", each optionally with a port: "api.openai.com:443",
// "1.2.3.4:5432", "[2001:db8::1]:443".
//
// Domains are resolved on the host when rules are applied (create, wake,
// policy update); "*.example.com" allows the addresses of example.com and
// www.example.com only — IP-level enforcement cannot follow arbitrary
// subdomains. DNS (port 53) stays open while any domain entry exists so the
// box can resolve allowed names.
package egress

import (
	"context"
	"fmt"
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Policy is the per-box egress policy (box.yaml `egress:` / API `egress`).
type Policy struct {
	Allow         []string `json:"allow"`
	DenyByDefault bool     `json:"deny_by_default"`
}

// Active reports whether the policy restricts traffic at all.
func (p *Policy) Active() bool { return p != nil && (p.DenyByDefault || len(p.Allow) > 0) }

// Rule is one parsed allowlist entry.
type Rule struct {
	Kind  string // cidr | domain
	Value string // normalized CIDR or lowercase domain (without "*.")
	Wild  bool   // *.domain
	Port  int    // 0 = any port
}

var domainRe = regexp.MustCompile(`^(\*\.)?([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)

// ParseEntry parses one allowlist entry.
func ParseEntry(s string) (Rule, error) {
	e := strings.ToLower(strings.TrimSpace(s))
	if e == "" {
		return Rule{}, fmt.Errorf("empty entry")
	}
	for _, pfx := range []string{"https://", "http://"} {
		e = strings.TrimPrefix(e, pfx)
	}
	e = strings.TrimSuffix(e, "/")
	port := 0
	host := e
	if strings.HasPrefix(e, "[") { // [v6]:port
		end := strings.Index(e, "]")
		if end < 0 {
			return Rule{}, fmt.Errorf("bad IPv6 entry %q", s)
		}
		host = e[1:end]
		if rest := e[end+1:]; rest != "" {
			if !strings.HasPrefix(rest, ":") {
				return Rule{}, fmt.Errorf("bad entry %q", s)
			}
			p, err := parsePort(rest[1:])
			if err != nil {
				return Rule{}, fmt.Errorf("bad port in %q", s)
			}
			port = p
		}
	} else if strings.Count(e, ":") == 1 { // host:port or v4:port (v6 has ≥2 colons)
		h, ps, _ := strings.Cut(e, ":")
		p, err := parsePort(ps)
		if err != nil {
			return Rule{}, fmt.Errorf("bad port in %q", s)
		}
		host, port = h, p
	}
	if strings.Contains(host, "/") {
		_, n, err := net.ParseCIDR(host)
		if err != nil {
			return Rule{}, fmt.Errorf("bad CIDR %q", s)
		}
		return Rule{Kind: "cidr", Value: n.String(), Port: port}, nil
	}
	if ip := net.ParseIP(host); ip != nil {
		return Rule{Kind: "cidr", Value: hostCIDR(ip), Port: port}, nil
	}
	if !domainRe.MatchString(host) {
		return Rule{}, fmt.Errorf("%q is not a domain, IP or CIDR", s)
	}
	r := Rule{Kind: "domain", Value: strings.TrimPrefix(host, "*."), Wild: strings.HasPrefix(host, "*."), Port: port}
	return r, nil
}

func parsePort(s string) (int, error) {
	p, err := strconv.Atoi(s)
	if err != nil || p < 1 || p > 65535 {
		return 0, fmt.Errorf("bad port")
	}
	return p, nil
}

func isHost(cidr string) bool {
	_, n, err := net.ParseCIDR(cidr)
	if err != nil {
		return false
	}
	ones, bits := n.Mask.Size()
	return ones == bits
}

func hostCIDR(ip net.IP) string {
	if v4 := ip.To4(); v4 != nil {
		return v4.String() + "/32"
	}
	return ip.String() + "/128"
}

// String renders the rule back in canonical entry form.
func (r Rule) String() string {
	v := r.Value
	if r.Kind == "domain" && r.Wild {
		v = "*." + v
	}
	if r.Kind == "cidr" && isHost(v) {
		v = v[:strings.LastIndex(v, "/")]
		if strings.Contains(v, ":") && r.Port > 0 {
			return "[" + v + "]:" + strconv.Itoa(r.Port)
		}
	}
	if r.Port > 0 {
		return v + ":" + strconv.Itoa(r.Port)
	}
	return v
}

// Normalize validates entries and returns them canonical, deduplicated, sorted.
// Max 200 entries.
func Normalize(entries []string) ([]string, error) {
	if len(entries) > 200 {
		return nil, fmt.Errorf("at most 200 egress entries")
	}
	seen := map[string]bool{}
	out := []string{}
	for _, e := range entries {
		r, err := ParseEntry(e)
		if err != nil {
			return nil, err
		}
		s := r.String()
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out, nil
}

// Within reports whether every entry of box is covered by the ceiling list
// (agent policy). A nil/empty ceiling allows anything.
func Within(box, ceiling []string) (bool, string) {
	if len(ceiling) == 0 {
		return true, ""
	}
	var cr []Rule
	for _, c := range ceiling {
		if r, err := ParseEntry(c); err == nil {
			cr = append(cr, r)
		}
	}
	for _, b := range box {
		br, err := ParseEntry(b)
		if err != nil {
			return false, b
		}
		ok := false
		for _, c := range cr {
			if covers(c, br) {
				ok = true
				break
			}
		}
		if !ok {
			return false, b
		}
	}
	return true, ""
}

func covers(c, b Rule) bool {
	if c.Port != 0 && c.Port != b.Port {
		return false
	}
	switch {
	case c.Kind == "domain" && b.Kind == "domain":
		if c.Value == b.Value && (c.Wild || !b.Wild) {
			return true
		}
		return c.Wild && strings.HasSuffix(b.Value, "."+c.Value)
	case c.Kind == "cidr" && b.Kind == "cidr":
		_, cn, err1 := net.ParseCIDR(c.Value)
		bip, bn, err2 := net.ParseCIDR(b.Value)
		if err1 != nil || err2 != nil {
			return false
		}
		co, _ := cn.Mask.Size()
		bo, _ := bn.Mask.Size()
		return cn.Contains(bip) && bo >= co
	}
	return false
}

// Target is a resolved destination.
type Target struct {
	CIDR   string
	Port   int
	Source string // original entry
}

// Resolver looks up host addresses.
type Resolver func(ctx context.Context, host string) ([]string, error)

// DefaultResolver uses the system resolver.
func DefaultResolver(ctx context.Context, host string) ([]string, error) {
	return net.DefaultResolver.LookupHost(ctx, host)
}

// Resolve turns a policy into concrete targets. Unresolvable domains are
// reported in warnings and skipped (they stay blocked).
func Resolve(ctx context.Context, p Policy, res Resolver) (targets []Target, hasDomain bool, warnings []string) {
	if res == nil {
		res = DefaultResolver
	}
	seen := map[string]bool{}
	add := func(t Target) {
		k := t.CIDR + "|" + strconv.Itoa(t.Port)
		if !seen[k] {
			seen[k] = true
			targets = append(targets, t)
		}
	}
	for _, e := range p.Allow {
		r, err := ParseEntry(e)
		if err != nil {
			warnings = append(warnings, err.Error())
			continue
		}
		if r.Kind == "cidr" {
			add(Target{CIDR: r.Value, Port: r.Port, Source: e})
			continue
		}
		hasDomain = true
		hosts := []string{r.Value}
		if r.Wild {
			hosts = append(hosts, "www."+r.Value)
		}
		for _, h := range hosts {
			ips, err := res(ctx, h)
			if err != nil {
				if h == r.Value {
					warnings = append(warnings, fmt.Sprintf("cannot resolve %s: %v", h, err))
				}
				continue
			}
			for _, ip := range ips {
				if pip := net.ParseIP(ip); pip != nil {
					add(Target{CIDR: hostCIDR(pip), Port: r.Port, Source: e})
				}
			}
		}
	}
	sort.SliceStable(targets, func(i, j int) bool {
		if targets[i].CIDR != targets[j].CIDR {
			return targets[i].CIDR < targets[j].CIDR
		}
		return targets[i].Port < targets[j].Port
	})
	return targets, hasDomain, warnings
}

// ACLName is the Incus network ACL name for a container.
func ACLName(container string) string {
	n := "boxaz-eg-" + trimCPrefix(container)
	if len(n) > 63 {
		n = n[:63]
	}
	return n
}

// IncusCommands returns the incus CLI argv lists that (re)create the ACL and
// attach it to the container NIC with default egress drop. With an inactive
// policy it returns the commands that detach and delete the ACL.
func IncusCommands(container, nic string, p Policy, targets []Target, allowDNS bool) [][]string {
	acl := ACLName(container)
	if nic == "" {
		nic = "eth0"
	}
	if !p.Active() {
		return [][]string{
			{"config", "device", "unset", container, nic, "security.acls"},
			{"config", "device", "unset", container, nic, "security.acls.default.egress.action"},
			{"network", "acl", "delete", acl},
		}
	}
	cmds := [][]string{
		{"network", "acl", "delete", acl}, // may fail when absent (ignored)
		{"network", "acl", "create", acl, "description=Boxaz egress allowlist for " + container},
	}
	if allowDNS {
		cmds = append(cmds,
			[]string{"network", "acl", "rule", "add", acl, "egress", "action=allow", "protocol=udp", "destination_port=53", "description=dns"},
			[]string{"network", "acl", "rule", "add", acl, "egress", "action=allow", "protocol=tcp", "destination_port=53", "description=dns"})
	}
	for _, t := range targets {
		if t.Port > 0 {
			for _, proto := range []string{"tcp", "udp"} {
				cmds = append(cmds, []string{"network", "acl", "rule", "add", acl, "egress", "action=allow", "destination=" + t.CIDR, "protocol=" + proto, "destination_port=" + strconv.Itoa(t.Port), "description=" + desc(t.Source)})
			}
			continue
		}
		cmds = append(cmds, []string{"network", "acl", "rule", "add", acl, "egress", "action=allow", "destination=" + t.CIDR, "description=" + desc(t.Source)})
	}
	cmds = append(cmds,
		[]string{"config", "device", "override", container, nic, "security.acls=" + acl, "security.acls.default.egress.action=drop", "security.acls.default.egress.logged=true"})
	return cmds
}

func desc(s string) string {
	s = strings.Map(func(r rune) rune {
		if r == ' ' || r == '"' || r == '\'' {
			return '_'
		}
		return r
	}, s)
	if len(s) > 60 {
		s = s[:60]
	}
	return s
}

// NftTable is the nftables table name for a container.
func NftTable(container string) string {
	return "boxaz_eg_" + strings.NewReplacer("-", "_").Replace(trimCPrefix(container))
}

// NftScript renders an `nft -f -` script that filters forwarded traffic from
// the container's addresses. With an inactive policy it only deletes the table.
func NftScript(container string, srcIPs []string, p Policy, targets []Target, allowDNS bool) string {
	table := NftTable(container)
	var b strings.Builder
	// "add table" + "delete table" makes the script idempotent.
	fmt.Fprintf(&b, "add table inet %s\ndelete table inet %s\n", table, table)
	if !p.Active() {
		return b.String()
	}
	var v4, v6 []string
	for _, ip := range srcIPs {
		if pip := net.ParseIP(ip); pip != nil {
			if pip.To4() != nil {
				v4 = append(v4, pip.String())
			} else {
				v6 = append(v6, pip.String())
			}
		}
	}
	fmt.Fprintf(&b, "table inet %s {\n", table)
	b.WriteString("  chain forward {\n    type filter hook forward priority -10; policy accept;\n")
	for _, fam := range []struct {
		name string
		src  []string
	}{{"ip", v4}, {"ip6", v6}} {
		if len(fam.src) == 0 {
			continue
		}
		src := fam.name + " saddr { " + strings.Join(fam.src, ", ") + " }"
		fmt.Fprintf(&b, "    %s ct state established,related accept\n", src)
		if allowDNS {
			fmt.Fprintf(&b, "    %s udp dport 53 accept\n    %s tcp dport 53 accept\n", src, src)
		}
		for _, t := range targets {
			isV6 := strings.Contains(t.CIDR, ":")
			if (fam.name == "ip6") != isV6 {
				continue
			}
			if t.Port > 0 {
				fmt.Fprintf(&b, "    %s %s daddr %s meta l4proto { tcp, udp } th dport %d accept\n", src, fam.name, t.CIDR, t.Port)
			} else {
				fmt.Fprintf(&b, "    %s %s daddr %s accept\n", src, fam.name, t.CIDR)
			}
		}
		fmt.Fprintf(&b, "    %s counter drop\n", src)
	}
	b.WriteString("  }\n}\n")
	return b.String()
}

// trimCPrefix strips the container prefix (current or pre-rename legacy).
func trimCPrefix(c string) string {
	if strings.HasPrefix(c, "boxaz-") {
		return strings.TrimPrefix(c, "boxaz-")
	}
	return strings.TrimPrefix(c, "boxa-") // compat: pre-rename containers
}
