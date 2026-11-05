// Package jobs defines the background jobs of the worker (River) and wires
// the River client: job argument types, their workers and the periodic
// schedule. Phase 1 ships the operational jobs; reconciliation jobs arrive
// with the harness integration (plano, fase do harness).
package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivertype"

	"github.com/mmtmn/docmatch/services/api/internal/audit"
	"github.com/mmtmn/docmatch/services/api/internal/credits"
	"github.com/mmtmn/docmatch/services/api/internal/db"
	"github.com/mmtmn/docmatch/services/api/internal/jobs/jobargs"
	"github.com/mmtmn/docmatch/services/api/internal/version"
)

// Job kinds. Never rename a kind: queued rows reference it.
const (
	KindHeartbeat            = "heartbeat"
	KindPartitionMaintenance = "partition_maintenance"
	KindExpireGrants         = "expire_grants"
)

// Schedule constants.
const (
	HeartbeatInterval    = time.Minute
	PartitionInterval    = 24 * time.Hour
	ExpireGrantsInterval = time.Hour
	// PartitionMonthsAhead is how many future months the daily job keeps ready.
	PartitionMonthsAhead = 2
	// DefaultMaxWorkers bounds concurrent jobs on the default queue.
	DefaultMaxWorkers = 10
)

// Pinger answers the heartbeat's liveness probe (pgxpool.Pool satisfies it).
type Pinger interface {
	Ping(ctx context.Context) error
}

// Partitioner creates the monthly partitions (db.EnsureMonthPartitions).
type Partitioner func(ctx context.Context, monthsAhead, monthsBack int) ([]db.Partition, error)

// PoolPartitioner adapts a pool or connection to Partitioner.
func PoolPartitioner(q db.Querier) Partitioner {
	return func(ctx context.Context, monthsAhead, monthsBack int) ([]db.Partition, error) {
		return db.EnsureMonthPartitions(ctx, q, monthsAhead, monthsBack)
	}
}

// ExpiredGrantLister finds organizations with grants past expiry.
type ExpiredGrantLister func(ctx context.Context, now time.Time) ([]uuid.UUID, error)

// GrantExpirer retires expired grants of one organization (credits.Service).
type GrantExpirer interface {
	ExpireGrants(ctx context.Context, orgID uuid.UUID, now time.Time) (credits.Expiry, error)
}

// Recorder appends system audit entries (audit.Service); nil disables it.
type Recorder interface {
	Record(ctx context.Context, orgID *uuid.UUID, actor audit.Actor, action string, target audit.Target, meta any) (audit.Entry, error)
}

// Deps are the collaborators shared by the workers.
type Deps struct {
	Logger      *slog.Logger
	DB          Pinger
	Partitioner Partitioner
	Lister      ExpiredGrantLister
	Expirer     GrantExpirer
	Audit       Recorder
	Hostname    string
	Now         func() time.Time
	// Pipeline wires the upload, run, learning and outbox jobs (nil in
	// processes that only insert them).
	Pipeline *Pipeline
}

func (d *Deps) defaults() {
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	if d.Hostname == "" {
		d.Hostname, _ = os.Hostname()
	}
	if d.Now == nil {
		d.Now = time.Now
	}
}

// systemActor identifies the worker in the audit trail.
func (d *Deps) systemActor() audit.Actor {
	return audit.Actor{Type: audit.ActorSystem, ID: "worker@" + d.Hostname}
}

// ---------------------------------------------------------------- heartbeat

// HeartbeatArgs carries nothing; the job proves the worker and its database
// connection are alive.
type HeartbeatArgs struct{}

// Kind implements river.JobArgs.
func (HeartbeatArgs) Kind() string { return KindHeartbeat }

// InsertOpts dedupes heartbeats inside one interval.
func (HeartbeatArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{MaxAttempts: 3, UniqueOpts: river.UniqueOpts{ByPeriod: HeartbeatInterval}}
}

// HeartbeatWorker logs one line per run and fails when the database does not answer.
type HeartbeatWorker struct {
	river.WorkerDefaults[HeartbeatArgs]
	deps    Deps
	started time.Time
}

// Timeout bounds a heartbeat.
func (w *HeartbeatWorker) Timeout(*river.Job[HeartbeatArgs]) time.Duration { return 30 * time.Second }

// Work implements river.Worker.
func (w *HeartbeatWorker) Work(ctx context.Context, job *river.Job[HeartbeatArgs]) error {
	if w.deps.DB != nil {
		if err := w.deps.DB.Ping(ctx); err != nil {
			return fmt.Errorf("heartbeat: banco não responde: %w", err)
		}
	}
	w.deps.Logger.Info("batimento do worker",
		slog.String("host", w.deps.Hostname),
		slog.String("version", version.GitSHA),
		slog.Duration("uptime", w.deps.Now().Sub(w.started)),
		slog.Int64("job_id", jobID(job.JobRow)))
	return nil
}

