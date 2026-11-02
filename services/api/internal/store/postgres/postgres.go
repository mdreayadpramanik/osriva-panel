// Package postgres implements store.Store on PostgreSQL 16+ using pgx.
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/osriva/osriva-panel/services/api/internal/auth"
	"github.com/osriva/osriva-panel/services/api/internal/store"
)

type Store struct {
	pool *pgxpool.Pool
}

var _ store.Store = (*Store)(nil)

func Open(ctx context.Context, databaseURL string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	cfg.ConnConfig.RuntimeParams["application_name"] = "osriva-api"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect to database: %w", err)
	}
	return &Store{pool: pool}, nil
}

func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }
func (s *Store) Close()                         { s.pool.Close() }

// Pool exposes the connection pool for tests and tooling.
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

func mapErr(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return store.ErrNotFound
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		switch pg.Code {
		case "23505": // unique_violation
			return fmt.Errorf("%w: %s", store.ErrConflict, pg.ConstraintName)
		case "23503": // foreign_key_violation
			return fmt.Errorf("%w: %s", store.ErrNotFound, pg.ConstraintName)
		}
	}
	return err
}

// ---- RBAC ----

func (s *Store) SyncPermissions(ctx context.Context, catalog map[string]string) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		keys := make([]string, 0, len(catalog))
		for k, d := range catalog {
			keys = append(keys, k)
			if _, err := tx.Exec(ctx, `INSERT INTO permissions (key, description) VALUES ($1, $2)
				ON CONFLICT (key) DO UPDATE SET description = EXCLUDED.description`, k, d); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, `DELETE FROM permissions WHERE key <> ALL($1)`, keys)
		return err
	})
}

