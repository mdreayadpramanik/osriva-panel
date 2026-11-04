// Package customer implements the Customer Panel: customer accounts that are
// separate from operator users, their own sessions, and access to the VPS they
// own only (docs/adr/0015-customer-panel.md).
package customer

import (
	"context"
	"errors"
	"log/slog"
	"net/mail"
	"strings"
	"time"

	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/auth"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/identity"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/provider"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/ratelimit"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/rbac"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/reqmeta"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/secrets"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/store"
)

const (
	SetupTTL      = 72 * time.Hour
	SessionTTL    = 12 * time.Hour
	IdleTimeout   = 2 * time.Hour
	touchInterval = time.Minute
)

type Options struct {
	Store    store.Store
	Identity *identity.Service
	Keys     *secrets.Keyring
	Provider *provider.Service
	Log      *slog.Logger
	// LoginLimiter limits login attempts per IP; Lockout locks an e-mail address.
	LoginLimiter ratelimit.Limiter
	Lockout      ratelimit.Locker
}

type Service struct {
	store    store.Store
	id       *identity.Service
	keys     *secrets.Keyring
	provider *provider.Service
	log      *slog.Logger
	limiter  ratelimit.Limiter
	lockout  ratelimit.Locker
}

func New(o Options) *Service {
	s := &Service{store: o.Store, id: o.Identity, keys: o.Keys, provider: o.Provider, log: o.Log, limiter: o.LoginLimiter, lockout: o.Lockout}
	if s.log == nil {
		s.log = slog.Default()
	}
	if s.limiter == nil {
		s.limiter = ratelimit.NewKeyed(0.2, 10)
	}
	if s.lockout == nil {
		s.lockout = ratelimit.NewLockout(10, 15*time.Minute, 15*time.Minute)
	}
	return s
}

// Principal is an authenticated customer. It carries no operator permissions.
type Principal struct {
	Customer *store.Customer
	Session  *store.CustomerSession
}

func verr(field, msg string) error { return &identity.ValidationError{Field: field, Message: msg} }

func normalizeEmail(e string) string { return strings.ToLower(strings.TrimSpace(e)) }

func (s *Service) audit(ctx context.Context, actor *store.User, action, id string, outcome store.AuditOutcome, md map[string]any) {
	s.id.Audit(ctx, actor, action, "customer", id, outcome, md)
}

// ---- operator side ----

// View is a customer with the number of VPS it owns.
type View struct {
	Customer store.Customer
	VPS      []store.VPS
}

func (s *Service) List(ctx context.Context, actor *identity.Principal) ([]View, error) {
	if err := s.id.Require(ctx, actor, rbac.CustomersRead); err != nil {
		return nil, err
	}
	list, err := s.store.ListCustomers(ctx, actor.User.OrganizationID)
	if err != nil {
		return nil, err
	}
	out := make([]View, 0, len(list))
	for _, c := range list {
		v, err := s.store.ListCustomerVPS(ctx, c.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, View{Customer: c, VPS: v})
	}
	return out, nil
}

type Input struct {
	Email string `json:"email"`
	Name  string `json:"name"`
}

// Created is shown to the operator once: the raw setup token goes into the link.
type Created struct {
	Customer   store.Customer
	SetupToken string
}

func (s *Service) newSetup() (string, []byte, time.Time, error) {
	token, hash, err := auth.NewSessionToken()
	return token, hash, s.id.Now().Add(SetupTTL), err
}

func (s *Service) Create(ctx context.Context, actor *identity.Principal, in Input) (*Created, error) {
	if err := s.id.Require(ctx, actor, rbac.CustomersWrite); err != nil {
		return nil, err
	}
	email := normalizeEmail(in.Email)
	if a, err := mail.ParseAddress(email); err != nil || a.Address != email || len(email) > 320 {
		return nil, verr("email", "must be a valid e-mail address")
	}
	name := strings.TrimSpace(in.Name)
	if name == "" || len(name) > 200 || strings.ContainsAny(name, "\r\n\t\x00") {
		return nil, verr("name", "must be 1-200 characters on one line")
	}
	token, hash, exp, err := s.newSetup()
	if err != nil {
		return nil, err
	}
	c := &store.Customer{ID: auth.NewID(), OrganizationID: actor.User.OrganizationID, Email: email, Name: name, Status: store.CustomerActive,
		SetupExpiresAt: &exp, CreatedBy: &actor.User.ID}
	if err := s.store.CreateCustomer(ctx, c, hash); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return nil, verr("email", "is already used by another customer")
		}
		return nil, err
	}
	s.audit(ctx, actor.User, "customers.create", c.ID, store.OutcomeSuccess, map[string]any{"email": email})
	return &Created{Customer: *c, SetupToken: token}, nil
}

func (s *Service) inOrg(ctx context.Context, actor *identity.Principal, id string) (*store.Customer, error) {
	c, err := s.store.GetCustomer(ctx, id)
	if err != nil {
		return nil, err
	}
	if c.OrganizationID != actor.User.OrganizationID {
		return nil, store.ErrNotFound
	}
	return c, nil
}

