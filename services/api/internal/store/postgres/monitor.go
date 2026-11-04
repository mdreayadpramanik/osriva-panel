package postgres

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/store"
)

const channelCols = `id::text, organization_id::text, name, kind, config, secret_enc, enabled, created_by::text, created_at`

func scanChannel(row pgx.Row) (*store.NotificationChannel, error) {
	var c store.NotificationChannel
	var cfg []byte
	if err := row.Scan(&c.ID, &c.OrganizationID, &c.Name, &c.Kind, &cfg, &c.SecretEnc, &c.Enabled, &c.CreatedBy, &c.CreatedAt); err != nil {
		return nil, mapErr(err)
	}
	c.Config = cfg
	return &c, nil
}

func (s *Store) CreateChannel(ctx context.Context, c *store.NotificationChannel) error {
	err := s.pool.QueryRow(ctx, `INSERT INTO notification_channels (id, organization_id, name, kind, config, secret_enc, enabled, created_by)
		VALUES ($1, $2, $3, $4, $5::jsonb, $6, $7, $8) RETURNING created_at`,
		c.ID, c.OrganizationID, c.Name, c.Kind, string(c.Config), c.SecretEnc, c.Enabled, c.CreatedBy).Scan(&c.CreatedAt)
	return mapErr(err)
}

func (s *Store) GetChannel(ctx context.Context, id string) (*store.NotificationChannel, error) {
	return scanChannel(s.pool.QueryRow(ctx, `SELECT `+channelCols+` FROM notification_channels WHERE id = $1`, id))
}

