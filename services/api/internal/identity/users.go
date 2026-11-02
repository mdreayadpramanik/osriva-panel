package identity

import (
	"context"
	"errors"
	"net/mail"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/auth"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/rbac"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/store"
)

// UserView is a user together with their roles.
type UserView struct {
	User  *store.User
	Roles []store.Role
}

type CreateUserInput struct {
	Email       string
	DisplayName string
	Password    string
	RoleKeys    []string
}

type UpdateUserInput struct {
	DisplayName *string
	Status      *store.UserStatus
	RoleKeys    *[]string
}

func validateEmail(e string) (string, error) {
	e = normalizeEmail(e)
	invalid := &ValidationError{Field: "email", Message: "must be a valid email address"}
	addr, err := mail.ParseAddress(e)
	if err != nil || addr.Address != e || len(e) > 320 {
		return "", invalid
	}
	domain := e[strings.LastIndexByte(e, '@')+1:]
	if !strings.Contains(domain, ".") && domain != "localhost" {
		return "", invalid
	}
	return e, nil
}

func validateDisplayName(n string) (string, error) {
	n = strings.TrimSpace(n)
	if n == "" || utf8.RuneCountInString(n) > 200 {
		return "", &ValidationError{Field: "display_name", Message: "must be 1–200 characters"}
	}
	return n, nil
}

// resolveRoles maps keys to roles in the organization, rejecting unknown keys.
func (s *Service) resolveRoles(ctx context.Context, orgID string, keys []string) ([]store.Role, error) {
	keys = slices.Compact(slices.Sorted(slices.Values(keys)))
	roles, err := s.store.GetRolesByKeys(ctx, orgID, keys)
	if err != nil {
		return nil, err
	}
	if len(roles) != len(keys) {
		return nil, &ValidationError{Field: "roles", Message: "contains an unknown role"}
	}
	return roles, nil
}

func roleIDs(roles []store.Role) []string {
	ids := make([]string, len(roles))
	for i, r := range roles {
		ids[i] = r.ID
	}
	return ids
}

func roleKeys(roles []store.Role) []string {
	keys := make([]string, len(roles))
	for i, r := range roles {
		keys[i] = r.Key
	}
	return keys
}

// checkGrant enforces the anti-escalation rule: an actor may only grant
// permissions they hold themselves.
func (s *Service) checkGrant(ctx context.Context, actor *Principal, roles []store.Role) error {
	if !actor.Permissions.Covers(permissionsOf(roles)) {
		s.audit(ctx, auditEvent{actor: actor.User, action: "authz.escalation_blocked", outcome: store.OutcomeDenied,
			metadata: map[string]any{"roles": roleKeys(roles)}})
		return ErrForbidden
	}
	return nil
}

func (s *Service) CreateUser(ctx context.Context, actor *Principal, in CreateUserInput) (*UserView, error) {
	if err := s.Require(ctx, actor, rbac.UsersWrite); err != nil {
		return nil, err
	}
	email, err := validateEmail(in.Email)
	if err != nil {
		return nil, err
	}
	name, err := validateDisplayName(in.DisplayName)
	if err != nil {
		return nil, err
	}
	if reason := auth.ValidatePasswordPolicy(in.Password, email); reason != "" {
		return nil, &ValidationError{Field: "password", Message: reason}
	}
	if len(in.RoleKeys) == 0 {
		in.RoleKeys = []string{rbac.RoleMember}
	}
	roles, err := s.resolveRoles(ctx, actor.User.OrganizationID, in.RoleKeys)
	if err != nil {
		return nil, err
	}
	if err := s.checkGrant(ctx, actor, roles); err != nil {
		return nil, err
	}
	hash, err := auth.HashPassword(in.Password, s.argon2)
	if err != nil {
		return nil, err
	}
	u := &store.User{ID: auth.NewID(), OrganizationID: actor.User.OrganizationID, Email: email, DisplayName: name,
		PasswordHash: hash, Status: store.UserActive}
	if err := s.store.CreateUser(ctx, u, roleIDs(roles), &actor.User.ID); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return nil, &ValidationError{Field: "email", Message: "is already in use"}
		}
		return nil, err
	}
	s.audit(ctx, auditEvent{actor: actor.User, action: "users.create", targetType: "user", targetID: u.ID,
		outcome: store.OutcomeSuccess, metadata: map[string]any{"email": email, "roles": roleKeys(roles)}})
	return &UserView{User: u, Roles: roles}, nil
}

