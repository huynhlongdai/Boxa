package model

// v1.9 readable gateway names: a box is reached at https://<name>.<domain>/
// (primary port) and https://<name>-<label>.<domain>/ (other ports). Names
// and labels are DNS-safe words; raw port numbers never appear in the host.

import (
	"regexp"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

const (
	// MaxBoxNameLen leaves room for "-<label>" inside one 63-char DNS label.
	MaxBoxNameLen = 40
	// MaxPortLabelLen is the longest port label.
	MaxPortLabelLen = 20
)

var (
	boxNameRE   = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)
	portLabelRE = regexp.MustCompile(`^[a-z]([a-z0-9-]*[a-z0-9])?$`)
	legacySegRE = regexp.MustCompile(`^[0-9a-f]{20}-[0-9]{1,5}$`)
	hasLetterRE = regexp.MustCompile(`[a-z]`)
)

// Slugify turns free text ("Demo App", "Cửa hàng") into a DNS-safe name:
// lowercase a-z0-9 and single hyphens, accents folded (đ → d), trimmed to max.
func Slugify(s string, max int) string {
	s = strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(s), "đ", "d"), "Đ", "D")
	var b strings.Builder
	dash := false
	for _, r := range norm.NFD.String(s) {
		if unicode.Is(unicode.Mn, r) {
			continue
		}
		r = unicode.ToLower(r)
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		default:
			if !dash && b.Len() > 0 {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if max > 0 && len(out) > max {
		out = strings.Trim(out[:max], "-")
	}
	return out
}

// BoxNameProblem explains why name cannot be a box's gateway name ("" = ok).
// Reserved names are checked by the manager (the list is configurable).
func BoxNameProblem(name string) string {
	switch {
	case name == "":
		return "name is empty"
	case len(name) > MaxBoxNameLen:
		return "name is longer than 40 characters"
	case !boxNameRE.MatchString(name) || strings.Contains(name, "--"):
		return "name must be lowercase letters, digits and single '-' (not at the start or end)"
	case !hasLetterRE.MatchString(name):
		return "name needs at least one letter (no bare numbers)"
	case legacySegRE.MatchString(name):
		return "name looks like a legacy <key>-<port> address"
	}
	return ""
}

// PortLabelProblem explains why label cannot name a port ("" = ok).
func PortLabelProblem(label string) string {
	switch {
	case label == "":
		return "label is empty"
	case len(label) > MaxPortLabelLen:
		return "label is longer than 20 characters"
	case !portLabelRE.MatchString(label) || strings.Contains(label, "--"):
		return "label must start with a letter and use lowercase letters, digits and single '-'"
	}
	return ""
}
