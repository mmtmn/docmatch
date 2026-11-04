package db

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/mmtmn/docmatch/services/api/internal/db/migrations"
)

// Migrator applies golang-migrate style SQL files: one transaction per
// migration, a session advisory lock so two deploys cannot race, and a
// checksum of every applied script so a file edited after deployment is
// refused instead of silently diverging.
//
// The app role name is passed to the scripts through the docmatch.app_role
// GUC on the migration connection.
type Migrator struct {
	conn    *pgx.Conn
	appRole string
	log     *slog.Logger
}

// NewMigrator wraps a dedicated connection. appRole may be empty (scripts
// fall back to docmatch_app).
func NewMigrator(conn *pgx.Conn, appRole string, log *slog.Logger) (*Migrator, error) {
	if conn == nil {
		return nil, errors.New("db: conexão nula")
	}
	if err := validateIdentifier(appRole); err != nil {
		return nil, err
	}
	if log == nil {
		log = slog.Default()
	}
	return &Migrator{conn: conn, appRole: appRole, log: log}, nil
}

// Applied is one row of schema_migrations.
type Applied struct {
	Version   int
	Name      string
	Checksum  string
	AppliedAt time.Time
}

// Status pairs a migration with its applied state.
type Status struct {
	Migration migrations.Migration
	Applied   *Applied // nil when pending
	Drifted   bool     // applied checksum differs from the file
}

const schemaMigrationsDDL = `CREATE TABLE IF NOT EXISTS schema_migrations (
  version    bigint PRIMARY KEY,
  name       text NOT NULL,
  checksum   text NOT NULL,
  applied_at timestamptz NOT NULL DEFAULT now()
)`

// lockKey is the advisory lock key shared by every migrator process.
func lockKey() int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte("docmatch:schema_migrations"))
	return int64(h.Sum64()) //nolint:gosec // deterministic key, wraparound is fine
}

func (m *Migrator) prepare(ctx context.Context) (func(), error) {
	if _, err := m.conn.Exec(ctx, "SELECT pg_advisory_lock($1)", lockKey()); err != nil {
		return nil, fmt.Errorf("db: advisory lock: %w", err)
	}
	unlock := func() {
		_, _ = m.conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", lockKey())
	}
	if m.appRole != "" {
		if _, err := m.conn.Exec(ctx, "SELECT set_config('docmatch.app_role', $1, false)", m.appRole); err != nil {
			unlock()
			return nil, fmt.Errorf("db: definir docmatch.app_role: %w", err)
		}
	}
	if _, err := m.conn.Exec(ctx, schemaMigrationsDDL); err != nil {
		unlock()
		return nil, fmt.Errorf("db: criar schema_migrations: %w", err)
	}
	return unlock, nil
}

func (m *Migrator) applied(ctx context.Context) (map[int]Applied, error) {
	rows, err := m.conn.Query(ctx, "SELECT version, name, checksum, applied_at FROM schema_migrations ORDER BY version")
	if err != nil {
		return nil, fmt.Errorf("db: ler schema_migrations: %w", err)
	}
	defer rows.Close()
	out := map[int]Applied{}
	for rows.Next() {
		var a Applied
		if err := rows.Scan(&a.Version, &a.Name, &a.Checksum, &a.AppliedAt); err != nil {
			return nil, err
		}
		out[a.Version] = a
	}
	return out, rows.Err()
}

