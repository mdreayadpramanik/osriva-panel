# OSRIVA Panel — Development status

Last updated: **2026-11-03** · Current phase: **4 (Docker, deployments) done** · Next: **Phase 5 (websites, reverse proxy, SSL, DNS)**

**OSRIVA Panel is not production-ready.** It can be installed, enrol servers, show live
metrics and logs, list services and processes, control services, and browse and edit files
inside each agent's allowed directories, manage SSH keys, view users, scheduled tasks, host name and timezone, manage Docker containers and images, and deploy apps from Git with history and rollback.

## Phase 4b report (deployments)

Design: [ADR-0007](docs/adr/0007-deployments.md). Evidence: `System tests` run 41507533185 (commit `a4d48b8`,
2026-11-03, both jobs passed): agent as root on Ubuntu 24.04 with real Git and Docker. The two earlier runs
found real bugs, both fixed and covered by tests:
* run 41505025936: plain-HTTP Git servers reject shallow clones, so the agent now falls back to a full clone;
* run 41506048788: the agent's umask 0077 made image files unreadable (nginx 403), so modes are now normalised.

### Implemented and tested

| Feature | Evidence |
|---------|----------|
| Apps (Git URL, branch/tag, build mode, sub-directory, ports, encrypted env); migration `0004` | store contract tests (memstore + PostgreSQL 17 in CI), e2e tests |
| `app.deploy`: shallow clone over http(s) (full-clone fallback), context tar without links/`.git`, Engine API build, container swap | fake-Engine unit tests; real `git` over dumb HTTP in a unit test; Ubuntu CI: static site v1 → v2, one container left |
| Runtimes: Dockerfile, static (nginx), Node.js (`npm start`, `PORT` set) | Ubuntu CI: static site served, Node app answered `node ok 3000` |
| Crashing release → previous version restarted (`recovered`); failed build → running version untouched; logs kept for failures | unit tests; Ubuntu CI (`process.exit(1)` release, npm install of a missing package) |
| History and rollback to an earlier build | e2e; Ubuntu CI: rollback brought v1 back |
| Secrets: env encrypted (AES-GCM, bound to the app), names only in API/audit, task parameters redacted after completion | e2e; Ubuntu CI checked PostgreSQL directly: no secret in `agent_tasks`, `apps` or `audit_logs` |
| Per-task run timeouts (20 min for deployments), deployments run beside the agent's task loop | contract tests |
| Delete app removes container and images | Ubuntu CI |
| Dashboard Apps tab (list, create/settings, deploy, history, logs, rollback, delete) | `tsc`; browser with a real agent on Windows 11 (create, validation error, deploy failing honestly with "Docker on Windows is not supported") |

### Not done / limitations

* ZIP uploads, Compose deployments and private repositories are not supported yet (see ADR-0007).
* Build logs arrive when the deployment ends, not live. The old container is stopped before the new one starts (brief downtime).
* Health check = "still running after 5 s"; no HTTP health checks yet. Runtimes beyond static and Node.js (Python, PHP, Go) need a Dockerfile.
* `.dockerignore` is not applied (the agent builds the context itself; `.git` is always excluded).

## Phase 4a report (Docker manager)

Design: [ADR-0006](docs/adr/0006-docker-manager.md). Evidence: `System tests` run 41500727424
(commit `cd7c45e`, 2026-11-03): agent as root on Ubuntu 24.04, next to the real OSRIVA control plane in Docker.

### Implemented and tested

