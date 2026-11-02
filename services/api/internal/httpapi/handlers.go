package httpapi

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/identity"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/store"
)

// ---- response DTOs (never include hashes, secrets or tokens) ----

type roleRef struct {
	Key  string `json:"key"`
	Name string `json:"name"`
}

type userDTO struct {
	ID          string    `json:"id"`
	Email       string    `json:"email"`
	DisplayName string    `json:"display_name"`
	Status      string    `json:"status"`
	MFAEnabled  bool      `json:"mfa_enabled"`
	Roles       []roleRef `json:"roles"`
	LastLoginAt *string   `json:"last_login_at"`
	CreatedAt   string    `json:"created_at"`
}

func toUserDTO(u *store.User, roles []store.Role) userDTO {
	refs := make([]roleRef, len(roles))
	for i, r := range roles {
		refs[i] = roleRef{Key: r.Key, Name: r.Name}
	}
	return userDTO{ID: u.ID, Email: u.Email, DisplayName: u.DisplayName, Status: string(u.Status),
		MFAEnabled: u.TOTPEnabled, Roles: refs, LastLoginAt: tsPtr(u.LastLoginAt), CreatedAt: ts(u.CreatedAt)}
}

type sessionDTO struct {
	ID         string `json:"id"`
	Current    bool   `json:"current"`
	IP         string `json:"ip"`
	UserAgent  string `json:"user_agent"`
	CreatedAt  string `json:"created_at"`
	LastSeenAt string `json:"last_seen_at"`
	ExpiresAt  string `json:"expires_at"`
}

func toSessionDTOs(list []store.Session, currentID string) []sessionDTO {
	out := make([]sessionDTO, len(list))
	for i, x := range list {
		out[i] = sessionDTO{ID: x.ID, Current: x.ID == currentID, IP: x.IP, UserAgent: x.UserAgent,
			CreatedAt: ts(x.CreatedAt), LastSeenAt: ts(x.LastSeenAt), ExpiresAt: ts(x.ExpiresAt)}
	}
	return out
}

type auditDTO struct {
	ID          int64          `json:"id"`
	ActorUserID *string        `json:"actor_user_id"`
	ActorLabel  string         `json:"actor_label"`
	Action      string         `json:"action"`
	TargetType  string         `json:"target_type"`
	TargetID    string         `json:"target_id"`
	Outcome     string         `json:"outcome"`
	IP          string         `json:"ip"`
	UserAgent   string         `json:"user_agent"`
	RequestID   string         `json:"request_id"`
	Metadata    map[string]any `json:"metadata"`
	CreatedAt   string         `json:"created_at"`
}

func toAuditDTOs(list []store.AuditEntry) []auditDTO {
	out := make([]auditDTO, len(list))
	for i, e := range list {
		out[i] = auditDTO{ID: e.ID, ActorUserID: e.ActorUserID, ActorLabel: e.ActorLabel, Action: e.Action,
			TargetType: e.TargetType, TargetID: e.TargetID, Outcome: string(e.Outcome), IP: e.IP, UserAgent: e.UserAgent,
			RequestID: e.RequestID, Metadata: e.Metadata, CreatedAt: ts(e.CreatedAt)}
	}
	return out
}

func (s *Server) meBody(r *http.Request, p *identity.Principal) (map[string]any, error) {
	mode, err := s.id.PanelMode(r.Context())
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"user":        toUserDTO(p.User, p.Roles),
		"permissions": p.Permissions.Sorted(),
		"csrf_token":  s.id.CSRFToken(p),
		"panel_mode":  mode,
		"session": map[string]any{
			"id": p.Session.ID, "expires_at": ts(p.Session.ExpiresAt),
			"reauthenticated_at": tsPtr(p.Session.ReauthenticatedAt),
		},
	}, nil
}

func queryInt(r *http.Request, key string) int {
	n, _ := strconv.Atoi(r.URL.Query().Get(key))
	return n
}

// ---- auth ----

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Email    string `json:"email"`
		Password string `json:"password"`
		TOTPCode string `json:"totp_code"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	res, err := s.id.Login(r.Context(), identity.LoginInput{Email: in.Email, Password: in.Password, TOTPCode: in.TOTPCode})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.setSessionCookie(w, res.Token)
	body, err := s.meBody(r, res.Principal)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, body)
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	body, err := s.meBody(r, principalFrom(r.Context()))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, body)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if err := s.id.Logout(r.Context(), principalFrom(r.Context())); err != nil {
		s.fail(w, r, err)
		return
	}
	s.clearSessionCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleReauth(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Password string `json:"password"`
		TOTPCode string `json:"totp_code"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	p := principalFrom(r.Context())
	if err := s.id.Reauthenticate(r.Context(), p, in.Password, in.TOTPCode); err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"reauthenticated_at": tsPtr(p.Session.ReauthenticatedAt)})
}

func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	var in struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	if err := s.id.ChangePassword(r.Context(), principalFrom(r.Context()), in.CurrentPassword, in.NewPassword); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	list, err := s.id.ListSessions(r.Context(), p.User.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": toSessionDTOs(list, p.Session.ID)})
}

