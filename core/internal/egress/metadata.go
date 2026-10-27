package egress

// v1.11: cloud metadata endpoints are blocked by default — in box egress
// (Incus ACL / nftables drop rules, even without an allowlist), in the
// on-demand browser (resolver rules + core's CDP filter) and for forward
// targets. A browser-driving agent must never read provider credentials.

import (
	"net"
	"net/url"
	"strconv"
	"strings"
)

// MetadataCIDRs are the instance-metadata addresses of the major clouds:
// AWS/GCP/Azure/OCI/DigitalOcean/Hetzner/OpenStack (169.254.169.254), AWS ECS
// task metadata (169.254.170.2), Tencent (169.254.0.23), Alibaba
// (100.100.100.200), AWS IPv6 IMDS (fd00:ec2::254).
var MetadataCIDRs = []string{"169.254.169.254/32", "169.254.170.2/32", "169.254.0.23/32", "100.100.100.200/32", "fd00:ec2::254/128"}

// metadataNames resolve to metadata services.
var metadataNames = []string{"metadata.google.internal", "metadata.goog", "metadata", "instance-data", "instance-data.ec2.internal", "metadata.tencentyun.com", "metadata.azure.internal"}

// parseIPv4Loose parses the forms browsers accept for IPv4 hosts: dotted
// with 1–4 parts, each decimal, octal (0…) or hex (0x…), e.g. 2852039166,
// 0xA9FEA9FE, 0251.0376.0251.0376, 169.254.43518.
func parseIPv4Loose(h string) net.IP {
	parts := strings.Split(h, ".")
	if len(parts) == 0 || len(parts) > 4 {
		return nil
	}
	var nums []uint64
	for _, p := range parts {
		if p == "" {
			return nil
		}
		base := 10
		s := p
		switch {
		case strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X"):
			base, s = 16, s[2:]
			if s == "" {
				s = "0"
			}
		case len(s) > 1 && s[0] == '0':
			base, s = 8, s[1:]
		}
		n, err := strconv.ParseUint(s, base, 32)
		if err != nil {
			return nil
		}
		nums = append(nums, n)
	}
	var v uint64
	last := nums[len(nums)-1]
	for i, n := range nums[:len(nums)-1] {
		if n > 255 {
			return nil
		}
		v |= n << (24 - 8*uint(i))
	}
	rem := uint(32 - 8*(len(nums)-1))
	if rem < 32 && last >= 1<<rem {
		return nil
	}
	v |= last
	return net.IPv4(byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
}

// IsMetadataIP reports whether ip is a metadata address or link-local.
func IsMetadataIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip.IsLinkLocalUnicast() { // 169.254.0.0/16, fe80::/10
		return true
	}
	for _, c := range MetadataCIDRs {
		if _, n, err := net.ParseCIDR(c); err == nil && n.Contains(ip) {
			return true
		}
	}
	return false
}

// IsMetadataHost reports whether a URL host (name, IPv4 in any browser
// notation, [IPv6]) points at a cloud metadata service.
func IsMetadataHost(host string) bool {
	h := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	if hh, _, err := net.SplitHostPort(h); err == nil {
		h = hh
	}
	h = strings.Trim(h, "[]")
	if i := strings.IndexByte(h, '%'); i >= 0 { // zone
		h = h[:i]
	}
	for _, n := range metadataNames {
		if h == n {
			return true
		}
	}
	if ip := net.ParseIP(h); ip != nil {
		return IsMetadataIP(ip)
	}
	return IsMetadataIP(parseIPv4Loose(h))
}

// IsMetadataURL reports whether a URL targets a metadata service.
func IsMetadataURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	if u.Host == "" && u.Opaque != "" { // "169.254.169.254/latest" without a scheme
		u, err = url.Parse("http://" + raw)
		if err != nil {
			return false
		}
	}
	return IsMetadataHost(u.Host)
}
