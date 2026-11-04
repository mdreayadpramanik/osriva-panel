# OSRIVA Panel — Compatibility matrix

Statuses: `SUPPORTED` (implemented **and** tested) · `PARTIALLY_SUPPORTED` ·
`UNSUPPORTED` · `NOT_TESTED` (expected to work, never verified) ·
`DEPENDENCY_MISSING` · `REQUIRES_CONFIGURATION`.

A row moves to `SUPPORTED` only with a test run recorded in the "Evidence" column.
"System tests run 41463687791" is the `System tests` workflow at commit `bd4052d`
(2026-11-02) on disposable GitHub Actions machines.
Last updated: 2026-11-03 (Phase 4a).

## Control plane (installer, Core API, dashboard)

| Platform | Status | Evidence / notes |
|----------|--------|------------------|
| Ubuntu 24.04 x86_64, installer + Docker Compose (PostgreSQL 17, API, dashboard, Caddy) | SUPPORTED | System tests run 41463687791: plan, install, verify, backup, upgrade, rollback, repair, uninstall --purge. Short-lived machine with `localhost` (Caddy local CA). |
| Other Linux distributions with Docker Compose v2 (Debian, RHEL family, SUSE …) | NOT_TESTED | The installer only needs bash, Docker Compose v2 and coreutils; never run on them. `--install-docker` is Ubuntu-only. |
| Linux ARM64 control plane | NOT_TESTED | Images are multi-arch capable; never built on ARM64. |
| Public domain with Let's Encrypt | NOT_TESTED | Caddy handles ACME; not exercised (CI uses `localhost`). |
| Windows / macOS / BSD as control plane | UNSUPPORTED | The installer refuses non-Linux hosts. Development on Windows works with the in-memory store (see README). |
| PostgreSQL 17 | SUPPORTED | Store contract and migration tests in CI; used by the system test. |
| PostgreSQL 16 | NOT_TESTED | Schema uses only PG ≥ 13 features. |

## Managed servers: OSRIVA Agent

What the agent can do today: enrol, run as a service, report inventory, renew its
certificate, stop when revoked. It cannot manage anything yet (Phase 3+).

| Platform | Inventory + enrolment | Service | Evidence / notes |
|----------|----------------------|---------|------------------|
| Windows 11 x86_64 | SUPPORTED | NOT_TESTED | Real agent on Windows 11 Pro 25H2 (2026-11-02), run in the foreground. |
| Windows Server 2022 x86_64 | SUPPORTED | SUPPORTED | System tests run 41463687791: `install.ps1` one-liner, Windows service as LocalSystem, protected DACL, revoke stops it, uninstall. |
| Windows ARM64 | NOT_TESTED | NOT_TESTED | Compiles. |
| Ubuntu 24.04 x86_64 | SUPPORTED | SUPPORTED (systemd) | System tests run 41463687791: `install.sh` one-liner, systemd unit, 0700 config dir, revoke → exit 3 without restart. |
| Other Linux x86_64 with systemd (Debian, RHEL family, SUSE, Arch …) | NOT_TESTED | NOT_TESTED | Same procfs/sysfs and systemd code paths. |
| Linux with OpenRC (Alpine, Gentoo …) | NOT_TESTED | NOT_TESTED | OpenRC script generated and unit-tested; never run. |
| Linux ARM64 / ARMv7 | NOT_TESTED | NOT_TESTED | Compiles; builds served by the panel. |
| FreeBSD | NOT_TESTED | NOT_TESTED | sysctl/getfsstat collector and rc.d script compile; never run. |
| OpenBSD, NetBSD | PARTIALLY_SUPPORTED (expected) | UNSUPPORTED | Compiles; no disk inventory; no service adapter (run `osriva-agent run` under your own supervisor). |
| illumos, macOS | UNSUPPORTED | UNSUPPORTED | Only portable facts are collected; no agent builds are served. |
| Immutable OSes (Fedora CoreOS, Flatcar, Talos …) | PARTIALLY_SUPPORTED (expected) | NOT_TESTED | Detected and flagged by the compatibility engine. A conventional agent install may be impossible. |

## Server management: tasks and live metrics (Phase 3a)

"System tests run 41467259328" is the `System tests` workflow at commit `0424ee6` (2026-11-02).