func (s *Server) handleRevokeSession(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	id := chi.URLParam(r, "id")
	if err := s.id.RevokeOwnSession(r.Context(), p, id); err != nil {
		s.fail(w, r, err)
		return
	}
	if id == p.Session.ID {
		s.clearSessionCookie(w)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleRevokeOtherSessions(w http.ResponseWriter, r *http.Request) {
	n, err := s.id.RevokeOtherSessions(r.Context(), principalFrom(r.Context()))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"revoked": n})
}

func (s *Server) handleLoginHistory(w http.ResponseWriter, r *http.Request) {
	list, err := s.id.LoginHistory(r.Context(), principalFrom(r.Context()), queryInt(r, "limit"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": toAuditDTOs(list)})
}

func (s *Server) handleTOTPSetup(w http.ResponseWriter, r *http.Request) {
	enr, err := s.id.BeginTOTP(r.Context(), principalFrom(r.Context()))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	// The secret is shown exactly once, during enrolment, to the account owner.
	writeJSON(w, http.StatusOK, map[string]string{"secret": enr.Secret, "otpauth_uri": enr.URI})
}

func (s *Server) handleTOTPEnable(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Code string `json:"code"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	if err := s.id.EnableTOTP(r.Context(), principalFrom(r.Context()), in.Code); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleTOTPDisable(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	if err := s.id.DisableTOTP(r.Context(), principalFrom(r.Context()), in.Password, in.Code); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- users & roles ----

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	list, total, err := s.id.ListUsers(r.Context(), principalFrom(r.Context()), q.Get("q"), queryInt(r, "limit"), queryInt(r, "offset"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	users := make([]userDTO, len(list))
	for i, v := range list {
		users[i] = toUserDTO(v.User, v.Roles)
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": users, "total": total})
}

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Email       string   `json:"email"`
		DisplayName string   `json:"display_name"`
		Password    string   `json:"password"`
		Roles       []string `json:"roles"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	v, err := s.id.CreateUser(r.Context(), principalFrom(r.Context()), identity.CreateUserInput{
		Email: in.Email, DisplayName: in.DisplayName, Password: in.Password, RoleKeys: in.Roles,
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toUserDTO(v.User, v.Roles))
}

func (s *Server) handleGetUser(w http.ResponseWriter, r *http.Request) {
	v, err := s.id.GetUser(r.Context(), principalFrom(r.Context()), chi.URLParam(r, "id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toUserDTO(v.User, v.Roles))
}

func (s *Server) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	var in struct {
		DisplayName *string   `json:"display_name"`
		Status      *string   `json:"status"`
		Roles       *[]string `json:"roles"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	upd := identity.UpdateUserInput{DisplayName: in.DisplayName, RoleKeys: in.Roles}
	if in.Status != nil {
		st := store.UserStatus(*in.Status)
		upd.Status = &st
	}
	v, err := s.id.UpdateUser(r.Context(), principalFrom(r.Context()), chi.URLParam(r, "id"), upd)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toUserDTO(v.User, v.Roles))
}

func (s *Server) handleUserSessions(w http.ResponseWriter, r *http.Request) {
	list, err := s.id.AdminSessions(r.Context(), principalFrom(r.Context()), chi.URLParam(r, "id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": toSessionDTOs(list, principalFrom(r.Context()).Session.ID)})
}

func (s *Server) handleRevokeUserSessions(w http.ResponseWriter, r *http.Request) {
	n, err := s.id.AdminRevokeSessions(r.Context(), principalFrom(r.Context()), chi.URLParam(r, "id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"revoked": n})
}

func (s *Server) handleListRoles(w http.ResponseWriter, r *http.Request) {
	roles, err := s.id.ListRoles(r.Context(), principalFrom(r.Context()))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	type roleDTO struct {
		Key         string   `json:"key"`
		Name        string   `json:"name"`
		Description string   `json:"description"`
		BuiltIn     bool     `json:"built_in"`
		Permissions []string `json:"permissions"`
	}
	out := make([]roleDTO, len(roles))
	for i, x := range roles {
		out[i] = roleDTO{Key: x.Key, Name: x.Name, Description: x.Description, BuiltIn: x.BuiltIn, Permissions: x.Permissions}
	}
	writeJSON(w, http.StatusOK, map[string]any{"roles": out})
}

func (s *Server) handleListAudit(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	before, _ := strconv.ParseInt(q.Get("before"), 10, 64)
	list, err := s.id.ListAudit(r.Context(), principalFrom(r.Context()), store.AuditFilter{
		ActorUserID: q.Get("actor"), ActionPrefix: q.Get("action"), Outcome: store.AuditOutcome(q.Get("outcome")),
		BeforeID: before, Limit: queryInt(r, "limit"),
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	body := map[string]any{"entries": toAuditDTOs(list), "next_before": nil}
	if len(list) > 0 {
		body["next_before"] = list[len(list)-1].ID
	}
	writeJSON(w, http.StatusOK, body)
}

// ---- settings ----

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	st, err := s.id.GetSettings(r.Context(), principalFrom(r.Context()))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) handleSetPanelMode(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Mode string `json:"mode"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	p := principalFrom(r.Context())
	if err := s.id.SetPanelMode(r.Context(), p, in.Mode); err != nil {
		s.fail(w, r, err)
		return
	}
	st, err := s.id.GetSettings(r.Context(), p)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}