// Status reports every migration with its applied state.
func (m *Migrator) Status(ctx context.Context, ms []migrations.Migration) ([]Status, error) {
	unlock, err := m.prepare(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()
	applied, err := m.applied(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Status, 0, len(ms))
	for _, mig := range ms {
		s := Status{Migration: mig}
		if a, ok := applied[mig.Version]; ok {
			s.Applied = &a
			s.Drifted = a.Checksum != mig.Checksum
		}
		out = append(out, s)
	}
	return out, nil
}

// Up applies every pending migration in order and returns the versions it
// applied. An applied migration whose file changed aborts before anything
// runs.
func (m *Migrator) Up(ctx context.Context, ms []migrations.Migration) ([]int, error) {
	unlock, err := m.prepare(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()
	applied, err := m.applied(ctx)
	if err != nil {
		return nil, err
	}
	for _, mig := range ms {
		if a, ok := applied[mig.Version]; ok && a.Checksum != mig.Checksum {
			return nil, fmt.Errorf("db: migração %04d_%s foi alterada depois de aplicada (checksum %s ≠ %s); crie uma nova migração", mig.Version, mig.Name, a.Checksum[:12], mig.Checksum[:12])
		}
	}
	var done []int
	for _, mig := range ms {
		if _, ok := applied[mig.Version]; ok {
			continue
		}
		start := time.Now()
		if err := m.runInTx(ctx, mig.Up, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, "INSERT INTO schema_migrations (version, name, checksum) VALUES ($1, $2, $3)", mig.Version, mig.Name, mig.Checksum)
			return err
		}); err != nil {
			return done, fmt.Errorf("db: migração %04d_%s (up): %w", mig.Version, mig.Name, err)
		}
		m.log.Info("migração aplicada", slog.Int("version", mig.Version), slog.String("name", mig.Name), slog.Duration("duration", time.Since(start)))
		done = append(done, mig.Version)
	}
	return done, nil
}

// Down reverts up to steps applied migrations, newest first, and returns the
// versions it reverted. steps <= 0 reverts everything.
func (m *Migrator) Down(ctx context.Context, ms []migrations.Migration, steps int) ([]int, error) {
	unlock, err := m.prepare(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()
	applied, err := m.applied(ctx)
	if err != nil {
		return nil, err
	}
	var done []int
	for i := len(ms) - 1; i >= 0; i-- {
		if steps > 0 && len(done) >= steps {
			break
		}
		mig := ms[i]
		if _, ok := applied[mig.Version]; !ok {
			continue
		}
		start := time.Now()
		if err := m.runInTx(ctx, mig.Down, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, "DELETE FROM schema_migrations WHERE version = $1", mig.Version)
			return err
		}); err != nil {
			return done, fmt.Errorf("db: migração %04d_%s (down): %w", mig.Version, mig.Name, err)
		}
		m.log.Info("migração revertida", slog.Int("version", mig.Version), slog.String("name", mig.Name), slog.Duration("duration", time.Since(start)))
		done = append(done, mig.Version)
	}
	return done, nil
}

// runInTx executes a multi-statement script and the bookkeeping in one
// transaction. Exec without arguments uses the simple protocol, which accepts
// several statements in one call.
func (m *Migrator) runInTx(ctx context.Context, script string, bookkeeping func(tx pgx.Tx) error) (err error) {
	tx, err := m.conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback(context.WithoutCancel(ctx))
		}
	}()
	if _, err = tx.Exec(ctx, script); err != nil {
		return err
	}
	if err = bookkeeping(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// GrantAppRole gives the application role access to tables created outside
// the versioned migrations (River's queue tables). Idempotent; call it after
// every migration run.
func GrantAppRole(ctx context.Context, conn *pgx.Conn, appRole string) error {
	if appRole == "" {
		return nil
	}
	if err := validateIdentifier(appRole); err != nil {
		return err
	}
	role := pgx.Identifier{appRole}.Sanitize()
	stmts := []string{
		`DO $$
DECLARE t text;
BEGIN
  FOR t IN SELECT c.relname FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
           WHERE n.nspname = 'public' AND c.relkind IN ('r', 'p') AND c.relname LIKE 'river\_%'
  LOOP
    EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON %I TO ` + role + `', t);
  END LOOP;
  FOR t IN SELECT c.relname FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
           WHERE n.nspname = 'public' AND c.relkind = 'S' AND c.relname LIKE 'river\_%'
  LOOP
    EXECUTE format('GRANT USAGE, SELECT, UPDATE ON SEQUENCE %I TO ` + role + `', t);
  END LOOP;
END $$`,
	}
	for _, s := range stmts {
		if _, err := conn.Exec(ctx, s); err != nil {
			return fmt.Errorf("db: conceder acesso a %s: %w", appRole, err)
		}
	}
	return nil
}
