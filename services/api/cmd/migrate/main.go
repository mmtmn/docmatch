// Command migrate applies the database schema (db/migrations, embedded) and
// River's queue schema, then grants the application role access.
//
// Usage:
//
//	migrate [up]            aplica todas as migrações pendentes (padrão)
//	migrate down [N]        reverte N migrações (padrão 1; 0 = todas)
//	migrate status          lista as migrações e o estado de cada uma
//	migrate -no-river ...   pula as migrações do River
//
// Environment: DATABASE_URL (obrigatória) e DM_DB_APP_ROLE (padrão docmatch_app).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mmtmn/docmatch/services/api/internal/config"
	"github.com/mmtmn/docmatch/services/api/internal/db"
	"github.com/mmtmn/docmatch/services/api/internal/db/migrations"
	"github.com/mmtmn/docmatch/services/api/internal/logx"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "migrate:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("migrate", flag.ContinueOnError)
	noRiver := fs.Bool("no-river", false, "não aplica as migrações do River")
	timeout := fs.Duration("timeout", 10*time.Minute, "tempo máximo total")
	if err := fs.Parse(args); err != nil {
		return err
	}
	command := "up"
	if fs.NArg() > 0 {
		command = fs.Arg(0)
	}

	dbURL, ok := os.LookupEnv(config.VarDatabaseURL)
	if !ok || dbURL == "" {
		return fmt.Errorf("%s é obrigatória (veja .env.example)", config.VarDatabaseURL)
	}
	appRole := config.DefaultDBAppRole
	if v, ok := os.LookupEnv(config.VarDBAppRole); ok && v != "" {
		appRole = v
	}
	env := os.Getenv(config.VarEnv)
	if env == "" {
		env = config.DefaultEnv
	}
	log := logx.New(os.Stdout, env)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()

	conn, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		return fmt.Errorf("conectar em %s: %w", db.RedactURL(dbURL), err)
	}
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()

	m, err := db.NewMigrator(conn, appRole, log)
	if err != nil {
		return err
	}
	ms := migrations.MustLoad()

	switch command {
	case "up":
		applied, err := m.Up(ctx, ms)
		if err != nil {
			return err
		}
		log.Info("migrações do esquema aplicadas", slog.Int("count", len(applied)), slog.Any("versions", applied))
		if !*noRiver {
			if err := migrateRiver(ctx, dbURL, log); err != nil {
				return err
			}
		}
		if err := db.GrantAppRole(ctx, conn, appRole); err != nil {
			return err
		}
		log.Info("acesso concedido ao papel da aplicação", slog.String("role", appRole))
		return nil
	case "down":
		steps := 1
		if fs.NArg() > 1 {
			n, err := strconv.Atoi(fs.Arg(1))
			if err != nil || n < 0 {
				return errors.New("down [N]: N precisa ser um inteiro não negativo (0 = todas)")
			}
			steps = n
		}
		reverted, err := m.Down(ctx, ms, steps)
		if err != nil {
			return err
		}
		log.Info("migrações revertidas", slog.Int("count", len(reverted)), slog.Any("versions", reverted))
		return nil
	case "status":
		st, err := m.Status(ctx, ms)
		if err != nil {
			return err
		}
		for _, s := range st {
			state := "pendente"
			if s.Applied != nil {
				state = "aplicada em " + s.Applied.AppliedAt.UTC().Format(time.RFC3339)
				if s.Drifted {
					state += " (ARQUIVO ALTERADO DEPOIS DE APLICADA)"
				}
			}
			fmt.Printf("%04d_%s\t%s\n", s.Migration.Version, s.Migration.Name, state)
		}
		return nil
	default:
		return fmt.Errorf("comando desconhecido %q (use up, down ou status)", command)
	}
}

// migrateRiver applies River's own schema with its migrator. River keeps its
// versions in river_migration; the tables are owned by the migration user.
func migrateRiver(ctx context.Context, dbURL string, log *slog.Logger) error {
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		return fmt.Errorf("river: abrir pool: %w", err)
	}
	defer pool.Close()
	versions, err := db.MigrateRiver(ctx, pool, log)
	if err != nil {
		return err
	}
	log.Info("migrações do River aplicadas", slog.Int("count", len(versions)), slog.Any("versions", versions))
	return nil
}
