package api

// v2 tenancy HTTP: /v1/auth/*, /v1/waitlist, /v1/workspaces and the platform
// admin users / invites / waitlist tools. Sessions are bxs_ bearer tokens; the
// dashboard keeps them in its httpOnly cookie exactly like an API key.

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"boxmes/core/internal/manager"
	"boxmes/core/internal/model"
)

// routesTenancyPublic are reachable without credentials.
func (s *Server) routesTenancyPublic(r chi.Router) {
	r.Get("/auth/config", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, s.M.AuthConfig(r.Context())) })
	r.Post("/auth/signup", s.signup)
	r.Post("/auth/login", s.login)
	r.Post("/auth/verify-email", s.verifyEmail)
	r.Post("/auth/password/forgot", s.forgotPassword)
	r.Post("/auth/password/reset", s.resetPassword)
	r.Post("/waitlist", s.joinWaitlist)
}

// routesTenancy need a key or session (mounted inside the authMW group).
func (s *Server) routesTenancy(r chi.Router) {
	r.Post("/auth/logout", s.logout)
	r.Get("/auth/me", s.me)
	r.Patch("/auth/me", s.patchMe)
	r.Post("/auth/password", s.changePassword)
	r.Post("/auth/verify-email/resend", s.resendVerify)
	r.Post("/auth/workspace", s.switchWorkspace)
	r.Get("/workspaces", s.listMyWorkspaces)
}

func (s *Server) routesTenancyAdmin(r chi.Router) {
	r.Get("/users", s.adminUsers)
	r.Patch("/users/{id}", s.adminPatchUser)
	r.Delete("/users/{id}", s.adminDeleteUser)
	r.Get("/invites", s.adminInvites)
	r.Post("/invites", s.adminCreateInvite)
	r.Delete("/invites/{id}", s.adminDeleteInvite)
	r.Get("/waitlist", s.adminWaitlist)
	r.Post("/waitlist/invite", s.adminWaitlistInvite)
	r.Delete("/waitlist/{email}", s.adminDeleteWaitlist)
}

// sessionGuard keeps signed-in users who are not platform admins out of the
// v1 operator API: until boxes/agents/keys are workspace-scoped they may only
// use the tenancy endpoints.
func (s *Server) sessionGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := pr(r)
		if p != nil && p.UserID != "" && !p.Admin && !tenancyPath(r.URL.Path) {
			writeErr(w, r, model.EH(model.ErrUnauthorized, "Use /v1/auth and /v1/workspaces, or an API key.", "this endpoint is not available to workspace members yet"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func tenancyPath(p string) bool {
	return strings.HasPrefix(p, "/v1/auth/") || p == "/v1/workspaces"
}

func clientInfo(r *http.Request) manager.ClientInfo {
	ip := clientIP(r)
	if i := strings.Index(ip, ","); i >= 0 {
		ip = strings.TrimSpace(ip[:i])
	}
	return manager.ClientInfo{UserAgent: r.UserAgent(), IP: ip}
}

func (s *Server) signup(w http.ResponseWriter, r *http.Request) {
	var in manager.SignupInput
	if err := decode(r, &in); err != nil {
		writeErr(w, r, err)
		return
	}
	res, err := s.M.Signup(r.Context(), in, clientInfo(r))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 201, res)
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var in manager.LoginInput
	if err := decode(r, &in); err != nil {
		writeErr(w, r, err)
		return
	}
	res, err := s.M.Login(r.Context(), in, clientInfo(r))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, res)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	all := r.URL.Query().Get("all") == "1" || r.URL.Query().Get("all") == "true"
	if err := s.M.Logout(r.Context(), pr(r), all); err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	v, err := s.M.Me(r.Context(), pr(r))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *Server) patchMe(w http.ResponseWriter, r *http.Request) {
	var in manager.ProfileInput
	if err := decode(r, &in); err != nil {
		writeErr(w, r, err)
		return
	}
	u, err := s.M.UpdateProfile(r.Context(), pr(r), in)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, u)
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
	var in manager.ChangePasswordInput
	if err := decode(r, &in); err != nil {
		writeErr(w, r, err)
		return
	}
	if err := s.M.ChangePassword(r.Context(), pr(r), in); err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) resendVerify(w http.ResponseWriter, r *http.Request) {
	if err := s.M.ResendVerify(r.Context(), pr(r)); err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 202, map[string]any{"ok": true})
}

