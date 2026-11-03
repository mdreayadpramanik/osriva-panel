-- OSRIVA Panel: alerts, notifications, long-term metrics (Phase 8).
-- See docs/adr/0011-alerts-notifications.md.

CREATE TABLE notification_channels (
    id              uuid PRIMARY KEY,
    organization_id uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    name            text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    kind            text NOT NULL CHECK (kind IN ('webhook', 'email')),
    -- Non-secret settings (URL, SMTP host, port, sender, recipients, TLS mode, user name).
    config          jsonb NOT NULL,
    -- Webhook signing secret or SMTP password, AES-256-GCM encrypted (bound to the channel id).
    secret_enc      bytea,
    enabled         boolean NOT NULL DEFAULT true,
    created_by      uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at      timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE alert_rules (
    id               uuid PRIMARY KEY,
    organization_id  uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    name             text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    server_id        uuid REFERENCES servers (id) ON DELETE CASCADE, -- NULL: every active server
    metric           text NOT NULL CHECK (metric IN ('cpu', 'memory', 'disk', 'load', 'offline', 'backup')),
    threshold        double precision NOT NULL,
    duration_minutes integer NOT NULL CHECK (duration_minutes BETWEEN 1 AND 1440),
    channel_ids      uuid[] NOT NULL DEFAULT '{}',
    enabled          boolean NOT NULL DEFAULT true,
    created_by       uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at       timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE alert_states (
    rule_id    uuid NOT NULL REFERENCES alert_rules (id) ON DELETE CASCADE,
    server_id  uuid NOT NULL REFERENCES servers (id) ON DELETE CASCADE,
    firing     boolean NOT NULL,
    since      timestamptz NOT NULL,
    value      double precision NOT NULL,
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (rule_id, server_id)
);

CREATE TABLE alert_events (
    id              uuid PRIMARY KEY,
    organization_id uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    rule_id         uuid REFERENCES alert_rules (id) ON DELETE SET NULL,
    rule_name       text NOT NULL,
    server_id       uuid REFERENCES servers (id) ON DELETE SET NULL,
    server_name     text NOT NULL,
    metric          text NOT NULL,
    state           text NOT NULL CHECK (state IN ('firing', 'resolved', 'test')),
    value           double precision NOT NULL,
    threshold       double precision NOT NULL,
    delivered       integer NOT NULL DEFAULT 0,
    failed          integer NOT NULL DEFAULT 0,
    delivery_error  text NOT NULL DEFAULT '',
    created_at      timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX alert_events_org_idx ON alert_events (organization_id, created_at DESC);

-- Hourly aggregates of the raw samples (kept 24 h); retained for 90 days.
CREATE TABLE metric_rollups (
    server_id uuid NOT NULL REFERENCES servers (id) ON DELETE CASCADE,
    hour      timestamptz NOT NULL,
    data      jsonb NOT NULL,
    PRIMARY KEY (server_id, hour)
);

CREATE INDEX metric_rollups_hour_idx ON metric_rollups (hour);
