# ADR-0007: Application deployments

Status: accepted (Phase 4b, 2026-11-03)

## Context

Phase 4 asks for Git, ZIP, Dockerfile and Compose deployments with build logs, history
and rollback, and for runtime adapters. Builds run untrusted code from a repository on
the managed server, and the deployment flow handles application secrets.

## Decision

* **An app = one Git repository + one build mode + one container on one server.** It
  is stored in the panel (`apps`), and every deployment is a row (`deployments`) that
  points at an `app.deploy` agent task. Status, errors and logs come from that task.
* **Source:** public repositories over `https://` (or `http://`, for internal mirrors)
  only. The URL must have no credentials, query or fragment. `file://`, `ssh`, `ext::`
  and option-looking refs are rejected on the panel and on the agent. The agent clones
  with `git clone --depth 1 --single-branch --no-tags --branch <ref> -- <url>` and runs:
  * with `protocol.allow=never` plus explicit http/https allowance;
  * with no system or global Git configuration;
  * with prompts off;
  * without submodules.
* **Build:** the agent tars the build context itself and sends it to the Engine API
  `/build`:
  * symbolic links are stored as links, never followed, so nothing outside the checkout
    is read;
  * `.git` is excluded;
  * the limits are 512 MiB and 100 000 files;
  * the context and the Dockerfile must resolve inside the checkout.
* **Build modes (the first runtime adapters):**
  * `dockerfile`: the repository's own Dockerfile;
  * `static`: nginx 1.27;
  * `node`: node:22-alpine, `npm ci`/`npm install --omit=dev`, `npm start` as the
    `node` user.

  `PORT` is set to the container port unless the app sets it. Images are tagged
  `osriva-app/<app>:<deployment-id>` and labelled `dev.osriva.app=<app>`. The 5 newest
  builds are kept for rollback.
* **Release:**
  1. The previous container is renamed and stopped, because it holds the host port.
  2. The new container is started with the same hardening as `docker.container.run`.
  3. After a settle period, the new container must be running with no restarts.
     Otherwise it is removed and the previous one is renamed back and restarted
     (`recovered: true`).
  4. A failed build never touches the running version.
  5. Containers or names that OSRIVA did not create are refused.
* **Rollback** runs an earlier successful image again, with the app's current settings
  and environment.
* **Secrets:** environment variables are stored AES-256-GCM encrypted, bound to the app
  ID. Only their names are returned. The agent needs the values, so they travel in the
  task parameters. When the task finishes, the stored parameters are replaced by their
  redacted form. Task listings and the audit log only ever show variable names.
* **Long tasks:** each task now has its own run timeout (`run_timeout_s`; 20 minutes for
  `app.deploy`). The agent runs deployments next to its normal task loop, one at a time,
  and sends the logs with failures too.
* **Permissions:** `servers.read` to view; `servers.operate` to create or edit apps.
  Deploy, rollback and delete also need re-authentication.

## Not done (later phases)

* **ZIP uploads:** they need a file-transfer channel to the agent; task parameters are
  limited to 1 MiB.
* **Compose deployments:** a Compose file can request privileged containers and host
  mounts, so it needs a policy validator first.
* **Private repositories:** they need deploy keys or tokens.
* **Live build logs:** logs arrive when the deployment finishes.
* **Zero-downtime releases:** the old container is stopped before the new one starts.
* **Health checks** beyond "still running", and **more runtimes** (Python, PHP, Go).
