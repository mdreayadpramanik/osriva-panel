-- OSRIVA Panel: plugin approvals and plugin notification channels (Phase 13).
-- See docs/adr/0016-plugins.md.

-- An approval pins the SHA-256 of the plugin binary; a changed binary does not run.
CREATE TABLE plugins (
    name            text PRIMARY KEY CHECK (name ~ '^[a-z0-9][a-z0-9-]{0,63}$'),
    enabled         boolean NOT NULL,
    approved_sha256 text NOT NULL CHECK (approved_sha256 ~ '^[0-9a-f]{64}$'),
    approved_by     uuid REFERENCES users (id) ON DELETE SET NULL,
    approved_at     timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE notification_channels DROP CONSTRAINT notification_channels_kind_check;
ALTER TABLE notification_channels ADD CONSTRAINT notification_channels_kind_check CHECK (kind IN ('webhook', 'email', 'plugin'));
