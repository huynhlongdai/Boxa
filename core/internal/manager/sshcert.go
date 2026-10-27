package manager

// v1.11 short-lived SSH user certificates. Core keeps an SSH user CA (ed25519,
// stored sealed). POST /v1/ssh/certs {box, public_key, ttl_s ≤ 43200} signs
// the caller's public key for one box (principals = box id, name, slug) for
// at most 12 hours. Every certificate is recorded as a ticket of purpose
// ssh_cert (key id = record id), so revoking it (DELETE
// /v1/boxes/{id}/tickets/{tid} or all) closes the sessions it opened and
// refuses it from then on, even before it expires.
//
// A certificate authenticates on its own, without an API key: over the
// WebSocket (wss://<name>.<domain>/.boxaz/ssh with no ticket — `box
// ssh-proxy --anon %h`) and on the direct listener (`ssh -p 2222
// <name>@ssh.<domain>`).

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"boxaz/core/internal/model"
	"boxaz/core/internal/secret"
)

const sshUserCAKV = "ssh:userca"

// MaxSSHCertTTL bounds certificate lifetime.
const MaxSSHCertTTL = 12 * time.Hour

// sshUserCA loads (or creates once) the user CA signer.
func (m *Manager) sshUserCA() (ssh.Signer, error) {
	m.acc.ssh.mu.Lock()
	defer m.acc.ssh.mu.Unlock()
	if m.acc.ssh.ca != nil {
		return m.acc.ssh.ca, nil
	}
	ctx := context.Background()
	var sealed string
	m.Store.GetKV(ctx, sshUserCAKV, &sealed)
	if sealed != "" {
		if pemText, err := m.Crypt.Open(sealed); err == nil {
			if s, err := ssh.ParsePrivateKey([]byte(pemText)); err == nil {
				m.acc.ssh.ca = s
				return s, nil
			}
		}
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	blk, err := ssh.MarshalPrivateKey(priv, "user ca")
	if err != nil {
		return nil, err
	}
	if sealed, err = m.Crypt.Seal(string(pem.EncodeToMemory(blk))); err != nil {
		return nil, err
	}
	if err := m.Store.PutKV(ctx, sshUserCAKV, sealed); err != nil {
		return nil, err
	}
	s, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		return nil, err
	}
	m.acc.ssh.ca = s
	return s, nil
}

// SSHUserCA returns the CA public key line (TrustedUserCAKeys for a box's own sshd).
func (m *Manager) SSHUserCA() string {
	s, err := m.sshUserCA()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(s.PublicKey())))
}

// SSHCertInput is POST /v1/ssh/certs.
type SSHCertInput struct {
	Box        string `json:"box"`
	PublicKey  string `json:"public_key"`
	TTLSeconds int    `json:"ttl_s,omitempty"` // default 3600, max 43200
}

// SSHCert is the signed certificate and how to use it.
type SSHCert struct {
	ID           string    `json:"id"` // ticket record (revoke with DELETE /v1/boxes/{box}/tickets/{id})
	BoxID        string    `json:"box_id"`
	Certificate  string    `json:"certificate"` // save as <key>-cert.pub (OpenSSH CertificateFile)
	Serial       uint64    `json:"serial"`
	Principals   []string  `json:"principals"`
	ValidAfter   time.Time `json:"valid_after"`
	ValidBefore  time.Time `json:"valid_before"`
	Fingerprint  string    `json:"fingerprint"`
	Host         string    `json:"host"`
	User         string    `json:"user"`
	ProxyCommand string    `json:"proxy_command"`
	KnownHosts   string    `json:"known_hosts"`
	Command      string    `json:"command"`
	Direct       string    `json:"direct_command,omitempty"`
	Config       string    `json:"config"`
	UserCA       string    `json:"user_ca"`
}

