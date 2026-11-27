package envcompat

import (
	"os"
	"strings"
	"testing"
)

func TestApply(t *testing.T) {
	t.Setenv("BOXA_COMPAT_ONLY_OLD", "old")
	t.Setenv("BOXA_COMPAT_BOTH", "old")
	t.Setenv("BOXMES_COMPAT_BOTH", "new")
	t.Cleanup(func() { os.Unsetenv("BOXMES_COMPAT_ONLY_OLD") })
	used, ignored := Apply()
	if os.Getenv("BOXMES_COMPAT_ONLY_OLD") != "old" {
		t.Fatalf("legacy var not mapped")
	}
	if os.Getenv("BOXMES_COMPAT_BOTH") != "new" {
		t.Fatalf("new name must win")
	}
	if !contains(used, "BOXA_COMPAT_ONLY_OLD") || !contains(ignored, "BOXA_COMPAT_BOTH") {
		t.Fatalf("used=%v ignored=%v", used, ignored)
	}
	if n := Note(used, ignored); !strings.Contains(n, "deprecated") || !strings.Contains(n, "BOXA_COMPAT_BOTH") {
		t.Fatalf("note: %q", n)
	}
	if Note(nil, nil) != "" {
		t.Fatal("empty note expected")
	}
}

// BOXAZ_* (the Boxaz-era name) still works, BOXMES_* wins over it, and it wins
// over the older BOXA_*.
func TestApplyBoxaz(t *testing.T) {
	t.Setenv("BOXAZ_COMPAT_AZ_ONLY", "az")
	t.Setenv("BOXAZ_COMPAT_AZ_NEW", "az")
	t.Setenv("BOXMES_COMPAT_AZ_NEW", "mes")
	t.Setenv("BOXAZ_COMPAT_AZ_A", "az")
	t.Setenv("BOXA_COMPAT_AZ_A", "a")
	t.Setenv("BOXAZ_COMPAT_ALL", "az")
	t.Setenv("BOXA_COMPAT_ALL", "a")
	t.Setenv("BOXMES_COMPAT_ALL", "mes")
	t.Cleanup(func() {
		os.Unsetenv("BOXMES_COMPAT_AZ_ONLY")
		os.Unsetenv("BOXMES_COMPAT_AZ_A")
	})
	used, ignored := Apply()
	for k, want := range map[string]string{
		"BOXMES_COMPAT_AZ_ONLY": "az",  // BOXAZ_* honoured
		"BOXMES_COMPAT_AZ_NEW":  "mes", // BOXMES_* wins over BOXAZ_*
		"BOXMES_COMPAT_AZ_A":    "az",  // BOXAZ_* wins over BOXA_*
		"BOXMES_COMPAT_ALL":     "mes",
	} {
		if got := os.Getenv(k); got != want {
			t.Fatalf("%s = %q, want %q", k, got, want)
		}
	}
	if !contains(used, "BOXAZ_COMPAT_AZ_ONLY") || !contains(used, "BOXAZ_COMPAT_AZ_A") || contains(used, "BOXA_COMPAT_AZ_A") {
		t.Fatalf("used=%v", used)
	}
	for _, k := range []string{"BOXAZ_COMPAT_AZ_NEW", "BOXA_COMPAT_AZ_A", "BOXAZ_COMPAT_ALL", "BOXA_COMPAT_ALL"} {
		if !contains(ignored, k) {
			t.Fatalf("%s not reported as ignored: %v", k, ignored)
		}
	}
	if n := Note(used, ignored); !strings.Contains(n, "rename them to BOXMES_*") || !strings.Contains(n, "BOXAZ_COMPAT_AZ_ONLY") {
		t.Fatalf("note: %q", n)
	}
}

func TestGet(t *testing.T) {
	t.Setenv("BOXA_COMPAT_G", "old")
	if Get("COMPAT_G") != "old" {
		t.Fatal("fallback to BOXA_")
	}
	t.Setenv("BOXAZ_COMPAT_G", "az")
	if Get("COMPAT_G") != "az" {
		t.Fatal("BOXAZ_ must win over BOXA_")
	}
	t.Setenv("BOXMES_COMPAT_G", "new")
	if Get("COMPAT_G") != "new" {
		t.Fatal("new wins")
	}
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}
