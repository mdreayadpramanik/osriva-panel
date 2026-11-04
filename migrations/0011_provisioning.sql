-- OSRIVA Panel: signed provisioning API, plans, provisioned services (Phase 11).
-- See docs/adr/0014-provisioning-api.md. Plans carry no prices, by design.

CREATE TABLE api_clients (
    id              uuid PRIMARY KEY,
    organization_id uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    name            text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    key_id          text NOT NULL UNIQUE,
    secret_enc      bytea NOT NULL,            -- AES-256-GCM, bound to the client id
    scopes          text[] NOT NULL,
    allowed_cidrs   cidr[] NOT NULL DEFAULT '{}',
    webhook_url     text NOT NULL DEFAULT '',
    enabled         boolean NOT NULL DEFAULT true,
    created_by      uuid NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
    created_at      timestamptz NOT NULL DEFAULT now(),
    last_used_at    timestamptz,
    UNIQUE (organization_id, name)
);

-- Replay protection: each nonce is accepted once while its timestamp is valid.
CREATE TABLE api_nonces (
    client_id  uuid NOT NULL REFERENCES api_clients (id) ON DELETE CASCADE,
    nonce      text NOT NULL,
    expires_at timestamptz NOT NULL,
    PRIMARY KEY (client_id, nonce)
);

CREATE INDEX api_nonces_expiry_idx ON api_nonces (expires_at);

CREATE TABLE idempotency_keys (
    client_id    uuid NOT NULL REFERENCES api_clients (id) ON DELETE CASCADE,
    key          text NOT NULL,
    request_hash text NOT NULL,
    status_code  integer NOT NULL DEFAULT 0,   -- 0 while the first request is in progress
    response     bytea,
    created_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (client_id, key)
);

CREATE INDEX idempotency_keys_created_idx ON idempotency_keys (created_at);

CREATE TABLE service_plans (
    id              uuid PRIMARY KEY,
    organization_id uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    name            text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    kind            text NOT NULL CHECK (kind IN ('vps')),
    template_id     uuid NOT NULL REFERENCES vm_templates (id) ON DELETE RESTRICT,
    pool_id         uuid NOT NULL REFERENCES ip_pools (id) ON DELETE RESTRICT,
    node_id         uuid REFERENCES nodes (id) ON DELETE RESTRICT, -- NULL: pick a node
    vcpus           integer NOT NULL CHECK (vcpus BETWEEN 1 AND 64),
    memory_mb       integer NOT NULL CHECK (memory_mb BETWEEN 256 AND 262144),
    disk_gb         integer NOT NULL CHECK (disk_gb BETWEEN 1 AND 4096),
    enabled         boolean NOT NULL DEFAULT true,
    created_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organization_id, name)
);

CREATE TABLE provisioned_services (
    id              uuid PRIMARY KEY,
    organization_id uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    client_id       uuid NOT NULL REFERENCES api_clients (id) ON DELETE RESTRICT,
    plan_id         uuid REFERENCES service_plans (id) ON DELETE SET NULL,
    external_id     text NOT NULL CHECK (length(external_id) BETWEEN 1 AND 128),
    vps_id          uuid REFERENCES vps (id) ON DELETE SET NULL,
    status          text NOT NULL CHECK (status IN ('provisioning', 'active', 'suspended', 'failed', 'terminating', 'terminated')),
    pending         text NOT NULL DEFAULT '',  -- operation the reconciler waits for
    last_error      text NOT NULL DEFAULT '',
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (client_id, external_id)
);

CREATE INDEX provisioned_services_pending_idx ON provisioned_services (updated_at) WHERE pending <> '';
