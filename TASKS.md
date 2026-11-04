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
- [x] OpenAPI ↔ router contract test (every /api/v1 route documented; Phase 9a)
- [ ] Generate `packages/api-client` from the spec
- [ ] Playwright e2e: login, MFA, user CRUD, audit
- [x] `govulncheck` in CI (Phase 14a)
- [ ] ESLint for the dashboard
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
- [x] Signed release artifacts + checksums (Sigstore keyless), GitHub release workflow: v0.1.0 pre-release published (Phase 14a)
- [ ] Agent self-update from the panel

## Phase 3a: Tasks, metrics, services, processes (done)

- [x] Typed, allowlisted agent tasks over mTLS (long poll), audited, real exit status (ADR-0005)
- [x] Live metrics (CPU, memory, swap, load, network, disks), 24 h retention
- [x] Service management via systemd and Windows SCM; process list (Linux, Windows)
- [ ] OpenRC and BSD service/process adapters; BSD metrics

## Phase 3b: Server management, continued (done)

- [x] System and service logs (journald / Windows Event Log), bounded and filtered (CI run 41494257938)
- [x] Users and groups (view); SSH authorized keys add/remove on Linux, behind servers.access + re-auth (CI run 41497711910)
- [x] Scheduled tasks (cron / Task Scheduler, view); hostname and timezone (CI run 41497711910)
- [x] File manager with agent-side path confinement: list, view, edit, create, delete (CI run 41494257938)
- [ ] User/group create, delete, password and group changes
- [ ] Editing cron entries / scheduled tasks
- [ ] File upload/download of large or binary files, rename, chmod/chown
- [ ] Disk I/O metrics; downsampled charts for long windows

## Phase 4a: Docker manager (done)

- [x] Docker Engine API over the local socket; containers list/start/stop/restart/remove/logs (CI run 41500727424)
- [x] Safe `docker run`: named volumes only, loopback ports by default, no privileged/host network; control-plane protection (CI run 41500727424)
- [x] Images pull/remove/prune, volumes and networks (view); Docker tab in the dashboard
- [ ] Docker on Windows (named pipe); rootless Docker and Podman verified
- [ ] Container stats (CPU/memory per container), exec console, volume/network create and delete

## Phase 4b: Deployments (done)

- [x] Git deployments (Dockerfile, static, Node.js) with logs, history, rollback and automatic recovery (CI run 41507533185)
- [x] Encrypted app environment; secrets redacted from task records after completion (CI run 41507533185)
- [ ] ZIP upload deployments (needs a file-transfer channel to the agent)
- [ ] Compose deployments with a policy validator (no privileged / host mounts)
- [ ] Private repositories (deploy keys / tokens); live build logs; zero-downtime releases; HTTP health checks
- [ ] More runtimes (Python, PHP, Go) without a Dockerfile

## Phase 5a: Reverse proxy and websites (done)

- [x] Caddy proxy managed by the agent, admin API on a root-only Unix socket (CI run 41510772108)
- [x] Websites (domain → local port), internal CA / HTTP only, HTTP→HTTPS redirect, live reload, certificate status (CI run 41510772108)
- [~] Let's Encrypt issuance (configured, never run against a public domain)
- [ ] DNS provider adapters (Cloudflare first), DNS-01 and wildcard certificates (Phase 5b)
- [ ] Sharing 80/443 with the control-plane Caddy on single-server installs
- [ ] Redirects, basic auth, static sites, per-site access logs

## Phase 6a: WordPress (done)

- [x] WordPress from the official images with WP-CLI: install, info, checksum verification, updates, remove (CI run 41599607565)
- [x] One-time admin password; database password never leaves the server (CI run 41599607565)
- [ ] Staging, clone, migrate (`wp search-replace`), change the site URL (Phase 6b)
- [ ] Other CMSes (Joomla, Drupal, Ghost …); multisite
- [ ] Backups before updates (Phase 7)

## Phase 7a: Encrypted backups (done)

- [x] age-encrypted volume and WordPress backups, verify, restore, delete (CI run 41603543535)
- [x] Recovery key export; schedules with retention (CI run 41603543535)
- [ ] Remote backup storage (S3-compatible, SFTP) and off-server copies (Phase 7b, needs credentials)
- [ ] Database services: create MySQL/MariaDB/PostgreSQL databases and users, per-database dumps
- [ ] App (Phase 4b) and plain container backups with stop-consistent snapshots

## Phase 8a: Alerts and retention (done)

- [x] Alert rules (cpu, memory, disk, load, offline, backup), signed webhooks, SMTP e-mail (CI run 41609898762)
- [x] Hourly metric rollups (90 days), audit retention (CI run 41609898762)
- [x] Notification queue with retries (Phase 9a, System tests run 41638738285)
- [ ] Silences and maintenance windows (8b)
- [ ] Automation: scheduled actions and remediation (8b)
- [ ] Slack / Telegram / Matrix adapters (webhooks work with any receiver today)

