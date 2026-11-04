# Production checklist (owner)

The code-side hardening is done and tested in CI (see `DEVELOPMENT_STATUS.md`). The
items below need people, money, real hosts or credentials, so the owner does them. Tick
each one only when its "done when" line is true. When everything in sections 1–3 is
ticked, a `v1.0.0` release can be tagged (docs/releasing.md).

## 1. Soak test on real hosts (2–4 weeks)

- [ ] **Panel host:** a fresh VPS with Ubuntu 24.04, at least 2 vCPU and 4 GB RAM, and a real
      domain pointing at it.

      ```sh
      sudo ./installer/osriva-install.sh install --domain panel.example.com --admin-email you@example.com --agent
      ```

      Done when the browser shows a valid Let's Encrypt certificate (this is NOT_TESTED
      in CI).
- [ ] **Firewall:** open 80/tcp, 443/tcp and 8443/tcp yourself. OSRIVA never changes it.
- [ ] **Agents:** enrol at least three servers with the one-line command from **Servers → Add
      server**, including one Debian 12 or Rocky/Alma 9 host if you have one.
      Done when all show *online* with live metrics.
- [ ] **Alerts:** under **Alerts**, add an e-mail or webhook channel and these rules:
      - `offline` (5 min)
      - `disk` (90 %)
      - `Panel backup missing` (26 h)

      Done when a test notification arrives.
- [ ] **Backups:**

      ```sh
      sudo ./installer/osriva-install.sh schedule-backup --at 03:15 --keep 14 --copy-to backup@other-host:/srv/osriva
      ```

      SSH key and rsync on both hosts are yours to set up (remote copy is NOT_TESTED in CI).
      Done when a backup appears on the other host the next morning.
- [ ] **Restore drill:** restore one of those copies onto a second, empty VPS (docs/operations.md,
      "Restoring a backup"). Done when you can log in there and the agents reconnect
      after you move the DNS name.
- [ ] **Daily use:** use the panel for real work for 2–4 weeks: deployments, websites,
      WordPress, server backups and, if you sell VPS, Provider Mode on a KVM host.
      Write down every error, odd behaviour or slow page with the time it happened.
- [ ] **Upgrade:** do one `upgrade` and one `rollback` on the real panel, with a backup copied
      off the host first.

**Done when:** nothing in the notes is unexplained and every bug found is fixed and
retested. Send the notes and `docker compose --project-name osriva logs api` output
for each problem, so it can be fixed.

## 2. Independent security review

- [ ] **Find a reviewer:** a firm or an experienced freelance penetration tester. Ask for a web
      application test of the panel and the agent protocol, and a source code review of
      `services/api`, `agent` and `installer`.
- [ ] **Give access:** a test installation (never the soak-test panel with real data) and the
      repository. `docs/security-audit.md` lists what was already checked.
- [ ] **Fix the findings:** every critical, high and medium finding fixed and retested by the
      reviewer. Low findings fixed or listed as accepted risks in
      `docs/security-audit.md`.

## 3. Owner decisions

- [ ] **Container images:** make the GHCR images public (GitHub → Packages → each package →
      Package settings → Change visibility), or document that installs build locally.
- [ ] **Security contact:** set up a security contact address for SECURITY.md and check that
      someone reads it.
- [ ] **Support policy:** decide which OS versions you support, and update
      COMPATIBILITY_MATRIX.md after the soak test.

## 4. Optional features (each needs your account or server)

These are not needed for v1.0, only for the features themselves:

| Feature | Needs |
|---------|-------|
| DNS automation (5b) | a Cloudflare API token limited to one test zone |
| Remote backup storage (7b) | an S3-compatible bucket and access key (Backblaze B2, Wasabi or AWS S3) |
| Proxmox VPS (10b) | a Proxmox VE test host and an API token |
| cPanel / Plesk (11b) | a licensed test installation |

Enter credentials yourself in the panel when the feature exists. Never paste them into
chat, issues or commits.
