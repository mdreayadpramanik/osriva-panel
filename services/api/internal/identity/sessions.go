package identity

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/osriva/osriva-panel/services/api/internal/auth"
	"github.com/osriva/osriva-panel/services/api/internal/reqmeta"
	"github.com/osriva/osriva-panel/services/api/internal/store"
)

type LoginInput struct {
	Email    string
	Password string
	TOTPCode string
}

type LoginResult struct {
	Token     string // raw session token; only ever sent to the client
	Principal *Principal
}

// touchInterval limits how often last_seen_at is written.
const touchInterval = time.Minute

func normalizeEmail(e string) string { return strings.ToLower(strings.TrimSpace(e)) }

func (s *Service) Login(ctx context.Context, in LoginInput) (*LoginResult, error) {
	email := normalizeEmail(in.Email)
	meta := reqmeta.From(ctx)
	fail := func(u *store.User, reason string, err error) (*LoginResult, error) {
		s.audit(ctx, auditEvent{actor: u, action: "auth.login", outcome: store.OutcomeFailure,
			metadata: map[string]any{"email": truncate(email, 320), "reason": reason}})
		return nil, err
	}

	if !s.ipLimiter.Allow(meta.IP) {
		return fail(nil, "ip_rate_limited", ErrRateLimited)
	}
	if email == "" || in.Password == "" || len(in.Password) > auth.MaxPasswordLen {
		return fail(nil, "missing_credentials", ErrInvalidCredentials)
	}
	if s.lockout.Locked(email) {
		return fail(nil, "account_locked", ErrRateLimited)
	}

	u, err := s.store.GetUserByEmail(ctx, email)
	if errors.Is(err, store.ErrNotFound) {
		_, _, _ = auth.VerifyPassword(in.Password, s.dummyHash, s.argon2)
		s.lockout.Fail(email)
		return fail(nil, "unknown_user", ErrInvalidCredentials)
	}
	if err != nil {
		return nil, err
	}
	ok, rehash, err := auth.VerifyPassword(in.Password, u.PasswordHash, s.argon2)
	if err != nil {
		s.log.Error("stored password hash is malformed", "user_id", u.ID, "err", err)
	}
	if !ok {
		s.lockout.Fail(email)
		return fail(u, "bad_password", ErrInvalidCredentials)
	}
	if u.Status != store.UserActive {
		return fail(u, "account_disabled", ErrInvalidCredentials)
	}

	now := s.now()
	if u.TOTPEnabled {
		if strings.TrimSpace(in.TOTPCode) == "" {
			return nil, ErrMFARequired
		}
		step, err := s.checkTOTP(u, in.TOTPCode, now)
		if err != nil {
			s.lockout.Fail(email)
			return fail(u, "bad_totp", err)
		}
		if err := s.store.UpdateUser(ctx, u.ID, store.UserUpdate{TOTPLastStep: &step}); err != nil {
			return nil, err
		}
	}

	upd := store.UserUpdate{LastLoginAt: &now}
	if rehash {
		if h, err := auth.HashPassword(in.Password, s.argon2); err == nil {
			upd.PasswordHash = &h
		}
	}
	if err := s.store.UpdateUser(ctx, u.ID, upd); err != nil {
		return nil, err
	}
	u.LastLoginAt = &now

	token, hash, err := auth.NewSessionToken()
	if err != nil {
		return nil, err
	}
	sess := &store.Session{
		ID: auth.NewID(), UserID: u.ID, TokenHash: hash, CreatedAt: now, ExpiresAt: now.Add(s.sessionTTL),
		LastSeenAt: now, ReauthenticatedAt: &now, IP: meta.IP, UserAgent: truncate(meta.UserAgent, 512),
	}
	if err := s.store.CreateSession(ctx, sess); err != nil {
		return nil, err
	}
	s.lockout.Reset(email)
	p, err := s.loadPrincipal(ctx, u, sess)
	if err != nil {
		return nil, err
	}
	s.audit(ctx, auditEvent{actor: u, action: "auth.login", targetType: "session", targetID: sess.ID,
		outcome: store.OutcomeSuccess, metadata: map[string]any{"mfa": u.TOTPEnabled}})
	return &LoginResult{Token: token, Principal: p}, nil
}

func (s *Service) checkTOTP(u *store.User, code string, now time.Time) (int64, error) {
	secret, err := s.keys.Decrypt(u.TOTPSecretEnc, []byte(u.ID))
	if err != nil {
		s.log.Error("cannot decrypt TOTP secret (wrong OSRIVA_SECRET_KEY?)", "user_id", u.ID)
		return 0, ErrInvalidMFA
	}
	step, ok := auth.ValidateTOTP(string(secret), code, now, u.TOTPLastStep)
	if !ok {
		return 0, ErrInvalidMFA
	}
	return step, nil
}

