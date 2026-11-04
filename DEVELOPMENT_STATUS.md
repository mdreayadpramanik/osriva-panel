# OSRIVA Panel — Development status

Last updated: **2026-11-04** · Current phase: **14a done, v0.1.0 pre-release published** · Next: **owner decisions: GHCR image visibility, external audit; credentials for 5b / 7b / 10b / 11b; 6b / 8b**

**OSRIVA Panel is not production-ready.** It can be installed, enrol servers, show live
metrics and logs, list services and processes, control services, and browse and edit files
inside each agent's allowed directories, manage SSH keys, view users, scheduled tasks, host name and timezone, manage Docker containers and images, deploy apps from Git with history and rollback, serve them on domains through a managed Caddy proxy with automatic HTTPS, install and maintain WordPress, take encrypted, verified backups with tested restores, alert by signed webhook or e-mail with retries, group servers to run one task across many of them, and in Hosting Provider Mode create, control and delete KVM virtual machines on libvirt nodes, also from an external billing system through a signed API, and give customers their own panel for the VPS they own.

## Phase 14a report (security audit, performance, compatibility, release dry run)

Owner decisions: Sigstore keyless signing, and a dry run only (no published release). Evidence: `CI` run 41840856234,
`System tests` run 41840856055 and `Release` dry run 41840910281 (commit `748baf4`, 2026-11-04, all jobs passed). CI run
41838015296 found a real security issue (below); release dry run 41838065000 had been built with the affected Go
release and was not published.

### Security (internal review, not an independent audit)

Report: [docs/security-audit.md](docs/security-audit.md).

| Check | Result |
|-------|--------|
| `govulncheck` in CI | **Finding fixed:** `go.mod` pinned `go 1.26.0`, which has reachable `crypto/x509` (GO-2026-6809) and `net/http` (GO-2026-6612, 6613, 6617) vulnerabilities. The minimum is now `go 1.26.10`, and the release log shows `go1.26.10`. Now: 0 vulnerabilities affecting the code. |
| Route-wide authentication test | 140 routes checked; no gaps (`authz_audit_test.go`) |
| `npm audit --omit=dev` | 0 vulnerabilities |
| Manual review: command execution, SQL, body limits, secrets, comparisons, cookies, XSS, redirects, generated XML, archives | No issue found; accepted risks are listed in the report |

### Performance (PostgreSQL 17 on the GitHub runner, 300 servers, 32 workers)

| Measurement | Result |
|-------------|--------|
| Metrics ingestion (60 samples per server in one batch) | 15 499 samples/s |
| One server's 1-hour chart read | p50 0.42 ms, p95 0.52 ms |
| 300 agents polling at once (one task each) | 12.3 ms per poll; no task handed out twice |
| Fleet list (300 servers) | p50 1.5 ms |
| Alert evaluation, 3 fleet-wide rules × 300 servers | 932 ms (812 ms second pass); 45 of 45 expected alerts fired |

These numbers come from one CI run. They show the order of magnitude, not a capacity guarantee. Alert evaluation makes
per-rule, per-server queries and grows linearly, about 3 ms per rule and server, so with the 30-second cycle it becomes
the first bottleneck at a few thousand servers × rules. Batching those queries is open work.

### Compatibility

| Platform | Result |
|----------|--------|
| linux/arm64 (GitHub `ubuntu-24.04-arm` runner) | agent and shared-package tests passed; real inventory reports `arm64` |
| Inventory and OS detection in containers | Debian 12, Rocky Linux 9.3, AlmaLinux 9.8, Alpine 3.20, openSUSE Leap 15.6 and Fedora 41 identified correctly |

The container runs check inventory parsing only. They do not cover the installer, systemd or tasks on those
distributions.

### Release dry run (Sigstore keyless)

| Step | Result |
|------|--------|
| 9 agent builds, 2 API builds, 3 `osriva-plugin` builds, installer, `SHA256SUMS` | built with `-trimpath`, version embedded |
| `cosign sign-blob` (keyless, GitHub OIDC) | Rekor transparency-log entry created |
| `cosign verify-blob` with the exact workflow identity, then `sha256sum -c` | `Verified OK`, every file OK |
| A tampered checksum list, and a different signer identity | both refused |
| Container images (API, dashboard) | built, not pushed |
| v0.1.0 pre-release (tag `v0.1.0`, Release run 41846227536) | 17 files published; signature verified independently with cosign; images pushed to GHCR and signed (still private) |

