package postgres

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/store"
)

func (s *Store) EnsureBackupKey(ctx context.Context, k *store.BackupKey) (*store.BackupKey, error) {
	if _, err := s.pool.Exec(ctx, `INSERT INTO backup_keys (server_id, recipient, identity_enc) VALUES ($1, $2, $3)
		ON CONFLICT (server_id) DO NOTHING`, k.ServerID, k.Recipient, k.IdentityEnc); err != nil {
		return nil, mapErr(err)
	}
	var out store.BackupKey
	err := s.pool.QueryRow(ctx, `SELECT server_id::text, recipient, identity_enc FROM backup_keys WHERE server_id = $1`, k.ServerID).
		Scan(&out.ServerID, &out.Recipient, &out.IdentityEnc)
	if err != nil {
		return nil, mapErr(err)
	}
	return &out, nil
}

const backupCols = `id::text, server_id::text, kind, target, task_id::text, policy_id::text, size_bytes, sha256, plain_sha256,
	files, created_by::text, created_at`

func scanBackup(row pgx.Row) (*store.Backup, error) {
	var b store.Backup
	if err := row.Scan(&b.ID, &b.ServerID, &b.Kind, &b.Target, &b.TaskID, &b.PolicyID, &b.Size, &b.SHA256, &b.PlainSHA256,
		&b.Files, &b.CreatedBy, &b.CreatedAt); err != nil {
		return nil, mapErr(err)
	}
	return &b, nil
}

func (s *Store) CreateBackup(ctx context.Context, b *store.Backup) error {
	err := s.pool.QueryRow(ctx, `INSERT INTO backups (id, server_id, kind, target, task_id, policy_id, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING created_at`,
		b.ID, b.ServerID, b.Kind, b.Target, b.TaskID, b.PolicyID, b.CreatedBy).Scan(&b.CreatedAt)
	return mapErr(err)
}

func (s *Store) GetBackup(ctx context.Context, id string) (*store.Backup, error) {
	return scanBackup(s.pool.QueryRow(ctx, `SELECT `+backupCols+` FROM backups WHERE id = $1`, id))
}

func (s *Store) ListBackups(ctx context.Context, serverID string, limit int) ([]store.Backup, error) {
	var lim any
	if limit > 0 {
		lim = limit
	}
	rows, err := s.pool.Query(ctx, `SELECT `+backupCols+` FROM backups WHERE server_id = $1 ORDER BY created_at DESC, id DESC LIMIT $2`, serverID, lim)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []store.Backup{}
	for rows.Next() {
		b, err := scanBackup(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *b)
	}
	return out, rows.Err()
}

func (s *Store) SetBackupResult(ctx context.Context, id string, size int64, sha, plainSHA string, files int) error {
	return s.execOne(ctx, `UPDATE backups SET size_bytes = $2, sha256 = $3, plain_sha256 = $4, files = $5 WHERE id = $1`,
		id, size, sha, plainSHA, files)
}

func (s *Store) DeleteBackup(ctx context.Context, id string) error {
	return s.execOne(ctx, `DELETE FROM backups WHERE id = $1`, id)
}

const policyCols = `id::text, server_id::text, kind, target, interval_hours, keep, last_run_at, created_by::text, created_at`

func scanPolicy(row pgx.Row) (*store.BackupPolicy, error) {
	var p store.BackupPolicy
	if err := row.Scan(&p.ID, &p.ServerID, &p.Kind, &p.Target, &p.IntervalHours, &p.Keep, &p.LastRunAt, &p.CreatedBy, &p.CreatedAt); err != nil {
		return nil, mapErr(err)
	}
	return &p, nil
}

func (s *Store) CreateBackupPolicy(ctx context.Context, p *store.BackupPolicy) error {
	err := s.pool.QueryRow(ctx, `INSERT INTO backup_policies (id, server_id, kind, target, interval_hours, keep, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING created_at`,
		p.ID, p.ServerID, p.Kind, p.Target, p.IntervalHours, p.Keep, p.CreatedBy).Scan(&p.CreatedAt)
	return mapErr(err)
}

func (s *Store) GetBackupPolicy(ctx context.Context, id string) (*store.BackupPolicy, error) {
	return scanPolicy(s.pool.QueryRow(ctx, `SELECT `+policyCols+` FROM backup_policies WHERE id = $1`, id))
}

func (s *Store) ListBackupPolicies(ctx context.Context, serverID string) ([]store.BackupPolicy, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+policyCols+` FROM backup_policies
		WHERE $1 = '' OR server_id::text = $1 ORDER BY target`, serverID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []store.BackupPolicy{}
	for rows.Next() {
		p, err := scanPolicy(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

func (s *Store) MarkBackupPolicyRun(ctx context.Context, id string, at time.Time) error {
	return s.execOne(ctx, `UPDATE backup_policies SET last_run_at = $2 WHERE id = $1`, id, at)
}

func (s *Store) DeleteBackupPolicy(ctx context.Context, id string) error {
	return s.execOne(ctx, `DELETE FROM backup_policies WHERE id = $1`, id)
}
