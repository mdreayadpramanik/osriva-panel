package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/store"
)

const groupCols = `g.id::text, g.organization_id::text, g.name, g.description, g.created_by::text, g.created_at,
	COALESCE((SELECT array_agg(m.server_id::text ORDER BY m.server_id::text) FROM server_group_members m WHERE m.group_id = g.id), '{}')`

func scanGroup(row pgx.Row) (*store.ServerGroup, error) {
	var g store.ServerGroup
	if err := row.Scan(&g.ID, &g.OrganizationID, &g.Name, &g.Description, &g.CreatedBy, &g.CreatedAt, &g.ServerIDs); err != nil {
		return nil, mapErr(err)
	}
	if g.ServerIDs == nil {
		g.ServerIDs = []string{}
	}
	return &g, nil
}

func (s *Store) CreateServerGroup(ctx context.Context, g *store.ServerGroup) error {
	err := s.pool.QueryRow(ctx, `INSERT INTO server_groups (id, organization_id, name, description, created_by)
		VALUES ($1, $2, $3, $4, $5) RETURNING created_at`, g.ID, g.OrganizationID, g.Name, g.Description, g.CreatedBy).Scan(&g.CreatedAt)
	g.ServerIDs = []string{}
	return mapErr(err)
}

func (s *Store) GetServerGroup(ctx context.Context, id string) (*store.ServerGroup, error) {
	return scanGroup(s.pool.QueryRow(ctx, `SELECT `+groupCols+` FROM server_groups g WHERE g.id = $1`, id))
}

func (s *Store) ListServerGroups(ctx context.Context, orgID string) ([]store.ServerGroup, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+groupCols+` FROM server_groups g WHERE g.organization_id = $1 ORDER BY g.name`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []store.ServerGroup{}
	for rows.Next() {
		g, err := scanGroup(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *g)
	}
	return out, rows.Err()
}

func (s *Store) UpdateServerGroup(ctx context.Context, g *store.ServerGroup) error {
	if err := s.execOne(ctx, `UPDATE server_groups SET name = $2, description = $3 WHERE id = $1`, g.ID, g.Name, g.Description); err != nil {
		return err
	}
	got, err := s.GetServerGroup(ctx, g.ID)
	if err != nil {
		return err
	}
	*g = *got
	return nil
}

func (s *Store) DeleteServerGroup(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM server_groups WHERE id = $1`, id)
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23503" { // alert_rules.group_id is ON DELETE RESTRICT
		return fmt.Errorf("%w: the group is used by an alert rule", store.ErrConflict)
	}
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (s *Store) SetServerGroupMembers(ctx context.Context, groupID string, serverIDs []string) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT true FROM server_groups WHERE id = $1 FOR UPDATE`, groupID).Scan(&exists); err != nil {
			return mapErr(err)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM server_group_members WHERE group_id = $1`, groupID); err != nil {
			return err
		}
		if len(serverIDs) == 0 {
			return nil
		}
		_, err := tx.Exec(ctx, `INSERT INTO server_group_members (group_id, server_id)
			SELECT $1, unnest($2::uuid[]) ON CONFLICT DO NOTHING`, groupID, serverIDs)
		return mapErr(err)
	})
}

const batchCols = `id::text, organization_id::text, type, params, group_id::text, target, skipped, requested_by::text, created_at`

func scanBatch(row pgx.Row) (*store.TaskBatch, error) {
	var b store.TaskBatch
	var params, skipped []byte
	if err := row.Scan(&b.ID, &b.OrganizationID, &b.Type, &params, &b.GroupID, &b.Target, &skipped, &b.RequestedBy, &b.CreatedAt); err != nil {
		return nil, mapErr(err)
	}
	b.Params, b.Skipped = params, skipped
	return &b, nil
}

func (s *Store) CreateTaskBatch(ctx context.Context, b *store.TaskBatch, tasks []*store.Task) error {
	skipped := b.Skipped
	if len(skipped) == 0 {
		skipped = []byte("[]")
	}
	params := b.Params
	if len(params) == 0 {
		params = []byte("{}")
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `INSERT INTO task_batches (id, organization_id, type, params, group_id, target, skipped, requested_by)
			VALUES ($1, $2, $3, $4::jsonb, $5, $6, $7::jsonb, $8) RETURNING created_at`,
			b.ID, b.OrganizationID, b.Type, string(params), b.GroupID, b.Target, string(skipped), b.RequestedBy).Scan(&b.CreatedAt)
		if err != nil {
			return mapErr(err)
		}
		b.Params, b.Skipped = params, skipped
		for _, t := range tasks {
			t.BatchID = &b.ID
			if err := insertTask(ctx, tx, t); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) GetTaskBatch(ctx context.Context, id string) (*store.TaskBatch, error) {
	return scanBatch(s.pool.QueryRow(ctx, `SELECT `+batchCols+` FROM task_batches WHERE id = $1`, id))
}

func (s *Store) ListTaskBatches(ctx context.Context, orgID string, limit int) ([]store.TaskBatch, error) {
	var lim any
	if limit > 0 {
		lim = limit
	}
	rows, err := s.pool.Query(ctx, `SELECT `+batchCols+` FROM task_batches WHERE organization_id = $1
		ORDER BY created_at DESC, id LIMIT $2`, orgID, lim)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []store.TaskBatch{}
	for rows.Next() {
		b, err := scanBatch(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *b)
	}
	return out, rows.Err()
}

func (s *Store) ListBatchTasks(ctx context.Context, batchID string) ([]store.Task, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+taskCols+` FROM agent_tasks WHERE batch_id = $1 ORDER BY server_id::text`, batchID)
	if err != nil {
		return nil, err
	}
	return scanTasks(rows)
}
