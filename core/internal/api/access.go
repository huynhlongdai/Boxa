package api

// v1.10 box access endpoints: tickets, the durable web terminal, the generic
// TCP stream and SSH over WebSocket. The three WebSockets authenticate with a
// single-use ticket (?ticket=bxa_… or Authorization: Bearer bxa_…) or an API
// key in the Authorization header — never an API key in the URL, never the
// dashboard cookie — and check Origin against the allowlist. They are served
// on core (/v1/boxes/{id}/{terminal,tcp,ssh}) and on every box host
// (wss://<name>.<domain>/.boxaz/{terminal,tcp,ssh}).

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"

	"boxaz/core/internal/manager"
	"boxaz/core/internal/model"
	"boxaz/core/internal/wsconn"
)

// accessUpgrader: Origin is checked before the upgrade (OriginAllowed).
var accessUpgrader = websocket.Upgrader{
	ReadBufferSize: 32 << 10, WriteBufferSize: 32 << 10,
	CheckOrigin: func(r *http.Request) bool { return true },
}

// routesAccessWS registers the access WebSockets (own auth, outside authMW).
func (s *Server) routesAccessWS(r chi.Router) {
	r.Get("/boxes/{id}/terminal", func(w http.ResponseWriter, r *http.Request) { s.accessTerminal(w, r, nil) })
	r.Get("/boxes/{id}/tcp", func(w http.ResponseWriter, r *http.Request) { s.accessTCP(w, r, nil) })
	r.Get("/boxes/{id}/ssh", func(w http.ResponseWriter, r *http.Request) { s.accessSSH(w, r, nil) })
	s.routesAccessP1Open(r)
}

// routesAccess registers the authenticated access API.
func (s *Server) routesAccess(r chi.Router) {
	r.Get("/boxes/{id}/access", s.boxAccess)
	r.Get("/boxes/{id}/tickets", s.listTickets)
	r.Post("/boxes/{id}/tickets", s.createTicket)
	r.Delete("/boxes/{id}/tickets/{tid}", s.revokeTicket)
	r.Post("/boxes/{id}/forward-ports", s.allowForwardPort)
	r.Get("/boxes/{id}/terminals", s.listTerminals)
	r.Delete("/boxes/{id}/terminals/{sid}", s.killTerminal)
	r.Get("/ssh", s.sshInfo)
	r.Get("/ssh/keys", s.listSSHKeys)
	r.Post("/ssh/keys", s.addSSHKey)
	r.Delete("/ssh/keys/{kid}", s.deleteSSHKey)
	r.Post("/ssh/certs", s.issueSSHCert)
	s.routesAccessP1(r)
}

func (s *Server) issueSSHCert(w http.ResponseWriter, r *http.Request) {
	var in manager.SSHCertInput
	if err := decode(r, &in); err != nil {
		writeErr(w, r, err)
		return
	}
	c, err := s.M.IssueSSHCert(r.Context(), pr(r), in)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 201, c)
}

// accessHost serves /.boxaz/{terminal,tcp,ssh} on a box host.
func (s *Server) accessHost(w http.ResponseWriter, r *http.Request, seg string) {
	b, err := s.M.BoxForAccessHost(r.Context(), seg)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if s.accessHostP1(w, r, b) { // v1.11 cdp / vnc / desktop / novnc
		return
	}
	switch strings.TrimSuffix(r.URL.Path, "/") {
	case "/.boxaz/terminal":
		s.accessTerminal(w, r, b)
	case "/.boxaz/tcp":
		s.accessTCP(w, r, b)
	case "/.boxaz/ssh":
		s.accessSSH(w, r, b)
	default:
		writeErr(w, r, model.EH(model.ErrNotFound, "Use /.boxaz/terminal, /.boxaz/tcp?port=N, /.boxaz/ssh, /.boxaz/cdp, /.boxaz/vnc or /.boxaz/desktop.", "no access endpoint %s", r.URL.Path))
	}
}

