// Package memstore is an in-memory store.Store for tests and local
// development. It is not durable and must never be used in production.
package memstore

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/osriva/osriva-panel/services/api/internal/auth"
	"github.com/osriva/osriva-panel/services/api/internal/store"
)

type Store struct {
	mu          sync.RWMutex
	permissions map[string]string
	roles       map[string]*store.Role // by ID
	users       map[string]*store.User
	userRoles   map[string][]string // userID -> roleIDs
	sessions    map[string]*store.Session
	audit       []store.AuditEntry
	settings    map[string]json.RawMessage
	nextAudit   int64
}

var _ store.Store = (*Store)(nil)

func New() *Store {
	return &Store{
		permissions: map[string]string{}, roles: map[string]*store.Role{},
		users: map[string]*store.User{}, userRoles: map[string][]string{},
		sessions: map[string]*store.Session{}, settings: map[string]json.RawMessage{},
	}
}

func (s *Store) Ping(context.Context) error { return nil }
func (s *Store) Close()                     {}

func (s *Store) SyncPermissions(_ context.Context, catalog map[string]string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.permissions = make(map[string]string, len(catalog))
	for k, v := range catalog {
		s.permissions[k] = v
	}
	for _, r := range s.roles {
		r.Permissions = slices.DeleteFunc(r.Permissions, func(p string) bool { _, ok := catalog[p]; return !ok })
	}
	return nil
}

func (s *Store) UpsertBuiltinRole(_ context.Context, key, name, description string, permissions []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range permissions {
		if _, ok := s.permissions[p]; !ok {
			return store.ErrNotFound
		}
	}
	perms := slices.Clone(permissions)
	sort.Strings(perms)
	for _, r := range s.roles {
		if r.BuiltIn && r.Key == key {
			r.Name, r.Description, r.Permissions = name, description, perms
			return nil
		}
	}
	id := auth.NewID()
	s.roles[id] = &store.Role{ID: id, Key: key, Name: name, Description: description, BuiltIn: true, Permissions: perms}
	return nil
}

func copyRole(r *store.Role) store.Role {
	c := *r
	c.Permissions = slices.Clone(r.Permissions)
	if c.Permissions == nil {
		c.Permissions = []string{}
	}
	return c
}

func roleVisible(r *store.Role, orgID string) bool {
	return r.OrganizationID == nil || *r.OrganizationID == orgID
}

func sortRoles(rs []store.Role) {
	sort.Slice(rs, func(i, j int) bool {
		if rs[i].BuiltIn != rs[j].BuiltIn {
			return rs[i].BuiltIn
		}
		return rs[i].Key < rs[j].Key
	})
}

func (s *Store) ListRoles(_ context.Context, orgID string) ([]store.Role, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []store.Role{}
	for _, r := range s.roles {
		if roleVisible(r, orgID) {
			out = append(out, copyRole(r))
		}
	}
	sortRoles(out)
	return out, nil
}

func (s *Store) GetRolesByKeys(_ context.Context, orgID string, keys []string) ([]store.Role, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []store.Role{}
	for _, r := range s.roles {
		if roleVisible(r, orgID) && slices.Contains(keys, r.Key) {
			out = append(out, copyRole(r))
		}
	}
	sortRoles(out)
	return out, nil
}

func (s *Store) emailTakenLocked(email, exceptID string) bool {
	for _, u := range s.users {
		if u.ID != exceptID && strings.EqualFold(u.Email, email) {
			return true
		}
	}
	return false
}

func (s *Store) CreateUser(_ context.Context, u *store.User, roleIDs []string, _ *string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.users[u.ID]; ok || s.emailTakenLocked(u.Email, "") {
		return store.ErrConflict
	}
	for _, id := range roleIDs {
		if _, ok := s.roles[id]; !ok {
			return store.ErrNotFound
		}
	}
	now := time.Now().UTC()
	c := *u
	c.CreatedAt, c.UpdatedAt, c.PasswordChangedAt = now, now, now
	if c.Status == "" {
		c.Status = store.UserActive
	}
	s.users[u.ID] = &c
	s.userRoles[u.ID] = slices.Clone(roleIDs)
	*u = c
	return nil
}

