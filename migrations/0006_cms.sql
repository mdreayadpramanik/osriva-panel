-- OSRIVA Panel: CMS instances (Phase 6, WordPress first).
-- See docs/adr/0009-cms-wordpress.md.

CREATE TABLE cms_instances (
    id              uuid PRIMARY KEY,
    server_id       uuid NOT NULL REFERENCES servers (id) ON DELETE CASCADE,
    kind            text NOT NULL CHECK (kind IN ('wordpress')),
    name            text NOT NULL CHECK (name ~ '^[a-z0-9][a-z0-9-]{0,39}$'),
    title           text NOT NULL,
    url             text NOT NULL,
    host_port       integer NOT NULL CHECK (host_port BETWEEN 1 AND 65535),
    admin_user      text NOT NULL,
    admin_email     text NOT NULL,
    install_task_id uuid REFERENCES agent_tasks (id) ON DELETE SET NULL,
    site_id         uuid REFERENCES sites (id) ON DELETE SET NULL,
    created_by      uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (server_id, name),
    UNIQUE (server_id, host_port)
);
