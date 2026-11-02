package postgres

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
)

// migrationLockID is an arbitrary constant identifying the OSRIVA migration advisory lock.
const migrationLockID int64 = 727274

// Migrate applies every *.sql file in fsys that is not yet recorded in
// schema_migrations, in lexical order, each in its own transaction. A
// session-level advisory lock serialises concurrent migrators.
func (s *Store) Migrate(ctx context.Context, fsys fs.FS, log *slog.Logger) ([]string, error) {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", migrationLockID); err != nil {
		return nil, fmt.Errorf("acquire migration lock: %w", err)
	}
	defer conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", migrationLockID) //nolint:errcheck

	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    text PRIMARY KEY,
		applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return nil, fmt.Errorf("create schema_migrations: %w", err)
	}

	applied := map[string]bool{}
	rows, err := conn.Query(ctx, "SELECT version FROM schema_migrations")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			return nil, err
		}
		applied[v] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	names, err := fs.Glob(fsys, "*.sql")
	if err != nil {
		return nil, err
	}
	sort.Strings(names)

	var done []string
	for _, name := range names {
		version := strings.TrimSuffix(name, ".sql")
		if applied[version] {
			continue
		}
		body, err := fs.ReadFile(fsys, name)
		if err != nil {
			return done, err
		}
		err = pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
			// Simple-protocol exec so a file may contain multiple statements.
			if _, err := tx.Conn().PgConn().Exec(ctx, string(body)).ReadAll(); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, "INSERT INTO schema_migrations (version) VALUES ($1)", version)
			return err
		})
		if err != nil {
			return done, fmt.Errorf("migration %s: %w", name, err)
		}
		if log != nil {
			log.Info("applied migration", "version", version)
		}
		done = append(done, version)
	}
	return done, nil
}
