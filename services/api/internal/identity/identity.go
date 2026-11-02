// Package identity is the OSRIVA authentication service: login, sessions,
// re-authentication, MFA, user management and RBAC enforcement. Every
// security-relevant action is written to the audit log.
package identity

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/osriva/osriva-panel/services/api/internal/auth"
	"github.com/osriva/osriva-panel/services/api/internal/ratelimit"
	"github.com/osriva/osriva-panel/services/api/internal/rbac"
	"github.com/osriva/osriva-panel/services/api/internal/reqmeta"
	"github.com/osriva/osriva-panel/services/api/internal/secrets"
	"github.com/osriva/osriva-panel/services/api/internal/store"
)

var (
	ErrInvalidCredentials = errors.New("invalid email or password")
	ErrMFARequired        = errors.New("a TOTP code is required")
	ErrInvalidMFA         = errors.New("invalid TOTP code")
	ErrRateLimited        = errors.New("too many attempts, try again later")
	ErrUnauthenticated    = errors.New("authentication required")
	ErrForbidden          = errors.New("you do not have permission to perform this action")
	ErrReauthRequired     = errors.New("confirm your password to continue")
)

// ValidationError reports invalid input for a specific field.
type ValidationError struct{ Field, Message string }

func (e *ValidationError) Error() string { return e.Field + ": " + e.Message }

// PolicyError reports a request that is well-formed but violates a safety rule
// (e.g. removing the last owner).
type PolicyError struct{ Message string }

func (e *PolicyError) Error() string { return e.Message }

// Principal is an authenticated caller.
type Principal struct {
	User        *store.User
	Session     *store.Session
	Roles       []store.Role
	Permissions rbac.Set
}

func (p *Principal) Can(perm string) bool { return p != nil && p.Permissions.Has(perm) }

type Options struct {
	Store        store.Store
	Keys         *secrets.Keyring
	Log          *slog.Logger
	SessionTTL   time.Duration
	IdleTimeout  time.Duration
	ReauthWindow time.Duration
	Argon2       auth.Argon2Params
	Now          func() time.Time
	// Login brute-force protection; defaults applied when nil.
	LoginIPLimiter *ratelimit.KeyedLimiter
	AccountLockout *ratelimit.Lockout
}

type Service struct {
	store        store.Store
	keys         *secrets.Keyring
	log          *slog.Logger
	sessionTTL   time.Duration
	idleTimeout  time.Duration
	reauthWindow time.Duration
	argon2       auth.Argon2Params
	now          func() time.Time
	ipLimiter    *ratelimit.KeyedLimiter
	lockout      *ratelimit.Lockout
	dummyHash    string
}

func New(o Options) (*Service, error) {
	if o.Store == nil || o.Keys == nil {
		return nil, errors.New("identity: store and keys are required")
	}
	s := &Service{
		store: o.Store, keys: o.Keys, log: o.Log,
		sessionTTL: o.SessionTTL, idleTimeout: o.IdleTimeout, reauthWindow: o.ReauthWindow,
		argon2: o.Argon2, now: o.Now, ipLimiter: o.LoginIPLimiter, lockout: o.AccountLockout,
	}
	if s.log == nil {
		s.log = slog.Default()
	}
	if s.sessionTTL == 0 {
		s.sessionTTL = 12 * time.Hour
	}
	if s.idleTimeout == 0 {
		s.idleTimeout = 2 * time.Hour
	}
	if s.reauthWindow == 0 {
		s.reauthWindow = 10 * time.Minute
	}
	if s.argon2 == (auth.Argon2Params{}) {
		s.argon2 = auth.DefaultArgon2Params
	}
	if s.now == nil {
		s.now = func() time.Time { return time.Now().UTC() }
	}
	if s.ipLimiter == nil {
		s.ipLimiter = ratelimit.NewKeyed(0.2, 10) // 10 attempts, then one per 5s per IP
	}
	if s.lockout == nil {
		s.lockout = ratelimit.NewLockout(10, 15*time.Minute, 15*time.Minute)
	}
	// Verified against when the account does not exist, so response time
	// does not reveal whether an email is registered.
	h, err := auth.HashPassword("osriva-timing-equalisation-dummy", s.argon2)
	if err != nil {
		return nil, err
	}
	s.dummyHash = h
	return s, nil
}