## Phase 9a: Fleet (done)

- [x] Server groups; alert rules for a group (System tests run 41638738285)
- [x] Fleet batches: one allowlisted task on up to 200 online servers, per-server status and results (System tests run 41638738285)
- [x] PostgreSQL job queue with leases, back-off and permanent errors; alert notifications retried (System tests run 41638738285)
- [x] Leases so periodic loops run in one API process, released on shutdown (System tests run 41638738285)
- [~] Optional Redis for shared rate limits and login lockout (Go tests against Redis 7 in CI; not in the Compose stack or system test)
- [ ] Shared long-poll wake-ups across API instances (LISTEN/NOTIFY or Redis pub/sub)
- [ ] Multi-instance deployment behind a load balancer, tested end to end
- [ ] Staged rollouts, scheduled batches, stop on failure (with automation, 8b)
- [ ] Fleet health overview (agent versions, certificate expiry, outdated agents)

## Phase 10a: Provider Mode (done)

- [x] Data centers, racks, nodes, IPv4 pools, VM templates; `provider.read` / `provider.write`; Provider Mode required (System tests run 41682706322)
- [x] libvirt/KVM VPS: create from a verified cloud image with cloud-init, start, shutdown, reboot, force stop, delete (System tests run 41682706322: real VM reached over SSH)
- [x] Address allocation and release, capacity with overcommit, serial console log (System tests run 41682706322)
- [ ] Proxmox VE adapter (10b, needs a test server and API token)
- [ ] OpenStack, CloudStack, vSphere adapters
- [ ] Bridged / VLAN networking, IPv6, several NICs
- [ ] Resize, rebuild, snapshots, migration, web console
- [ ] Other cloud images (Debian, Rocky, Alma) verified

## Phase 11a: Provisioning API (done)

- [x] API clients with scopes, source networks, webhook URL; secret shown once (System tests run 41800723205)
- [x] Signed requests (HMAC-SHA256, 5-minute window, single-use nonces), Idempotency-Key, unique external ids (System tests run 41800723205)
- [x] VPS plans without prices; provision, suspend, unsuspend, reboot, terminate (System tests run 41800723205)
- [x] Signed webhooks for billing systems through the job queue (System tests run 41800723205)
- [ ] Web hosting backend adapters: cPanel/WHM, Plesk, DirectAdmin, ISPConfig (11b, needs test installations)
- [ ] WHMCS / Blesta / HostBill modules built on the API
- [ ] Shared hosting, e-mail and DNS products in plans
- [ ] Generated API client from the OpenAPI spec (`packages/api-client`)

## Phase 12a: Customer Panel (done)

- [x] Customer accounts separate from operators; one-time setup links; login with rate limit and lockout (System tests run 41829604752)
- [x] Separate sessions, cookie path and CSRF; no crossover with operator sessions (System tests run 41829604752)
- [x] Customers see and start, shut down, reboot only their own VPS; suspended services stay off (System tests run 41829604752)
- [ ] Customer MFA and password reset by e-mail
- [ ] Websites, backups, console and usage graphs in the Customer Panel
- [ ] Resellers and customer sub-users

## Phase 13a: Plugin SDK (done)

- [x] Apache-2.0 SDK, protocol v1 over stdio, signing tool, example notifier (CI run 41834145603)
- [x] Signed manifests, trusted keys, approval pinning the binary hash (CI run 41834145603)
- [x] Contained runner: clean environment, timeout, output limits, secrets on stdin (CI run 41834145603; Windows 11 locally)
- [x] Plugin notification channels (CI run 41834145603)
- [ ] OS sandbox for plugins (seccomp / namespaces / a dedicated user)
- [ ] Windows job objects so plugin children are killed too
- [ ] DNS, backup-storage and hosting-backend extension points
- [ ] Plugin directory in the Compose stack and a system-test step

## Phase 14a: Audit, performance, compatibility, release (done)

- [x] govulncheck in CI; Go 1.26.10 minimum (finding fixed; CI run 41840856234)
- [x] Route-wide authentication and CSRF test (140 routes)
- [x] Internal security review: docs/security-audit.md
- [x] PostgreSQL performance test with 300 servers (CI run 41840856234)
- [x] linux/arm64 runner and six distribution containers (CI run 41840856234)
- [x] Release workflow, Sigstore keyless signatures, verified dry run (Release run 41840910281)
- [ ] Independent security audit, penetration test, fuzzing
- [x] First pre-release v0.1.0 published and verified (Release run 41846227536)
- [ ] Make the GHCR images public (owner); SBOM and licence report
- [ ] Batch the alert evaluation queries (linear cost per rule × server)
- [ ] Installer and tasks on distributions other than Ubuntu 24.04

## Phases 5–14 (summary; expanded when each phase starts)

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
