# OSRIVA Panel

**Open Source Resource & Infrastructure Virtualization Administration Panel**
*One Platform. Every Server. Total Control.*

OSRIVA Panel is a free and open-source (AGPL-3.0) platform for managing servers, VPSs,
websites, CMSs, applications and hosting infrastructure from the browser. It has two
operating modes, **Personal Server Mode** and **Hosting Provider Mode**, plus a separate
**Customer Panel**. It has no paid editions, no license keys and no billing.

> **Status: v1.0.0, a full release on the owner's decision (all 14 phases have a first part, plus production hardening).**
> Servers are connected through the OSRIVA Agent and managed from the browser:
> * **Basics:** metrics, logs, services, processes, files, SSH keys and host settings.
> * **Apps and sites:** Docker, Git deployments, websites behind Caddy with automatic HTTPS, WordPress.
> * **Data and alerts:** encrypted backups with restore, and alerts by webhook, e-mail or plugin.
> * **Fleet:** server groups and batches.
> * **Hosting Provider Mode:** KVM VPS, a signed billing API and a Customer Panel.
> * **Panel installer:** backup, restore, upgrade and rollback of the panel itself.
>
> Exactly what is tested, and what is not, is in [DEVELOPMENT_STATUS.md](DEVELOPMENT_STATUS.md).
> Before running it for customers, work through [docs/production-checklist.md](docs/production-checklist.md)
> (a soak test on real hosts and an independent security review).

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

## Installing the control plane on a Linux server

The installer runs from a checkout of this repository and deploys PostgreSQL, the API,
the dashboard and Caddy with Docker Compose. It needs Docker Engine with the Compose v2
plugin (on Ubuntu it can install them from Ubuntu's own packages with `--install-docker`).

See what it would do first. `plan` changes nothing:

```bash
sudo ./installer/osriva-install.sh plan --domain panel.example.com --agent
```

Install (asks for confirmation, then prints the owner's generated password once):

```bash
sudo ./installer/osriva-install.sh install --domain panel.example.com --agent --admin-email you@example.com
```

Day-two operations:

```bash
sudo ./installer/osriva-install.sh verify
```

```bash
sudo ./installer/osriva-install.sh upgrade --source /path/to/newer/checkout
```

`upgrade` takes a verified database backup first and rolls back automatically if the new
release fails its checks. `backup`, `rollback`, `restore --from DIR`, `repair` and
`uninstall [--purge]` are also available (`--help`). Configuration and the secret key live
in `/etc/osriva/osriva.env` (mode 0600). Back it up. Backups, restores on a new host and
upgrades are described in [docs/operations.md](docs/operations.md).

The installer never changes firewall, SSH, disk or network settings. Open 80/tcp and
443/tcp (and 8443/tcp with `--agent`) yourself. For a real domain, Caddy obtains a
Let's Encrypt certificate automatically. `localhost` uses Caddy's local CA.

## Connecting a server (OSRIVA Agent)

1. The control plane needs the agent endpoint enabled (`--agent` in the installer, or
   `OSRIVA_PUBLIC_URL`, `OSRIVA_AGENT_ADDR` and `OSRIVA_AGENT_PUBLIC_URL`; see `.env.example`).
2. In the dashboard: **Servers → Add server**. Copy the one-line command for the server's
   OS and run it there:
   * **Linux / BSD** (with sudo): downloads the agent from your panel, checks its SHA-256,
     enrols, and installs a systemd / OpenRC / rc.d service.
   * **Windows** (elevated PowerShell): the same, as a Windows service.
3. The server shows as online within a minute.

The agent first checks that the panel's CA matches the fingerprint in the command, and
only then sends the one-time token. It reports inventory every 60 s and does not accept
commands yet (Phase 3). Without the one-liner:

```bash
osriva-agent enroll --panel-url https://panel.example.com --token TOKEN --ca-fingerprint sha256:...
```

```bash
sudo osriva-agent service install
```

Check what the agent would report, without any network access:

```bash
osriva-agent inventory --capabilities
```

Agents connect directly to the agent port (8443). Open it in your firewall yourself.

## License

AGPL-3.0-only for the platform. Apache-2.0 for the plugin SDK (`pkg/pluginsdk`, see [docs/plugins.md](docs/plugins.md)) and the future API client. See
[docs/adr/0001-license.md](docs/adr/0001-license.md). OSRIVA Panel contains no billing or
payment processing and never will.
