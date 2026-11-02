# ADR-0003: Forward-only database migrations

* Status: Accepted (2026-11-02)

## Context

Down-migrations are rarely exercised, often lossy (dropping a column destroys
data), and give a false sense of safety during a failed upgrade.

## Decision

* Migrations are forward-only SQL files `NNNN_description.sql` in `migrations/`.
* Each file runs in its own transaction under a PostgreSQL advisory lock, so two
  API instances starting at once cannot race.
* Applied versions are recorded in `schema_migrations`. An applied file must
  never be edited. Changes go in a new migration.
* **Rollback = restore.** The installer's `upgrade` command (Phase 2) takes a
  `pg_dump` before migrating, and `rollback` restores that dump together with the
  previous binaries.

## Consequences

* Migrations must be written so that the previous release still works
  during a rolling upgrade (expand, then contract in a later release).