// IssueSSHCert signs a user public key for one box.
func (m *Manager) IssueSSHCert(ctx context.Context, p *Principal, in SSHCertInput) (*SSHCert, error) {
	if err := p.Require(model.ScopeBoxExec); err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.Box) == "" {
		return nil, model.EH(model.ErrInvalid, "Pass the box id or name: {\"box\":\"my-box\",\"public_key\":\"ssh-ed25519 …\"}.", "box is required")
	}
	b, err := m.ResolveBox(ctx, p, in.Box)
	if err != nil {
		return nil, err
	}
	if !b.Active() {
		return nil, model.EH(model.ErrNotFound, "This box is gone; create a new one with box_up.", "box is %s", b.Status)
	}
	if b.TakenOver && !p.Admin {
		return nil, model.EH(model.ErrPolicyDenied, "A human has taken over this box; wait for release.", "box %s is under human takeover", b.Slug)
	}
	pk, _, _, _, err := ssh.ParseAuthorizedKey([]byte(strings.TrimSpace(in.PublicKey)))
	if err != nil {
		return nil, model.EH(model.ErrInvalid, "Paste one line of ~/.ssh/id_ed25519.pub (or id_rsa.pub / ecdsa).", "public_key is not an SSH public key")
	}
	if _, isCert := pk.(*ssh.Certificate); isCert {
		return nil, model.EH(model.ErrInvalid, "Pass the plain public key, not a certificate.", "public_key is a certificate")
	}
	ttl := time.Hour
	if in.TTLSeconds != 0 {
		ttl = time.Duration(in.TTLSeconds) * time.Second
	}
	if ttl < time.Minute || ttl > MaxSSHCertTTL {
		return nil, model.EH(model.ErrInvalid, "Use a ttl_s between 60 and 43200 (12 h).", "ttl_s out of range")
	}
	ca, err := m.sshUserCA()
	if err != nil {
		return nil, err
	}
	var sb [8]byte
	rand.Read(sb[:])
	serial := binary.BigEndian.Uint64(sb[:]) >> 1
	now := time.Now().UTC()
	principals := []string{b.ID}
	for _, x := range []string{b.Subdomain, b.Slug} {
		if x != "" && x != principals[len(principals)-1] && x != b.ID {
			principals = append(principals, x)
		}
	}
	rec := &model.AccessTicket{ID: secret.ID("tkt"), BoxID: b.ID, Purpose: model.PurposeSSHCert, Prefix: "cert " + ssh.FingerprintSHA256(pk)[:19] + "…",
		ExpiresAt: now.Add(ttl), CreatedAt: now, Actor: p.Actor(), Serial: serial, Fingerprint: ssh.FingerprintSHA256(pk), Principals: principals, Reusable: true}
	if p.Key != nil {
		rec.KeyID = p.Key.ID
	}
	cert := &ssh.Certificate{Key: pk, Serial: serial, CertType: ssh.UserCert, KeyId: rec.ID, ValidPrincipals: principals,
		ValidAfter: uint64(now.Add(-time.Minute).Unix()), ValidBefore: uint64(rec.ExpiresAt.Unix()),
		Permissions: ssh.Permissions{Extensions: map[string]string{"permit-pty": "", "permit-port-forwarding": ""}}}
	if err := cert.SignCert(rand.Reader, ca); err != nil {
		return nil, err
	}
	lock := m.boxLock(b.ID)
	lock.Lock()
	list := m.loadTickets(ctx, b.ID)
	list = append(m.pruneTickets(list, now), rec)
	err = m.Store.PutKV(ctx, ticketKey(b.ID), list)
	lock.Unlock()
	if err != nil {
		return nil, err
	}
	m.Emit(ctx, p.Actor(), b.ID, b.AgentID, "box.ticket", fmt.Sprintf("SSH certificate %s issued for %s (valid until %s)", rec.ID, rec.Fingerprint, rec.ExpiresAt.Format(time.RFC3339)), nil)
	name := b.Subdomain
	if name == "" {
		name = b.Slug
	}
	host := name + ".boxaz"
	if d := m.GatewayDomain(ctx); d != "" && b.Subdomain != "" {
		host = b.Subdomain + "." + d
	}
	known, _ := m.SSHKnownHosts()
	out := &SSHCert{ID: rec.ID, BoxID: b.ID, Certificate: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(cert))), Serial: serial, Principals: principals,
		ValidAfter: now.Add(-time.Minute), ValidBefore: rec.ExpiresAt, Fingerprint: rec.Fingerprint, Host: host, User: name,
		ProxyCommand: "box ssh-proxy --anon %h", KnownHosts: known, UserCA: m.SSHUserCA(),
		Command: "ssh -o CertificateFile=<key>-cert.pub -i <key> -o ProxyCommand='box ssh-proxy --anon %h' -o HostKeyAlias=" + SSHHostKeyAlias + " " + name + "@" + host}
	if d := m.SSHDirect(); d != "" {
		h, port, _ := strings.Cut(d, ":")
		out.Direct = "ssh -p " + port + " -o CertificateFile=<key>-cert.pub -i <key> " + name + "@" + h
	}
	out.Config = fmt.Sprintf("Host %s\n  HostName %s\n  User %s\n  IdentityFile <key>\n  CertificateFile <key>-cert.pub\n  IdentitiesOnly yes\n  ProxyCommand box ssh-proxy --anon %%h\n  HostKeyAlias %s\n  UserKnownHostsFile ~/.boxaz/known_hosts\n  ServerAliveInterval 25\n", host, host, name, SSHHostKeyAlias)
	return out, nil
}

