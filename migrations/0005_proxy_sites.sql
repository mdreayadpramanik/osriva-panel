-- OSRIVA Panel: reverse proxy settings and websites per server (Phase 5).
-- See docs/adr/0008-reverse-proxy.md.

CREATE TABLE proxy_settings (
    server_id    uuid PRIMARY KEY REFERENCES servers (id) ON DELETE CASCADE,
    enabled      boolean NOT NULL DEFAULT false,
    http_port    integer NOT NULL DEFAULT 80 CHECK (http_port BETWEEN 1 AND 65535),
    https_port   integer NOT NULL DEFAULT 443 CHECK (https_port BETWEEN 1 AND 65535),
    acme_email   text NOT NULL DEFAULT '',
    acme_staging boolean NOT NULL DEFAULT false,
    updated_at   timestamptz NOT NULL DEFAULT now(),
    CHECK (http_port <> https_port)
);

CREATE TABLE sites (
    id            uuid PRIMARY KEY,
    server_id     uuid NOT NULL REFERENCES servers (id) ON DELETE CASCADE,
    -- One domain is served from one place.
    domain        text NOT NULL UNIQUE CHECK (domain = lower(domain) AND length(domain) <= 253),
    upstream_port integer NOT NULL CHECK (upstream_port BETWEEN 1 AND 65535),
    tls_mode      text NOT NULL CHECK (tls_mode IN ('acme', 'internal', 'none')),
    created_by    uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX sites_server_idx ON sites (server_id, domain);
