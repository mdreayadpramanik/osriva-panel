# ADR-0012: Server groups, fleet tasks, job queue and shared rate limiting

Status: accepted (Phase 9a, 2026-11-03)

## Context

Phase 9 asks for a multi-server fleet, server groups, and Redis-backed rate limiting
and queues. Before this phase:

* every action targeted one server;
* notifications were sent once, synchronously, without retries;
* the background loops (alert evaluation, backup schedules) ran in every API process,
  so two API instances would evaluate alerts twice and queue scheduled backups twice;
* rate limits and login lockouts were kept in each process's memory.

## Decision

### Server groups

* A group belongs to an organization and has a unique name, a description and members.
  A server can be in any number of groups.
* Reading groups needs `servers.read`; changing them needs `servers.write`.
* An alert rule can target one server, one group, or every server. A group used by an
  alert rule cannot be deleted. Deleting it would otherwise turn the rule off silently
  or widen it to every server.

### Fleet tasks (batches)

* One task can be queued on many servers at once: a group, or an explicit list of at
  most 200 servers.
* Each server gets an ordinary agent task, created through the same path as a
  single-server task. It goes through the same validation, permission class, audit
  entry and redaction, and records its batch id.
* Only a closed allowlist of task types can be run as a batch (`agenttask.Bulk`):
  * read-only inventory tasks;
  * service actions;
  * SSH key add and remove;
  * Docker image pull and prune;
  * time zone change.
* Tasks that name a per-server object, such as a container, app, site, backup or file
  path, are excluded. So are tasks whose result can carry file contents. Re-running
  something that only makes sense on one host should be a deliberate, per-server act.
* Re-authentication is checked once for the whole batch.
* Servers that are pending, revoked or offline are skipped with a reason and are not
  queued. A task on an offline agent would only expire two minutes later.
* The batch is created in one transaction with its tasks, so a batch is never half
  queued.

### Job queue (PostgreSQL, not Redis)

* Durable background work uses a `jobs` table claimed with
  `UPDATE … WHERE id IN (SELECT … FOR UPDATE SKIP LOCKED)`.
* A claim takes a lease (2 minutes). A job whose worker died is claimed again when the
  lease runs out.
* Failures are retried with exponential back-off: 30 s, 1 min, 2 min, 4 min, 8 min.
  After 6 attempts the job is marked `failed`. A handler can declare an error permanent
  to stop retrying at once.
* The first user is alert notifications. Each (event, channel) pair is one job.
  * **Webhooks:** an HTTP 4xx response is permanent, except 408 and 429; network errors
    and 5xx responses are retried. Every attempt carries an `X-Osriva-Delivery` id that
    stays the same across retries, so receivers can drop duplicates. A lease can expire
    while a slow receiver still answers, so delivery is at least once.
  * **E-mail:** SMTP 5xx replies are permanent.
  * **Events:** each event tracks pending, delivered and failed counts.
  * **Test notification:** it is still sent synchronously, because the operator wants
    the answer now.
* Finished jobs are deleted after 7 days. Their payload holds only ids, never secrets.
* `GET /api/v1/jobs` (`settings.read`) shows queue counts and recent failures.

PostgreSQL was chosen over Redis for the queue because it is already required. It also
keeps a job transactional with the rows it refers to and survives restarts without
extra configuration. A Redis queue would add a second durable store to back up and
secure, for no gain at this scale.

### Leases for singleton loops

* A `leases` row (name, holder, expiry) lets exactly one API process run each periodic
  loop: alert evaluation, rollups and audit retention (`monitor`), and task expiry and
  backup schedules (`janitor`).
* The holder renews the lease every cycle. Another process takes over within
  3 × the cycle time when the holder stops.
* Metric ingestion, task polling and the job workers run in every process.

### Redis for rate limiting (optional)

* Set `OSRIVA_REDIS_URL` to a `redis://` or `rediss://` URL to share these limits between
  API instances:
  * the per-IP limiters (API requests, login, agent enrolment);
  * the per-account login lockout.
* Rate limiting uses GCRA in a Lua script with Redis' own clock (`TIME`), so API hosts
  with skewed clocks agree.
* The lockout counts failures in a fixed window (`INCR` + `PEXPIRE`), unlike the
  in-memory sliding window, and locks the account with a key that expires.
* **When Redis cannot be reached:** each request falls back to that process's in-memory
  limiter, and a warning is logged at most once a minute. Failing open would remove
  brute-force protection; failing closed would lock everyone out during a Redis outage.
* Without `OSRIVA_REDIS_URL`, nothing changes: the in-memory limiters are used and a
  single API instance is assumed.

## Limits

* **Long-poll wake-ups:** they are still per process. With several API instances, a task
  queued on another instance reaches the agent within the 25 s poll timeout. No shared
  bus (LISTEN/NOTIFY or Redis pub/sub) yet.
* **Batches:** no schedules, staged rollouts (canary, percentage) or automatic stop on
  failure; that belongs to automation (8b).
* **Redis:** it is not part of the default Compose stack. Running several API instances
  behind a load balancer is possible but has not been tested end to end, and high
  availability is not claimed.
* **Notification jobs:** they are not encrypted at rest beyond what PostgreSQL provides.
  They hold only ids.