func copyUser(u *store.User) *store.User {
	c := *u
	c.TOTPSecretEnc = bytes.Clone(u.TOTPSecretEnc)
	if u.LastLoginAt != nil {
		t := *u.LastLoginAt
		c.LastLoginAt = &t
	}
	return &c
}

func (s *Store) GetUserByID(_ context.Context, id string) (*store.User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if u, ok := s.users[id]; ok {
		return copyUser(u), nil
	}
	return nil, store.ErrNotFound
}

func (s *Store) GetUserByEmail(_ context.Context, email string) (*store.User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, u := range s.users {
		if strings.EqualFold(u.Email, email) {
			return copyUser(u), nil
		}
	}
	return nil, store.ErrNotFound
}

func (s *Store) ListUsers(_ context.Context, f store.UserFilter) ([]store.User, int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	q := strings.ToLower(f.Query)
	var all []store.User
	for _, u := range s.users {
		if u.OrganizationID != f.OrganizationID {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(u.Email), q) && !strings.Contains(strings.ToLower(u.DisplayName), q) {
			continue
		}
		all = append(all, *copyUser(u))
	}
	sort.Slice(all, func(i, j int) bool {
		if !all[i].CreatedAt.Equal(all[j].CreatedAt) {
			return all[i].CreatedAt.Before(all[j].CreatedAt)
		}
		return all[i].ID < all[j].ID
	})
	total := len(all)
	start := min(f.Offset, total)
	end := total
	if f.Limit > 0 {
		end = min(start+f.Limit, total)
	}
	return all[start:end], total, nil
}

func (s *Store) UpdateUser(_ context.Context, id string, upd store.UserUpdate) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.users[id]
	if !ok {
		return store.ErrNotFound
	}
	now := time.Now().UTC()
	if upd.DisplayName != nil {
		u.DisplayName = *upd.DisplayName
	}
	if upd.Status != nil {
		u.Status = *upd.Status
	}
	if upd.PasswordHash != nil {
		u.PasswordHash = *upd.PasswordHash
		u.PasswordChangedAt = now
	}
	if upd.TOTPSecretEnc != nil {
		if len(*upd.TOTPSecretEnc) == 0 {
			u.TOTPSecretEnc = nil
		} else {
			u.TOTPSecretEnc = bytes.Clone(*upd.TOTPSecretEnc)
		}
	}
	if upd.TOTPEnabled != nil {
		u.TOTPEnabled = *upd.TOTPEnabled
	}
	if upd.TOTPLastStep != nil {
		u.TOTPLastStep = *upd.TOTPLastStep
	}
	if upd.LastLoginAt != nil {
		t := *upd.LastLoginAt
		u.LastLoginAt = &t
	}
	u.UpdatedAt = now
	return nil
}

func (s *Store) SetUserRoles(_ context.Context, userID string, roleIDs []string, _ *string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.users[userID]; !ok {
		return store.ErrNotFound
	}
	for _, id := range roleIDs {
		if _, ok := s.roles[id]; !ok {
			return store.ErrNotFound
		}
	}
	s.userRoles[userID] = slices.Clone(roleIDs)
	return nil
}

func (s *Store) GetUserRoles(_ context.Context, userID string) ([]store.Role, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []store.Role{}
	for _, id := range s.userRoles[userID] {
		if r, ok := s.roles[id]; ok {
			out = append(out, copyRole(r))
		}
	}
	sortRoles(out)
	return out, nil
}

func (s *Store) CountActiveUsersWithRole(_ context.Context, orgID, roleKey string) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	n := 0
	for uid, roleIDs := range s.userRoles {
		u := s.users[uid]
		if u == nil || u.OrganizationID != orgID || u.Status != store.UserActive {
			continue
		}
		for _, rid := range roleIDs {
			if r := s.roles[rid]; r != nil && r.Key == roleKey {
				n++
				break
			}
		}
	}
	return n, nil
}

func copySession(x *store.Session) *store.Session {
	c := *x
	c.TokenHash = bytes.Clone(x.TokenHash)
	if x.ReauthenticatedAt != nil {
		t := *x.ReauthenticatedAt
		c.ReauthenticatedAt = &t
	}
	if x.RevokedAt != nil {
		t := *x.RevokedAt
		c.RevokedAt = &t
	}
	return &c
}

