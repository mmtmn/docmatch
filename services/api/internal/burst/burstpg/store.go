// Package burstpg is the Postgres store of the pool of on-demand workers
// (db/migrations/0022_burst_workers.up.sql). Every call runs in system mode
// (the pool serves every organization) and every instant comes from the
// database's clock.
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

// reasonMax bounds the reason column (600 characters in the schema).
const reasonMax = 500

func (s *Store) system(ctx context.Context, fn func(q *sqlc.Queries) error) error {
	return db.WithSystemTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		return fn(sqlc.New(tx))
	})
}

func fromRow(r sqlc.BurstJob) burst.Job {
	j := burst.Job{
		ID: r.ID, OrgID: r.OrgID, RunID: r.RunID, State: r.State, Request: r.Request, DeadlineSeconds: r.DeadlineSeconds,
		TokenHash: r.TokenHash, ClaimedAt: r.ClaimedAt, LeaseExpiresAt: r.LeaseExpiresAt, FinishedAt: r.FinishedAt,
		Reason: r.Reason, EventsCount: int64(r.EventsCount), EventsBytes: r.EventsBytes, CreatedAt: r.CreatedAt,
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

// CreateJob implements burst.Store: capacity is reserved under a
// transaction-scoped advisory lock, so concurrent dispatches never pass maxLive.
func (s *Store) CreateJob(ctx context.Context, j burst.Job, maxLive int) (burst.Job, error) {
	var out burst.Job
	err := s.system(ctx, func(q *sqlc.Queries) error {
		if err := q.LockBurstCapacity(ctx); err != nil {
			return err
		}
		if maxLive > 0 {
			live, err := q.CountLiveBurstJobs(ctx)
			if err != nil {
				return err
			}
			if live >= int64(maxLive) {
				return burst.ErrPoolFull
			}
		}
		r, err := q.InsertBurstJob(ctx, sqlc.InsertBurstJobParams{
			ID: j.ID, OrgID: j.OrgID, RunID: j.RunID, Request: j.Request, DeadlineSeconds: j.DeadlineSeconds,
		})
		out = fromRow(r)
		return err
	})
	if db.IsUniqueViolation(err) {
		return burst.Job{}, burst.ErrJobActive
	}
	return out, err
}

func (s *Store) getOne(ctx context.Context, get func(q *sqlc.Queries) (sqlc.BurstJob, error)) (burst.Job, bool, error) {
	var (
		j  burst.Job
		ok bool
	)
	err := s.system(ctx, func(q *sqlc.Queries) error {
		var err error
		j, ok, err = one(get(q))
		return err
	})
	return j, ok, err
}

// ActiveJob implements burst.Store.
func (s *Store) ActiveJob(ctx context.Context, runID uuid.UUID) (burst.Job, bool, error) {
	return s.getOne(ctx, func(q *sqlc.Queries) (sqlc.BurstJob, error) { return q.GetLiveBurstJobForRun(ctx, runID) })
}

// LatestJob implements burst.Store.
func (s *Store) LatestJob(ctx context.Context, runID uuid.UUID) (burst.Job, bool, error) {
	return s.getOne(ctx, func(q *sqlc.Queries) (sqlc.BurstJob, error) { return q.GetLatestBurstJobForRun(ctx, runID) })
}

// GetJob implements burst.Store.
func (s *Store) GetJob(ctx context.Context, id uuid.UUID) (burst.Job, bool, error) {
	return s.getOne(ctx, func(q *sqlc.Queries) (sqlc.BurstJob, error) { return q.GetBurstJob(ctx, id) })
}

// Claim implements burst.Store.
func (s *Store) Claim(ctx context.Context, instanceID string, tokenHash []byte, lease time.Duration) (burst.Job, bool, error) {
	return s.getOne(ctx, func(q *sqlc.Queries) (sqlc.BurstJob, error) {
		return q.ClaimBurstJob(ctx, sqlc.ClaimBurstJobParams{InstanceID: &instanceID, TokenHash: tokenHash, LeaseSeconds: burst.Seconds(lease)})
	})
}

func (s *Store) rows(ctx context.Context, fn func(q *sqlc.Queries) (int64, error)) (bool, error) {
	var n int64
	err := s.system(ctx, func(q *sqlc.Queries) error {
		var err error
		n, err = fn(q)
		return err
	})
	return n > 0, err
}

// Extend implements burst.Store.
func (s *Store) Extend(ctx context.Context, id uuid.UUID, tokenHash []byte, lease time.Duration) (bool, error) {
	return s.rows(ctx, func(q *sqlc.Queries) (int64, error) {
		return q.ExtendBurstLease(ctx, sqlc.ExtendBurstLeaseParams{LeaseSeconds: burst.Seconds(lease), ID: id, TokenHash: tokenHash})
	})
}

// Finish implements burst.Store.
func (s *Store) Finish(ctx context.Context, id uuid.UUID, tokenHash []byte, state, reason string) (bool, error) {
	return s.rows(ctx, func(q *sqlc.Queries) (int64, error) {
		return q.FinishBurstJob(ctx, sqlc.FinishBurstJobParams{State: state, Reason: burst.Truncate(reason, reasonMax), ID: id, TokenHash: tokenHash})
	})
}

// Abandon implements burst.Store.
func (s *Store) Abandon(ctx context.Context, id uuid.UUID, wait time.Duration) (bool, error) {
	return s.rows(ctx, func(q *sqlc.Queries) (int64, error) {
		return q.AbandonBurstJob(ctx, sqlc.AbandonBurstJobParams{ID: id, WaitSeconds: burst.Seconds(wait)})
	})
}

// Cancel implements burst.Store.
func (s *Store) Cancel(ctx context.Context, id uuid.UUID, reason string) (bool, error) {
	return s.rows(ctx, func(q *sqlc.Queries) (int64, error) {
		return q.CancelBurstJob(ctx, sqlc.CancelBurstJobParams{Reason: burst.Truncate(reason, reasonMax), ID: id})
	})
}

// CancelRun implements burst.Store.
func (s *Store) CancelRun(ctx context.Context, runID uuid.UUID, reason string) (bool, error) {
	return s.rows(ctx, func(q *sqlc.Queries) (int64, error) {
		return q.CancelLiveBurstJobForRun(ctx, sqlc.CancelLiveBurstJobForRunParams{Reason: burst.Truncate(reason, reasonMax), RunID: runID})
	})
}

// ExpireLeases implements burst.Store.
func (s *Store) ExpireLeases(ctx context.Context) ([]uuid.UUID, error) {
	var ids []uuid.UUID
	err := s.system(ctx, func(q *sqlc.Queries) error {
		var err error
		ids, err = q.ExpireBurstLeases(ctx)
		return err
	})
	return ids, err
}

// AppendEvents implements burst.Store: the job row is locked while the batch
// goes in, and the quota counts only the rows actually inserted.
func (s *Store) AppendEvents(ctx context.Context, job burst.Job, events []burst.Event, quota burst.EventQuota) error {
	if len(events) == 0 {
		return nil
	}
	return s.system(ctx, func(q *sqlc.Queries) error {
		cur, err := q.LockBurstJobEvents(ctx, job.ID)
		if err != nil {
			return err
		}
		var added, size int64
		for _, e := range events {
			n, err := q.InsertBurstEvent(ctx, sqlc.InsertBurstEventParams{JobID: job.ID, OrgID: job.OrgID, Seq: e.Seq, Event: e.Data})
			if err != nil {
				return err
			}
			if n > 0 {
				added++
				size += int64(len(e.Data))
			}
		}
		if (quota.MaxEvents > 0 && int64(cur.EventsCount)+added > quota.MaxEvents) || (quota.MaxBytes > 0 && cur.EventsBytes+size > quota.MaxBytes) {
			return burst.ErrEventQuota // the transaction rolls the batch back
		}
		return q.AddBurstJobEvents(ctx, sqlc.AddBurstJobEventsParams{Events: int32(min(added, 1<<30)), Bytes: size, ID: job.ID}) //nolint:gosec // bounded
	})
}

// Events implements burst.Store.
func (s *Store) Events(ctx context.Context, jobID uuid.UUID, afterSeq int64, limit int, maxBytes int64) ([]burst.Event, error) {
	var out []burst.Event
	err := s.system(ctx, func(q *sqlc.Queries) error {
		rs, err := q.ListBurstEvents(ctx, sqlc.ListBurstEventsParams{JobID: jobID, AfterSeq: afterSeq, MaxRows: int32(min(max(limit, 1), 10_000)), MaxBytes: max(maxBytes, 1)}) //nolint:gosec // bounded
		for _, r := range rs {
			out = append(out, burst.Event{Seq: r.Seq, Data: r.Event})
		}
		return err
	})
	return out, err
}

// TouchInstance implements burst.Store.
func (s *Store) TouchInstance(ctx context.Context, in burst.Instance) error {
	return s.system(ctx, func(q *sqlc.Queries) error {
		return q.TouchBurstInstance(ctx, sqlc.TouchBurstInstanceParams{InstanceID: in.ID, InstanceType: burst.Truncate(in.Type, 64), Lifecycle: burst.Truncate(in.Lifecycle, 32)})
	})
}

// InstanceSeen implements burst.Store.
func (s *Store) InstanceSeen(ctx context.Context, id string, within time.Duration) (bool, error) {
	var seen bool
	err := s.system(ctx, func(q *sqlc.Queries) error {
		var err error
		seen, err = q.BurstInstanceSeen(ctx, sqlc.BurstInstanceSeenParams{InstanceID: id, WithinSeconds: burst.Seconds(within)})
		return err
	})
	return seen, err
}

// NoteContact implements burst.Store.
func (s *Store) NoteContact(ctx context.Context, c burst.Contact, register bool) error {
	return s.system(ctx, func(q *sqlc.Queries) error {
		if register {
			if err := q.TouchBurstInstance(ctx, sqlc.TouchBurstInstanceParams{InstanceID: c.InstanceID}); err != nil {
				return err
			}
		}
		_, err := q.NoteBurstContact(ctx, sqlc.NoteBurstContactParams{
			AgentVersion: burst.Truncate(c.Version, 64), LastError: burst.Truncate(c.LastError, reasonMax), InstanceID: c.InstanceID,
		})
		return err
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
		live, err := q.CountLiveBurstInstances(ctx, burst.Seconds(sq.LiveWindow))
		if err != nil {
			return err
		}
		used, err := q.BurstUsageSeconds(ctx, sqlc.BurstUsageSecondsParams{MonthStart: sq.MonthStart, BootSeconds: sq.BootSeconds})
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
func (s *Store) NoteNoClaim(ctx context.Context, threshold int, cooldown time.Duration) error {
	return s.system(ctx, func(q *sqlc.Queries) error {
		return q.NoteBurstNoClaim(ctx, sqlc.NoteBurstNoClaimParams{Threshold: int32(min(max(threshold, 1), 1000)), CooldownSeconds: burst.Seconds(cooldown)}) //nolint:gosec // bounded
	})
}

// Prune implements burst.Store.
func (s *Store) Prune(ctx context.Context, olderThan time.Duration) (int64, error) {
	var n int64
	err := s.system(ctx, func(q *sqlc.Queries) error {
		var err error
		n, err = q.PruneBurstJobs(ctx, burst.Seconds(olderThan))
		return err
	})
	return n, err
}