// pruneTickets drops records an hour past expiry with no live session (≤ 200 kept).
func (m *Manager) pruneTickets(list []*model.AccessTicket, now time.Time) []*model.AccessTicket {
	kept := list[:0]
	for _, x := range list {
		dead := now.After(x.ExpiresAt.Add(time.Hour)) && !m.ticketLive(x.ID)
		if !dead && len(kept) < 200 {
			kept = append(kept, x)
		}
	}
	return kept
}

var errCert = errors.New("certificate not accepted")

// sshCertAuth validates a user certificate signed by core's CA, still
// recorded and not revoked. wantBox ("" = any) pins the box (WebSocket path);
// principal ("" = skip) is the SSH username (direct listener).
func (m *Manager) sshCertAuth(key ssh.PublicKey, wantBox, principal string) (*ssh.Permissions, error) {
	cert, ok := key.(*ssh.Certificate)
	if !ok || cert.CertType != ssh.UserCert || len(cert.ValidPrincipals) == 0 {
		return nil, errCert
	}
	ca, err := m.sshUserCA()
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(cert.SignatureKey.Marshal(), ca.PublicKey().Marshal()) {
		return nil, errCert
	}
	checker := &ssh.CertChecker{}
	p := principal
	if p == "" {
		p = cert.ValidPrincipals[0]
	}
	if err := checker.CheckCert(p, cert); err != nil {
		return nil, err
	}
	boxID := cert.ValidPrincipals[0]
	if wantBox != "" && wantBox != boxID {
		return nil, errCert
	}
	ctx := context.Background()
	var rec *model.AccessTicket
	for _, t := range m.loadTickets(ctx, boxID) {
		if t.ID == cert.KeyId && t.Purpose == model.PurposeSSHCert && t.Serial == cert.Serial {
			rec = t
		}
	}
	if rec == nil || rec.RevokedAt != nil || time.Now().After(rec.ExpiresAt) {
		return nil, errCert
	}
	b, err := m.Store.GetBox(ctx, boxID)
	if err != nil || !b.Active() {
		return nil, errCert
	}
	if _, err := m.principalForKey(ctx, rec.KeyID); err != nil {
		return nil, err
	}
	m.markCertUsed(ctx, boxID, rec.ID)
	return &ssh.Permissions{Extensions: map[string]string{"box": boxID, "key": rec.KeyID, "ticket": rec.ID}}, nil
}

func (m *Manager) markCertUsed(ctx context.Context, boxID, id string) {
	lock := m.boxLock(boxID)
	lock.Lock()
	defer lock.Unlock()
	list := m.loadTickets(ctx, boxID)
	now := time.Now().UTC()
	for _, t := range list {
		if t.ID == id {
			t.Uses++
			if t.RedeemedAt == nil {
				t.RedeemedAt = &now
			}
		}
	}
	m.Store.PutKV(ctx, ticketKey(boxID), list)
}

// sshRegisteredKeyAuth accepts a registered public key whose owner owns box.
func (m *Manager) sshRegisteredKeyAuth(key ssh.PublicKey, boxRef string) (*ssh.Permissions, error) {
	fp := ssh.FingerprintSHA256(key)
	ctx := context.Background()
	for _, k := range m.loadSSHKeys(ctx) {
		if k.Fingerprint != fp {
			continue
		}
		p, err := m.principalForKey(ctx, k.KeyID)
		if err != nil {
			continue
		}
		if b, err := m.ResolveBox(ctx, p, boxRef); err == nil && b.Active() {
			return &ssh.Permissions{Extensions: map[string]string{"box": b.ID, "key": k.KeyID}}, nil
		}
	}
	return nil, errors.New("no box for this user and key")
}

// BoxForSSH finds an active box by id, slug or name without a principal (the
// unauthenticated WebSocket path; SSH auth then decides).
func (m *Manager) BoxForSSH(ctx context.Context, ref string) (*model.Box, error) {
	if b, err := m.Store.GetBox(ctx, ref); err == nil && b.Active() {
		return b, nil
	}
	if b := m.boxByName(ctx, ref); b != nil {
		return b, nil
	}
	return nil, model.E(model.ErrNotFound, "no box at this address")
}

// ServeSSHAnon runs SSH on a WebSocket opened without a ticket or key: the
// client must authenticate with a core-signed certificate for this box or a
// registered public key of the box's owner.
func (m *Manager) ServeSSHAnon(ctx context.Context, nc net.Conn, b *model.Box) {
	signer, err := m.sshSigner()
	if err != nil {
		nc.Close()
		return
	}
	cfg := &ssh.ServerConfig{ServerVersion: "SSH-2.0-boxaz", MaxAuthTries: 6,
		PublicKeyCallback: func(c ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if _, ok := key.(*ssh.Certificate); ok {
				return m.sshCertAuth(key, b.ID, "")
			}
			return m.sshRegisteredKeyAuth(key, b.ID)
		}}
	cfg.AddHostKey(signer)
	m.serveSSHConn(ctx, nc, cfg, nil)
}
