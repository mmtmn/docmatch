// Package jobargs declares the argument types of the pipeline jobs and the
// Enqueuer the API services use to insert them. It lives apart from the
// workers so the services (uploads, runs, rules) can enqueue without
// importing the worker package that depends on them.
package jobargs

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

// Job kinds. Never rename a kind: queued rows reference it.
const (
	KindScan   = "upload_scan"
	KindRun    = "run_execute"
	KindLearn  = "rules_learn"
	KindOutbox = "outbox_send"
)

// activeStates dedupes a job while it is pending or running, but lets the
// same arguments be enqueued again once a previous job finished (a resumed
// run, a new learning pass).
var activeStates = []rivertype.JobState{
	rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRetryable,
	rivertype.JobStateRunning, rivertype.JobStateScheduled,
}

// ScanArgs asks the scanner for a verdict on an upload.
type ScanArgs struct {
	OrgID    uuid.UUID `json:"org_id"`
	UploadID uuid.UUID `json:"upload_id"`
}

// Kind implements river.JobArgs.
func (ScanArgs) Kind() string { return KindScan }

// InsertOpts dedupes concurrent scans of the same upload.
func (ScanArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{MaxAttempts: 5, UniqueOpts: river.UniqueOpts{ByArgs: true, ByState: activeStates}}
}

// RunArgs executes (or resumes) a reconciliation run.
type RunArgs struct {
	OrgID  uuid.UUID `json:"org_id"`
	RunID  uuid.UUID `json:"run_id"`
	Resume bool      `json:"resume"`
	// ExpectedSeconds sizes the job timeout (the deadline scales with it);
	// zero in jobs enqueued before it existed, which get the floor.
	ExpectedSeconds int64 `json:"expected_seconds,omitempty"`
}

// Kind implements river.JobArgs.
func (RunArgs) Kind() string { return KindRun }

// InsertOpts dedupes a run while it is queued or executing.
func (RunArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{MaxAttempts: 3, UniqueOpts: river.UniqueOpts{ByArgs: true, ByState: activeStates}}
}

// LearnArgs turns the feedback of a run into proposed rules.
type LearnArgs struct {
	OrgID uuid.UUID `json:"org_id"`
	RunID uuid.UUID `json:"run_id"`
}

// Kind implements river.JobArgs.
func (LearnArgs) Kind() string { return KindLearn }

// InsertOpts coalesces a burst of feedback into one learning pass per minute.
func (LearnArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{MaxAttempts: 3, UniqueOpts: river.UniqueOpts{ByArgs: true, ByPeriod: time.Minute, ByState: activeStates}}
}

// DedicatedOutboxKinds are outbox kinds delivered by their own worker (the
// e-mail outbox of internal/email/emailpg); the generic outbox_send job skips
// them. Keep in sync with email.OutboxKind (a test checks it).
var DedicatedOutboxKinds = []string{"email"}

// OutboxArgs drains the transactional outbox.
type OutboxArgs struct{}

// Kind implements river.JobArgs.
func (OutboxArgs) Kind() string { return KindOutbox }

// InsertOpts dedupes drains inside one interval.
func (OutboxArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{MaxAttempts: 3, UniqueOpts: river.UniqueOpts{ByPeriod: 30 * time.Second}}
}

// Enqueuer inserts jobs, optionally inside the caller's transaction so a
// row and its job are committed together.
type Enqueuer interface {
	Enqueue(ctx context.Context, args river.JobArgs) error
	EnqueueTx(ctx context.Context, tx pgx.Tx, args river.JobArgs) error
}

// RiverEnqueuer adapts a River client to Enqueuer.
type RiverEnqueuer struct {
	Client *river.Client[pgx.Tx]
}

// Enqueue implements Enqueuer.
func (e RiverEnqueuer) Enqueue(ctx context.Context, args river.JobArgs) error {
	if e.Client == nil {
		return errors.New("jobs: fila não configurada")
	}
	_, err := e.Client.Insert(ctx, args, nil)
	return err
}

// EnqueueTx implements Enqueuer.
func (e RiverEnqueuer) EnqueueTx(ctx context.Context, tx pgx.Tx, args river.JobArgs) error {
	if e.Client == nil {
		return errors.New("jobs: fila não configurada")
	}
	if tx == nil {
		_, err := e.Client.Insert(ctx, args, nil)
		return err
	}
	_, err := e.Client.InsertTx(ctx, tx, args, nil)
	return err
}

// LazyEnqueuer forwards to an Enqueuer set later: services are built
// before the River client (whose workers depend on those services), so
// they receive this and the process binds the client afterwards.
type LazyEnqueuer struct {
	mu     sync.RWMutex
	target Enqueuer
}

// Set binds the real enqueuer.
func (l *LazyEnqueuer) Set(e Enqueuer) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.target = e
}

func (l *LazyEnqueuer) get() (Enqueuer, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if l.target == nil {
		return nil, errors.New("jobs: fila ainda não ligada")
	}
	return l.target, nil
}

// Enqueue implements Enqueuer.
func (l *LazyEnqueuer) Enqueue(ctx context.Context, args river.JobArgs) error {
	e, err := l.get()
	if err != nil {
		return err
	}
	return e.Enqueue(ctx, args)
}

// EnqueueTx implements Enqueuer.
func (l *LazyEnqueuer) EnqueueTx(ctx context.Context, tx pgx.Tx, args river.JobArgs) error {
	e, err := l.get()
	if err != nil {
		return err
	}
	return e.EnqueueTx(ctx, tx, args)
}

// Recorder is an in-memory Enqueuer that remembers what was enqueued (tests).
type Recorder struct {
	mu   sync.Mutex
	Jobs []river.JobArgs
	// Err, when set, is returned by every call.
	Err error
}

// Enqueue implements Enqueuer.
func (r *Recorder) Enqueue(_ context.Context, args river.JobArgs) error {
	if r.Err != nil {
		return r.Err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Jobs = append(r.Jobs, args)
	return nil
}

// EnqueueTx implements Enqueuer.
func (r *Recorder) EnqueueTx(ctx context.Context, _ pgx.Tx, args river.JobArgs) error {
	return r.Enqueue(ctx, args)
}

// Kinds lists the kinds recorded so far, in order.
func (r *Recorder) Kinds() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.Jobs))
	for _, j := range r.Jobs {
		out = append(out, j.Kind())
	}
	return out
}