### Not done / limitations

* **No independent audit:** no third-party audit, penetration test or fuzzing yet.
* **Release:** on owner request, **v0.1.0 was published as a GitHub pre-release** (Release run 41846227536, tag on
  `b6c6091`). The signature and checksums were verified independently with cosign 2.4.1, and a tampered checksum list
  was refused. That check also showed the documented identity regex failed: `.` escapes did not match on Windows, so
  docs/releasing.md now uses the exact identity. The images are signed but still private on GHCR, and making them
  public is the owner's choice. SBOM and licence report are missing.
* **Verbatim licence texts:** the AGPL-3.0 and Apache-2.0 texts are still to be added from the official sources (Phase 1
  item).
* **Remaining items needing owner input:** 5b, 7b, 10b and 11b need credentials or test servers; 6b and 8b are open.

## Phase 13a report (out-of-process plugin SDK, signed manifests)

Design: [ADR-0016](docs/adr/0016-plugins.md); author guide: [docs/plugins.md](docs/plugins.md). Evidence: `CI` run
41834145603 and `System tests` run 41834145614 (commit `9283fdd`, 2026-11-04, all jobs passed on the first run).

### Implemented and tested

| Feature | Evidence |
|---------|----------|
| Apache-2.0 SDK `pkg/pluginsdk`: JSON over stdio protocol v1, `Serve` helper, Ed25519-signed manifests with per-platform SHA-256 | unit tests (tampered version or hash, untrusted key, path outside the plugin directory refused) |
| `osriva-plugin keygen / sign / verify`; example `chat-notifier` (incoming-webhook format) | CI: key created with mode 0600, plugin signed and verified, `describe` works with an empty environment, appending one line to the binary makes `verify` fail |
| Registry: plugins only from `OSRIVA_PLUGIN_DIR`, signed by `OSRIVA_PLUGIN_TRUSTED_KEYS`; invalid plugins listed with their reason; symlinked binaries refused | tests on Linux (CI) and Windows 11 (local) |
| Approval with `settings.write` + re-auth pins the binary hash; checked again before every call; a changed binary stops | tests: unapproved, tampered and disabled plugins never ran |
| Runner: one process per call, panel environment removed, plugin directory as cwd, 20 s timeout (process group killed on Unix), 1 MiB output cap, secrets only on stdin | tests: `OSRIVA_SECRET_KEY` and the database URL did not reach the plugin, a hanging plugin was killed after 2 s, 2 MiB output and non-JSON answers refused, permanent and retryable errors kept apart |
| Notification channels of kind `plugin` through the job queue; settings masked for readers; the secret stays out of the audit log | e2e test over HTTP: approve, test notification delivered through the plugin to a receiver, unknown plugin refused, disabled plugin stops |
| Dashboard: Settings → Plugins (approve, disable, rescan), plugin channel type in Alerts | `tsc` |

### Limitations

* **No OS sandbox:** there is no seccomp, namespace or AppArmor confinement. A plugin has the API user's network and
  file access, so approve trusted publishers only.
* **Windows:** on timeout only the plugin process is killed, not its children (no job object).
* **Extension points:** only notifiers. DNS, backup storage and hosting backends will be plugins later (5b, 7b, 11b).
* **Distribution:** no marketplace or automatic updates.
* **Chat services:** delivery through the example plugin to Slack, Mattermost or Rocket.Chat themselves is NOT_TESTED
  (only the format).
* **System test:** the Compose stack has no plugin directory mounted, so plugins are not exercised in the system test;
  the evidence comes from Go tests that start real processes.
* **Dashboard:** the plugin cards were type-checked, not clicked through in a browser.

## Phase 12a report (Customer Panel with tenant isolation)

Design: [ADR-0015](docs/adr/0015-customer-panel.md). Evidence: `System tests` run 41829604752 and `CI` run 41829604759
(commit `82ad26f`, 2026-11-04, all jobs passed on the first run). The browser check found a real bug before CI: in
development React runs effects twice, so the setup page threw the one-time token away on the second run; it now only
reads a non-empty URL fragment.

### Implemented and tested

