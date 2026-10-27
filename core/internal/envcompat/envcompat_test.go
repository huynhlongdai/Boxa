package envcompat

import (
	"os"
	"strings"
	"testing"
)

func TestApply(t *testing.T) {
	t.Setenv("BOXA_COMPAT_ONLY_OLD", "old")
	t.Setenv("BOXA_COMPAT_BOTH", "old")
	t.Setenv("BOXAZ_COMPAT_BOTH", "new")
	t.Cleanup(func() { os.Unsetenv("BOXAZ_COMPAT_ONLY_OLD") })
	used, ignored := Apply()
	if os.Getenv("BOXAZ_COMPAT_ONLY_OLD") != "old" {
		t.Fatalf("legacy var not mapped")
	}
	if os.Getenv("BOXAZ_COMPAT_BOTH") != "new" {
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

func TestGet(t *testing.T) {
	t.Setenv("BOXA_COMPAT_G", "old")
	if Get("COMPAT_G") != "old" {
		t.Fatal("fallback")
	}
	t.Setenv("BOXAZ_COMPAT_G", "new")
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