// getInOrg loads a user, hiding users of other organizations as not found.
func (s *Service) getInOrg(ctx context.Context, actor *Principal, id string) (*store.User, error) {
	u, err := s.store.GetUserByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if u.OrganizationID != actor.User.OrganizationID {
		return nil, store.ErrNotFound
	}
	return u, nil
}

func (s *Service) GetUser(ctx context.Context, actor *Principal, id string) (*UserView, error) {
	if err := s.Require(ctx, actor, rbac.UsersRead); err != nil {
		return nil, err
	}
	u, err := s.getInOrg(ctx, actor, id)
	if err != nil {
		return nil, err
	}
	roles, err := s.store.GetUserRoles(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	return &UserView{User: u, Roles: roles}, nil
}

func (s *Service) ListUsers(ctx context.Context, actor *Principal, query string, limit, offset int) ([]UserView, int, error) {
	if err := s.Require(ctx, actor, rbac.UsersRead); err != nil {
		return nil, 0, err
	}
	users, total, err := s.store.ListUsers(ctx, store.UserFilter{
		OrganizationID: actor.User.OrganizationID, Query: strings.TrimSpace(query),
		Limit: clampLimit(limit, 50, 200), Offset: max(offset, 0),
	})
	if err != nil {
		return nil, 0, err
	}
	out := make([]UserView, len(users))
	for i := range users {
		roles, err := s.store.GetUserRoles(ctx, users[i].ID)
		if err != nil {
			return nil, 0, err
		}
		out[i] = UserView{User: &users[i], Roles: roles}
	}
	return out, total, nil
}

func hasRole(roles []store.Role, key string) bool {
	return slices.ContainsFunc(roles, func(r store.Role) bool { return r.Key == key })
}

func (s *Service) UpdateUser(ctx context.Context, actor *Principal, id string, in UpdateUserInput) (*UserView, error) {
	if err := s.Require(ctx, actor, rbac.UsersWrite); err != nil {
		return nil, err
	}
	target, err := s.getInOrg(ctx, actor, id)
	if err != nil {
		return nil, err
	}
	current, err := s.store.GetUserRoles(ctx, target.ID)
	if err != nil {
		return nil, err
	}
	// Nobody may manage a user who holds permissions they lack.
	if err := s.checkGrant(ctx, actor, current); err != nil {
		return nil, err
	}

	privileged := in.Status != nil || in.RoleKeys != nil
	if privileged {
		if err := s.RequireRecentReauth(actor); err != nil {
			return nil, err
		}
	}

	upd := store.UserUpdate{}
	changes := map[string]any{}
	if in.DisplayName != nil {
		name, err := validateDisplayName(*in.DisplayName)
		if err != nil {
			return nil, err
		}
		upd.DisplayName = &name
		changes["display_name"] = name
	}
	if in.Status != nil {
		if *in.Status != store.UserActive && *in.Status != store.UserDisabled {
			return nil, &ValidationError{Field: "status", Message: "must be active or disabled"}
		}
		if target.ID == actor.User.ID && *in.Status == store.UserDisabled {
			return nil, &PolicyError{Message: "you cannot disable your own account"}
		}
		upd.Status = in.Status
		changes["status"] = string(*in.Status)
	}

	newRoles := current
	if in.RoleKeys != nil {
		if len(*in.RoleKeys) == 0 {
			return nil, &ValidationError{Field: "roles", Message: "must contain at least one role"}
		}
		newRoles, err = s.resolveRoles(ctx, actor.User.OrganizationID, *in.RoleKeys)
		if err != nil {
			return nil, err
		}
		if err := s.checkGrant(ctx, actor, newRoles); err != nil {
			return nil, err
		}
		changes["roles"] = roleKeys(newRoles)
	}

	// Keep at least one active owner, or the panel becomes unmanageable.
	losesOwner := hasRole(current, rbac.RoleOwner) && target.Status == store.UserActive &&
		(!hasRole(newRoles, rbac.RoleOwner) || (in.Status != nil && *in.Status == store.UserDisabled))
	if losesOwner {
		n, err := s.store.CountActiveUsersWithRole(ctx, actor.User.OrganizationID, rbac.RoleOwner)
		if err != nil {
			return nil, err
		}
		if n <= 1 {
			return nil, &PolicyError{Message: "this is the last active owner; promote another owner first"}
		}
	}

	if upd != (store.UserUpdate{}) {
		if err := s.store.UpdateUser(ctx, target.ID, upd); err != nil {
			return nil, err
		}
	}
	if in.RoleKeys != nil {
		if err := s.store.SetUserRoles(ctx, target.ID, roleIDs(newRoles), &actor.User.ID); err != nil {
			return nil, err
		}
	}
	if in.Status != nil && *in.Status == store.UserDisabled {
		if _, err := s.store.RevokeUserSessions(ctx, target.ID, "", s.now()); err != nil {
			return nil, err
		}
	}
	if len(changes) > 0 {
		s.audit(ctx, auditEvent{actor: actor.User, action: "users.update", targetType: "user", targetID: target.ID,
			outcome: store.OutcomeSuccess, metadata: changes})
	}
	return s.GetUser(ctx, actor, target.ID)
}

// AdminSessions lists another user's active sessions.
func (s *Service) AdminSessions(ctx context.Context, actor *Principal, userID string) ([]store.Session, error) {
	if err := s.Require(ctx, actor, rbac.SessionsManageAll); err != nil {
		return nil, err
	}
	if _, err := s.getInOrg(ctx, actor, userID); err != nil {
		return nil, err
	}
	return s.ListSessions(ctx, userID)
}

// AdminRevokeSessions signs a user out everywhere.
func (s *Service) AdminRevokeSessions(ctx context.Context, actor *Principal, userID string) (int, error) {
	if err := s.Require(ctx, actor, rbac.SessionsManageAll); err != nil {
		return 0, err
	}
	target, err := s.getInOrg(ctx, actor, userID)
	if err != nil {
		return 0, err
	}
	roles, err := s.store.GetUserRoles(ctx, target.ID)
	if err != nil {
		return 0, err
	}
	if err := s.checkGrant(ctx, actor, roles); err != nil {
		return 0, err
	}
	n, err := s.store.RevokeUserSessions(ctx, target.ID, "", s.now())
	if err != nil {
		return 0, err
	}
	s.audit(ctx, auditEvent{actor: actor.User, action: "users.sessions_revoke", targetType: "user", targetID: target.ID,
		outcome: store.OutcomeSuccess, metadata: map[string]any{"count": n}})
	return n, nil
}

func (s *Service) ListRoles(ctx context.Context, actor *Principal) ([]store.Role, error) {
	if err := s.Require(ctx, actor, rbac.RolesRead); err != nil {
		return nil, err
	}
	return s.store.ListRoles(ctx, actor.User.OrganizationID)
}

func (s *Service) ListAudit(ctx context.Context, actor *Principal, f store.AuditFilter) ([]store.AuditEntry, error) {
	if err := s.Require(ctx, actor, rbac.AuditRead); err != nil {
		return nil, err
	}
	f.OrganizationID = actor.User.OrganizationID
	f.Limit = clampLimit(f.Limit, 50, 200)
	return s.store.ListAudit(ctx, f)
}

// BootstrapUser creates a user from the host CLI (trusted: the operator has
// shell access to the control plane). It bypasses RBAC checks but not validation.
func (s *Service) BootstrapUser(ctx context.Context, email, displayName, password, roleKey, via string) (*store.User, error) {
	email, err := validateEmail(email)
	if err != nil {
		return nil, err
	}
	name, err := validateDisplayName(displayName)
	if err != nil {
		return nil, err
	}
	if reason := auth.ValidatePasswordPolicy(password, email); reason != "" {
		return nil, &ValidationError{Field: "password", Message: reason}
	}
	roles, err := s.resolveRoles(ctx, store.DefaultOrganizationID, []string{roleKey})
	if err != nil {
		return nil, err
	}
	hash, err := auth.HashPassword(password, s.argon2)
	if err != nil {
		return nil, err
	}
	u := &store.User{ID: auth.NewID(), OrganizationID: store.DefaultOrganizationID, Email: email, DisplayName: name,
		PasswordHash: hash, Status: store.UserActive}
	if err := s.store.CreateUser(ctx, u, roleIDs(roles), nil); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return nil, &ValidationError{Field: "email", Message: "is already in use"}
		}
		return nil, err
	}
	s.audit(ctx, auditEvent{action: "users.bootstrap", targetType: "user", targetID: u.ID, outcome: store.OutcomeSuccess,
		metadata: map[string]any{"email": email, "role": roleKey, "via": via}})
	return u, nil
}
