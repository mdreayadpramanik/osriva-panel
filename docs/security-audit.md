# Security audit (Phase 14a)

Date: 2026-11-04. Scope: the Core API, the agent, the dashboard and the plugin SDK at
the commit that adds this file.

**Who did it:** this is an **internal review**, done by the development assistant with
the help of automated tools. It is **not an independent third-party audit**. Before
production use, an external audit and penetration test are still recommended; see
"Not done" below.

## 1. Automated checks (repeatable, in CI)

| Check | Result | Where |
|-------|--------|-------|
| `govulncheck ./...` (Go vulnerability database) | **Finding 1 (fixed):** `go.mod` pinned `go 1.26.0`, so CI and release builds used a Go release with reachable standard-library vulnerabilities: GO-2026-6809 (`crypto/x509`, reached through SMTP over TLS) and GO-2026-6612, 6613 and 6617 (`net/http`, reached by the API server and the agent client). The minimum is now `go 1.26.10`, which contains the fixes. A local run with Go 1.27.3 had missed this; CI found it. One module-level advisory remains, GO-2026-5932 (the unmaintained `golang.org/x/crypto/openpgp`), and OSRIVA never imports that package. | `CI` job `go`, step "Known vulnerabilities" |
| `npm audit --omit=dev` | 0 vulnerabilities in the dashboard's production dependencies | local run; dependencies pinned in `package-lock.json` |
| Every HTTP route checked for its authentication by walking the router: 140 routes | No gaps: 114 operator routes answer 401 without a session and 403 without the CSRF token; 6 customer routes reject operator sessions; 6 signed routes reject unsigned requests; 14 public routes are on an explicit allowlist | `httpapi/authz_audit_test.go`: a new route without protection fails CI |
| Every `/api/v1` route documented in OpenAPI | Yes | `httpapi/openapi_contract_test.go` |
| Tenant isolation (customer A vs B, unassigned VPS) | 404 for everything not owned | `httpapi/customer_e2e_test.go`, store contract |
| Plugin containment (environment, timeout, output, tampering) | Contained, within the limits stated below | `plugins/plugins_test.go` |
| Signed provisioning requests (bad signature, old timestamp, tampered query, replay) | All rejected | `httpapi/provisioning_e2e_test.go` |
| Agent listener without a client certificate, or with an untrusted one | Rejected | `httpapi/agent_e2e_test.go` |
| Agent file access outside allowed roots, symlink escape, traversal in archives | Rejected | agent `fsguard` and backup tests |

## 2. Manual review

| Area | What was checked | Finding |
|------|------------------|---------|
| Command execution (agent) | Every `exec.Command`: arguments are passed as an array, never through a shell. The only shell scripts, WP-CLI and the MariaDB dump inside containers, are fixed strings; user values reach them only as environment variables. The PowerShell scripts on Windows are fixed. | No issue |
| SQL | Parameterised queries everywhere. The one dynamic fragment (`UpdateUser`) builds column names from constants. | No issue |
| Request bodies | The JSON decoder enforces 1 MiB, rejects unknown fields and accepts a single object. Signed requests are capped before HMAC verification. | No issue |
| Secrets at rest | AES-256-GCM with purpose-bound AAD (agent CA key, app environment, channel secrets, backup identities, API client secrets). Hashes only for session tokens, enrolment tokens and setup tokens. | No issue |
| Secrets in logs and audit | Webhook paths, SMTP passwords, API secrets, plugin secrets and the backup identity are kept out of audit metadata; task parameters are redacted after completion. | No issue |
| Comparisons | HMAC and CSRF checks are constant time. Unknown accounts still run an Argon2 verification against a dummy hash. | No issue |
| Cookies | `HttpOnly`, `SameSite=Strict`, `Secure` in production. The customer cookie is scoped to `/api/v1/customer`. | No issue |
| XSS (dashboard) | React escaping. `dangerouslySetInnerHTML` appears only once, for a static theme script. No HTML from the API is rendered. | No issue |
| Open redirects | `?next=` on the login page accepts same-site relative paths only. | No issue |
| XML and YAML generation | libvirt domain XML is escaped; cloud-init files are JSON. | No issue |
| Archive extraction | The restore refuses absolute paths, `..`, symlinks and hard links leaving the target. | No issue |

## 3. Accepted risks (documented, not fixed in 14a)

1. **Server-side requests to internal addresses:** alert webhooks, billing webhooks and
   plugins can reach internal addresses. Only operators with `alerts.write` or
   `settings.write` configure them, and self-hosted receivers need it.
2. **Plaintext webhooks:** `http://` webhook URLs are allowed (for local receivers), so
   signed payloads can travel unencrypted.
3. **No OS sandbox for plugins:** a plugin runs with the API user's file and network
   access (ADR-0016).
4. **Account lockout can be abused:** anyone can lock an e-mail address for 15 minutes
   (SECURITY.md).
5. **No master-key rotation, no MFA for customers, no admin password reset tool.**
6. **Rate limits are per instance** without Redis.
7. **Plugin binaries:** the plugin directory must be writable only by the operator, since
   a binary swapped between the hash check and the start would still run (ADR-0016).

## 4. Not done (recommended before production)

* An independent third-party security audit and penetration test.
* Fuzzing of the JSON decoders, the agent task validators and the archive extractor.
* A dependency licence report and an SBOM for release artifacts.
* Hardening reviews of the installer on distributions other than Ubuntu 24.04.
