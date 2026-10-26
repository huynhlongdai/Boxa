package boxd

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"

	"boxa/core/internal/egress"
	"boxa/core/internal/proto"
)

// EgressApplier is implemented by drivers that can restrict outbound traffic.
type EgressApplier interface {
	ApplyEgress(ctx context.Context, p proto.EgressParams) (proto.EgressResult, error)
}

const localEgressWarning = "local driver has no network isolation: the egress allowlist is NOT enforced (exported as BOXA_EGRESS_ALLOW for tools that honour it); use the incus driver in production"

// ApplyEgress on the local driver is best effort: the policy is exported to
// the box environment (BOXA_EGRESS_ALLOW, BOXA_EGRESS_ENFORCED=0) and the
// result says clearly that nothing is enforced.
func (d *LocalDriver) ApplyEgress(ctx context.Context, p proto.EgressParams) (proto.EgressResult, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	m, err := d.meta(p.BoxID)
	if err != nil {
		return proto.EgressResult{}, err
	}
	if m.Env == nil {
		m.Env = map[string]string{}
	}
	pol := egress.Policy{Allow: p.Allow, DenyByDefault: p.DenyByDefault}
	if pol.Active() {
		m.Env["BOXA_EGRESS_ALLOW"] = strings.Join(p.Allow, ",")
		m.Env["BOXA_EGRESS_ENFORCED"] = "0"
	} else {
		delete(m.Env, "BOXA_EGRESS_ALLOW")
		delete(m.Env, "BOXA_EGRESS_ENFORCED")
	}
	if err := d.saveMeta(m); err != nil {
		return proto.EgressResult{}, err
	}
	res := proto.EgressResult{Enforced: false, Mode: "none"}
	if pol.Active() {
		res.Warning = localEgressWarning
	}
	return res, nil
}

// ApplyEgress on Incus: resolve domains, then either attach a network ACL to
// the container NIC (default, needs the nftables firewall driver / OVN) or
// load an nftables table filtering the container's addresses (EgressMode "nft").
func (d *IncusDriver) ApplyEgress(ctx context.Context, p proto.EgressParams) (proto.EgressResult, error) {
	name := cname(p.BoxID)
	pol := egress.Policy{Allow: p.Allow, DenyByDefault: p.DenyByDefault}
	res := proto.EgressResult{Mode: "incus-acl"}
	if d.EgressMode == "nft" {
		res.Mode = "nftables"
	}
	targets, hasDomain, warns := egress.Resolve(ctx, pol, d.Resolver)
	res.Warnings = warns
	if res.Mode == "nftables" {
		ips, err := d.containerIPs(ctx, name)
		if err != nil && pol.Active() {
			return res, err
		}
		script := egress.NftScript(name, ips, pol, targets, hasDomain)
		if err := d.runNft(ctx, script); err != nil {
			return res, fmt.Errorf("nft: %w", err)
		}
	} else {
		for i, args := range egress.IncusCommands(name, "eth0", pol, targets, hasDomain) {
			_, err := d.incus(ctx, args...)
			if err == nil {
				continue
			}
			last := strings.Join(args, " ")
			switch {
			case i == 0 && pol.Active(), !pol.Active():
				// deleting an absent ACL / unsetting absent keys is fine
			case strings.HasPrefix(last, "config device override"):
				// NIC already overridden at instance level: set the keys instead.
				setArgs := append([]string{"config", "device", "set", name, "eth0"}, args[5:]...)
				if _, err2 := d.incus(ctx, setArgs...); err2 != nil {
					return res, err
				}
			default:
				return res, err
			}
		}
	}
	if pol.Active() {
		res.Enforced = true
		res.Rules = len(targets)
		if len(warns) > 0 {
			res.Warning = strings.Join(warns, "; ")
		}
	}
	return res, nil
}

// containerIPs returns the container's IPv4/IPv6 addresses (incus list csv).
func (d *IncusDriver) containerIPs(ctx context.Context, name string) ([]string, error) {
	out, err := d.incus(ctx, "list", name, "--format", "csv", "-c", "46")
	if err != nil {
		return nil, err
	}
	var ips []string
	for _, f := range strings.FieldsFunc(out, func(r rune) bool { return r == ',' || r == '\n' || r == ' ' || r == '"' }) {
		if strings.Count(f, ".") == 3 || strings.Count(f, ":") >= 2 {
			ips = append(ips, f)
		}
	}
	if len(ips) == 0 {
		return nil, boxErr("PROVIDER_FAILED", "container has no IP address (is it running?)")
	}
	return ips, nil
}

func (d *IncusDriver) runNft(ctx context.Context, script string) error {
	if d.nft != nil {
		return d.nft(ctx, script)
	}
	cmd := exec.CommandContext(ctx, "nft", "-f", "-")
	cmd.Stdin = strings.NewReader(script)
	var errb bytes.Buffer
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(errb.String()))
	}
	return nil
}
