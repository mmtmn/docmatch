// Package burstpg is the Postgres store of the pool of on-demand workers
// (db/migrations/0022_burst_workers.up.sql). Every call runs in system mode:
// the pool serves every organization.
package burstpg

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/mmtmn/docmatch/services/api/internal/burst"
	"github.com/mmtmn/docmatch/services/api/internal/db"
	"github.com/mmtmn/docmatch/services/api/internal/db/sqlc"
)

// Store implements burst.Store.
type Store struct {
	pool db.Beginner
}

// New returns a store over pool.
func New(pool db.Beginner) *Store { return &Store{pool: pool} }

var _ burst.Store = (*Store)(nil)

func (s *Store) system(ctx context.Context, fn func(q *sqlc.Queries) error) error {
	return db.WithSystemTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		return fn(sqlc.New(tx))
	})
}

func fromRow(r sqlc.BurstJob) burst.Job {
	j := burst.Job{
		ID: r.ID, OrgID: r.OrgID, RunID: r.RunID, State: r.State, Request: r.Request, DeadlineSeconds: r.DeadlineSeconds,
		TokenHash: r.TokenHash, ClaimedAt: r.ClaimedAt, LeaseExpiresAt: r.LeaseExpiresAt, FinishedAt: r.FinishedAt,
		Reason: r.Reason, CreatedAt: r.CreatedAt,
	}
	if r.InstanceID != nil {
		j.InstanceID = *r.InstanceID
	}
	return j
}

func one(r sqlc.BurstJob, err error) (burst.Job, bool, error) {
	if errors.Is(err, pgx.ErrNoRows) {
		return burst.Job{}, false, nil
	}
	if err != nil {
		return burst.Job{}, false, err
	}
	return fromRow(r), true, nil
}

// CreateJob implements burst.Store.
func (s *Store) CreateJob(ctx context.Context, j burst.Job) (burst.Job, error) {
	var out burst.Job
	err := s.system(ctx, func(q *sqlc.Queries) error {
		r, err := q.InsertBurstJob(ctx, sqlc.InsertBurstJobParams{
			ID: j.ID, OrgID: j.OrgID, RunID: j.RunID, Request: j.Request, DeadlineSeconds: j.DeadlineSeconds, CreatedAt: j.CreatedAt,
		})
		out = fromRow(r)
		return err
	})
	if db.IsUniqueViolation(err) {
		return burst.Job{}, burst.ErrJobActive
	}
	return out, err
}

// ActiveJob implements burst.Store.
func (s *Store) ActiveJob(ctx context.Context, runID uuid.UUID) (burst.Job, bool, error) {
	var (
		j  burst.Job
		ok bool
	)
	err := s.system(ctx, func(q *sqlc.Queries) error {
		var err error
		j, ok, err = one(q.GetLiveBurstJobForRun(ctx, runID))
		return err
	})
	return j, ok, err
}

// GetJob implements burst.Store.
func (s *Store) GetJob(ctx context.Context, id uuid.UUID) (burst.Job, bool, error) {
	var (
		j  burst.Job
		ok bool
	)
	err := s.system(ctx, func(q *sqlc.Queries) error {
		var err error
		j, ok, err = one(q.GetBurstJob(ctx, id))
		return err
	})
	return j, ok, err
}

// Claim implements burst.Store.
func (s *Store) Claim(ctx context.Context, instanceID string, tokenHash []byte, now, leaseUntil time.Time) (burst.Job, bool, error) {
	var (
		j  burst.Job
		ok bool
	)
	err := s.system(ctx, func(q *sqlc.Queries) error {
		var err error
		j, ok, err = one(q.ClaimBurstJob(ctx, sqlc.ClaimBurstJobParams{InstanceID: &instanceID, TokenHash: tokenHash, Now: &now, LeaseUntil: &leaseUntil}))
		return err
	})
	return j, ok, err
}

func rows(n int64, err error) (bool, error) { return n > 0, err }

// Extend implements burst.Store.
func (s *Store) Extend(ctx context.Context, id uuid.UUID, tokenHash []byte, leaseUntil time.Time) (bool, error) {
	var ok bool
	err := s.system(ctx, func(q *sqlc.Queries) error {
		var err error
		ok, err = rows(q.ExtendBurstLease(ctx, sqlc.ExtendBurstLeaseParams{LeaseUntil: &leaseUntil, ID: id, TokenHash: tokenHash}))
		return err
	})
	return ok, err
}

// Finish implements burst.Store.
func (s *Store) Finish(ctx context.Context, id uuid.UUID, tokenHash []byte, state, reason string, now time.Time) (bool, error) {
	var ok bool
	err := s.system(ctx, func(q *sqlc.Queries) error {
		var err error
		ok, err = rows(q.FinishBurstJob(ctx, sqlc.FinishBurstJobParams{State: state, Reason: reason, Now: &now, ID: id, TokenHash: tokenHash}))
		return err
	})
	return ok, err
}

