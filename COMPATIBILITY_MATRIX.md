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
| Linux x86_64 (Ubuntu runner), Go 1.26, PostgreSQL 17 | PARTIALLY_SUPPORTED | GitHub Actions CI passed on 2026-11-02 (commit 3b7d96e): gofmt, vet, `go test -race` including the PostgreSQL contract and migration tests. Not yet run as a long-lived deployment. |
| Docker images (API, dashboard) | PARTIALLY_SUPPORTED | Both images built in CI on 2026-11-02. Containers not yet started. |
| Docker Compose stack | NOT_TESTED | `docker compose up` has never been run. |
| Linux ARM64 | NOT_TESTED | Static Go binary should cross-compile. Unverified. |
| PostgreSQL 17 | SUPPORTED | Store contract suite and migration idempotency test passed in CI on 2026-11-02. |
| PostgreSQL 16 | NOT_TESTED | Schema uses only PG ≥ 13 features. Not run in CI. |

## Managed servers: OSRIVA Agent (Phase 2a)

What the agent can do today: enrol, report inventory, renew its certificate.
It cannot manage anything yet (Phase 3+). The status below is for **inventory
and enrolment only**.

| Platform | Status | Evidence / notes |
|----------|--------|------------------|
| Windows 11 x86_64 | SUPPORTED | Real agent enrolled, reported and was revoked on Windows 11 Pro 25H2 (2026-11-02). |
| Windows Server | NOT_TESTED | Same Win32/registry code paths as Windows 11; not run on Server yet. |
| Windows ARM64 | NOT_TESTED | Compiles. |
| Linux x86_64 (Ubuntu, Debian, RHEL family, SUSE, Arch …) | NOT_TESTED | Parsers unit-tested with real file formats; `Collect()` runs in CI on Ubuntu after the next push. |
| Linux ARM64 / ARMv7 | NOT_TESTED | Compiles. |
| FreeBSD | NOT_TESTED | sysctl + getfsstat collector compiles; never run. |
| OpenBSD, NetBSD | PARTIALLY_SUPPORTED (expected) | Compiles; disk inventory not implemented (reported as a warning). Never run. |
| illumos, macOS | UNSUPPORTED | Only portable facts (hostname, arch, NICs) are collected. |
| Immutable OSes (Fedora CoreOS, Flatcar, Talos …) | PARTIALLY_SUPPORTED (expected) | Detected and flagged by the compatibility engine. A conventional agent install may be impossible. Assessed individually. |

## Runtimes, frameworks, CMS, databases, virtualization

None are integrated yet. Each one is added here as its adapter lands (Phases 4–11).
