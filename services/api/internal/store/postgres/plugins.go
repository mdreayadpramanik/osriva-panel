package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/store"
)

func scanApproval(row pgx.Row) (*store.PluginApproval, error) {
	var a store.PluginApproval
	if err := row.Scan(&a.Name, &a.Enabled, &a.ApprovedSHA256, &a.ApprovedBy, &a.ApprovedAt); err != nil {
		return nil, mapErr(err)
	}
	return &a, nil
}

func (s *Store) ListPluginApprovals(ctx context.Context) ([]store.PluginApproval, error) {
	rows, err := s.pool.Query(ctx, `SELECT name, enabled, approved_sha256, approved_by::text, approved_at FROM plugins ORDER BY name`)
	if err != nil {
		return nil, err
	}
	return scanAll(rows, scanApproval)
}

func (s *Store) GetPluginApproval(ctx context.Context, name string) (*store.PluginApproval, error) {
	return scanApproval(s.pool.QueryRow(ctx, `SELECT name, enabled, approved_sha256, approved_by::text, approved_at FROM plugins WHERE name = $1`, name))
}

func (s *Store) SetPluginApproval(ctx context.Context, a *store.PluginApproval) error {
	return mapErr(s.pool.QueryRow(ctx, `INSERT INTO plugins (name, enabled, approved_sha256, approved_by) VALUES ($1, $2, $3, $4)
		ON CONFLICT (name) DO UPDATE SET enabled = EXCLUDED.enabled, approved_sha256 = EXCLUDED.approved_sha256,
			approved_by = EXCLUDED.approved_by, approved_at = now()
		RETURNING approved_at`, a.Name, a.Enabled, a.ApprovedSHA256, a.ApprovedBy).Scan(&a.ApprovedAt))
}
