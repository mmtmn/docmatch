package burst

import (
	"context"
	"errors"
	"time"
	"unicode/utf8"

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

// Errors of the store.
var (
	// ErrJobActive: the run already has a live job.
	ErrJobActive = errors.New("burst: a execução já tem um trabalho ativo no pool")
	// ErrPoolFull: every place the pool may have is taken (checked atomically).
	ErrPoolFull = errors.New("burst: o pool está no teto de trabalhos simultâneos")
	// ErrEventQuota: the job stored as many events or bytes as it may.
	ErrEventQuota = errors.New("burst: o trabalho passou da cota de eventos")
)

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
	EventsCount     int64
	EventsBytes     int64
	CreatedAt       time.Time
}

// Event is one harness event a worker posted (pb.RunEvent, protobuf).
type Event struct {
	Seq  int64
	Data []byte
}

// EventQuota bounds what one job may store.
type EventQuota struct {
	MaxEvents int64
	MaxBytes  int64
}

// Instance is a worker of the pool (table burst_instances).
type Instance struct {
	ID        string
	Type      string
	Lifecycle string
}

// Contact is the worker's sign of life: its version and its last error.
type Contact struct {
	InstanceID string
	Version    string
	LastError  string
}

// PoolStatus is what routing and scaling look at.
type PoolStatus struct {
	Queued int
	Busy   int
	// Live counts instances seen recently; Idle are the live ones without a job.
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
	// MonthStart is the first instant of the month the usage counts from.
	MonthStart time.Time
	// LiveWindow: an instance seen within it is live.
	LiveWindow time.Duration
	// BootSeconds are charged once per instance first seen this month
	// (manual pools: boot and install happen before the first call).
	BootSeconds float64
}

// Store persists jobs, events, instances and the pool state. Every lease,
// expiry and age is computed with the store's clock (the database's now()).
type Store interface {
	// CreateJob inserts a queued job unless the run has one (ErrJobActive)
	// or maxLive jobs are already queued or claimed (ErrPoolFull; 0 = no limit).
	CreateJob(ctx context.Context, j Job, maxLive int) (Job, error)
	// ActiveJob returns the queued or claimed job of a run.
	ActiveJob(ctx context.Context, runID uuid.UUID) (Job, bool, error)
	// LatestJob returns the newest job of a run, in any state.
	LatestJob(ctx context.Context, runID uuid.UUID) (Job, bool, error)
	GetJob(ctx context.Context, id uuid.UUID) (Job, bool, error)
	// Claim hands the oldest queued job to instanceID with a lease.
	Claim(ctx context.Context, instanceID string, tokenHash []byte, lease time.Duration) (Job, bool, error)
	// Extend renews a lease that has not expired, of a job claimed with tokenHash.
	Extend(ctx context.Context, id uuid.UUID, tokenHash []byte, lease time.Duration) (bool, error)
	// Finish moves a job claimed with tokenHash to a terminal state.
	Finish(ctx context.Context, id uuid.UUID, tokenHash []byte, state, reason string) (bool, error)
	// Abandon moves a job queued for at least wait to abandoned.
	Abandon(ctx context.Context, id uuid.UUID, wait time.Duration) (bool, error)
	// Cancel moves a queued or claimed job to cancelled.
	Cancel(ctx context.Context, id uuid.UUID, reason string) (bool, error)
	// CancelRun cancels the live job of a run, if any.
	CancelRun(ctx context.Context, runID uuid.UUID, reason string) (bool, error)
	// ExpireLeases moves claimed jobs whose lease ended to lost and returns them.
	ExpireLeases(ctx context.Context) ([]uuid.UUID, error)
	// AppendEvents stores events of a job within the quota; a seq already
	// stored is skipped.
	AppendEvents(ctx context.Context, job Job, events []Event, quota EventQuota) error
	// Events returns the events after seq, in order, up to limit rows and
	// maxBytes bytes (the first row always).
	Events(ctx context.Context, jobID uuid.UUID, afterSeq int64, limit int, maxBytes int64) ([]Event, error)
	// TouchInstance records that an instance exists now (usage and liveness).
	TouchInstance(ctx context.Context, in Instance) error
	// InstanceSeen reports whether an instance was touched within the window.
	InstanceSeen(ctx context.Context, id string, within time.Duration) (bool, error)
	// NoteContact records a worker's sign of life; register creates the
	// instance when it is unknown (manual pools).
	NoteContact(ctx context.Context, c Contact, register bool) error
	// Status aggregates the pool.
	Status(ctx context.Context, q StatusQuery) (PoolStatus, error)
	// BusyInstances lists the instances holding a claimed job.
	BusyInstances(ctx context.Context) ([]string, error)
	// NoteClaim resets the failure count of the pool (a worker showed up).
	NoteClaim(ctx context.Context) error
	// NoteNoClaim counts a job nobody claimed while the pool had room; at
	// threshold the pool is unhealthy for cooldown.
	NoteNoClaim(ctx context.Context, threshold int, cooldown time.Duration) error
	// Prune deletes jobs (and their events) finished more than olderThan ago.
	Prune(ctx context.Context, olderThan time.Duration) (int64, error)
}

// MonthStart is the first instant of the UTC month of t.
func MonthStart(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}

// Truncate cuts s to at most n bytes on a rune boundary (Postgres refuses a
// split UTF-8 sequence).
func Truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// Seconds turns a duration into whole seconds for SQL (at least one).
func Seconds(d time.Duration) int32 {
	s := int64(d / time.Second)
	if s < 1 {
		s = 1
	}
	if s > 1<<30 {
		s = 1 << 30
	}
	return int32(s) //nolint:gosec // bounded above
}
