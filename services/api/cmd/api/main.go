// Command api serves the public HTTP API of docmatch.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mmtmn/docmatch/services/api/internal/account"
	"github.com/mmtmn/docmatch/services/api/internal/account/accountpg"
	"github.com/mmtmn/docmatch/services/api/internal/api"
	"github.com/mmtmn/docmatch/services/api/internal/audit"
	"github.com/mmtmn/docmatch/services/api/internal/auth"
	"github.com/mmtmn/docmatch/services/api/internal/config"
	"github.com/mmtmn/docmatch/services/api/internal/credits"
	"github.com/mmtmn/docmatch/services/api/internal/crypto"
	"github.com/mmtmn/docmatch/services/api/internal/db"
	"github.com/mmtmn/docmatch/services/api/internal/db/pgstore"
	"github.com/mmtmn/docmatch/services/api/internal/email"
	"github.com/mmtmn/docmatch/services/api/internal/email/emailpg"
	"github.com/mmtmn/docmatch/services/api/internal/geo"
	"github.com/mmtmn/docmatch/services/api/internal/httpx"
	"github.com/mmtmn/docmatch/services/api/internal/jobs"
	"github.com/mmtmn/docmatch/services/api/internal/jobs/jobargs"
	"github.com/mmtmn/docmatch/services/api/internal/limits"
	"github.com/mmtmn/docmatch/services/api/internal/logx"
	"github.com/mmtmn/docmatch/services/api/internal/ratelimit"
	"github.com/mmtmn/docmatch/services/api/internal/runs/apix"
	"github.com/mmtmn/docmatch/services/api/internal/runs/wire"
	"github.com/mmtmn/docmatch/services/api/internal/session"
	"github.com/mmtmn/docmatch/services/api/internal/session/sessionpg"
	"github.com/mmtmn/docmatch/services/api/internal/version"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "api:", err)
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
	log.Info("iniciando api", slog.Any("version", version.Current()), slog.Any("config", cfg))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := db.Connect(ctx, db.Options{
		URL:             cfg.Database.URL.Reveal(),
		AppRole:         cfg.Database.AppRole,
		ApplicationName: "docmatch-api",
		Logger:          log,
	})
	if err != nil {
		return err
	}
	defer pool.Close()

	// Authentication, sessions and account routes.
	authSvc, accountSvc, sessions, err := authStack(cfg, log, pool)
	if err != nil {
		return err
	}
	// Pipeline route groups (uploads, estimates, runs, workflows, rules),
	// identified by the session the middleware above authenticated.
	sys, err := wire.New(ctx, wire.Options{Config: cfg, Logger: log, Pool: pool, Auth: sessionResolver(pool)})
	if err != nil {
		return err
	}
	defer sys.Close()
	queue, err := jobs.NewClient(pool, jobs.Deps{Logger: log, DB: pool}, jobs.ClientOptions{Periodic: false})
	if err != nil {
		return err
	}
	sys.Jobs.Set(jobargs.RiverEnqueuer{Client: queue})
	go func() { _ = sys.ListenEvents(ctx) }()
	// Internal endpoint of the pool of on-demand workers (DM_BURST_LISTEN,
	// VPC only). A failure here leaves heavy runs on the main box (the pool
	// falls back), never the API down.
	go func() {
		if err := sys.ServeBurst(ctx); err != nil {
			log.Error("endpoint interno do pool sob demanda parou; execuções pesadas ficam na máquina principal", slog.String("err", err.Error()))
		}
	}()

	handler := api.NewHandler(api.Deps{
		Config: cfg, Logger: log, Limits: limits.Must(), DB: pool,
		Mount:        append([]func(*httpx.Router){authSvc.Mount, accountSvc.Mount}, sys.Mount()...),
		Middleware:   []httpx.Middleware{sessions.Middleware},
		OriginExempt: auth.OriginExempt,
	})
	srv := httpx.New(httpx.ServerOptions{Addr: cfg.APIListen, Handler: handler, Limits: limits.Must(), Logger: log})
	if err := srv.Listen(); err != nil {
		return fmt.Errorf("escutar em %s: %w", cfg.APIListen, err)
	}
	return srv.Serve(ctx)
}

