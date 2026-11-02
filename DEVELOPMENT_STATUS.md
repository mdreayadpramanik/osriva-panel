# OSRIVA Panel — Development status

Last updated: **2026-11-02** · Current phase: **2 (agent & installer) done** · Next: **Phase 3 (server management)**

**OSRIVA Panel is not production-ready.** It can be installed, can enrol servers and
shows what their agents report. It cannot change anything on a server yet.

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
| 3 | Live metrics, service management, security management, file manager | ⏭ Next |
| 4–14 | See [TASKS.md](TASKS.md) | Not started |
