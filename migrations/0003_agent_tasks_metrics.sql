-- OSRIVA Panel: typed agent tasks and live metric samples.
-- See docs/adr/0005-agent-tasks-and-metrics.md.

CREATE TABLE agent_tasks (
    id           uuid PRIMARY KEY,
    server_id    uuid NOT NULL REFERENCES servers (id) ON DELETE CASCADE,
    type         text NOT NULL CHECK (type ~ '^[a-z]+(\.[a-z_]+)+$'),
    params       jsonb NOT NULL DEFAULT '{}'::jsonb,
    status       text NOT NULL DEFAULT 'queued'
                 CHECK (status IN ('queued', 'running', 'succeeded', 'failed', 'expired')),
    requested_by uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    expires_at   timestamptz NOT NULL,
    started_at   timestamptz,
    finished_at  timestamptz,
    result       jsonb,
    error        text NOT NULL DEFAULT '',
    CHECK ((status IN ('succeeded', 'failed', 'expired')) = (finished_at IS NOT NULL))
);

CREATE INDEX agent_tasks_queue_idx ON agent_tasks (server_id, created_at) WHERE status = 'queued';
CREATE INDEX agent_tasks_running_idx ON agent_tasks (started_at) WHERE status = 'running';
CREATE INDEX agent_tasks_server_idx ON agent_tasks (server_id, created_at DESC);

-- Raw samples; pruned after 24 h. Long-term retention arrives with Phase 8.
CREATE TABLE metric_samples (
    server_id uuid NOT NULL REFERENCES servers (id) ON DELETE CASCADE,
    ts        timestamptz NOT NULL,
    data      jsonb NOT NULL,
    PRIMARY KEY (server_id, ts)
);

CREATE INDEX metric_samples_ts_idx ON metric_samples (ts);
