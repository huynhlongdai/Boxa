package manager

// v1.9 readable gateway URLs. Every box gets a DNS name (its subdomain) and
// every exposed port a text label:
//
//	https://<name>.<domain>/          → the box's primary port (app spec port, else the first port)
//	https://<name>-<label>.<domain>/  → any port by its label (web, api, db, …)
//
// Path mode (no gateway domain) uses the same label: <public>/x/<name>/ and
// <public>/x/<name>-<label>/. The v1.7/v1.8 <key>-<port> addresses keep
// working as aliases. Names are unique across the gateway; a derived name that
// is taken gets a word suffix (demo-app → demo-app-blue), an explicit name
// that is taken is refused with a suggestion.

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"boxaz/core/internal/model"
	"boxaz/core/internal/secret"
	"boxaz/core/internal/store"
)

type nameState struct {
	mu      sync.Mutex
	pending map[string]pendingName // host label → box, claimed but maybe not stored yet
}

type pendingName struct {
	box string
	at  time.Time
}

// nameSuffixes make a taken derived name unique without digits.
var nameSuffixes = []string{"blue", "green", "amber", "coral", "jade", "ruby", "teal", "navy", "sky", "sun",
	"moon", "star", "leaf", "oak", "pine", "fox", "owl", "wolf", "bear", "hawk"}

// labelWords name ports that have no well-known label (never a number).
var labelWords = []string{"alpha", "bravo", "charlie", "delta", "echo", "foxtrot", "golf", "hotel", "india", "juliet",
	"kilo", "lima", "mike", "november", "oscar", "papa", "quebec", "romeo", "sierra", "tango", "uniform", "victor",
	"whiskey", "xray", "yankee", "zulu"}

// wellKnownLabels is the default label of common ports.
var wellKnownLabels = map[int]string{
	80: "web", 3000: "web", 4200: "web", 4321: "web", 5000: "web", 5173: "web", 8000: "web", 8080: "web",
	3001: "api", 4000: "api", 8001: "api", 8081: "admin", 9000: "admin", 9001: "console",
	5432: "db", 3306: "mysql", 6379: "redis", 27017: "mongo", 9200: "search", 5672: "amqp", 15672: "rabbitmq",
	8888: "jupyter", 7860: "gradio", 8501: "streamlit", 6006: "tensorboard", 11434: "ollama", 9229: "debug",
	9090: "metrics", 6080: "vnc", 8765: "ws", 2368: "blog", 1880: "nodered", 5555: "flower",
}

// webAlternates follow "web" when a box has several web ports.
var webAlternates = []string{"app", "site", "ui"}

// PrimaryPort is the port served at https://<name>.<domain>/: the app spec
// port when it is exposed, else the first exposed port (0 = none).
func PrimaryPort(b *model.Box) int {
	if len(b.Ports) == 0 {
		return 0
	}
	if b.App != nil && b.App.Port > 0 {
		for _, p := range b.Ports {
			if p == b.App.Port {
				return p
			}
		}
	}
	return b.Ports[0]
}

// PortLabel is the text label of a box port ("" before names are assigned).
func PortLabel(b *model.Box, port int) string { return b.PortLabels[port] }

// hostLabel is the gateway host label of a box port: <name> for the primary
// port, <name>-<label> otherwise ("" when the box has no name yet).
func hostLabel(b *model.Box, port int) string {
	if b.Subdomain == "" {
		return ""
	}
	if port == PrimaryPort(b) {
		return b.Subdomain
	}
	if l := PortLabel(b, port); l != "" {
		return b.Subdomain + "-" + l
	}
	return ""
}

// boxHosts lists every host label a box answers on → port.
func boxHosts(b *model.Box) map[string]int {
	out := map[string]int{}
	if b.Subdomain == "" {
		return out
	}
	if pp := PrimaryPort(b); pp > 0 {
		out[b.Subdomain] = pp
	} else {
		out[b.Subdomain] = 0 // a name with no port yet still holds the name
	}
	for _, p := range b.Ports {
		if l := PortLabel(b, p); l != "" {
			out[b.Subdomain+"-"+l] = p
		}
	}
	return out
}

