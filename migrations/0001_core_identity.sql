-- OSRIVA Panel: core identity, access control, sessions, audit and settings.
-- Forward-only (see docs/adr/0003-migrations-forward-only.md). Never edit after release.

CREATE TABLE organizations (
    id          uuid PRIMARY KEY,
    slug        text NOT NULL UNIQUE CHECK (slug ~ '^[a-z0-9][a-z0-9-]{0,62}$'),
    name        text NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    kind        text NOT NULL DEFAULT 'operator' CHECK (kind IN ('operator', 'customer')),
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

INSERT INTO organizations (id, slug, name, kind)
VALUES ('00000000-0000-4000-8000-000000000001', 'default', 'Default Organization', 'operator');

CREATE TABLE users (
    id                  uuid PRIMARY KEY,
    organization_id     uuid NOT NULL REFERENCES organizations (id) ON DELETE RESTRICT,
    email               text NOT NULL CHECK (length(email) BETWEEN 3 AND 320),
    display_name        text NOT NULL CHECK (length(display_name) BETWEEN 1 AND 200),
    password_hash       text NOT NULL,
    status              text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
    totp_secret_enc     bytea,
    totp_enabled        boolean NOT NULL DEFAULT false,
    totp_last_step      bigint NOT NULL DEFAULT 0,
    password_changed_at timestamptz NOT NULL DEFAULT now(),
    last_login_at       timestamptz,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),
    CHECK (NOT totp_enabled OR totp_secret_enc IS NOT NULL)
);

CREATE UNIQUE INDEX users_email_lower_key ON users (lower(email));
CREATE INDEX users_organization_idx ON users (organization_id);

CREATE TABLE permissions (
    key         text PRIMARY KEY CHECK (key ~ '^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$'),
    description text NOT NULL
);

CREATE TABLE roles (
    id              uuid PRIMARY KEY,
    organization_id uuid REFERENCES organizations (id) ON DELETE CASCADE,
    key             text NOT NULL CHECK (key ~ '^[a-z][a-z0-9_-]{0,62}$'),
    name            text NOT NULL,
    description     text NOT NULL DEFAULT '',
    built_in        boolean NOT NULL DEFAULT false,
    created_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organization_id, key),
    CHECK (built_in = (organization_id IS NULL))
);

CREATE UNIQUE INDEX roles_builtin_key ON roles (key) WHERE organization_id IS NULL;

CREATE TABLE role_permissions (
    role_id        uuid NOT NULL REFERENCES roles (id) ON DELETE CASCADE,
    permission_key text NOT NULL REFERENCES permissions (key) ON DELETE CASCADE,
    PRIMARY KEY (role_id, permission_key)
);

CREATE TABLE user_roles (
    user_id    uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    role_id    uuid NOT NULL REFERENCES roles (id) ON DELETE CASCADE,
    granted_by uuid REFERENCES users (id) ON DELETE SET NULL,
    granted_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, role_id)
);

CREATE INDEX user_roles_role_idx ON user_roles (role_id);

CREATE TABLE sessions (
    id                 uuid PRIMARY KEY,
    user_id            uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    token_hash         bytea NOT NULL UNIQUE CHECK (length(token_hash) = 32),
    created_at         timestamptz NOT NULL DEFAULT now(),
    expires_at         timestamptz NOT NULL,
    last_seen_at       timestamptz NOT NULL DEFAULT now(),
    reauthenticated_at timestamptz,
    revoked_at         timestamptz,
    ip                 inet,
    user_agent         text NOT NULL DEFAULT ''
);

CREATE INDEX sessions_user_active_idx ON sessions (user_id) WHERE revoked_at IS NULL;
CREATE INDEX sessions_expires_idx ON sessions (expires_at);

CREATE TABLE audit_logs (
    id              bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    organization_id uuid REFERENCES organizations (id) ON DELETE SET NULL,
    actor_user_id   uuid REFERENCES users (id) ON DELETE SET NULL,
    actor_label     text NOT NULL DEFAULT '',
    action          text NOT NULL,
    target_type     text NOT NULL DEFAULT '',
    target_id       text NOT NULL DEFAULT '',
    outcome         text NOT NULL CHECK (outcome IN ('success', 'failure', 'denied')),
    ip              inet,
    user_agent      text NOT NULL DEFAULT '',
    request_id      text NOT NULL DEFAULT '',
    metadata        jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at      timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX audit_logs_org_created_idx ON audit_logs (organization_id, id DESC);
CREATE INDEX audit_logs_actor_idx ON audit_logs (actor_user_id, id DESC);
CREATE INDEX audit_logs_action_idx ON audit_logs (action, id DESC);

CREATE TABLE settings (
    key        text PRIMARY KEY CHECK (key ~ '^[a-z][a-z0-9_.]*$'),
    value      jsonb NOT NULL,
    updated_by uuid REFERENCES users (id) ON DELETE SET NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);
