package identity

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/osriva/osriva-panel/services/api/internal/auth"
	"github.com/osriva/osriva-panel/services/api/internal/rbac"
	"github.com/osriva/osriva-panel/services/api/internal/reqmeta"
	"github.com/osriva/osriva-panel/services/api/internal/secrets"
	"github.com/osriva/osriva-panel/services/api/internal/store"
	"github.com/osriva/osriva-panel/services/api/internal/store/memstore"
)

const pw = "a long enough password"

type fixture struct {
	t     *testing.T
	svc   *Service
	store *memstore.Store
	now   time.Time
	ctx   context.Context
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{t: t, store: memstore.New(), now: time.Unix(1_800_000_000, 0).UTC()}
	keys, err := secrets.NewKeyring(bytes.Repeat([]byte{1}, 32))
	if err != nil {
		t.Fatal(err)
	}
	f.svc, err = New(Options{
		Store: f.store, Keys: keys, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Argon2: auth.Argon2Params{Memory: 1024, Iterations: 1, Parallelism: 1, SaltLen: 16, KeyLen: 32},
		Now:    func() time.Time { return f.now },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.svc.SyncRBAC(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.ctx = reqmeta.With(context.Background(), reqmeta.Meta{IP: "198.51.100.7", UserAgent: "go-test", RequestID: "req-1"})
	return f
}

func (f *fixture) user(email, role string) *store.User {
	f.t.Helper()
	u, err := f.svc.BootstrapUser(f.ctx, email, "User "+email, pw, role, "test")
	if err != nil {
		f.t.Fatalf("bootstrap %s: %v", email, err)
	}
	return u
}

func (f *fixture) login(email string) *Principal {
	f.t.Helper()
	res, err := f.svc.Login(f.ctx, LoginInput{Email: email, Password: pw})
	if err != nil {
		f.t.Fatalf("login %s: %v", email, err)
	}
	return res.Principal
}

func (f *fixture) loginToken(email string) string {
	f.t.Helper()
	res, err := f.svc.Login(f.ctx, LoginInput{Email: email, Password: pw})
	if err != nil {
		f.t.Fatalf("login %s: %v", email, err)
	}
	return res.Token
}

func (f *fixture) auditActions(outcome store.AuditOutcome) []string {
	entries, _ := f.store.ListAudit(context.Background(), store.AuditFilter{Outcome: outcome, Limit: 500})
	var out []string
	for _, e := range entries {
		out = append(out, e.Action)
	}
	return out
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func TestLoginAndAuthenticate(t *testing.T) {
	f := newFixture(t)
	f.user("owner@example.com", rbac.RoleOwner)
	res, err := f.svc.Login(f.ctx, LoginInput{Email: "  OWNER@example.com ", Password: pw})
	if err != nil {
		t.Fatal(err)
	}
	if res.Token == "" || !res.Principal.Can(rbac.SettingsWrite) {
		t.Fatalf("bad login result: %+v", res.Principal)
	}
	if res.Principal.Session.IP != "198.51.100.7" {
		t.Fatalf("session ip = %q", res.Principal.Session.IP)
	}
	p, err := f.svc.Authenticate(f.ctx, res.Token)
	if err != nil || p.User.Email != "owner@example.com" {
		t.Fatalf("authenticate: %v", err)
	}
	if _, err := f.svc.Authenticate(f.ctx, "garbage"); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("garbage token: %v", err)
	}
	entries, _ := f.store.ListAudit(context.Background(), store.AuditFilter{ActionPrefix: "auth.login"})
	if len(entries) != 1 || entries[0].Outcome != store.OutcomeSuccess || entries[0].IP != "198.51.100.7" || entries[0].RequestID != "req-1" {
		t.Fatalf("login audit: %+v", entries)
	}
}

func TestLoginFailuresAreIndistinguishable(t *testing.T) {
	f := newFixture(t)
	u := f.user("a@example.com", rbac.RoleMember)
	_, errUnknown := f.svc.Login(f.ctx, LoginInput{Email: "nobody@example.com", Password: pw})
	_, errBad := f.svc.Login(f.ctx, LoginInput{Email: "a@example.com", Password: "wrong password!!"})
	disabled := store.UserDisabled
	_ = f.store.UpdateUser(context.Background(), u.ID, store.UserUpdate{Status: &disabled})
	_, errDisabled := f.svc.Login(f.ctx, LoginInput{Email: "a@example.com", Password: pw})
	for name, err := range map[string]error{"unknown": errUnknown, "bad": errBad, "disabled": errDisabled} {
		if !errors.Is(err, ErrInvalidCredentials) {
			t.Errorf("%s: got %v, want ErrInvalidCredentials", name, err)
		}
	}
	if n := len(f.auditActions(store.OutcomeFailure)); n != 3 {
		t.Errorf("expected 3 failure audit entries, got %d", n)
	}
}

func TestAccountLockout(t *testing.T) {
	f := newFixture(t)
	f.user("a@example.com", rbac.RoleMember)
	// Spread attempts over IPs so only the account lockout (not the IP limiter) applies.
	for i := range 10 {
		ctx := reqmeta.With(context.Background(), reqmeta.Meta{IP: "203.0.113." + string(rune('0'+i))})
		_, _ = f.svc.Login(ctx, LoginInput{Email: "a@example.com", Password: "wrong password!!"})
	}
	if _, err := f.svc.Login(f.ctx, LoginInput{Email: "a@example.com", Password: pw}); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("correct password during lockout: %v", err)
	}
}

func TestIPRateLimit(t *testing.T) {
	f := newFixture(t)
	var err error
	for range 11 {
		_, err = f.svc.Login(f.ctx, LoginInput{Email: "x@example.com", Password: "wrong password!!"})
	}
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("11th attempt from one IP: %v", err)
	}
}

func TestSessionExpiryIdleAndRevocation(t *testing.T) {
	f := newFixture(t)
	f.user("a@example.com", rbac.RoleMember)

	tok := f.loginToken("a@example.com")
	f.now = f.now.Add(2*time.Hour + time.Second) // idle timeout
	if _, err := f.svc.Authenticate(f.ctx, tok); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("idle session accepted: %v", err)
	}

	tok = f.loginToken("a@example.com")
	for range 13 { // keep it active, but cross the 12h absolute TTL
		f.now = f.now.Add(time.Hour)
		_, _ = f.svc.Authenticate(f.ctx, tok)
	}
	if _, err := f.svc.Authenticate(f.ctx, tok); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("expired session accepted: %v", err)
	}

	tok = f.loginToken("a@example.com")
	p, _ := f.svc.Authenticate(f.ctx, tok)
	if err := f.svc.Logout(f.ctx, p); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Authenticate(f.ctx, tok); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("logged-out session accepted: %v", err)
	}
}