// Abandon implements burst.Store.
func (s *Store) Abandon(ctx context.Context, id uuid.UUID, now time.Time) (bool, error) {
	var ok bool
	err := s.system(ctx, func(q *sqlc.Queries) error {
		var err error
		ok, err = rows(q.AbandonBurstJob(ctx, sqlc.AbandonBurstJobParams{Now: &now, ID: id}))
		return err
	})
	return ok, err
}

// Cancel implements burst.Store.
func (s *Store) Cancel(ctx context.Context, id uuid.UUID, reason string, now time.Time) (bool, error) {
	if len(reason) > 500 {
		reason = reason[:500]
	}
	var ok bool
	err := s.system(ctx, func(q *sqlc.Queries) error {
		var err error
		ok, err = rows(q.CancelBurstJob(ctx, sqlc.CancelBurstJobParams{Reason: reason, Now: &now, ID: id}))
		return err
	})
	return ok, err
}

// ExpireLeases implements burst.Store.
func (s *Store) ExpireLeases(ctx context.Context, now time.Time) ([]uuid.UUID, error) {
	var ids []uuid.UUID
	err := s.system(ctx, func(q *sqlc.Queries) error {
		var err error
		ids, err = q.ExpireBurstLeases(ctx, &now)
		return err
	})
	return ids, err
}

// AppendEvents implements burst.Store.
func (s *Store) AppendEvents(ctx context.Context, job burst.Job, events []burst.Event) error {
	if len(events) == 0 {
		return nil
	}
	return s.system(ctx, func(q *sqlc.Queries) error {
		for _, e := range events {
			if err := q.InsertBurstEvent(ctx, sqlc.InsertBurstEventParams{JobID: job.ID, OrgID: job.OrgID, Seq: e.Seq, Event: e.Data}); err != nil {
				return err
			}
		}
		return nil
	})
}

// Events implements burst.Store.
func (s *Store) Events(ctx context.Context, jobID uuid.UUID, afterSeq int64, limit int) ([]burst.Event, error) {
	var out []burst.Event
	err := s.system(ctx, func(q *sqlc.Queries) error {
		rs, err := q.ListBurstEvents(ctx, sqlc.ListBurstEventsParams{JobID: jobID, Seq: afterSeq, Limit: int32(min(max(limit, 1), 10_000))}) //nolint:gosec // bounded
		for _, r := range rs {
			out = append(out, burst.Event{Seq: r.Seq, Data: r.Event})
		}
		return err
	})
	return out, err
}

// TouchInstance implements burst.Store.
func (s *Store) TouchInstance(ctx context.Context, in burst.Instance, now time.Time) error {
	return s.system(ctx, func(q *sqlc.Queries) error {
		return q.TouchBurstInstance(ctx, sqlc.TouchBurstInstanceParams{InstanceID: in.ID, InstanceType: in.Type, Lifecycle: in.Lifecycle, Now: now})
	})
}

// Status implements burst.Store.
func (s *Store) Status(ctx context.Context, sq burst.StatusQuery) (burst.PoolStatus, error) {
	var st burst.PoolStatus
	err := s.system(ctx, func(q *sqlc.Queries) error {
		jobs, err := q.CountBurstJobs(ctx)
		if err != nil {
			return err
		}
		live, err := q.CountLiveBurstInstances(ctx, sq.LiveSince)
		if err != nil {
			return err
		}
		used, err := q.BurstUsageSeconds(ctx, sqlc.BurstUsageSecondsParams{Now: sq.Now, MonthStart: sq.MonthStart, BootSeconds: sq.BootSeconds})
		if err != nil {
			return err
		}
		pool, err := q.GetBurstPool(ctx)
		if err != nil {
			return err
		}
		st = burst.PoolStatus{
			Queued: int(jobs.Queued), Busy: int(jobs.Busy), Live: int(live.Live), Idle: int(live.Idle),
			UsedSeconds: used, UnhealthyUntil: pool.UnhealthyUntil,
		}
		return nil
	})
	return st, err
}

// BusyInstances implements burst.Store.
func (s *Store) BusyInstances(ctx context.Context) ([]string, error) {
	var out []string
	err := s.system(ctx, func(q *sqlc.Queries) error {
		var err error
		out, err = q.ListBusyBurstInstances(ctx)
		return err
	})
	return out, err
}

// NoteClaim implements burst.Store.
func (s *Store) NoteClaim(ctx context.Context) error {
	return s.system(ctx, func(q *sqlc.Queries) error { return q.ResetBurstPool(ctx) })
}

// NoteNoClaim implements burst.Store.
func (s *Store) NoteNoClaim(ctx context.Context, threshold int, cooldownUntil time.Time) error {
	return s.system(ctx, func(q *sqlc.Queries) error {
		return q.NoteBurstNoClaim(ctx, sqlc.NoteBurstNoClaimParams{Threshold: int32(min(max(threshold, 1), 1000)), CooldownUntil: cooldownUntil}) //nolint:gosec // bounded
	})
}

// Prune implements burst.Store.
func (s *Store) Prune(ctx context.Context, before time.Time) (int64, error) {
	var n int64
	err := s.system(ctx, func(q *sqlc.Queries) error {
		var err error
		n, err = q.PruneBurstJobs(ctx, &before)
		return err
	})
	return n, err
}