type accessCaller struct {
	box      *model.Box
	p        *manager.Principal
	ticket   *model.AccessTicket
	ticketID string
}

// accessAuth authenticates an access WebSocket before the upgrade. hostBox is
// the box of the host (/.boxaz/ paths), nil on core paths ({id}).
func (s *Server) accessAuth(w http.ResponseWriter, r *http.Request, hostBox *model.Box, purpose string, port int) (*accessCaller, bool) {
	ctx := r.Context()
	if !s.M.OriginAllowed(r, s.CORSOrigins) {
		writeErr(w, r, model.EH(model.ErrPolicyDenied, "Open the terminal from the Boxaz dashboard, or add the origin to BOXAZ_ACCESS_ORIGINS.", "origin %s may not open box access sockets", r.Header.Get("Origin")))
		return nil, false
	}
	tok := strings.TrimSpace(r.URL.Query().Get("ticket"))
	auth := r.Header.Get("Authorization")
	if tok == "" && strings.HasPrefix(auth, "Bearer "+manager.AccessTicketPrefix) {
		tok = strings.TrimSpace(auth[7:])
	}
	ref := chi.URLParam(r, "id")
	if tok != "" {
		boxID := ""
		if hostBox != nil {
			boxID = hostBox.ID
		}
		t, p, b, err := s.M.RedeemTicket(ctx, tok, purpose, boxID, port)
		if err != nil {
			writeErr(w, r, err)
			return nil, false
		}
		if ref != "" && ref != b.ID && ref != b.Slug && ref != b.Subdomain {
			writeErr(w, r, model.EH(model.ErrUnauthorized, "Get a fresh ticket: POST /v1/boxes/{id}/tickets.", "access ticket is invalid, expired, revoked or already used"))
			return nil, false
		}
		return &accessCaller{box: b, p: p, ticket: t, ticketID: t.ID}, true
	}
	if strings.HasPrefix(auth, "Bearer ") {
		p, err := s.M.Authenticate(ctx, strings.TrimSpace(auth[7:]))
		if err != nil {
			writeErr(w, r, err)
			return nil, false
		}
		if err := p.Require(model.ScopeBoxExec); err != nil {
			writeErr(w, r, err)
			return nil, false
		}
		if hostBox != nil {
			ref = hostBox.ID
		}
		b, err := s.M.ResolveBox(ctx, p, ref)
		if err != nil {
			writeErr(w, r, err)
			return nil, false
		}
		if b.TakenOver && !p.Admin {
			writeErr(w, r, model.EH(model.ErrPolicyDenied, "A human has taken over this box; wait for release.", "box %s is under human takeover", b.Slug))
			return nil, false
		}
		return &accessCaller{box: b, p: p}, true
	}
	w.Header().Set("WWW-Authenticate", `Bearer realm="boxaz"`)
	msg := model.EH(model.ErrUnauthorized, "Get a single-use ticket (POST /v1/boxes/{id}/tickets) and pass ?ticket=, or send the API key in the Authorization header.", "this socket needs an access ticket or an Authorization header")
	if r.URL.Query().Get("token") != "" {
		msg = model.EH(model.ErrUnauthorized, "Get a single-use ticket (POST /v1/boxes/{id}/tickets) and pass ?ticket=, or send the API key in the Authorization header.", "API keys are not accepted in URLs")
	}
	writeErr(w, r, msg)
	return nil, false
}

