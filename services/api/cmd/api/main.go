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

	"github.com/mmtmn/docmatch/services/api/internal/api"
	"github.com/mmtmn/docmatch/services/api/internal/config"
	"github.com/mmtmn/docmatch/services/api/internal/db"
	"github.com/mmtmn/docmatch/services/api/internal/httpx"
	"github.com/mmtmn/docmatch/services/api/internal/limits"
	"github.com/mmtmn/docmatch/services/api/internal/logx"
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

	handler := api.NewHandler(api.Deps{Config: cfg, Logger: log, Limits: limits.Must(), DB: pool})
	srv := httpx.New(httpx.ServerOptions{Addr: cfg.APIListen, Handler: handler, Limits: limits.Must(), Logger: log})
	if err := srv.Listen(); err != nil {
		return fmt.Errorf("escutar em %s: %w", cfg.APIListen, err)
	}
	return srv.Serve(ctx)
}