| Platform | Live metrics | Services (list + control) | Processes | Evidence / notes |
|----------|--------------|---------------------------|-----------|------------------|
| Ubuntu 24.04 (systemd) | SUPPORTED | SUPPORTED | SUPPORTED | System tests run 41467259328: `cron.service` stop/start/restart, process list, CPU/memory/load metrics. |
| Other systemd distributions | NOT_TESTED | NOT_TESTED | NOT_TESTED | Same `/proc` and `systemctl --output=json` (systemd ≥ 246) paths. |
| Linux with OpenRC | NOT_TESTED | UNSUPPORTED | NOT_TESTED | No OpenRC service adapter yet; tasks fail with an explicit error. |
| Windows Server 2022 | SUPPORTED | SUPPORTED | SUPPORTED | System tests run 41467259328: Spooler stop/start as LocalSystem, process list, CPU/memory metrics. |
| Windows 11 | SUPPORTED | PARTIALLY_SUPPORTED | SUPPORTED | Real agent unprivileged: 281 services listed, control correctly denied without admin; as a service (LocalSystem) control is expected to work but was not run. |
| FreeBSD, OpenBSD, NetBSD | UNSUPPORTED | UNSUPPORTED | UNSUPPORTED | No collectors or adapters yet. |

### Logs and file manager (Phase 3b-1)

"System tests run 41494257938" is the `System tests` workflow at commit `fd8c13b` (2026-11-02).

| Platform | Logs | File manager | Evidence / notes |
|----------|------|--------------|------------------|
| Ubuntu 24.04 (journald) | SUPPORTED | SUPPORTED | System tests run 41494257938, agent as root: journal + unit logs; write/read/delete under `/srv`; secrets, `/root` and a symlink escape refused. |
| Other Linux with journald | NOT_TESTED | NOT_TESTED | Same `journalctl --output=json` and file code paths. |
| Linux without journald (OpenRC …) | UNSUPPORTED | NOT_TESTED | Logs need journald; files use portable code. |
| Windows Server 2022 | SUPPORTED (System, Application) | SUPPORTED | System tests run 41494257938, agent as LocalSystem: Event Log, write/read/delete under `C:\ProgramData`, SAM and agent key refused. Per-service logs: UNSUPPORTED. |
| Windows 11 | SUPPORTED | SUPPORTED | Real agent (unprivileged) on 2026-11-02. |
| BSD | UNSUPPORTED | NOT_TESTED | No log backend; files use portable code. |

### Users, SSH keys, scheduled tasks, hostname, timezone (Phase 3b-2)

"System tests run 41497711910" is the `System tests` workflow at commit `e24f883` (2026-11-03).

| Platform | Users (view) | SSH keys | Scheduled tasks (view) | Hostname / timezone | Evidence / notes |
|----------|--------------|----------|------------------------|---------------------|------------------|
| Ubuntu 24.04 (systemd) | SUPPORTED | SUPPORTED | SUPPORTED (cron) | SUPPORTED / SUPPORTED | System tests run 41497711910, agent as root. |
| Other systemd distributions | NOT_TESTED | NOT_TESTED | NOT_TESTED | NOT_TESTED | Same `/etc/passwd`, `hostnamectl`, `timedatectl` paths; RHEL cron spool `/var/spool/cron` handled but untested. |
| Windows Server 2022 | SUPPORTED | UNSUPPORTED | SUPPORTED (Task Scheduler) | UNSUPPORTED / SUPPORTED | System tests run 41497711910, agent as LocalSystem. Renaming needs a reboot and is not offered. |
| Windows 11 | SUPPORTED | UNSUPPORTED | SUPPORTED | UNSUPPORTED / NOT_TESTED | Real agent unprivileged (view only). |
| BSD | UNSUPPORTED | UNSUPPORTED | UNSUPPORTED | UNSUPPORTED | No adapters yet. |

## Docker manager (Phase 4a)

"System tests run 41500727424" is the `System tests` workflow at commit `cd7c45e` (2026-11-03).