// NewSetupLink issues a fresh one-time link (e.g. a forgotten password) and
// signs the customer out everywhere.
func (s *Service) NewSetupLink(ctx context.Context, actor *identity.Principal, id string) (*Created, error) {
	if err := s.id.Require(ctx, actor, rbac.CustomersWrite); err != nil {
		return nil, err
	}
	if err := s.id.RequireRecentReauth(actor); err != nil {
		return nil, err
	}
	c, err := s.inOrg(ctx, actor, id)
	if err != nil {
		return nil, err
	}
	token, hash, exp, err := s.newSetup()
	if err != nil {
		return nil, err
	}
	if err := s.store.SetCustomerSetupToken(ctx, c.ID, hash, exp); err != nil {
		return nil, err
	}
	_ = s.store.RevokeCustomerSessions(ctx, c.ID, "", s.id.Now())
	s.audit(ctx, actor.User, "customers.setup_link", c.ID, store.OutcomeSuccess, map[string]any{"email": c.Email})
	c.SetupExpiresAt = &exp
	return &Created{Customer: *c, SetupToken: token}, nil
}

func (s *Service) SetEnabled(ctx context.Context, actor *identity.Principal, id string, enabled bool) (*store.Customer, error) {
	if err := s.id.Require(ctx, actor, rbac.CustomersWrite); err != nil {
		return nil, err
	}
	c, err := s.inOrg(ctx, actor, id)
	if err != nil {
		return nil, err
	}
	status := store.CustomerActive
	if !enabled {
		status = store.CustomerDisabled
	}
	if err := s.store.SetCustomerStatus(ctx, c.ID, status); err != nil {
		return nil, err
	}
	if !enabled {
		_ = s.store.RevokeCustomerSessions(ctx, c.ID, "", s.id.Now())
	}
	s.audit(ctx, actor.User, "customers.update", c.ID, store.OutcomeSuccess, map[string]any{"email": c.Email, "enabled": enabled})
	return s.store.GetCustomer(ctx, c.ID)
}

// ---- customer side ----

var (
	ErrInvalidCredentials = identity.ErrInvalidCredentials
	ErrUnauthenticated    = identity.ErrUnauthenticated
)

type LoginResult struct {
	Token     string
	Principal *Principal
}

func (s *Service) Login(ctx context.Context, email, password string) (*LoginResult, error) {
	email = normalizeEmail(email)
	meta := reqmeta.From(ctx)
	fail := func(c *store.Customer, reason string, err error) (*LoginResult, error) {
		id := ""
		if c != nil {
			id = c.ID
		}
		s.audit(ctx, nil, "customer.login", id, store.OutcomeFailure, map[string]any{"email": email, "reason": reason})
		return nil, err
	}
	if !s.limiter.Allow("customer:" + meta.IP) {
		return fail(nil, "ip_rate_limited", identity.ErrRateLimited)
	}
	if email == "" || password == "" || len(password) > auth.MaxPasswordLen {
		return fail(nil, "missing_credentials", ErrInvalidCredentials)
	}
	key := "customer:" + email
	if s.lockout.Locked(key) {
		return fail(nil, "account_locked", identity.ErrRateLimited)
	}
	c, err := s.store.GetCustomerByEmail(ctx, email)
	if errors.Is(err, store.ErrNotFound) {
		_, _, _ = auth.VerifyPassword(password, s.id.DummyHash(), s.id.Argon2())
		s.lockout.Fail(key)
		return fail(nil, "unknown_customer", ErrInvalidCredentials)
	}
	if err != nil {
		return nil, err
	}
	hash := c.PasswordHash
	if hash == "" {
		hash = s.id.DummyHash() // the setup link was never used
	}
	ok, _, _ := auth.VerifyPassword(password, hash, s.id.Argon2())
	if !ok || c.PasswordHash == "" {
		s.lockout.Fail(key)
		return fail(c, "bad_password", ErrInvalidCredentials)
	}
	if c.Status != store.CustomerActive {
		return fail(c, "disabled", ErrInvalidCredentials)
	}
	s.lockout.Reset(key)
	token, th, err := auth.NewSessionToken()
	if err != nil {
		return nil, err
	}
	now := s.id.Now()
	sess := &store.CustomerSession{ID: auth.NewID(), CustomerID: c.ID, TokenHash: th, IP: meta.IP, UserAgent: truncate(meta.UserAgent, 512),
		CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(SessionTTL)}
	if err := s.store.CreateCustomerSession(ctx, sess); err != nil {
		return nil, err
	}
	_ = s.store.TouchCustomerLogin(ctx, c.ID, now)
	s.audit(ctx, nil, "customer.login", c.ID, store.OutcomeSuccess, map[string]any{"email": email, "session_id": sess.ID})
	return &LoginResult{Token: token, Principal: &Principal{Customer: c, Session: sess}}, nil
}

