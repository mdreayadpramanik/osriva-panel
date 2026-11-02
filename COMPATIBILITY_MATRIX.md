# OSRIVA Panel — Compatibility matrix

Statuses: `SUPPORTED` (implemented **and** tested) · `PARTIALLY_SUPPORTED` ·
`UNSUPPORTED` · `NOT_TESTED` (expected to work, never verified) ·
`DEPENDENCY_MISSING` · `REQUIRES_CONFIGURATION`.

A row moves to `SUPPORTED` only with a test run recorded in the "Evidence" column.
Last updated: 2026-11-02 (Phase 1).

## Control plane (Core API + dashboard)

| Platform | Status | Evidence / notes |
|----------|--------|------------------|
| Windows 11 x86_64, Go 1.27.3, Node 24, in-memory store | PARTIALLY_SUPPORTED | Development only: unit + HTTP tests pass, dashboard built and exercised in a browser (2026-11-02). Not a production target. |
| Linux x86_64 (Ubuntu runner), Go 1.26, PostgreSQL 17 | NOT_TESTED | CI workflow defined (`.github/workflows/ci.yml`) but not yet run. |
| Docker Compose stack | NOT_TESTED | Dockerfiles and compose written. No Docker available during Phase 1. |
| Linux ARM64 | NOT_TESTED | Static Go binary should cross-compile. Unverified. |
| PostgreSQL 16 / 17 | NOT_TESTED | Schema uses only PG ≥ 13 features. Contract tests run in CI on 17. |

## Managed servers (OSRIVA Agent)

The agent does not exist yet (Phase 2). Every managed OS below is therefore
`UNSUPPORTED` today, regardless of eventual plans.

| OS family | Targets | Status |
|-----------|---------|--------|
| Linux | Ubuntu, Debian, Fedora, RHEL, AlmaLinux, Rocky, openSUSE/SUSE, Arch | UNSUPPORTED (Phase 2) |
| Windows | Windows Server | UNSUPPORTED (Phase 2) |
| BSD / Unix-like | FreeBSD, OpenBSD, NetBSD, illumos | UNSUPPORTED (Phase 2) |
| Immutable / restricted OSes | e.g. Fedora CoreOS, Talos, ChromeOS | UNSUPPORTED. A conventional agent install may be impossible. Each will be assessed individually. |

## Runtimes, frameworks, CMS, databases, virtualization

None are integrated yet. Each one is added here as its adapter lands (Phases 4–11).