| Platform | Docker manager | Evidence / notes |
|----------|----------------|------------------|
| Ubuntu 24.04, Docker Engine (GitHub runner) | SUPPORTED | System tests run 41500727424, agent as root next to the OSRIVA control plane: info, list, run nginx (loopback port, named volume), logs, stop/start/remove, pull/remove/prune images; control-plane containers and volume refused. |
| Other Linux with Docker Engine ≥ 20.10 (API v1.41) | NOT_TESTED | Same Unix-socket API code. |
| Rootless Docker, Podman (Docker-compatible socket) | NOT_TESTED | Set `DOCKER_HOST=unix://…` for the agent service; never run. |
| Windows (Docker Desktop / named pipe) | UNSUPPORTED | `docker.info` reports "not supported" (checked in CI on Windows Server 2022). |
| BSD | UNSUPPORTED | No Docker Engine. |

### Deployments (Phase 4b)

"System tests run 41507533185" is the `System tests` workflow at commit `a4d48b8` (2026-11-03).

| Platform / feature | Status | Evidence / notes |
|--------------------|--------|------------------|
| Ubuntu 24.04 + Docker Engine + git: Git deploy, static and Node.js runtimes, rollback, recovery | SUPPORTED | System tests run 41507533185 |
| Dockerfile build mode | PARTIALLY_SUPPORTED | Unit tests with a fake Engine; the real Engine build path is the same one used by the static and Node.js modes |
| Git over HTTPS (GitHub, GitLab …) | NOT_TESTED | CI used a loopback dumb-HTTP server; smart HTTPS uses the same `git clone` |
| Windows | UNSUPPORTED | No Docker support on Windows yet |

## Reverse proxy and websites (Phase 5a)

"System tests run 41510772108" is the `System tests` workflow at commit `64e0486` (2026-11-03).

| Platform / feature | Status | Evidence / notes |
|--------------------|--------|------------------|
| Ubuntu 24.04 + Docker Engine: Caddy proxy, internal-CA and HTTP-only sites, reload, disable | SUPPORTED | System tests run 41510772108 |
| Let's Encrypt (HTTP-01 / TLS-ALPN-01) | NOT_TESTED | Needs a public domain; Caddy handles it, OSRIVA only configures it |
| Control-plane host (80/443 already used) | PARTIALLY_SUPPORTED | Proxy works on other ports; public ACME needs 80/443 |
| Windows, BSD | UNSUPPORTED | Needs Docker on Linux |

## CMS: WordPress (Phase 6a)

"System tests run 41599607565" is the `System tests` workflow at commit `d08a569` (2026-11-03).

| Platform / feature | Status | Evidence / notes |
|--------------------|--------|------------------|
| Ubuntu 24.04 + Docker: WordPress 6.6 (php8.3-apache), MariaDB 11.4, WP-CLI 2.11 | SUPPORTED | System tests run 41599607565 |
| WordPress behind the OSRIVA proxy with a public domain | NOT_TESTED | Same limitation as Phase 5a |
| Windows, BSD | UNSUPPORTED | Needs Docker on Linux |
| Joomla, Drupal, Ghost and other CMSes | UNSUPPORTED | Not implemented |

## Backups (Phase 7a)

"System tests run 41603543535" is the `System tests` workflow at commit `d6adfe1` (2026-11-03).

| Platform / feature | Status | Evidence / notes |
|--------------------|--------|------------------|
| Ubuntu 24.04 + Docker (local volumes): volume and WordPress backups, verify, restore, schedules | SUPPORTED | System tests run 41603543535 |
| Rootless Docker, non-local volume drivers | UNSUPPORTED | The agent reads volumes from their local mount point and refuses other drivers |
| Remote storage (S3, SFTP) | UNSUPPORTED | Phase 7b |
| Windows, BSD | UNSUPPORTED | Needs Docker on Linux |

## Alerts and notifications (Phase 8a)

"System tests run 41609898762" is the `System tests` workflow at commit `639a2b4` (2026-11-03).

| Feature | Status | Evidence / notes |
|---------|--------|------------------|
| Webhook delivery (any HTTP receiver) | SUPPORTED | System tests run 41609898762 (signatures verified) |
| SMTP without TLS to a local relay | SUPPORTED | System tests run 41609898762 (Mailpit) |
| SMTP with STARTTLS / TLS and authentication | NOT_TESTED | Go `net/smtp`; no TLS SMTP server in CI |
| Alerts for Windows agents (CPU, memory, offline) | NOT_TESTED | Same panel-side evaluation; metrics are reported by Windows agents |

## Runtimes, frameworks, CMS, databases, virtualization

None are integrated yet. Each one is added here as its adapter lands (Phases 4b–11).