// authStack wires the login, session and account services (see
// internal/auth, internal/session and internal/account). Google, Apple and
// the e-mail sender are optional: a missing configuration disables that
// login with a clear 503 and leaves the rest working.
func authStack(cfg *config.Config, log *slog.Logger, pool *pgxpool.Pool) (*auth.Service, *account.Service, *session.Manager, error) {
	keys, err := crypto.NewKeyring(cfg.Auth.SessionSecret.Reveal())
	if err != nil {
		return nil, nil, nil, err
	}
	inserter, err := emailpg.NewInserter(pool)
	if err != nil {
		return nil, nil, nil, err
	}
	edge := geo.New(cfg.OriginSecret.Reveal(), geo.WithHeaderName(cfg.OriginSecretHeader))
	secure := !cfg.IsDev() || cfg.APIBaseURL.Scheme != "http"
	sessions := session.NewManager(sessionpg.New(pool), session.Config{Secure: secure, AllowedOrigins: cfg.AllowedOrigins(), ClientIP: edge.ClientIP})
	store := accountpg.New(pool, inserter)
	auditSvc := audit.New(pgstore.NewAuditStore(pool))
	policies := ratelimit.Defaults()
	limiter := ratelimit.NewMemory()

	var google, apple *auth.Provider
	if cfg.Auth.GoogleClientID != "" && cfg.Auth.GoogleClientSecret.IsSet() {
		google = auth.NewGoogle(cfg.Auth.GoogleClientID, cfg.Auth.GoogleClientSecret.Reveal(), nil)
	} else {
		log.Warn("login com Google desativado: GOOGLE_OAUTH_CLIENT_ID/SECRET ausentes")
	}
	if cfg.Auth.ApplePrivateKey.IsSet() {
		apple, err = auth.NewApple(cfg.Auth.AppleTeamID, cfg.Auth.AppleClientID, cfg.Auth.AppleKeyID, cfg.Auth.ApplePrivateKey.Reveal(), nil)
		if err != nil {
			return nil, nil, nil, err
		}
	} else {
		log.Warn("login com Apple desativado: APPLE_* ausentes")
	}
	_, emailErr := email.NewSender(cfg)
	if emailErr != nil {
		log.Warn("login por e-mail desativado", slog.String("reason", emailErr.Error()))
	}
	var devGrant auth.DevGrantFunc
	if cfg.Auth.DevLogin {
		log.Warn("LOGIN LOCAL LIGADO (DM_DEV_LOGIN): qualquer pessoa entra sem provedor; só para desenvolvimento")
		devGrant = localCreditGrant(credits.New(pgstore.NewCreditStore(pool)))
	}
	authSvc, err := auth.New(auth.Deps{
		Store: store, Sessions: sessions, Keys: keys, Limits: limits.Must(), Audit: auditSvc,
		Google: google, Apple: apple, EmailEnabled: emailErr == nil,
		GoogleAudiences: cfg.Auth.GoogleNativeClientIDs, AppleAudiences: cfg.Auth.AppleNativeClientIDs, DevLogin: cfg.Auth.DevLogin, DevGrant: devGrant,
		PublicBaseURL: cfg.PublicBaseURL, APIBaseURL: cfg.APIBaseURL, Secure: secure,
		Limiter: limiter, Lockout: ratelimit.NewLockout(policies.Lockout, nil), Policy: policies.Auth,
		ClientIP: edge.ClientIP, Country: edge.Country, Logger: log,
	})
	if err != nil {
		return nil, nil, nil, err
	}
	entitlements, err := account.LoadEntitlements(cfg.Billing.PricingFile)
	if err != nil {
		return nil, nil, nil, err
	}
	support := ""
	if cfg.Email.SecurityContact != nil {
		support = cfg.Email.SecurityContact.Address
	}
	accountSvc, err := account.New(account.Deps{
		Store: store, Sessions: sessions, Keys: keys, Limits: limits.Must(), Entitlements: entitlements, Audit: auditSvc,
		Enqueue: accountpg.NewEnqueuer(inserter), Limiter: limiter, APIBaseURL: cfg.APIBaseURL, SupportContact: support, Logger: log,
	})
	if err != nil {
		return nil, nil, nil, err
	}
	return authSvc, accountSvc, sessions, nil
}

// devLoginCredits is what the local sign-in tops the organization up with,
// at most once a day (five times the starter plan): enough to run the
// examples over and over without Stripe.
const devLoginCredits = 10000

// localCreditGrant gives the local sign-in's organization its daily credits;
// the idempotency key makes every further sign-in that day a no-op.
func localCreditGrant(ledger *credits.Service) auth.DevGrantFunc {
	return func(ctx context.Context, orgID uuid.UUID) error {
		key := "dev-login:" + orgID.String() + ":" + time.Now().UTC().Format(time.DateOnly)
		_, err := ledger.Grant(ctx, orgID, credits.GrantParams{
			Kind: credits.GrantBonus, Amount: devLoginCredits, Source: "login local (desenvolvimento)", IdempotencyKey: key,
		})
		return err
	}
}

// sessionResolver identifies callers of the business endpoints by the session
// the middleware already authenticated, and reads the organization's plan
// under row-level security.
func sessionResolver(pool *pgxpool.Pool) apix.Resolver {
	return func(r *http.Request) (apix.Principal, error) {
		p, ok := session.FromContext(r.Context())
		if !ok {
			if errors.Is(session.AuthError(r.Context()), session.ErrAccessExpired) {
				return apix.Principal{}, apix.ErrSessionExpired
			}
			return apix.Principal{}, apix.ErrUnauthenticated
		}
		var plan string
		err := db.WithOrgTx(r.Context(), pool, p.OrgID, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, "SELECT plan_code FROM orgs WHERE id = $1", p.OrgID).Scan(&plan)
		})
		if err != nil {
			return apix.Principal{}, fmt.Errorf("plano da organização: %w", err)
		}
		uid := p.UserID
		return apix.Principal{OrgID: p.OrgID, UserID: &uid, Plan: plan}, nil
	}
}
