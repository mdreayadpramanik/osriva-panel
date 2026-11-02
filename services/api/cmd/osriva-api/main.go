// Command osriva-api runs the OSRIVA Panel Core API and its administrative tasks.
package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/osriva/osriva-panel/migrations"
	"github.com/osriva/osriva-panel/services/api/internal/config"
	"github.com/osriva/osriva-panel/services/api/internal/httpapi"
	"github.com/osriva/osriva-panel/services/api/internal/identity"
	"github.com/osriva/osriva-panel/services/api/internal/logging"
	"github.com/osriva/osriva-panel/services/api/internal/rbac"
	"github.com/osriva/osriva-panel/services/api/internal/secrets"
	"github.com/osriva/osriva-panel/services/api/internal/store"
	"github.com/osriva/osriva-panel/services/api/internal/store/memstore"
	"github.com/osriva/osriva-panel/services/api/internal/store/postgres"
)

// version is set at build time: -ldflags "-X main.version=1.2.3".
var version = "0.1.0-dev"

const usage = `OSRIVA Panel Core API

Usage:
  osriva-api serve                     Run the HTTP API
  osriva-api migrate                   Apply pending database migrations
  osriva-api admin create [flags]      Create a user from the host (e.g. the first owner)
  osriva-api gen-secret                Print a new random OSRIVA_SECRET_KEY
  osriva-api version                   Print the version

Configuration is read from OSRIVA_* environment variables; see .env.example.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "serve":
		err = cmdServe()
	case "migrate":
		err = cmdMigrate()
	case "admin":
		if len(os.Args) < 3 || os.Args[2] != "create" {
			err = errors.New("usage: osriva-api admin create --email EMAIL [--name NAME] [--role owner]")
		} else {
			err = cmdAdminCreate(os.Args[3:])
		}
	case "gen-secret":
		err = cmdGenSecret()
	case "version", "--version", "-v":
		fmt.Println(version)
	case "help", "--help", "-h":
		fmt.Print(usage)
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

type runtime struct {
	cfg   *config.Config
	log   *slog.Logger
	store store.Store
	pg    *postgres.Store // nil when using the development memory store
	id    *identity.Service
}

func setup(ctx context.Context) (*runtime, error) {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return nil, fmt.Errorf("invalid configuration:\n%w", err)
	}
	log := logging.New(os.Stderr, cfg.LogLevel, cfg.LogFormat)
	rt := &runtime{cfg: cfg, log: log}
	if cfg.DevMemoryStore {
		log.Warn("USING IN-MEMORY STORE: development only, ALL DATA IS LOST ON RESTART")
		rt.store = memstore.New()
	} else {
		pg, err := postgres.Open(ctx, cfg.DatabaseURL)
		if err != nil {
			return nil, err
		}
		rt.store, rt.pg = pg, pg
	}
	keys, err := secrets.NewKeyring(cfg.SecretKey)
	if err != nil {
		return nil, err
	}
	rt.id, err = identity.New(identity.Options{
		Store: rt.store, Keys: keys, Log: log,
		SessionTTL: cfg.SessionTTL, IdleTimeout: cfg.SessionIdleTimeout, ReauthWindow: cfg.ReauthWindow,
	})
	if err != nil {
		return nil, err
	}
	return rt, nil
}

// ensureSchema verifies the schema exists. The memory store needs no migrations.
func (rt *runtime) ensureSchema(ctx context.Context) error {
	if rt.pg == nil {
		return nil
	}
	var exists bool
	err := rt.pg.Pool().QueryRow(ctx, `SELECT to_regclass('public.schema_migrations') IS NOT NULL`).Scan(&exists)
	if err != nil {
		return err
	}
	if !exists {
		return errors.New("database is not migrated; run `osriva-api migrate` first")
	}
	names, _ := migrationsList()
	var applied int
	if err := rt.pg.Pool().QueryRow(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&applied); err != nil {
		return err
	}
	if applied < len(names) {
		return fmt.Errorf("database has %d of %d migrations applied; run `osriva-api migrate`", applied, len(names))
	}
	return nil
}

func migrationsList() ([]string, error) {
	entries, err := migrations.FS.ReadDir(".")
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".sql") {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

func cmdServe() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	rt, err := setup(ctx)
	if err != nil {
		return err
	}
	defer rt.store.Close()
	if err := rt.ensureSchema(ctx); err != nil {
		return err
	}
	if err := rt.id.SyncRBAC(ctx); err != nil {
		return err
	}
	if rt.cfg.DevMemoryStore {
		if err := seedDevOwner(ctx, rt); err != nil {
			return err
		}
	}

	srv := &http.Server{
		Addr: rt.cfg.HTTPAddr,
		Handler: httpapi.New(httpapi.Options{
			Identity: rt.id, DB: rt.store, Log: rt.log, Version: version,
			CookieSecure: rt.cfg.CookieSecure, SessionTTL: rt.cfg.SessionTTL, TrustedProxies: rt.cfg.TrustedProxies,
		}),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    64 << 10,
		ErrorLog:          slog.NewLogLogger(rt.log.Handler(), slog.LevelWarn),
	}
	errCh := make(chan error, 1)
	go func() {
		rt.log.Info("OSRIVA Panel API listening", "addr", rt.cfg.HTTPAddr, "version", version, "env", rt.cfg.Env)
		errCh <- srv.ListenAndServe()
	}()
	select {
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		rt.log.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return err
		}
	}
	return nil
}

// seedDevOwner creates an owner in the throwaway memory store from
// OSRIVA_DEV_ADMIN_EMAIL / OSRIVA_DEV_ADMIN_PASSWORD so the dashboard can be
// exercised without PostgreSQL. config.Load guarantees development mode here.
func seedDevOwner(ctx context.Context, rt *runtime) error {
	email, password := os.Getenv("OSRIVA_DEV_ADMIN_EMAIL"), os.Getenv("OSRIVA_DEV_ADMIN_PASSWORD")
	if email == "" || password == "" {
		rt.log.Warn("memory store has no users; set OSRIVA_DEV_ADMIN_EMAIL and OSRIVA_DEV_ADMIN_PASSWORD to seed an owner")
		return nil
	}
	if _, err := rt.id.BootstrapUser(ctx, email, "Development Owner", password, rbac.RoleOwner, "dev-seed"); err != nil {
		return fmt.Errorf("seed development owner: %w", err)
	}
	rt.log.Warn("seeded development owner in memory store", "email", email)
	return nil
}

func cmdMigrate() error {
	ctx := context.Background()
	rt, err := setup(ctx)
	if err != nil {
		return err
	}
	defer rt.store.Close()
	if rt.pg == nil {
		return errors.New("migrate requires PostgreSQL (OSRIVA_DEV_MEMORY_STORE is set)")
	}
	applied, err := rt.pg.Migrate(ctx, migrations.FS, rt.log)
	if err != nil {
		return err
	}
	if err := rt.id.SyncRBAC(ctx); err != nil {
		return err
	}
	if len(applied) == 0 {
		fmt.Println("database is up to date")
	} else {
		fmt.Printf("applied %d migration(s): %s\n", len(applied), strings.Join(applied, ", "))
	}
	return nil
}

func cmdAdminCreate(args []string) error {
	fs := flag.NewFlagSet("admin create", flag.ContinueOnError)
	email := fs.String("email", "", "email address (required)")
	name := fs.String("name", "Administrator", "display name")
	role := fs.String("role", rbac.RoleOwner, "role key: owner, admin, auditor or member")
	passwordStdin := fs.Bool("password-stdin", false, "read the password from the first line of stdin instead of generating one")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *email == "" {
		return errors.New("--email is required")
	}

	password, generated := "", false
	if *passwordStdin {
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		password = strings.TrimRight(line, "\r\n")
	} else {
		raw := make([]byte, 18)
		if _, err := rand.Read(raw); err != nil {
			return err
		}
		password, generated = base64.RawURLEncoding.EncodeToString(raw), true
	}

	ctx := context.Background()
	rt, err := setup(ctx)
	if err != nil {
		return err
	}
	defer rt.store.Close()
	if rt.pg == nil {
		return errors.New("admin create requires PostgreSQL; the in-memory store does not persist")
	}
	if err := rt.ensureSchema(ctx); err != nil {
		return err
	}
	if err := rt.id.SyncRBAC(ctx); err != nil {
		return err
	}
	u, err := rt.id.BootstrapUser(ctx, *email, *name, password, *role, "cli")
	if err != nil {
		return err
	}
	fmt.Printf("created %s user %s (%s)\n", *role, u.Email, u.ID)
	if generated {
		// Printed once to the operator's terminal; it is never stored in plaintext or logged.
		fmt.Printf("generated password: %s\nsign in and change it from Account → Security.\n", password)
	}
	return nil
}

func cmdGenSecret() error {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return err
	}
	fmt.Println(base64.StdEncoding.EncodeToString(raw))
	return nil
}
