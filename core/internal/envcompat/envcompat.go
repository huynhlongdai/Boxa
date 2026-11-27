// Package envcompat keeps pre-rename configuration working. The product was
// renamed twice — Boxa → Boxaz, then Boxaz → Boxmes — so every BOXA_* and
// BOXAZ_* environment variable is now BOXMES_*. Apply copies each legacy
// variable onto its BOXMES_* name when that is unset. Lookup order is
// BOXMES_* → BOXAZ_* → BOXA_* (the newest name always wins) and the legacy
// names are reported so callers can log a deprecation note once at startup.
package envcompat

import (
	"os"
	"sort"
	"strings"
)

const (
	Prefix       = "BOXMES_"
	LegacyPrefix = "BOXAZ_" // deprecated (Boxaz → Boxmes), still honoured
	OldestPrefix = "BOXA_"  // deprecated (Boxa → Boxaz), still honoured
)

// LegacyPrefixes lists the deprecated prefixes, newest first: when both a
// BOXAZ_X and a BOXA_X are set (and BOXMES_X is not), BOXAZ_X wins.
var LegacyPrefixes = []string{LegacyPrefix, OldestPrefix}

// legacyName reports the suffix of a legacy variable and the rank of its
// prefix in LegacyPrefixes (-1 when k is not a legacy name).
func legacyName(k string) (string, int) {
	for i, p := range LegacyPrefixes {
		if strings.HasPrefix(k, p) {
			return strings.TrimPrefix(k, p), i
		}
	}
	return "", -1
}

// Apply maps legacy BOXAZ_* / BOXA_* variables onto BOXMES_* and returns the
// legacy names that were honoured (sorted). Variables already set under the
// new name are left alone; their legacy twins are reported as ignored via the
// second result, as is a BOXA_X shadowed by a BOXAZ_X.
func Apply() (used, ignored []string) {
	type cand struct {
		key, val string
		rank     int
	}
	best := map[string]cand{} // suffix -> legacy variable to use
	var shadowed []string
	for _, kv := range os.Environ() {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		suffix, rank := legacyName(k)
		if rank < 0 {
			continue
		}
		if _, set := os.LookupEnv(Prefix + suffix); set {
			ignored = append(ignored, k)
			continue
		}
		if cur, ok := best[suffix]; ok {
			if rank < cur.rank {
				shadowed = append(shadowed, cur.key)
				best[suffix] = cand{k, v, rank}
			} else {
				shadowed = append(shadowed, k)
			}
			continue
		}
		best[suffix] = cand{k, v, rank}
	}
	for suffix, c := range best {
		os.Setenv(Prefix+suffix, c.val)
		used = append(used, c.key)
	}
	ignored = append(ignored, shadowed...)
	sort.Strings(used)
	sort.Strings(ignored)
	return used, ignored
}

// Note is the one-line deprecation message for the result of Apply ("" when
// nothing legacy was found).
func Note(used, ignored []string) string {
	var parts []string
	if len(used) > 0 {
		parts = append(parts, "deprecated env vars honoured, rename them to BOXMES_*: "+strings.Join(used, ", "))
	}
	if len(ignored) > 0 {
		parts = append(parts, "legacy env vars ignored because a newer name is set: "+strings.Join(ignored, ", "))
	}
	return strings.Join(parts, "; ")
}

// Get reads BOXMES_<name>, falling back to the legacy BOXAZ_<name>, then
// BOXA_<name>.
func Get(name string) string {
	if v := os.Getenv(Prefix + name); v != "" {
		return v
	}
	for _, p := range LegacyPrefixes {
		if v := os.Getenv(p + name); v != "" {
			return v
		}
	}
	return ""
}
