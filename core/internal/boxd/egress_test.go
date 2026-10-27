package boxd

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"boxaz/core/internal/proto"
)

func fakeResolver(ctx context.Context, host string) ([]string, error) {
	if host == "pypi.org" {
		return []string{"151.101.0.223"}, nil
	}
	return nil, errors.New("nxdomain")
}

func TestIncusEgressACL(t *testing.T) {
	d := NewIncusDriver("", "")
	d.Resolver = fakeResolver
	var calls []string
	d.run = func(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, args ...string) error {
		c := strings.Join(args, " ")
		calls = append(calls, c)
		if strings.HasPrefix(c, "network acl delete") {
			stderr.Write([]byte("Error: Network ACL not found"))
			return errors.New("exit 1")
		}
		if strings.HasPrefix(c, "config device override") {
			stderr.Write([]byte("Error: The device already exists"))
			return errors.New("exit 1")
		}
		return nil
	}
	res, err := d.ApplyEgress(context.Background(), proto.EgressParams{BoxID: "box_e1", Allow: []string{"pypi.org:443", "10.0.0.0/8"}, DenyByDefault: true})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Enforced || res.Mode != "incus-acl" || res.Rules != 2 {
		t.Fatalf("result %+v", res)
	}
	all := strings.Join(calls, "\n")
	for _, w := range []string{
		"network acl create boxaz-eg-box-e1",
		"network acl rule add boxaz-eg-box-e1 egress action=allow protocol=udp destination_port=53 description=dns",
		"network acl rule add boxaz-eg-box-e1 egress action=allow destination=151.101.0.223/32 protocol=tcp destination_port=443",
		"network acl rule add boxaz-eg-box-e1 egress action=allow destination=10.0.0.0/8",
		"config device set boxaz-box-e1 eth0 security.acls=boxaz-eg-box-e1 security.acls.default.egress.action=drop",
	} {
		if !strings.Contains(all, w) {
			t.Errorf("missing %q in\n%s", w, all)
		}
	}
	// clearing the policy detaches + deletes, ignoring "not found"
	calls = nil
	res, err = d.ApplyEgress(context.Background(), proto.EgressParams{BoxID: "box_e1"})
	if err != nil || res.Enforced {
		t.Fatalf("clear %+v %v", res, err)
	}
	if calls[len(calls)-1] != "network acl delete boxaz-eg-box-e1" {
		t.Fatalf("clear calls %v", calls)
	}
}

func TestIncusEgressNft(t *testing.T) {
	d := NewIncusDriver("", "")
	d.EgressMode = "nft"
	d.Resolver = fakeResolver
	d.run = func(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, args ...string) error {
		if args[0] == "list" {
			stdout.Write([]byte("\"10.88.0.7 (eth0)\",\"fd42:1::7 (eth0)\"\n"))
		}
		return nil
	}
	var script string
	d.nft = func(ctx context.Context, s string) error { script = s; return nil }
	res, err := d.ApplyEgress(context.Background(), proto.EgressParams{BoxID: "box_n1", Allow: []string{"pypi.org", "unknown.invalid"}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Enforced || res.Mode != "nftables" || len(res.Warnings) != 1 {
		t.Fatalf("result %+v", res)
	}
	for _, w := range []string{"ip saddr { 10.88.0.7 } ip daddr 151.101.0.223/32 accept", "ip6 saddr { fd42:1::7 } counter drop", "udp dport 53 accept"} {
		if !strings.Contains(script, w) {
			t.Errorf("script lacks %q:\n%s", w, script)
		}
	}
}

func TestLocalEgressBestEffort(t *testing.T) {
	d, err := NewLocalDriver(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := d.Create(ctx, proto.CreateParams{BoxID: "box_l1"}); err != nil {
		t.Fatal(err)
	}
	res, err := d.ApplyEgress(ctx, proto.EgressParams{BoxID: "box_l1", Allow: []string{"pypi.org"}, DenyByDefault: true})
	if err != nil || res.Enforced || res.Mode != "none" || !strings.Contains(res.Warning, "NOT enforced") {
		t.Fatalf("local egress %+v %v", res, err)
	}
	var out strings.Builder
	d.Run(ctx, "box_l1", proto.ExecParams{Command: "echo $BOXAZ_EGRESS_ALLOW/$BOXAZ_EGRESS_ENFORCED", TimeoutS: 5}, &out, io.Discard)
	if strings.TrimSpace(out.String()) != "pypi.org/0" {
		t.Fatalf("env %q", out.String())
	}
	res, _ = d.ApplyEgress(ctx, proto.EgressParams{BoxID: "box_l1"})
	if res.Warning != "" {
		t.Fatal("cleared policy must not warn")
	}
	_ = os.Remove(filepath.Join(d.Root, "x"))
}