func (s *Store) ListChannels(ctx context.Context, orgID string) ([]store.NotificationChannel, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+channelCols+` FROM notification_channels WHERE organization_id = $1 ORDER BY name`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []store.NotificationChannel{}
	for rows.Next() {
		c, err := scanChannel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

func (s *Store) UpdateChannel(ctx context.Context, c *store.NotificationChannel) error {
	err := s.pool.QueryRow(ctx, `UPDATE notification_channels SET name = $2, config = $3::jsonb, secret_enc = $4, enabled = $5
		WHERE id = $1 RETURNING organization_id::text, kind, created_by::text, created_at`,
		c.ID, c.Name, string(c.Config), c.SecretEnc, c.Enabled).Scan(&c.OrganizationID, &c.Kind, &c.CreatedBy, &c.CreatedAt)
	return mapErr(err)
}

func (s *Store) DeleteChannel(ctx context.Context, id string) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE alert_rules SET channel_ids = array_remove(channel_ids, $1::uuid) WHERE $1::uuid = ANY (channel_ids)`, id); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `DELETE FROM notification_channels WHERE id = $1`, id)
		if err != nil {
			return mapErr(err)
		}
		if tag.RowsAffected() == 0 {
			return store.ErrNotFound
		}
		return nil
	})
}

const ruleCols = `id::text, organization_id::text, name, server_id::text, metric, threshold, duration_minutes,
	channel_ids::text[], enabled, created_by::text, created_at, group_id::text`

func scanRule(row pgx.Row) (*store.AlertRule, error) {
	var r store.AlertRule
	if err := row.Scan(&r.ID, &r.OrganizationID, &r.Name, &r.ServerID, &r.Metric, &r.Threshold, &r.DurationMinutes,
		&r.ChannelIDs, &r.Enabled, &r.CreatedBy, &r.CreatedAt, &r.GroupID); err != nil {
		return nil, mapErr(err)
	}
	if r.ChannelIDs == nil {
		r.ChannelIDs = []string{}
	}
	return &r, nil
}

func (s *Store) CreateAlertRule(ctx context.Context, r *store.AlertRule) error {
	if r.ChannelIDs == nil {
		r.ChannelIDs = []string{} // a nil slice would be sent as NULL
	}
	err := s.pool.QueryRow(ctx, `INSERT INTO alert_rules (id, organization_id, name, server_id, metric, threshold, duration_minutes,
			channel_ids, enabled, created_by, group_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8::uuid[], $9, $10, $11) RETURNING created_at`,
		r.ID, r.OrganizationID, r.Name, r.ServerID, r.Metric, r.Threshold, r.DurationMinutes, r.ChannelIDs, r.Enabled, r.CreatedBy, r.GroupID).
		Scan(&r.CreatedAt)
	return mapErr(err)
}

func (s *Store) GetAlertRule(ctx context.Context, id string) (*store.AlertRule, error) {
	return scanRule(s.pool.QueryRow(ctx, `SELECT `+ruleCols+` FROM alert_rules WHERE id = $1`, id))
}

func (s *Store) ListAlertRules(ctx context.Context, orgID string) ([]store.AlertRule, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+ruleCols+` FROM alert_rules WHERE $1 = '' OR organization_id::text = $1 ORDER BY name`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []store.AlertRule{}
	for rows.Next() {
		r, err := scanRule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

func (s *Store) UpdateAlertRule(ctx context.Context, r *store.AlertRule) error {
	if r.ChannelIDs == nil {
		r.ChannelIDs = []string{}
	}
	err := s.pool.QueryRow(ctx, `UPDATE alert_rules SET name = $2, server_id = $3, metric = $4, threshold = $5, duration_minutes = $6,
			channel_ids = $7::uuid[], enabled = $8, group_id = $9
		WHERE id = $1 RETURNING organization_id::text, created_by::text, created_at`,
		r.ID, r.Name, r.ServerID, r.Metric, r.Threshold, r.DurationMinutes, r.ChannelIDs, r.Enabled, r.GroupID).
		Scan(&r.OrganizationID, &r.CreatedBy, &r.CreatedAt)
	return mapErr(err)
}

func (s *Store) DeleteAlertRule(ctx context.Context, id string) error {
	return s.execOne(ctx, `DELETE FROM alert_rules WHERE id = $1`, id)
}

func (s *Store) GetAlertState(ctx context.Context, ruleID, serverID string) (*store.AlertState, error) {
	var st store.AlertState
	err := s.pool.QueryRow(ctx, `SELECT rule_id::text, COALESCE(server_id::text, ''), firing, since, value, updated_at FROM alert_states
		WHERE rule_id = $1 AND server_id IS NOT DISTINCT FROM NULLIF($2, '')::uuid`, ruleID, serverID).Scan(&st.RuleID, &st.ServerID, &st.Firing, &st.Since, &st.Value, &st.UpdatedAt)
	if err != nil {
		return nil, mapErr(err)
	}
	return &st, nil
}

func (s *Store) PutAlertState(ctx context.Context, st *store.AlertState) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO alert_states (rule_id, server_id, firing, since, value, updated_at)
		VALUES ($1, NULLIF($2, '')::uuid, $3, $4, $5, $6)
		ON CONFLICT (rule_id, server_id) DO UPDATE SET firing = $3, since = $4, value = $5, updated_at = $6`,
		st.RuleID, st.ServerID, st.Firing, st.Since, st.Value, st.UpdatedAt)
	return mapErr(err)
}

func (s *Store) InsertAlertEvent(ctx context.Context, e *store.AlertEvent) error {
	err := s.pool.QueryRow(ctx, `INSERT INTO alert_events (id, organization_id, rule_id, rule_name, server_id, server_name, metric,
			state, value, threshold, delivered, failed, delivery_error, pending)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14) RETURNING created_at`,
		e.ID, e.OrganizationID, e.RuleID, e.RuleName, e.ServerID, e.ServerName, e.Metric, e.State, e.Value, e.Threshold,
		e.Delivered, e.Failed, e.DeliveryError, e.Pending).Scan(&e.CreatedAt)
	return mapErr(err)
}

func (s *Store) SetAlertEventDelivery(ctx context.Context, id string, delivered, failed int, errMsg string) error {
	return s.execOne(ctx, `UPDATE alert_events SET delivered = $2, failed = $3, delivery_error = $4 WHERE id = $1`, id, delivered, failed, errMsg)
}