// nameIndex maps every host label in use (all boxes that are not terminated,
// plus recent claims) to its box id, leaving out box exclude.
func (m *Manager) nameIndex(ctx context.Context, exclude string) map[string]string {
	idx := map[string]string{}
	boxes, _ := m.Store.ListBoxes(ctx, store.BoxFilter{})
	for _, b := range boxes {
		if b.ID == exclude || b.Status == model.StatusTerminated {
			continue
		}
		for h := range boxHosts(b) {
			idx[h] = b.ID
		}
	}
	for h, pn := range m.names.pending {
		if time.Since(pn.at) > 2*time.Minute {
			delete(m.names.pending, h)
			continue
		}
		if sb, err := m.Store.GetBox(ctx, pn.box); err == nil {
			// stored: the store is the truth once the claim reached it, the
			// box ended, or the claim is older than a store write takes
			if _, has := boxHosts(sb)[h]; has || sb.Status == model.StatusTerminated || time.Since(pn.at) > 10*time.Second {
				delete(m.names.pending, h)
				continue
			}
		}
		if pn.box != exclude {
			if _, ok := idx[h]; !ok {
				idx[h] = pn.box
			}
		}
	}
	return idx
}

func (m *Manager) claim(b *model.Box) {
	if m.names.pending == nil {
		m.names.pending = map[string]pendingName{}
	}
	for h := range boxHosts(b) {
		m.names.pending[h] = pendingName{box: b.ID, at: time.Now()}
	}
}

// nameClash returns the first host of (name, labels) that is reserved or
// used by another box ("" = free).
func nameClash(idx map[string]string, name string, labels map[int]string) string {
	if IsReservedSubdomain(name) {
		return name
	}
	if _, ok := idx[name]; ok {
		return name
	}
	for _, l := range labels {
		if _, ok := idx[name+"-"+l]; ok {
			return name + "-" + l
		}
	}
	return ""
}

// freeName makes base unique with word suffixes (base, base-blue, …,
// base-blue-oak, …); a hex suffix only as the very last resort.
func freeName(idx map[string]string, base string, labels map[int]string) string {
	if base == "" || model.BoxNameProblem(base) != "" {
		base = "box"
	}
	try := func(n string) bool { return model.BoxNameProblem(n) == "" && nameClash(idx, n, labels) == "" }
	if try(base) {
		return base
	}
	cut := func(n string, room int) string {
		if len(n) > model.MaxBoxNameLen-room {
			n = strings.Trim(n[:model.MaxBoxNameLen-room], "-")
		}
		return n
	}
	for _, w := range nameSuffixes {
		if n := cut(base, len(w)+1) + "-" + w; try(n) {
			return n
		}
	}
	for _, w1 := range nameSuffixes {
		for _, w2 := range nameSuffixes {
			if w1 == w2 {
				continue
			}
			if n := cut(base, len(w1)+len(w2)+2) + "-" + w1 + "-" + w2; try(n) {
				return n
			}
		}
	}
	for {
		if n := cut(base, 9) + "-x" + randWord(); try(n) {
			return n
		}
	}
}

func randWord() string {
	var b strings.Builder
	for _, c := range secret.RandHex(4) { // hex → letters a-p (no digits)
		if c >= '0' && c <= '9' {
			b.WriteByte(byte('a' + c - '0'))
		} else {
			b.WriteByte(byte('k' + c - 'a'))
		}
	}
	return b.String()
}

// labelCandidates lists the labels to try for a port, best first.
func labelCandidates(b *model.Box, port int) []string {
	var c []string
	if b.App != nil && b.App.Port == port && b.App.Label != "" {
		c = append(c, b.App.Label)
	}
	if l, ok := wellKnownLabels[port]; ok {
		c = append(c, l)
		if l == "web" {
			c = append(c, webAlternates...)
		}
	}
	return append(c, labelWords...)
}

// assignLabels gives every port without a label the first free candidate
// (unique in the box, and <name>-<label> unused by other boxes).
func assignLabels(idx map[string]string, b *model.Box) {
	if b.PortLabels == nil {
		b.PortLabels = map[int]string{}
	}
	for p := range b.PortLabels { // forget labels of ports that are gone
		found := false
		for _, x := range b.Ports {
			found = found || x == p
		}
		if !found {
			delete(b.PortLabels, p)
		}
	}
	used := map[string]bool{}
	for _, l := range b.PortLabels {
		used[l] = true
	}
	for _, p := range b.Ports {
		if b.PortLabels[p] != "" {
			continue
		}
		for _, l := range labelCandidates(b, p) {
			if used[l] || model.PortLabelProblem(l) != "" {
				continue
			}
			if b.Subdomain != "" {
				if _, taken := idx[b.Subdomain+"-"+l]; taken {
					continue
				}
			}
			b.PortLabels[p], used[l] = l, true
			break
		}
	}
	if len(b.PortLabels) == 0 {
		b.PortLabels = nil
	}
}

