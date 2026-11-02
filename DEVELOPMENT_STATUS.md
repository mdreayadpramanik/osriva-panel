# OSRIVA Panel — Development status

Last updated: **2026-11-02** · Current phase: **3b-1 (logs, file manager) done** · Next: **Phase 3b-2 (users, SSH keys, cron, hostname)**

**OSRIVA Panel is not production-ready.** It can be installed, enrol servers, show live
metrics and logs, list services and processes, control services, and browse and edit files
inside each agent's allowed directories. Users, SSH keys and cron cannot be managed yet.

## Phase 3b-1 report (logs and file manager)

Evidence: `System tests` run 41494257938 (commit `fd8c13b`, 2026-11-02, both jobs passed)
plus a real agent on Windows 11.

### Implemented and tested

| Feature | Evidence |
|---------|----------|
| `logs.read`: journald (whole journal or one unit, level filter, ≤ 1000 entries) | Ubuntu 24.04 CI (journal + `osriva-agent.service`) |
| `logs.read`: Windows System / Application Event Log (wevtutil RenderedXml); per-service logs on Windows explicitly unsupported | Windows Server 2022 CI, Windows 11 |
| File manager: list, read (text ≤ 512 KiB, binary detected), atomic write, mkdir, non-recursive delete | unit tests; Ubuntu CI (as root) and Windows CI (as LocalSystem); Windows 11 |
| Agent-side confinement: roots from `file_roots` (panel cannot widen), deny list, lexical check before disk access, symlink resolution | unit tests; Ubuntu CI as root: `/etc/shadow`, `/etc/sudoers`, agent key, `/etc/osriva/osriva.env`, `/root`, `/proc/1/environ`, `/etc/../root`, symlink to `/root` all refused; Windows CI: SAM, traversal, agent key refused |
| `servers.files` permission, re-auth for writes, content redacted from task list and audit, file results hidden from readers without `servers.files` | e2e tests; CI checks that the written secret never appears in the task list |
| Dashboard Logs and Files tabs | `tsc` + browser with a real agent on Windows 11 |

### Limitations

* Files larger than 512 KiB can only be viewed partially and cannot be edited. Binary files cannot be viewed. No upload or download of arbitrary files yet.
* No rename, chmod or chown yet.
* Non-ASCII Windows event messages depend on the console code page of `wevtutil` and may be garbled.
* Logs on OpenRC/BSD hosts (no journald) are not supported.

## Phase 3a report (typed agent tasks, live metrics, services, processes)

Design: [ADR-0005](docs/adr/0005-agent-tasks-and-metrics.md). Evidence: `System tests`
run 41467259328 (commit `0424ee6`, 2026-11-02, both jobs passed) plus a real agent on
Windows 11.

### Implemented and tested

| Feature | Evidence |
|---------|----------|
| Closed task allowlist (`services.list`, `service.action`, `processes.list`), validated on control plane **and** agent; no shell task exists | `agenttask`, `tasks`, e2e tests; `shell.exec` and `x;reboot` rejected in CI on both OSes |
| Delivery by long poll over mutual TLS (woken immediately when a task is queued), results once only, agents isolated from each other | e2e tests over real TLS |
| Lifecycle: queued → running → succeeded/failed/expired; 2 min pickup, 5 min run timeout, janitor | store contract tests (memstore + PostgreSQL 17) |
| Permissions: `servers.read` for listing, new `servers.operate` for actions, re-auth for stop/disable; every task audited | e2e tests |
| systemd: list services (unit + unit-file state), start/stop/restart/enable/disable | Ubuntu 24.04 CI: `cron.service` stopped, started, restarted, confirmed with `systemctl` |
| Windows SCM: list (least-privilege access), start/stop/restart/enable/disable | Windows Server 2022 CI: Spooler stopped and started, confirmed with `Get-Service`; Windows 11: 281 services listed unprivileged, action correctly denied |
| Process list: Linux `/proc` (user, RSS, threads, command), Windows toolhelp (RSS, image path) | Ubuntu CI, Windows Server 2022 CI, Windows 11 (246 processes) |
| Live metrics every 10 s, uploaded every 30 s: CPU %, memory, swap, load (Linux), network rx/tx rate, disks; no invented first-sample rates; 24 h retention | `metrics` tests (incl. counter reset), Ubuntu CI (CPU, memory, load), Windows CI and Windows 11 (CPU, memory, network, disks) |
| Dashboard: Live metrics, Services (with actions), Processes and Activity tabs | `tsc` + `next build`; tabs clicked through in a browser with a real agent on Windows 11 |

### Partially implemented / untested

* **OpenRC, FreeBSD and other BSDs:** no service or process adapter. Tasks fail with an explicit "not supported" error.
* **Metrics on BSD:** not collected (samples carry only a timestamp; the dashboard shows "not reported").
* **Swap on Windows:** not reported.
* Metrics charts are simple line charts of raw samples, without downsampling over 24 h.

### Missing (Phase 3b)

* Logs (journald / Windows Event Log), users and groups, SSH keys, cron, hostname, timezone, file manager.

