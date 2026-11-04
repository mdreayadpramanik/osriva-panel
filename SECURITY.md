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
| Agent identity | Internal ECDSA P-256 CA (key encrypted at rest), 30-day client certs bound to the server ID, renewal with fresh keys, revocation checked on every request | `internal/pki`, `internal/fleet` |
| Agent enrolment | One-time 256-bit token (hash stored, 1 h, atomic single use), CA fingerprint pinned **before** the token is sent, CSR signature + key strength checked, CSR subject ignored, per-IP rate limit, all attempts audited | `fleet.Enroll`, `pkg/agentclient` |
| Agent channel | Separate TLS 1.3 listener, client certificate required and verified, agent requests ignore forwarded headers, 1 MiB bodies, schema version checked, agent clock not trusted | `httpapi.NewAgentHandler` |
| Agent scope | The agent executes only typed tasks from a closed allowlist (`services.list`, `service.action`, `processes.list`). There is no shell or "run command" task. Every task is validated on the control plane **and** again on the agent, and tools are run with explicit argument vectors and `--` before user values. | `pkg/agenttask`, `agent/internal/tasks` |
| Task authorization | `servers.read` for read-only tasks, `servers.operate` for service actions, re-authentication for stop/disable, organization-scoped, every task and result audited, results accepted once and only from the target server's certificate | `fleet/tasks.go` |
| File manager confinement | Allowed roots come from `file_roots` in the agent's local `agent.json` (defaults: Linux `/etc /srv /var/www /var/log /opt /home`, Windows `C:\inetpub C:\ProgramData`); the control plane cannot widen them. A deny list keeps secrets unreadable even inside a root (`/etc/shadow`, `/etc/gshadow`, sudoers, `/etc/ssl/private`, SSH host keys, users' `~/.ssh/id_*` and `~/.gnupg`, `/etc/osriva`, the agent's own credentials, Windows Crypto). Paths are checked lexically first (nothing outside a root touches the disk), then symlinks are resolved and checked again. Text only, at most 512 KiB, atomic writes, no recursive delete, roots cannot be deleted. | `agent/internal/tasks/fsguard.go` |
| File access authorization | New `servers.files` permission, needed even to read (files can hold secrets); writes, mkdir and delete need re-auth; written content is replaced by its size in task listings and the audit log; file task results are hidden from users without `servers.files` | `fleet/tasks.go` |
| SSH key management | New `servers.access` permission plus re-auth; keys parsed with `x/crypto/ssh` (no options such as `command=`, RSA ≥ 2048, ed25519/ecdsa/sk accepted); other `authorized_keys` lines kept byte for byte; atomic write with `~/.ssh` 0700 and `authorized_keys` 0600 owned by the user; symlinks and foreign-owned files refused (verified in CI with a planted link to `/etc/shadow`) | `agent/internal/tasks/system_linux.go` |
| Host settings | Hostname validated per RFC 1123, timezone checked against the tz database (`/usr/share/zoneinfo`) or `tzutil /l`, values passed after `--`; PowerShell is only used with fixed scripts, never built from task parameters | `pkg/agenttask/system.go`, `system_windows.go` |
| Least privilege (Windows) | Service listing opens the SCM with connect/enumerate rights only; actions request only start/stop/query/change-config rights | `tasks_windows.go` |
| Agent service | systemd unit with `NoNewPrivileges`, `PrivateTmp`, kernel protections, `UMask=0077` (`/home` stays writable because SSH key management needs it); credential dir 0700 (Unix) or a protected DACL for SYSTEM and Administrators (Windows); revoked agents exit with code 3 and are not restarted | `agent/internal/service`, `agent/internal/state` |
| Agent distribution | Builds served only from a fixed allow-list of file names (no path traversal), SHA-256 checked by the one-line installers, scripts quote every interpolated value | `internal/agentdist` |
| Docker | Agent uses the local Engine API socket only (never exposed). `docker.container.run` has no privileged / host-network / capability / device / bind-mount fields (strict decoding rejects them); agent forces `Privileged=false`, bridge network, `no-new-privileges`; ports bind to 127.0.0.1 unless marked public; OSRIVA's own containers (label `dev.osriva.control-plane`) cannot be stopped or removed and their volumes cannot be mounted; no forced removal; env values redacted from task lists and audit; re-auth for run/stop/remove/prune | `pkg/agenttask/docker.go`, `agent/internal/tasks/docker.go`, ADR-0006 |
| Deployments | Git over http(s) only, no credentials in URLs, refs and paths validated on panel and agent, clone without user/system Git config or submodules; build context tarred by the agent without following links and without `.git`; release refused if a container of the same name was not created by OSRIVA; app env AES-256-GCM encrypted, names only in API and audit, task parameters redacted once a task finishes; deploy/rollback/delete need re-auth | `agent/internal/tasks/deploy.go`, `fleet/apps.go`, ADR-0007 |
| Reverse proxy | Caddy container on the host network with all capabilities dropped except NET_BIND_SERVICE, no-new-privileges; admin API only on a Unix socket inside a 0700 root-only directory; Caddyfile rendered only from validated values (RFC 1123 domains, integers, checked e-mail); busy ports refused; new configs validated by Caddy before the old one is replaced; no firewall changes | `agent/internal/tasks/proxy.go`, ADR-0008 |
| CMS (WordPress) | Official pinned images; MariaDB on a private network without published ports; WordPress on 127.0.0.1; database password generated on the server; admin password shown once and redacted (any `*password*` parameter) from task records and audit; user values reach WP-CLI only as environment variables; read operations skip plugin and theme code; objects not labelled by OSRIVA are never reused or removed; deleting data needs re-auth and a typed confirmation | `agent/internal/tasks/cms.go`, ADR-0009 |
| Backups | age (X25519) encryption; the agent only receives the recipient, the identity is stored encrypted in the panel, sent only for verify/restore and redacted (any `*_key` parameter) from task records; recovery-key export needs re-auth and is audited; archives verified (file hash, full decrypt and read) before any restore; extraction refuses traversal, absolute paths and symlink tricks; restores need re-auth plus a typed confirmation; control-plane volumes refused; files 0600 in a 0700 directory | `agent/internal/tasks/backup.go`, `fleet/backup.go`, ADR-0010 |
| Alerts and notifications | Webhooks signed with HMAC-SHA256 (timestamp + body), 256-bit secret shown once, redirects not followed, 10 s timeout; SMTP STARTTLS required when selected, PLAIN auth never on plain connections; channel secrets AES-GCM encrypted; webhook paths kept out of the audit log and masked for readers; CR/LF refused in names and stripped from subjects; separate `alerts.read` / `alerts.write` permissions; audit retention changes need `settings.write` + re-auth and every cleanup is audited | `services/api/internal/monitor`, ADR-0011 |
| Provider Mode / VPS | Changes need `provider.write` and Hosting Provider Mode; VM tasks are also `servers.operate`; force stop and delete need re-auth, delete also the typed name; images over HTTPS only and verified by SHA-256 before use; cloud-init files generated as JSON; SSH keys only, password login disabled; the agent acts only on domains with OSRIVA metadata, never creates or changes libvirt networks, bridges or firewall rules, and installs nothing; VM storage in libvirt's image directory (0711 directories, 0600 disks) | `agent/internal/tasks/vm.go`, `internal/provider`, ADR-0013 |
| Fleet and jobs | Batches only for an allowlist of task types (no file, container, app, site or backup tasks), each server task checked like a single one (permission class, re-auth once per batch, audit with the batch id), at most 200 servers; job payloads hold only ids; webhook retries keep one delivery id so receivers can drop duplicates; groups used by alert rules cannot be deleted (no silent widening to all servers) | `fleet/groups.go`, `internal/jobs`, ADR-0012 |
| Installer | `plan` changes nothing; every change needs confirmation (or `--yes`); secrets generated locally into a 0600 file; verified database backup before upgrades with automatic rollback; `--purge` requires a typed confirmation; never touches firewall, SSH, disk or network settings | `installer/osriva-install.sh` |