// baseName is the name a box would like: its subdomain, its box_ensure
// name, else its slug.
func baseName(b *model.Box) string {
	for _, s := range []string{b.Subdomain, b.Name, b.Slug} {
		if n := model.Slugify(s, model.MaxBoxNameLen); n != "" && hasLetter(n) {
			return n
		}
	}
	return "box"
}

func hasLetter(s string) bool {
	return strings.IndexFunc(s, func(r rune) bool { return r >= 'a' && r <= 'z' }) >= 0
}

// ensureNames gives a box a unique gateway name (word suffix on a clash) and
// a label for every port. It does not store the box; the caller does.
func (m *Manager) ensureNames(ctx context.Context, b *model.Box) {
	m.names.mu.Lock()
	defer m.names.mu.Unlock()
	idx := m.nameIndex(ctx, b.ID)
	assignLabels(idx, b)
	if b.Subdomain == "" || model.BoxNameProblem(b.Subdomain) != "" || nameClash(idx, b.Subdomain, b.PortLabels) != "" {
		b.Subdomain = freeName(idx, baseName(b), b.PortLabels)
	}
	// labels picked before the name was known may clash under the new name
	for p, l := range b.PortLabels {
		if _, taken := idx[b.Subdomain+"-"+l]; taken {
			delete(b.PortLabels, p)
		}
	}
	assignLabels(idx, b)
	m.claim(b)
}

// ensureNamesStored assigns missing names to a stored box and saves it
// (boxes created before v1.9, or a port added without going through Expose).
func (m *Manager) ensureNamesStored(ctx context.Context, b *model.Box) *model.Box {
	if !needsNames(b) {
		return b
	}
	lock := m.boxLock(b.ID)
	lock.Lock()
	defer lock.Unlock()
	cur, err := m.Store.GetBox(ctx, b.ID)
	if err != nil {
		return b
	}
	if needsNames(cur) {
		m.ensureNames(ctx, cur)
		m.Store.PutBox(ctx, cur)
	}
	return cur
}

func needsNames(b *model.Box) bool {
	if b.Status == model.StatusTerminated {
		return false
	}
	if b.Subdomain == "" {
		return true
	}
	for _, p := range b.Ports {
		if b.PortLabels[p] == "" {
			return true
		}
	}
	return false
}

// backfillNames names every live box that has no name yet (startup).
func (m *Manager) backfillNames(ctx context.Context) {
	boxes, _ := m.Store.ListBoxes(ctx, store.BoxFilter{})
	sort.Slice(boxes, func(i, j int) bool { return boxes[i].CreatedAt.Before(boxes[j].CreatedAt) })
	for _, b := range boxes {
		m.ensureNamesStored(ctx, b)
	}
}

// claimExplicitName sets a name the caller asked for: refused when invalid,
// reserved or taken (the error suggests a free one).
func (m *Manager) claimExplicitName(ctx context.Context, b *model.Box, want string) error {
	name := model.Slugify(want, 0) // "Tiệm Phở" → tiem-pho
	if name == "" {
		name = strings.ToLower(strings.TrimSpace(want))
	}
	if msg := model.BoxNameProblem(name); msg != "" {
		return model.EH(model.ErrInvalid, "Use 1-40 lowercase letters, digits and '-', e.g. \"demo-app\".", "invalid box name %q: %s", want, msg)
	}
	m.names.mu.Lock()
	defer m.names.mu.Unlock()
	idx := m.nameIndex(ctx, b.ID)
	if IsReservedSubdomain(name) {
		e := model.EH(model.ErrInvalid, "Pick another name; reserved names belong to other services of the domain.", "box name %q is reserved", name)
		e.Next = []string{fmt.Sprintf("box url %s --name %s", b.ID, freeName(idx, name, b.PortLabels))}
		return e
	}
	if c := nameClash(idx, name, b.PortLabels); c != "" {
		e := model.EH(model.ErrInvalid, "Pick another name, or let Boxaz add a word suffix by leaving it out.", "box name %q is taken (%s is in use)", name, c)
		e.Next = []string{fmt.Sprintf("box url %s --name %s", b.ID, freeName(idx, name, b.PortLabels))}
		return e
	}
	b.Subdomain = name
	for p, l := range b.PortLabels {
		if _, taken := idx[name+"-"+l]; taken {
			delete(b.PortLabels, p)
		}
	}
	assignLabels(idx, b)
	m.claim(b)
	return nil
}

