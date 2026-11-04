# ADR-0016: Out-of-process plugins with signed manifests

Status: accepted (Phase 13a, 2026-11-04)

## Context

Phase 13 asks for an out-of-process plugin SDK under Apache-2.0 (ADR-0001) and adapter
documentation. ARCHITECTURE.md says third-party code must never run inside the Core API
process.

A plugin is code written by somebody else. The panel holds credentials, so a plugin must
not be able to:

* read the panel's environment, including `OSRIVA_SECRET_KEY`;
* hang a panel goroutine forever;
* flood its memory;
* be swapped on disk for a different binary after an operator approved it.

## Decision

### Process model and protocol

* **One process per call.** A plugin is an executable that the panel starts for every
  call; no plugin keeps state in the panel. Calls never share a process, so one call
  cannot see another's secrets.
* **Protocol (version 1):** the panel writes one JSON request to stdin and closes it. The
  plugin writes one JSON response to stdout and exits.

  ```
  -> {"protocol":1,"method":"notify","params":{...}}
  <- {"ok":true,"result":{...}}
     {"ok":false,"error":{"code":"…","message":"…","permanent":true}}
  ```

  The methods are `describe` (name, version, kinds) and `notify` (for the `notifier`
  kind). The protocol is plain JSON, so plugins can be written in any language. The
  Apache-2.0 Go SDK `pkg/pluginsdk` implements it in a few lines.
* **Limits on every call:**
  * The environment is replaced by a minimal one: `PATH`, `OSRIVA_PLUGIN_PROTOCOL` and,
    on Windows, `SystemRoot`. Nothing of the panel's environment is inherited.
  * The working directory is the plugin's own directory.
  * There is a 20-second timeout. On Linux the plugin runs in its own process group and
    the whole group is killed.
  * stdout is capped at 1 MiB, and only the first 8 KiB of stderr are logged.
  * Secrets reach a plugin only in the request on stdin, never as arguments or
    environment variables.

### Manifests, signatures, approval

* **The manifest:** each plugin directory holds a `plugin.json` with:
  * name, version, publisher, description and kinds;
  * per platform (`linux/amd64`, `windows/amd64`, …), the executable and its SHA-256.

  It is signed with Ed25519. The signature covers the manifest's canonical JSON without
  the signature field.
* **Trust:**
  * The panel trusts only the publisher keys in `OSRIVA_PLUGIN_TRUSTED_KEYS` (base64
    Ed25519 public keys) and loads plugins only from `OSRIVA_PLUGIN_DIR`.
  * Without those settings, plugins are off.
  * A plugin with a bad signature, an untrusted key, a missing binary or a binary whose
    hash differs from the manifest is listed as invalid with the reason, and never runs.
* **Approval:**
  * An operator enables a plugin with `settings.write` and re-authentication. This pins
    the binary's SHA-256 in the `plugins` table.
  * Before every call the binary is hashed again. If it changed, the call is refused
    until the plugin is approved again.
* **Signing tool:** `osriva-plugin keygen | sign | verify` (Apache-2.0, in the SDK) lets
  publishers sign without the panel.

### First extension point: notification channels

* **Channel kind:** a notification channel of kind `plugin` names an enabled `notifier`
  plugin, its settings (JSON, at most 4 KiB) and an optional secret, stored encrypted
  like other channel secrets.
* **Delivery:** it uses the job queue with retries (ADR-0012). A plugin error marked
  `permanent` is not retried.
* **Example plugin:** `pkg/pluginsdk/examples/chat-notifier` posts `{"text": …}` to an
  incoming-webhook URL, the format used by Slack, Mattermost and Rocket.Chat.

## Limits

* **No OS sandbox:** there is no seccomp, namespace or AppArmor confinement. A plugin can
  use the network and read files the API's user can read. Approve only plugins you
  trust. Under the Compose stack the API runs as a non-root user in a distroless
  container.
* **TOCTOU:** the binary is hashed right before it is started. A local attacker who can
  write the plugin directory between hash and start could still swap it; the directory
  must be writable only by the operator.
* **Extension points:** only notifiers. DNS, backup-storage and hosting-backend adapters
  will use the same protocol when they arrive (5b, 7b, 11b).
* **Distribution:** no plugin marketplace or automatic updates.
