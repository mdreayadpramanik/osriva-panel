# ADR-0001: License

* Status: Accepted (2026-11-02, chosen by the project owner)
* Decision: **AGPL-3.0-only** for the platform (Core API, worker, agent,
  installer, dashboard, customer panel, adapters). **Apache-2.0** for the client-side
  libraries meant to be embedded in other people's software: `packages/plugin-sdk`
  and `packages/api-client` (when they are created).

## Options evaluated

| License | Effect for OSRIVA |
|---------|-------------------|
| Apache-2.0 | Permissive, with a patent grant. Anyone, including a hosting company, can fork OSRIVA into a closed, proprietary panel and offer it as a service without sharing changes. That conflicts with the goal of staying completely free and open. |
| GPL-3.0 | Strong copyleft, but it triggers only on *distribution*. A hosting provider running a modified panel for its customers over the network distributes nothing, so its changes can stay private (the "SaaS loophole"). |
| AGPL-3.0 | GPL-3.0 plus §13: anyone who lets users interact with a modified version over a network must offer those users the source. This matches the hosting-provider use case, where the Customer Panel is exactly such a network service. |

## Consequences

* Hosting providers can use OSRIVA commercially and for free. If they modify it
  and expose it to their customers, they must offer the modified source to those
  users. Running it unmodified imposes nothing extra.
* External billing systems that talk to OSRIVA only over its REST API/webhooks are
  separate programs and are not covered by the AGPL.
* The plugin SDK and API client are Apache-2.0, so third parties can write
  integrations and plugins under any license. Plugins run out-of-process
  (ADR planned with Phase 13), which keeps that boundary clean.
* Contributions are accepted under the same licenses (inbound = outbound), with
  a DCO sign-off. No CLA, so the project can never be relicensed as proprietary.

## Follow-up

* The `LICENSE` file currently holds the SPDX identifier and pointers. The
  verbatim AGPL-3.0 and Apache-2.0 texts must be added from the official sources
  (gnu.org / apache.org) before the first tagged release. This is tracked in TASKS.md.
