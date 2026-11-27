// Package i18n localizes every agent- and operator-facing text Boxmes emits:
// API error message+hint, snippet comments, AGENTS.md, llms.txt intro, MCP
// prompts and next-call hints. Supported locales: en (default for the API),
// vi, zh, ja. The catalog is embedded JSON (catalog/<locale>.json):
//
//	{"msg":  {"<English fmt>": "<translated fmt>"},   // error messages (fmt verbs kept in order)
//	 "hint": {"<English hint>": "<translated hint>"}, // error hints
//	 "text": {"<key>": "<text>"}}                     // keyed texts (en.json is the source)
//
// Errors use the English format string itself as the key (gettext style), so
// call sites keep writing model.E(code, "box %s offline", id) and the gateway
// localizes at write time. A missing translation falls back to English.
package i18n

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

//go:embed catalog/*.json
var catalogFS embed.FS

// Supported locales, default first.
var Locales = []string{"en", "vi", "zh", "ja"}

const Default = "en"

type catalog struct {
	Msg  map[string]string `json:"msg"`
	Hint map[string]string `json:"hint"`
	Text map[string]string `json:"text"`
}

var cats = map[string]*catalog{}

func init() {
	for _, l := range Locales {
		b, err := catalogFS.ReadFile("catalog/" + l + ".json")
		if err != nil {
			panic("i18n: missing catalog " + l)
		}
		var c catalog
		if err := json.Unmarshal(b, &c); err != nil {
			panic("i18n: bad catalog " + l + ": " + err.Error())
		}
		cats[l] = &c
	}
}

// Supported reports whether l is a supported locale id.
func Supported(l string) bool { _, ok := cats[l]; return ok }

// Normalize maps a language tag (vi-VN, zh-Hant, ja_JP.UTF-8, EN) to a supported
// locale, or "" when unsupported.
func Normalize(tag string) string {
	t := strings.ToLower(strings.TrimSpace(tag))
	if i := strings.IndexAny(t, "-_.@"); i >= 0 {
		t = t[:i]
	}
	if Supported(t) {
		return t
	}
	return ""
}

// Parse picks the best supported locale from an Accept-Language header,
// honouring q-values; returns def when nothing matches.
func Parse(header, def string) string {
	type cand struct {
		loc string
		q   float64
		i   int
	}
	var cs []cand
	for i, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		q := 1.0
		if semi := strings.Index(part, ";"); semi >= 0 {
			for _, p := range strings.Split(part[semi+1:], ";") {
				p = strings.TrimSpace(p)
				if strings.HasPrefix(p, "q=") {
					if v, err := strconv.ParseFloat(p[2:], 64); err == nil {
						q = v
					}
				}
			}
			part = part[:semi]
		}
		if l := Normalize(part); l != "" && q > 0 {
			cs = append(cs, cand{l, q, i})
		}
	}
	if len(cs) == 0 {
		return def
	}
	sort.SliceStable(cs, func(a, b int) bool { return cs[a].q > cs[b].q })
	return cs[0].loc
}

// FromRequest resolves the locale of an HTTP request: ?lang= wins, then
// Accept-Language, then def.
func FromRequest(r *http.Request, def string) string {
	if l := Normalize(r.URL.Query().Get("lang")); l != "" {
		return l
	}
	return Parse(r.Header.Get("Accept-Language"), def)
}

type ctxKey struct{}

// WithLocale stores the locale in a context.
func WithLocale(ctx context.Context, l string) context.Context {
	return context.WithValue(ctx, ctxKey{}, l)
}

// FromContext returns the locale stored by WithLocale, or Default.
func FromContext(ctx context.Context) string {
	if l, ok := ctx.Value(ctxKey{}).(string); ok && l != "" {
		return l
	}
	return Default
}

func get(l string) *catalog {
	if c, ok := cats[l]; ok {
		return c
	}
	return cats[Default]
}

// Msg formats an error message format in locale l.
func Msg(l, format string, args ...any) string {
	f := format
	if t, ok := get(l).Msg[format]; ok && t != "" {
		f = t
	}
	if len(args) == 0 && !strings.Contains(f, "%") {
		return f
	}
	return fmt.Sprintf(f, args...)
}

// Hint translates an English hint (unknown hints are returned unchanged).
func Hint(l, hint string) string {
	if t, ok := get(l).Hint[hint]; ok && t != "" {
		return t
	}
	return hint
}

// T returns the keyed text in locale l, falling back to English, then the key.
// Optional args are applied with fmt.Sprintf.
func T(l, key string, args ...any) string {
	s, ok := get(l).Text[key]
	if !ok || s == "" {
		s, ok = cats[Default].Text[key]
		if !ok {
			s = key
		}
	}
	if len(args) > 0 {
		return fmt.Sprintf(s, args...)
	}
	return s
}

// HasText reports whether the English catalog defines key.
func HasText(key string) bool { _, ok := cats[Default].Text[key]; return ok }

// Keys returns the keys of one section ("msg", "hint", "text") of locale l.
func Keys(l, section string) []string {
	c := get(l)
	var m map[string]string
	switch section {
	case "msg":
		m = c.Msg
	case "hint":
		m = c.Hint
	default:
		m = c.Text
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Name is the native display name of a locale.
func Name(l string) string {
	switch l {
	case "vi":
		return "Tiếng Việt"
	case "zh":
		return "中文"
	case "ja":
		return "日本語"
	}
	return "English"
}

// Lookup returns the locale stored by WithLocale and whether one was set.
func Lookup(ctx context.Context) (string, bool) {
	l, ok := ctx.Value(ctxKey{}).(string)
	return l, ok && l != ""
}
