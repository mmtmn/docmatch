//go:build integration

// Package testdb provides a migrated PostgreSQL for integration tests.
//
// With DM_TEST_DATABASE_URL set it creates a scratch database on that server
// (fast; the dev compose works); otherwise it starts a pgvector/pgvector:pg18
// container through testcontainers. Either way the schema is migrated with
// the real migrator, the application role is created, and two pools are
// handed out: App (SET ROLE docmatch_app, RLS enforced) and Admin (the
// migration user, which bypasses RLS as a superuser).
package testdb

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/mmtmn/docmatch/services/api/internal/db"
	"github.com/mmtmn/docmatch/services/api/internal/db/migrations"
)

// Environment variables honoured by the harness.
const (
	// EnvDatabaseURL points at an existing server; a scratch database is
	// created there instead of starting a container.
	EnvDatabaseURL = "DM_TEST_DATABASE_URL"
	// EnvImage overrides the container image (default pgvector/pgvector:pg18).
	EnvImage = "DM_TEST_PG_IMAGE"
)

const (
	defaultImage = "pgvector/pgvector:pg18"
	// AppRole is the application role the tests assume.
	AppRole = "docmatch_app"
)

// DB is a migrated database with the two pools.
type DB struct {
	// URL connects as the owner (superuser in the container).
	URL string
	// App assumes the application role: RLS and grants apply.
	App *pgxpool.Pool
	// Admin is the owner connection: bypasses RLS, for seeding and tampering.
	Admin *pgxpool.Pool
	close func()
}

var (
	mu      sync.Mutex
	shared  *DB
	sharedN int
)

// Acquire returns the package-shared database, starting it on first use and
// tearing it down when the last caller releases it. Call it from TestMain or
// from each test; the returned release function must be called.
func Acquire(ctx context.Context) (*DB, func(), error) {
	mu.Lock()
	defer mu.Unlock()
	if shared == nil {
		d, err := start(ctx)
		if err != nil {
			return nil, nil, err
		}
		shared = d
	}
	sharedN++
	release := func() {
		mu.Lock()
		defer mu.Unlock()
		sharedN--
		if sharedN == 0 && shared != nil {
			shared.close()
			shared = nil
		}
	}
	return shared, release, nil
}

// Main is a TestMain helper: starts the database, runs the tests, tears down.
func Main(m *testing.M) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	d, release, err := Acquire(ctx)
	cancel()
	if err != nil {
		fmt.Fprintln(os.Stderr, "testdb:", err)
		os.Exit(1)
	}
	_ = d
	code := m.Run()
	release()
	os.Exit(code)
}

// Get returns the shared database for a test and releases it on cleanup.
func Get(t testing.TB) *DB {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	d, release, err := Acquire(ctx)
	if err != nil {
		t.Fatalf("testdb: %v", err)
	}
	t.Cleanup(release)
	return d
}

func start(ctx context.Context) (*DB, error) {
	var (
		adminURL string
		stop     func()
		err      error
	)
	if base := os.Getenv(EnvDatabaseURL); base != "" {
		adminURL, stop, err = scratchDatabase(ctx, base)
	} else {
		adminURL, stop, err = container(ctx)
	}
	if err != nil {
		return nil, err
	}
	d, err := migrateAndOpen(ctx, adminURL)
	if err != nil {
		stop()
		return nil, err
	}
	prev := d.close
	d.close = func() {
		prev()
		stop()
	}
	return d, nil
}

func container(ctx context.Context) (string, func(), error) {
	image := os.Getenv(EnvImage)
	if image == "" {
		image = defaultImage
	}
	pg, err := tcpostgres.Run(ctx, image,
		tcpostgres.WithDatabase("docmatch_test"),
		tcpostgres.WithUsername("docmatch"),
		tcpostgres.WithPassword("docmatch"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(2*time.Minute)),
	)
	if err != nil {
		return "", nil, fmt.Errorf("testdb: iniciar contêiner %s: %w", image, err)
	}
	url, err := pg.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		_ = pg.Terminate(ctx)
		return "", nil, err
	}
	return url, func() { _ = pg.Terminate(context.Background()) }, nil
}

func scratchDatabase(ctx context.Context, base string) (string, func(), error) {
	var b [6]byte
	_, _ = rand.Read(b[:])
	name := "dm_test_" + hex.EncodeToString(b[:])
	conn, err := pgx.Connect(ctx, base)
	if err != nil {
		return "", nil, fmt.Errorf("testdb: conectar em %s: %w", EnvDatabaseURL, err)
	}
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		_ = conn.Close(ctx)
		return "", nil, fmt.Errorf("testdb: criar banco de teste: %w", err)
	}
	_ = conn.Close(ctx)
	u, err := url.Parse(base)
	if err != nil {
		return "", nil, err
	}
	u.Path = "/" + name
	stop := func() {
		c, err := pgx.Connect(context.Background(), base)
		if err != nil {
			return
		}
		defer func() { _ = c.Close(context.Background()) }()
		_, _ = c.Exec(context.Background(), "DROP DATABASE IF EXISTS "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")
	}
	return u.String(), stop, nil
}

func migrateAndOpen(ctx context.Context, adminURL string) (*DB, error) {
	conn, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		return nil, fmt.Errorf("testdb: conectar: %w", err)
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	m, err := db.NewMigrator(conn, AppRole, log)
	if err != nil {
		_ = conn.Close(ctx)
		return nil, err
	}
	if _, err := m.Up(ctx, migrations.MustLoad()); err != nil {
		_ = conn.Close(ctx)
		return nil, err
	}

	admin, err := db.Connect(ctx, db.Options{URL: adminURL, ApplicationName: "docmatch-test-admin", MaxConns: 8})
	if err != nil {
		_ = conn.Close(ctx)
		return nil, err
	}
	// Mirror cmd/migrate: River's schema, then grants for the application role.
	if _, err := db.MigrateRiver(ctx, admin, log); err != nil {
		_ = conn.Close(ctx)
		admin.Close()
		return nil, err
	}
	if err := db.GrantAppRole(ctx, conn, AppRole); err != nil {
		_ = conn.Close(ctx)
		admin.Close()
		return nil, err
	}
	_ = conn.Close(ctx)
	app, err := db.Connect(ctx, db.Options{URL: adminURL, AppRole: AppRole, ApplicationName: "docmatch-test-app", MaxConns: 32})
	if err != nil {
		admin.Close()
		return nil, err
	}
	return &DB{URL: adminURL, App: app, Admin: admin, close: func() {
		app.Close()
		admin.Close()
	}}, nil
}

// Redact hides the password of a URL for messages.
func Redact(u string) string { return db.RedactURL(u) }

// Short reports whether tests run with -short (skip the heavy ones).
func Short() bool { return testing.Short() }

// IsPermissionDenied reports a PostgreSQL privilege or RLS failure.
func IsPermissionDenied(err error) bool {
	return err != nil && (db.IsRLSViolation(err) || strings.Contains(err.Error(), "row-level security"))
}
