package model

// v1.7: app spec (process supervisor), wake-on-request, scoped URL tokens.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path"
	"sort"
	"strings"
	"time"
)

// AppSpec declares the long-running app of a box. boxd (or, on exec-only T3
// sandboxes, a small supervisor script) clones the repo, runs install once per
// spec, starts the app, restarts it on crash with backoff, re-runs it after a
// cold resume / restore / clone and gates gateway traffic until it is ready.
type AppSpec struct {
	Repo    string `json:"repo,omitempty"`    // git URL cloned into Workdir when Workdir has no checkout yet
	Ref     string `json:"ref,omitempty"`     // branch or tag (default: the repo default branch)
	Install string `json:"install,omitempty"` // run once per spec (e.g. "npm ci")
	Start   string `json:"start"`             // the app command (foreground; e.g. "npm start")
	Port    int    `json:"port,omitempty"`    // port the app listens on (gates gateway traffic for this port)
	// Label (v1.9) names the app port in gateway URLs (<box>-<label>.<domain>);
	// the app port is also the box's primary port (<box>.<domain>).
	Label string `json:"label,omitempty"`
	// Ready is the readiness check: {"http":"/healthz"} or {"tcp":true}.
	// Default: tcp on Port when Port is set, otherwise "started" counts as ready.
	Ready   *AppReady         `json:"ready,omitempty"`
	Restart string            `json:"restart,omitempty"` // always (default) | on-failure | never
	Env     map[string]string `json:"env,omitempty"`
	// Workdir is a box path (default /work/<repo name> with Repo, else /work).
	Workdir     string `json:"workdir,omitempty"`
	BackoffMaxS int    `json:"backoff_max_s,omitempty"` // restart backoff ceiling (default 30)
}

// AppReady is a readiness check.
type AppReady struct {
	HTTP string `json:"http,omitempty"` // path answered with a status < 500
	TCP  bool   `json:"tcp,omitempty"`  // port accepts a connection
}

// Restart policies.
const (
	RestartAlways    = "always"
	RestartOnFailure = "on-failure"
	RestartNever     = "never"
)

// App states.
const (
	AppNone       = "none"
	AppCloning    = "cloning"
	AppInstalling = "installing"
	AppStarting   = "starting"
	AppReadyState = "ready"
	AppBackoff    = "backoff"
	AppExited     = "exited"
	AppStopped    = "stopped"
	AppFailed     = "failed"
	AppSleeping   = "sleeping"
)

