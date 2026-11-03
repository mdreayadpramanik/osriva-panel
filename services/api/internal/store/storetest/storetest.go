// Package storetest is the contract every store.Store implementation must
// satisfy. Implementations call Run from their own tests.
package storetest

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/auth"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/rbac"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/store"
)

// Run executes the contract. newStore must return an empty, migrated store.
func Run(t *testing.T, newStore func(t *testing.T) store.Store) {
	tests := map[string]func(t *testing.T, s store.Store){
		"RolesSync":           testRolesSync,
		"Users":               testUsers,
		"UserRoles":           testUserRoles,
		"Sessions":            testSessions,
		"Audit":               testAudit,
		"Settings":            testSettings,
		"ListUsersPaging":     testListUsersPaging,
		"UpdateClearsTOTP":    testUpdateClearsTOTP,
		"AgentCA":             testAgentCA,
		"ServerEnrollment":    testServerEnrollment,
		"ServerTokenRotation": testServerTokenRotation,
		"ServerReports":       testServerReports,
		"TaskQueue":           testTaskQueue,
		"Metrics":             testMetrics,
		"Apps":                testApps,
		"Proxy":               testProxy,
		"CMS":                 testCMS,
	}
	for name, fn := range tests {
		t.Run(name, func(t *testing.T) {
			s := newStore(t)
			t.Cleanup(s.Close)
			seedRBAC(t, s)
			fn(t, s)
		})
	}
}

func ctx() context.Context { return context.Background() }

func seedRBAC(t *testing.T, s store.Store) {
	t.Helper()
	if err := s.SyncPermissions(ctx(), rbac.Catalog); err != nil {
		t.Fatalf("SyncPermissions: %v", err)
	}
	for _, r := range rbac.BuiltinRoles() {
		if err := s.UpsertBuiltinRole(ctx(), r.Key, r.Name, r.Description, r.Permissions); err != nil {
			t.Fatalf("UpsertBuiltinRole %s: %v", r.Key, err)
		}
	}
}

func roleID(t *testing.T, s store.Store, key string) string {
	t.Helper()
	rs, err := s.GetRolesByKeys(ctx(), store.DefaultOrganizationID, []string{key})
	if err != nil || len(rs) != 1 {
		t.Fatalf("role %s: %v %v", key, rs, err)
	}
	return rs[0].ID
}

func newUser(t *testing.T, s store.Store, email string, roleKeys ...string) *store.User {
	t.Helper()
	var ids []string
	for _, k := range roleKeys {
		ids = append(ids, roleID(t, s, k))
	}
	u := &store.User{
		ID: auth.NewID(), OrganizationID: store.DefaultOrganizationID, Email: email,
		DisplayName: "Test " + email, PasswordHash: "$argon2id$placeholder", Status: store.UserActive,
	}
	if err := s.CreateUser(ctx(), u, ids, nil); err != nil {
		t.Fatalf("CreateUser %s: %v", email, err)
	}
	return u
}

func testRolesSync(t *testing.T, s store.Store) {
	// Re-running the sync must be idempotent and update in place.
	seedRBAC(t, s)
	if err := s.UpsertBuiltinRole(ctx(), rbac.RoleAuditor, "Auditor v2", "d", []string{rbac.AuditRead}); err != nil {
		t.Fatal(err)
	}
	roles, err := s.ListRoles(ctx(), store.DefaultOrganizationID)
	if err != nil {
		t.Fatal(err)
	}
	if len(roles) != len(rbac.BuiltinRoles()) {
		t.Fatalf("got %d roles, want %d", len(roles), len(rbac.BuiltinRoles()))
	}
	for _, r := range roles {
		if !r.BuiltIn || r.OrganizationID != nil {
			t.Errorf("role %s should be built-in and global", r.Key)
		}
		if r.Key == rbac.RoleAuditor && (r.Name != "Auditor v2" || len(r.Permissions) != 1 || r.Permissions[0] != rbac.AuditRead) {
			t.Errorf("auditor not updated: %+v", r)
		}
		if r.Key == rbac.RoleMember && (r.Permissions == nil || len(r.Permissions) != 0) {
			t.Errorf("member permissions should be empty non-nil slice: %#v", r.Permissions)
		}
	}
	if err := s.UpsertBuiltinRole(ctx(), "bogus", "B", "", []string{"no.such"}); err == nil {
		t.Error("role with unknown permission accepted")
	}
}

