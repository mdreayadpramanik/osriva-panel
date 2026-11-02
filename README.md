# OSRIVA Panel

**Open Source Resource & Infrastructure Virtualization Administration Panel**
*One Platform. Every Server. Total Control.*

OSRIVA Panel is a free and open-source (AGPL-3.0) platform for managing servers, VPSs,
websites, CMSs, applications and hosting infrastructure from the browser. It has two
operating modes, **Personal Server Mode** and **Hosting Provider Mode**, plus a separate
**Customer Panel**. It has no paid editions, no license keys and no billing.

> **Status: early development (Phase 2a of 14).** What works today: authentication,
> sessions, TOTP two-factor, role-based access control, user management, the audit log,
> panel settings, and **connecting servers**: the OSRIVA Agent enrols securely and reports
> each server's real OS, hardware, storage and network, plus a compatibility assessment.
> Managing servers (services, files, deployment, CMS, Docker, backups, monitoring) and
> provider features **do not exist yet**. See [DEVELOPMENT_STATUS.md](DEVELOPMENT_STATUS.md).
> OSRIVA Panel is **not production-ready**.

## Repository

| Path | What |
|------|------|
| `services/api` | Core API (Go): identity, RBAC, audit, settings |
| `apps/dashboard` | Web dashboard (Next.js + TypeScript + Tailwind) |
| `migrations` | PostgreSQL migrations (embedded into the API binary) |
| `infrastructure` | Dockerfiles and the Caddy reverse proxy config |
| `docs` | Architecture decisions, schema design |

Read [ARCHITECTURE.md](ARCHITECTURE.md) first.

## Development setup

Requirements: **Go 1.26+**, **Node.js 20.9+** (22 recommended), and **PostgreSQL 16+**
(optional for UI work, see below).

```bash
npm install
```

### Option A: run without PostgreSQL (in-memory store, data lost on restart)

Create `.env.dev.local` in the repository root (it is git-ignored):

```ini
OSRIVA_ENV=development
OSRIVA_DEV_MEMORY_STORE=true
OSRIVA_SECRET_KEY=<output of: go run ./services/api/cmd/osriva-api gen-secret>
OSRIVA_TRUSTED_PROXIES=127.0.0.1,::1
OSRIVA_LOG_FORMAT=text
OSRIVA_DEV_ADMIN_EMAIL=dev@osriva.localhost
OSRIVA_DEV_ADMIN_PASSWORD=<at least 12 characters>
```

Then, in two terminals:

```bash
node scripts/dev-api.mjs
```

```bash
npm run dev:dashboard
```

Open http://localhost:3000 and sign in with the dev owner.

### Option B: with PostgreSQL

```bash
createdb osriva
```

Put `OSRIVA_DATABASE_URL=postgres://…/osriva` in `.env.dev.local` instead of
`OSRIVA_DEV_MEMORY_STORE`, then:

```bash
go run ./services/api/cmd/osriva-api migrate
```

```bash
go run ./services/api/cmd/osriva-api admin create --email you@example.com
```

`admin create` prints a generated password once (or use `--password-stdin`).

### Tests

```bash
go test ./...
```

```bash
npm run typecheck
```

The PostgreSQL integration tests run only when `OSRIVA_TEST_DATABASE_URL` is set, and they
**drop and recreate the `public` schema** of that database. Point it only at a throwaway database.

## Connecting a server (OSRIVA Agent)

1. Start the API with the agent endpoint configured: `OSRIVA_PUBLIC_URL`,
   `OSRIVA_AGENT_ADDR` and `OSRIVA_AGENT_PUBLIC_URL` (see `.env.example`). For local
   development, `http://127.0.0.1:8080`, `127.0.0.1:8443` and `https://127.0.0.1:8443` work.
2. Build the agent for the target machine, for example:

   ```bash
   GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o osriva-agent ./agent/cmd/osriva-agent
   ```

   `make agents` builds every supported platform into `dist/`.
3. In the dashboard: **Servers → Add server**. Copy the one-time command and run it on
   the server as root / Administrator, then start reporting:

   ```bash
   osriva-agent run
   ```

Check what the agent would report without any network access:

```bash
osriva-agent inventory --capabilities
```

The agent must be able to reach the agent port directly. Open it in your firewall
yourself; OSRIVA does not change firewall rules. Running it as a system service,
and a one-line installer, arrive in Phase 2b.

## Production (single server, Docker Compose)

```bash
cp .env.example .env
```

Set `OSRIVA_SECRET_KEY`, `POSTGRES_PASSWORD` and `OSRIVA_DOMAIN`, then:

```bash
docker compose up -d --build
```

```bash
docker compose run --rm api admin create --email you@example.com
```

Only Caddy publishes ports (80/443), and it obtains TLS certificates automatically for a
real domain. PostgreSQL and the API are on an internal network. Open ports 80 and 443 in
your firewall yourself. OSRIVA never changes firewall rules on its own.

> The Compose stack is not yet verified end-to-end (no Docker environment was available
> during Phase 1). CI builds both images.

The Universal Installer (`curl … | sh`-style, with OS detection, an installation plan and
rollback) is Phase 2 work and does not exist yet.

## License

AGPL-3.0-only for the platform. Apache-2.0 for the future plugin SDK and API client. See
[docs/adr/0001-license.md](docs/adr/0001-license.md). OSRIVA Panel contains no billing or
payment processing and never will.
