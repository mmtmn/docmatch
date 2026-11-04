// Package db holds the PostgreSQL plumbing: the pgx pool that assumes the
// application role, tenant-scoped transactions that set app.org_id for
// row-level security, the migrator and the pgx stores used by the domain
// packages.
//
// Tenant isolation model (see db/migrations/0001_initial.up.sql):
//   - every tenant table has ENABLE + FORCE ROW LEVEL SECURITY with a policy
//     `org_id = app_org_id() OR app_is_system()`;
//   - app_org_id() reads current_setting('app.org_id', true), which WithOrgTx
//     sets with set_config(..., true) so it lives for one transaction only;
//   - app_is_system() reads app.system = 'on', which WithSystemTx sets for
//     cross-tenant maintenance (expiring grants, relaying the outbox);
//   - the API connects as the migration user and immediately SET ROLEs to
//     the NOBYPASSRLS application role, so policies apply even in dev where
//     the login user is a superuser.
package db

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Options configure Connect.
type Options struct {
	// URL is the PostgreSQL connection string.
	URL string
	// AppRole, when non-empty, is assumed with SET ROLE on every connection.
	AppRole string
	// ApplicationName shows up in pg_stat_activity.
	ApplicationName string
	// MaxConns bounds the pool (default 8).
	MaxConns int32
	Logger   *slog.Logger
}

// Connect opens a pool, verifies it with a ping and returns it.
func Connect(ctx context.Context, opts Options) (*pgxpool.Pool, error) {
	if opts.URL == "" {
		return nil, errors.New("db: URL de conexão vazia")
	}
	if err := validateIdentifier(opts.AppRole); err != nil {
		return nil, err
	}
	cfg, err := pgxpool.ParseConfig(opts.URL)
	if err != nil {
		return nil, fmt.Errorf("db: DATABASE_URL inválida: %w", err)
	}
	if opts.MaxConns > 0 {
		cfg.MaxConns = opts.MaxConns
	} else {
		cfg.MaxConns = 8
	}
	if opts.ApplicationName != "" {
		cfg.ConnConfig.RuntimeParams["application_name"] = opts.ApplicationName
	}
	cfg.MaxConnLifetime = time.Hour
	cfg.MaxConnIdleTime = 10 * time.Minute
	cfg.HealthCheckPeriod = 30 * time.Second
	if opts.AppRole != "" {
		role := pgx.Identifier{opts.AppRole}.Sanitize()
		cfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
			_, err := conn.Exec(ctx, "SET ROLE "+role)
			if err != nil {
				return fmt.Errorf("db: SET ROLE %s: %w", opts.AppRole, err)
			}
			return nil
		}
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("db: abrir pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("db: ping inicial: %w", err)
	}
	if opts.Logger != nil {
		opts.Logger.Info("banco conectado", slog.String("role", opts.AppRole), slog.Int("max_conns", int(cfg.MaxConns)))
	}
	return pool, nil
}

// validateIdentifier accepts a simple SQL identifier (or empty).
func validateIdentifier(s string) error {
	if s == "" {
		return nil
	}
	if len(s) > 63 {
		return fmt.Errorf("db: identificador %q longo demais", s)
	}
	for i, c := range s {
		isLetter := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_'
		isDigit := c >= '0' && c <= '9'
		valid := isLetter || (isDigit && i > 0)
		if !valid {
			return fmt.Errorf("db: identificador %q inválido", s)
		}
	}
	return nil
}

// Beginner is satisfied by *pgxpool.Pool and *pgx.Conn.
type Beginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// TxFunc runs inside a transaction.
type TxFunc func(ctx context.Context, tx pgx.Tx) error

// WithOrgTx runs fn in a transaction scoped to orgID: it sets the app.org_id
// GUC locally (SET LOCAL semantics via set_config(..., true)) so row-level
// security policies see exactly that tenant. The transaction commits when fn
// returns nil and rolls back otherwise (including on panic).
func WithOrgTx(ctx context.Context, b Beginner, orgID uuid.UUID, fn TxFunc) error {
	if orgID == uuid.Nil {
		return errors.New("db: org_id vazio")
	}
	return withTx(ctx, b, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT set_config('app.org_id', $1, true)", orgID.String()); err != nil {
			return fmt.Errorf("db: definir app.org_id: %w", err)
		}
		return fn(ctx, tx)
	})
}

// WithSystemTx runs fn in a transaction with app.system = 'on', which the
// policies accept for cross-tenant maintenance. Use it only from workers.
func WithSystemTx(ctx context.Context, b Beginner, fn TxFunc) error {
	return withTx(ctx, b, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT set_config('app.system', 'on', true)"); err != nil {
			return fmt.Errorf("db: definir app.system: %w", err)
		}
		return fn(ctx, tx)
	})
}

func withTx(ctx context.Context, b Beginner, fn TxFunc) (err error) {
	tx, err := b.Begin(ctx)
	if err != nil {
		return fmt.Errorf("db: begin: %w", err)
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback(ctx)
			panic(p)
		}
		if err != nil {
			if rbErr := tx.Rollback(ctx); rbErr != nil && !errors.Is(rbErr, pgx.ErrTxClosed) {
				err = errors.Join(err, fmt.Errorf("db: rollback: %w", rbErr))
			}
		}
	}()
	if err = fn(ctx, tx); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("db: commit: %w", err)
	}
	return nil
}

// IsUniqueViolation reports whether err is a unique constraint violation.
func IsUniqueViolation(err error) bool {
	return pgErrorCode(err) == "23505"
}

// IsCheckViolation reports whether err is a CHECK constraint violation.
func IsCheckViolation(err error) bool {
	return pgErrorCode(err) == "23514"
}

// IsRLSViolation reports whether err is a row-level security policy violation.
func IsRLSViolation(err error) bool {
	return pgErrorCode(err) == "42501"
}

func pgErrorCode(err error) string {
	var pgErr interface{ SQLState() string }
	if errors.As(err, &pgErr) {
		return pgErr.SQLState()
	}
	return ""
}

// RedactURL hides the password of a connection string for logs.
func RedactURL(u string) string {
	at := strings.LastIndex(u, "@")
	scheme := strings.Index(u, "://")
	if at < 0 || scheme < 0 || at < scheme {
		return u
	}
	creds := u[scheme+3 : at]
	if colon := strings.Index(creds, ":"); colon >= 0 {
		creds = creds[:colon] + ":***"
	}
	return u[:scheme+3] + creds + u[at:]
}
