-- OSRIVA Panel: Provider Mode infrastructure and VPS (Phase 10).
-- See docs/adr/0013-provider-mode-libvirt.md. There are deliberately no billing tables.

CREATE TABLE datacenters (
    id              uuid PRIMARY KEY,
    organization_id uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    name            text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    location        text NOT NULL DEFAULT '' CHECK (length(location) <= 200),
    created_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organization_id, name)
);

CREATE TABLE racks (
    id            uuid PRIMARY KEY,
    datacenter_id uuid NOT NULL REFERENCES datacenters (id) ON DELETE RESTRICT,
    name          text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    units         integer NOT NULL CHECK (units BETWEEN 1 AND 100),
    created_at    timestamptz NOT NULL DEFAULT now(),
    UNIQUE (datacenter_id, name)
);

-- A node is an enrolled server used as a libvirt/KVM hypervisor.
CREATE TABLE nodes (
    id                uuid PRIMARY KEY,
    organization_id   uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    server_id         uuid NOT NULL UNIQUE REFERENCES servers (id) ON DELETE RESTRICT,
    datacenter_id     uuid NOT NULL REFERENCES datacenters (id) ON DELETE RESTRICT,
    rack_id           uuid REFERENCES racks (id) ON DELETE RESTRICT,
    hypervisor        text NOT NULL CHECK (hypervisor IN ('libvirt')),
    network           text NOT NULL,          -- existing libvirt network the VMs join
    cpu_overcommit    double precision NOT NULL CHECK (cpu_overcommit BETWEEN 1 AND 16),
    memory_overcommit double precision NOT NULL CHECK (memory_overcommit BETWEEN 1 AND 4),
    enabled           boolean NOT NULL DEFAULT true,
    created_at        timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE ip_pools (
    id              uuid PRIMARY KEY,
    organization_id uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    datacenter_id   uuid REFERENCES datacenters (id) ON DELETE RESTRICT,
    name            text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    cidr            cidr NOT NULL,
    gateway         inet NOT NULL,
    first_address   inet NOT NULL,
    last_address    inet NOT NULL,
    dns             text[] NOT NULL DEFAULT '{}',
    created_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organization_id, name)
);

CREATE TABLE vm_templates (
    id              uuid PRIMARY KEY,
    organization_id uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    name            text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    image_url       text NOT NULL,
    image_sha256    text NOT NULL CHECK (image_sha256 ~ '^[0-9a-f]{64}$'),
    min_disk_gb     integer NOT NULL CHECK (min_disk_gb BETWEEN 1 AND 4096),
    default_user    text NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organization_id, name)
);

CREATE TABLE vps (
    id              uuid PRIMARY KEY,
    organization_id uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    node_id         uuid NOT NULL REFERENCES nodes (id) ON DELETE RESTRICT,
    template_id     uuid REFERENCES vm_templates (id) ON DELETE SET NULL,
    pool_id         uuid NOT NULL REFERENCES ip_pools (id) ON DELETE RESTRICT,
    name            text NOT NULL,
    vcpus           integer NOT NULL CHECK (vcpus BETWEEN 1 AND 64),
    memory_mb       integer NOT NULL CHECK (memory_mb BETWEEN 256 AND 262144),
    disk_gb         integer NOT NULL CHECK (disk_gb BETWEEN 1 AND 4096),
    ip_address      inet NOT NULL,
    ssh_keys        text[] NOT NULL DEFAULT '{}',
    state           text NOT NULL CHECK (state IN ('creating', 'running', 'stopped', 'error', 'deleting')),
    pending_task_id uuid REFERENCES agent_tasks (id) ON DELETE SET NULL,
    pending_op      text NOT NULL DEFAULT '',
    last_error      text NOT NULL DEFAULT '',
    created_by      uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organization_id, name)
);

-- Allocated addresses; the primary key makes concurrent allocation safe.
CREATE TABLE ip_addresses (
    pool_id    uuid NOT NULL REFERENCES ip_pools (id) ON DELETE RESTRICT,
    address    inet NOT NULL,
    vps_id     uuid NOT NULL UNIQUE REFERENCES vps (id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (pool_id, address)
);