## Fleet, job queue and rate limits (Phase 9a)

"System tests run 41638738285" is the `System tests` workflow at commit `55d34cb` (2026-11-03).

| Feature | Status | Evidence / notes |
|---------|--------|------------------|
| Server groups, fleet batches on Linux agents (Ubuntu 24.04) | SUPPORTED | System tests run 41638738285 |
| Fleet batches on Windows agents | PARTIALLY_SUPPORTED | Browser check on Windows 11 (`system.time` batch); not in CI |
| Job queue and leases on PostgreSQL 17 | SUPPORTED | CI run 41638738815 (store contract), System tests run 41638738285 |
| Shared rate limits with Redis 7 | PARTIALLY_SUPPORTED | Go tests against Redis 7 in CI run 41638738815; not in the Compose stack |
| Several API instances behind a load balancer | NOT_TESTED | Designed for it (leases, SKIP LOCKED, Redis); never run end to end |

## Provider Mode virtualization (Phase 10a)

"System tests run 41682706322" is the `System tests` workflow at commit `cb9be7b` (2026-11-03).

| Hypervisor / guest | Status | Evidence / notes |
|--------------------|--------|------------------|
| libvirt/KVM on Ubuntu 24.04 (libvirt NAT network `default`) | SUPPORTED | System tests run 41682706322 |
| Guest: Ubuntu 24.04 minimal cloud image | SUPPORTED | System tests run 41682706322 (cloud-init, SSH key, static address) |
| libvirt/KVM on other distributions, bridged networks with public addresses | NOT_TESTED | Same tools (`virsh`, `qemu-img`, `genisoimage`); never run |
| Other guests (Debian, Rocky, Alma cloud images) | NOT_TESTED | NoCloud + virtio are standard; not verified |
| Proxmox VE | UNSUPPORTED | Phase 10b (needs a test server) |
| OpenStack, CloudStack, vSphere | UNSUPPORTED | Not implemented |
| Nodes on Windows, BSD, macOS | UNSUPPORTED | `vm.host` reports the reason (checked on Windows 11) |

## Provisioning API (Phase 11a)

"System tests run 41800723205" is the `System tests` workflow at commit `749d022` (2026-11-04).

| Integration | Status | Evidence / notes |
|-------------|--------|------------------|
| Any HTTP client that signs requests as documented (tested with curl + openssl) | SUPPORTED | System tests run 41800723205 |
| WHMCS, Blesta, HostBill | UNSUPPORTED | No module yet; the signed API is documented in OpenAPI |
| cPanel/WHM, Plesk, DirectAdmin, ISPConfig as hosting backends | UNSUPPORTED | Phase 11b (needs licensed test installations) |

## Customer Panel (Phase 12a)

| Feature | Status | Evidence / notes |
|---------|--------|------------------|
| Customer login, own VPS list, start / shutdown / reboot on libvirt/KVM nodes | SUPPORTED | System tests run 41829604752 (commit `82ad26f`) |
| Customer MFA, password reset by e-mail | UNSUPPORTED | Not implemented; operators issue a new setup link |

## Plugins (Phase 13a)

"CI run 41834145603" is the `CI` workflow at commit `9283fdd` (2026-11-04).

| Platform / plugin | Status | Evidence / notes |
|-------------------|--------|------------------|
| Plugin runner on Linux (amd64) | SUPPORTED | CI run 41834145603 (real plugin processes, race detector) |
| Plugin runner on Windows (amd64) | PARTIALLY_SUPPORTED | Same tests passed on Windows 11 locally; children of a timed-out plugin are not killed |
| Example chat-notifier → Slack / Mattermost / Rocket.Chat | NOT_TESTED | The incoming-webhook format only; tested against a local receiver |

## Additional platforms (Phase 14a)

"CI run 41840856234" is the `CI` workflow at commit `748baf4` (2026-11-04).

| Platform | Scope | Status | Evidence |
|----------|-------|--------|----------|
| Linux arm64 (Ubuntu 24.04 runner) | agent tests, real inventory | SUPPORTED | CI run 41840856234 |
| Debian 12, Rocky Linux 9, AlmaLinux 9, Alpine 3.20, openSUSE Leap 15.6, Fedora 41 | inventory and OS detection only (containers) | PARTIALLY_SUPPORTED | CI run 41840856234; installer, systemd and tasks NOT_TESTED there |