type verifyTokenReq struct {
	Token string `json:"token"`
}

func (s *Server) verifyEmail(w http.ResponseWriter, r *http.Request) {
	var in verifyTokenReq
	if err := decode(r, &in); err != nil {
		writeErr(w, r, err)
		return
	}
	u, err := s.M.VerifyEmail(r.Context(), in.Token)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "email": u.Email})
}

type emailReq struct {
	Email string `json:"email"`
}

func (s *Server) forgotPassword(w http.ResponseWriter, r *http.Request) {
	var in emailReq
	if err := decode(r, &in); err != nil {
		writeErr(w, r, err)
		return
	}
	s.M.ForgotPassword(r.Context(), in.Email)
	writeJSON(w, 202, map[string]any{"ok": true}) // same answer whether or not the account exists
}

func (s *Server) resetPassword(w http.ResponseWriter, r *http.Request) {
	var in manager.ResetPasswordInput
	if err := decode(r, &in); err != nil {
		writeErr(w, r, err)
		return
	}
	if err := s.M.ResetPassword(r.Context(), in); err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

type workspaceReq struct {
	WorkspaceID string `json:"workspace_id"`
}

func (s *Server) switchWorkspace(w http.ResponseWriter, r *http.Request) {
	var in workspaceReq
	if err := decode(r, &in); err != nil {
		writeErr(w, r, err)
		return
	}
	v, err := s.M.SwitchWorkspace(r.Context(), pr(r), in.WorkspaceID)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *Server) listMyWorkspaces(w http.ResponseWriter, r *http.Request) {
	v, err := s.M.Me(r.Context(), pr(r))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	list(w, v.Workspaces)
}

func (s *Server) joinWaitlist(w http.ResponseWriter, r *http.Request) {
	var in manager.WaitlistInput
	if err := decode(r, &in); err != nil {
		writeErr(w, r, err)
		return
	}
	res, err := s.M.JoinWaitlist(r.Context(), in)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 201, res)
}

// ---- platform admin ----

func (s *Server) adminUsers(w http.ResponseWriter, r *http.Request) {
	us, err := s.M.ListUsersAdmin(r.Context(), pr(r))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	list(w, us)
}

func (s *Server) adminPatchUser(w http.ResponseWriter, r *http.Request) {
	var in manager.AdminUserInput
	if err := decode(r, &in); err != nil {
		writeErr(w, r, err)
		return
	}
	u, err := s.M.PatchUserAdmin(r.Context(), pr(r), chi.URLParam(r, "id"), in)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, u)
}

func (s *Server) adminDeleteUser(w http.ResponseWriter, r *http.Request) {
	if err := s.M.DeleteUserAdmin(r.Context(), pr(r), chi.URLParam(r, "id")); err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) adminInvites(w http.ResponseWriter, r *http.Request) {
	v, err := s.M.ListInvites(r.Context(), pr(r))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	list(w, v)
}

func (s *Server) adminCreateInvite(w http.ResponseWriter, r *http.Request) {
	var in manager.InviteInput
	if err := decode(r, &in); err != nil {
		writeErr(w, r, err)
		return
	}
	c, err := s.M.CreateInvite(r.Context(), pr(r), in)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 201, c)
}

func (s *Server) adminDeleteInvite(w http.ResponseWriter, r *http.Request) {
	if err := s.M.DeleteInvite(r.Context(), pr(r), chi.URLParam(r, "id")); err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) adminWaitlist(w http.ResponseWriter, r *http.Request) {
	v, err := s.M.ListWaitlistAdmin(r.Context(), pr(r))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	list(w, v)
}

func (s *Server) adminWaitlistInvite(w http.ResponseWriter, r *http.Request) {
	var in manager.WaitlistInviteInput
	if err := decode(r, &in); err != nil {
		writeErr(w, r, err)
		return
	}
	v, err := s.M.InviteFromWaitlist(r.Context(), pr(r), in)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	list(w, v)
}

func (s *Server) adminDeleteWaitlist(w http.ResponseWriter, r *http.Request) {
	if err := s.M.DeleteWaitlistAdmin(r.Context(), pr(r), chi.URLParam(r, "email")); err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}
