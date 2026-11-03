package postgres

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/store"
)

const jobCols = `id::text, kind, payload, status, attempts, max_attempts, run_at, locked_until, last_error, created_at, updated_at`

func scanJob(row pgx.Row) (*store.Job, error) {
	var j store.Job
	var payload []byte
	var status string
	if err := row.Scan(&j.ID, &j.Kind, &payload, &status, &j.Attempts, &j.MaxAttempts, &j.RunAt, &j.LockedUntil,
		&j.LastError, &j.CreatedAt, &j.UpdatedAt); err != nil {
		return nil, mapErr(err)
	}
	j.Payload, j.Status = payload, store.JobStatus(status)
	return &j, nil
}

func scanJobs(rows pgx.Rows) ([]store.Job, error) {
	defer rows.Close()
	out := []store.Job{}
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *j)
	}
	return out, rows.Err()
}

func (s *Store) EnqueueJob(ctx context.Context, j *store.Job) error {
	payload := j.Payload
	if len(payload) == 0 {
		payload = []byte("{}")
	}
	if j.RunAt.IsZero() {
		j.RunAt = time.Now().UTC()
	}
	err := s.pool.QueryRow(ctx, `INSERT INTO jobs (id, kind, payload, status, max_attempts, run_at)
		VALUES ($1, $2, $3::jsonb, 'queued', $4, $5) RETURNING created_at, updated_at`,
		j.ID, j.Kind, string(payload), j.MaxAttempts, j.RunAt).Scan(&j.CreatedAt, &j.UpdatedAt)
	j.Payload, j.Status, j.Attempts, j.LockedUntil = payload, store.JobQueued, 0, nil
	return mapErr(err)
}

func (s *Store) ClaimJobs(ctx context.Context, now time.Time, lease time.Duration, limit int) ([]store.Job, error) {
	// SKIP LOCKED: concurrent workers (other API instances) never claim the same job.
	rows, err := s.pool.Query(ctx, `UPDATE jobs SET status = 'running', attempts = attempts + 1, locked_until = $2, updated_at = $1
		WHERE id IN (
			SELECT id FROM jobs
			WHERE (status = 'queued' AND run_at <= $1) OR (status = 'running' AND locked_until <= $1)
			ORDER BY run_at LIMIT $3 FOR UPDATE SKIP LOCKED)
		RETURNING `+jobCols, now, now.Add(lease), limit)
	if err != nil {
		return nil, err
	}
	return scanJobs(rows)
}

func (s *Store) FinishJob(ctx context.Context, id string, status store.JobStatus, retryAt time.Time, errMsg string, now time.Time) error {
	if status == store.JobQueued {
		return s.execOne(ctx, `UPDATE jobs SET status = 'queued', run_at = $2, locked_until = NULL, last_error = $3, updated_at = $4
			WHERE id = $1 AND status = 'running'`, id, retryAt, errMsg, now)
	}
	return s.execOne(ctx, `UPDATE jobs SET status = $2, locked_until = NULL, last_error = $3, updated_at = $4
		WHERE id = $1 AND status = 'running'`, id, string(status), errMsg, now)
}

func (s *Store) GetJob(ctx context.Context, id string) (*store.Job, error) {
	return scanJob(s.pool.QueryRow(ctx, `SELECT `+jobCols+` FROM jobs WHERE id = $1`, id))
}

func (s *Store) ListJobs(ctx context.Context, status store.JobStatus, limit int) ([]store.Job, error) {
	var lim any
	if limit > 0 {
		lim = limit
	}
	rows, err := s.pool.Query(ctx, `SELECT `+jobCols+` FROM jobs WHERE $1 = '' OR status = $1
		ORDER BY created_at DESC, id LIMIT $2`, string(status), lim)
	if err != nil {
		return nil, err
	}
	return scanJobs(rows)
}

func (s *Store) CountJobs(ctx context.Context) (map[store.JobStatus]int, error) {
	rows, err := s.pool.Query(ctx, `SELECT status, count(*) FROM jobs GROUP BY status`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[store.JobStatus]int{}
	for rows.Next() {
		var st string
		var n int
		if err := rows.Scan(&st, &n); err != nil {
			return nil, err
		}
		out[store.JobStatus(st)] = n
	}
	return out, rows.Err()
}

func (s *Store) PruneJobs(ctx context.Context, before time.Time) (int, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM jobs WHERE status IN ('succeeded', 'failed') AND updated_at < $1`, before)
	return int(tag.RowsAffected()), err
}

func (s *Store) AcquireLease(ctx context.Context, name, holder string, now time.Time, ttl time.Duration) (bool, error) {
	// The upsert only takes over a lease that is ours or has expired; otherwise no row is returned.
	var got string
	err := s.pool.QueryRow(ctx, `INSERT INTO leases (name, holder, expires_at) VALUES ($1, $2, $3)
		ON CONFLICT (name) DO UPDATE SET holder = EXCLUDED.holder, expires_at = EXCLUDED.expires_at
			WHERE leases.holder = EXCLUDED.holder OR leases.expires_at <= $4
		RETURNING holder`, name, holder, now.Add(ttl), now).Scan(&got)
	if err == pgx.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return got == holder, nil
}
