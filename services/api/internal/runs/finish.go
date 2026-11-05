package runs

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/mmtmn/docmatch/services/api/internal/email"
	"github.com/mmtmn/docmatch/services/api/internal/estimate"
)

// MailKeyPrefix prefixes the outbox dedupe key of the end-of-run e-mail: one
// e-mail per run, whatever retries happen.
const MailKeyPrefix = "run_finished:"

// attempt is one Execute call: when the harness sent its first event (the
// slot wait is queue, not work) and the active time of the earlier attempts.
type attempt struct {
	begin   time.Time
	priorMs int64
}

// touchActive writes the active time so far into the run's timing.
func (s *Service) touchActive(r *Run, a *attempt) {
	if a == nil || a.begin.IsZero() {
		return
	}
	t := r.Timing()
	t.ActiveMs = a.priorMs + max(s.d.Clock().Sub(a.begin).Milliseconds(), 0)
	r.SetTiming(t)
}

// finished reports whether the run ended in a way the person is told about
// (cancellation is their own doing).
func finished(r Run) bool {
	return r.FinishedAt != nil && (r.Status == StatusCompleted || r.Status == StatusFailed)
}

// shouldMail reports whether the end of r deserves an e-mail: its quote was
// large, or it took longer than notify_after_seconds from creation to end.
// API-key runs have no person to write to (their webhooks say the same).
func (s *Service) shouldMail(r Run) bool {
	if s.d.Mailer == nil || s.d.ResultsURL == nil || r.UserID == nil || !finished(r) {
		return false
	}
	if r.Timing().Large {
		return true
	}
	cfg := s.d.Estimates.Throughput()
	return cfg != nil && r.FinishedAt.Sub(r.CreatedAt) > time.Duration(cfg.NotifyAfterSeconds)*time.Second
}

// finishedHook is the TxHook of the save that ends r: inside that
// transaction it enqueues the e-mail to the person who started the run, keyed
// by the run so it goes out once. A savepoint isolates it, so a failure to
// write the e-mail is logged and never keeps the run from finishing.
func (s *Service) finishedHook(r Run) TxHook {
	if !s.shouldMail(r) {
		return nil
	}
	return func(ctx context.Context, tx pgx.Tx) error {
		if tx == nil {
			s.logMailErr(r, s.mailFinished(ctx, nil, r))
			return nil
		}
		sp, err := tx.Begin(ctx)
		if err != nil {
			s.logMailErr(r, err)
			return nil
		}
		if err := s.mailFinished(ctx, sp, r); err != nil {
			_ = sp.Rollback(ctx)
			s.logMailErr(r, err)
			return nil
		}
		return sp.Commit(ctx)
	}
}

func (s *Service) mailFinished(ctx context.Context, tx pgx.Tx, r Run) error {
	to, err := s.d.Mailer.UserEmail(ctx, tx, *r.UserID)
	if err != nil || to == "" {
		return err
	}
	msg := email.RunFinishedMessage(to, email.RunFinishedData{Link: s.d.ResultsURL(r.PublicID()), Completed: r.Status == StatusCompleted, StartedAt: r.CreatedAt})
	if err := msg.Validate(); err != nil {
		return err
	}
	return s.d.Mailer.EnqueueOnce(ctx, tx, r.OrgID, MailKeyPrefix+r.PublicID(), msg)
}

func (s *Service) logMailErr(r Run, err error) {
	if err == nil {
		return
	}
	level := slog.LevelWarn
	if errors.Is(err, email.ErrInvalidTo) {
		level = slog.LevelInfo
	}
	s.d.Logger.Log(context.Background(), level, "e-mail de fim de execução não enfileirado", slog.String("run_id", r.PublicID()), slog.String("err", err.Error()))
}

// recordTiming adds a finished run to the history the estimator learns from.
func (s *Service) recordTiming(ctx context.Context, r Run) {
	if !finished(r) {
		return
	}
	status := estimate.TimingCompleted
	if r.Status == StatusFailed {
		status = estimate.TimingFailed
	}
	t := r.Timing()
	err := s.d.Estimates.RecordTiming(ctx, estimate.RunTiming{
		RunID: r.ID, OrgID: r.OrgID, Status: status, Units: t.Units, ActiveSeconds: float64(t.ActiveMs) / 1000,
		ExpectedSeconds: t.ExpectedSeconds, FinishedAt: *r.FinishedAt,
	})
	if err != nil {
		s.d.Logger.Warn("gravar duração da execução falhou", slog.String("run_id", r.PublicID()), slog.String("err", err.Error()))
	}
}
