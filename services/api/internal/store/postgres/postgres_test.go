package postgres

import (
	"context"
	"os"
	"testing"

	"github.com/osriva/osriva-panel/migrations"
	"github.com/osriva/osriva-panel/services/api/internal/store"
	"github.com/osriva/osriva-panel/services/api/internal/store/storetest"
)

// These tests need a disposable PostgreSQL database. They DROP and recreate
// the public schema of OSRIVA_TEST_DATABASE_URL. Never point it at real data.
func testURL(t *testing.T) string {
	url := os.Getenv("OSRIVA_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("OSRIVA_TEST_DATABASE_URL not set; skipping PostgreSQL integration tests")
	}
	return url
}

func freshStore(t *testing.T) *Store {
	t.Helper()
	ctx := context.Background()
	s, err := Open(ctx, testURL(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{`DROP SCHEMA public CASCADE`, `CREATE SCHEMA public`} {
		if _, err := s.pool.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Migrate(ctx, migrations.FS, nil); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestContract(t *testing.T) {
	testURL(t)
	storetest.Run(t, func(t *testing.T) store.Store { return freshStore(t) })
}

func TestMigrateIsIdempotent(t *testing.T) {
	s := freshStore(t)
	defer s.Close()
	applied, err := s.Migrate(context.Background(), migrations.FS, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) != 0 {
		t.Fatalf("second run applied %v", applied)
	}
}
