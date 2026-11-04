// Command worker runs the background jobs of docmatch on River: periodic
// partition maintenance, heartbeat, credit expiry and, in later phases, the
// reconciliation pipeline.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/google/uuid"

	"github.com/mmtmn/docmatch/services/api/internal/audit"
	"github.com/mmtmn/docmatch/services/api/internal/config"
	"github.com/mmtmn/docmatch/services/api/internal/credits"
	"github.com/mmtmn/docmatch/services/api/internal/db"
	"github.com/mmtmn/docmatch/services/api/internal/db/pgstore"
	"github.com/mmtmn/docmatch/services/api/internal/jobs"
	"github.com/mmtmn/docmatch/services/api/internal/logx"
	"github.com/mmtmn/docmatch/services/api/internal/version"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "worker:", err)
		var ve *config.ValidationError
		if errors.As(err, &ve) {
			fmt.Fprintln(os.Stderr, "Consulte .env.example na raiz do repositório.")
			os.Exit(2)
		}
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := logx.New(os.Stdout, cfg.Env)
	log.Info("iniciando worker", slog.Any("version", version.Current()), slog.Any("config", cfg))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := db.Connect(ctx, db.Options{
		URL:             cfg.Database.URL.Reveal(),
		AppRole:         cfg.Database.AppRole,
		ApplicationName: "docmatch-worker",
		MaxConns:        int32(jobs.DefaultMaxWorkers + 4),
		Logger:          log,
	})
	if err != nil {
		return err
	}
	defer pool.Close()

	creditSvc := credits.New(pgstore.NewCreditStore(pool))
	host, _ := os.Hostname()
	deps := jobs.Deps{
		Logger:      log,
		DB:          pool,
		Partitioner: jobs.PoolPartitioner(pool),
		Lister: func(ctx context.Context, now time.Time) ([]uuid.UUID, error) {
			return pgstore.ListOrgsWithExpiredGrants(ctx, pool, now)
		},
		Expirer:  creditSvc,
		Audit:    audit.New(pgstore.NewAuditStore(pool)),
		Hostname: host,
	}
	client, err := jobs.NewClient(pool, deps, jobs.ClientOptions{Periodic: true, ID: host})
	if err != nil {
		return err
	}
	if err := client.Start(ctx); err != nil {
		return fmt.Errorf("iniciar River: %w", err)
	}
	log.Info("worker pronto", slog.String("host", host))

	<-ctx.Done()
	log.Info("encerrando worker: aguardando jobs em andamento")
	stopCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := client.Stop(stopCtx); err != nil {
		log.Warn("jobs não terminaram a tempo; cancelando", slog.String("err", err.Error()))
		hardCtx, hardCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer hardCancel()
		if err := client.StopAndCancel(hardCtx); err != nil {
			return fmt.Errorf("encerramento forçado: %w", err)
		}
	}
	log.Info("worker encerrado")
	return nil
}
