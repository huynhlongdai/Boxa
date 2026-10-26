package boxyaml

import "testing"

func TestParseSpecExample(t *testing.T) {
	src := `name: shopee-crawler
template: python-browser
spec: { cpu: 2, ram: 4G, disk: 20G }
lifecycle: session
idle_minutes: 5
ttl: 6h
budget_usd: 0.5
ports: [8080]
secrets: [SHOPEE_COOKIE]
setup: ["pip install -r requirements.txt"]
`
	r, errs := Parse(src)
	if errs != nil {
		t.Fatal(errs)
	}
	if r.Slug != "shopee-crawler" || r.Spec.CPU != 2 || r.Spec.RAMMB != 4096 || r.Spec.DiskGB != 20 || r.TTLSeconds != 6*3600 ||
		r.BudgetUSD != 0.5 || r.Ports[0] != 8080 || r.Secrets[0] != "SHOPEE_COOKIE" || len(r.Setup) != 1 || r.Lifecycle != "session" {
		t.Fatalf("%+v", r)
	}
}

func TestParseErrorsHaveLines(t *testing.T) {
	src := "template: python\nspec:\n  cpu: lots\n  ram: 64M\nports: [99999]\nsecrets: [lower]\nwhat: 1\n"
	_, errs := Parse(src)
	want := map[int]bool{3: true, 4: true, 5: true, 6: true, 7: true}
	if len(errs) != len(want) {
		t.Fatalf("errs: %+v", errs)
	}
	for _, e := range errs {
		if !want[e.Line] {
			t.Fatalf("unexpected line %d: %s", e.Line, e.Message)
		}
	}
	if _, errs := Parse("template: [unclosed\n"); len(errs) != 1 || errs[0].Line < 1 {
		t.Fatalf("syntax: %+v", errs)
	}
	if _, errs := Parse("name: x\n"); len(errs) != 1 {
		t.Fatalf("missing template: %+v", errs)
	}
}

func TestSizes(t *testing.T) {
	cases := map[string]int{"512M": 512, "4G": 4096, "4GB": 4096, "1.5G": 1536, "2GiB": 2048}
	for in, want := range cases {
		got, err := ParseSize(in, 1)
		if err != nil || got != want {
			t.Fatalf("%s: %d %v", in, got, err)
		}
	}
	d, err := ParseDuration("2d")
	if err != nil || d.Hours() != 48 {
		t.Fatal(d, err)
	}
}

func TestEgressYAML(t *testing.T) {
	r, errs := Parse("template: base\negress:\n  allow: [PyPI.org, \"*.github.com\", 10.0.0.0/8]\n  deny_by_default: true\n")
	if errs != nil || r.Egress == nil || !r.Egress.DenyByDefault || len(r.Egress.Allow) != 3 || r.Egress.Allow[0] != "pypi.org" {
		t.Fatalf("%+v %v", r, errs)
	}
	r, errs = Parse("template: base\negress: [api.openai.com:443]\n")
	if errs != nil || r.Egress.Allow[0] != "api.openai.com:443" || r.Egress.DenyByDefault {
		t.Fatalf("list form %+v %v", r.Egress, errs)
	}
	_, errs = Parse("template: base\negress:\n  allow: [\"bad host\"]\n  deny: true\n")
	if len(errs) != 2 || errs[0].Line != 3 || errs[1].Line != 4 {
		t.Fatalf("errors %+v", errs)
	}
}
