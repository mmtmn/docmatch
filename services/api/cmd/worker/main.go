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
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/mmtmn/docmatch/services/api/internal/account"
	"github.com/mmtmn/docmatch/services/api/internal/account/accountpg"
	"github.com/mmtmn/docmatch/services/api/internal/applecredentials"
	"github.com/mmtmn/docmatch/services/api/internal/audit"
	"github.com/mmtmn/docmatch/services/api/internal/auth"
	"github.com/mmtmn/docmatch/services/api/internal/config"
	"github.com/mmtmn/docmatch/services/api/internal/credits"
	"github.com/mmtmn/docmatch/services/api/internal/crypto"
	"github.com/mmtmn/docmatch/services/api/internal/db"
	"github.com/mmtmn/docmatch/services/api/internal/db/pgstore"
	"github.com/mmtmn/docmatch/services/api/internal/email"
	"github.com/mmtmn/docmatch/services/api/internal/email/emailpg"
	"github.com/mmtmn/docmatch/services/api/internal/extensions"
	"github.com/mmtmn/docmatch/services/api/internal/jobs"
	"github.com/mmtmn/docmatch/services/api/internal/jobs/jobargs"
	"github.com/mmtmn/docmatch/services/api/internal/logx"
	"github.com/mmtmn/docmatch/services/api/internal/platforms"
	"github.com/mmtmn/docmatch/services/api/internal/runs/wire"
	"github.com/mmtmn/docmatch/services/api/internal/session"
	"github.com/mmtmn/docmatch/services/api/internal/session/sessionpg"
	"github.com/mmtmn/docmatch/services/api/internal/version"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "burst" {
		if err := runBurst(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "worker burst:", err)
			var ve *config.ValidationError
			if errors.As(err, &ve) {
				fmt.Fprintln(os.Stderr, "Consulte .env.example na raiz do repositório (DM_BURST_*).")
				os.Exit(2)
			}
			os.Exit(1)
		}
		return
	}
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

	// Pipeline jobs (scan, run, learn, outbox) and the scanner verdict consumer.
	sys, err := wire.New(ctx, wire.Options{Config: cfg, Logger: log, Pool: pool})
	if err != nil {
		return err
	}
	defer sys.Close()

	creditSvc := credits.New(pgstore.NewCreditStore(pool))
	host, _ := os.Hostname()
	deps := jobs.Deps{
		Pipeline:    sys.Pipeline(),
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
	// E-mail outbox and account jobs (exports, LGPD erasure).
	register, periodic, err := accountJobs(cfg, log, pool, deps.Audit)
	if err != nil {
		return err
	}
	client, err := jobs.NewClient(pool, deps, jobs.ClientOptions{Periodic: true, ID: host, Register: register, ExtraPeriodic: periodic})
	if err != nil {
		return err
	}
	sys.Jobs.Set(jobargs.RiverEnqueuer{Client: client})
	if err := client.Start(ctx); err != nil {
		return fmt.Errorf("iniciar River: %w", err)
	}
	if consume, ok, err := sys.ScanConsumer(ctx); err != nil {
		return err
	} else if ok {
		go func() { _ = consume(ctx) }()
	}
	// The pool of on-demand workers: leases, caps and the Auto Scaling group.
	if scaler, err := sys.BurstScaler(ctx); err != nil {
		return err
	} else if scaler != nil {
		go func() { _ = scaler.Run(ctx) }()
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

// accountJobs wires the e-mail deliverer and the account service for the
// worker (see internal/email/emailpg and internal/account/accountpg).
func accountJobs(cfg *config.Config, log *slog.Logger, pool *pgxpool.Pool, auditSvc account.Recorder) ([]func(*river.Workers), []*river.PeriodicJob, error) {
	sender, err := email.NewSender(cfg)
	if err != nil {
		log.Warn("envio de e-mail desativado: as mensagens ficam no outbox", slog.String("reason", err.Error()))
	}
	deliverer := emailpg.NewDeliverer(pool, sender, log)
	keys, err := crypto.NewKeyring(cfg.Auth.SessionSecret.Reveal())
	if err != nil {
		return nil, nil, err
	}
	inserter, err := emailpg.NewInserter(pool)
	if err != nil {
		return nil, nil, err
	}
	entitlements, err := account.LoadEntitlements(cfg.Billing.PricingFile)
	if err != nil {
		return nil, nil, err
	}
	support := ""
	if cfg.Email.SecurityContact != nil {
		support = cfg.Email.SecurityContact.Address
	}
	sessions := session.NewManager(sessionpg.New(pool), session.Config{Secure: !cfg.IsDev(), AllowedOrigins: cfg.AllowedOrigins()})
	accountSvc, err := account.New(account.Deps{
		Store: accountpg.New(pool, inserter), Sessions: sessions, Keys: keys, Entitlements: entitlements, Audit: auditSvc,
		Enqueue: accountpg.NewEnqueuer(inserter), APIBaseURL: cfg.APIBaseURL, SupportContact: support, Logger: log,
	})
	if err != nil {
		return nil, nil, err
	}
	platformsSvc, err := platforms.New(cfg, pool)
	if err != nil {
		return nil, nil, err
	}
	var revoke applecredentials.Revoker
	if cfg.Auth.ApplePrivateKey.IsSet() {
		apple, err := auth.NewApple(cfg.Auth.AppleTeamID, cfg.Auth.AppleClientID, cfg.Auth.AppleKeyID, cfg.Auth.ApplePrivateKey.Reveal(), nil)
		if err != nil {
			return nil, nil, err
		}
		revoke = apple.RevokeApple
	}
	appleGrants := applecredentials.New(pool, keys, revoke, log)
	register := []func(*river.Workers){emailpg.AddWorkers(deliverer), applecredentials.AddWorkers(appleGrants), accountpg.AddWorkers(accountSvc), extensions.AddWorkers(pool), platforms.AddWorkers(platformsSvc)}
	periodic := append(emailpg.PeriodicJobs(), accountpg.PeriodicJobs()...)
	periodic = append(periodic, applecredentials.PeriodicJobs()...)
	periodic = append(periodic, extensions.PeriodicJobs()...)
	periodic = append(periodic, platforms.PeriodicJobs()...)
	return register, periodic, nil
}
