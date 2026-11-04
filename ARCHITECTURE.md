# OSRIVA Panel — Architecture

> Open Source Resource & Infrastructure Virtualization Administration Panel.
> *One Platform. Every Server. Total Control.*

This document is the source of truth for how OSRIVA Panel is put together. It
records the current state, not aspirations; planned components are marked
**(planned)**. Significant decisions are recorded as ADRs in [`docs/adr/`](docs/adr/).

## 1. Guiding principles

1. **Free and open source, no billing.** No payment, invoice, subscription or
   pricing code or tables, ever. External billing systems integrate through the
   provisioning API (planned, Phase 11). See [ADR-0001](docs/adr/0001-license.md).
2. **Real functionality only.** A feature appears in the UI only when the
   backend implements it. Unimplemented modules are listed on the dashboard's
   Roadmap page, labelled as planned, never as working.
3. **The control plane never runs arbitrary code on hosts.** Privileged work is
   done by the OSRIVA Agent (planned, Phase 2) through an allowlisted, audited
   task protocol. The Core API never shells out on a managed server.
4. **Capability detection over assumption.** OS/runtime/CMS support is detected
   and reported with explicit statuses (`SUPPORTED`, `PARTIALLY_SUPPORTED`,
   `UNSUPPORTED`, `NOT_TESTED`, `DEPENDENCY_MISSING`, `REQUIRES_CONFIGURATION`).
5. **Secure by default.** Least privilege, safe defaults, every privileged
   action audited, dangerous actions require recent re-authentication.

## 2. Component map

```
                    ┌──────────────────────────┐
 Browser ──HTTPS──▶ │ Reverse proxy (Caddy)    │
                    └──────┬────────────┬──────┘
                    /api/* │            │ /*
                 ┌─────────▼───┐   ┌────▼──────────────┐
                 │ Core API    │   │ Dashboard (Next.js)│
                 │ (Go)        │   └───────────────────┘
                 └──┬───────┬──┘
                    │       │ task queue (planned)
            ┌───────▼──┐  ┌─▼───────────────┐  mTLS (planned)  ┌───────────────┐
            │PostgreSQL│  │ Worker (planned)│ ◀──────────────▶ │ OSRIVA Agent  │
            └──────────┘  └─────────────────┘                  │ on each server│
                                                               └───────────────┘
```

| # | Component | Location | Status |
|---|-----------|----------|--------|
| 1 | Web Dashboard | `apps/dashboard` | Phase 1 implemented |
| 2 | Customer Panel | `apps/customer-panel` | planned (Phase 12) |
| 3 | Core API | `services/api` | Phase 1 implemented |
| 4 | Authentication service | `services/api/internal/identity`, `internal/auth` | Phase 1 implemented (in-process module of the Core API) |
| 5 | Server Agent | `agent/`, `pkg/inventory`, `pkg/agentclient` | Phase 2a implemented: enrolment, inventory, mTLS reports, renewal |
| 6 | Universal Installer | `installer/osriva-install.sh`, agent `install.sh` / `install.ps1` | Phase 2b implemented (control plane: Linux + Docker Compose; agent: Linux/BSD/Windows one-liners) |
| — | Compatibility engine | `pkg/compat` | Phase 2a implemented |
| — | Fleet service (servers, agent CA, enrolment) | `services/api/internal/fleet`, `internal/pki` | Phase 2a implemented |
| 7–20 | Deployment, CMS, Runtime, DB, Website, SSL, Backup, Monitoring, Security, Virtualization, Provisioning, Plugins, Notifications, Webhooks | `services/*`, `adapters/*` | planned (Phases 3–13) |

The Authentication Service is currently a module inside the Core API process
rather than a separate network service. Its boundary is the
`identity.Service` type; it can be split out later without changing callers.

## 3. Repository layout

