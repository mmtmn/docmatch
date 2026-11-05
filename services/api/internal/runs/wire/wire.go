// Package wire assembles the pipeline from the process configuration: the
// object stores, the harness client, the pricing table, the signer and the
// services of uploads, estimates, rules, workflows and runs. cmd/api mounts
// the route groups it returns; cmd/worker hands its Pipeline to the River
// client. Keeping the assembly here keeps the two main packages to a few
// lines each.
package wire

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mmtmn/docmatch/services/api/internal/audit"
	"github.com/mmtmn/docmatch/services/api/internal/config"
	"github.com/mmtmn/docmatch/services/api/internal/credits"
	dbpgstore "github.com/mmtmn/docmatch/services/api/internal/db/pgstore"
	"github.com/mmtmn/docmatch/services/api/internal/email/emailpg"
	"github.com/mmtmn/docmatch/services/api/internal/estimate"
	"github.com/mmtmn/docmatch/services/api/internal/estimate/pricing"
	"github.com/mmtmn/docmatch/services/api/internal/estimate/throughput"
	"github.com/mmtmn/docmatch/services/api/internal/harness"
	"github.com/mmtmn/docmatch/services/api/internal/httpx"
	"github.com/mmtmn/docmatch/services/api/internal/jobs"
	"github.com/mmtmn/docmatch/services/api/internal/jobs/jobargs"
	"github.com/mmtmn/docmatch/services/api/internal/limits"
	"github.com/mmtmn/docmatch/services/api/internal/rules"
	"github.com/mmtmn/docmatch/services/api/internal/runs"
	"github.com/mmtmn/docmatch/services/api/internal/runs/apix"
	"github.com/mmtmn/docmatch/services/api/internal/runs/pgstore"
	"github.com/mmtmn/docmatch/services/api/internal/sse"
	"github.com/mmtmn/docmatch/services/api/internal/storage"
	"github.com/mmtmn/docmatch/services/api/internal/uploads"
	"github.com/mmtmn/docmatch/services/api/internal/workflows"
)

// Options configure New.
type Options struct {
	Config *config.Config
	Logger *slog.Logger
	Pool   *pgxpool.Pool
	// Auth identifies callers; nil refuses every request (no auth wired).
	Auth   apix.Resolver
	Limits *limits.Limits
}

// System is the assembled pipeline.
type System struct {
	Store   *pgstore.Store
	Stores  storage.Stores
	Local   *storage.LocalStore
	Harness harness.Client
	Pricing *pricing.Pricing
	// Throughput times quotes; nil when no throughput.yaml was found.
	Throughput *throughput.Config
	Audit      *audit.Service
	Credits    *credits.Service
	Broker     *sse.Broker
	Jobs       *jobargs.LazyEnqueuer
	Uploads    *uploads.Service
	Estimates  *estimate.Service
	Rules      *rules.Service
	Workflows  *workflows.Service
	Runs       *runs.Service

	cfg  *config.Config
	log  *slog.Logger
	pool *pgxpool.Pool
	auth apix.Resolver
}

