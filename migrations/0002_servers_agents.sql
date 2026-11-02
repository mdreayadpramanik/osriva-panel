-- OSRIVA Panel: managed servers, agent enrolment and certificates, inventory.
-- See docs/adr/0004-agent-enrolment-and-mtls.md.

-- Single-row internal CA. The private key is AES-256-GCM encrypted with the master key.
CREATE TABLE agent_ca (
    id         smallint PRIMARY KEY CHECK (id = 1),
    cert_pem   text NOT NULL,
    key_enc    bytea NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE servers (
    id                     uuid PRIMARY KEY,
    organization_id        uuid NOT NULL REFERENCES organizations (id) ON DELETE RESTRICT,
    name                   text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    -- pending: created, agent not enrolled yet; active: enrolled; revoked: credentials revoked.
    -- Online/offline is derived from last_seen_at and never stored.
    status                 text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'active', 'revoked')),
    enrollment_token_hash  bytea UNIQUE CHECK (enrollment_token_hash IS NULL OR length(enrollment_token_hash) = 32),
    enrollment_expires_at  timestamptz,
    hostname               text NOT NULL DEFAULT '',
    os_family              text NOT NULL DEFAULT '',
    os_name                text NOT NULL DEFAULT '',
    os_version             text NOT NULL DEFAULT '',
    kernel                 text NOT NULL DEFAULT '',
    arch                   text NOT NULL DEFAULT '',
    agent_version          text NOT NULL DEFAULT '',
    last_seen_at           timestamptz,
    last_seen_ip           inet,
    enrolled_at            timestamptz,
    created_by             uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at             timestamptz NOT NULL DEFAULT now(),
    updated_at             timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organization_id, name),
    CHECK ((enrollment_token_hash IS NULL) = (enrollment_expires_at IS NULL))
);

CREATE INDEX servers_organization_idx ON servers (organization_id);

CREATE TABLE agent_certificates (
    serial      text PRIMARY KEY CHECK (serial ~ '^[0-9a-f]{1,40}$'),
    server_id   uuid NOT NULL REFERENCES servers (id) ON DELETE CASCADE,
    fingerprint text NOT NULL UNIQUE,
    not_after   timestamptz NOT NULL,
    issued_at   timestamptz NOT NULL DEFAULT now(),
    revoked_at  timestamptz
);

CREATE INDEX agent_certificates_server_idx ON agent_certificates (server_id);

-- Latest inventory snapshot per server, plus the capabilities computed from it.
CREATE TABLE server_inventory (
    server_id    uuid PRIMARY KEY REFERENCES servers (id) ON DELETE CASCADE,
    collected_at timestamptz NOT NULL,
    received_at  timestamptz NOT NULL DEFAULT now(),
    data         jsonb NOT NULL,
    capabilities jsonb NOT NULL DEFAULT '[]'::jsonb
);