func (s *Store) UpsertBuiltinRole(ctx context.Context, key, name, description string, permissions []string) error {
	return mapErr(pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var id string
		err := tx.QueryRow(ctx, `INSERT INTO roles (id, organization_id, key, name, description, built_in)
			VALUES ($1, NULL, $2, $3, $4, true)
			ON CONFLICT (key) WHERE organization_id IS NULL
			DO UPDATE SET name = EXCLUDED.name, description = EXCLUDED.description
			RETURNING id::text`, auth.NewID(), key, name, description).Scan(&id)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM role_permissions WHERE role_id = $1`, id); err != nil {
			return err
		}
		if len(permissions) == 0 {
			return nil
		}
		_, err = tx.Exec(ctx, `INSERT INTO role_permissions (role_id, permission_key) SELECT $1, unnest($2::text[])`, id, permissions)
		return err
	}))
}

const roleSelect = `SELECT r.id::text, r.organization_id::text, r.key, r.name, r.description, r.built_in,
	COALESCE(array_agg(rp.permission_key ORDER BY rp.permission_key) FILTER (WHERE rp.permission_key IS NOT NULL), '{}')
	FROM roles r LEFT JOIN role_permissions rp ON rp.role_id = r.id`

const roleOrder = ` GROUP BY r.id ORDER BY r.built_in DESC, r.key`

func scanRoles(rows pgx.Rows) ([]store.Role, error) {
	defer rows.Close()
	out := []store.Role{}
	for rows.Next() {
		var r store.Role
		if err := rows.Scan(&r.ID, &r.OrganizationID, &r.Key, &r.Name, &r.Description, &r.BuiltIn, &r.Permissions); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) ListRoles(ctx context.Context, orgID string) ([]store.Role, error) {
	rows, err := s.pool.Query(ctx, roleSelect+` WHERE r.organization_id IS NULL OR r.organization_id = $1`+roleOrder, orgID)
	if err != nil {
		return nil, err
	}
	return scanRoles(rows)
}

func (s *Store) GetRolesByKeys(ctx context.Context, orgID string, keys []string) ([]store.Role, error) {
	rows, err := s.pool.Query(ctx, roleSelect+` WHERE (r.organization_id IS NULL OR r.organization_id = $1) AND r.key = ANY($2)`+roleOrder, orgID, keys)
	if err != nil {
		return nil, err
	}
	return scanRoles(rows)
}

// ---- Users ----

const userCols = `id::text, organization_id::text, email, display_name, password_hash, status, totp_secret_enc,
	totp_enabled, totp_last_step, password_changed_at, last_login_at, created_at, updated_at`

func scanUser(row pgx.Row) (*store.User, error) {
	var u store.User
	var status string
	err := row.Scan(&u.ID, &u.OrganizationID, &u.Email, &u.DisplayName, &u.PasswordHash, &status, &u.TOTPSecretEnc,
		&u.TOTPEnabled, &u.TOTPLastStep, &u.PasswordChangedAt, &u.LastLoginAt, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		return nil, mapErr(err)
	}
	u.Status = store.UserStatus(status)
	return &u, nil
}

func insertUserRoles(ctx context.Context, tx pgx.Tx, userID string, roleIDs []string, grantedBy *string) error {
	if len(roleIDs) == 0 {
		return nil
	}
	_, err := tx.Exec(ctx, `INSERT INTO user_roles (user_id, role_id, granted_by)
		SELECT $1, unnest($2::uuid[]), $3`, userID, roleIDs, grantedBy)
	return err
}

func (s *Store) CreateUser(ctx context.Context, u *store.User, roleIDs []string, grantedBy *string) error {
	if u.Status == "" {
		u.Status = store.UserActive
	}
	return mapErr(pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `INSERT INTO users (id, organization_id, email, display_name, password_hash, status, totp_secret_enc, totp_enabled)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			RETURNING created_at, updated_at, password_changed_at`,
			u.ID, u.OrganizationID, u.Email, u.DisplayName, u.PasswordHash, string(u.Status), u.TOTPSecretEnc, u.TOTPEnabled,
		).Scan(&u.CreatedAt, &u.UpdatedAt, &u.PasswordChangedAt)
		if err != nil {
			return err
		}
		return insertUserRoles(ctx, tx, u.ID, roleIDs, grantedBy)
	}))
}

func (s *Store) GetUserByID(ctx context.Context, id string) (*store.User, error) {
	return scanUser(s.pool.QueryRow(ctx, `SELECT `+userCols+` FROM users WHERE id = $1`, id))
}

func (s *Store) GetUserByEmail(ctx context.Context, email string) (*store.User, error) {
	return scanUser(s.pool.QueryRow(ctx, `SELECT `+userCols+` FROM users WHERE lower(email) = lower($1)`, email))
}

func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

func (s *Store) ListUsers(ctx context.Context, f store.UserFilter) ([]store.User, int, error) {
	where := `organization_id = $1 AND ($2 = '' OR email ILIKE '%' || $2 || '%' OR display_name ILIKE '%' || $2 || '%')`
	q := likeEscape(f.Query)
	var total int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM users WHERE `+where, f.OrganizationID, q).Scan(&total); err != nil {
		return nil, 0, err
	}
	limit := any(nil)
	if f.Limit > 0 {
		limit = f.Limit
	}
	rows, err := s.pool.Query(ctx, `SELECT `+userCols+` FROM users WHERE `+where+
		` ORDER BY created_at, id LIMIT $3 OFFSET $4`, f.OrganizationID, q, limit, f.Offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []store.User{}
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *u)
	}
	return out, total, rows.Err()
}

