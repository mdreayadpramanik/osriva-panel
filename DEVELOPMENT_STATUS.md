# OSRIVA Panel — Development status

Last updated: **2026-11-02** · Current phase: **1 (Foundation): complete with caveats** · Next: **Phase 2 (Agent & installer)**

**OSRIVA Panel is not production-ready.** It manages no servers yet.

## Phase 1 report

### Implemented and tested

| Feature | Tests |
|---------|-------|
| Env-based configuration with strict validation (refuses insecure production config) | `config` unit tests |
| Structured JSON logging, request IDs, access log without bodies/queries | HTTP tests (request-ID sanitising) |
| Argon2id password hashing, policy, automatic rehash | `auth` unit tests |
| TOTP MFA (RFC 6238), encrypted secret, replay protection, enrol/enable/disable | RFC test vectors + lifecycle test |
| AES-256-GCM secret encryption with purpose-derived keys | `secrets` unit tests (tamper, wrong AAD, wrong key) |
| Sessions: hashed tokens, TTL + idle timeout, logout, revoke one/others, revoke on password change and disable | `identity` tests |
| CSRF protection, strict JSON input, body limits, security headers | `httpapi` tests |
| Login brute-force protection (per-IP + per-account lockout), indistinguishable failures | `identity` tests |
| RBAC: permission catalog, 4 built-in roles synced at startup, anti-escalation, last-owner rule, no self-disable | `rbac` + `identity` tests |
| Re-authentication ("sudo mode") for dangerous operations | `identity` test |
| Organization scoping of user queries | `identity` test |
| User management API (list/search, create, update name/status/roles, admin session revoke) | `identity` + `httpapi` tests |
| Audit log (append-only, filters, keyset pagination) incl. denials and escalation attempts | `storetest` + `identity` + `httpapi` |
| Panel mode setting (personal/provider), owner-only + re-auth | `identity` test |
| Store contract suite run against the in-memory store | `storetest` via `memstore` |
| OpenAPI 3.1 spec served at `/api/v1/openapi.yaml` | — (hand-written, see "Missing") |
| CLI: `serve`, `migrate`, `admin create`, `gen-secret`, `version` | `serve` + `version` run manually |
| Dashboard: login (incl. TOTP step), overview, users & roles, audit log, account (password, 2FA, sessions, security activity), settings, roadmap; light/dark/system theme; responsive shell | `tsc` + `next build`. In a browser against the real API: login, redirect when signed out, user creation and disable, CSRF rejection, HttpOnly cookie, rendering of every page. |

Totals: 45 Go test cases pass locally; the 2 PostgreSQL integration tests skip locally (no database here) and pass in CI.

### Partially implemented / untested

* **PostgreSQL store and migrator:** the store contract suite and migration tests passed against
  PostgreSQL 17 in GitHub Actions CI (2026-11-02, commit 3b7d96e). Not yet exercised by a running deployment.
* **Docker:** both images built in CI. The Compose stack and Caddyfile have **never been started**.
* **Dashboard UI clicks:** the browser checks used in-page API calls and page rendering.
  Modal dialogs and buttons were not clicked through, because the app window was hidden during verification.
* **Mobile/tablet layout:** responsive classes exist but were not visually checked.
* **Organizations:** only the default organization exists. No UI or API to create more.

### Missing (from Phase 1 scope)

* Automated contract test that `openapi.yaml` matches the handlers.
* Frontend unit/e2e tests (Playwright planned) and ESLint configuration.
* Admin-initiated password reset, MFA recovery codes, QR code for TOTP enrolment.
* Custom role editor (the schema supports custom roles. There is no API/UI for them).
* Verbatim license texts in `LICENSE`.
* `govulncheck` in CI.

### Known bugs

* None known. Unverified areas are listed above.

### Security concerns

See [SECURITY.md](SECURITY.md#known-limitations-and-risks): in-process rate limiting,
lockout-based DoS, no key rotation, no account recovery tooling, unbounded audit retention.

## Phase overview

| Phase | Scope | Status |
|-------|-------|--------|
| 1 | Architecture, API, DB, auth, RBAC, logging, dashboard core | ✅ Done (caveats above) |
| 2 | Agent, installer, OS/hardware detection, compatibility engine, registration | ⏭ Next |
| 3–14 | See [TASKS.md](TASKS.md) | Not started |