// setPortLabel names one port of a box: refused when invalid, used by
// another port of the box, or <name>-<label> is taken.
func (m *Manager) setPortLabel(ctx context.Context, b *model.Box, port int, want string) error {
	label := strings.ToLower(strings.TrimSpace(want))
	if msg := model.PortLabelProblem(label); msg != "" {
		return model.EH(model.ErrInvalid, "Use a word like web, api, admin or db (letters first, at most 20 characters).", "invalid port label %q: %s", want, msg)
	}
	for p, l := range b.PortLabels {
		if p != port && l == label {
			return model.EH(model.ErrInvalid, "Each port of a box needs its own label.", "label %q is already used by port %d", label, p)
		}
	}
	m.names.mu.Lock()
	defer m.names.mu.Unlock()
	idx := m.nameIndex(ctx, b.ID)
	if b.Subdomain != "" {
		if _, taken := idx[b.Subdomain+"-"+label]; taken {
			return model.EH(model.ErrInvalid, "Pick another label or rename the box.", "%s is already in use by another box", b.Subdomain+"-"+label)
		}
	}
	if b.PortLabels == nil {
		b.PortLabels = map[int]string{}
	}
	b.PortLabels[port] = label
	m.claim(b)
	return nil
}

// BoxForHostLabel resolves a readable gateway label (<name> or
// <name>-<label>) to a running or sleeping box and port.
func (m *Manager) BoxForHostLabel(ctx context.Context, label string) (*model.Box, int, error) {
	label = strings.ToLower(label)
	boxes, err := m.Store.ListBoxes(ctx, store.BoxFilter{Statuses: []string{model.StatusRunning, model.StatusSleeping}})
	if err != nil {
		return nil, 0, err
	}
	for _, b := range boxes {
		if p, ok := boxHosts(b)[label]; ok && p > 0 {
			return b, p, nil
		}
	}
	return nil, 0, model.E(model.ErrNotFound, "no box at this address")
}

// legacySeg is the v1.7/v1.8 alias segment of a box port: <key>-<port>.
func (m *Manager) legacySeg(b *model.Box, port int) string {
	return m.exposureKey(b.ID) + "-" + strconv.Itoa(port)
}

// gatewayURLFor builds the gateway URL of one segment (a host label in host
// mode, a path segment otherwise).
func (m *Manager) gatewayURLFor(seg string) string {
	if d := m.GatewayDomain(context.Background()); d != "" {
		scheme := "https://"
		if strings.HasPrefix(m.Cfg.PublicURL, "http://") {
			scheme = "http://"
		}
		return scheme + seg + "." + d + "/"
	}
	if m.Cfg.PublicURL == "" {
		return "/x/" + seg + "/"
	}
	return strings.TrimRight(m.Cfg.PublicURL, "/") + "/x/" + seg + "/"
}

// GatewayAliasURL is the v1.7/v1.8 <key>-<port> URL of a box port; it keeps
// working next to the readable URL.
func (m *Manager) GatewayAliasURL(b *model.Box, port int) string {
	return m.gatewayURLFor(m.legacySeg(b, port))
}

// refreshNamedURLs rewrites the stored URLs that carry the box's readable
// address (box URL, ingress exposures) after a rename or relabel.
func (m *Manager) refreshNamedURLs(b, before *model.Box) {
	for i := range b.Exposures {
		e := &b.Exposures[i]
		if e.Kind == KindIngress && e.URL == m.ingressURL(before, e.Port) {
			e.URL = m.ingressURL(b, e.Port)
		}
	}
	if pp := PrimaryPort(b); pp > 0 && b.URL != "" {
		switch b.URL {
		case m.GatewayURL(before, pp):
			b.URL = m.GatewayURL(b, pp)
		case m.ingressURL(before, pp):
			b.URL = m.ingressURL(b, pp)
		}
	}
}

// namesCopy is a copy of b whose label map is its own (for refreshNamedURLs).
func namesCopy(b *model.Box) *model.Box {
	c := *b
	c.PortLabels = map[int]string{}
	for k, v := range b.PortLabels {
		c.PortLabels[k] = v
	}
	return &c
}
