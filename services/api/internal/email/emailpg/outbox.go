// Package emailpg delivers the e-mail outbox with River: Enqueue writes the
// outbox row and the immediate send job in the caller's transaction, the
// send worker delivers one row through the configured Sender, and the sweep
// job relays rows whose immediate job was lost. Every path checks sent_at
// before sending and passes the outbox id as the idempotency key.
package emailpg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"github.com/mmtmn/docmatch/services/api/internal/db"
	"github.com/mmtmn/docmatch/services/api/internal/db/sqlc"
	"github.com/mmtmn/docmatch/services/api/internal/email"
)

// Client is the River client type bound to pgx.
type Client = river.Client[pgx.Tx]

// NewInserter returns an insert-only River client for the API process: it
// can insert jobs (inside transactions too) but never works them.
func NewInserter(pool *pgxpool.Pool) (*Client, error) {
	if pool == nil {
		return nil, errors.New("emailpg: pool nulo")
	}
	return river.NewClient(riverpgxv5.New(pool), &river.Config{})
}

// Enqueue writes msg to the outbox inside tx and inserts the send job in the
// same transaction. client may be nil: the sweep job delivers the row later.
func Enqueue(ctx context.Context, tx pgx.Tx, client *Client, orgID *uuid.UUID, msg email.Message) (uuid.UUID, error) {
	if err := msg.Validate(); err != nil {
		return uuid.Nil, err
	}
	payload, err := json.Marshal(msg)
	if err != nil {
		return uuid.Nil, err
	}
	row, err := sqlc.New(tx).EnqueueOutbox(ctx, sqlc.EnqueueOutboxParams{OrgID: orgID, Kind: email.OutboxKind, Payload: payload})
	if err != nil {
		return uuid.Nil, fmt.Errorf("emailpg: outbox: %w", err)
	}
	if client != nil {
		if _, err := client.InsertTx(ctx, tx, email.SendArgs{OutboxID: row.ID}, nil); err != nil {
			return uuid.Nil, fmt.Errorf("emailpg: job: %w", err)
		}
	}
	return row.ID, nil
}

// Deliverer sends outbox rows and records the outcome.
type Deliverer struct {
	pool   *pgxpool.Pool
	sender email.Sender
	log    *slog.Logger
	now    func() time.Time
}

// NewDeliverer wires the pool and the sender (nil sender: every delivery
// fails with email.ErrNotConfigured and stays in the outbox).
func NewDeliverer(pool *pgxpool.Pool, sender email.Sender, log *slog.Logger) *Deliverer {
	if log == nil {
		log = slog.Default()
	}
	return &Deliverer{pool: pool, sender: sender, log: log, now: time.Now}
}

// deliver sends one row (already loaded) and marks it; it returns the send
// error so the caller can retry.
func (d *Deliverer) deliver(ctx context.Context, row sqlc.Outbox) error {
	if row.SentAt != nil {
		return nil
	}
	var msg email.Message
	if err := json.Unmarshal(row.Payload, &msg); err != nil {
		return d.retryLater(ctx, row, fmt.Errorf("payload inválido: %w", err))
	}
	if d.sender == nil {
		return d.retryLater(ctx, row, email.ErrNotConfigured)
	}
	if err := d.sender.Send(ctx, row.ID.String(), msg); err != nil {
		return d.retryLater(ctx, row, err)
	}
	return db.WithSystemTx(ctx, d.pool, func(ctx context.Context, tx pgx.Tx) error {
		return sqlc.New(tx).MarkOutboxSent(ctx, row.ID)
	})
}

func (d *Deliverer) retryLater(ctx context.Context, row sqlc.Outbox, cause error) error {
	attempt := int(row.Attempts)
	if attempt < 1 {
		attempt = 1
	}
	wait := email.Backoff(attempt)
	if attempt >= email.MaxDeliveryTries {
		wait = 24 * time.Hour
		d.log.Error("e-mail desistido após muitas tentativas", slog.String("outbox_id", row.ID.String()), slog.String("err", cause.Error()))
	}
	reason := cause.Error()
	if err := db.WithSystemTx(ctx, d.pool, func(ctx context.Context, tx pgx.Tx) error {
		return sqlc.New(tx).RetryOutboxLater(ctx, sqlc.RetryOutboxLaterParams{ID: row.ID, AvailableAt: d.now().Add(wait), LastError: &reason})
	}); err != nil {
		return errors.Join(cause, err)
	}
	return cause
}