// Authenticate resolves a raw session token to a principal.
func (s *Service) Authenticate(ctx context.Context, token string) (*Principal, error) {
	if token == "" || len(token) > 128 {
		return nil, ErrUnauthenticated
	}
	sess, err := s.store.GetSessionByTokenHash(ctx, auth.HashToken(token))
	if errors.Is(err, store.ErrNotFound) {
		return nil, ErrUnauthenticated
	}
	if err != nil {
		return nil, err
	}
	now := s.now()
	if sess.RevokedAt != nil || !now.Before(sess.ExpiresAt) {
		return nil, ErrUnauthenticated
	}
	if now.Sub(sess.LastSeenAt) > s.idleTimeout {
		_ = s.store.RevokeSession(ctx, sess.ID, now)
		return nil, ErrUnauthenticated
	}
	u, err := s.store.GetUserByID(ctx, sess.UserID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, ErrUnauthenticated
	}
	if err != nil {
		return nil, err
	}
	if u.Status != store.UserActive {
		return nil, ErrUnauthenticated
	}
	if now.Sub(sess.LastSeenAt) > touchInterval {
		if err := s.store.TouchSession(ctx, sess.ID, now); err == nil {
			sess.LastSeenAt = now
		}
	}
	return s.loadPrincipal(ctx, u, sess)
}

func (s *Service) Logout(ctx context.Context, p *Principal) error {
	if err := s.store.RevokeSession(ctx, p.Session.ID, s.now()); err != nil {
		return err
	}
	s.audit(ctx, auditEvent{actor: p.User, action: "auth.logout", targetType: "session", targetID: p.Session.ID, outcome: store.OutcomeSuccess})
	return nil
}

// verifySecondFactor checks the password and, when enabled, a TOTP code.
func (s *Service) verifySecondFactor(ctx context.Context, p *Principal, password, totpCode, action string) error {
	if !s.ipLimiter.Allow(reqmeta.From(ctx).IP) || s.lockout.Locked(p.User.Email) {
		return ErrRateLimited
	}
	u, err := s.store.GetUserByID(ctx, p.User.ID)
	if err != nil {
		return err
	}
	ok, _, _ := auth.VerifyPassword(password, u.PasswordHash, s.argon2)
	if !ok {
		s.lockout.Fail(u.Email)
		s.audit(ctx, auditEvent{actor: u, action: action, outcome: store.OutcomeFailure, metadata: map[string]any{"reason": "bad_password"}})
		return ErrInvalidCredentials
	}
	if u.TOTPEnabled {
		if strings.TrimSpace(totpCode) == "" {
			return ErrMFARequired
		}
		step, err := s.checkTOTP(u, totpCode, s.now())
		if err != nil {
			s.lockout.Fail(u.Email)
			s.audit(ctx, auditEvent{actor: u, action: action, outcome: store.OutcomeFailure, metadata: map[string]any{"reason": "bad_totp"}})
			return err
		}
		if err := s.store.UpdateUser(ctx, u.ID, store.UserUpdate{TOTPLastStep: &step}); err != nil {
			return err
		}
	}
	return nil
}

// Reauthenticate confirms the user's identity and opens the sudo window.
func (s *Service) Reauthenticate(ctx context.Context, p *Principal, password, totpCode string) error {
	if err := s.verifySecondFactor(ctx, p, password, totpCode, "auth.reauth"); err != nil {
		return err
	}
	now := s.now()
	if err := s.store.MarkSessionReauthenticated(ctx, p.Session.ID, now); err != nil {
		return err
	}
	p.Session.ReauthenticatedAt = &now
	s.audit(ctx, auditEvent{actor: p.User, action: "auth.reauth", outcome: store.OutcomeSuccess})
	return nil
}

// ChangePassword sets a new password and revokes every other session.
func (s *Service) ChangePassword(ctx context.Context, p *Principal, current, next string) error {
	if reason := auth.ValidatePasswordPolicy(next, p.User.Email); reason != "" {
		return &ValidationError{Field: "new_password", Message: reason}
	}
	if current == next {
		return &ValidationError{Field: "new_password", Message: "must differ from the current password"}
	}
	u, err := s.store.GetUserByID(ctx, p.User.ID)
	if err != nil {
		return err
	}
	if ok, _, _ := auth.VerifyPassword(current, u.PasswordHash, s.argon2); !ok {
		s.lockout.Fail(u.Email)
		s.audit(ctx, auditEvent{actor: u, action: "auth.password_change", outcome: store.OutcomeFailure})
		return &ValidationError{Field: "current_password", Message: "is incorrect"}
	}
	h, err := auth.HashPassword(next, s.argon2)
	if err != nil {
		return err
	}
	if err := s.store.UpdateUser(ctx, u.ID, store.UserUpdate{PasswordHash: &h}); err != nil {
		return err
	}
	n, err := s.store.RevokeUserSessions(ctx, u.ID, p.Session.ID, s.now())
	if err != nil {
		return err
	}
	s.audit(ctx, auditEvent{actor: u, action: "auth.password_change", outcome: store.OutcomeSuccess,
		metadata: map[string]any{"revoked_sessions": n}})
	return nil
}

