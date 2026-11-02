package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/store"
)

const appCols = `id::text, server_id::text, name, git_url, git_ref, build_mode, build_context, dockerfile,
	container_port, host_port, public, env_enc, created_by::text, created_at, updated_at`

func scanApp(row pgx.Row) (*store.App, error) {
	var a store.App
	if err := row.Scan(&a.ID, &a.ServerID, &a.Name, &a.GitURL, &a.GitRef, &a.BuildMode, &a.BuildContext, &a.Dockerfile,
		&a.ContainerPort, &a.HostPort, &a.Public, &a.EnvEnc, &a.CreatedBy, &a.CreatedAt, &a.UpdatedAt); err != nil {
		return nil, mapErr(err)
	}
	return &a, nil
}

func (s *Store) CreateApp(ctx context.Context, a *store.App) error {
	err := s.pool.QueryRow(ctx, `INSERT INTO apps (id, server_id, name, git_url, git_ref, build_mode, build_context,
			dockerfile, container_port, host_port, public, env_enc, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13) RETURNING created_at, updated_at`,
		a.ID, a.ServerID, a.Name, a.GitURL, a.GitRef, a.BuildMode, a.BuildContext, a.Dockerfile,
		a.ContainerPort, a.HostPort, a.Public, a.EnvEnc, a.CreatedBy).Scan(&a.CreatedAt, &a.UpdatedAt)
	return mapErr(err)
}

func (s *Store) GetApp(ctx context.Context, id string) (*store.App, error) {
	return scanApp(s.pool.QueryRow(ctx, `SELECT `+appCols+` FROM apps WHERE id = $1`, id))
}

func (s *Store) ListApps(ctx context.Context, serverID string) ([]store.App, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+appCols+` FROM apps WHERE server_id = $1 ORDER BY name`, serverID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []store.App{}
	for rows.Next() {
		a, err := scanApp(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

func (s *Store) UpdateApp(ctx context.Context, a *store.App) error {
	// Server, name and creation data are immutable.
	err := s.pool.QueryRow(ctx, `UPDATE apps SET git_url = $2, git_ref = $3, build_mode = $4, build_context = $5,
			dockerfile = $6, container_port = $7, host_port = $8, public = $9, env_enc = $10, updated_at = now()
		WHERE id = $1 RETURNING server_id::text, name, created_by::text, created_at, updated_at`,
		a.ID, a.GitURL, a.GitRef, a.BuildMode, a.BuildContext, a.Dockerfile, a.ContainerPort, a.HostPort, a.Public, a.EnvEnc).
		Scan(&a.ServerID, &a.Name, &a.CreatedBy, &a.CreatedAt, &a.UpdatedAt)
	return mapErr(err)
}

func (s *Store) DeleteApp(ctx context.Context, id string) error {
	return s.execOne(ctx, `DELETE FROM apps WHERE id = $1`, id)
}

const deploymentCols = `id::text, app_id::text, task_id::text, kind, rollback_of::text, image, git_ref, created_by::text, created_at`

func scanDeployment(row pgx.Row) (*store.Deployment, error) {
	var d store.Deployment
	if err := row.Scan(&d.ID, &d.AppID, &d.TaskID, &d.Kind, &d.RollbackOf, &d.Image, &d.GitRef, &d.CreatedBy, &d.CreatedAt); err != nil {
		return nil, mapErr(err)
	}
	return &d, nil
}

func (s *Store) CreateDeployment(ctx context.Context, d *store.Deployment) error {
	err := s.pool.QueryRow(ctx, `INSERT INTO deployments (id, app_id, task_id, kind, rollback_of, image, git_ref, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING created_at`,
		d.ID, d.AppID, d.TaskID, d.Kind, d.RollbackOf, d.Image, d.GitRef, d.CreatedBy).Scan(&d.CreatedAt)
	return mapErr(err)
}

func (s *Store) GetDeployment(ctx context.Context, id string) (*store.Deployment, error) {
	return scanDeployment(s.pool.QueryRow(ctx, `SELECT `+deploymentCols+` FROM deployments WHERE id = $1`, id))
}

func (s *Store) ListDeployments(ctx context.Context, appID string, limit int) ([]store.Deployment, error) {
	var lim any
	if limit > 0 {
		lim = limit
	}
	rows, err := s.pool.Query(ctx, `SELECT `+deploymentCols+` FROM deployments WHERE app_id = $1
		ORDER BY created_at DESC, id DESC LIMIT $2`, appID, lim)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []store.Deployment{}
	for rows.Next() {
		d, err := scanDeployment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}