// SendOne loads the row, bumps its attempt counter and delivers it.
func (d *Deliverer) SendOne(ctx context.Context, id uuid.UUID) error {
	var row sqlc.Outbox
	var found bool
	err := db.WithSystemTx(ctx, d.pool, func(ctx context.Context, tx pgx.Tx) error {
		r, err := sqlc.New(tx).AuthMarkOutboxAttempt(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // already sent or unknown: nothing to do
		}
		if err != nil {
			return err
		}
		row, found = r, true
		return nil
	})
	if err != nil || !found {
		return err
	}
	return d.deliver(ctx, row)
}

// Sweep delivers rows that have waited at least minAge without being sent.
func (d *Deliverer) Sweep(ctx context.Context, minAge time.Duration, batch int) (sent, failed int, err error) {
	var rows []sqlc.Outbox
	err = db.WithSystemTx(ctx, d.pool, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		rows, err = sqlc.New(tx).AuthClaimStaleOutbox(ctx, sqlc.AuthClaimStaleOutboxParams{
			Kind: email.OutboxKind, AvailableAt: d.now().Add(-minAge), Limit: int32(batch), //nolint:gosec // small batch
		})
		return err
	})
	if err != nil {
		return 0, 0, err
	}
	for _, row := range rows {
		if err := d.deliver(ctx, row); err != nil {
			failed++
			continue
		}
		sent++
	}
	return sent, failed, nil
}

// SendWorker works email.SendArgs.
type SendWorker struct {
	river.WorkerDefaults[email.SendArgs]
	d *Deliverer
}

// Timeout bounds one delivery.
func (w *SendWorker) Timeout(*river.Job[email.SendArgs]) time.Duration {
	return 2 * email.DeliveryTimeout
}

// Work implements river.Worker.
func (w *SendWorker) Work(ctx context.Context, job *river.Job[email.SendArgs]) error {
	return w.d.SendOne(ctx, job.Args.OutboxID)
}

// SweepWorker works email.SweepArgs.
type SweepWorker struct {
	river.WorkerDefaults[email.SweepArgs]
	d *Deliverer
}

// Timeout bounds a sweep.
func (w *SweepWorker) Timeout(*river.Job[email.SweepArgs]) time.Duration { return 5 * time.Minute }

// Work implements river.Worker.
func (w *SweepWorker) Work(ctx context.Context, _ *river.Job[email.SweepArgs]) error {
	sent, failed, err := w.d.Sweep(ctx, email.SweepMinAge, email.SweepBatch)
	if err != nil {
		return fmt.Errorf("email_outbox_sweep: %w", err)
	}
	if sent+failed > 0 {
		w.d.log.Info("outbox de e-mail varrido", slog.Int("sent", sent), slog.Int("failed", failed))
	}
	return nil
}

// AddWorkers registers the e-mail workers (jobs.ClientOptions.Register).
func AddWorkers(d *Deliverer) func(*river.Workers) {
	return func(w *river.Workers) {
		river.AddWorker(w, &SendWorker{d: d})
		river.AddWorker(w, &SweepWorker{d: d})
	}
}

// PeriodicJobs returns the sweep schedule (jobs.ClientOptions.ExtraPeriodic).
func PeriodicJobs() []*river.PeriodicJob {
	return []*river.PeriodicJob{
		river.NewPeriodicJob(river.PeriodicInterval(email.SweepInterval),
			func() (river.JobArgs, *river.InsertOpts) { return email.SweepArgs{}, nil },
			&river.PeriodicJobOpts{ID: email.KindSweep, RunOnStart: true}),
	}
}
