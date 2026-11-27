package model

import "time"

// v1.10 access tickets: short-lived, single-use credentials for one box and
// one purpose (web terminal, TCP stream to one port, SSH session). Redeemed
// once (the connection is then bound to the ticket: revoking it closes the
// session); never the API key in a URL.

// Ticket purposes.
const (
	PurposeTerminal = "terminal"
	PurposeTCP      = "tcp"
	PurposeSSH      = "ssh"
	// v1.11
	PurposeSSHCert = "ssh_cert"  // record of a short-lived SSH user certificate (revocable like a ticket)
	PurposeCDP     = "cdp"       // Chrome DevTools Protocol endpoint of the box's browser
	PurposeVNC     = "vnc"       // desktop (RFB over WebSocket)
	PurposeShare   = "vnc_share" // view-only desktop share link (reusable until it expires or is revoked)
)

// AccessTicket is one issued ticket (Ticket only in the create response).
type AccessTicket struct {
	ID         string     `json:"id"`
	BoxID      string     `json:"box_id"`
	Purpose    string     `json:"purpose"`
	Port       int        `json:"port,omitempty"`
	SessionID  string     `json:"session_id,omitempty"` // terminal: reattach this session only
	Prefix     string     `json:"prefix"`
	Ticket     string     `json:"ticket,omitempty"`
	URL        string     `json:"url,omitempty"`      // where to use it (WebSocket URL with ?ticket=, or ssh command)
	CoreURL    string     `json:"core_url,omitempty"` // the same endpoint on core's own host
	Command    string     `json:"command,omitempty"`  // ssh: ready-to-run command
	ExpiresAt  time.Time  `json:"expires_at"`         // must be redeemed before
	CreatedAt  time.Time  `json:"created_at"`
	RedeemedAt *time.Time `json:"redeemed_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
	Live       int        `json:"live,omitempty"` // sessions currently bound to it
	Actor      string     `json:"actor,omitempty"`
	KeyID      string     `json:"key_id,omitempty"`
	Hash       string     `json:"hash,omitempty"` // stored only
	// v1.11
	ViewOnly    bool     `json:"view_only,omitempty"`   // vnc / vnc_share: input from the viewer is dropped by core
	Reusable    bool     `json:"reusable,omitempty"`    // cdp / vnc_share: may be used until expires_at (not single-use)
	Serial      uint64   `json:"serial,omitempty"`      // ssh_cert: certificate serial
	Fingerprint string   `json:"fingerprint,omitempty"` // ssh_cert: SHA256 of the certified public key
	Principals  []string `json:"principals,omitempty"`  // ssh_cert
	Uses        int      `json:"uses,omitempty"`        // reusable tickets: times redeemed
}

// SSHKey is a user public key accepted by the SSH gateway for the owner's boxes.
type SSHKey struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	PublicKey   string    `json:"public_key"`
	Fingerprint string    `json:"fingerprint"`
	AgentID     string    `json:"agent_id,omitempty"` // "" = admin key owner
	KeyID       string    `json:"key_id,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	WorkspaceID string    `json:"workspace_id,omitempty"` // v2.0
}

// TerminalSession is a durable PTY in a box (survives WebSocket drops).
type TerminalSession struct {
	ID         string     `json:"id"`
	BoxID      string     `json:"box_id"`
	Name       string     `json:"name,omitempty"`
	Cols       int        `json:"cols"`
	Rows       int        `json:"rows"`
	Viewers    int        `json:"viewers"`
	Controlled bool       `json:"controlled"` // a viewer holds control (input)
	CreatedAt  time.Time  `json:"created_at"`
	LastIO     time.Time  `json:"last_io"`
	DetachedAt *time.Time `json:"detached_at,omitempty"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"` // killed then unless reattached
	Exited     bool       `json:"exited,omitempty"`
	ExitCode   *int       `json:"exit_code,omitempty"`
	Buffered   int        `json:"buffered_bytes"`
	Actor      string     `json:"actor,omitempty"`
}