func (s *Store) UpdateUser(ctx context.Context, id string, upd store.UserUpdate) error {
	sets := []string{"updated_at = now()"}
	args := []any{id}
	add := func(col string, v any) {
		args = append(args, v)
		sets = append(sets, fmt.Sprintf("%s = $%d", col, len(args)))
	}
	if upd.DisplayName != nil {
		add("display_name", *upd.DisplayName)
	}
	if upd.Status != nil {
		add("status", string(*upd.Status))
	}
	if upd.PasswordHash != nil {
		add("password_hash", *upd.PasswordHash)
		sets = append(sets, "password_changed_at = now()")
	}
	if upd.TOTPSecretEnc != nil {
		var v []byte
		if len(*upd.TOTPSecretEnc) > 0 {
			v = *upd.TOTPSecretEnc
		}
		add("totp_secret_enc", v)
	}
	if upd.TOTPEnabled != nil {
		add("totp_enabled", *upd.TOTPEnabled)
	}
	if upd.TOTPLastStep != nil {
		add("totp_last_step", *upd.TOTPLastStep)
	}
	if upd.LastLoginAt != nil {
		add("last_login_at", *upd.LastLoginAt)
	}
	tag, err := s.pool.Exec(ctx, `UPDATE users SET `+strings.Join(sets, ", ")+` WHERE id = $1`, args...)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (s *Store) SetUserRoles(ctx context.Context, userID string, roleIDs []string, grantedBy *string) error {
	return mapErr(pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var locked string
		if err := tx.QueryRow(ctx, `SELECT id::text FROM users WHERE id = $1 FOR UPDATE`, userID).Scan(&locked); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM user_roles WHERE user_id = $1`, userID); err != nil {
			return err
		}
		return insertUserRoles(ctx, tx, userID, roleIDs, grantedBy)
	}))
}

func (s *Store) GetUserRoles(ctx context.Context, userID string) ([]store.Role, error) {
	rows, err := s.pool.Query(ctx, roleSelect+` JOIN user_roles ur ON ur.role_id = r.id WHERE ur.user_id = $1`+roleOrder, userID)
	if err != nil {
		return nil, err
	}
	return scanRoles(rows)
}

func (s *Store) CountActiveUsersWithRole(ctx context.Context, orgID, roleKey string) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT count(DISTINCT u.id) FROM users u
		JOIN user_roles ur ON ur.user_id = u.id JOIN roles r ON r.id = ur.role_id
		WHERE u.organization_id = $1 AND u.status = 'active' AND r.key = $2`, orgID, roleKey).Scan(&n)
	return n, err
}

// ---- Sessions ----

const sessionCols = `id::text, user_id::text, token_hash, created_at, expires_at, last_seen_at,
	reauthenticated_at, revoked_at, COALESCE(host(ip), ''), user_agent`

func scanSession(row pgx.Row) (*store.Session, error) {
	var x store.Session
	err := row.Scan(&x.ID, &x.UserID, &x.TokenHash, &x.CreatedAt, &x.ExpiresAt, &x.LastSeenAt,
		&x.ReauthenticatedAt, &x.RevokedAt, &x.IP, &x.UserAgent)
	if err != nil {
		return nil, mapErr(err)
	}
	return &x, nil
}

func (s *Store) CreateSession(ctx context.Context, x *store.Session) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO sessions (id, user_id, token_hash, created_at, expires_at, last_seen_at, reauthenticated_at, ip, user_agent)
		VALUES ($1, $2, $3, $4, $5, $6, $7, NULLIF($8, '')::inet, $9)`,
		x.ID, x.UserID, x.TokenHash, x.CreatedAt, x.ExpiresAt, x.LastSeenAt, x.ReauthenticatedAt, x.IP, x.UserAgent)
	return mapErr(err)
}

func (s *Store) GetSessionByTokenHash(ctx context.Context, hash []byte) (*store.Session, error) {
	return scanSession(s.pool.QueryRow(ctx, `SELECT `+sessionCols+` FROM sessions WHERE token_hash = $1`, hash))
}

func (s *Store) execOne(ctx context.Context, sql string, args ...any) error {
	tag, err := s.pool.Exec(ctx, sql, args...)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (s *Store) TouchSession(ctx context.Context, id string, at time.Time) error {
	return s.execOne(ctx, `UPDATE sessions SET last_seen_at = $2 WHERE id = $1`, id, at)
}

func (s *Store) MarkSessionReauthenticated(ctx context.Context, id string, at time.Time) error {
	return s.execOne(ctx, `UPDATE sessions SET reauthenticated_at = $2 WHERE id = $1`, id, at)
}

func (s *Store) RevokeSession(ctx context.Context, id string, at time.Time) error {
	return s.execOne(ctx, `UPDATE sessions SET revoked_at = COALESCE(revoked_at, $2) WHERE id = $1`, id, at)
}

func (s *Store) RevokeUserSessions(ctx context.Context, userID, exceptID string, at time.Time) (int, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE sessions SET revoked_at = $3
		WHERE user_id = $1 AND revoked_at IS NULL AND ($2 = '' OR id::text <> $2)`, userID, exceptID, at)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