// jobID reads the id of a job row, tolerating jobs built without one (tests).
func jobID(row *rivertype.JobRow) int64 {
	if row == nil {
		return 0
	}
	return row.ID
}

// --------------------------------------------------------- partition upkeep

// PartitionMaintenanceArgs selects how many months to prepare.
type PartitionMaintenanceArgs struct {
	MonthsAhead int `json:"months_ahead"`
	MonthsBack  int `json:"months_back"`
}

// Kind implements river.JobArgs.
func (PartitionMaintenanceArgs) Kind() string { return KindPartitionMaintenance }

// InsertOpts dedupes concurrent requests.
func (PartitionMaintenanceArgs) InsertOpts() river.InsertOpts {
	// Unique by args across River's default non-terminal states.
	return river.InsertOpts{MaxAttempts: 5, UniqueOpts: river.UniqueOpts{ByArgs: true}}
}

// PartitionWorker calls ensure_month_partitions and records what it created.
type PartitionWorker struct {
	river.WorkerDefaults[PartitionMaintenanceArgs]
	deps Deps
}

// Timeout bounds the DDL.
func (w *PartitionWorker) Timeout(*river.Job[PartitionMaintenanceArgs]) time.Duration {
	return 2 * time.Minute
}

// Work implements river.Worker.
func (w *PartitionWorker) Work(ctx context.Context, job *river.Job[PartitionMaintenanceArgs]) error {
	if w.deps.Partitioner == nil {
		return errors.New("partition_maintenance: sem acesso ao banco")
	}
	ahead := job.Args.MonthsAhead
	if ahead <= 0 {
		ahead = PartitionMonthsAhead
	}
	parts, err := w.deps.Partitioner(ctx, ahead, max(job.Args.MonthsBack, 0))
	if err != nil {
		return fmt.Errorf("partition_maintenance: %w", err)
	}
	var created []string
	for _, p := range parts {
		if p.Created {
			created = append(created, p.Name)
		}
	}
	w.deps.Logger.Info("partições mensais verificadas",
		slog.Int("checked", len(parts)), slog.Int("created", len(created)), slog.Any("names", created))
	if w.deps.Audit != nil && len(created) > 0 {
		if _, err := w.deps.Audit.Record(ctx, nil, w.deps.systemActor(), "partitions.ensure", audit.Target{Type: "database"}, map[string]any{"created": created}); err != nil {
			return fmt.Errorf("partition_maintenance: auditoria: %w", err)
		}
	}
	return nil
}

// ------------------------------------------------------------ grant expiry

// ExpireGrantsArgs carries nothing; the job sweeps every organization.
type ExpireGrantsArgs struct{}

// Kind implements river.JobArgs.
func (ExpireGrantsArgs) Kind() string { return KindExpireGrants }

// InsertOpts dedupes sweeps inside one interval.
func (ExpireGrantsArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{MaxAttempts: 5, UniqueOpts: river.UniqueOpts{ByPeriod: ExpireGrantsInterval}}
}

// ExpireGrantsWorker retires expired credits organization by organization.
type ExpireGrantsWorker struct {
	river.WorkerDefaults[ExpireGrantsArgs]
	deps Deps
}

// Timeout bounds a sweep.
func (w *ExpireGrantsWorker) Timeout(*river.Job[ExpireGrantsArgs]) time.Duration {
	return 10 * time.Minute
}

// Work implements river.Worker. One failing organization does not stop the
// others; the job fails at the end so River retries it.
func (w *ExpireGrantsWorker) Work(ctx context.Context, _ *river.Job[ExpireGrantsArgs]) error {
	if w.deps.Lister == nil || w.deps.Expirer == nil {
		return errors.New("expire_grants: sem acesso aos tokens")
	}
	now := w.deps.Now().UTC()
	orgs, err := w.deps.Lister(ctx, now)
	if err != nil {
		return fmt.Errorf("expire_grants: listar: %w", err)
	}
	var failed []error
	var expired int64
	for _, org := range orgs {
		exp, err := w.deps.Expirer.ExpireGrants(ctx, org, now)
		if err != nil {
			failed = append(failed, fmt.Errorf("org %s: %w", org, err))
			continue
		}
		expired += exp.Expired
		if w.deps.Audit != nil && exp.Expired > 0 {
			orgID := org
			if _, err := w.deps.Audit.Record(ctx, &orgID, w.deps.systemActor(), "credits.expire", audit.Target{Type: "credit_account", ID: org.String()},
				map[string]any{"expired": exp.Expired, "grants": exp.Grants, "deferred": exp.Deferred}); err != nil {
				failed = append(failed, fmt.Errorf("org %s: auditoria: %w", org, err))
			}
		}
	}
	w.deps.Logger.Info("concessões vencidas processadas", slog.Int("orgs", len(orgs)), slog.Int64("expired", expired), slog.Int("failed", len(failed)))
	if len(failed) > 0 {
		return fmt.Errorf("expire_grants: %d organizações falharam: %w", len(failed), errors.Join(failed...))
	}
	return nil
}

