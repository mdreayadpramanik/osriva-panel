# ADR-0004: Agent enrolment and mutual-TLS channel

* Status: Accepted (2026-11-02)

## Context

Managed servers run the OSRIVA Agent. The control plane must know which agent
it is talking to, agents must not be impersonable, and a stolen credential must
expire on its own. Agents are often behind NAT, so the agent always dials out.

## Decision

1. **Internal CA.** On first start the API creates an ECDSA P-256 CA (10-year
   validity). Its private key is stored only encrypted (AES-256-GCM via the
   OSRIVA master key) in the `agent_ca` table.
2. **Separate agent listener.** Agents connect to a dedicated TLS listener
   (`OSRIVA_AGENT_ADDR`, e.g. `:8443`). It is not behind the reverse proxy
   because the API must see the client certificate itself. Its server certificate
   is issued by the internal CA for the host names in `OSRIVA_AGENT_PUBLIC_URL`.
   TLS 1.3 only, client certificate required and verified against the CA.
3. **Enrolment.** An operator with `servers.write` creates a server record, which
   yields a one-time token (256-bit, SHA-256 hash stored, 1 h expiry). The
   enrolment command shown in the dashboard contains the panel URL, the token
   and the **CA fingerprint**. The agent:
   * fetches the public CA from `GET /api/v1/agent/ca`, computes its SHA-256
     fingerprint itself and **stops before sending the token** if it does not
     match the command (a spoofed panel never sees the token, and the token is
     not burned),
   * generates its private key locally (it never leaves the host),
   * sends a CSR plus the token to `POST /api/v1/agent/enroll` over the
     panel's normal HTTPS,
   * receives its certificate (CN = server ID) and checks it matches its key.
   The token is consumed atomically, so a replayed or raced token fails.
4. **Short-lived credentials.** Client certificates are valid for 30 days. The
   agent renews over mTLS (`POST /agent/v1/renew`) once a third of the
   lifetime remains, with a fresh key each time. Every request checks the
   certificate serial against `agent_certificates` (not revoked, server active).
   Revoking a server revokes all of its certificates at once.
5. **Protocol.** Phase 2 uses HTTPS request/response from agent to API:
   `POST /agent/v1/report` every 60 s with the inventory. The control plane
   cannot send commands yet. Allowlisted typed tasks arrive in Phase 3, over the
   same authenticated channel.

## Consequences

* A renewed certificate does not revoke the previous one; the old one simply
  expires (≤ 30 days). Revoking the server revokes all of them.

* Losing the master key makes the CA unusable, and every agent must re-enrol.
* Agents need outbound access to the agent port. Operators must open it in
  their firewall themselves; OSRIVA does not change firewall rules.
* Online/offline is derived from `last_seen_at` (offline after 3 missed
  reports). It is never stored as a guess.