func (s *Store) CreateSession(_ context.Context, x *store.Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.users[x.UserID]; !ok {
		return store.ErrNotFound
	}
	for _, e := range s.sessions {
		if bytes.Equal(e.TokenHash, x.TokenHash) {
			return store.ErrConflict
		}
	}
	s.sessions[x.ID] = copySession(x)
	return nil
}

func (s *Store) GetSessionByTokenHash(_ context.Context, hash []byte) (*store.Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, e := range s.sessions {
		if bytes.Equal(e.TokenHash, hash) {
			return copySession(e), nil
		}
	}
	return nil, store.ErrNotFound
}

func (s *Store) withSession(id string, fn func(*store.Session)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.sessions[id]
	if !ok {
		return store.ErrNotFound
	}
	fn(e)
	return nil
}

func (s *Store) TouchSession(_ context.Context, id string, at time.Time) error {
	return s.withSession(id, func(e *store.Session) { e.LastSeenAt = at })
}

func (s *Store) MarkSessionReauthenticated(_ context.Context, id string, at time.Time) error {
	return s.withSession(id, func(e *store.Session) { e.ReauthenticatedAt = &at })
}

func (s *Store) RevokeSession(_ context.Context, id string, at time.Time) error {
	return s.withSession(id, func(e *store.Session) {
		if e.RevokedAt == nil {
			e.RevokedAt = &at
		}
	})
}

func (s *Store) RevokeUserSessions(_ context.Context, userID, exceptID string, at time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, e := range s.sessions {
		if e.UserID == userID && e.ID != exceptID && e.RevokedAt == nil {
			t := at
			e.RevokedAt = &t
			n++
		}
	}
	return n, nil
}

func (s *Store) ListActiveSessions(_ context.Context, userID string, now time.Time) ([]store.Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []store.Session{}
	for _, e := range s.sessions {
		if e.UserID == userID && e.RevokedAt == nil && e.ExpiresAt.After(now) {
			out = append(out, *copySession(e))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastSeenAt.After(out[j].LastSeenAt) })
	return out, nil
}

func (s *Store) InsertAudit(_ context.Context, e *store.AuditEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextAudit++
	c := *e
	c.ID = s.nextAudit
	c.CreatedAt = time.Now().UTC()
	if c.Metadata == nil {
		c.Metadata = map[string]any{}
	}
	// Round-trip metadata through JSON so stored values match what PostgreSQL returns.
	raw, err := json.Marshal(c.Metadata)
	if err != nil {
		return err
	}
	c.Metadata = map[string]any{}
	_ = json.Unmarshal(raw, &c.Metadata)
	s.audit = append(s.audit, c)
	e.ID, e.CreatedAt = c.ID, c.CreatedAt
	return nil
}

func (s *Store) ListAudit(_ context.Context, f store.AuditFilter) ([]store.AuditEntry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	out := []store.AuditEntry{}
	for i := len(s.audit) - 1; i >= 0 && len(out) < limit; i-- {
		e := s.audit[i]
		if f.OrganizationID != "" && (e.OrganizationID == nil || *e.OrganizationID != f.OrganizationID) {
			continue
		}
		if f.ActorUserID != "" && (e.ActorUserID == nil || *e.ActorUserID != f.ActorUserID) {
			continue
		}
		if f.ActionPrefix != "" && !strings.HasPrefix(e.Action, f.ActionPrefix) {
			continue
		}
		if f.Outcome != "" && e.Outcome != f.Outcome {
			continue
		}
		if f.BeforeID > 0 && e.ID >= f.BeforeID {
			continue
		}
		out = append(out, e)
	}
	return out, nil
}

func (s *Store) GetSetting(_ context.Context, key string) (json.RawMessage, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.settings[key]
	if !ok {
		return nil, store.ErrNotFound
	}
	return bytes.Clone(v), nil
}

func (s *Store) PutSetting(_ context.Context, key string, value json.RawMessage, _ *string) error {
	if !json.Valid(value) {
		return store.ErrConflict
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.settings[key] = bytes.Clone(value)
	return nil
}