// accessTCP: binary WebSocket frames = raw TCP bytes to one port in the box.
func (s *Server) accessTCP(w http.ResponseWriter, r *http.Request, hostBox *model.Box) {
	port, err := strconv.Atoi(r.URL.Query().Get("port"))
	if err != nil || port < 1 || port > 65535 {
		writeErr(w, r, model.EH(model.ErrInvalid, "Pass ?port=<in-box port>, e.g. ?port=5432.", "bad port"))
		return
	}
	c, ok := s.accessAuth(w, r, hostBox, model.PurposeTCP, port)
	if !ok {
		return
	}
	if !manager.ForwardAllowed(c.box, port) {
		writeErr(w, r, model.EH(model.ErrPolicyDenied, "Expose the port, or pass allow=true (box forward does) to add it to the box's forward allowlist.", "port %d is not in the forward allowlist of box %s", port, c.box.ID))
		return
	}
	b, peer, err := s.M.AccessLink(r.Context(), c.box)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	cctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	sc, err := peer.OpenStream(cctx, b.ID, port)
	cancel()
	if err != nil {
		e := model.EH(model.ErrProviderFailed, "Start a server on that port inside the box (listening on 127.0.0.1 or 0.0.0.0).", "cannot connect to port %d in box %s: %v", port, b.ID, err)
		e.Status = http.StatusBadGateway
		writeErr(w, r, e)
		return
	}
	ws, err := accessUpgrader.Upgrade(w, r, nil)
	if err != nil {
		sc.Close()
		return
	}
	conn := wsconn.New(ws)
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	unbind := s.M.BindAccess(b.ID, c.ticketID, "tcp", func(reason string) {
		conn.CloseWithReason(websocket.CloseGoingAway, reason)
		sc.Close()
	})
	defer unbind()
	go s.M.KeepActive(ctx, b.ID)
	go func() {
		io.Copy(sc, conn)
		sc.CloseWrite()
	}()
	io.Copy(conn, sc)
	conn.Close()
	sc.Close()
}

// accessSSH: SSH protocol over binary WebSocket frames (box ssh-proxy).
func (s *Server) accessSSH(w http.ResponseWriter, r *http.Request, hostBox *model.Box) {
	if r.URL.Query().Get("ticket") == "" && r.Header.Get("Authorization") == "" && r.URL.Query().Get("token") == "" {
		// v1.11: no transport credential — the SSH layer authenticates
		// (core-signed certificate or a registered key of the box owner).
		if !s.M.OriginAllowed(r, s.CORSOrigins) {
			writeErr(w, r, model.EH(model.ErrPolicyDenied, "Use an SSH client (box ssh-proxy --anon %h).", "origin %s may not open box access sockets", r.Header.Get("Origin")))
			return
		}
		b := hostBox
		if b == nil {
			var err error
			if b, err = s.M.BoxForSSH(r.Context(), chi.URLParam(r, "id")); err != nil {
				writeErr(w, r, err)
				return
			}
		}
		ws, err := accessUpgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		s.M.ServeSSHAnon(context.Background(), wsconn.New(ws), b)
		return
	}
	c, ok := s.accessAuth(w, r, hostBox, model.PurposeSSH, 0)
	if !ok {
		return
	}
	ws, err := accessUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	conn := wsconn.New(ws)
	s.M.ServeSSH(context.Background(), conn, &manager.SSHTarget{Box: c.box, P: c.p, TicketID: c.ticketID})
}