func testUsers(t *testing.T, s store.Store) {
	u := newUser(t, s, "Alice@Example.com", rbac.RoleAdmin)
	if u.CreatedAt.IsZero() || u.Status != store.UserActive {
		t.Fatalf("CreateUser did not populate defaults: %+v", u)
	}
	dup := &store.User{ID: auth.NewID(), OrganizationID: store.DefaultOrganizationID, Email: "alice@example.COM", DisplayName: "x", PasswordHash: "x", Status: store.UserActive}
	if err := s.CreateUser(ctx(), dup, nil, nil); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("case-insensitive duplicate email: got %v, want ErrConflict", err)
	}
	got, err := s.GetUserByEmail(ctx(), "ALICE@example.com")
	if err != nil || got.ID != u.ID {
		t.Fatalf("GetUserByEmail: %v %v", got, err)
	}
	if _, err := s.GetUserByID(ctx(), auth.NewID()); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing user: %v", err)
	}
	name, status, hash, now := "Alice B", store.UserDisabled, "$argon2id$new", time.Now().UTC().Truncate(time.Microsecond)
	if err := s.UpdateUser(ctx(), u.ID, store.UserUpdate{DisplayName: &name, Status: &status, PasswordHash: &hash, LastLoginAt: &now}); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetUserByID(ctx(), u.ID)
	if got.DisplayName != name || got.Status != status || got.PasswordHash != hash || got.LastLoginAt == nil || !got.LastLoginAt.Equal(now) {
		t.Fatalf("update not applied: %+v", got)
	}
	if err := s.UpdateUser(ctx(), auth.NewID(), store.UserUpdate{DisplayName: &name}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("update missing user: %v", err)
	}
}