// SyncRBAC writes the code-defined permission catalog and built-in roles to the store.
func (s *Service) SyncRBAC(ctx context.Context) error {
	if err := s.store.SyncPermissions(ctx, rbac.Catalog); err != nil {
		return fmt.Errorf("sync permissions: %w", err)
	}
	for _, r := range rbac.BuiltinRoles() {
		if err := s.store.UpsertBuiltinRole(ctx, r.Key, r.Name, r.Description, r.Permissions); err != nil {
			return fmt.Errorf("sync role %s: %w", r.Key, err)
		}
	}
	return nil
}

// CSRFToken returns the CSRF token for the principal's session.
func (s *Service) CSRFToken(p *Principal) string { return s.keys.CSRFToken(p.Session.TokenHash) }

func (s *Service) VerifyCSRF(p *Principal, token string) bool {
	return s.keys.VerifyCSRF(p.Session.TokenHash, token)
}

// ---- audit helpers ----

type auditEvent struct {
	actor      *store.User
	action     string
	targetType string
	targetID   string
	outcome    store.AuditOutcome
	metadata   map[string]any
}

func (s *Service) audit(ctx context.Context, ev auditEvent) {
	m := reqmeta.From(ctx)
	e := &store.AuditEntry{
		Action: ev.action, TargetType: ev.targetType, TargetID: ev.targetID, Outcome: ev.outcome,
		IP: m.IP, UserAgent: truncate(m.UserAgent, 512), RequestID: m.RequestID, Metadata: ev.metadata,
	}
	if ev.actor != nil {
		org, id := ev.actor.OrganizationID, ev.actor.ID
		e.OrganizationID, e.ActorUserID, e.ActorLabel = &org, &id, ev.actor.Email
	} else {
		org := store.DefaultOrganizationID
		e.OrganizationID = &org
	}
	// Audit failures must not break the request, but they must be visible.
	if err := s.store.InsertAudit(context.WithoutCancel(ctx), e); err != nil {
		s.log.Error("audit write failed", "action", ev.action, "err", err)
	}
}

// AuditDenied records an authorization failure detected outside the service (e.g. middleware).
func (s *Service) AuditDenied(ctx context.Context, p *Principal, permission string) {
	s.audit(ctx, auditEvent{actor: p.User, action: "authz.denied", outcome: store.OutcomeDenied,
		metadata: map[string]any{"permission": permission}})
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// ---- principal loading ----

func (s *Service) loadPrincipal(ctx context.Context, u *store.User, sess *store.Session) (*Principal, error) {
	roles, err := s.store.GetUserRoles(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	return &Principal{User: u, Session: sess, Roles: roles, Permissions: permissionsOf(roles)}, nil
}

func permissionsOf(roles []store.Role) rbac.Set {
	perms := rbac.NewSet()
	for _, r := range roles {
		for _, p := range r.Permissions {
			perms[p] = struct{}{}
		}
	}
	return perms
}

// Require returns ErrForbidden (and audits the denial) when p lacks perm.
func (s *Service) Require(ctx context.Context, p *Principal, perm string) error {
	if p.Can(perm) {
		return nil
	}
	s.AuditDenied(ctx, p, perm)
	return ErrForbidden
}

// RequireRecentReauth enforces "sudo mode" for dangerous operations.
func (s *Service) RequireRecentReauth(p *Principal) error {
	at := p.Session.ReauthenticatedAt
	if at == nil || s.now().Sub(*at) > s.reauthWindow {
		return ErrReauthRequired
	}
	return nil
}
