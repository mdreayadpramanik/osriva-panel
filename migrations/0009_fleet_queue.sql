-- OSRIVA Panel: server groups, fleet task batches, job queue, leases (Phase 9).
-- See docs/adr/0012-fleet-groups-queue.md.

CREATE TABLE server_groups (
    id              uuid PRIMARY KEY,
    organization_id uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    name            text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    description     text NOT NULL DEFAULT '' CHECK (length(description) <= 500),
    created_by      uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organization_id, name)
);

CREATE TABLE server_group_members (
    group_id  uuid NOT NULL REFERENCES server_groups (id) ON DELETE CASCADE,
    server_id uuid NOT NULL REFERENCES servers (id) ON DELETE CASCADE,
    PRIMARY KEY (group_id, server_id)
);

CREATE INDEX server_group_members_server_idx ON server_group_members (server_id);

-- An alert rule targets one server, one group, or (both NULL) every server.
-- RESTRICT: deleting a group must not silently disable or widen a rule.
ALTER TABLE alert_rules ADD COLUMN group_id uuid REFERENCES server_groups (id) ON DELETE RESTRICT;
ALTER TABLE alert_rules ADD CONSTRAINT alert_rules_single_target CHECK (server_id IS NULL OR group_id IS NULL);

-- Notifications still being delivered by the job queue.
ALTER TABLE alert_events ADD COLUMN pending integer NOT NULL DEFAULT 0;

CREATE TABLE task_batches (
    id              uuid PRIMARY KEY,
    organization_id uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    type            text NOT NULL,
    params          jsonb NOT NULL,             -- redacted
    group_id        uuid REFERENCES server_groups (id) ON DELETE SET NULL,
    target          text NOT NULL,              -- display: group name or "N servers"
    skipped         jsonb NOT NULL DEFAULT '[]', -- [{server_id, name, reason}]
    requested_by    uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at      timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX task_batches_org_idx ON task_batches (organization_id, created_at DESC);

ALTER TABLE agent_tasks ADD COLUMN batch_id uuid REFERENCES task_batches (id) ON DELETE SET NULL;
CREATE INDEX agent_tasks_batch_idx ON agent_tasks (batch_id) WHERE batch_id IS NOT NULL;

-- Durable background jobs, claimed with FOR UPDATE SKIP LOCKED. Payloads hold ids, never secrets.
CREATE TABLE jobs (
    id           uuid PRIMARY KEY,
    kind         text NOT NULL,
    payload      jsonb NOT NULL,
    status       text NOT NULL CHECK (status IN ('queued', 'running', 'succeeded', 'failed')),
    attempts     integer NOT NULL DEFAULT 0,
    max_attempts integer NOT NULL CHECK (max_attempts BETWEEN 1 AND 50),
    run_at       timestamptz NOT NULL,
    locked_until timestamptz,
    last_error   text NOT NULL DEFAULT '',
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX jobs_due_idx ON jobs (run_at) WHERE status = 'queued';
CREATE INDEX jobs_lease_idx ON jobs (locked_until) WHERE status = 'running';
CREATE INDEX jobs_finished_idx ON jobs (updated_at) WHERE status IN ('succeeded', 'failed');

-- Leases let exactly one API process run each periodic loop.
CREATE TABLE leases (
    name       text PRIMARY KEY,
    holder     text NOT NULL,
    expires_at timestamptz NOT NULL
);
