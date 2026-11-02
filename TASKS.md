# OSRIVA Panel — Tasks & roadmap

`[x]` done and tested · `[~]` done but unverified · `[ ]` open

## Phase 1: Foundation

- [x] Repository inspection, monorepo layout, ADRs (license, stack, migrations)
- [x] Core schema migration `0001_core_identity.sql` + full planned schema design (`docs/database-schema.md`)
- [x] Config, logging, secrets, Argon2id, TOTP, sessions, CSRF, rate limiting
- [x] RBAC catalog, built-in roles, anti-escalation, audit log
- [x] User management, sessions, MFA, settings API + OpenAPI spec
- [x] Dashboard core (login, overview, users, audit, account, settings, roadmap, theming)
- [x] PostgreSQL store + migrator (contract suite passed in CI on PostgreSQL 17)
- [~] Docker Compose stack (images build in CI; `docker compose up` not yet verified)
- [ ] Add verbatim AGPL-3.0 / Apache-2.0 license texts
- [ ] OpenAPI ↔ handler contract test; generate `packages/api-client` from the spec
- [ ] Playwright e2e: login, MFA, user CRUD, audit
- [ ] ESLint for the dashboard; `govulncheck` in CI
- [ ] Admin password reset, MFA recovery codes, TOTP QR code
- [ ] Audit log CSV export

## Phase 2a: Agent, enrolment, inventory (done)

- [x] `agent/` CLI: inventory, enroll, run, status; atomic credential storage
- [x] Enrolment: one-time token → CSR → 30-day mTLS client cert from the internal CA; CA pinned before the token is sent
- [x] Agent ↔ control plane: HTTPS over mutual TLS, reports every 60 s, renewal, revocation (ADR-0004)
- [x] Inventory: Linux procfs/sysfs, Windows Win32/registry (verified on Windows 11), BSD sysctl (compiles only)
- [x] Compatibility engine with the 6 statuses, reasons and alternatives
- [x] Migration `0002_servers_agents.sql` + store contract tests
- [x] Dashboard: Servers list, Add-server flow, server detail with capabilities
- [x] Linux inventory on Ubuntu (CI runner, `0674191`); `linux/amd64` added to `compat.testedPlatforms`
- [ ] Linux inventory on other distributions and on ARM64
- [ ] BSD inventory verified on real FreeBSD/OpenBSD/NetBSD; OpenBSD/NetBSD disk inventory
- [ ] Delete server (pending/revoked)

## Phase 2b: Installer & agent service (done)

- [x] Agent service: systemd (Ubuntu, CI) and Windows SCM (Server 2022, CI); restrictive config dir (0700 / protected DACL)
- [~] OpenRC and FreeBSD rc.d services (generated and unit-tested, never run)
- [x] `installer/osriva-install.sh`: plan → approval → install → verify (Ubuntu 24.04, CI)
- [x] Installer commands: install, upgrade (verified `pg_dump`, auto-rollback), repair, rollback, backup, verify, uninstall [--purge]
- [x] Panel serves agent builds + SHA-256 and the install.sh / install.ps1 one-liners shown in the Add-server dialog
- [x] Docker Compose stack verified end to end (Ubuntu 24.04 CI runner)
- [ ] Installer on Debian, RHEL family, SUSE; `--install-docker` beyond Ubuntu
- [ ] Let's Encrypt with a real domain on a long-lived VPS
- [ ] Signed release artifacts + checksums (cosign / minisign), GitHub release workflow, published container images
- [ ] Agent self-update from the panel

## Phase 3: Server management (next)

- [ ] Typed, allowlisted agent tasks over the mTLS channel (control plane → agent), with audit and real exit codes
- [ ] Live metrics: CPU %, memory, disk I/O, network throughput, load; retention
- [ ] Service management (list/start/stop/restart) via systemd, OpenRC, Windows SCM, rc.d
- [ ] Process list, system logs (journald / Event Log), users and groups, SSH keys, cron, hostname and timezone (dangerous ops behind re-auth)
- [ ] File manager with path confinement, upload/download, permission editing

## Phases 3–14 (summary; expanded when each phase starts)

3. Live server dashboard (metrics from the agent), services, users/groups, SSH keys, logs, processes, cron, file manager with path confinement
4. Docker manager via the agent, Git/ZIP/Dockerfile/Compose deployments, runtime adapters, build logs, rollback
5. Websites/vhosts, reverse proxy, ACME SSL, DNS adapters
6. CMS catalog + adapters (WordPress via WP-CLI first), staging/clone/migrate, verified downloads
7. Database engines, storage, encrypted and verified backups/restores
8. Metrics retention, alerts, SMTP/webhook notifications, automation, audit retention
9. Multi-server fleet, server groups, Redis-backed rate limiting/queues
10. Provider Mode: data centers, sites, racks, nodes, pools, virtualization adapters (Proxmox first), VPS lifecycle
11. Hosting backends, technical templates (no prices), signed provisioning API (HMAC, idempotency, replay protection)
12. Customer Panel with tenant isolation
13. Out-of-process plugin SDK (Apache-2.0), adapter docs
14. Security audit, performance and compatibility testing, release
