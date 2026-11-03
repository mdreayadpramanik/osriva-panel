-- OSRIVA Panel: encrypted backups (Phase 7). See docs/adr/0010-backups.md.

-- One age key pair per server. The agent only receives the recipient; the
-- identity is stored encrypted with the panel's master key.
CREATE TABLE backup_keys (
    server_id    uuid PRIMARY KEY REFERENCES servers (id) ON DELETE CASCADE,
    recipient    text NOT NULL,
    identity_enc bytea NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE backup_policies (
    id             uuid PRIMARY KEY,
    server_id      uuid NOT NULL REFERENCES servers (id) ON DELETE CASCADE,
    kind           text NOT NULL CHECK (kind IN ('volume', 'wordpress')),
    target         text NOT NULL,
    interval_hours integer NOT NULL CHECK (interval_hours BETWEEN 1 AND 720),
    keep           integer NOT NULL CHECK (keep BETWEEN 1 AND 100),
    last_run_at    timestamptz,
    created_by     uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at     timestamptz NOT NULL DEFAULT now(),
    UNIQUE (server_id, kind, target)
);

CREATE TABLE backups (
    id           uuid PRIMARY KEY,
    server_id    uuid NOT NULL REFERENCES servers (id) ON DELETE CASCADE,
    kind         text NOT NULL CHECK (kind IN ('volume', 'wordpress')),
    target       text NOT NULL,
    task_id      uuid REFERENCES agent_tasks (id) ON DELETE SET NULL,
    policy_id    uuid REFERENCES backup_policies (id) ON DELETE SET NULL,
    -- Copied from the task result so verify and restore never depend on task retention.
    size_bytes   bigint,
    sha256       text,
    plain_sha256 text,
    files        integer,
    created_by   uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX backups_server_idx ON backups (server_id, created_at DESC);
CREATE INDEX backups_policy_idx ON backups (policy_id, created_at DESC);
