-- OSRIVA Panel: applications and their deployments (Phase 4b).
-- See docs/adr/0007-deployments.md.

-- Each task carries its own run timeout (deployments run far longer than listings).
ALTER TABLE agent_tasks ADD COLUMN run_timeout_s integer NOT NULL DEFAULT 300
    CHECK (run_timeout_s BETWEEN 10 AND 7200);

CREATE TABLE apps (
    id             uuid PRIMARY KEY,
    server_id      uuid NOT NULL REFERENCES servers (id) ON DELETE CASCADE,
    name           text NOT NULL CHECK (name ~ '^[a-z0-9][a-z0-9-]{0,39}$'),
    git_url        text NOT NULL,
    git_ref        text NOT NULL,
    build_mode     text NOT NULL CHECK (build_mode IN ('dockerfile', 'static', 'node')),
    build_context  text NOT NULL DEFAULT '',
    dockerfile     text NOT NULL DEFAULT '',
    container_port integer NOT NULL CHECK (container_port BETWEEN 1 AND 65535),
    host_port      integer NOT NULL CHECK (host_port BETWEEN 1 AND 65535),
    public         boolean NOT NULL DEFAULT false,
    -- Environment variables as AES-256-GCM ciphertext (bound to the app id); never stored in clear.
    env_enc        bytea,
    created_by     uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    UNIQUE (server_id, name),
    UNIQUE (server_id, host_port)
);

CREATE TABLE deployments (
    id          uuid PRIMARY KEY,
    app_id      uuid NOT NULL REFERENCES apps (id) ON DELETE CASCADE,
    task_id     uuid REFERENCES agent_tasks (id) ON DELETE SET NULL,
    kind        text NOT NULL CHECK (kind IN ('deploy', 'rollback')),
    rollback_of uuid REFERENCES deployments (id) ON DELETE SET NULL,
    image       text NOT NULL,
    git_ref     text NOT NULL DEFAULT '',
    created_by  uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX deployments_app_idx ON deployments (app_id, created_at DESC);
