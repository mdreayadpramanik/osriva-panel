package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/store"
)

func (s *Store) GetProxySettings(ctx context.Context, serverID string) (*store.ProxySettings, error) {
	var p store.ProxySettings
	err := s.pool.QueryRow(ctx, `SELECT server_id::text, enabled, http_port, https_port, acme_email, acme_staging, updated_at
		FROM proxy_settings WHERE server_id = $1`, serverID).
		Scan(&p.ServerID, &p.Enabled, &p.HTTPPort, &p.HTTPSPort, &p.ACMEEmail, &p.ACMEStaging, &p.UpdatedAt)
	if err != nil {
		return nil, mapErr(err)
	}
	return &p, nil
}

func (s *Store) SaveProxySettings(ctx context.Context, p *store.ProxySettings) error {
	err := s.pool.QueryRow(ctx, `INSERT INTO proxy_settings (server_id, enabled, http_port, https_port, acme_email, acme_staging)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (server_id) DO UPDATE SET enabled = $2, http_port = $3, https_port = $4, acme_email = $5,
			acme_staging = $6, updated_at = now()
		RETURNING updated_at`, p.ServerID, p.Enabled, p.HTTPPort, p.HTTPSPort, p.ACMEEmail, p.ACMEStaging).Scan(&p.UpdatedAt)
	return mapErr(err)
}

const siteCols = `id::text, server_id::text, domain, upstream_port, tls_mode, created_by::text, created_at, updated_at`

func scanSite(row pgx.Row) (*store.Site, error) {
	var x store.Site
	if err := row.Scan(&x.ID, &x.ServerID, &x.Domain, &x.UpstreamPort, &x.TLSMode, &x.CreatedBy, &x.CreatedAt, &x.UpdatedAt); err != nil {
		return nil, mapErr(err)
	}
	return &x, nil
}

func (s *Store) CreateSite(ctx context.Context, x *store.Site) error {
	err := s.pool.QueryRow(ctx, `INSERT INTO sites (id, server_id, domain, upstream_port, tls_mode, created_by)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING created_at, updated_at`,
		x.ID, x.ServerID, x.Domain, x.UpstreamPort, x.TLSMode, x.CreatedBy).Scan(&x.CreatedAt, &x.UpdatedAt)
	return mapErr(err)
}

func (s *Store) GetSite(ctx context.Context, id string) (*store.Site, error) {
	return scanSite(s.pool.QueryRow(ctx, `SELECT `+siteCols+` FROM sites WHERE id = $1`, id))
}

func (s *Store) ListSites(ctx context.Context, serverID string) ([]store.Site, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+siteCols+` FROM sites WHERE server_id = $1 ORDER BY domain`, serverID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []store.Site{}
	for rows.Next() {
		x, err := scanSite(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *x)
	}
	return out, rows.Err()
}

func (s *Store) UpdateSite(ctx context.Context, x *store.Site) error {
	err := s.pool.QueryRow(ctx, `UPDATE sites SET domain = $2, upstream_port = $3, tls_mode = $4, updated_at = now()
		WHERE id = $1 RETURNING server_id::text, created_by::text, created_at, updated_at`,
		x.ID, x.Domain, x.UpstreamPort, x.TLSMode).Scan(&x.ServerID, &x.CreatedBy, &x.CreatedAt, &x.UpdatedAt)
	return mapErr(err)
}

func (s *Store) DeleteSite(ctx context.Context, id string) error {
	return s.execOne(ctx, `DELETE FROM sites WHERE id = $1`, id)
}