// New builds every service. It does not start goroutines.
func New(ctx context.Context, o Options) (*System, error) {
	if o.Config == nil || o.Pool == nil {
		return nil, errors.New("wire: config e pool são obrigatórios")
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Limits == nil {
		o.Limits = limits.Must()
	}
	if o.Auth == nil {
		o.Auth = apix.Unauthenticated()
	}
	cfg, log := o.Config, o.Logger
	sys := &System{cfg: cfg, log: log, pool: o.Pool, auth: o.Auth, Broker: sse.NewBroker(), Jobs: &jobargs.LazyEnqueuer{}}
	sys.Store = pgstore.New(o.Pool)
	sys.Audit = audit.New(dbpgstore.NewAuditStore(o.Pool))
	sys.Credits = credits.New(dbpgstore.NewCreditStore(o.Pool))

	pr, err := pricing.Load(resolveFile(cfg.Billing.PricingFile), pricing.Overrides{
		TargetMargin: cfg.Billing.TargetMargin, IncomeTax: cfg.Billing.IncomeTaxRate, Payment: cfg.Billing.PaymentRate,
		Infra: cfg.Billing.InfraRate, OpenRouterFee: cfg.Billing.OpenRouterFee,
	})
	if err != nil {
		return nil, err
	}
	sys.Pricing = pr
	if sys.Throughput, err = loadThroughput(cfg, log); err != nil {
		return nil, err
	}
	signer, err := estimate.NewSigner(cfg.Signing.QuoteHMACKey.Reveal())
	if err != nil {
		return nil, err
	}
	if err := sys.buildStores(ctx); err != nil {
		return nil, err
	}
	sys.Harness, err = harness.New(cfg.Harness.Socket)
	if err != nil {
		return nil, err
	}
	if cfg.Harness.Socket != "" {
		// The harness reads only local files; remote sources are copied for each call.
		sys.Harness = &harness.Staged{Client: sys.Harness, Root: cfg.Storage.MediaLocalDir, Open: sys.openRemote}
	}
	if cfg.Harness.Socket == "" {
		log.Warn("DM_HARNESS_SOCKET vazio: estimativas e execuções respondem harness_unavailable (rode o fakeharness ou o harness)")
	}
	if err := estimate.EnsureModelPrices(ctx, sys.Store); err != nil {
		log.Warn("semear preços de modelo falhou; o estimador usa a tabela estática", slog.String("err", err.Error()))
	}

	var scanner uploads.ScanRunner = uploads.QueueScanner{}
	if cfg.Scan.QueueURL == "" {
		scanner = uploads.NewLocalScanner(cfg.Scan.ScannerBin, o.Limits, log)
	}
	sys.Uploads, err = uploads.New(uploads.Deps{Store: sys.Store, Stores: sys.Stores, Limits: o.Limits, Pricing: pr, Scanner: scanner, Jobs: sys.Jobs, Audit: sys.Audit, Logger: log})
	if err != nil {
		return nil, err
	}
	sys.Estimates, err = estimate.New(estimate.Deps{Store: sys.Store, Harness: sys.Harness, Pricing: pr, Throughput: sys.Throughput, Signer: signer, Sources: sys.Uploads, Limits: o.Limits, Audit: sys.Audit, Logger: log})
	if err != nil {
		return nil, err
	}
	sys.Rules, err = rules.New(rules.Deps{Store: sys.Store, Idem: sys.Store, Limits: o.Limits, Audit: sys.Audit, Logger: log})
	if err != nil {
		return nil, err
	}
	sys.Workflows, err = workflows.New(workflows.Deps{Store: sys.Store, Rules: sys.Rules, Limits: o.Limits, Audit: sys.Audit, Logger: log})
	if err != nil {
		return nil, err
	}
	// The end-of-run e-mail goes through the e-mail outbox in the transaction
	// that finishes the run; without a sender configured it waits there.
	mailInserter, err := emailpg.NewInserter(o.Pool)
	if err != nil {
		return nil, err
	}
	sys.Runs, err = runs.New(runs.Deps{
		Store: sys.Store, Uploads: sys.Uploads, Estimates: sys.Estimates, Rules: sys.Rules, Credits: sys.Credits, Jobs: sys.Jobs,
		Harness: sys.Harness, Broker: sys.Broker, Idem: sys.Store, Limits: o.Limits, Audit: sys.Audit, Outbox: sys.Store,
		Mailer: emailpg.NewTxMailer(mailInserter), ResultsURL: ResultsURL(cfg.PublicBaseURL), Logger: log,
	})
	if err != nil {
		return nil, err
	}
	return sys, nil
}

// buildStores picks S3/R2 or the local filesystem from the configuration.
func (s *System) buildStores(ctx context.Context) error {
	cfg := s.cfg
	if cfg.Storage.S3UploadsBucket != "" {
		docs, err := storage.NewS3(ctx, storage.S3Options{
			Name: storage.NameS3, Bucket: cfg.Storage.S3UploadsBucket, Region: cfg.Storage.AWSRegion, Endpoint: cfg.Storage.S3Endpoint,
			PathStyle: cfg.Storage.S3Endpoint != nil, AccessKeyID: cfg.Storage.AWSAccessKeyID, SecretAccessKey: cfg.Storage.AWSSecretKey.Reveal(),
		})
		if err != nil {
			return err
		}
		s.Stores.Documents = docs
	}
	if cfg.Storage.R2Configured() {
		endpoint := cfg.Storage.R2Endpoint
		if endpoint == nil {
			u, err := url.Parse("https://" + cfg.Storage.R2AccountID + ".r2.cloudflarestorage.com")
			if err != nil {
				return fmt.Errorf("wire: endpoint do R2: %w", err)
			}
			endpoint = u
		}
		media, err := storage.NewS3(ctx, storage.S3Options{
			Name: storage.NameR2, Bucket: cfg.Storage.R2MediaBucket, Region: "auto", Endpoint: endpoint,
			AccessKeyID: cfg.Storage.R2AccessKeyID, SecretAccessKey: cfg.Storage.R2SecretKey.Reveal(),
		})
		if err != nil {
			return err
		}
		s.Stores.Media = media
	}
	if s.Stores.Documents == nil || s.Stores.Media == nil {
		if cfg.APIBaseURL == nil {
			return errors.New("wire: DM_API_BASE_URL é necessária para o armazenamento local")
		}
		local, err := storage.NewLocal(cfg.Storage.MediaLocalDir, cfg.Storage.R2MediaBucket, cfg.APIBaseURL)
		if err != nil {
			return err
		}
		s.Local = local
		if s.Stores.Documents == nil {
			s.log.Warn("DM_S3_UPLOADS_BUCKET vazio: documentos ficam na pasta local", slog.String("dir", local.Root()))
			s.Stores.Documents = local
		}
		if s.Stores.Media == nil {
			s.log.Info("R2 não configurado: mídia fica na pasta local", slog.String("dir", local.Root()))
			s.Stores.Media = local
		}
	}
	return nil
}

// openRemote opens an s3:// or r2:// source of one of the configured stores
// for the harness; ok is false for local:// and unknown URIs.
func (s *System) openRemote(ctx context.Context, uri string) (io.ReadCloser, bool, error) {
	scheme, rest, ok := strings.Cut(uri, "://")
	if !ok || scheme == storage.NameLocal {
		return nil, false, nil
	}
	bucket, key, ok := strings.Cut(rest, "/")
	if !ok || key == "" {
		return nil, false, nil
	}
	for _, st := range []storage.Store{s.Stores.Documents, s.Stores.Media} {
		if st != nil && st.Name() == scheme && st.Bucket() == bucket {
			rc, err := st.Open(ctx, key)
			return rc, true, err
		}
	}
	return nil, false, nil
}

// Mount returns the route groups for api.Deps.Mount.
func (s *System) Mount() []func(r *httpx.Router) {
	uri := func(u uploads.Upload) string { return estimate.SourceURI(u, s.Uploads.StoreFor(u).URI) }
	groups := []func(r *httpx.Router){
		uploads.NewHandler(s.Uploads, s.auth).Mount,
		estimate.NewHandler(s.Estimates, s.auth, uri).Mount,
		runs.NewHandler(s.Runs, s.auth).Mount,
		workflows.NewHandler(s.Workflows, s.auth).Mount,
		rules.NewHandler(s.Rules, s.auth).Mount,
	}
	if s.Local != nil {
		local := s.Local
		groups = append(groups, func(r *httpx.Router) {
			r.Handle(http.MethodPut, storage.LocalPath+"{key...}", httpx.BodyMedia, local)
			r.Handle(http.MethodPost, storage.LocalPath+"{key...}", httpx.BodyDocument, local)
			r.Handle(http.MethodGet, storage.LocalPath+"{key...}", httpx.BodyNone, local)
		})
	}
	return groups
}

// Pipeline returns the worker-side collaborators.
func (s *System) Pipeline() *jobs.Pipeline {
	return &jobs.Pipeline{Uploads: s.Uploads, Runs: s.Runs, Outbox: s.Store, Deliverer: jobs.LogDeliverer{Logger: s.log}}
}

// ListenEvents forwards run_events notifications to the broker until ctx ends.
func (s *System) ListenEvents(ctx context.Context) error {
	l := &sse.Listener{Pool: s.pool, Broker: s.Broker, Logger: s.log}
	return l.Run(ctx)
}

// ScanConsumer returns the SQS verdict consumer when DM_SCAN_QUEUE_URL is set.
func (s *System) ScanConsumer(ctx context.Context) (func(context.Context) error, bool, error) {
	if s.cfg.Scan.QueueURL == "" {
		return nil, false, nil
	}
	c, err := uploads.NewSQSConsumer(ctx, s.cfg.Storage.AWSRegion, s.cfg.Scan.QueueURL, s.Uploads, s.log)
	if err != nil {
		return nil, false, err
	}
	return c.Run, true, nil
}

// Close releases the harness connection.
func (s *System) Close() {
	if s.Harness != nil {
		_ = s.Harness.Close()
	}
}

// ResultsPath is the web page with the results of a run (apps/web/src/pages/resultados).
const ResultsPath = "/resultados"

// ResultsURL renders DM_PUBLIC_BASE_URL + ResultsPath?run=<id>; nil without a
// base URL (the end-of-run e-mail then stays off).
func ResultsURL(base *url.URL) func(runID string) string {
	if base == nil {
		return nil
	}
	return func(runID string) string {
		u := *base
		u.Path = strings.TrimRight(u.Path, "/") + ResultsPath
		u.RawPath, u.Fragment = "", ""
		u.RawQuery = url.Values{"run": {runID}}.Encode()
		return u.String()
	}
}

// loadThroughput reads DM_THROUGHPUT_FILE, which must load when set. Unset,
// it looks for throughput.yaml next to the pricing table (the release
// installs both there); a missing file only turns the timing of quotes off.
func loadThroughput(cfg *config.Config, log *slog.Logger) (*throughput.Config, error) {
	if path := cfg.Billing.ThroughputFile; path != "" {
		return throughput.Load(resolveFile(path))
	}
	path := filepath.Join(filepath.Dir(resolveFile(cfg.Billing.PricingFile)), config.ThroughputFileName)
	tp, err := throughput.Load(path)
	if errors.Is(err, fs.ErrNotExist) {
		log.Warn("throughput.yaml ausente: orçamentos sem tempo esperado nem e-mail de execução longa", slog.String("path", path))
		return nil, nil
	}
	return tp, err
}

// resolveFile finds a repo-relative file (config/pricing.yaml) from the
// working directory or one of its parents, so `make run-api` inside
// services/api works as well as a binary started at the repo root.
func resolveFile(path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	dir, err := os.Getwd()
	if err != nil {
		return path
	}
	for i := 0; i < 8; i++ { // deep enough from any package of the module to the repo root
		candidate := filepath.Join(dir, path)
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return path
}