// accessTerminal: the durable web terminal.
//
// Query: session=<id>|new (default new), cols, rows, mode=control|view
// (default control), proto=2. Protocol 2: binary frames = PTY bytes both
// ways; JSON text frames = control: client {type: resize|control|ping},
// server {type: session|control|viewers|exit|ping}. Without proto=2 (v1):
// text frames carry output (valid UTF-8), text keystrokes and the
// {"type":"resize"} JSON frame, as before.
func (s *Server) accessTerminal(w http.ResponseWriter, r *http.Request, hostBox *model.Box) {
	c, ok := s.accessAuth(w, r, hostBox, model.PurposeTerminal, 0)
	if !ok {
		return
	}
	q := r.URL.Query()
	sid := q.Get("session")
	if c.ticket != nil && c.ticket.SessionID != "" {
		if sid != "" && sid != c.ticket.SessionID {
			writeErr(w, r, model.EH(model.ErrUnauthorized, "Get a fresh ticket: POST /v1/boxes/{id}/tickets.", "access ticket is invalid, expired, revoked or already used"))
			return
		}
		sid = c.ticket.SessionID
	}
	cols, _ := strconv.Atoi(q.Get("cols"))
	rows, _ := strconv.Atoi(q.Get("rows"))
	control := q.Get("mode") != "view"
	v2 := q.Get("proto") == "2"
	h, err := s.M.TerminalAttach(r.Context(), c.p, c.box, sid, q.Get("name"), cols, rows, control)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	ws, err := accessUpgrader.Upgrade(w, r, nil)
	if err != nil {
		h.Detach()
		return
	}
	defer ws.Close()
	defer h.Detach()
	var wmu sync.Mutex
	send := func(mt int, data []byte) error {
		wmu.Lock()
		defer wmu.Unlock()
		ws.SetWriteDeadline(time.Now().Add(15 * time.Second))
		return ws.WriteMessage(mt, data)
	}
	sendJSON := func(v any) {
		if v2 {
			b, _ := json.Marshal(v)
			send(websocket.TextMessage, b)
		}
	}
	split := &utf8Splitter{}
	out := func(b []byte) error {
		if v2 {
			return send(websocket.BinaryMessage, b)
		}
		if txt := split.feed(b); txt != "" {
			return send(websocket.TextMessage, []byte(txt))
		}
		return nil
	}
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	kicked := make(chan string, 1)
	unbind := s.M.BindAccess(c.box.ID, c.ticketID, "terminal", func(reason string) {
		select {
		case kicked <- reason:
		default:
		}
	})
	defer unbind()
	go s.M.KeepActive(ctx, c.box.ID)
	info := h.Snapshot()
	sendJSON(map[string]any{"type": "session", "id": h.ID(), "created": h.Created, "control": control, "replay": len(h.Replay),
		"cols": info.Cols, "rows": info.Rows, "viewers": info.Viewers, "detach_ttl_s": int(s.M.TerminalDetachTTL().Seconds())})
	if len(h.Replay) > 0 {
		for i := 0; i < len(h.Replay); i += 32 << 10 {
			j := min(i+32<<10, len(h.Replay))
			if out(h.Replay[i:j]) != nil {
				return
			}
		}
	}
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		ws.SetReadLimit(1 << 20)
		for {
			mt, msg, err := ws.ReadMessage()
			if err != nil {
				return
			}
			if mt == websocket.TextMessage && len(msg) > 0 && msg[0] == '{' {
				var ctl struct {
					Type    string `json:"type"`
					Cols    int    `json:"cols"`
					Rows    int    `json:"rows"`
					Control *bool  `json:"control"`
				}
				if json.Unmarshal(msg, &ctl) == nil && ctl.Type != "" {
					switch ctl.Type {
					case "resize":
						h.Resize(ctl.Cols, ctl.Rows)
					case "control":
						h.Control(ctl.Control == nil || *ctl.Control)
					case "ping":
						sendJSON(map[string]any{"type": "pong"})
					}
					continue
				}
			}
			if v2 && mt == websocket.TextMessage {
				continue // v2: input is binary only
			}
			h.Input(msg)
		}
	}()
	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()
	for {
		select {
		case b := <-h.Viewer.Out:
			if out(b) != nil {
				return
			}
		case m := <-h.Viewer.Ctl:
			sendJSON(m)
			if m["type"] == "exit" && !v2 {
				send(websocket.TextMessage, []byte("\r\n[boxaz] session ended\r\n"))
			}
		case <-h.Viewer.Done:
		flush:
			for { // flush what is queued (the exit message comes last)
				select {
				case b := <-h.Viewer.Out:
					out(b)
					continue
				case m := <-h.Viewer.Ctl:
					sendJSON(m)
					if m["type"] == "exit" && !v2 {
						send(websocket.TextMessage, []byte("\r\n[boxaz] session ended\r\n"))
					}
					continue
				default:
					break flush
				}
			}
			reason := h.Viewer.Reason
			if reason != "" && !v2 {
				send(websocket.TextMessage, []byte("\r\n"+reason+"\r\n"))
			}
			send(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, trunc(reason, 100)))
			return
		case reason := <-kicked:
			sendJSON(map[string]any{"type": "exit", "reason": reason})
			if !v2 {
				send(websocket.TextMessage, []byte("\r\n"+reason+"\r\n"))
			}
			send(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseGoingAway, trunc(reason, 100)))
			return
		case <-ping.C:
			sendJSON(map[string]any{"type": "ping"})
			wmu.Lock()
			ws.WriteControl(websocket.PingMessage, nil, time.Now().Add(10*time.Second))
			wmu.Unlock()
		case <-readDone:
			return // detached: the session keeps running for the detach TTL
		}
	}
}