```
osriva-panel/
├── apps/dashboard/            Next.js admin dashboard (TypeScript, Tailwind)
├── services/api/
│   ├── cmd/osriva-api/        CLI entrypoint: serve, migrate, admin create, gen-secret
│   ├── internal/config/       env-based configuration + validation
│   ├── internal/logging/      structured logging (log/slog)
│   ├── internal/secrets/      AES-256-GCM secret encryption, key derivation, CSRF tokens
│   ├── internal/auth/         Argon2id passwords, TOTP (RFC 6238), session tokens
│   ├── internal/rbac/         permission catalog + built-in roles
│   ├── internal/identity/     authentication, sessions, MFA, user management, audit
│   ├── internal/ratelimit/    per-key token buckets + login lockout
│   ├── internal/reqmeta/      request metadata (request id, client ip, UA) in context
│   ├── internal/store/        persistence interface
│   │   ├── postgres/          PostgreSQL implementation + migrator
│   │   ├── memstore/          in-memory implementation (tests and dev only)
│   │   └── storetest/         contract tests run against every implementation
│   ├── internal/httpapi/      HTTP router, middleware, handlers
│   └── openapi/openapi.yaml   OpenAPI 3.1 spec (served at /api/v1/openapi.yaml)
│   ├── internal/fleet/        servers, agent CA lifecycle, enrolment, agent auth, reports
│   ├── internal/pki/          internal CA: client-cert signing, listener certificate
├── agent/
│   ├── cmd/osriva-agent/      agent CLI: inventory, enroll, run, status
│   └── internal/state/        persisted identity (atomic writes, 0600 key)
├── pkg/                       code shared by the agent and the control plane
│   ├── inventory/             host facts: Linux procfs/sysfs, Win32/registry, BSD sysctl
│   ├── compat/                compatibility engine (6 statuses, reasons, alternatives)
│   ├── agentproto/            wire types and paths
│   └── agentclient/           enrolment with CA pinning, mTLS reports, renewal
├── migrations/                 versioned SQL migrations (embedded into the binary)
├── infrastructure/            Dockerfiles, Caddyfile
├── docs/                      ADRs, schema design, guides
├── docker-compose.yml         production-oriented control-plane stack
└── *.md                       ARCHITECTURE, DEVELOPMENT_STATUS, TASKS, SECURITY, COMPATIBILITY_MATRIX
```

Directories from the master plan that do not exist yet (
`adapters/`, `packages/*`, `apps/customer-panel`) are created in the phase that
first puts real code in them. We do not commit empty scaffolding.

A single Go module at the repository root (`go.mod`) holds all Go code. Each
service keeps its code under its own `internal/`, so Go's visibility rules
enforce the module boundaries.

## 4. Core API

* **Language/runtime:** Go (stdlib `net/http` + `go-chi/chi` router).
* **Persistence:** PostgreSQL via `jackc/pgx/v5`. All access goes through the
  `store.Store` interface, and every implementation must pass the shared contract
  suite in `store/storetest`.
* **Migrations:** plain SQL files in `migrations/`, embedded with `go:embed`,
  applied in order under a PostgreSQL advisory lock, one transaction per file,
  tracked in `schema_migrations`. Run with `osriva-api migrate`. They only move
  forward; rollback means restoring the pre-upgrade backup the installer takes
  (see ADR-0003).
* **Configuration:** environment variables only (`OSRIVA_*`). See `.env.example`.
  Invalid configuration aborts startup with a clear message.
* **Logging:** `log/slog`, JSON by default, one access-log line per request with
  a request ID (`X-Request-ID`). Secrets, tokens and passwords are never logged.
* **API style:** versioned REST under `/api/v1`, JSON, errors as
  `{"error":{"code","message","field?"},"request_id"}`. Spec in
  `services/api/openapi/openapi.yaml`.

### 4.1 Request pipeline

`request-id → client-ip/metadata → panic recovery → access log → security
headers → (/api) per-IP rate limit → [authenticated routes] session auth →
CSRF check (unsafe methods) → permission check → handler`.

### 4.2 Authentication and sessions

* Passwords: Argon2id (`m=64MiB, t=3, p=2`, 16-byte salt), PHC string format,
  transparently rehashed on login when the parameters change. Minimum 12 characters.
* Sessions: 256-bit random token in an `HttpOnly`, `SameSite=Strict`, `Secure`
  (configurable) cookie. Only its SHA-256 hash is stored. Sessions have an
  absolute TTL (default 12h) and an idle timeout (default 2h), and can be revoked
  individually or all at once.
* CSRF: unsafe methods require `X-CSRF-Token` = HMAC-SHA256(csrf-key,
  session-token-hash). JSON bodies must be sent as `application/json`, which forces a CORS
  preflight for cross-origin requests. No CORS is enabled.
* MFA: TOTP (RFC 6238, SHA-1, 6 digits, 30s, ±1 step). Secrets are encrypted
  with AES-256-GCM, with the user ID bound as associated data. A code cannot be
  used twice (`totp_last_step`).
* Re-authentication ("sudo mode"): dangerous operations (role changes, disabling
  users, panel-mode change, MFA disable) require a password confirmation within
  the last 10 minutes (`OSRIVA_REAUTH_WINDOW`).
* Brute-force protection: per-IP token bucket on login, plus per-account lockout
  (10 failures in 15 minutes locks the account for 15 minutes). Login takes
  the same time whether or not the account exists (the password is checked
  against a dummy hash when there is no user).

### 4.3 Authorization (RBAC)

