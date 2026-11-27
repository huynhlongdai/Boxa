package api

// v2.0 tenancy HTTP: workspaces, members, roles, workspace invites, usage,
// box locations and the platform-admin workspace list.

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"boxmes/core/internal/manager"
	"boxmes/core/internal/store"
)

func (s *Server) createWorkspace(w http.ResponseWriter, r *http.Request) {
	var in manager.CreateWorkspaceInput
	if err := decode(r, &in); err != nil {
		writeErr(w, r, err)
		return
	}
	v, err := s.M.CreateWorkspace(r.Context(), pr(r), in)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 201, v)
}

func (s *Server) getCurrentWorkspace(w http.ResponseWriter, r *http.Request) {
	v, err := s.M.GetCurrentWorkspace(r.Context(), pr(r))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *Server) patchCurrentWorkspace(w http.ResponseWriter, r *http.Request) {
	var in manager.PatchWorkspaceInput
	if err := decode(r, &in); err != nil {
		writeErr(w, r, err)
		return
	}
	v, err := s.M.PatchCurrentWorkspace(r.Context(), pr(r), in)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *Server) workspaceUsage(w http.ResponseWriter, r *http.Request) {
	v, err := s.M.WorkspaceUsage(r.Context(), pr(r))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *Server) workspaceAudit(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	es, err := s.M.WorkspaceAudit(r.Context(), pr(r), store.EventFilter{Actor: q.Get("actor"), Type: q.Get("type"), BoxID: q.Get("box"), AgentID: q.Get("agent"), Limit: qint(r, "limit")})
	if err != nil {
		writeErr(w, r, err)
		return
	}
	list(w, es)
}

func (s *Server) listMembers(w http.ResponseWriter, r *http.Request) {
	v, err := s.M.ListMembers(r.Context(), pr(r))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	list(w, v)
}

func (s *Server) patchMember(w http.ResponseWriter, r *http.Request) {
	var in manager.PatchMemberInput
	if err := decode(r, &in); err != nil {
		writeErr(w, r, err)
		return
	}
	v, err := s.M.PatchMember(r.Context(), pr(r), chi.URLParam(r, "user_id"), in)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *Server) removeMember(w http.ResponseWriter, r *http.Request) {
	if err := s.M.RemoveMember(r.Context(), pr(r), chi.URLParam(r, "user_id")); err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) listWorkspaceInvites(w http.ResponseWriter, r *http.Request) {
	v, err := s.M.ListWorkspaceInvites(r.Context(), pr(r))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	list(w, v)
}

func (s *Server) createWorkspaceInvite(w http.ResponseWriter, r *http.Request) {
	var in manager.WorkspaceInviteInput
	if err := decode(r, &in); err != nil {
		writeErr(w, r, err)
		return
	}
	v, err := s.M.CreateWorkspaceInvite(r.Context(), pr(r), in)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 201, v)
}

func (s *Server) deleteWorkspaceInvite(w http.ResponseWriter, r *http.Request) {
	if err := s.M.DeleteWorkspaceInvite(r.Context(), pr(r), chi.URLParam(r, "id")); err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) previewWorkspaceInvite(w http.ResponseWriter, r *http.Request) {
	v, err := s.M.PreviewWorkspaceInvite(r.Context(), chi.URLParam(r, "token"))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *Server) acceptWorkspaceInvite(w http.ResponseWriter, r *http.Request) {
	v, err := s.M.AcceptWorkspaceInvite(r.Context(), pr(r), chi.URLParam(r, "token"))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *Server) adminWorkspaces(w http.ResponseWriter, r *http.Request) {
	v, err := s.M.ListWorkspacesAdmin(r.Context(), pr(r))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	list(w, v)
}

func (s *Server) adminPatchWorkspace(w http.ResponseWriter, r *http.Request) {
	var in manager.PatchWorkspaceInput
	if err := decode(r, &in); err != nil {
		writeErr(w, r, err)
		return
	}
	v, err := s.M.PatchWorkspaceAdmin(r.Context(), pr(r), chi.URLParam(r, "id"), in)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *Server) boxLocations(w http.ResponseWriter, r *http.Request) {
	v, err := s.M.BoxLocations(r.Context(), pr(r))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, v)
}
