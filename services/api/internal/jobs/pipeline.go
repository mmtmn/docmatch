package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/riverqueue/river"

	"github.com/mmtmn/docmatch/services/api/internal/jobs/jobargs"
	"github.com/mmtmn/docmatch/services/api/internal/runs"
	"github.com/mmtmn/docmatch/services/api/internal/runs/pgstore"
	"github.com/mmtmn/docmatch/services/api/internal/uploads"
)

// Pipeline timeouts and the outbox schedule.
const (
	ScanTimeout    = 10 * time.Minute
	LearnTimeout   = 10 * time.Minute
	RunTimeoutPad  = 15 * time.Minute
	OutboxInterval = 30 * time.Second
	OutboxBatch    = 50
	// OutboxMaxAttempts is when a message stops being retried (it stays
	// unsent with last_error for an operator to look at).
	OutboxMaxAttempts = 20
)

// OutboxStore claims and settles outbox rows (pgstore.Store).
type OutboxStore interface {
	ClaimOutbox(ctx context.Context, limit int, skip []string) ([]pgstore.OutboxMessage, error)
	MarkOutboxSent(ctx context.Context, id uuid.UUID) error
	RetryOutboxLater(ctx context.Context, id uuid.UUID, at time.Time, lastErr string) error
}

// Deliverer sends one outbox message to its destination (webhook endpoints,
// e-mail). The webhooks subsystem provides the real one; LogDeliverer is
// the default and only logs.
type Deliverer interface {
	Deliver(ctx context.Context, m pgstore.OutboxMessage) error
}

// LogDeliverer logs messages instead of delivering them.
type LogDeliverer struct {
	Logger *slog.Logger
}

// Deliver implements Deliverer.
func (d LogDeliverer) Deliver(_ context.Context, m pgstore.OutboxMessage) error {
	log := d.Logger
	if log == nil {
		log = slog.Default()
	}
	log.Info("mensagem do outbox sem entregador configurado; registrada apenas", slog.String("kind", m.Kind), slog.String("id", m.ID.String()))
	return nil
}

// Pipeline holds the collaborators of the pipeline jobs.
type Pipeline struct {
	Uploads   *uploads.Service
	Runs      *runs.Service
	Outbox    OutboxStore
	Deliverer Deliverer
	// RunDeadline sizes the run job timeout (runs.DefaultRunDeadline + pad).
	RunDeadline time.Duration
}

// ErrPipelineNotWired is returned by pipeline workers of a client without a Pipeline.
var ErrPipelineNotWired = errors.New("jobs: pipeline não configurado neste processo")

// ------------------------------------------------------------------- scan

// ScanWorker asks the scanner for a verdict and applies it.
type ScanWorker struct {
	river.WorkerDefaults[jobargs.ScanArgs]
	deps Deps
}

// Timeout bounds a scan.
func (w *ScanWorker) Timeout(*river.Job[jobargs.ScanArgs]) time.Duration { return ScanTimeout }

// Work implements river.Worker.
func (w *ScanWorker) Work(ctx context.Context, job *river.Job[jobargs.ScanArgs]) error {
	if w.deps.Pipeline == nil || w.deps.Pipeline.Uploads == nil {
		return ErrPipelineNotWired
	}
	if err := w.deps.Pipeline.Uploads.RunScan(ctx, job.Args.OrgID, job.Args.UploadID); err != nil {
		return fmt.Errorf("upload_scan: %w", err)
	}
	return nil
}

// -------------------------------------------------------------------- run

// RunWorker executes (or resumes) a run against the harness.
type RunWorker struct {
	river.WorkerDefaults[jobargs.RunArgs]
	deps Deps
}

// Timeout bounds a run: the harness deadline plus a margin for persistence.
func (w *RunWorker) Timeout(*river.Job[jobargs.RunArgs]) time.Duration {
	d := runs.DefaultRunDeadline
	if w.deps.Pipeline != nil && w.deps.Pipeline.RunDeadline > 0 {
		d = w.deps.Pipeline.RunDeadline
	}
	return d + RunTimeoutPad
}

