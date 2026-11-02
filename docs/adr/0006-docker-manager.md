# ADR-0006: Docker manager through the agent

Status: accepted (Phase 4a, 2026-11-03)

## Context

Phase 4 adds container management. Access to the Docker Engine is equivalent to
root on the host: one `docker run --privileged -v /:/host` is enough to take over
the machine. The panel must offer useful container management without turning
`servers.operate` into unrestricted root.

## Decision

* The agent talks to the **local Docker Engine API over its Unix socket**
  (`/var/run/docker.sock`, or `DOCKER_HOST=unix://…`) with plain `net/http`. No
  Docker SDK dependency, and the socket is never exposed over the network.
* Docker work is done through new typed tasks in the closed allowlist
  (`docker.info`, `docker.containers`, `docker.container.action|logs|run`,
  `docker.images`, `docker.image.pull|remove`, `docker.images.prune`,
  `docker.volumes`, `docker.networks`). They are validated on the panel **and** on the agent.
* **Running a container has a deliberately small schema:** image, name, command, env,
  ports, named volumes, restart policy. There are no fields for privileged mode,
  host networking or PID/IPC namespaces, capabilities, devices, security options or
  host bind mounts, so strict decoding rejects them. The agent also sets
  `Privileged=false`, `NetworkMode=bridge` and `no-new-privileges`.
* **Ports bind to 127.0.0.1 by default.** Binding on all interfaces needs an explicit `public` flag.
* **Control-plane protection:** OSRIVA's Compose services carry the label
  `dev.osriva.control-plane=true`. The agent refuses start/stop/restart/remove on
  them, and it refuses mounting any volume that they use or that belongs to their
  Compose project, which would expose the panel database.
* **No forced removals:** removing a running container, or an image that is in use,
  fails. Prune only touches dangling images.
* Permissions: listing and logs need `servers.read`; changes need `servers.operate`.
  Re-authentication is required for run, stop, remove, image remove and prune.
  Environment values are replaced by their names in task listings and the audit log.
* `docker.info` reports Docker's absence honestly (`available: false` plus a reason) instead of failing.
  Docker on Windows (named pipe) is **unsupported** for now.

## Consequences

* An operator with `servers.operate` can still run any image. A malicious image
  is contained by Docker's default isolation, not by OSRIVA. Image allowlists and
  registry policy may be added later.
* Exposing a container's port on 127.0.0.1 is safe by default, but it needs a reverse
  proxy (Phase 5) or an explicit public port to be reachable.
* Compose projects are visible (by label) but cannot be deployed yet. That is Phase 4b
  (deployments), which will reuse this client.