| Feature | Evidence |
|---------|----------|
| Customers are separate accounts (not users, no permissions); one-time setup link (72 h, token only in the URL fragment); Argon2id passwords; login limited per IP with a lockout per address | store contract (memory + PostgreSQL 17: expired, reused and unknown tokens refused); e2e test; Ubuntu CI: setup 204, the same link again 409 |
| Separate session table and cookie `osriva_customer` (HttpOnly, SameSite=Strict, Path=/api/v1/customer) with its own CSRF token | e2e test (cookie attributes, CSRF 403); browser on Windows 11 (cookie not readable by scripts); Ubuntu CI (cookie path) |
| No crossover: customer cookie or token on operator endpoints, and operator cookie or token on the customer API, all give 401 | e2e test (four combinations); Ubuntu CI (both directions); browser: an operator page redirects a signed-in customer to the operator login |
| Ownership: every customer query filters by customer id; another customer's VPS or an unassigned one answers 404; the customer view hides node, pool, task and internal errors | store contract, e2e test; Ubuntu CI (`node_id` absent) |
| Customers may start, shut down and reboot their own VPS; force stop refused; a suspended billing service cannot be started; actions audited with the customer | e2e test; Ubuntu CI under KVM: the customer rebooted the billed VPS, audit entry found |
| Password change signs out other sessions; disabling a customer signs out everywhere | e2e test |
| Operators: `customers.read` / `customers.write`, new setup link (re-auth), VPS assignment; billing API `customer_email` | e2e test; Ubuntu CI (billed VPS appeared in the customer's list) |
| Dashboard: Customers page, owner column on the VPS page, separate `/customer` panel (login, setup, servers, password) | `tsc`; browser: setup link → password → login → "Your servers" |

### Limitations

* **Customer accounts:** no MFA and no self-service password reset by e-mail; the operator issues a new setup link.
* **Panel scope:** customers see VPS only, not websites, backups, consoles or usage graphs.
* **Organizations:** one organization; no resellers or sub-users.
* **Browser check:** the Customers page and VPS assignment were type-checked, and assignment is tested through the
  API; they were not clicked through in a browser this phase.

## Phase 11a report (signed provisioning API, technical plans, billing webhooks)

Design: [ADR-0014](docs/adr/0014-provisioning-api.md). Evidence: `System tests` run 41800723205 and `CI` run
41800723043 (commit `749d022`, 2026-11-04, all jobs passed). System tests run 41798973962 failed only on the Fleet
step's lease check, which predated the new `provisioning` loop lease; the expectation was updated.

### Implemented and tested

| Feature | Evidence |
|---------|----------|
| API clients (`settings.write` + re-auth): secret shown once and encrypted; scopes `provisioning.read` / `provisioning.write` limited to the creator's current permissions; optional source networks and webhook URL; disable or delete | e2e test (auditor refused, read-only client gets 403, disabled client 401) |
| Signed requests: HMAC-SHA256 over method, path and query, timestamp, nonce and body hash; 5-minute window; each nonce accepted once | e2e test (bad signature, old timestamp, tampered query, replay all 401); Ubuntu CI with `openssl`: signed call 200, the same nonce again 401, unsigned 401 |
| Idempotency: POST/DELETE need `Idempotency-Key`; replay returns the stored response, a different body gives 422; one service per (client, `external_id`) | store contract, e2e test; Ubuntu CI: retry with the same key and with a new key gave the same service |
| VPS plans without prices; node chosen by free memory when the plan names none | e2e test; Ubuntu CI |
| Service lifecycle: provisioning → active → suspended → active → terminated (graceful shutdown, then forced off if needed); a client sees only its own services; the VPS and address are released on terminate | e2e test; Ubuntu CI under KVM: billed VPS active at 192.168.122.100, then terminated |
| Signed webhooks through the job queue (`service.active`, `service.suspended`, `service.unsuspended`, `service.terminated`, `service.failed`) | e2e test (all four signatures verified); Ubuntu CI: `service.active` and `service.terminated` received, signature checked with `openssl` |
| Audit entries carry `api_client` beside the creating user | e2e test; Ubuntu CI (`audit_logs`) |
| Dashboard: Plans card (Infrastructure), API clients card (Settings) with the one-time secret | `tsc` |

### Limitations

* **Hosting backends (11b):** cPanel/WHM, Plesk, DirectAdmin and ISPConfig adapters are not implemented. They need
  licensed or credentialed test installations.
* **Billing-system modules:** there is no WHMCS, Blesta or HostBill module yet; the API is documented in OpenAPI.
* **Plans:** only VPS plans; no shared hosting, e-mail or DNS products. No usage metering, by design.
* **Webhooks:** they may arrive out of order and at least once; receivers should use `event_id` and `at`.
* **Dashboard:** the API clients and Plans cards were type-checked but not clicked through in a browser this phase.

## Phase 10a report (Provider Mode: infrastructure and libvirt/KVM VPS)

Design: [ADR-0013](docs/adr/0013-provider-mode-libvirt.md). The owner chose libvirt/KVM as the first adapter because
it can be verified in CI; Proxmox VE follows when a test server is available. Evidence: `System tests` run 41682706322
and `CI` run 41682706833 (commit `cb9be7b`, 2026-11-03, all jobs passed).

The system test found two real bugs, both fixed:

* **Run 41647344934:** under the agent's umask 0077 the image directory was 0700, so QEMU could not open the disk.
  The directories are now set to 0711 explicitly.
* **Run 41658143268:** the guest's console log showed it booting as `ubuntu` with no cloud-init output. The minimal
  image's `linux-kvm` kernel has no SATA driver, so the seed CD-ROM was invisible; it is now a read-only virtio disk.

### Implemented and tested

| Feature | Evidence |
|---------|----------|
| Data centers, racks, nodes (enrolled Linux servers), IPv4 pools, templates; anything still referenced cannot be deleted | store contract (memory + PostgreSQL 17), e2e test; Ubuntu CI |
| `provider.read` / `provider.write`; changes need Hosting Provider Mode | e2e test (refused in Personal Mode, auditor refused) |
| Address allocation: lowest free address, gateway skipped, unique per pool (safe under concurrency), released on delete | store contract, e2e test (pool exhausted); Ubuntu CI: 192.168.122.100 allocated and released |
| Capacity: inventory × overcommit (default 4:1 vCPU, 1:1 memory) | e2e test; Ubuntu CI: a 64-vCPU VPS refused |
| `vm.create`: HTTPS image verified against the publisher's SHA-256, qcow2 overlay, NoCloud seed (hostname, SSH keys, static address), domain with OSRIVA metadata | unit tests (domain XML, cloud-init, checksum mismatch, damaged cache); Ubuntu CI under KVM: Ubuntu 24.04 minimal booted, SSH with the injected key, hostname and address from cloud-init, password login refused |
| Start, graceful shutdown, reboot, force stop (re-auth); delete with re-auth and the typed name removes the domain and disk | unit tests (foreign domains refused, files removed); e2e test (failed operations restore the state); Ubuntu CI: shutdown → stopped, start → SSH again, delete → domain and disk gone, libvirt network untouched |
| Serial console log per VPS (`/var/log/libvirt/qemu/osriva-<id>-console.log`) | Ubuntu CI (used to find the seed bug) |
| Dashboard Infrastructure and VPS pages (Provider Mode only), host check | `tsc`; browser on Windows 11: data center created, a Windows server refused as a node ("libvirt/KVM nodes must run Linux"); `vm.host` on Windows reports unsupported |

### Limitations

* **Hypervisors:** libvirt/KVM on Linux only. The Proxmox VE adapter (10b) needs a test server and API token from the
  owner; OpenStack, CloudStack and vSphere are not implemented.
* **Networking:** IPv4 only, one NIC, the node's existing libvirt network. CI used the `default` NAT network; bridged
  networks with public addresses on a real data-center host are NOT_TESTED.
* **Not implemented:** resize, rebuild, snapshots, live migration, console access in the dashboard, bandwidth and
  usage accounting. No billing, by design.
* **Capacity and disk:** capacity trusts the node's inventory report. Disk space is checked only roughly before
  creating the overlay.
* **Tested images:** only Ubuntu 24.04 minimal. Other cloud images are expected to work with NoCloud and virtio but
  are NOT_TESTED.

## Phase 9a report (server groups, fleet batches, job queue, leases, shared rate limits)

Design: [ADR-0012](docs/adr/0012-fleet-groups-queue.md). Evidence: `System tests` run 41638738285 and `CI` run
41638738815 (commit `55d34cb`, 2026-11-03, all jobs passed). System tests runs 41635158966 and 41637022452 found a real
bug: after an API restart the old process's loop lease blocked alert evaluation and audit retention for up to 90 s.
The process now releases its leases at the start of shutdown, before agent long polls are drained.

### Implemented and tested

| Feature | Evidence |
|---------|----------|
| Server groups (`servers.read` / `servers.write`), unique names, members from the organization only | store contract (memory + PostgreSQL 17), e2e test; Ubuntu CI |
| Alert rules for a group; a group used by a rule cannot be deleted (409) | e2e test (a server outside the group does not fire); Ubuntu CI: group CPU rule fired, delete refused |
| Fleet batches: one allowlisted task on up to 200 servers, same validation, permissions, re-auth and audit as single tasks; offline, pending and revoked servers skipped with a reason; batch and tasks stored in one transaction | unit + e2e tests (two agents over mTLS, file/container/hostname tasks refused, re-auth for SSH keys); Ubuntu CI: batch on the real agent, pending server skipped, `fleet.batch` audited |
| PostgreSQL job queue: `SKIP LOCKED`, 2-minute leases, back-off 30 s · 1 · 2 · 4 · 8 min, 6 attempts, permanent errors (HTTP 4xx except 408/429, SMTP 5xx) | unit tests with a fake clock; store contract; Ubuntu CI: webhook answered 503, 503, then delivered on attempt 3 with the same `X-Osriva-Delivery` id |
| Alert events show pending / delivered / failed per channel; `GET /api/v1/jobs` (`settings.read`) | e2e test; Ubuntu CI |
| Leases: alert evaluation, rollups, retention and the janitor run in one API process; released on shutdown | store contract; Ubuntu CI: `leases` holds `janitor` and `monitor`; retention ran right after a restart |
| Optional `OSRIVA_REDIS_URL`: shared per-IP limits (API, login, enrolment) and login lockout; in-memory fallback when Redis is down | unit tests against Redis 7 in CI (two limiters share one bucket, lock visible to every instance, keys hashed and expiring); fallback test with no Redis |
| OpenAPI contract test: every `/api/v1` route is documented with its method, and the YAML parses (it caught an existing YAML error) | `go test` |
| Dashboard: Fleet page (groups, run a batch, per-server status and results), group target in alert rules, retrying count, job queue card, roadmap corrected | `tsc`; browser on Windows 11 with a real agent: group created, batch ran (1 succeeded, 1 skipped), result shown, alert to an unreachable webhook shown as "1 retrying" |

### Limitations

* **Redis:** it is not in the Compose stack and not exercised by the system test; only the Go tests use a real Redis.
  Several API instances behind a load balancer have not been tested end to end, and HA is not claimed.
* **Long-poll wake-ups:** still per process. A task queued on another instance reaches its agent within 25 s.
* **Batches:** no staged rollout, schedule or stop-on-failure. Only the allowlisted types can run in a batch.
* **Crashed holder:** when the process holding a lease crashes, the loops pause for up to 90 s until the lease expires.
* **Silences, maintenance windows and automation (8b):** not done.

## Phase 8a report (alerts, notifications, metrics and audit retention)

Design: [ADR-0011](docs/adr/0011-alerts-notifications.md). Evidence: `System tests` run 41609898762 and `CI` run
41609898845 (commit `639a2b4`, 2026-11-03, all jobs passed). CI run 41608156420 found a real bug: the PostgreSQL store
sent a rule without channels as `NULL`; fixed.

### Implemented and tested

| Feature | Evidence |
|---------|----------|
| Rules: CPU, memory, disk, load (window with coverage check), agent offline, failed backup; transitions only (firing / resolved) | e2e test; Ubuntu CI: CPU rule fired, offline rule fired after stopping the agent and resolved after starting it |
| Webhooks signed with HMAC-SHA256 over timestamp and body, secret shown once, no redirects | e2e test; Ubuntu CI: every delivery verified with `openssl dgst -hmac` |
| SMTP e-mail (STARTTLS / TLS / local relay), encrypted password, header-safe subjects | e2e test with a fake SMTP server; Ubuntu CI with Mailpit (test and FIRING mails received) |
| Secrets: webhook path and secret never in the audit log; masked for readers without `alerts.write` | e2e test; Ubuntu CI checked `audit_logs` |
| New permissions `alerts.read` / `alerts.write` | e2e test |
| Hourly rollups (90 days) for 7- and 30-day charts | e2e test; Ubuntu CI: seeded samples rolled up and served with `resolution: hour` |
| Audit retention 30-3650 days, hourly, records `audit.pruned` | e2e test; Ubuntu CI: a 40-day-old entry removed after a restart |
| Dashboard Alerts page, metric ranges, retention card | `tsc`; browser on Windows 11 (channel created, one-time secret, failed test shown with its HTTP status) |

### Limitations

* Notifications were sent synchronously without retries; Phase 9a added the job queue.
* No silences, maintenance windows, escalation or per-rule repeat reminders.
* Automation (scheduled actions, remediation) is not done (8b).
* Hourly rollups do not include per-disk series, only the fullest disk.

## Phase 7a report (encrypted backups and restores)

Design: [ADR-0010](docs/adr/0010-backups.md). Evidence: `System tests` run 41603543535 (commit `d6adfe1`,
2026-11-03, both jobs passed) on Ubuntu 24.04. Run 41602378762 passed every volume check and restored the WordPress
database; its file check used HTTP, where WordPress redirects a missing URL, so the check now looks inside the container.

### Implemented and tested

| Feature | Evidence |
|---------|----------|
| age (X25519) encrypted `tar.gz`; agent holds only the recipient; identity encrypted in the panel, redacted from task records, exportable as recovery key (re-auth, audited) | unit + e2e tests (recovery key decrypts data for the recipient); Ubuntu CI: file 0600 and not readable as plain text, no key in `agent_tasks` or `audit_logs` |
| Volume backups preserving owners, modes and symlinks | Ubuntu CI: uid 1234, mode 640 and a symlink restored, newer files removed |
| WordPress backups: consistent `mariadb-dump` + files volume | Ubuntu CI: title changed in the database and a file added after the backup, both undone by the restore |
| Verify: file checksum, full decryption and read; damage reported | unit tests (wrong key, flipped byte); Ubuntu CI: corrupted archive detected |
| Restore verifies first, refuses damaged archives without touching data, stops and restarts affected containers | unit tests; Ubuntu CI: live data unchanged after a refused restore, WordPress back online |
| Extraction refuses traversal, absolute paths and writes through symlinks | unit tests |
| Control-plane volumes refused (installer backup covers them) | unit test |
| Schedules (every N hours, keep newest K) run by the panel janitor as audited system tasks; pruning | e2e test; Ubuntu CI: scheduled backup ran within a minute |
| Dashboard Backups tab (back up now, schedules, verify, restore with typed confirmation, delete, recovery key) | `tsc`; browser with a real agent on Windows 11 (tab and recovery key) |

### Limitations

* **Backups are stored on the same server.** They do not survive the loss of the server. Remote storage (S3-compatible, SFTP) needs owner credentials: Phase 7b.
* Volume backups of running databases are not crash-consistent (the dashboard says so); WordPress backups are.
* Database engines as a service (creating MySQL/PostgreSQL databases and users) are not implemented yet.
* Backups and restores need Docker on Linux; Windows and BSD agents are unsupported.

## Phase 6a report (WordPress)

Design: [ADR-0009](docs/adr/0009-cms-wordpress.md). Evidence: `System tests` run 41599607565 (commit `d08a569`,
2026-11-03, both jobs passed) with the official WordPress, MariaDB and WP-CLI images on Ubuntu 24.04. Two earlier
runs failed because of the test script itself (a curl timeout racing the long poll, and `grep -q` with `pipefail`), not the product.

### Implemented and tested

| Feature | Evidence |
|---------|----------|
| `cms.wp.install`: private network, MariaDB (health check, no published port), WordPress on 127.0.0.1, labelled volumes, WP-CLI install | fake-Engine unit tests; Ubuntu CI: front page served with the title, DB port not published, loopback binding checked |
| Generated admin password shown once, redacted from task records and audit; database password never leaves the server | unit + e2e tests; Ubuntu CI: admin login with the generated password (302 to wp-admin), PostgreSQL checked for the password |
| User values passed to WP-CLI as environment variables only | unit test (title with quotes and `197121id)`) |
| `cms.wp.info`: core version and update, plugins, themes, site URL (without running plugin code) | unit test (incl. a 6000-character line); Ubuntu CI (core 6.x, akismet listed) |
| `cms.wp.verify`: checksums against wordpress.org | Ubuntu CI: core and plugins verified |
| `cms.wp.update` core / plugins / themes | Ubuntu CI: plugin update |
| `cms.remove` keeping or deleting data; refuses objects it did not create | unit tests; Ubuntu CI: volumes kept, then deleted |
| Optional domain creates a website; removing the instance removes it | e2e test |
| Dashboard CMS tab (install, one-time password, info, verify, updates, remove with typed confirmation) | `tsc`; browser with a real agent on Windows 11 (install form, one-time password, honest failure on Windows) |

### Not done / limitations

* Staging, clone and migration, URL changes and backups are not done yet (6b / Phase 7).
* Only WordPress; no other CMS. Core-update rollback is not automatic: take a backup first (Phase 7).
* WordPress through a public domain with Let's Encrypt is NOT_TESTED (same reason as Phase 5a).

## Phase 5a report (reverse proxy, websites, SSL)

Design: [ADR-0008](docs/adr/0008-reverse-proxy.md). Evidence: `System tests` run 41510772108 (commit `64e0486`,
2026-11-03, both jobs passed): real Caddy started by the agent on Ubuntu 24.04, next to the OSRIVA control plane.

### Implemented and tested

| Feature | Evidence |
|---------|----------|
| `proxy.apply` / `proxy.status` / `proxy.disable`: Caddy `2.8.4-alpine` container, host network, capabilities dropped, state in a 0700 root-only directory | fake-Engine unit tests; Ubuntu CI: container settings and directory mode checked |
| Admin API only on a Unix socket (never TCP) | Ubuntu CI: `127.0.0.1:2019` unreachable |
| Busy web ports refused without touching them | unit tests; Ubuntu CI: HTTPS port 443 (held by the control plane) refused, panel still reachable |
| Sites: domain → local port; internal CA, HTTP only, Let's Encrypt; HTTP→HTTPS redirect | Ubuntu CI: `https://ci.localhost` served nginx through the proxy, HTTP returned 308, plain HTTP site served |
| Live reload through the admin API; a rejected config keeps the old one | unit tests; Ubuntu CI: adding and removing sites while running |
| Certificate status (issuer, expiry) from Caddy's storage | unit test with a generated certificate; Ubuntu CI: internal certificate for `ci.localhost` listed |
| DNS hint per domain (resolves / points here / elsewhere) | e2e test; browser with a real agent on Windows 11 (github.com shown as pointing elsewhere) |
| Domains unique across servers, strict validation (no wildcards, IPs, injection), re-auth for all changes | unit + e2e tests |
| Dashboard Websites tab (proxy settings, sites, certificates, DNS) | `tsc`; browser with a real agent on Windows 11 |

### Not tested / limitations

* **Let's Encrypt issuance was not exercised** (needs a public domain and ports 80/443 reachable from the internet). Status: NOT_TESTED.
* On the server that runs the control plane, ports 80/443 are taken by the panel's own Caddy: the proxy must use other ports there (then only internal-CA or HTTP-only sites work).
* No DNS provider adapters, no wildcard or DNS-01 certificates, no custom directives, redirects or auth (Phase 5b and later).

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
| 5a | Reverse proxy (Caddy), websites, automatic HTTPS, certificate status, DNS hint | ✅ Done (Let's Encrypt NOT_TESTED) |
| 5b | DNS provider adapters | Pending (needs provider credentials) |
| 6a | WordPress install, info, checksum verification, updates, remove | ✅ Done |
| 6b | Staging, clone, migrate, other CMSes | Pending |
| 7a | Encrypted, verified backups and restores (volumes, WordPress), schedules | ✅ Done |
| 7b | Remote backup storage, database services | Pending (remote storage needs credentials) |
| 8a | Alerts, webhooks, e-mail, metric rollups, audit retention | ✅ Done |
| 8b | Automation, silences, maintenance windows | Pending (notification queue done in 9a) |
| 9a | Server groups, fleet batches, PostgreSQL job queue, loop leases, optional Redis rate limits | ✅ Done (multi-instance HA not tested) |
| 10a | Provider Mode: data centers, racks, nodes, IP pools, templates, libvirt/KVM VPS lifecycle | ✅ Done (bridged networking NOT_TESTED) |
| 10b | Proxmox VE adapter, more hypervisors | Pending (needs a Proxmox test server) |
| 11a | Signed provisioning API (HMAC, nonce, idempotency), VPS plans, billing webhooks | ✅ Done |
| 11b | Web hosting backend adapters (cPanel, Plesk, DirectAdmin …) | Pending (needs licensed test installations) |
| 12a | Customer Panel: separate accounts and sessions, own VPS only, start / shutdown / reboot | ✅ Done |
| 13a | Out-of-process plugin SDK (Apache-2.0), signed manifests, notifier plugins, adapter docs | ✅ Done (no OS sandbox) |
| 14a | Internal security audit, performance test, arm64 and distro checks, signed release dry run | ✅ Done (no external audit, nothing published) |
