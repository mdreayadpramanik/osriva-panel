package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/store"
)

const cmsCols = `id::text, server_id::text, kind, name, title, url, host_port, admin_user, admin_email,
	install_task_id::text, site_id::text, created_by::text, created_at`

func scanCMS(row pgx.Row) (*store.CMSInstance, error) {
	var c store.CMSInstance
	if err := row.Scan(&c.ID, &c.ServerID, &c.Kind, &c.Name, &c.Title, &c.URL, &c.HostPort, &c.AdminUser, &c.AdminEmail,
		&c.InstallTaskID, &c.SiteID, &c.CreatedBy, &c.CreatedAt); err != nil {
		return nil, mapErr(err)
	}
	return &c, nil
}

func (s *Store) CreateCMS(ctx context.Context, c *store.CMSInstance) error {
	err := s.pool.QueryRow(ctx, `INSERT INTO cms_instances (id, server_id, kind, name, title, url, host_port, admin_user,
			admin_email, install_task_id, site_id, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12) RETURNING created_at`,
		c.ID, c.ServerID, c.Kind, c.Name, c.Title, c.URL, c.HostPort, c.AdminUser, c.AdminEmail, c.InstallTaskID, c.SiteID, c.CreatedBy).
		Scan(&c.CreatedAt)
	return mapErr(err)
}

func (s *Store) GetCMS(ctx context.Context, id string) (*store.CMSInstance, error) {
	return scanCMS(s.pool.QueryRow(ctx, `SELECT `+cmsCols+` FROM cms_instances WHERE id = $1`, id))
}

func (s *Store) ListCMS(ctx context.Context, serverID string) ([]store.CMSInstance, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+cmsCols+` FROM cms_instances WHERE server_id = $1 ORDER BY name`, serverID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []store.CMSInstance{}
	for rows.Next() {
		c, err := scanCMS(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

func (s *Store) DeleteCMS(ctx context.Context, id string) error {
	return s.execOne(ctx, `DELETE FROM cms_instances WHERE id = $1`, id)
}
