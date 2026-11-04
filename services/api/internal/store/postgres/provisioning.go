package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/store"
)

const clientCols = `id::text, organization_id::text, name, key_id, secret_enc, scopes, allowed_cidrs::text[], webhook_url, enabled,
	created_by::text, created_at, last_used_at`

func scanClient(row pgx.Row) (*store.APIClient, error) {
	var c store.APIClient
	if err := row.Scan(&c.ID, &c.OrganizationID, &c.Name, &c.KeyID, &c.SecretEnc, &c.Scopes, &c.AllowedCIDRs, &c.WebhookURL, &c.Enabled,
		&c.CreatedBy, &c.CreatedAt, &c.LastUsedAt); err != nil {
		return nil, mapErr(err)
	}
	if c.AllowedCIDRs == nil {
		c.AllowedCIDRs = []string{}
	}
	return &c, nil
}

func (s *Store) CreateAPIClient(ctx context.Context, c *store.APIClient) error {
	cidrs := c.AllowedCIDRs
	if cidrs == nil {
		cidrs = []string{}
	}
	return mapErr(s.pool.QueryRow(ctx, `INSERT INTO api_clients (id, organization_id, name, key_id, secret_enc, scopes, allowed_cidrs, webhook_url, enabled, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7::cidr[], $8, $9, $10) RETURNING created_at`,
		c.ID, c.OrganizationID, c.Name, c.KeyID, c.SecretEnc, c.Scopes, cidrs, c.WebhookURL, c.Enabled, c.CreatedBy).Scan(&c.CreatedAt))
}

func (s *Store) GetAPIClient(ctx context.Context, id string) (*store.APIClient, error) {
	return scanClient(s.pool.QueryRow(ctx, `SELECT `+clientCols+` FROM api_clients WHERE id = $1`, id))
}

func (s *Store) GetAPIClientByKey(ctx context.Context, keyID string) (*store.APIClient, error) {
	return scanClient(s.pool.QueryRow(ctx, `SELECT `+clientCols+` FROM api_clients WHERE key_id = $1`, keyID))
}

func (s *Store) ListAPIClients(ctx context.Context, orgID string) ([]store.APIClient, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+clientCols+` FROM api_clients WHERE organization_id = $1 ORDER BY name`, orgID)
	if err != nil {
		return nil, err
	}
	return scanAll(rows, scanClient)
}

func (s *Store) SetAPIClientEnabled(ctx context.Context, id string, enabled bool) error {
	return s.execOne(ctx, `UPDATE api_clients SET enabled = $2 WHERE id = $1`, id, enabled)
}

func (s *Store) TouchAPIClient(ctx context.Context, id string, at time.Time) error {
	return s.execOne(ctx, `UPDATE api_clients SET last_used_at = $2 WHERE id = $1`, id, at)
}

func (s *Store) DeleteAPIClient(ctx context.Context, id string) error {
	return s.deleteRestricted(ctx, `DELETE FROM api_clients WHERE id = $1`, id)
}

func (s *Store) UseNonce(ctx context.Context, clientID, nonce string, expiresAt, now time.Time) error {
	// An expired row may be reused; an unexpired one means a replay (no row returned).
	var one int
	err := s.pool.QueryRow(ctx, `INSERT INTO api_nonces (client_id, nonce, expires_at) VALUES ($1, $2, $3)
		ON CONFLICT (client_id, nonce) DO UPDATE SET expires_at = EXCLUDED.expires_at WHERE api_nonces.expires_at <= $4
		RETURNING 1`, clientID, nonce, expiresAt, now).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.ErrConflict
	}
	return mapErr(err)
}

func (s *Store) PruneNonces(ctx context.Context, before time.Time) (int, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM api_nonces WHERE expires_at < $1`, before)
	return int(tag.RowsAffected()), err
}

func (s *Store) BeginIdempotent(ctx context.Context, rec *store.IdempotencyRecord) (*store.IdempotencyRecord, error) {
	tag, err := s.pool.Exec(ctx, `INSERT INTO idempotency_keys (client_id, key, request_hash) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`,
		rec.ClientID, rec.Key, rec.RequestHash)
	if err != nil {
		return nil, mapErr(err)
	}
	if tag.RowsAffected() == 1 {
		return nil, nil
	}
	var old store.IdempotencyRecord
	err = s.pool.QueryRow(ctx, `SELECT client_id::text, key, request_hash, status_code, response, created_at FROM idempotency_keys
		WHERE client_id = $1 AND key = $2`, rec.ClientID, rec.Key).Scan(&old.ClientID, &old.Key, &old.RequestHash, &old.StatusCode, &old.Response, &old.CreatedAt)
	if err != nil {
		return nil, mapErr(err)
	}
	return &old, nil
}

func (s *Store) FinishIdempotent(ctx context.Context, clientID, key string, status int, response []byte) error {
	return s.execOne(ctx, `UPDATE idempotency_keys SET status_code = $3, response = $4 WHERE client_id = $1 AND key = $2`, clientID, key, status, response)
}

func (s *Store) DeleteIdempotent(ctx context.Context, clientID, key string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM idempotency_keys WHERE client_id = $1 AND key = $2`, clientID, key)
	return err
}