func (s *Store) RecordAlertDelivery(ctx context.Context, id string, ok bool, errMsg string) error {
	if ok {
		return s.execOne(ctx, `UPDATE alert_events SET delivered = delivered + 1, pending = GREATEST(pending - 1, 0) WHERE id = $1`, id)
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var prev string
		if err := tx.QueryRow(ctx, `SELECT delivery_error FROM alert_events WHERE id = $1 FOR UPDATE`, id).Scan(&prev); err != nil {
			return mapErr(err)
		}
		_, err := tx.Exec(ctx, `UPDATE alert_events SET failed = failed + 1, pending = GREATEST(pending - 1, 0), delivery_error = $2
			WHERE id = $1`, id, store.AppendDeliveryError(prev, errMsg))
		return err
	})
}

func (s *Store) ListAlertEvents(ctx context.Context, orgID string, limit int) ([]store.AlertEvent, error) {
	var lim any
	if limit > 0 {
		lim = limit
	}
	rows, err := s.pool.Query(ctx, `SELECT id::text, organization_id::text, rule_id::text, rule_name, server_id::text, server_name, metric,
			state, value, threshold, delivered, failed, delivery_error, created_at, pending
		FROM alert_events WHERE organization_id = $1 ORDER BY created_at DESC, id LIMIT $2`, orgID, lim)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []store.AlertEvent{}
	for rows.Next() {
		var e store.AlertEvent
		if err := rows.Scan(&e.ID, &e.OrganizationID, &e.RuleID, &e.RuleName, &e.ServerID, &e.ServerName, &e.Metric, &e.State,
			&e.Value, &e.Threshold, &e.Delivered, &e.Failed, &e.DeliveryError, &e.CreatedAt, &e.Pending); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) UpsertRollup(ctx context.Context, r *store.MetricRollup) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO metric_rollups (server_id, hour, data) VALUES ($1, $2, $3::jsonb)
		ON CONFLICT (server_id, hour) DO UPDATE SET data = $3::jsonb`, r.ServerID, r.Hour, string(r.Data))
	return mapErr(err)
}

func (s *Store) ListRollups(ctx context.Context, serverID string, since time.Time) ([]store.MetricRollup, error) {
	rows, err := s.pool.Query(ctx, `SELECT server_id::text, hour, data FROM metric_rollups WHERE server_id = $1 AND hour >= $2 ORDER BY hour`, serverID, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []store.MetricRollup{}
	for rows.Next() {
		var r store.MetricRollup
		var data []byte
		if err := rows.Scan(&r.ServerID, &r.Hour, &data); err != nil {
			return nil, err
		}
		r.Data = data
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) PruneRollups(ctx context.Context, before time.Time) (int, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM metric_rollups WHERE hour < $1`, before)
	return int(tag.RowsAffected()), err
}

func (s *Store) PruneAudit(ctx context.Context, before time.Time) (int, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM audit_logs WHERE created_at < $1`, before)
	return int(tag.RowsAffected()), err
}

func (s *Store) GetAlertEvent(ctx context.Context, id string) (*store.AlertEvent, error) {
	var e store.AlertEvent
	err := s.pool.QueryRow(ctx, `SELECT id::text, organization_id::text, rule_id::text, rule_name, server_id::text, server_name, metric,
			state, value, threshold, delivered, failed, delivery_error, created_at, pending
		FROM alert_events WHERE id = $1`, id).Scan(&e.ID, &e.OrganizationID, &e.RuleID, &e.RuleName, &e.ServerID, &e.ServerName,
		&e.Metric, &e.State, &e.Value, &e.Threshold, &e.Delivered, &e.Failed, &e.DeliveryError, &e.CreatedAt, &e.Pending)
	if err != nil {
		return nil, mapErr(err)
	}
	return &e, nil
}

func (s *Store) ListAlertStates(ctx context.Context) ([]store.AlertState, error) {
	rows, err := s.pool.Query(ctx, `SELECT rule_id::text, COALESCE(server_id::text, ''), firing, since, value, updated_at FROM alert_states`)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []store.AlertState
	for rows.Next() {
		var st store.AlertState
		if err := rows.Scan(&st.RuleID, &st.ServerID, &st.Firing, &st.Since, &st.Value, &st.UpdatedAt); err != nil {
			return nil, mapErr(err)
		}
		out = append(out, st)
	}
	return out, mapErr(rows.Err())
}