// AppStatus is the live state of a box's app.
type AppStatus struct {
	State      string     `json:"state"`
	Ready      bool       `json:"ready"`
	PID        int        `json:"pid,omitempty"`
	Restarts   int        `json:"restarts"`
	LastExit   *int       `json:"last_exit,omitempty"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	ReadyAt    *time.Time `json:"ready_at,omitempty"`
	Message    string     `json:"message,omitempty"`
	Supervisor string     `json:"supervisor,omitempty"` // boxd | script (T3 exec-only sandboxes)
	SpecHash   string     `json:"spec_hash,omitempty"`
}

// AppView is GET /v1/boxes/{id}/app.
type AppView struct {
	BoxID  string    `json:"box_id"`
	Spec   *AppSpec  `json:"spec"`
	Status AppStatus `json:"status"`
}

// Normalize fills defaults in place.
func (s *AppSpec) Normalize() {
	s.Start = strings.TrimSpace(s.Start)
	s.Repo = strings.TrimSpace(s.Repo)
	s.Label = strings.ToLower(strings.TrimSpace(s.Label))
	if s.Restart == "" {
		s.Restart = RestartAlways
	}
	if s.Workdir == "" {
		s.Workdir = "/work"
		if s.Repo != "" {
			s.Workdir = "/work/" + RepoDir(s.Repo)
		}
	}
	if !strings.HasPrefix(s.Workdir, "/") {
		s.Workdir = "/work/" + s.Workdir
	}
	s.Workdir = path.Clean(s.Workdir)
	if s.Ready == nil && s.Port > 0 {
		s.Ready = &AppReady{TCP: true}
	}
	if s.Ready != nil && s.Ready.HTTP != "" && !strings.HasPrefix(s.Ready.HTTP, "/") {
		s.Ready.HTTP = "/" + s.Ready.HTTP
	}
	if s.BackoffMaxS <= 0 {
		s.BackoffMaxS = 30
	}
}

// Validate reports the first problem of a (normalized) spec, "" when fine.
func (s *AppSpec) Validate() string {
	switch {
	case s.Start == "":
		return "start is required"
	case s.Port < 0 || s.Port > 65535:
		return "port must be 1-65535"
	case s.Restart != RestartAlways && s.Restart != RestartOnFailure && s.Restart != RestartNever:
		return "restart must be always, on-failure or never"
	case s.Ready != nil && (s.Ready.HTTP != "" || s.Ready.TCP) && s.Port == 0:
		return "a readiness check needs port"
	case s.Ready != nil && s.Ready.HTTP != "" && s.Ready.TCP:
		return "ready takes http or tcp, not both"
	case s.Repo != "" && !(strings.HasPrefix(s.Repo, "https://") || strings.HasPrefix(s.Repo, "http://") || strings.HasPrefix(s.Repo, "git@") || strings.HasPrefix(s.Repo, "ssh://")):
		return "repo must be a git URL (https://… or git@…)"
	case strings.Contains(s.Workdir, ".."):
		return "workdir must not contain .."
	case s.Label != "" && s.Port == 0:
		return "label needs port"
	case s.Label != "" && PortLabelProblem(s.Label) != "":
		return PortLabelProblem(s.Label)
	}
	for k := range s.Env {
		if k == "" || strings.ContainsAny(k, "= \t\n'\"$") {
			return "bad env name " + k
		}
	}
	return ""
}

// Hash identifies the prepare step (repo, ref, install, workdir): install
// re-runs only when it changes.
func (s *AppSpec) Hash() string {
	h := sha256.New()
	for _, x := range []string{s.Repo, s.Ref, s.Install, s.Workdir} {
		h.Write([]byte(x))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:12]
}

// FullHash identifies the whole spec.
func (s *AppSpec) FullHash() string {
	keys := make([]string, 0, len(s.Env))
	for k := range s.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	b, _ := json.Marshal(struct {
		S *AppSpec
		K []string
	}{s, keys})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:12]
}

// RepoDir is the checkout directory name of a git URL.
func RepoDir(repo string) string {
	r := strings.TrimRight(repo, "/")
	if i := strings.LastIndexAny(r, "/:"); i >= 0 {
		r = r[i+1:]
	}
	r = strings.TrimSuffix(r, ".git")
	if r == "" || r == "." || r == ".." {
		return "app"
	}
	return r
}

// ---- URL tokens ----

// URLToken is a Boxmes-issued token for one port of one box (value shown once).
type URLToken struct {
	ID        string     `json:"id"`
	BoxID     string     `json:"box_id"`
	Port      int        `json:"port"`
	Label     string     `json:"label,omitempty"`
	Prefix    string     `json:"prefix"`
	Token     string     `json:"token,omitempty"` // only in the create response
	URL       string     `json:"url,omitempty"`   // only in the create response: gateway URL with ?token=
	ExpiresAt time.Time  `json:"expires_at"`
	CreatedAt time.Time  `json:"created_at"`
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
	Expired   bool       `json:"expired,omitempty"`
	Hash      string     `json:"hash,omitempty"` // stored only, never returned
}

// Port access modes.
const (
	AccessPublic = "public" // anyone with the gateway URL (the URL key is unguessable)
	AccessToken  = "token"  // a Boxmes URL token is required
)

// WakeOn reports the per-box wake_on_request setting (default on).
func (b *Box) WakeOn() bool { return b.WakeOnRequest == nil || *b.WakeOnRequest }

// AccessFor is the access mode of a port (default public).
func (b *Box) AccessFor(port int) string {
	if b.PortAccess != nil {
		if a, ok := b.PortAccess[port]; ok && a != "" {
			return a
		}
	}
	return AccessPublic
}