func (s *Store) PruneIdempotency(ctx context.Context, before time.Time) (int, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM idempotency_keys WHERE created_at < $1`, before)
	return int(tag.RowsAffected()), err
}

const planCols = `id::text, organization_id::text, name, kind, template_id::text, pool_id::text, node_id::text, vcpus, memory_mb, disk_gb, enabled, created_at`

func scanPlan(row pgx.Row) (*store.ServicePlan, error) {
	var p store.ServicePlan
	if err := row.Scan(&p.ID, &p.OrganizationID, &p.Name, &p.Kind, &p.TemplateID, &p.PoolID, &p.NodeID, &p.VCPUs, &p.MemoryMB, &p.DiskGB,
		&p.Enabled, &p.CreatedAt); err != nil {
		return nil, mapErr(err)
	}
	return &p, nil
}

func (s *Store) CreateServicePlan(ctx context.Context, p *store.ServicePlan) error {
	return mapErr(s.pool.QueryRow(ctx, `INSERT INTO service_plans (id, organization_id, name, kind, template_id, pool_id, node_id, vcpus, memory_mb, disk_gb, enabled)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11) RETURNING created_at`,
		p.ID, p.OrganizationID, p.Name, p.Kind, p.TemplateID, p.PoolID, p.NodeID, p.VCPUs, p.MemoryMB, p.DiskGB, p.Enabled).Scan(&p.CreatedAt))
}

func (s *Store) GetServicePlan(ctx context.Context, id string) (*store.ServicePlan, error) {
	return scanPlan(s.pool.QueryRow(ctx, `SELECT `+planCols+` FROM service_plans WHERE id = $1`, id))
}

func (s *Store) ListServicePlans(ctx context.Context, orgID string) ([]store.ServicePlan, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+planCols+` FROM service_plans WHERE organization_id = $1 ORDER BY name`, orgID)
	if err != nil {
		return nil, err
	}
	return scanAll(rows, scanPlan)
}

func (s *Store) DeleteServicePlan(ctx context.Context, id string) error {
	return s.execOne(ctx, `DELETE FROM service_plans WHERE id = $1`, id)
}

const svcCols = `id::text, organization_id::text, client_id::text, plan_id::text, external_id, vps_id::text, status, pending, last_error, created_at, updated_at`

func scanService(row pgx.Row) (*store.ProvisionedService, error) {
	var x store.ProvisionedService
	if err := row.Scan(&x.ID, &x.OrganizationID, &x.ClientID, &x.PlanID, &x.ExternalID, &x.VPSID, &x.Status, &x.Pending, &x.LastError,
		&x.CreatedAt, &x.UpdatedAt); err != nil {
		return nil, mapErr(err)
	}
	return &x, nil
}

func (s *Store) CreateProvisionedService(ctx context.Context, x *store.ProvisionedService) error {
	return mapErr(s.pool.QueryRow(ctx, `INSERT INTO provisioned_services (id, organization_id, client_id, plan_id, external_id, vps_id, status, pending, last_error)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING created_at, updated_at`,
		x.ID, x.OrganizationID, x.ClientID, x.PlanID, x.ExternalID, x.VPSID, x.Status, x.Pending, x.LastError).Scan(&x.CreatedAt, &x.UpdatedAt))
}

func (s *Store) GetProvisionedService(ctx context.Context, id string) (*store.ProvisionedService, error) {
	return scanService(s.pool.QueryRow(ctx, `SELECT `+svcCols+` FROM provisioned_services WHERE id = $1`, id))
}

func (s *Store) GetProvisionedServiceByExternal(ctx context.Context, clientID, externalID string) (*store.ProvisionedService, error) {
	return scanService(s.pool.QueryRow(ctx, `SELECT `+svcCols+` FROM provisioned_services WHERE client_id = $1 AND external_id = $2`, clientID, externalID))
}

func (s *Store) ListProvisionedServices(ctx context.Context, orgID string) ([]store.ProvisionedService, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+svcCols+` FROM provisioned_services WHERE organization_id = $1 ORDER BY created_at DESC, id`, orgID)
	if err != nil {
		return nil, err
	}
	return scanAll(rows, scanService)
}

func (s *Store) ListPendingServices(ctx context.Context, limit int) ([]store.ProvisionedService, error) {
	var lim any
	if limit > 0 {
		lim = limit
	}
	rows, err := s.pool.Query(ctx, `SELECT `+svcCols+` FROM provisioned_services WHERE pending <> '' ORDER BY updated_at LIMIT $1`, lim)
	if err != nil {
		return nil, err
	}
	return scanAll(rows, scanService)
}

func (s *Store) UpdateProvisionedService(ctx context.Context, x *store.ProvisionedService) error {
	return mapErr(s.pool.QueryRow(ctx, `UPDATE provisioned_services SET status = $2, pending = $3, last_error = $4, vps_id = $5, updated_at = now()
		WHERE id = $1 RETURNING updated_at`, x.ID, x.Status, x.Pending, x.LastError, x.VPSID).Scan(&x.UpdatedAt))
}

func (s *Store) DeleteProvisionedService(ctx context.Context, id string) error {
	return s.execOne(ctx, `DELETE FROM provisioned_services WHERE id = $1`, id)
}
