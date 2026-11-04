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

The installer does **not** do the following. They are your job:

* **Copy backups off the host.** A backup on the same disk does not survive a disk failure.
  Copy the whole directory with `rsync`, `scp` or your backup system.
* **Protect the copies like a password.** They contain the secret key and every user's data.
  Encrypt them at rest wherever they go.
* **Prune old backups.** There is no retention: each run adds a directory. Delete old
  ones yourself, for example `find /var/backups/osriva -maxdepth 1 -name '20*' -mtime +30`,
  and check the list before adding `-exec rm -r {} +`.
* **Schedule it.** For example, a root cron entry or a systemd timer running `backup`
  nightly, followed by your off-host copy.

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