### Known bugs

* None known.

### Security notes

* Stopping or disabling a service always asks for the password again, but OSRIVA cannot tell
  which service keeps the operator connected (for example `sshd`). The dashboard warns, and the action is audited.

## Phase 2b report (installer, agent service, one-line installers)

### Implemented and tested on real systems

All evidence comes from the `System tests` workflow on disposable GitHub Actions machines
(run 41463687791, commit `bd4052d`, 2026-11-02, both jobs passed).

| Feature | Evidence |
|---------|----------|
| Control-plane installer `plan`: detects OS, arch, RAM, disk, Docker/Compose, busy ports; **changes nothing** | Ubuntu 24.04: `/opt/osriva` absent after `plan` |
| `install`: plan → confirmation → secrets in `/etc/osriva/osriva.env` (0600) → release under `/opt/osriva` → Compose build/start → owner account → health checks | Ubuntu 24.04 |
| Full Docker Compose stack (PostgreSQL, migrate, API with agent listener, dashboard, Caddy with HTTPS) | Ubuntu 24.04, all checks green |
| Agent downloads from the panel (9 platforms, SHA-256) + `install.sh` one-liner: download, checksum, enrol, systemd service | Ubuntu 24.04: server online in the panel, `/etc/osriva-agent` is 0700 |
| `backup` (verified `pg_dump` + config), `upgrade` (new release, data kept), `rollback` (previous release + DB restore), `repair`, `verify` | Ubuntu 24.04: agent online again after each |
| Revocation stops the agent permanently (exit 3, systemd `RestartPreventExitStatus`) | Ubuntu 24.04: `ExecMainStatus=3`, not restarted |
| `uninstall --purge` removes containers, `/opt/osriva`, `/etc/osriva`; `service uninstall` removes the unit | Ubuntu 24.04 |
| Windows: `install.ps1` one-liner → Windows service (automatic start, recovery actions), LocalSystem, elevated inventory | Windows Server 2022 |
| Windows credential directory: protected DACL with only SYSTEM and Administrators | Windows Server 2022 |
| Windows: revocation stops the service; `service uninstall` deletes it | Windows Server 2022 |
| Unit tests: service definitions (systemd/OpenRC/rc.d quoting), dist scanning (no path traversal), script quoting, download routes | `go test` |

### Partially implemented / untested

* **OpenRC and FreeBSD rc.d services:** generated and unit-tested, never run on a real host.
* **Installer on other distributions:** written for any Linux with Docker Compose v2, but
  only tested on Ubuntu 24.04. `--install-docker` works only on Ubuntu (and was not
  exercised, because the CI runner already has Docker).
* **Real domains / Let's Encrypt:** CI used `localhost` with Caddy's local CA. Public ACME
  issuance has not been exercised.
* **Long-lived operation:** every test machine was disposable and short-lived.
* **Agent binaries are not signed.** The checksum detects corruption; authenticity rests on
  the panel's HTTPS plus the CA fingerprint pin used for enrolment.
* Phase 1 caveats remain: dashboard dialogs not clicked through in a browser, mobile layout unchecked.

### Missing

* Signed release artifacts (cosign/minisign) and published container images. Installing
  requires a source checkout while the repository is private.
* Upgrading agents from the panel (agents must be re-installed to update).
* Delete server (only revoke exists).

### Known bugs

* None known.

### Security concerns

* `curl … | sudo sh` trusts the panel's TLS certificate. Operators who prefer to inspect
  the script first can download `/api/v1/agent/install.sh` and read it.
* The enrolment token appears in the one-liner (environment variable on Unix, parameter on
  Windows) and may land in shell history. It is single-use and expires after 1 hour.

## Phase 2a report (agent, enrolment, inventory, compatibility)

Done 2026-11-02: internal CA, one-time enrolment with CA pinning before the token is sent,
mutual-TLS agent listener, renewal and revocation, inventory (Linux, Windows, BSD), the
compatibility engine, Servers UI. Verified with a real agent on Windows 11 and in CI on
Ubuntu (`0674191`). Details in git history and [COMPATIBILITY_MATRIX.md](COMPATIBILITY_MATRIX.md).

## Phase 1 report (foundation)

Done 2026-11-02: authentication, sessions, TOTP, RBAC, user management, audit log,
settings, dashboard core. PostgreSQL 17 store contract tests pass in CI.

## Phase overview

| Phase | Scope | Status |
|-------|-------|--------|
| 1 | Architecture, API, DB, auth, RBAC, logging, dashboard core | ✅ Done |
| 2a | Agent, secure enrolment, mTLS, inventory, compatibility engine, Servers UI | ✅ Done |
| 2b | Installer, agent service, one-line installers | ✅ Done (caveats above) |
| 3a | Typed agent tasks, live metrics, services, processes | ✅ Done |
| 3b-1 | Logs, confined file manager | ✅ Done |
| 3b-2 | Users / groups, SSH keys, cron, hostname / timezone | ⏭ Next |
| 4–14 | See [TASKS.md](TASKS.md) | Not started |
