-- OSRIVA Panel: customers, their sessions, VPS ownership (Phase 12).
-- See docs/adr/0015-customer-panel.md. Customers are not users and hold no permissions.

CREATE TABLE customers (
    id               uuid PRIMARY KEY,
    organization_id  uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    email            text NOT NULL UNIQUE CHECK (email = lower(email) AND length(email) BETWEEN 3 AND 320),
    name             text NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    status           text NOT NULL CHECK (status IN ('active', 'disabled')),
    password_hash    text NOT NULL DEFAULT '',   -- empty until the customer uses the setup link
    setup_token_hash bytea UNIQUE,
    setup_expires_at timestamptz,
    created_by       uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at       timestamptz NOT NULL DEFAULT now(),
    last_login_at    timestamptz
);

CREATE TABLE customer_sessions (
    id           uuid PRIMARY KEY,
    customer_id  uuid NOT NULL REFERENCES customers (id) ON DELETE CASCADE,
    token_hash   bytea NOT NULL UNIQUE,
    ip           text NOT NULL DEFAULT '',
    user_agent   text NOT NULL DEFAULT '',
    created_at   timestamptz NOT NULL DEFAULT now(),
    expires_at   timestamptz NOT NULL,
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    revoked_at   timestamptz
);

CREATE INDEX customer_sessions_customer_idx ON customer_sessions (customer_id);

ALTER TABLE vps ADD COLUMN customer_id uuid REFERENCES customers (id) ON DELETE SET NULL;
CREATE INDEX vps_customer_idx ON vps (customer_id) WHERE customer_id IS NOT NULL;
