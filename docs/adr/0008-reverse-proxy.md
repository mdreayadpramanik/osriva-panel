# ADR-0008: Reverse proxy and websites

Status: accepted (Phase 5a, 2026-11-03)

## Context

Apps listen on 127.0.0.1 (ADR-0006/0007). Visitors need domains, HTTPS and automatic
certificate renewal. The proxy runs on managed servers that OSRIVA does not own, and it
must not silently take ports or change firewall settings.

## Decision

* **Caddy as the proxy.** The agent runs it in a container named `osriva-proxy`
  (`caddy:2.8.4-alpine`):
  * host networking, so it binds the web ports and reaches apps on 127.0.0.1;
  * not privileged, `no-new-privileges`, all capabilities dropped except `NET_BIND_SERVICE`;
  * restarted unless stopped.

  Caddy handles ACME (Let's Encrypt by default, optionally the staging CA), renewal,
  HTTP→HTTPS redirects and an internal CA.
* **State on the server:** `/var/lib/osriva-agent/proxy`, mode 0700 and root only. It
  holds the Caddyfile, the applied configuration, Caddy's data (certificates and keys)
  and the admin socket.
  * **The admin API listens only on a Unix socket in that directory.** It is never on
    TCP, so local users cannot reconfigure the proxy.
* **Desired state from the panel.** The panel stores the proxy settings (enabled, ports,
  ACME e-mail, staging) and the sites (domain → local port, TLS mode: `acme`,
  `internal` or `none`). After every change it sends the complete configuration
  (`proxy.apply`).
  * The agent renders a Caddyfile only from validated values: RFC 1123 domains,
    integers, and an e-mail address without spaces or braces.
  * When Caddy is running, the agent applies the new configuration through
    `POST /load`. Caddy validates it, keeps the old configuration if it is rejected,
    and the file on disk is only replaced after success.
* **No silent port grabs.** Before starting, the agent checks that the HTTP and HTTPS
  ports are free. If they are not, it refuses and names the port. On the control-plane
  host, ports 80/443 already belong to the OSRIVA panel itself.
* **No firewall changes:** the dashboard says so.
* **Disabling** removes the container and keeps certificates and settings on disk.
* **Certificate status** comes from Caddy's storage (issuer and expiry of each leaf
  certificate).
* **DNS check:** the panel resolves each domain and compares it with the address the
  agent connects from. It is shown as a hint, never as a gate (NAT, CDNs).
* **Permissions:** `servers.read` to view. `servers.operate` plus re-authentication for
  any proxy or site change.
* Domains are unique across all servers.

## Not done (later)

* DNS provider adapters (Cloudflare and others: Phase 5b, needs API credentials) and
  DNS-01 / wildcard certificates.
* Sharing ports 80/443 with the control-plane Caddy on a single-server install.
* Custom Caddy directives, redirects, basic auth, static file sites, per-site logs.
* Proxies other than Caddy (nginx, Apache, Traefik) and existing web-server configs.
