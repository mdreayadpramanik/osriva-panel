package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/store"
)

func (s *Store) GetAgentCA(ctx context.Context) (string, []byte, error) {
	var cert string
	var key []byte
	err := s.pool.QueryRow(ctx, `SELECT cert_pem, key_enc FROM agent_ca WHERE id = 1`).Scan(&cert, &key)
	if err != nil {
		return "", nil, mapErr(err)
	}
	return cert, key, nil
}

func (s *Store) InsertAgentCA(ctx context.Context, certPEM string, keyEnc []byte) error {
	tag, err := s.pool.Exec(ctx, `INSERT INTO agent_ca (id, cert_pem, key_enc) VALUES (1, $1, $2) ON CONFLICT (id) DO NOTHING`, certPEM, keyEnc)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return store.ErrConflict
	}
	return nil
}

const serverCols = `id::text, organization_id::text, name, status, enrollment_token_hash, enrollment_expires_at,
	hostname, os_family, os_name, os_version, kernel, arch, agent_version, last_seen_at, COALESCE(host(last_seen_ip), ''),
	enrolled_at, created_by::text, created_at, updated_at`

func scanServer(row pgx.Row) (*store.Server, error) {
	var x store.Server
	var status string
	f := &x.Facts
	err := row.Scan(&x.ID, &x.OrganizationID, &x.Name, &status, &x.EnrollmentTokenHash, &x.EnrollmentExpiresAt,
		&f.Hostname, &f.OSFamily, &f.OSName, &f.OSVersion, &f.Kernel, &f.Arch, &f.AgentVersion, &x.LastSeenAt, &x.LastSeenIP,
		&x.EnrolledAt, &x.CreatedBy, &x.CreatedAt, &x.UpdatedAt)
	if err != nil {
		return nil, mapErr(err)
	}
	x.Status = store.ServerStatus(status)
	return &x, nil
}

func (s *Store) CreateServer(ctx context.Context, x *store.Server) error {
	if x.Status == "" {
		x.Status = store.ServerPending
	}
	err := s.pool.QueryRow(ctx, `INSERT INTO servers (id, organization_id, name, status, enrollment_token_hash, enrollment_expires_at, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING created_at, updated_at`,
		x.ID, x.OrganizationID, x.Name, string(x.Status), x.EnrollmentTokenHash, x.EnrollmentExpiresAt, x.CreatedBy,
	).Scan(&x.CreatedAt, &x.UpdatedAt)
	return mapErr(err)
}

func (s *Store) GetServer(ctx context.Context, id string) (*store.Server, error) {
	return scanServer(s.pool.QueryRow(ctx, `SELECT `+serverCols+` FROM servers WHERE id = $1`, id))
}

func (s *Store) ListServers(ctx context.Context, orgID string) ([]store.Server, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+serverCols+` FROM servers WHERE organization_id = $1 ORDER BY name`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []store.Server{}
	for rows.Next() {
		x, err := scanServer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *x)
	}
	return out, rows.Err()
}

func (s *Store) SetEnrollmentToken(ctx context.Context, id string, hash []byte, expiresAt time.Time) error {
	tag, err := s.pool.Exec(ctx, `UPDATE servers SET enrollment_token_hash = $2, enrollment_expires_at = $3, updated_at = now()
		WHERE id = $1 AND status = 'pending'`, id, hash, expiresAt)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		if _, err := s.GetServer(ctx, id); err != nil {
			return err
		}
		return store.ErrConflict
	}
	return nil
}

func (s *Store) FindPendingEnrollment(ctx context.Context, hash []byte, now time.Time) (*store.Server, error) {
	return scanServer(s.pool.QueryRow(ctx, `SELECT `+serverCols+` FROM servers
		WHERE enrollment_token_hash = $1 AND status = 'pending' AND enrollment_expires_at > $2`, hash, now))
}