* Permissions are dotted strings defined in code (`internal/rbac`) and synced to
  the `permissions` table at startup.
* Built-in roles (`owner`, `admin`, `auditor`, `member`) are synced at startup.
  Custom per-organization roles are supported by the schema; there is no
  editing UI yet.
* A user's effective permissions are the union of their roles' permissions.
* **Anti-escalation rule:** an actor can only create or modify a user, or grant
  a role, if the target's permissions (current and new) are a subset of the
  actor's own. Nobody can disable themselves, and the last active owner cannot
  be demoted or disabled.
* Denied authorization attempts are written to the audit log (`authz.denied`).

### 4.4 Tenancy

Every user belongs to an `organization`. Phase 1 has one default organization
for the operator. Queries are scoped by the actor's organization. Customer
organizations (`kind='customer'`) and strict tenant isolation for the Customer
Panel arrive in Phase 12. The column and check constraint already exist.

## 5. Dashboard

* Next.js (App Router) + React + TypeScript + Tailwind CSS v4.
* Talks to the API same-origin (`/api/*`). In development Next.js rewrites
  `/api/*` to the Go API. In production the reverse proxy routes it.
* No secrets in frontend code. The CSRF token comes from `/api/v1/auth/me` and
  is kept in memory.
* Light/dark theme (system default, user override). Responsive layout.
* The navigation lists only implemented modules. Planned modules are shown on
  the Roadmap page with their phase and are not clickable as features.

## 6. Data model

Implemented tables: `organizations`, `users`, `permissions`, `roles`,
`role_permissions`, `user_roles`, `sessions`, `audit_logs`, `settings`
(`0001_core_identity.sql`); `agent_ca`, `servers`, `agent_certificates`,
`server_inventory` (`0002_servers_agents.sql`); plus `schema_migrations`.

The complete planned schema for all phases, including servers, agents,
capabilities, applications, CMS, domains, SSL, backups, monitoring, provider
infrastructure, customers, provisioning and API credentials, is designed in
[`docs/database-schema.md`](docs/database-schema.md). Each phase adds its tables
in its own migration. There are **no** billing/payment/invoice/subscription tables, by design.

## 7. Deployment topology

* **Development:** `go run ./services/api/cmd/osriva-api serve` + `npm run dev`
  (see README). An in-memory store is available *only* with
  `OSRIVA_ENV=development` and `OSRIVA_DEV_MEMORY_STORE=true`. It logs a loud
  warning and loses all data on restart.
* **Single server:** `docker-compose.yml`: Caddy (only public service, 80/443),
  dashboard, API, a one-shot migrator, and PostgreSQL on an internal network
  with no published ports. Redis is not included: the job queue uses PostgreSQL, and
  Redis (`OSRIVA_REDIS_URL`, optional) only shares rate limits between API instances.
* **Multi-instance / HA:** prepared, not tested end to end. Periodic loops hold a
  PostgreSQL lease so only one instance runs them; jobs are claimed with `SKIP LOCKED`;
  rate limits are shared through Redis when configured. Long-poll wake-ups are still
  per instance (up to 25 s delay). High availability is **not** claimed (ADR-0012).

## 8. Planned architecture (summary)

* **Agent (Phase 2a, implemented):** see [ADR-0004](docs/adr/0004-agent-enrolment-and-mtls.md).
  The agent dials out to a dedicated mutual-TLS listener (`OSRIVA_AGENT_ADDR`),
  separate from the reverse-proxied API because the API must verify the client
  certificate itself. It reports inventory every 60 s and accepts **no commands**
  yet. Typed, allowlisted tasks with real exit status arrive in Phase 3.
* **Job queue (Phase 9a, implemented):** PostgreSQL `jobs` table claimed with
  `SELECT … FOR UPDATE SKIP LOCKED`, leases and exponential back-off
  ([ADR-0012](docs/adr/0012-fleet-groups-queue.md)). Used for alert notifications.
* **Provider Mode (Phase 10a, implemented):** data centers, racks, hypervisor nodes,
  IPv4 pools and templates in the panel; VPS run on libvirt/KVM nodes through typed
  `vm.*` agent tasks ([ADR-0013](docs/adr/0013-provider-mode-libvirt.md)). Proxmox VE
  and other hypervisors are planned adapters.
* **Provisioning API (Phase 11a, implemented):** billing systems call `/api/v1/provisioning/*`
  with HMAC-signed requests and receive signed webhooks; services are created from
  technical plans through the Provider Mode code path
  ([ADR-0014](docs/adr/0014-provisioning-api.md)).
* **Adapters/plugins (Phase 13):** out-of-process plugins over gRPC/stdio with a
  signed manifest, so third-party code never runs inside the Core API process.
