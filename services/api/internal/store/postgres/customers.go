package postgres

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/store"
)

const customerCols = `id::text, organization_id::text, email, name, status, password_hash, setup_expires_at, created_by::text, created_at, last_login_at`

func scanCustomer(row pgx.Row) (*store.Customer, error) {
	var c store.Customer
	var status string
	if err := row.Scan(&c.ID, &c.OrganizationID, &c.Email, &c.Name, &status, &c.PasswordHash, &c.SetupExpiresAt, &c.CreatedBy, &c.CreatedAt, &c.LastLoginAt); err != nil {
		return nil, mapErr(err)
	}
	c.Status = store.CustomerStatus(status)
	return &c, nil
}

func (s *Store) CreateCustomer(ctx context.Context, c *store.Customer, setupHash []byte) error {
	return mapErr(s.pool.QueryRow(ctx, `INSERT INTO customers (id, organization_id, email, name, status, password_hash, setup_token_hash, setup_expires_at, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING created_at`,
		c.ID, c.OrganizationID, c.Email, c.Name, string(c.Status), c.PasswordHash, setupHash, c.SetupExpiresAt, c.CreatedBy).Scan(&c.CreatedAt))
}

func (s *Store) GetCustomer(ctx context.Context, id string) (*store.Customer, error) {
	return scanCustomer(s.pool.QueryRow(ctx, `SELECT `+customerCols+` FROM customers WHERE id = $1`, id))
}

func (s *Store) GetCustomerByEmail(ctx context.Context, email string) (*store.Customer, error) {
	return scanCustomer(s.pool.QueryRow(ctx, `SELECT `+customerCols+` FROM customers WHERE email = $1`, email))
}

func (s *Store) ListCustomers(ctx context.Context, orgID string) ([]store.Customer, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+customerCols+` FROM customers WHERE organization_id = $1 ORDER BY email`, orgID)
	if err != nil {
		return nil, err
	}
	return scanAll(rows, scanCustomer)
}

func (s *Store) SetCustomerStatus(ctx context.Context, id string, status store.CustomerStatus) error {
	return s.execOne(ctx, `UPDATE customers SET status = $2 WHERE id = $1`, id, string(status))
}

func (s *Store) SetCustomerSetupToken(ctx context.Context, id string, hash []byte, expiresAt time.Time) error {
	return s.execOne(ctx, `UPDATE customers SET setup_token_hash = $2, setup_expires_at = $3 WHERE id = $1`, id, hash, expiresAt)
}

func (s *Store) ConsumeCustomerSetup(ctx context.Context, hash []byte, passwordHash string, now time.Time) (*store.Customer, error) {
	return scanCustomer(s.pool.QueryRow(ctx, `UPDATE customers SET password_hash = $2, setup_token_hash = NULL, setup_expires_at = NULL
		WHERE setup_token_hash = $1 AND setup_expires_at > $3 RETURNING `+customerCols, hash, passwordHash, now))
}

func (s *Store) SetCustomerPassword(ctx context.Context, id, passwordHash string) error {
	return s.execOne(ctx, `UPDATE customers SET password_hash = $2 WHERE id = $1`, id, passwordHash)
}

func (s *Store) TouchCustomerLogin(ctx context.Context, id string, at time.Time) error {
	return s.execOne(ctx, `UPDATE customers SET last_login_at = $2 WHERE id = $1`, id, at)
}

const custSessCols = `id::text, customer_id::text, token_hash, ip, user_agent, created_at, expires_at, last_seen_at, revoked_at`

func (s *Store) CreateCustomerSession(ctx context.Context, x *store.CustomerSession) error {
	return mapErr(s.pool.QueryRow(ctx, `INSERT INTO customer_sessions (id, customer_id, token_hash, ip, user_agent, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING created_at, last_seen_at`,
		x.ID, x.CustomerID, x.TokenHash, x.IP, x.UserAgent, x.ExpiresAt).Scan(&x.CreatedAt, &x.LastSeenAt))
}

func (s *Store) GetCustomerSessionByTokenHash(ctx context.Context, hash []byte) (*store.CustomerSession, error) {
	var x store.CustomerSession
	err := s.pool.QueryRow(ctx, `SELECT `+custSessCols+` FROM customer_sessions WHERE token_hash = $1`, hash).Scan(&x.ID, &x.CustomerID, &x.TokenHash,
		&x.IP, &x.UserAgent, &x.CreatedAt, &x.ExpiresAt, &x.LastSeenAt, &x.RevokedAt)
	if err != nil {
		return nil, mapErr(err)
	}
	return &x, nil
}

func (s *Store) TouchCustomerSession(ctx context.Context, id string, at time.Time) error {
	return s.execOne(ctx, `UPDATE customer_sessions SET last_seen_at = $2 WHERE id = $1`, id, at)
}

func (s *Store) RevokeCustomerSession(ctx context.Context, id string, at time.Time) error {
	return s.execOne(ctx, `UPDATE customer_sessions SET revoked_at = COALESCE(revoked_at, $2) WHERE id = $1`, id, at)
}

func (s *Store) RevokeCustomerSessions(ctx context.Context, customerID, keepID string, at time.Time) error {
	_, err := s.pool.Exec(ctx, `UPDATE customer_sessions SET revoked_at = $3 WHERE customer_id = $1 AND id::text <> $2 AND revoked_at IS NULL`,
		customerID, keepID, at)
	return err
}

func (s *Store) SetVPSCustomer(ctx context.Context, vpsID string, customerID *string) error {
	return s.execOne(ctx, `UPDATE vps SET customer_id = $2, updated_at = now() WHERE id = $1`, vpsID, customerID)
}

func (s *Store) ListCustomerVPS(ctx context.Context, customerID string) ([]store.VPS, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+vpsCols+` FROM vps WHERE customer_id = $1 ORDER BY name`, customerID)
	if err != nil {
		return nil, err
	}
	return scanAll(rows, scanVPS)
}

func (s *Store) GetCustomerVPS(ctx context.Context, customerID, vpsID string) (*store.VPS, error) {
	return scanVPS(s.pool.QueryRow(ctx, `SELECT `+vpsCols+` FROM vps WHERE id = $1 AND customer_id = $2`, vpsID, customerID))
}

func (s *Store) GetProvisionedServiceByVPS(ctx context.Context, vpsID string) (*store.ProvisionedService, error) {
	return scanService(s.pool.QueryRow(ctx, `SELECT `+svcCols+` FROM provisioned_services WHERE vps_id = $1`, vpsID))
}