func trunc(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// ---- REST ----

func (s *Server) boxAccess(w http.ResponseWriter, r *http.Request) {
	out, err := s.M.Access(r.Context(), pr(r), chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, out)
}

func (s *Server) createTicket(w http.ResponseWriter, r *http.Request) {
	var in manager.CreateTicketInput
	if err := decode(r, &in); err != nil {
		writeErr(w, r, err)
		return
	}
	t, err := s.M.CreateTicket(r.Context(), pr(r), chi.URLParam(r, "id"), in)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 201, t)
}

func (s *Server) listTickets(w http.ResponseWriter, r *http.Request) {
	items, err := s.M.ListTickets(r.Context(), pr(r), chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": items})
}

func (s *Server) revokeTicket(w http.ResponseWriter, r *http.Request) {
	n, err := s.M.RevokeTicket(r.Context(), pr(r), chi.URLParam(r, "id"), chi.URLParam(r, "tid"))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"revoked": n})
}

func (s *Server) allowForwardPort(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Port int `json:"port"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, r, err)
		return
	}
	b, err := s.M.AllowForwardPort(r.Context(), pr(r), chi.URLParam(r, "id"), in.Port)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"box_id": b.ID, "allowed_ports": manager.ForwardPortsOf(b)})
}

func (s *Server) listTerminals(w http.ResponseWriter, r *http.Request) {
	items, err := s.M.ListTerminals(r.Context(), pr(r), chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": items, "detach_ttl_s": int(s.M.TerminalDetachTTL().Seconds())})
}

func (s *Server) killTerminal(w http.ResponseWriter, r *http.Request) {
	n, err := s.M.KillTerminal(r.Context(), pr(r), chi.URLParam(r, "id"), chi.URLParam(r, "sid"))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"killed": n})
}

func (s *Server) sshInfo(w http.ResponseWriter, r *http.Request) {
	known, fp := s.M.SSHKnownHosts()
	writeJSON(w, 200, map[string]any{"known_hosts": known, "host_key_fingerprint": fp, "host_key_alias": manager.SSHHostKeyAlias,
		"direct": s.M.SSHDirect(), "proxy_command": "box ssh-proxy %h", "domain": s.M.GatewayDomain(r.Context()),
		"user_ca": s.M.SSHUserCA(), "cert_max_ttl_s": int(manager.MaxSSHCertTTL.Seconds()), "sftp": "always (box sftp-server when present, else served by core)"})
}

func (s *Server) listSSHKeys(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"items": s.M.ListSSHKeys(r.Context(), pr(r))})
}

func (s *Server) addSSHKey(w http.ResponseWriter, r *http.Request) {
	var in manager.AddSSHKeyInput
	if err := decode(r, &in); err != nil {
		writeErr(w, r, err)
		return
	}
	k, err := s.M.AddSSHKey(r.Context(), pr(r), in)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 201, k)
}

func (s *Server) deleteSSHKey(w http.ResponseWriter, r *http.Request) {
	if err := s.M.DeleteSSHKey(r.Context(), pr(r), chi.URLParam(r, "kid")); err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"deleted": true})
}
