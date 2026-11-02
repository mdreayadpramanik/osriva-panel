package postgres

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/store"
)

const taskCols = `id::text, server_id::text, type, params, status, requested_by::text, created_at, expires_at,
	started_at, finished_at, result, error`

func scanTask(row pgx.Row) (*store.Task, error) {
	var t store.Task
	var status string
	var params, result []byte
	if err := row.Scan(&t.ID, &t.ServerID, &t.Type, &params, &status, &t.RequestedBy, &t.CreatedAt, &t.ExpiresAt,
		&t.StartedAt, &t.FinishedAt, &result, &t.Error); err != nil {
		return nil, mapErr(err)
	}
	t.Status, t.Params, t.Result = store.TaskStatus(status), params, result
	return &t, nil
}

func scanTasks(rows pgx.Rows) ([]store.Task, error) {
	defer rows.Close()
	out := []store.Task{}
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

func jsonOrNil(b json.RawMessage) any {
	if len(b) == 0 {
		return nil
	}
	return string(b)
}

func (s *Store) CreateTask(ctx context.Context, t *store.Task) error {
	if t.Status == "" {
		t.Status = store.TaskQueued
	}
	params := t.Params
	if len(params) == 0 {
		params = []byte("{}")
	}
	err := s.pool.QueryRow(ctx, `INSERT INTO agent_tasks (id, server_id, type, params, status, requested_by, expires_at)
		VALUES ($1, $2, $3, $4::jsonb, $5, $6, $7) RETURNING created_at`,
		t.ID, t.ServerID, t.Type, string(params), string(t.Status), t.RequestedBy, t.ExpiresAt).Scan(&t.CreatedAt)
	t.Params = params
	return mapErr(err)
}

func (s *Store) GetTask(ctx context.Context, id string) (*store.Task, error) {
	return scanTask(s.pool.QueryRow(ctx, `SELECT `+taskCols+` FROM agent_tasks WHERE id = $1`, id))
}

func (s *Store) ListTasks(ctx context.Context, serverID string, limit int) ([]store.Task, error) {
	var lim any
	if limit > 0 {
		lim = limit
	}
	rows, err := s.pool.Query(ctx, `SELECT `+taskCols+` FROM agent_tasks WHERE server_id = $1
		ORDER BY created_at DESC, id LIMIT $2`, serverID, lim)
	if err != nil {
		return nil, err
	}
	return scanTasks(rows)
}

func (s *Store) ClaimTasks(ctx context.Context, serverID string, now time.Time, limit int) ([]store.Task, error) {
	// SKIP LOCKED: concurrent polls (e.g. two API instances) never receive the same task.
	rows, err := s.pool.Query(ctx, `UPDATE agent_tasks SET status = 'running', started_at = $2
		WHERE id IN (
			SELECT id FROM agent_tasks
			WHERE server_id = $1 AND status = 'queued' AND expires_at > $2
			ORDER BY created_at LIMIT $3 FOR UPDATE SKIP LOCKED)
		RETURNING `+taskCols, serverID, now, limit)
	if err != nil {
		return nil, err
	}
	out, err := scanTasks(rows)
	if err != nil {
		return nil, err
	}
	// RETURNING order is unspecified; deliver in creation order.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].CreatedAt.Before(out[j-1].CreatedAt); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out, nil
}

func (s *Store) CompleteTask(ctx context.Context, serverID, taskID string, status store.TaskStatus, result json.RawMessage, errMsg string, now time.Time) error {
	return s.execOne(ctx, `UPDATE agent_tasks SET status = $3, result = $4::jsonb, error = $5, finished_at = $6
		WHERE id = $1 AND server_id = $2 AND status = 'running'`,
		taskID, serverID, string(status), jsonOrNil(result), errMsg, now)
}

func (s *Store) ExpireTasks(ctx context.Context, now, runningBefore time.Time) (int, error) {
	var n int64
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		a, err := tx.Exec(ctx, `UPDATE agent_tasks SET status = 'expired', finished_at = $1,
				error = 'not picked up by the agent in time'
			WHERE status = 'queued' AND expires_at <= $1`, now)
		if err != nil {
			return err
		}
		b, err := tx.Exec(ctx, `UPDATE agent_tasks SET status = 'failed', finished_at = $1,
				error = 'timeout: the agent did not report a result'
			WHERE status = 'running' AND started_at < $2`, now, runningBefore)
		if err != nil {
			return err
		}
		n = a.RowsAffected() + b.RowsAffected()
		return nil
	})
	return int(n), err
}

func (s *Store) InsertMetrics(ctx context.Context, samples []store.MetricSample) error {
	if len(samples) == 0 {
		return nil
	}
	batch := &pgx.Batch{}
	for _, m := range samples {
		batch.Queue(`INSERT INTO metric_samples (server_id, ts, data) VALUES ($1, $2, $3::jsonb)
			ON CONFLICT (server_id, ts) DO NOTHING`, m.ServerID, m.TS, string(m.Data))
	}
	br := s.pool.SendBatch(ctx, batch)
	defer br.Close()
	for range samples {
		if _, err := br.Exec(); err != nil {
			return mapErr(err)
		}
	}
	return nil
}

func (s *Store) ListMetrics(ctx context.Context, serverID string, since time.Time, limit int) ([]store.MetricSample, error) {
	var lim any
	if limit > 0 {
		lim = limit
	}
	// Newest `limit` samples after `since`, returned oldest first.
	rows, err := s.pool.Query(ctx, `SELECT server_id::text, ts, data FROM (
			SELECT server_id, ts, data FROM metric_samples WHERE server_id = $1 AND ts > $2 ORDER BY ts DESC LIMIT $3
		) recent ORDER BY ts`, serverID, since, lim)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []store.MetricSample{}
	for rows.Next() {
		var m store.MetricSample
		var data []byte
		if err := rows.Scan(&m.ServerID, &m.TS, &data); err != nil {
			return nil, err
		}
		m.Data = data
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) PruneMetrics(ctx context.Context, before time.Time) (int, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM metric_samples WHERE ts < $1`, before)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}