func (s *Store) ListActiveSessions(ctx context.Context, userID string, now time.Time) ([]store.Session, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+sessionCols+` FROM sessions
		WHERE user_id = $1 AND revoked_at IS NULL AND expires_at > $2 ORDER BY last_seen_at DESC`, userID, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []store.Session{}
	for rows.Next() {
		x, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *x)
	}
	return out, rows.Err()
}

// ---- Audit ----

func (s *Store) InsertAudit(ctx context.Context, e *store.AuditEntry) error {
	md := e.Metadata
	if md == nil {
		md = map[string]any{}
	}
	raw, err := json.Marshal(md)
	if err != nil {
		return err
	}
	return s.pool.QueryRow(ctx, `INSERT INTO audit_logs (organization_id, actor_user_id, actor_label, action, target_type,
			target_id, outcome, ip, user_agent, request_id, metadata)
		VALUES ($1, $2, $3, $4, $5, $6, $7, NULLIF($8, '')::inet, $9, $10, $11::jsonb)
		RETURNING id, created_at`,
		e.OrganizationID, e.ActorUserID, e.ActorLabel, e.Action, e.TargetType, e.TargetID, string(e.Outcome),
		e.IP, e.UserAgent, e.RequestID, string(raw)).Scan(&e.ID, &e.CreatedAt)
}

func (s *Store) ListAudit(ctx context.Context, f store.AuditFilter) ([]store.AuditEntry, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `SELECT id, organization_id::text, actor_user_id::text, actor_label, action, target_type,
			target_id, outcome, COALESCE(host(ip), ''), user_agent, request_id, metadata, created_at
		FROM audit_logs
		WHERE ($1 = '' OR organization_id::text = $1)
		  AND ($2 = '' OR actor_user_id::text = $2)
		  AND ($3 = '' OR starts_with(action, $3))
		  AND ($4 = '' OR outcome = $4)
		  AND ($5 = 0 OR id < $5)
		ORDER BY id DESC LIMIT $6`,
		f.OrganizationID, f.ActorUserID, f.ActionPrefix, string(f.Outcome), f.BeforeID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []store.AuditEntry{}
	for rows.Next() {
		var e store.AuditEntry
		var outcome string
		var raw []byte
		if err := rows.Scan(&e.ID, &e.OrganizationID, &e.ActorUserID, &e.ActorLabel, &e.Action, &e.TargetType,
			&e.TargetID, &outcome, &e.IP, &e.UserAgent, &e.RequestID, &raw, &e.CreatedAt); err != nil {
			return nil, err
		}
		e.Outcome = store.AuditOutcome(outcome)
		e.Metadata = map[string]any{}
		if err := json.Unmarshal(raw, &e.Metadata); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ---- Settings ----

func (s *Store) GetSetting(ctx context.Context, key string) (json.RawMessage, error) {
	var raw []byte
	if err := s.pool.QueryRow(ctx, `SELECT value FROM settings WHERE key = $1`, key).Scan(&raw); err != nil {
		return nil, mapErr(err)
	}
	return raw, nil
}

func (s *Store) PutSetting(ctx context.Context, key string, value json.RawMessage, updatedBy *string) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO settings (key, value, updated_by, updated_at) VALUES ($1, $2::jsonb, $3, now())
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_by = EXCLUDED.updated_by, updated_at = now()`,
		key, string(value), updatedBy)
	return mapErr(err)
}
