# OSRIVA Panel — Tasks & roadmap

`[x]` done and tested · `[~]` done but unverified · `[ ]` open

## Phase 1: Foundation

- [x] Repository inspection, monorepo layout, ADRs (license, stack, migrations)
- [x] Core schema migration `0001_core_identity.sql` + full planned schema design (`docs/database-schema.md`)
- [x] Config, logging, secrets, Argon2id, TOTP, sessions, CSRF, rate limiting
- [x] RBAC catalog, built-in roles, anti-escalation, audit log
- [x] User management, sessions, MFA, settings API + OpenAPI spec
- [x] Dashboard core (login, overview, users, audit, account, settings, roadmap, theming)
- [~] PostgreSQL store + migrator (written, needs a real-DB run: push the repo so CI runs)
- [~] Docker Compose stack (written, needs `docker compose up` verification)
- [ ] Add verbatim AGPL-3.0 / Apache-2.0 license texts
- [ ] OpenAPI ↔ handler contract test; generate `packages/api-client` from the spec
- [ ] Playwright e2e: login, MFA, user CRUD, audit
- [ ] ESLint for the dashboard; `govulncheck` in CI
- [ ] Admin password reset, MFA recovery codes, TOTP QR code
- [ ] Audit log CSV export

## Phase 2: Agent & installer (next)

- [ ] `agent/`: Go agent skeleton, config, service install (systemd / OpenRC / Windows SCM / rc.d)
- [ ] Enrolment: one-time token → CSR → short-lived mTLS client cert issued by an OSRIVA CA
- [ ] Agent ↔ control-plane protocol: outbound WebSocket over mTLS, typed allowlisted tasks, results with real exit codes
- [ ] OS/hardware discovery adapters: Linux (`/etc/os-release`, `/proc`, sysfs), Windows (WMI/CIM), BSD (`sysctl`)
- [ ] Compatibility engine with the 6 statuses + reasons + alternatives
- [ ] Migration `0002_servers_agents.sql` (servers, agents, enrolment tokens, OS, capabilities, agent_tasks)
- [ ] Dashboard: Servers list, "Add server" flow (shows the enrolment command), server detail with detected capabilities
- [ ] `installer/`: POSIX sh bootstrap + Go installer; detect → plan → explicit approval → install → verify; upgrade (with `pg_dump`), repair, rollback, uninstall, backup-config
- [ ] Signed release artifacts + checksums (cosign / minisign)

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
