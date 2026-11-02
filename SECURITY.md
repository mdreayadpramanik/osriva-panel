# Security

## Reporting a vulnerability

Please do **not** open a public issue. Until a dedicated security contact and
GitHub Security Advisories are configured for the public repository, report
privately to the project maintainers. *(TODO before the first release: publish
a security contact address and a PGP key.)*

## Implemented controls (Phase 1)

| Area | Control | Where |
|------|---------|-------|
| Passwords | Argon2id (m=64 MiB, t=3, p=2), PHC format, automatic rehash, min 12 chars, bounded cost parsing | `internal/auth/password.go` |
| Sessions | 256-bit random tokens, only SHA-256 hashes stored, `HttpOnly` + `SameSite=Strict` + `Secure` cookie, absolute TTL + idle timeout, per-session and bulk revocation, all sessions revoked on password change or account disable | `internal/identity/sessions.go` |
| CSRF | HMAC-derived per-session token in `X-CSRF-Token` required for unsafe methods. JSON content type required. No CORS. | `internal/httpapi/middleware.go` |
| MFA | TOTP (RFC 6238, verified against the RFC test vectors), secret encrypted with AES-256-GCM bound to the user ID, single-use codes | `internal/auth/totp.go` |
| Sudo mode | Password re-confirmation within 10 min for role changes, disabling users, mode changes and MFA disable | `identity.RequireRecentReauth` |
| Brute force | Per-IP token bucket on login and reauth, per-account lockout (10 failures / 15 min), timing equalisation for unknown accounts, generic error messages | `internal/ratelimit`, `identity.Login` |
| Authorization | Code-defined permission catalog, deny by default, anti-escalation (you can only grant or manage what you hold), last-owner protection, no self-disable, denials audited | `internal/identity/users.go` |
| Tenancy | All user queries scoped to the actor's organization. Cross-org IDs return 404. | `identity.getInOrg` |
| Input | Strict JSON decoding (unknown fields rejected), 1 MiB body limit, single object, validated emails/names | `internal/httpapi/respond.go` |
| SQL injection | Parameterised queries only. `LIKE` input escaped. | `store/postgres` |
| Headers | `nosniff`, `X-Frame-Options: DENY`, restrictive CSP on API responses, `no-store`, HSTS when secure | `securityHeaders` |
| Client IP | `X-Forwarded-For` honoured only from configured trusted proxies, right-to-left | `clientIP` |
| Secrets | Master key from the environment only, purpose-separated subkeys, never logged. Responses never include hashes, tokens or TOTP secrets (except the one-time enrolment response to the account owner). | `internal/secrets` |
| Logging | Structured logs without bodies, headers or query strings. Request IDs sanitised. | `accessLog` |
| Audit | Append-only audit table for logins (success/failure), logout, reauth, password/MFA changes, user/role changes, setting changes, authorization denials and escalation attempts | `audit_logs` |
| Containers | API: distroless, non-root, static binary. Dashboard: non-root. PostgreSQL on an internal-only network. | `infrastructure/docker` |

## Known limitations and risks

* **Rate limiting and lockout are in-process.** With several API instances,
  each one has its own counters. A shared backend (Redis) is planned.
* **Account lockout can be abused for denial of service:** anyone who knows an
  email can lock that account for 15 minutes. This is the usual trade-off. The
  owner can still sign in after the lock expires, and per-IP limiting slows mass attempts.
* **No master-key rotation yet.** The ciphertext format carries a version byte
  so that rotation can be added. Losing `OSRIVA_SECRET_KEY` makes TOTP secrets
  unreadable, and the affected users then need MFA reset (no admin reset tool yet).
* **No admin password reset or MFA recovery codes yet.** An owner who loses
  both factors needs `osriva-api admin create` on the host to regain access.
* **Audit retention is unbounded.** Log growth needs a retention job (Phase 8).
* The PostgreSQL store passes the shared contract suite in CI only. It has not
  been run on the development machine (no PostgreSQL there).
* No external security audit has been performed (planned for Phase 14).

## Dangerous-operation policy (applies to all future phases)

Server shutdown/reboot, disk formatting, volume/database/backup deletion, VPS
termination, firewall/SSH changes and privilege escalation will all require
re-authentication plus explicit confirmation of the target. They will be audited and
executed only by the agent through allowlisted tasks. OSRIVA never changes
firewall, SSH, disk or network settings silently.
