package extensions

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
)

const cleanupKind = "extension_credentials_cleanup"

type cleanupArgs struct{}

func (cleanupArgs) Kind() string { return cleanupKind }

type cleanupWorker struct {
	river.WorkerDefaults[cleanupArgs]
	pool *pgxpool.Pool
}

func (w *cleanupWorker) Timeout(*river.Job[cleanupArgs]) time.Duration { return time.Minute }
func (w *cleanupWorker) Work(ctx context.Context, _ *river.Job[cleanupArgs]) error {
	return Cleanup(ctx, w.pool)
}

// Cleanup bounds credential retention using the existing worker, with indexed,
// bounded batches. Consumed codes stay one day so replay can revoke their grant.
func Cleanup(ctx context.Context, pool *pgxpool.Pool) error {
	for _, query := range []string{
		`DELETE FROM extension_codes WHERE code_hash IN (SELECT code_hash FROM extension_codes WHERE expires_at<now()-interval '1 day' LIMIT 1000)`,
		`DELETE FROM extension_spent_refresh WHERE token_hash IN (SELECT token_hash FROM extension_spent_refresh WHERE expires_at<now() LIMIT 1000)`,
		`UPDATE extension_grants SET retry_response=NULL WHERE session_id IN (SELECT session_id FROM extension_grants WHERE retry_until<now() AND retry_response IS NOT NULL LIMIT 1000)`,
		`DELETE FROM sessions WHERE id IN (SELECT s.id FROM sessions s JOIN extension_grants g ON g.session_id=s.id WHERE s.expires_at<now()-interval '30 days' OR s.revoked_at<now()-interval '30 days' LIMIT 1000)`,
	} {
		if _, err := pool.Exec(ctx, query); err != nil {
			return err
		}
	}
	return nil
}
func AddWorkers(pool *pgxpool.Pool) func(*river.Workers) {
	return func(w *river.Workers) { river.AddWorker(w, &cleanupWorker{pool: pool}) }
}
func PeriodicJobs() []*river.PeriodicJob {
	return []*river.PeriodicJob{river.NewPeriodicJob(river.PeriodicInterval(time.Hour), func() (river.JobArgs, *river.InsertOpts) {
		return cleanupArgs{}, &river.InsertOpts{MaxAttempts: 3, UniqueOpts: river.UniqueOpts{ByPeriod: time.Hour}}
	}, &river.PeriodicJobOpts{ID: cleanupKind, RunOnStart: true})}
}
