package sandbox

// M29-S: templates (E2B, Novita) and snapshots (Daytona) belong to one
// provider account (E2B project, Novita team, Daytona project). Core keeps
// a per-account availability list so a box built from template X is only
// placed on an account that has X. TemplateLister lets core sync that list
// from the provider API; drivers without it rely on the account's manual
// list and on "template not found" answers learned at create time.

import (
	"context"
	"encoding/json"
	"strings"
)

// TemplateLister lists the templates (or snapshots) the credential's
// account can create sandboxes from: the account's own plus the public ones
// the provider returns.
type TemplateLister interface {
	ListTemplates(ctx context.Context, c Cred) ([]string, error)
}

// GlobalTemplate reports whether a template reference works from any
// account of the provider: a namespaced public template ("team-slug/name",
// E2B) or one of the provider's built-in sizes (e.g. E2B / Novita "base").
func GlobalTemplate(info Info, ref string) bool {
	if strings.Contains(ref, "/") {
		return true
	}
	for _, z := range info.Sizes {
		if z.SKU == ref {
			return true
		}
	}
	return false
}

// ListTemplates (E2B, Novita): GET /templates answers the account's
// templates with their id and aliases/names.
func (d *E2B) ListTemplates(ctx context.Context, c Cred) ([]string, error) {
	var raw json.RawMessage
	if err := d.h.do(ctx, "GET", d.api(c, "")+"/templates", d.hdr(c), nil, &raw); err != nil {
		return nil, wrapErr(d.info.Name, err)
	}
	var l []struct {
		TemplateID string   `json:"templateID"`
		Aliases    []string `json:"aliases"`
		Names      []string `json:"names"`
	}
	if err := json.Unmarshal(raw, &l); err != nil {
		var page struct {
			Templates json.RawMessage `json:"templates"`
		}
		if json.Unmarshal(raw, &page) != nil || json.Unmarshal(page.Templates, &l) != nil {
			return nil, err
		}
	}
	var out []string
	for _, t := range l {
		out = append(out, t.TemplateID)
		out = append(out, t.Aliases...)
		for _, n := range t.Names { // "team-slug/name" (and the short name works inside the team)
			out = append(out, n)
			if i := strings.LastIndex(n, "/"); i >= 0 {
				out = append(out, n[i+1:])
			}
		}
	}
	return dedupe(out), nil
}

// ListTemplates (Daytona): snapshots of the key's project.
func (d *Daytona) ListTemplates(ctx context.Context, c Cred) ([]string, error) {
	var raw json.RawMessage
	if err := d.h.do(ctx, "GET", d.api(c)+"/snapshots", d.hdr(c), nil, &raw); err != nil {
		return nil, wrapErr(d.info.Name, err)
	}
	type snap struct {
		ID    string `json:"id"`
		Name  string `json:"name"`
		State string `json:"state"`
	}
	var l []snap
	if err := json.Unmarshal(raw, &l); err != nil {
		var page struct {
			Items []snap `json:"items"`
		}
		if err := json.Unmarshal(raw, &page); err != nil {
			return nil, err
		}
		l = page.Items
	}
	var out []string
	for _, s := range l {
		if st := strings.ToLower(s.State); st != "" && st != "active" && st != "ready" {
			continue // building / error snapshots cannot be restored yet
		}
		out = append(out, s.Name, s.ID)
	}
	return dedupe(out), nil
}

func dedupe(l []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, x := range l {
		x = strings.TrimSpace(x)
		if x == "" || seen[x] {
			continue
		}
		seen[x] = true
		out = append(out, x)
	}
	return out
}
