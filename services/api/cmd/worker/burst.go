package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mmtmn/docmatch/services/api/internal/burst"
	"github.com/mmtmn/docmatch/services/api/internal/burst/agent"
	"github.com/mmtmn/docmatch/services/api/internal/config"
	"github.com/mmtmn/docmatch/services/api/internal/harness"
	"github.com/mmtmn/docmatch/services/api/internal/logx"
	"github.com/mmtmn/docmatch/services/api/internal/version"
)

// runBurst is `docmatch-worker burst`: the burst worker of a pool instance
// (docs/escala.md). It reads DM_BURST_API_URL, DM_BURST_POOL_SECRET,
// DM_HARNESS_SOCKET and DM_MEDIA_LOCAL_DIR only: no database, no store
// credentials.
func runBurst(args []string) error {
	fs := flag.NewFlagSet("burst", flag.ContinueOnError)
	imdsURL := fs.String("imds", agent.DefaultIMDS, "serviço de metadados da instância (vazio fora da AWS)")
	instance := fs.String("instance-id", "", "identidade do trabalhador fora da AWS (padrão: o nome da máquina)")
	once := fs.Bool("once", false, "termina depois do primeiro trabalho")
	maxIdle := fs.Duration("max-idle", 0, "termina depois deste tempo sem trabalho (0 = nunca)")
	noticeEvery := fs.Duration("notice-every", agent.DefaultNoticeInterval, "intervalo de consulta do aviso de encerramento (Spot, Auto Scaling)")
	stateDir := fs.String("state-dir", os.Getenv("STATE_DIRECTORY"), "pasta onde fica o último erro, relatado no próximo início (padrão: STATE_DIRECTORY do systemd)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.LoadBurstWorker()
	if err != nil {
		return err
	}
	log := logx.New(os.Stdout, cfg.Env)
	keys, err := burst.DeriveKeys(cfg.PoolSecret.Reveal())
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	id := agent.Identity{InstanceID: *instance, Lifecycle: "local"}
	var imds *agent.IMDS
	if *imdsURL != "" {
		imds = agent.NewIMDS(*imdsURL)
		idCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		got, err := imds.Identity(idCtx)
		cancel()
		if err != nil {
			return fmt.Errorf("identidade da instância: %w (fora da AWS use -imds '')", err)
		}
		id = got
	}
	if id.InstanceID == "" {
		host, _ := os.Hostname()
		id.InstanceID = "local-" + host
	}
	h, err := harness.New(cfg.HarnessSocket)
	if err != nil {
		return err
	}
	defer func() { _ = h.Close() }()
	a, err := agent.New(agent.Options{
		APIURL: cfg.APIURL, Keys: keys, Harness: h, MediaRoot: cfg.MediaLocalDir, Identity: id, IMDS: imds,
		APIDownAfter: agent.DefaultAPIDownAfter, Once: *once, MaxIdle: *maxIdle, NoticeInterval: *noticeEvery, Logger: log,
		StateDir: *stateDir, Version: version.Current().Version + "+" + version.Current().GitSHA,
	})
	if err != nil {
		return err
	}
	log.Info("iniciando trabalhador sob demanda", slog.Any("version", version.Current()), slog.String("instance", id.InstanceID))
	if err := a.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		agent.RecordFailure(*stateDir, err)
		return err
	}
	return nil
}