func TestTOTPLifecycle(t *testing.T) {
	f := newFixture(t)
	f.user("a@example.com", rbac.RoleMember)
	p := f.login("a@example.com")

	enr, err := f.svc.BeginTOTP(f.ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	stored, _ := f.store.GetUserByID(context.Background(), p.User.ID)
	if bytes.Contains(stored.TOTPSecretEnc, []byte(enr.Secret)) {
		t.Fatal("TOTP secret stored in plaintext")
	}
	if err := f.svc.EnableTOTP(f.ctx, p, "000000"); err == nil {
		t.Fatal("wrong code enabled TOTP")
	}
	code, _ := auth.TOTPCode(enr.Secret, auth.TOTPStep(f.now))
	if err := f.svc.EnableTOTP(f.ctx, p, code); err != nil {
		t.Fatal(err)
	}

	if _, err := f.svc.Login(f.ctx, LoginInput{Email: "a@example.com", Password: pw}); !errors.Is(err, ErrMFARequired) {
		t.Fatalf("login without code: %v", err)
	}
	// Same step already used to enable: replay must fail.
	if _, err := f.svc.Login(f.ctx, LoginInput{Email: "a@example.com", Password: pw, TOTPCode: code}); !errors.Is(err, ErrInvalidMFA) {
		t.Fatalf("replayed code: %v", err)
	}
	f.now = f.now.Add(30 * time.Second)
	code, _ = auth.TOTPCode(enr.Secret, auth.TOTPStep(f.now))
	res, err := f.svc.Login(f.ctx, LoginInput{Email: "a@example.com", Password: pw, TOTPCode: code})
	if err != nil {
		t.Fatalf("login with fresh code: %v", err)
	}

	f.now = f.now.Add(30 * time.Second)
	code, _ = auth.TOTPCode(enr.Secret, auth.TOTPStep(f.now))
	if err := f.svc.DisableTOTP(f.ctx, res.Principal, "wrong password!!", code); err == nil {
		t.Fatal("disabled TOTP with wrong password")
	}
	if err := f.svc.DisableTOTP(f.ctx, res.Principal, pw, code); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Login(f.ctx, LoginInput{Email: "a@example.com", Password: pw}); err != nil {
		t.Fatalf("login after disabling TOTP: %v", err)
	}
}

func TestRBACAndEscalation(t *testing.T) {
	f := newFixture(t)
	owner := f.user("owner@example.com", rbac.RoleOwner)
	f.user("admin@example.com", rbac.RoleAdmin)
	f.user("auditor@example.com", rbac.RoleAuditor)
	admin := f.login("admin@example.com")
	auditor := f.login("auditor@example.com")

	if _, err := f.svc.CreateUser(f.ctx, auditor, CreateUserInput{Email: "n@example.com", DisplayName: "N", Password: pw}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("auditor created user: %v", err)
	}
	if !contains(f.auditActions(store.OutcomeDenied), "authz.denied") {
		t.Error("denied attempt not audited")
	}
	if _, err := f.svc.CreateUser(f.ctx, admin, CreateUserInput{Email: "n@example.com", DisplayName: "N", Password: pw, RoleKeys: []string{rbac.RoleOwner}}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("admin created an owner: %v", err)
	}
	if !contains(f.auditActions(store.OutcomeDenied), "authz.escalation_blocked") {
		t.Error("escalation attempt not audited")
	}
	v, err := f.svc.CreateUser(f.ctx, admin, CreateUserInput{Email: "n@example.com", DisplayName: "N", Password: pw, RoleKeys: []string{rbac.RoleAuditor}})
	if err != nil || v.Roles[0].Key != rbac.RoleAuditor {
		t.Fatalf("admin creating auditor: %v", err)
	}
	name := "Renamed"
	if _, err := f.svc.UpdateUser(f.ctx, admin, owner.ID, UpdateUserInput{DisplayName: &name}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("admin modified owner: %v", err)
	}
	if _, err := f.svc.CreateUser(f.ctx, admin, CreateUserInput{Email: "n@example.com", DisplayName: "N", Password: pw}); err == nil {
		t.Fatal("duplicate email accepted")
	}
	for _, bad := range []CreateUserInput{
		{Email: "not-an-email", DisplayName: "N", Password: pw},
		{Email: "x@nodomain", DisplayName: "N", Password: pw},
		{Email: "x@example.com", DisplayName: "", Password: pw},
		{Email: "x@example.com", DisplayName: "N", Password: "short"},
		{Email: "x@example.com", DisplayName: "N", Password: pw, RoleKeys: []string{"nope"}},
	} {
		var ve *ValidationError
		if _, err := f.svc.CreateUser(f.ctx, admin, bad); !errors.As(err, &ve) {
			t.Errorf("input %+v: got %v, want ValidationError", bad, err)
		}
	}
}

func TestPrivilegedUpdatesNeedReauthAndKeepAnOwner(t *testing.T) {
	f := newFixture(t)
	f.user("owner@example.com", rbac.RoleOwner)
	member := f.user("m@example.com", rbac.RoleMember)
	owner := f.login("owner@example.com")
	memberTok := f.loginToken("m@example.com")

	f.now = f.now.Add(11 * time.Minute) // leave the sudo window opened by login
	_, _ = f.svc.Authenticate(f.ctx, memberTok)
	disabled := store.UserDisabled
	if _, err := f.svc.UpdateUser(f.ctx, owner, member.ID, UpdateUserInput{Status: &disabled}); !errors.Is(err, ErrReauthRequired) {
		t.Fatalf("privileged update without reauth: %v", err)
	}
	if err := f.svc.Reauthenticate(f.ctx, owner, "wrong password!!", ""); err == nil {
		t.Fatal("reauth with wrong password succeeded")
	}
	if err := f.svc.Reauthenticate(f.ctx, owner, pw, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.UpdateUser(f.ctx, owner, member.ID, UpdateUserInput{Status: &disabled}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Authenticate(f.ctx, memberTok); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal("disabled user's session still valid")
	}

	var pe *PolicyError
	if _, err := f.svc.UpdateUser(f.ctx, owner, owner.User.ID, UpdateUserInput{Status: &disabled}); !errors.As(err, &pe) {
		t.Fatalf("self-disable: %v", err)
	}
	demote := []string{rbac.RoleAdmin}
	if _, err := f.svc.UpdateUser(f.ctx, owner, owner.User.ID, UpdateUserInput{RoleKeys: &demote}); !errors.As(err, &pe) {
		t.Fatalf("last owner demotion: %v", err)
	}
	f.user("owner2@example.com", rbac.RoleOwner)
	if _, err := f.svc.UpdateUser(f.ctx, owner, owner.User.ID, UpdateUserInput{RoleKeys: &demote}); err != nil {
		t.Fatalf("demotion with a second owner present: %v", err)
	}
}

func TestChangePasswordRevokesOtherSessions(t *testing.T) {
	f := newFixture(t)
	f.user("a@example.com", rbac.RoleMember)
	other := f.loginToken("a@example.com")
	p := f.login("a@example.com")
	const next = "an even longer new password"
	var ve *ValidationError
	if err := f.svc.ChangePassword(f.ctx, p, "wrong password!!", next); !errors.As(err, &ve) {
		t.Fatalf("wrong current password: %v", err)
	}
	if err := f.svc.ChangePassword(f.ctx, p, pw, "short"); !errors.As(err, &ve) {
		t.Fatalf("weak new password: %v", err)
	}
	if err := f.svc.ChangePassword(f.ctx, p, pw, next); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Authenticate(f.ctx, other); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal("other session survived password change")
	}
	if _, err := f.svc.Login(f.ctx, LoginInput{Email: "a@example.com", Password: next}); err != nil {
		t.Fatalf("login with new password: %v", err)
	}
}

func TestOrganizationIsolation(t *testing.T) {
	f := newFixture(t)
	f.user("owner@example.com", rbac.RoleOwner)
	owner := f.login("owner@example.com")
	foreign := &store.User{ID: auth.NewID(), OrganizationID: auth.NewID(), Email: "x@other.org", DisplayName: "X", PasswordHash: "x"}
	if err := f.store.CreateUser(context.Background(), foreign, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.GetUser(f.ctx, owner, foreign.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("cross-org read: %v", err)
	}
	users, total, _ := f.svc.ListUsers(f.ctx, owner, "", 0, 0)
	if total != 1 || len(users) != 1 {
		t.Fatalf("cross-org list leaked: %d", total)
	}
}

func TestPanelMode(t *testing.T) {
	f := newFixture(t)
	f.user("owner@example.com", rbac.RoleOwner)
	f.user("admin@example.com", rbac.RoleAdmin)
	owner, admin := f.login("owner@example.com"), f.login("admin@example.com")
	if m, _ := f.svc.PanelMode(f.ctx); m != ModePersonal {
		t.Fatalf("default mode %s", m)
	}
	if err := f.svc.SetPanelMode(f.ctx, admin, ModeProvider); !errors.Is(err, ErrForbidden) {
		t.Fatalf("admin changed mode: %v", err)
	}
	if err := f.svc.SetPanelMode(f.ctx, owner, "bogus"); err == nil {
		t.Fatal("bogus mode accepted")
	}
	if err := f.svc.SetPanelMode(f.ctx, owner, ModeProvider); err != nil {
		t.Fatal(err)
	}
	if m, _ := f.svc.PanelMode(f.ctx); m != ModeProvider {
		t.Fatalf("mode = %s", m)
	}
}
