// Package envcompat keeps pre-rename configuration working: the product was
// renamed Boxa → Boxaz, so every BOXA_* environment variable is now BOXAZ_*.
// Apply copies each legacy BOXA_X into BOXAZ_X when BOXAZ_X is unset (the new
// name always wins) and reports the legacy names so callers can log a
// deprecation note once at startup.
package envcompat

import (
	"os"
	"sort"
	"strings"
)

const (
	Prefix       = "BOXAZ_"
	LegacyPrefix = "BOXA_" // deprecated, still honoured
)

// Apply maps legacy BOXA_* variables onto BOXAZ_* and returns the legacy names
// that were honoured (sorted). Variables already set under the new name are
// left alone; their legacy twins are reported as ignored via the second result.
func Apply() (used, ignored []string) {
	for _, kv := range os.Environ() {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || !strings.HasPrefix(k, LegacyPrefix) {
			continue
		}
		nk := Prefix + strings.TrimPrefix(k, LegacyPrefix)
		if _, set := os.LookupEnv(nk); set {
			ignored = append(ignored, k)
			continue
		}
		os.Setenv(nk, v)
		used = append(used, k)
	}
	sort.Strings(used)
	sort.Strings(ignored)
	return used, ignored
}

// Note is the one-line deprecation message for the result of Apply ("" when
// nothing legacy was found).
func Note(used, ignored []string) string {
	var parts []string
	if len(used) > 0 {
		parts = append(parts, "deprecated env vars honoured, rename them to BOXAZ_*: "+strings.Join(used, ", "))
	}
	if len(ignored) > 0 {
		parts = append(parts, "legacy env vars ignored because the BOXAZ_* name is set: "+strings.Join(ignored, ", "))
	}
	return strings.Join(parts, "; ")
}

// Get reads BOXAZ_<name>, falling back to the legacy BOXA_<name>.
func Get(name string) string {
	if v := os.Getenv(Prefix + name); v != "" {
		return v
	}
	return os.Getenv(LegacyPrefix + name)
}
