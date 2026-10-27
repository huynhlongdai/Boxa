package model

import "testing"

func TestSlugifyAndNames(t *testing.T) {
	for in, want := range map[string]string{
		"Demo App":        "demo-app",
		"Cửa Hàng Đẹp!!":  "cua-hang-dep",
		"--a__b--":        "a-b",
		"node_server 2":   "node-server-2",
		"こんにちは":           "",
		"Tiệm Phở Hà Nội": "tiem-pho-ha-noi",
	} {
		if got := Slugify(in, MaxBoxNameLen); got != want {
			t.Errorf("Slugify(%q) = %q, want %q", in, got, want)
		}
	}
	if got := Slugify("a-very-long-name-that-goes-on-and-on-and-on-forever", 40); len(got) > 40 || got[len(got)-1] == '-' {
		t.Errorf("long: %q", got)
	}
	for _, ok := range []string{"demo-app", "a", "shop2", "x-1"} {
		if p := BoxNameProblem(ok); p != "" {
			t.Errorf("%q: %s", ok, p)
		}
	}
	for _, bad := range []string{"", "-a", "a-", "a--b", "Demo", "123", "0123456789abcdef0123-8000", "a.b", "a_b"} {
		if BoxNameProblem(bad) == "" {
			t.Errorf("%q should be refused", bad)
		}
	}
	for _, ok := range []string{"web", "api", "db2", "admin-ui"} {
		if p := PortLabelProblem(ok); p != "" {
			t.Errorf("label %q: %s", ok, p)
		}
	}
	for _, bad := range []string{"", "8000", "2web", "web-", "a--b", "WEB", "this-label-is-way-too-long"} {
		if PortLabelProblem(bad) == "" {
			t.Errorf("label %q should be refused", bad)
		}
	}
}
