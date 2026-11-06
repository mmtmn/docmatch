//go:build integration

package jobs_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/mmtmn/docmatch/services/api/internal/audit"
	"github.com/mmtmn/docmatch/services/api/internal/credits"
	"github.com/mmtmn/docmatch/services/api/internal/db"
	"github.com/mmtmn/docmatch/services/api/internal/db/pgstore"
	"github.com/mmtmn/docmatch/services/api/internal/jobs"
	"github.com/mmtmn/docmatch/services/api/internal/logx"
	"github.com/mmtmn/docmatch/services/api/internal/testdb"
)

func TestMain(m *testing.M) { testdb.Main(m) }

// TestWorkerRunsJobsOnPostgres starts a real River client on the application
// pool, lets the periodic jobs run at start-up and checks their effects.
func TestWorkerRunsJobsOnPostgres(t *testing.T) {
	d := testdb.Get(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// An organization with an expired grant, so the sweep has work to do.
	org := uuid.New()
	if err := db.WithOrgTx(ctx, d.App, org, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "INSERT INTO orgs (id, name, slug) VALUES ($1, 'Jobs', $2)", org, "jobs-"+org.String())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	creditSvc := credits.New(pgstore.NewCreditStore(d.App))
	past := time.Now().UTC().Add(-time.Hour)
	if _, err := creditSvc.Grant(ctx, org, credits.GrantParams{Kind: credits.GrantPlan, Amount: 100, ExpiresAt: &past}); err != nil {
		t.Fatal(err)
	}

	deps := jobs.Deps{
		Logger:      logx.Discard(),
		DB:          d.App,
		Partitioner: jobs.PoolPartitioner(d.App),
		Lister: func(ctx context.Context, now time.Time) ([]uuid.UUID, error) {
			return pgstore.ListOrgsWithExpiredGrants(ctx, d.App, now)
		},
		Expirer:  creditSvc,
		Audit:    audit.New(pgstore.NewAuditStore(d.App)),
		Hostname: "test",
	}
	client, err := jobs.NewClient(d.App, deps, jobs.ClientOptions{Periodic: true, MaxWorkers: 4, ID: "jobs-test"})
	if err != nil {
		t.Fatal(err)
	}
	completed, unsubscribe := client.Subscribe(river.EventKindJobCompleted, river.EventKindJobFailed)
	defer unsubscribe()
	if err := client.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer stopCancel()
		if err := client.Stop(stopCtx); err != nil {
			_ = client.StopAndCancel(stopCtx)
		}
	}()

	seen := map[string]bool{}
	deadline := time.After(90 * time.Second)
	// Other periodic jobs may finish first; wait for these effects, not any three events.
	for !seen[jobs.KindHeartbeat] || !seen[jobs.KindPartitionMaintenance] || !seen[jobs.KindExpireGrants] {
		select {
		case ev := <-completed:
			if ev.Kind == river.EventKindJobFailed {
				t.Fatalf("job %s falhou: %v", ev.Job.Kind, ev.Job.Errors)
			}
			seen[ev.Job.Kind] = true
		case <-deadline:
			t.Fatalf("jobs periódicos não concluíram a tempo: %v", seen)
		}
	}
	for _, kind := range []string{jobs.KindHeartbeat, jobs.KindPartitionMaintenance, jobs.KindExpireGrants} {
		if !seen[kind] {
			t.Fatalf("job %s não rodou", kind)
		}
	}

	// Effects: partitions two months ahead exist and the grant was retired.
	parts, err := db.ListPartitions(ctx, d.Admin)
	if err != nil {
		t.Fatal(err)
	}
	ahead := time.Now().UTC().AddDate(0, jobs.PartitionMonthsAhead, 0)
	want := "audit_log_" + time.Date(ahead.Year(), ahead.Month(), 1, 0, 0, 0, 0, time.UTC).Format("y2006m01")
	found := false
	for _, p := range parts {
		if p.Name == want {
			found = true
		}
	}
	if !found {
		t.Fatalf("partição %s deveria existir após o job", want)
	}
	acct, err := creditSvc.Balance(ctx, org)
	if err != nil || acct.Balance != 0 {
		t.Fatalf("concessão vencida deveria ter sido retirada: %+v %v", acct, err)
	}
	rep, err := audit.New(pgstore.NewAuditStore(d.App)).Verify(ctx, &org, 1, 0)
	if err != nil || rep.Checked != 1 {
		t.Fatalf("auditoria da expiração: %+v %v", rep, err)
	}

	// Ad-hoc inserts from the API side (no periodic schedule) also work.
	inserter, err := jobs.NewClient(d.App, deps, jobs.ClientOptions{})
	if err != nil {
		t.Fatal(err)
	}
	res, err := inserter.Insert(ctx, jobs.PartitionMaintenanceArgs{MonthsAhead: 1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	deadline = time.After(60 * time.Second)
	for {
		select {
		case ev := <-completed:
			if ev.Job.ID == res.Job.ID {
				if ev.Kind != river.EventKindJobCompleted {
					t.Fatalf("job inserido falhou: %v", ev.Job.Errors)
				}
				return
			}
		case <-deadline:
			t.Fatal("job inserido não concluiu")
		}
	}
}