func testUpdateClearsTOTP(t *testing.T, s store.Store) {
	u := newUser(t, s, "totp@example.com")
	secret, enabled, step := []byte{1, 2, 3}, true, int64(42)
	if err := s.UpdateUser(ctx(), u.ID, store.UserUpdate{TOTPSecretEnc: &secret, TOTPEnabled: &enabled, TOTPLastStep: &step}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetUserByID(ctx(), u.ID)
	if !got.TOTPEnabled || string(got.TOTPSecretEnc) != string(secret) || got.TOTPLastStep != 42 {
		t.Fatalf("totp not stored: %+v", got)
	}
	empty, disabled := []byte{}, false
	if err := s.UpdateUser(ctx(), u.ID, store.UserUpdate{TOTPSecretEnc: &empty, TOTPEnabled: &disabled}); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetUserByID(ctx(), u.ID)
	if got.TOTPEnabled || got.TOTPSecretEnc != nil {
		t.Fatalf("totp not cleared: %+v", got)
	}
}

func testUserRoles(t *testing.T, s store.Store) {
	u := newUser(t, s, "bob@example.com", rbac.RoleOwner)
	roles, err := s.GetUserRoles(ctx(), u.ID)
	if err != nil || len(roles) != 1 || roles[0].Key != rbac.RoleOwner || len(roles[0].Permissions) != len(rbac.Catalog) {
		t.Fatalf("GetUserRoles: %+v %v", roles, err)
	}
	if n, _ := s.CountActiveUsersWithRole(ctx(), store.DefaultOrganizationID, rbac.RoleOwner); n != 1 {
		t.Fatalf("owner count = %d", n)
	}
	if err := s.SetUserRoles(ctx(), u.ID, []string{roleID(t, s, rbac.RoleAuditor), roleID(t, s, rbac.RoleMember)}, nil); err != nil {
		t.Fatal(err)
	}
	roles, _ = s.GetUserRoles(ctx(), u.ID)
	if len(roles) != 2 || roles[0].Key != rbac.RoleAuditor || roles[1].Key != rbac.RoleMember {
		t.Fatalf("roles after set: %+v", roles)
	}
	if n, _ := s.CountActiveUsersWithRole(ctx(), store.DefaultOrganizationID, rbac.RoleOwner); n != 0 {
		t.Fatalf("owner count after demotion = %d", n)
	}
	if err := s.SetUserRoles(ctx(), u.ID, []string{auth.NewID()}, nil); err == nil {
		t.Fatal("unknown role accepted")
	}
}

func testSessions(t *testing.T, s store.Store) {
	u := newUser(t, s, "carol@example.com")
	now := time.Now().UTC().Truncate(time.Microsecond)
	mk := func() *store.Session {
		_, h, _ := auth.NewSessionToken()
		x := &store.Session{ID: auth.NewID(), UserID: u.ID, TokenHash: h, CreatedAt: now, ExpiresAt: now.Add(time.Hour), LastSeenAt: now, IP: "192.0.2.10", UserAgent: "test"}
		if err := s.CreateSession(ctx(), x); err != nil {
			t.Fatal(err)
		}
		return x
	}
	a, b, c := mk(), mk(), mk()
	got, err := s.GetSessionByTokenHash(ctx(), a.TokenHash)
	if err != nil || got.ID != a.ID || got.IP != "192.0.2.10" || !got.ExpiresAt.Equal(a.ExpiresAt) {
		t.Fatalf("GetSessionByTokenHash: %+v %v", got, err)
	}
	if _, err := s.GetSessionByTokenHash(ctx(), make([]byte, 32)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing session: %v", err)
	}
	later := now.Add(time.Minute)
	if err := s.TouchSession(ctx(), a.ID, later); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkSessionReauthenticated(ctx(), a.ID, later); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetSessionByTokenHash(ctx(), a.TokenHash)
	if !got.LastSeenAt.Equal(later) || got.ReauthenticatedAt == nil || !got.ReauthenticatedAt.Equal(later) {
		t.Fatalf("touch/reauth not stored: %+v", got)
	}
	if err := s.RevokeSession(ctx(), b.ID, later); err != nil {
		t.Fatal(err)
	}
	active, _ := s.ListActiveSessions(ctx(), u.ID, now)
	if len(active) != 2 {
		t.Fatalf("active sessions = %d, want 2", len(active))
	}
	n, err := s.RevokeUserSessions(ctx(), u.ID, a.ID, later)
	if err != nil || n != 1 {
		t.Fatalf("RevokeUserSessions = %d, %v (want 1: only c)", n, err)
	}
	active, _ = s.ListActiveSessions(ctx(), u.ID, now)
	if len(active) != 1 || active[0].ID != a.ID {
		t.Fatalf("after revoke-others: %+v", active)
	}
	if active, _ = s.ListActiveSessions(ctx(), u.ID, now.Add(2*time.Hour)); len(active) != 0 {
		t.Fatal("expired session listed as active")
	}
	_ = c
}

func testAudit(t *testing.T, s store.Store) {
	u := newUser(t, s, "dave@example.com")
	org := store.DefaultOrganizationID
	for i, a := range []string{"auth.login", "auth.login", "users.create", "auth.logout"} {
		outcome := store.OutcomeSuccess
		if i == 0 {
			outcome = store.OutcomeFailure
		}
		e := &store.AuditEntry{OrganizationID: &org, ActorUserID: &u.ID, ActorLabel: u.Email, Action: a, Outcome: outcome,
			IP: "2001:db8::1", Metadata: map[string]any{"i": i}}
		if err := s.InsertAudit(ctx(), e); err != nil {
			t.Fatal(err)
		}
		if e.ID == 0 || e.CreatedAt.IsZero() {
			t.Fatal("InsertAudit did not set ID/CreatedAt")
		}
	}
	all, err := s.ListAudit(ctx(), store.AuditFilter{OrganizationID: org})
	if err != nil || len(all) != 4 || all[0].Action != "auth.logout" {
		t.Fatalf("ListAudit newest-first: %+v %v", all, err)
	}
	if all[3].IP != "2001:db8::1" || all[3].Metadata["i"] != float64(0) {
		t.Fatalf("fields not round-tripped: %+v", all[3])
	}
	logins, _ := s.ListAudit(ctx(), store.AuditFilter{OrganizationID: org, ActionPrefix: "auth.login"})
	if len(logins) != 2 {
		t.Fatalf("prefix filter: %d", len(logins))
	}
	failed, _ := s.ListAudit(ctx(), store.AuditFilter{OrganizationID: org, Outcome: store.OutcomeFailure})
	if len(failed) != 1 {
		t.Fatalf("outcome filter: %d", len(failed))
	}
	page, _ := s.ListAudit(ctx(), store.AuditFilter{OrganizationID: org, BeforeID: all[1].ID, Limit: 1})
	if len(page) != 1 || page[0].ID != all[2].ID {
		t.Fatalf("keyset paging: %+v", page)
	}
	mine, _ := s.ListAudit(ctx(), store.AuditFilter{ActorUserID: u.ID, Limit: 2})
	if len(mine) != 2 {
		t.Fatalf("actor filter + limit: %d", len(mine))
	}
}

func testSettings(t *testing.T, s store.Store) {
	if _, err := s.GetSetting(ctx(), "panel.mode"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing setting: %v", err)
	}
	if err := s.PutSetting(ctx(), "panel.mode", json.RawMessage(`"personal"`), nil); err != nil {
		t.Fatal(err)
	}
	if err := s.PutSetting(ctx(), "panel.mode", json.RawMessage(`"provider"`), nil); err != nil {
		t.Fatal(err)
	}
	v, err := s.GetSetting(ctx(), "panel.mode")
	var mode string
	if err != nil || json.Unmarshal(v, &mode) != nil || mode != "provider" {
		t.Fatalf("GetSetting = %s %v", v, err)
	}
}

func testListUsersPaging(t *testing.T, s store.Store) {
	for _, e := range []string{"u1@example.com", "u2@example.com", "zed@other.org"} {
		newUser(t, s, e)
		time.Sleep(2 * time.Millisecond) // distinct created_at for stable ordering
	}
	page, total, err := s.ListUsers(ctx(), store.UserFilter{OrganizationID: store.DefaultOrganizationID, Limit: 2})
	if err != nil || total != 3 || len(page) != 2 || page[0].Email != "u1@example.com" {
		t.Fatalf("page1: %d %d %v", total, len(page), err)
	}
	page, _, _ = s.ListUsers(ctx(), store.UserFilter{OrganizationID: store.DefaultOrganizationID, Limit: 2, Offset: 2})
	if len(page) != 1 || page[0].Email != "zed@other.org" {
		t.Fatalf("page2: %+v", page)
	}
	page, total, _ = s.ListUsers(ctx(), store.UserFilter{OrganizationID: store.DefaultOrganizationID, Query: "EXAMPLE"})
	if total != 2 || len(page) != 2 {
		t.Fatalf("query filter: total=%d", total)
	}
	_, total, _ = s.ListUsers(ctx(), store.UserFilter{OrganizationID: auth.NewID()})
	if total != 0 {
		t.Fatal("users leaked across organizations")
	}
}
