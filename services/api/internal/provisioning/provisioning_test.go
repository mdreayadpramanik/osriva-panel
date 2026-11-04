package provisioning

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/auth"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/identity"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/secrets"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/store/memstore"
)

func TestAbandonedIdempotencyKeyIsReleased(t *testing.T) {
	st := memstore.New()
	keys, _ := secrets.NewKeyring(bytes.Repeat([]byte{6}, 32))
	clock := time.Now()
	id, err := identity.New(identity.Options{Store: st, Keys: keys, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Argon2: auth.Argon2Params{Memory: 1024, Iterations: 1, Parallelism: 1, SaltLen: 16, KeyLen: 32}, Now: func() time.Time { return clock }})
	if err != nil {
		t.Fatal(err)
	}
	s := New(st, id, nil, keys, nil)
	ctx := context.Background()
	if old, err := s.Begin(ctx, "c1", "key-00000001", "h"); err != nil || old != nil {
		t.Fatalf("first: %+v %v", old, err)
	}
	// The API dies before Finish: the key is in progress.
	if old, _ := s.Begin(ctx, "c1", "key-00000001", "h"); old == nil || old.StatusCode != 0 {
		t.Fatalf("still running: %+v", old)
	}
	clock = clock.Add(abandonAfter + time.Second)
	if old, err := s.Begin(ctx, "c1", "key-00000001", "h"); err != nil || old != nil {
		t.Fatalf("an abandoned key must be released for a retry: %+v %v", old, err)
	}
	// A finished response is still replayed, however old.
	s.Finish(ctx, "c1", "key-00000001", 202, []byte(`{"id":"x"}`))
	clock = clock.Add(time.Hour)
	if old, _ := s.Begin(ctx, "c1", "key-00000001", "h"); old == nil || old.StatusCode != 202 {
		t.Fatalf("finished responses are kept: %+v", old)
	}
}