## Known limitations and risks

* **Rate limiting and lockout are in-process unless `OSRIVA_REDIS_URL` is set.** With
  several API instances and no Redis, each one has its own counters. With Redis, keys
  are hashed (no IPs or e-mail addresses stored); if Redis is unreachable each instance
  falls back to its own limiter (ADR-0012). The Redis lockout uses a fixed window.
* **Account lockout can be abused for denial of service:** anyone who knows an
  email can lock that account for 15 minutes. This is the usual trade-off. The
  owner can still sign in after the lock expires, and per-IP limiting slows mass attempts.
* **No master-key rotation yet.** The ciphertext format carries a version byte
  so that rotation can be added. Losing `OSRIVA_SECRET_KEY` makes TOTP secrets
  unreadable, and the affected users then need MFA reset (no admin reset tool yet).
* **No admin password reset or MFA recovery codes yet.** An owner who loses
  both factors needs `osriva-api admin create` on the host to regain access.
* The PostgreSQL store passes the shared contract suite in CI only. It has not
  been run on the development machine (no PostgreSQL there).
* No external security audit has been performed (planned for Phase 14).
* **Docker access is root-equivalent.** OSRIVA blocks the obvious escapes (privileged mode, host paths, host network), but anyone with `servers.operate` can still run any image, including a malicious one, inside Docker's default isolation. Grant `servers.operate` accordingly.

## Dangerous-operation policy (applies to all future phases)

Server shutdown/reboot, disk formatting, volume/database/backup deletion, VPS
termination, firewall/SSH changes and privilege escalation will all require
re-authentication plus explicit confirmation of the target. They will be audited and
executed only by the agent through allowlisted tasks. OSRIVA never changes
firewall, SSH, disk or network settings silently.
