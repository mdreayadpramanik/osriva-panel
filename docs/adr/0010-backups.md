# ADR-0010: Encrypted, verified backups

Status: accepted (Phase 7a, 2026-11-03)

## Context

Phase 7 asks for encrypted and verified backups and restores. Backups hold everything
an attacker wants (databases, uploads, configuration), and a backup that cannot be
restored is worse than none, because people rely on it.

## Decision

* **Format:** `tar` (modes, owners, symlinks preserved) → gzip → **age** encryption
  (X25519, `filippo.io/age`). It is a published, audited format: an operator can decrypt
  a backup without OSRIVA (`age -d -i key.txt file.osb | tar -xz`). The archive starts
  with `manifest.json`.
* **Keys:** one age key pair per server, created on first use.
  * **The agent only receives the recipient (public key).** A stolen server disk does not
    reveal backup contents.
  * The identity is stored in the panel, encrypted with the master key (AES-GCM, bound to
    the server ID).
  * It is sent to the agent only for `backup.verify` and `backup.restore`, and the task
    parameters are redacted when the task ends (any `*_key` parameter).
  * Owners can export the identity as a recovery key (re-authentication, audited)
    and keep it outside OSRIVA.
  * A server's key is never replaced (`EnsureBackupKey`), so existing backups stay readable.
* **Sources:**
  * `volume`: a local Docker volume, read from its mount point by the root agent. Files
    are copied while containers run, so this is not crash-consistent for databases; the
    dashboard says so.
  * `wordpress`: `mariadb-dump --single-transaction` (a consistent InnoDB snapshot
    without downtime) in a short-lived client container on the instance network, plus
    the files volume.
  * Volumes used by the OSRIVA control plane are refused; `osriva-install.sh backup`
    covers those.
* **Integrity:**
  * The SHA-256 of the encrypted file and of the plain tar stream are recorded at
    creation.
  * `backup.verify` checks the file hash, decrypts and decompresses everything, and
    reads every tar entry. Damage is reported as a result, not hidden.
* **Restore is all-or-nothing up front:**
  1. The archive is verified completely before anything is touched.
  2. The containers that use the data are stopped, the volume is emptied and the
     entries are extracted.
     * Traversal, absolute names and writes through symlinks are refused.
     * Files are created with `O_EXCL`.
     * Owners and modes are restored.
  3. The database is imported, and the stopped containers are started again.
  4. Re-authentication and a typed confirmation are required.
* **Storage:** `/var/lib/osriva-agent/backups` (0700, files 0600) on the server itself.
* **Schedules:** per target, every N hours, keep the newest K. The panel's janitor (every
  30 s) starts due backups as system tasks (audited without an actor) and prunes older
  scheduled backups. Manual backups are never pruned.

## Consequences and limits

* **Local backups do not survive the loss of the server.** Remote targets (S3-compatible
  storage, SFTP) need credentials from the owner and follow in Phase 7b.
* **Restoring needs the panel or the exported recovery key.**
* **Database engines as managed services** (MySQL/PostgreSQL databases and users) are
  not part of 7a.
