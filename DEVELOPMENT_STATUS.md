# OSRIVA Panel — Development status

Last updated: **2026-11-02** · Current phase: **2a (agent, enrolment, inventory, compatibility) done** · Next: **Phase 2b (installer)**

**OSRIVA Panel is not production-ready.** It can now enrol servers and show what
their agents report. It cannot change anything on a server yet.

## Phase 2a report

### Implemented and tested

| Feature | Tests / evidence |
|---------|------------------|
| Internal agent CA (ECDSA P-256), key encrypted at rest, created on first start, concurrency-safe | `pki` tests; created on a real start |
| One-time enrolment tokens (hashed, 1 h, single use, atomic consumption, rotation) | `storetest` + e2e |
| CA pinning: agent verifies the CA fingerprint **before** sending the token | e2e test + real agent on this PC |
| Mutual-TLS agent listener (TLS 1.3, client cert required, per-request revocation/expiry/status check) | e2e tests incl. no-cert and rogue-CA rejection |
| Certificate renewal (30-day certs, renew at 1/3 lifetime, new key each time) | e2e test |
| Server revocation (all certs revoked, agent locked out immediately; re-auth required) | e2e test + real agent |
| Inventory: OS, kernel, arch, CPU, memory, disks, NICs, uptime, privileges, init system, package managers, tools, virtualization, immutability | Linux parsers with fixtures; `Collect()` run on real Windows 11 |
| Compatibility engine (6 statuses, reasons, alternatives; never SUPPORTED for unbuilt modules) | `compat` tests |
| Agent CLI: `inventory`, `enroll`, `run` (backoff, renewal), `status`; atomic credential writes | `state` tests + real run |
| Servers API (`/servers`, detail, token regeneration, revoke) + OpenAPI | e2e + browser |
| Dashboard: Servers list (online/offline/pending/revoked), Add-server flow with one-time command, server detail (system, hardware, network, storage, compatibility), overview counts | `tsc` + `next build`; detail page checked in a browser with the real agent's data |
| Store contract extended (CA, servers, enrolment, certificates, reports) | memstore locally; PostgreSQL in CI |

**Real end-to-end run (2026-11-02, this Windows 11 PC):** an actual `osriva-agent.exe`
was enrolled through the panel, reported over mutual TLS, and the dashboard showed
this PC's real hardware. A wrong fingerprint was refused before the token was
sent, the replayed token was rejected, and after revocation the agent got 403.

Builds: the agent compiles for linux/amd64, linux/arm64, windows/amd64, windows/arm64,
freebsd, openbsd and netbsd (checked locally; CI also runs the Linux inventory on Ubuntu).

### Partially implemented / untested

* **Linux inventory on a real host:** parsers unit-tested, `Collect()` runs in CI on
  the Ubuntu runner (after the next push). It has not been run on a real VPS.
* **BSD inventory:** compiles, never run. Disk inventory on OpenBSD/NetBSD is not
  implemented and reported as a warning.
* **No agent service:** `osriva-agent run` must be started by hand. Service
  installation (systemd/OpenRC/Windows SCM/rc.d) is Phase 2b.
* **Compose:** the agent port (8443) is wired up but the stack has never been started.
* **Phase 1 caveats still apply:** UI dialogs not clicked through, mobile layout not checked.

### Missing

* Universal installer (detect → plan → approve → install → verify; upgrade/repair/rollback/uninstall).
* Signed agent releases and checksums.
* Live metrics (CPU %, network throughput) arrive in Phase 3. Phase 2 reports a snapshot every 60 s.
* Deleting servers (only revoke exists).

### Known bugs

* None known.

### Security concerns

* Agent private key file is `0600` on Unix. On Windows, its protection relies on the
  directory ACL, which the Phase 2b installer must set. Until then, prefer an
  admin-only `--config-dir`.
* The enrolment token is passed on the command line (visible to other local users
  in the process list while `enroll` runs). `OSRIVA_ENROLL_TOKEN` can be used instead.
* Agent audit events are attributed to the default organization (single-org today).

## Phase 1 report (foundation)

Done 2026-11-02: authentication, sessions, TOTP, RBAC, user management, audit log,
settings, dashboard core. CI (2026-11-02, `3b7d96e`) passed, including the
PostgreSQL 17 store contract and migration tests and both Docker image builds.
Remaining Phase 1 gaps: see [TASKS.md](TASKS.md).

## Phase overview

| Phase | Scope | Status |
|-------|-------|--------|
| 1 | Architecture, API, DB, auth, RBAC, logging, dashboard core | ✅ Done |
| 2a | Agent, secure enrolment, mTLS, inventory, compatibility engine, Servers UI | ✅ Done (caveats above) |
| 2b | Universal installer, agent service install, signed releases | ⏭ Next |
| 3–14 | See [TASKS.md](TASKS.md) | Not started |