func (s *Store) CompleteEnrollment(ctx context.Context, serverID string, hash []byte, now time.Time, cert *store.AgentCertificate) error {
	return mapErr(pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// The conditional UPDATE consumes the token exactly once even under concurrent enrolment.
		tag, err := tx.Exec(ctx, `UPDATE servers SET status = 'active', enrollment_token_hash = NULL, enrollment_expires_at = NULL,
				enrolled_at = $3, updated_at = $3
			WHERE id = $1 AND enrollment_token_hash = $2 AND status = 'pending' AND enrollment_expires_at > $3`, serverID, hash, now)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return pgx.ErrNoRows
		}
		_, err = tx.Exec(ctx, `INSERT INTO agent_certificates (serial, server_id, fingerprint, not_after, issued_at)
			VALUES ($1, $2, $3, $4, $5)`, cert.Serial, serverID, cert.Fingerprint, cert.NotAfter, now)
		return err
	}))
}

func (s *Store) AddAgentCertificate(ctx context.Context, c *store.AgentCertificate) error {
	issued := c.IssuedAt
	if issued.IsZero() {
		issued = time.Now().UTC()
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO agent_certificates (serial, server_id, fingerprint, not_after, issued_at)
		VALUES ($1, $2, $3, $4, $5)`, c.Serial, c.ServerID, c.Fingerprint, c.NotAfter, issued)
	return mapErr(err)
}

func (s *Store) GetAgentCertificate(ctx context.Context, serial string) (*store.AgentCertificate, error) {
	var c store.AgentCertificate
	err := s.pool.QueryRow(ctx, `SELECT serial, server_id::text, fingerprint, not_after, issued_at, revoked_at
		FROM agent_certificates WHERE serial = $1`, serial).Scan(&c.Serial, &c.ServerID, &c.Fingerprint, &c.NotAfter, &c.IssuedAt, &c.RevokedAt)
	if err != nil {
		return nil, mapErr(err)
	}
	return &c, nil
}

func (s *Store) RevokeServer(ctx context.Context, id string, now time.Time) error {
	return mapErr(pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE servers SET status = 'revoked', enrollment_token_hash = NULL, enrollment_expires_at = NULL,
			updated_at = $2 WHERE id = $1`, id, now)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return pgx.ErrNoRows
		}
		_, err = tx.Exec(ctx, `UPDATE agent_certificates SET revoked_at = $2 WHERE server_id = $1 AND revoked_at IS NULL`, id, now)
		return err
	}))
}

func (s *Store) RecordReport(ctx context.Context, serverID string, f store.ServerFacts, ip string, now time.Time, inv *store.ServerInventory) error {
	return mapErr(pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE servers SET hostname = $2, os_family = $3, os_name = $4, os_version = $5, kernel = $6,
				arch = $7, agent_version = $8, last_seen_at = $9, last_seen_ip = NULLIF($10, '')::inet, updated_at = $9
			WHERE id = $1`, serverID, f.Hostname, f.OSFamily, f.OSName, f.OSVersion, f.Kernel, f.Arch, f.AgentVersion, now, ip)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return pgx.ErrNoRows
		}
		if inv == nil {
			return nil
		}
		caps := inv.Capabilities
		if len(caps) == 0 {
			caps = []byte("[]")
		}
		_, err = tx.Exec(ctx, `INSERT INTO server_inventory (server_id, collected_at, received_at, data, capabilities)
			VALUES ($1, $2, $3, $4::jsonb, $5::jsonb)
			ON CONFLICT (server_id) DO UPDATE SET collected_at = EXCLUDED.collected_at, received_at = EXCLUDED.received_at,
				data = EXCLUDED.data, capabilities = EXCLUDED.capabilities`,
			serverID, inv.CollectedAt, now, string(inv.Data), string(caps))
		return err
	}))
}

func (s *Store) GetServerInventory(ctx context.Context, serverID string) (*store.ServerInventory, error) {
	var inv store.ServerInventory
	var data, caps []byte
	err := s.pool.QueryRow(ctx, `SELECT server_id::text, collected_at, received_at, data, capabilities
		FROM server_inventory WHERE server_id = $1`, serverID).Scan(&inv.ServerID, &inv.CollectedAt, &inv.ReceivedAt, &data, &caps)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	inv.Data, inv.Capabilities = data, caps
	return &inv, nil
}