// ----------------------------------------------------------------- wiring

// NewWorkers registers every worker with its dependencies.
func NewWorkers(d Deps) *river.Workers {
	d.defaults()
	workers := river.NewWorkers()
	river.AddWorker(workers, &HeartbeatWorker{deps: d, started: d.Now()})
	river.AddWorker(workers, &PartitionWorker{deps: d})
	river.AddWorker(workers, &ExpireGrantsWorker{deps: d})
	river.AddWorker(workers, &ScanWorker{deps: d})
	river.AddWorker(workers, &RunWorker{deps: d})
	river.AddWorker(workers, &LearnWorker{deps: d})
	river.AddWorker(workers, &OutboxWorker{deps: d})
	return workers
}

// PeriodicJobs returns the schedule: heartbeat every minute, partition
// upkeep daily and grant expiry hourly, each also run at start-up.
func PeriodicJobs() []*river.PeriodicJob {
	return []*river.PeriodicJob{
		river.NewPeriodicJob(river.PeriodicInterval(HeartbeatInterval),
			func() (river.JobArgs, *river.InsertOpts) { return HeartbeatArgs{}, nil },
			&river.PeriodicJobOpts{ID: KindHeartbeat, RunOnStart: true}),
		river.NewPeriodicJob(river.PeriodicInterval(PartitionInterval),
			func() (river.JobArgs, *river.InsertOpts) {
				return PartitionMaintenanceArgs{MonthsAhead: PartitionMonthsAhead}, nil
			},
			&river.PeriodicJobOpts{ID: KindPartitionMaintenance, RunOnStart: true}),
		river.NewPeriodicJob(river.PeriodicInterval(ExpireGrantsInterval),
			func() (river.JobArgs, *river.InsertOpts) { return ExpireGrantsArgs{}, nil },
			&river.PeriodicJobOpts{ID: KindExpireGrants, RunOnStart: true}),
		river.NewPeriodicJob(river.PeriodicInterval(OutboxInterval),
			func() (river.JobArgs, *river.InsertOpts) { return jobargs.OutboxArgs{}, nil },
			&river.PeriodicJobOpts{ID: jobargs.KindOutbox, RunOnStart: true}),
	}
}

// ClientOptions tune NewClient.
type ClientOptions struct {
	// MaxWorkers bounds concurrency on the default queue (DefaultMaxWorkers).
	MaxWorkers int
	// Periodic enables the periodic schedule (true for the worker, false
	// for the API, which only inserts jobs).
	Periodic bool
	// ID names the client in River's leader election (default hostname).
	ID string
	// Register adds the workers of other packages (e-mail outbox, accounts).
	Register []func(*river.Workers)
	// ExtraPeriodic adds periodic jobs of other packages (with Periodic).
	ExtraPeriodic []*river.PeriodicJob
}

// NewClient builds a River client bound to pool. It does not start it.
func NewClient(pool *pgxpool.Pool, d Deps, opts ClientOptions) (*river.Client[pgx.Tx], error) {
	if pool == nil {
		return nil, errors.New("jobs: pool nulo")
	}
	d.defaults()
	maxWorkers := opts.MaxWorkers
	if maxWorkers <= 0 {
		maxWorkers = DefaultMaxWorkers
	}
	workers := NewWorkers(d)
	for _, register := range opts.Register {
		register(workers)
	}
	cfg := &river.Config{
		ID:                  opts.ID,
		Logger:              d.Logger,
		JobTimeout:          10 * time.Minute,
		FetchOnlyKnownKinds: true,
		Queues:              map[string]river.QueueConfig{river.QueueDefault: {MaxWorkers: maxWorkers}},
		Workers:             workers,
	}
	if opts.Periodic {
		cfg.PeriodicJobs = append(PeriodicJobs(), opts.ExtraPeriodic...)
	}
	client, err := river.NewClient(riverpgxv5.New(pool), cfg)
	if err != nil {
		return nil, fmt.Errorf("jobs: criar cliente River: %w", err)
	}
	return client, nil
}
