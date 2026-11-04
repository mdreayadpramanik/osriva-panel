# ADR-0014: Signed provisioning API, technical plans and billing webhooks

Status: accepted (Phase 11a, 2026-11-04)

## Context

Phase 11 asks for:

* hosting backends;
* technical templates, deliberately without prices;
* a signed provisioning API, with HMAC signatures, idempotency and replay protection,
  for external billing systems such as WHMCS, Blesta or HostBill.

OSRIVA does no billing. A billing system decides when a customer's service is created,
suspended, unsuspended or terminated, and calls OSRIVA to do it.

A leaked API credential must not be able to do more than the provisioning it was
issued for. A captured request must not be replayable. A billing system's network retry
must not create a second VPS.

## Decision

### API clients

* **Who can manage them:** an operator with `settings.write`, after re-authentication.
  Each client has a name, scopes, an optional list of allowed source networks (CIDR) and
  an optional webhook URL.
* **Secret:** 256 bits, shown once and stored AES-256-GCM encrypted, bound to the client
  id. The plaintext is needed to verify HMACs, so it cannot be a one-way hash.
* **Effective permissions:** a client acts on behalf of the user who created it. Its
  permissions are that user's current permissions intersected with its scopes:
  * `provisioning.read` grants `provider.read` and `servers.read`;
  * `provisioning.write` adds `provider.write` and `servers.operate`.

  A disabled creator or a revoked role therefore immediately limits the client. A client
  can be disabled or deleted at any time.
* **Audit:** every entry records the client id (`api_client`) next to the creator.

### Request signing

Every `/api/v1/provisioning/*` request carries:

```
X-Osriva-Key:       <client key id>
X-Osriva-Timestamp: <unix seconds>
X-Osriva-Nonce:     <16-64 characters [A-Za-z0-9_-], unique per request>
X-Osriva-Signature: sha256=<hex HMAC-SHA256(secret, canonical)>

canonical = METHOD "\n" PATH[?QUERY] "\n" TIMESTAMP "\n" NONCE "\n" hex(SHA-256(body))
```

* **Rejected with 401 (`invalid_signature`):** a timestamp more than 5 minutes off, an
  unknown or disabled key, a signature mismatch (compared in constant time), or a source
  IP outside the allowed networks.
* **Replay protection:** the nonce is stored with the client id (unique) until the
  timestamp window has passed. Reusing it gives 401 `replayed_request`. A request is
  therefore valid exactly once, and only for 5 minutes.
* **Cookies and CSRF tokens:** they play no part; a session cookie on such a request is
  ignored.
* **Rate limit:** per client, beside the per-IP API limit.

### Idempotency

* **Header:** POST and DELETE require `Idempotency-Key` (8-128 characters).
* **Storage:** the first response, its status and its body, is stored per (client, key)
  with the SHA-256 of method, path and body, and kept 24 hours.
  * The same key with the same request returns the stored response, with
    `Idempotency-Replayed: true`.
  * The same key with a different request returns 422.
  * A key still in progress returns 409.
* **External id:** service creation is additionally unique per (client, `external_id`),
  the billing system's own service id. Even a new idempotency key cannot create a second
  VPS for the same service.

### Plans (technical templates)

* **Contents:** a plan names the template, the IP pool, vCPUs, memory and disk.
  Optionally it also names one node; otherwise OSRIVA picks the enabled node with the
  most free memory that still fits the plan.
* **No billing data:** there are no prices, currencies or billing cycles, by design.
* **Permissions:** plans are managed with `provider.write` in Provider Mode.

### Provisioned services

* **Create:** `POST /provisioning/services {plan_id, external_id, hostname, ssh_keys}`
  creates a VPS through the same code path as the dashboard: the same validation,
  capacity, address allocation and audit.
* **States:** `provisioning` → `active`, `suspended`, `failed` → `terminated`.
  * Suspend is a graceful shutdown and unsuspend a start. A suspended service cannot be
    started from the dashboard by mistake.
  * Terminate deletes the VPS and its disk. The service record stays as `terminated`
    for the billing system's history.
* **Reconciliation:** a background loop holds the `provisioning` lease and checks
  services with an operation pending every 15 seconds. The billing system can therefore
  rely on webhooks instead of polling.

### Webhooks

* **Events:** `service.active`, `service.failed`, `service.suspended`,
  `service.unsuspended` and `service.terminated` are POSTed to the client's webhook URL.
* **Signature:** the same scheme as alert webhooks,
  `X-Osriva-Signature: sha256=HMAC(secret, timestamp "." body)`, with the client secret.
* **Delivery:** through the durable job queue (ADR-0012), with retries. Every attempt
  carries `X-Osriva-Delivery`, so receivers can drop duplicates. Deliveries run in parallel and
  may arrive out of order: receivers should compare the `at` time or read the service
  state instead of relying on order.

## Not in 11a

* **Hosting backends** (cPanel/WHM, Plesk, DirectAdmin, ISPConfig, …): they need
  licensed or credentialed test installations, so they are planned as 11b and are not
  claimed.
* **Billing-system modules:** there is no WHMCS, Blesta or HostBill module yet. The API
  is documented in OpenAPI, so a module can be written against it.
* **Scope:** only VPS plans; no shared-hosting, e-mail or DNS products.
