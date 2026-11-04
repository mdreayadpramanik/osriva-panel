# Operating OSRIVA Panel

This guide is for whoever runs the control plane installed with
`installer/osriva-install.sh`. Every command below was exercised by the Linux system test
(`tests/system/linux-install.sh`) on an Ubuntu 24.04 GitHub runner. Other distributions are
not tested for the installer.

OSRIVA Panel is a **pre-release**. Before you rely on it, read the open items in
`DEVELOPMENT_STATUS.md`.

## What must never be lost

| Item | Where | Why |
|------|-------|-----|
| `OSRIVA_SECRET_KEY` | `/etc/osriva/osriva.env` (mode 0600) | It encrypts every stored secret: agent CA key, deployment secrets, backup keys, notification credentials, API client secrets. A database without its key **cannot be decrypted**, and enrolled agents cannot be served. |
| Database | Docker volume `osriva_postgres-data` | Everything else: users, servers, tasks, history, audit log. |

## Backups of the panel itself

```sh
sudo ./installer/osriva-install.sh backup
```

* **Contents:** each run writes `/var/backups/osriva/<UTC timestamp>/` (mode 0700) with:
  * `database.sql.gz`, a `pg_dump` that is checked with `gzip -t` and for the "dump complete" marker;
  * `osriva.env`, the configuration **including the secret key**;
  * `release`, the release that was running.
* **`latest`:** points to the newest backup.
* **Before upgrades:** `upgrade` always takes a backup first.

### Retention, off-host copies and a daily schedule

```sh
# keep the newest 7, copy the new backup to another machine
sudo ./installer/osriva-install.sh backup --keep 7 --copy-to backup@vault.example.com:/srv/osriva-backups

# the same every day at 03:15 (systemd timer; missed runs catch up after boot)
sudo ./installer/osriva-install.sh schedule-backup --at 03:15 --keep 7 --copy-to backup@vault.example.com:/srv/osriva-backups
sudo ./installer/osriva-install.sh unschedule-backup   # removes the timer, keeps the backups
```

* **`--keep N`:** keeps the newest N backup directories on this host. It deletes only
  directories named like a backup (`<timestamp>` or `<timestamp>-<n>`), never the one `latest`
  points to, and never anything at the destination of `--copy-to`.
* **`--copy-to`:** runs `rsync`. The value is either a local directory (for example a mounted
  second disk, checked byte for byte after the copy) or `user@host:/dir` over SSH.
  * The installer does **not** create SSH keys, install rsync or touch `known_hosts`.
  * Set up root's key and the host key yourself first. SSH runs in batch mode, so a missing
    key fails the run instead of hanging.
  * Old copies at the destination are **not** pruned there; that is the destination's job.
* **Failures:** a failed scheduled backup shows in `systemctl status osriva-backup.service`
  and `journalctl -u osriva-backup.service`.

### Alert when panel backups stop

* **Recording:** every fully successful `backup` (dump verified, copy done) is recorded in
  the panel.
* **The rule:** add a rule under **Alerts → Add rule → Panel backup missing** with a limit
  in hours (26 for a daily backup gives two hours of slack). It notifies your channels when
  no successful backup happened within that time, and resolves after the next one.
* **What it catches:** a failed backup, a failed copy and a timer that stopped running.
* **New rules:** a rule created before the first backup counts from its creation time.
* **What it cannot catch:** the panel itself being down. Use an outside monitor for that,
  for example an uptime service checking `https://<domain>/api/v1/meta`, or a heartbeat
  service pinged after each backup (`ExecStartPost=` in a drop-in for
  `osriva-backup.service`). OSRIVA does not set either up.

The system test runs three backups with `--keep 2 --copy-to <local dir>`, checks the count
and that the copy is identical, refuses `--keep 0` and an unsafe destination, then creates
the timer, starts its service once and removes it again. Copying to a remote host over SSH
is **NOT_TESTED** in CI.

**Protect the copies like a password.** They contain the secret key and every user's data.
Encrypt them at rest wherever they go.

Server backups made *by* the panel (Docker volumes, WordPress) are separate. They are
described in `DEVELOPMENT_STATUS.md` (Phase 7a).

## Restoring a backup (for example on a new host)

1. Install OSRIVA on the new host as usual (same or newer release than the backup):
   `sudo ./installer/osriva-install.sh install --domain panel.example.com --agent`.
2. Copy the backup directory to the new host.
3. Read the plan and confirm:

   ```sh
   sudo ./installer/osriva-install.sh restore --from /path/to/20261104T120000Z
   ```

**Checks before anything changes.** The restore refuses:

* a damaged or incomplete dump;
* a backup without a secret key;
* a backup from a **newer** release, which has migrations this release does not know (upgrade first).

**What it does:**

1. Takes a backup of the current state.
2. Replaces the database with the dump. Anything written on this host since then is lost.
3. Takes over `OSRIVA_SECRET_KEY` from the backup. The previous file is kept as
   `osriva.env.before-restore`.
4. Keeps this host's database password, domain and agent settings.
5. Applies pending migrations and verifies.

**Agents** keep their certificates, because the agent CA is in the database. They reconnect
when the panel answers at the same address they were enrolled with (`OSRIVA_AGENT_PUBLIC_URL`).
If the address changes, point DNS at the new host, or re-enrol the agents.

The system test does the following and checks the outcome:

* backs up;
* creates a group;
* puts a *different* secret key in place, as on a fresh host;
* confirms that a damaged backup is refused;
* restores, then checks that the group is gone and the enrolled agent comes back online.

## Upgrades and rollback

```sh
sudo ./installer/osriva-install.sh upgrade --source /path/to/new/checkout
sudo ./installer/osriva-install.sh rollback   # previous release + the backup taken before the upgrade
```

* **Upgrade order:** `upgrade` backs up, deploys the new release next to the old one,
  migrates and verifies. It rolls back on its own if the checks fail.
* **Migrations:** they are forward-only (ADR-0003), so going back to an older release
  always means restoring the database taken before the upgrade. Data written after the
  upgrade is lost.
* **Which backup rollback uses:** the one `upgrade` took, kept as
  `/var/backups/osriva/pre-upgrade`. Later scheduled backups do not change it, and
  `--keep` never prunes it. Rollback refuses a dump with migrations the previous
  release does not know.
* **One run at a time:** installer runs that change something take turns. A scheduled
  backup that starts during an upgrade waits for it to finish, for up to 15 minutes.
* **Downloads:** verify release files before using them (docs/releasing.md).

## Health and logs

* **Checks:** `sudo ./installer/osriva-install.sh verify` runs the same checks as after an install.
* **Readiness:** the API answers `GET /readyz` on the internal network. The installer checks it
  through the Caddy container, and an external monitor can use `https://<domain>/api/v1/meta`.
* **Logs:** `docker compose --project-name osriva logs api` (structured JSON in production).
  The audit log is in the panel under Audit.
* **Problems:** `repair` rebuilds and recreates the containers of the current release.

## Host hardening (your responsibility)

The installer never changes firewall, SSH, disk or network settings. On a production host:

* **Firewall:** open only 80/443 (and 8443 when agents are used).
* **Access:** keep SSH key-only and the OS patched.
* **Disk:** watch free space (PostgreSQL and backups grow).
* **Mail:** set up SMTP or a webhook channel and an `offline` alert rule, so you hear when
  agents stop reporting.