// Work implements river.Worker. A transient harness failure returns an
// error so River retries; the last attempt fails the run instead.
func (w *RunWorker) Work(ctx context.Context, job *river.Job[jobargs.RunArgs]) error {
	if w.deps.Pipeline == nil || w.deps.Pipeline.Runs == nil {
		return ErrPipelineNotWired
	}
	lastAttempt := job.Attempt >= job.MaxAttempts
	if err := w.deps.Pipeline.Runs.Execute(ctx, job.Args.OrgID, job.Args.RunID, lastAttempt); err != nil {
		return fmt.Errorf("run_execute: %w", err)
	}
	return nil
}

// ------------------------------------------------------------------ learn

// LearnWorker turns feedback into proposed rules through the harness.
type LearnWorker struct {
	river.WorkerDefaults[jobargs.LearnArgs]
	deps Deps
}

// Timeout bounds a learning pass.
func (w *LearnWorker) Timeout(*river.Job[jobargs.LearnArgs]) time.Duration { return LearnTimeout }

// Work implements river.Worker.
func (w *LearnWorker) Work(ctx context.Context, job *river.Job[jobargs.LearnArgs]) error {
	if w.deps.Pipeline == nil || w.deps.Pipeline.Runs == nil {
		return ErrPipelineNotWired
	}
	if err := w.deps.Pipeline.Runs.Learn(ctx, job.Args.OrgID, job.Args.RunID); err != nil {
		return fmt.Errorf("rules_learn: %w", err)
	}
	return nil
}

// ----------------------------------------------------------------- outbox

// OutboxWorker drains the transactional outbox.
type OutboxWorker struct {
	river.WorkerDefaults[jobargs.OutboxArgs]
	deps Deps
}

// Timeout bounds a drain.
func (w *OutboxWorker) Timeout(*river.Job[jobargs.OutboxArgs]) time.Duration { return 2 * time.Minute }

// Work implements river.Worker: each claimed message is delivered and
// marked sent, or rescheduled with quadratic backoff.
func (w *OutboxWorker) Work(ctx context.Context, _ *river.Job[jobargs.OutboxArgs]) error {
	p := w.deps.Pipeline
	if p == nil || p.Outbox == nil {
		return nil // nothing to drain in this process
	}
	deliverer := p.Deliverer
	if deliverer == nil {
		deliverer = LogDeliverer{Logger: w.deps.Logger}
	}
	msgs, err := p.Outbox.ClaimOutbox(ctx, OutboxBatch, jobargs.DedicatedOutboxKinds)
	if err != nil {
		return fmt.Errorf("outbox_send: reivindicar: %w", err)
	}
	var failed int
	for _, m := range msgs {
		if m.Attempts > OutboxMaxAttempts {
			w.deps.Logger.Error("mensagem do outbox abandonada após muitas tentativas", slog.String("id", m.ID.String()), slog.String("kind", m.Kind))
			continue
		}
		if err := deliverer.Deliver(ctx, m); err != nil {
			failed++
			delay := time.Duration(m.Attempts*m.Attempts) * OutboxInterval
			if delay > time.Hour {
				delay = time.Hour
			}
			if rerr := p.Outbox.RetryOutboxLater(ctx, m.ID, w.deps.Now().Add(delay), err.Error()); rerr != nil {
				w.deps.Logger.Error("reagendar mensagem do outbox falhou", slog.String("id", m.ID.String()), slog.String("err", rerr.Error()))
			}
			continue
		}
		if err := p.Outbox.MarkOutboxSent(ctx, m.ID); err != nil {
			w.deps.Logger.Error("marcar mensagem do outbox como enviada falhou", slog.String("id", m.ID.String()), slog.String("err", err.Error()))
		}
	}
	if len(msgs) > 0 {
		w.deps.Logger.Info("outbox drenado", slog.Int("messages", len(msgs)), slog.Int("failed", failed))
	}
	return nil
}