| Feature | Evidence |
|---------|----------|
| `docker.info` (version, counts, driver); honest "unavailable" result when there is no socket or on Windows | unit tests; Ubuntu CI; Windows Server 2022 CI (reported unsupported) |
| Containers: list (state, ports, Compose project), start/stop/restart, remove (no force), logs (multiplexed stream decoded, stdout/stderr, timestamps) | unit tests against a fake Engine API; Ubuntu CI with nginx |
| `docker.container.run`: image, name, command, env, ports, named volumes, restart policy; missing images pulled with an explicit tag | unit tests; Ubuntu CI: port bound to 127.0.0.1, `Privileged=false`, bridge, `no-new-privileges` confirmed with `docker inspect`, page served on loopback |
| Refusals: privileged / host network / host path rejected by the API; OSRIVA API container stop/restart/remove refused and still running; `osriva_postgres-data` mount refused; running container removal and in-use image removal refused | unit tests; Ubuntu CI |
| Images pull/list/remove/prune (dangling only); volumes and networks listed | Ubuntu CI |
| Env values replaced by names in task lists and audit; re-auth for run/stop/remove/prune | unit tests; Ubuntu CI checks the secret never appears in the task list |
| Dashboard Docker tab (containers, logs, run dialog, images, volumes, networks) | `tsc`. **Not clicked through in a browser yet**: the local dev agent is on Windows, where Docker is unsupported. |

### Limitations

* Docker on Windows (named pipe), rootless Docker and Podman: not supported or not tested.
* No per-container CPU/memory stats, exec console, or volume/network create/delete yet.
* Compose projects are shown but cannot be deployed yet (deferred from Phase 4b: needs a policy validator).

### Security notes

* Docker access is root-equivalent. OSRIVA removes the obvious escapes, but `servers.operate` still allows running any image.

## Phase 3b-2 report (users, SSH keys, scheduled tasks, hostname, timezone)

Evidence: `System tests` run 41497711910 (commit `e24f883`, 2026-11-03, both jobs passed)
plus a real agent on Windows 11. The first run (41496590486) failed and found a real
bug: the systemd unit's `ProtectHome=read-only` blocked writing SSH keys; it was removed.

### Implemented and tested

| Feature | Evidence |
|---------|----------|
| `users.list`: `/etc/passwd` + `/etc/group`; Windows local users and groups | Ubuntu CI, Windows Server 2022 CI, Windows 11 |
| `sshkeys.list/add/remove` (Linux): strict key parsing (no options, RSA ≥ 2048), other lines preserved, `~/.ssh` 0700 and `authorized_keys` 0600 owned by the user | unit tests; Ubuntu CI as root: a real key added, listed and removed for a new user, owner/mode checked, key with `command=` refused |
| Symlink defence: a planted `authorized_keys -> /etc/shadow` is refused and `/etc/shadow` stays unchanged | Ubuntu CI |
| `cron.list`: `/etc/crontab`, `/etc/cron.d`, user crontabs; Windows Task Scheduler (built-in tasks hidden) | unit tests; Ubuntu CI (`/etc/cron.d` entry found with user and schedule); Windows CI; Windows 11 |
| `system.time`, `system.hostname` (hostnamectl), `system.timezone` (timedatectl / tzutil, validated against the tz database / `tzutil /l`) | Ubuntu CI: timezone and hostname changed, confirmed, restored, unknown zone refused; Windows CI: timezone changed and restored |
| Permission classes shared by panel and agent; `servers.access` for SSH keys; re-auth for every change | unit + e2e tests |
| Dashboard System tab | `tsc`; browser with a real agent on Windows 11 |

### Limitations

* No user/group creation, deletion or password changes (deliberately deferred: easy to lock yourself out).
* SSH keys on Windows (OpenSSH `administrators_authorized_keys`): unsupported.
* Scheduled tasks are view only; renaming a Windows computer is unsupported (needs a reboot).

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
| 3b-2 | Users / groups (view), SSH keys, scheduled tasks (view), hostname / timezone | ✅ Done |
| 4a | Docker manager (containers, images, volumes, networks) | ✅ Done |
| 4b | Git deployments (Dockerfile, static, Node.js), history, rollback | ✅ Done (ZIP, Compose, private repos deferred) |
| 5 | Websites, reverse proxy, SSL, DNS | ⏭ Next |
| 6–14 | See [TASKS.md](TASKS.md) | Not started |
