# ADR-0005: Agent tasks and live metrics

* Status: Accepted (2026-11-02)

## Context

Phase 3 lets operators act on servers (list and control services, list
processes) and see live resource usage. The master plan forbids exposing an
unrestricted root shell and requires allowlisted, audited operations.

## Decision

### Tasks

1. **Typed tasks only.** A task is `{id, type, params}` where `type` comes from a
   closed list compiled into both the control plane and the agent:
   `services.list`, `service.action`, `processes.list`. There is no
   "run command" type and no way to add one at runtime.
2. **Defence in depth.** The control plane validates the type, parameters and the
   caller's permission. The agent validates them **again**, independently,
   before acting (e.g. a service name must match `^[A-Za-z0-9@._:-]{1,128}$`,
   an action must be `start|stop|restart|enable|disable`). Tools are invoked with
   explicit argument vectors, never through a shell, and with `--` before user values.
3. **Delivery = long poll over the existing mTLS channel.** The agent calls
   `POST /agent/v1/tasks/poll`. The server answers immediately with pending
   tasks, or holds the request for up to 25 s and wakes it as soon as a task is
   queued. The agent posts results to `POST /agent/v1/tasks/{id}/result`. No
   inbound connection to the server is ever needed.
4. **Lifecycle.** `queued → running → succeeded | failed | expired`. A queued
   task that is not picked up within 2 minutes expires. A running task without a
   result within 5 minutes fails as `timeout`. Results are capped at 1 MiB.
   Every task stores who requested it, and is audited when queued and when
   completed.
5. **Permissions.** `servers.read` may run read-only tasks (`services.list`,
   `processes.list`). `servers.operate` (new) is required for `service.action`.
   `stop` and `disable` additionally need recent re-authentication, because
   stopping the wrong service (e.g. `sshd`) can lock an operator out.
6. **Synchronous UX, asynchronous core.** The API offers `POST
   /servers/{id}/tasks` (queue) and `GET /servers/{id}/tasks/{tid}?wait=20`
   (wait for completion), so the dashboard can show a result as soon as it exists
   without inventing one.

### Metrics

1. The agent samples every 10 s and sends batches with its regular traffic
   (`POST /agent/v1/metrics`, every 30 s): CPU %, memory used/total, swap, load
   average (Unix), disk used/total per mount, network rx/tx bytes per second.
   Rates are computed **on the agent** from two real readings. The first interval
   after start produces no rate rather than a guessed one.
2. The control plane stores samples in `metric_samples` (server, ts, jsonb) and
   prunes those older than 24 h. Long-term retention and alerting arrive in Phase 8.
3. A metric the platform cannot provide is **omitted**, never zero-filled.

## Consequences

* Latency from a click to the agent is about one network round trip (long poll).
  Each agent holds one idle HTTPS request.
* Waiting long polls are tracked in memory per API instance. With several API
  instances, a task queued on another instance is delivered within the 25 s
  poll timeout (no shared bus until Phase 9).
