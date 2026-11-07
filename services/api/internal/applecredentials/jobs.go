package applecredentials

import (
	"context"
	"time"

	"github.com/riverqueue/river"
)

// SweepArgs carries no user identity or credentials.
type SweepArgs struct{}

// Kind implements river.JobArgs.
func (SweepArgs) Kind() string { return "apple_revocation_sweep" }

// InsertOpts limits scheduled duplicates.
func (SweepArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{MaxAttempts: 3, UniqueOpts: river.UniqueOpts{ByPeriod: time.Minute}}
}

type worker struct {
	river.WorkerDefaults[SweepArgs]
	store *Store
}

func (w *worker) Timeout(*river.Job[SweepArgs]) time.Duration             { return 5 * time.Minute }
func (w *worker) Work(ctx context.Context, _ *river.Job[SweepArgs]) error { return w.store.Sweep(ctx) }

// AddWorkers registers on the existing worker process.
func AddWorkers(store *Store) func(*river.Workers) {
	return func(w *river.Workers) { river.AddWorker(w, &worker{store: store}) }
}

// PeriodicJobs recovers pending grants even if the process stopped during deletion.
func PeriodicJobs() []*river.PeriodicJob {
	return []*river.PeriodicJob{river.NewPeriodicJob(river.PeriodicInterval(time.Minute), func() (river.JobArgs, *river.InsertOpts) { return SweepArgs{}, nil }, &river.PeriodicJobOpts{ID: "apple_revocation_sweep", RunOnStart: true})}
}
