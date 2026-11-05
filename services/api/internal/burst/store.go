package burst

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

// Job states. A job lives from queued to one terminal state; at most one job
// per run is queued or claimed at a time.
const (
	StateQueued  = "queued"
	StateClaimed = "claimed"
	// StateDone: the worker streamed the terminal event and finished.
	StateDone = "done"
	// StateInterrupted: the worker got a termination notice (Spot, scale-in).
	StateInterrupted = "interrupted"
	// StateFailed: the worker's harness or transport broke (requeued too).
	StateFailed = "failed"
	// StateLost: the lease expired without a word from the worker.
	StateLost = "lost"
	// StateAbandoned: nobody claimed it in time; the run fell back to the main box.
	StateAbandoned = "abandoned"
	// StateCancelled: dropped by the main worker (run cancelled or timed out).
	StateCancelled = "cancelled"
)

// Terminal reports whether a job state is final.
func Terminal(state string) bool { return state != StateQueued && state != StateClaimed }

// ErrJobActive is returned by CreateJob when the run already has a live job.
var ErrJobActive = errors.New("burst: a execução já tem um trabalho ativo no pool")

// Job is one run handed to the pool (table burst_jobs).
type Job struct {
	ID    uuid.UUID
	OrgID uuid.UUID
	RunID uuid.UUID
	State string
	// Request is the pb.RunRequest (protobuf) the main worker built.
	Request []byte
	// DeadlineSeconds is how long the worker may take once it claims the job.
	DeadlineSeconds int64
	InstanceID      string
	TokenHash       []byte
	ClaimedAt       *time.Time
	LeaseExpiresAt  *time.Time
	FinishedAt      *time.Time
	Reason          string
	CreatedAt       time.Time
}

// Event is one harness event a worker posted (pb.RunEvent, protobuf).
type Event struct {
	Seq  int64
	Data []byte
}

// Instance is a worker the API heard from (table burst_instances).
type Instance struct {
	ID        string
	Type      string
	Lifecycle string
}

// PoolStatus is what routing and scaling look at.
type PoolStatus struct {
	Queued int
	Busy   int
	// Live counts instances heard from recently; Idle are the live ones
	// without a job.
	Live int
	Idle int
	// UsedSeconds are the instance-seconds of the current month (UTC).
	UsedSeconds float64
	// UnhealthyUntil is set when recent scale-ups produced no worker.
	UnhealthyUntil *time.Time
}

// Unhealthy reports whether the pool is cooling down at now.
func (p PoolStatus) Unhealthy(now time.Time) bool {
	return p.UnhealthyUntil != nil && now.Before(*p.UnhealthyUntil)
}

// UsedHours are the instance-hours of the month.
func (p PoolStatus) UsedHours() float64 { return p.UsedSeconds / 3600 }

// StatusQuery parameterises Store.Status.
type StatusQuery struct {
	Now time.Time
	// MonthStart is the first instant of the month the usage counts from.
	MonthStart time.Time
	// LiveSince is the oldest last-seen time of a live instance.
	LiveSince time.Time
	// BootSeconds is charged once per instance that started this month
	// (boot and install happen before the first call).
	BootSeconds float64
}

// Store persists jobs, events, instances and the pool state.
type Store interface {
	// CreateJob inserts a queued job (ErrJobActive when the run has one).
	CreateJob(ctx context.Context, j Job) (Job, error)
	// ActiveJob returns the queued or claimed job of a run.
	ActiveJob(ctx context.Context, runID uuid.UUID) (Job, bool, error)
	GetJob(ctx context.Context, id uuid.UUID) (Job, bool, error)
	// Claim hands the oldest queued job to instanceID with a lease.
	Claim(ctx context.Context, instanceID string, tokenHash []byte, now, leaseUntil time.Time) (Job, bool, error)
	// Extend renews the lease of a job claimed with tokenHash; ok is false
	// when the job is no longer claimed by that token.
	Extend(ctx context.Context, id uuid.UUID, tokenHash []byte, leaseUntil time.Time) (bool, error)
	// Finish moves a job claimed with tokenHash to a terminal state.
	Finish(ctx context.Context, id uuid.UUID, tokenHash []byte, state, reason string, now time.Time) (bool, error)
	// Abandon moves a still-queued job to abandoned.
	Abandon(ctx context.Context, id uuid.UUID, now time.Time) (bool, error)
	// Cancel moves a queued or claimed job to cancelled.
	Cancel(ctx context.Context, id uuid.UUID, reason string, now time.Time) (bool, error)
	// ExpireLeases moves claimed jobs whose lease ended to lost.
	ExpireLeases(ctx context.Context, now time.Time) ([]uuid.UUID, error)
	// AppendEvents stores events of a job; a seq already stored is skipped.
	AppendEvents(ctx context.Context, job Job, events []Event) error
	// Events returns the events after seq, in order.
	Events(ctx context.Context, jobID uuid.UUID, afterSeq int64, limit int) ([]Event, error)
	// TouchInstance records that a worker called (usage and liveness).
	TouchInstance(ctx context.Context, in Instance, now time.Time) error
	// Status aggregates the pool.
	Status(ctx context.Context, q StatusQuery) (PoolStatus, error)
	// BusyInstances lists the instances holding a claimed job.
	BusyInstances(ctx context.Context) ([]string, error)
	// NoteClaim resets the failure count of the pool (a worker showed up).
	NoteClaim(ctx context.Context) error
	// NoteNoClaim counts a job nobody claimed; at threshold the pool is
	// marked unhealthy until cooldownUntil.
	NoteNoClaim(ctx context.Context, threshold int, cooldownUntil time.Time) error
	// Prune deletes jobs (and their events) finished before.
	Prune(ctx context.Context, before time.Time) (int64, error)
}

// MonthStart is the first instant of the UTC month of t.
func MonthStart(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}
