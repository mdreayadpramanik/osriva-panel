# OSRIVA Panel — Compatibility matrix

Statuses: `SUPPORTED` (implemented **and** tested) · `PARTIALLY_SUPPORTED` ·
`UNSUPPORTED` · `NOT_TESTED` (expected to work, never verified) ·
`DEPENDENCY_MISSING` · `REQUIRES_CONFIGURATION`.

A row moves to `SUPPORTED` only with a test run recorded in the "Evidence" column.
"System tests run 41463687791" is the `System tests` workflow at commit `bd4052d`
(2026-11-02) on disposable GitHub Actions machines.
Last updated: 2026-11-02 (Phase 2b).

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

## Runtimes, frameworks, CMS, databases, virtualization

None are integrated yet. Each one is added here as its adapter lands (Phases 4–11).
