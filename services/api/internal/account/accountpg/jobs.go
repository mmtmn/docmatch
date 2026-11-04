package accountpg

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/riverqueue/river"

	"github.com/mmtmn/docmatch/services/api/internal/account"
	"github.com/mmtmn/docmatch/services/api/internal/email/emailpg"
)

// Job kinds and schedule.
const (
	KindExport          = "account_export"
	KindErase           = "account_erase"
	KindMaintenance     = "account_maintenance"
	MaintenanceInterval = time.Hour
)

// ExportArgs builds one export.
type ExportArgs struct {
	ExportID uuid.UUID `json:"export_id"`
}

// Kind implements river.JobArgs.
func (ExportArgs) Kind() string { return KindExport }

// InsertOpts dedupes per export.
func (ExportArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{MaxAttempts: 5, UniqueOpts: river.UniqueOpts{ByArgs: true}}
}

// EraseArgs erases one account when its grace period ends.
type EraseArgs struct {
	UserID uuid.UUID `json:"user_id"`
}

// Kind implements river.JobArgs.
func (EraseArgs) Kind() string { return KindErase }

// InsertOpts dedupes per user.
func (EraseArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{MaxAttempts: 10, UniqueOpts: river.UniqueOpts{ByArgs: true}}
}

// MaintenanceArgs expires exports and catches up on due erasures.
type MaintenanceArgs struct{}

// Kind implements river.JobArgs.
func (MaintenanceArgs) Kind() string { return KindMaintenance }

// InsertOpts dedupes inside one interval.
func (MaintenanceArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{MaxAttempts: 3, UniqueOpts: river.UniqueOpts{ByPeriod: MaintenanceInterval}}
}

// Enqueuer implements account.Enqueuer with the insert-only River client.
type Enqueuer struct {
	client *emailpg.Client
}

// NewEnqueuer wraps the client (nil disables enqueueing).
func NewEnqueuer(client *emailpg.Client) *Enqueuer { return &Enqueuer{client: client} }

// EnqueueExport implements account.Enqueuer.
func (e *Enqueuer) EnqueueExport(ctx context.Context, exportID uuid.UUID) error {
	if e.client == nil {
		return fmt.Errorf("accountpg: river indisponível")
	}
	_, err := e.client.Insert(ctx, ExportArgs{ExportID: exportID}, nil)
	return err
}

// EnqueueErase implements account.Enqueuer.
func (e *Enqueuer) EnqueueErase(ctx context.Context, userID uuid.UUID, at time.Time) error {
	if e.client == nil {
		return fmt.Errorf("accountpg: river indisponível")
	}
	_, err := e.client.Insert(ctx, EraseArgs{UserID: userID}, &river.InsertOpts{ScheduledAt: at})
	return err
}

// ExportWorker works ExportArgs.
type ExportWorker struct {
	river.WorkerDefaults[ExportArgs]
	svc *account.Service
}

// Timeout bounds one export.
func (w *ExportWorker) Timeout(*river.Job[ExportArgs]) time.Duration { return 5 * time.Minute }

// Work implements river.Worker.
func (w *ExportWorker) Work(ctx context.Context, job *river.Job[ExportArgs]) error {
	return w.svc.RunExport(ctx, job.Args.ExportID)
}

// EraseWorker works EraseArgs.
type EraseWorker struct {
	river.WorkerDefaults[EraseArgs]
	svc *account.Service
}

// Timeout bounds one erasure.
func (w *EraseWorker) Timeout(*river.Job[EraseArgs]) time.Duration { return 5 * time.Minute }

// Work implements river.Worker.
func (w *EraseWorker) Work(ctx context.Context, job *river.Job[EraseArgs]) error {
	return w.svc.RunErase(ctx, job.Args.UserID)
}

// MaintenanceWorker works MaintenanceArgs.
type MaintenanceWorker struct {
	river.WorkerDefaults[MaintenanceArgs]
	svc *account.Service
}

// Timeout bounds a maintenance pass.
func (w *MaintenanceWorker) Timeout(*river.Job[MaintenanceArgs]) time.Duration {
	return 10 * time.Minute
}

// Work implements river.Worker.
func (w *MaintenanceWorker) Work(ctx context.Context, _ *river.Job[MaintenanceArgs]) error {
	return w.svc.RunMaintenance(ctx)
}

// AddWorkers registers the account workers (jobs.ClientOptions.Register).
func AddWorkers(svc *account.Service) func(*river.Workers) {
	return func(w *river.Workers) {
		river.AddWorker(w, &ExportWorker{svc: svc})
		river.AddWorker(w, &EraseWorker{svc: svc})
		river.AddWorker(w, &MaintenanceWorker{svc: svc})
	}
}

// PeriodicJobs returns the maintenance schedule (jobs.ClientOptions.ExtraPeriodic).
func PeriodicJobs() []*river.PeriodicJob {
	return []*river.PeriodicJob{
		river.NewPeriodicJob(river.PeriodicInterval(MaintenanceInterval),
			func() (river.JobArgs, *river.InsertOpts) { return MaintenanceArgs{}, nil },
			&river.PeriodicJobOpts{ID: KindMaintenance, RunOnStart: true}),
	}
}