func (s *Service) ListSessions(ctx context.Context, userID string) ([]store.Session, error) {
	return s.store.ListActiveSessions(ctx, userID, s.now())
}

// RevokeOwnSession revokes one of the principal's own sessions.
func (s *Service) RevokeOwnSession(ctx context.Context, p *Principal, sessionID string) error {
	sessions, err := s.ListSessions(ctx, p.User.ID)
	if err != nil {
		return err
	}
	for _, x := range sessions {
		if x.ID == sessionID {
			if err := s.store.RevokeSession(ctx, sessionID, s.now()); err != nil {
				return err
			}
			s.audit(ctx, auditEvent{actor: p.User, action: "auth.session_revoke", targetType: "session", targetID: sessionID, outcome: store.OutcomeSuccess})
			return nil
		}
	}
	return store.ErrNotFound
}

func (s *Service) RevokeOtherSessions(ctx context.Context, p *Principal) (int, error) {
	n, err := s.store.RevokeUserSessions(ctx, p.User.ID, p.Session.ID, s.now())
	if err != nil {
		return 0, err
	}
	s.audit(ctx, auditEvent{actor: p.User, action: "auth.session_revoke_others", outcome: store.OutcomeSuccess, metadata: map[string]any{"count": n}})
	return n, nil
}

// LoginHistory returns the principal's own authentication events.
func (s *Service) LoginHistory(ctx context.Context, p *Principal, limit int) ([]store.AuditEntry, error) {
	return s.store.ListAudit(ctx, store.AuditFilter{ActorUserID: p.User.ID, ActionPrefix: "auth.", Limit: clampLimit(limit, 50, 200)})
}

func clampLimit(n, def, max int) int {
	if n <= 0 {
		return def
	}
	if n > max {
		return max
	}
	return n
}

// ---- MFA (TOTP) ----

type TOTPEnrollment struct {
	Secret string
	URI    string
}

// BeginTOTP generates and stores (encrypted, not yet enabled) a new TOTP secret.
func (s *Service) BeginTOTP(ctx context.Context, p *Principal) (*TOTPEnrollment, error) {
	if p.User.TOTPEnabled {
		return nil, &PolicyError{Message: "two-factor authentication is already enabled; disable it first"}
	}
	secret, err := auth.GenerateTOTPSecret()
	if err != nil {
		return nil, err
	}
	enc, err := s.keys.Encrypt([]byte(secret), []byte(p.User.ID))
	if err != nil {
		return nil, err
	}
	var zero int64
	if err := s.store.UpdateUser(ctx, p.User.ID, store.UserUpdate{TOTPSecretEnc: &enc, TOTPLastStep: &zero}); err != nil {
		return nil, err
	}
	return &TOTPEnrollment{Secret: secret, URI: auth.TOTPURI("OSRIVA Panel", p.User.Email, secret)}, nil
}

// EnableTOTP activates the pending secret after the user proves possession.
func (s *Service) EnableTOTP(ctx context.Context, p *Principal, code string) error {
	u, err := s.store.GetUserByID(ctx, p.User.ID)
	if err != nil {
		return err
	}
	if u.TOTPEnabled {
		return &PolicyError{Message: "two-factor authentication is already enabled"}
	}
	if len(u.TOTPSecretEnc) == 0 {
		return &PolicyError{Message: "start two-factor setup first"}
	}
	step, err := s.checkTOTP(u, code, s.now())
	if err != nil {
		return &ValidationError{Field: "code", Message: "is not valid; check your authenticator's clock"}
	}
	enabled := true
	if err := s.store.UpdateUser(ctx, u.ID, store.UserUpdate{TOTPEnabled: &enabled, TOTPLastStep: &step}); err != nil {
		return err
	}
	p.User.TOTPEnabled = true
	s.audit(ctx, auditEvent{actor: u, action: "auth.mfa_enable", outcome: store.OutcomeSuccess, metadata: map[string]any{"method": "totp"}})
	return nil
}

// DisableTOTP removes TOTP after re-verifying the password and a current code.
func (s *Service) DisableTOTP(ctx context.Context, p *Principal, password, code string) error {
	if !p.User.TOTPEnabled {
		return &PolicyError{Message: "two-factor authentication is not enabled"}
	}
	if err := s.verifySecondFactor(ctx, p, password, code, "auth.mfa_disable"); err != nil {
		return err
	}
	empty, disabled := []byte{}, false
	if err := s.store.UpdateUser(ctx, p.User.ID, store.UserUpdate{TOTPSecretEnc: &empty, TOTPEnabled: &disabled}); err != nil {
		return err
	}
	p.User.TOTPEnabled = false
	s.audit(ctx, auditEvent{actor: p.User, action: "auth.mfa_disable", outcome: store.OutcomeSuccess})
	return nil
}
