# ADR-0015: Customer Panel and tenant isolation

Status: accepted (Phase 12a, 2026-11-04)

## Context

In Hosting Provider Mode the provider's customers need their own panel to see and
control the VPS they rent. A customer is not an operator: a customer must never reach
operator endpoints, never see another customer's resources, and never learn that other
customers exist.

## Decision

### Separate identities

* **Customers** are their own table, not users with a small role:
  * an e-mail address (unique), a name and a status (active or disabled);
  * an Argon2id password hash;
  * a one-time setup token.

  An operator permission can therefore never be granted to a customer by mistake.
* **Permissions:** operators manage customers with `customers.read` and
  `customers.write` (owner and admin; the auditor reads).
* **Accounts:**
  * Creating a customer returns a one-time setup link, valid 72 hours. Only the hash of
    its token is stored. The customer sets a password with it, and the operator never
    knows that password.
  * A new link can be issued at any time; issuing one revokes the customer's sessions.
  * Disabling a customer revokes the sessions immediately.

### Separate sessions

* **Storage:** `customer_sessions` is a separate table. The token hash is stored, with
  a 12-hour lifetime and a 2-hour idle timeout.
* **Cookie:** `osriva_customer`, HttpOnly, SameSite=Strict, `Secure` in production, with
  `Path=/api/v1/customer`. The browser never sends it to operator endpoints.
* **No crossover:** the operator middleware reads only `osriva_session` and the customer
  middleware reads only `osriva_customer`. A session of one kind is never accepted where
  the other is required.
* **CSRF:** a separate token, derived from the customer session.
* **Logins:** limited per IP, with a lockout per e-mail address. Unknown addresses spend
  the same Argon2 time as known ones, and every attempt is audited.

### Ownership

* **Assignment:** a VPS has an optional `customer_id`. An operator assigns it with
  `customers.write` + `provider.write`. A billing system can pass `customer_email`
  when it provisions a service, to assign it to an existing customer.
* **Queries:** every customer request goes through a store query filtered by the
  customer id. A VPS of another customer, or one with no customer, answers 404 exactly
  like one that does not exist.
* **What a customer can do:**
  * see their VPS: name, address, size, state;
  * start, shut down and reboot it.

  Force stop, delete, rebuild and resizing stay with the operator or the billing system.
* **Suspended services:** a VPS whose provisioned service is suspended, terminating or
  terminated cannot be started by its customer.
* **Agent tasks:** customer actions are queued as system tasks with the same typed
  validation as every agent task. The audit entry records the customer, not an operator.

## Limits

* **Login:** password only. Customers have no MFA or password reset by e-mail yet; the
  operator issues a new setup link instead.
* **Organizations:** one organization; reseller hierarchies and sub-users are not
  implemented.
* **Panel scope:** the panel shows VPS only. Websites, CMS and backups are not offered to
  customers yet, and there are no invoices, by design.
