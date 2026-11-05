// Command api serves the public HTTP API of docmatch.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mmtmn/docmatch/services/api/internal/account"
	"github.com/mmtmn/docmatch/services/api/internal/account/accountpg"
	"github.com/mmtmn/docmatch/services/api/internal/api"
	"github.com/mmtmn/docmatch/services/api/internal/audit"
	"github.com/mmtmn/docmatch/services/api/internal/auth"
	"github.com/mmtmn/docmatch/services/api/internal/config"
	"github.com/mmtmn/docmatch/services/api/internal/crypto"
	"github.com/mmtmn/docmatch/services/api/internal/db"
	"github.com/mmtmn/docmatch/services/api/internal/db/pgstore"
	"github.com/mmtmn/docmatch/services/api/internal/email"
	"github.com/mmtmn/docmatch/services/api/internal/email/emailpg"
	"github.com/mmtmn/docmatch/services/api/internal/geo"
	"github.com/mmtmn/docmatch/services/api/internal/httpx"
	"github.com/mmtmn/docmatch/services/api/internal/limits"
	"github.com/mmtmn/docmatch/services/api/internal/logx"
	"github.com/mmtmn/docmatch/services/api/internal/ratelimit"
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
	handler := api.NewHandler(api.Deps{
		Config: cfg, Logger: log, Limits: limits.Must(), DB: pool,
		Mount:        []func(*httpx.Router){authSvc.Mount, accountSvc.Mount},
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
	edge := geo.New(cfg.OriginSecret.Reveal())
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
	authSvc, err := auth.New(auth.Deps{
		Store: store, Sessions: sessions, Keys: keys, Limits: limits.Must(), Audit: auditSvc,
		Google: google, Apple: apple, EmailEnabled: emailErr == nil,
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