func truncate(v string, n int) string {
	if len(v) > n {
		return v[:n]
	}
	return v
}

// Setup sets the first (or a reset) password with a one-time link token.
func (s *Service) Setup(ctx context.Context, token, password string) error {
	if !s.limiter.Allow("customer-setup:" + reqmeta.From(ctx).IP) {
		return identity.ErrRateLimited
	}
	if msg := auth.ValidatePasswordPolicy(password, ""); msg != "" {
		return verr("password", msg)
	}
	if token == "" || len(token) > 128 {
		return &identity.PolicyError{Message: "the setup link is invalid, expired or already used"}
	}
	hash, err := auth.HashPassword(password, s.id.Argon2())
	if err != nil {
		return err
	}
	c, err := s.store.ConsumeCustomerSetup(ctx, auth.HashToken(token), hash, s.id.Now())
	if errors.Is(err, store.ErrNotFound) {
		s.audit(ctx, nil, "customer.setup", "", store.OutcomeFailure, map[string]any{"reason": "invalid_token"})
		return &identity.PolicyError{Message: "the setup link is invalid, expired or already used"}
	}
	if err != nil {
		return err
	}
	_ = s.store.RevokeCustomerSessions(ctx, c.ID, "", s.id.Now())
	s.audit(ctx, nil, "customer.setup", c.ID, store.OutcomeSuccess, map[string]any{"email": c.Email})
	return nil
}

func (s *Service) Authenticate(ctx context.Context, token string) (*Principal, error) {
	if token == "" || len(token) > 128 {
		return nil, ErrUnauthenticated
	}
	sess, err := s.store.GetCustomerSessionByTokenHash(ctx, auth.HashToken(token))
	if errors.Is(err, store.ErrNotFound) {
		return nil, ErrUnauthenticated
	}
	if err != nil {
		return nil, err
	}
	now := s.id.Now()
	if sess.RevokedAt != nil || !now.Before(sess.ExpiresAt) {
		return nil, ErrUnauthenticated
	}
	if now.Sub(sess.LastSeenAt) > IdleTimeout {
		_ = s.store.RevokeCustomerSession(ctx, sess.ID, now)
		return nil, ErrUnauthenticated
	}
	c, err := s.store.GetCustomer(ctx, sess.CustomerID)
	if err != nil || c.Status != store.CustomerActive {
		return nil, ErrUnauthenticated
	}
	if now.Sub(sess.LastSeenAt) > touchInterval {
		if s.store.TouchCustomerSession(ctx, sess.ID, now) == nil {
			sess.LastSeenAt = now
		}
	}
	return &Principal{Customer: c, Session: sess}, nil
}

func (s *Service) Logout(ctx context.Context, p *Principal) error {
	if err := s.store.RevokeCustomerSession(ctx, p.Session.ID, s.id.Now()); err != nil {
		return err
	}
	s.audit(ctx, nil, "customer.logout", p.Customer.ID, store.OutcomeSuccess, map[string]any{"email": p.Customer.Email})
	return nil
}

// ChangePassword checks the current password and signs out the other sessions.
func (s *Service) ChangePassword(ctx context.Context, p *Principal, current, next string) error {
	if ok, _, _ := auth.VerifyPassword(current, p.Customer.PasswordHash, s.id.Argon2()); !ok {
		s.audit(ctx, nil, "customer.password", p.Customer.ID, store.OutcomeFailure, map[string]any{"reason": "bad_password"})
		return verr("current_password", "is wrong")
	}
	if msg := auth.ValidatePasswordPolicy(next, p.Customer.Email); msg != "" {
		return verr("password", msg)
	}
	hash, err := auth.HashPassword(next, s.id.Argon2())
	if err != nil {
		return err
	}
	if err := s.store.SetCustomerPassword(ctx, p.Customer.ID, hash); err != nil {
		return err
	}
	_ = s.store.RevokeCustomerSessions(ctx, p.Customer.ID, p.Session.ID, s.id.Now())
	s.audit(ctx, nil, "customer.password", p.Customer.ID, store.OutcomeSuccess, map[string]any{"email": p.Customer.Email})
	return nil
}

func (s *Service) CSRFToken(p *Principal) string { return s.keys.CSRFToken(p.Session.TokenHash) }

func (s *Service) VerifyCSRF(p *Principal, token string) bool {
	return s.keys.VerifyCSRF(p.Session.TokenHash, token)
}

// ---- the customer's VPS ----

func (s *Service) ListVPS(ctx context.Context, p *Principal) ([]store.VPS, error) {
	return s.provider.CustomerVPS(ctx, p.Customer.ID)
}

func (s *Service) GetVPS(ctx context.Context, p *Principal, id string) (*store.VPS, error) {
	return s.provider.GetCustomerVPS(ctx, p.Customer.ID, id)
}

func (s *Service) VPSAction(ctx context.Context, p *Principal, id, action string) (*store.VPS, error) {
	return s.provider.CustomerAction(ctx, p.Customer.ID, p.Customer.Email, id, action)
}
