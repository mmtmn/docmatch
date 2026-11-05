//go:build integration

package burstpg_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/mmtmn/docmatch/services/api/internal/burst"
	"github.com/mmtmn/docmatch/services/api/internal/burst/burstpg"
	"github.com/mmtmn/docmatch/services/api/internal/db"
	"github.com/mmtmn/docmatch/services/api/internal/testdb"
)

// seedRun creates an organization and a run the jobs can reference.
func seedRun(t *testing.T, d *testdb.DB) (org, run uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	org, run = uuid.New(), uuid.New()
	err := db.WithOrgTx(ctx, d.App, org, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO orgs (id, name, slug, plan_code) VALUES ($1, 'Pool', $2, 'pro')`, org, "org-"+org.String()); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO runs (id, org_id, lane, status) VALUES ($1, $2, 'app', 'queued')`, run, org)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return org, run
}

func TestStoreLifecycleOnPostgres(t *testing.T) {
	d := testdb.Get(t)
	ctx := context.Background()
	st := burstpg.New(d.App)
	org, run := seedRun(t, d)
	now := time.Now().UTC().Truncate(time.Microsecond)

	job, err := st.CreateJob(ctx, burst.Job{ID: uuid.New(), OrgID: org, RunID: run, Request: []byte{1, 2}, DeadlineSeconds: 7200, CreatedAt: now})
	if err != nil || job.State != burst.StateQueued || job.DeadlineSeconds != 7200 {
		t.Fatalf("create = %+v %v", job, err)
	}
	// One live job per run: a restarted main worker re-attaches instead.
	if _, err := st.CreateJob(ctx, burst.Job{ID: uuid.New(), OrgID: org, RunID: run, Request: []byte{1}, DeadlineSeconds: 1, CreatedAt: now}); !errors.Is(err, burst.ErrJobActive) {
		t.Fatalf("segundo trabalho vivo: %v", err)
	}
	if live, ok, err := st.ActiveJob(ctx, run); err != nil || !ok || live.ID != job.ID {
		t.Fatalf("active = %+v %v %v", live, ok, err)
	}
	st0, err := st.Status(ctx, burst.StatusQuery{Now: now, MonthStart: burst.MonthStart(now), LiveSince: now.Add(-time.Minute), BootSeconds: 180})
	if err != nil || st0.Queued != 1 || st0.Busy != 0 || st0.Unhealthy(now) {
		t.Fatalf("status = %+v %v", st0, err)
	}

	// Claim: the oldest queued job, a lease, the token hash.
	token, hash, _ := burst.NewToken()
	claimed, ok, err := st.Claim(ctx, "i-0abc", hash, now, now.Add(90*time.Second))
	if err != nil || !ok || claimed.ID != job.ID || claimed.InstanceID != "i-0abc" || !burst.SameToken(token, claimed.TokenHash) {
		t.Fatalf("claim = %+v %v %v", claimed, ok, err)
	}
	if _, ok, _ := st.Claim(ctx, "i-0def", hash, now, now.Add(time.Minute)); ok {
		t.Fatal("não há outro na fila")
	}
	if ok, err := st.Extend(ctx, job.ID, hash, now.Add(3*time.Minute)); err != nil || !ok {
		t.Fatalf("extend = %v %v", ok, err)
	}
	if ok, _ := st.Extend(ctx, job.ID, burst.HashToken("outro"), now.Add(3*time.Minute)); ok {
		t.Fatal("token errado não renova")
	}

	// Events: ordered, idempotent by seq, paged.
	ev := []burst.Event{{Seq: 1, Data: []byte("a")}, {Seq: 2, Data: []byte("b")}, {Seq: 3, Data: []byte("c")}}
	if err := st.AppendEvents(ctx, claimed, ev); err != nil {
		t.Fatal(err)
	}
	if err := st.AppendEvents(ctx, claimed, []burst.Event{{Seq: 2, Data: []byte("dup")}, {Seq: 4, Data: []byte("d")}}); err != nil {
		t.Fatal(err)
	}
	got, err := st.Events(ctx, job.ID, 1, 2)
	if err != nil || len(got) != 2 || got[0].Seq != 2 || string(got[0].Data) != "b" || got[1].Seq != 3 {
		t.Fatalf("events = %+v %v", got, err)
	}
	if err := st.AppendEvents(ctx, claimed, nil); err != nil {
		t.Fatal(err)
	}

	// Instances and usage: 2 h inside the month plus one boot.
	if err := st.TouchInstance(ctx, burst.Instance{ID: "i-0abc", Type: "c7g.large", Lifecycle: "spot"}, now.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := st.TouchInstance(ctx, burst.Instance{ID: "i-0abc"}, now); err != nil {
		t.Fatal(err)
	}
	busy, _ := st.BusyInstances(ctx)
	if len(busy) != 1 || busy[0] != "i-0abc" {
		t.Fatalf("ocupadas = %v", busy)
	}
	monthStart := now.Add(-3 * time.Hour)
	st1, err := st.Status(ctx, burst.StatusQuery{Now: now, MonthStart: monthStart, LiveSince: now.Add(-time.Minute), BootSeconds: 180})
	if err != nil || st1.Busy != 1 || st1.Live != 1 || st1.Idle != 0 || st1.UsedSeconds < 7200+179 || st1.UsedSeconds > 7200+181 {
		t.Fatalf("status = %+v %v", st1, err)
	}

	// Lease expiry, then the worker's late finish is refused.
	lost, err := st.ExpireLeases(ctx, now.Add(4*time.Minute))
	if err != nil || len(lost) != 1 || lost[0] != job.ID {
		t.Fatalf("expire = %v %v", lost, err)
	}
	if ok, _ := st.Finish(ctx, job.ID, hash, burst.StateDone, "", now); ok {
		t.Fatal("um trabalho perdido não termina como feito")
	}
	if j, _, _ := st.GetJob(ctx, job.ID); j.State != burst.StateLost || j.FinishedAt == nil {
		t.Fatalf("job = %+v", j)
	}

	// A new attempt: queued, then abandoned (nobody came), then the breaker.
	again, err := st.CreateJob(ctx, burst.Job{ID: uuid.New(), OrgID: org, RunID: run, Request: []byte{1}, DeadlineSeconds: 60, CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := st.Abandon(ctx, again.ID, now); err != nil || !ok {
		t.Fatalf("abandon = %v %v", ok, err)
	}
	if ok, _ := st.Cancel(ctx, again.ID, "x", now); ok {
		t.Fatal("abandonado não cancela")
	}
	for range 2 {
		if err := st.NoteNoClaim(ctx, 2, now.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	sick, _ := st.Status(ctx, burst.StatusQuery{Now: now, MonthStart: monthStart, LiveSince: now})
	if !sick.Unhealthy(now) {
		t.Fatalf("disjuntor: %+v", sick)
	}
	if err := st.NoteClaim(ctx); err != nil {
		t.Fatal(err)
	}
	if healthy, _ := st.Status(ctx, burst.StatusQuery{Now: now, MonthStart: monthStart, LiveSince: now}); healthy.Unhealthy(now) {
		t.Fatal("um trabalhador apareceu: o disjuntor zera")
	}

	// Cancel and finish paths, then prune.
	third, _ := st.CreateJob(ctx, burst.Job{ID: uuid.New(), OrgID: org, RunID: run, Request: []byte{1}, DeadlineSeconds: 60, CreatedAt: now})
	if ok, err := st.Cancel(ctx, third.ID, "cancelada pelo cliente", now); err != nil || !ok {
		t.Fatalf("cancel = %v %v", ok, err)
	}
	fourth, _ := st.CreateJob(ctx, burst.Job{ID: uuid.New(), OrgID: org, RunID: run, Request: []byte{1}, DeadlineSeconds: 60, CreatedAt: now})
	_, h4, _ := burst.NewToken()
	if _, ok, _ := st.Claim(ctx, "i-0abc", h4, now, now.Add(time.Minute)); !ok {
		t.Fatal("claim do quarto")
	}
	if ok, err := st.Finish(ctx, fourth.ID, h4, burst.StateInterrupted, "interrupção Spot", now); err != nil || !ok {
		t.Fatalf("finish = %v %v", ok, err)
	}
	n, err := st.Prune(ctx, now.Add(5*time.Minute)) // the lost one finished at now+4m
	if err != nil || n != 4 {
		t.Fatalf("prune = %d %v", n, err)
	}
	if _, ok, _ := st.GetJob(ctx, job.ID); ok {
		t.Fatal("o trabalho podado sumiu com os eventos")
	}
	if left, _ := st.Events(ctx, job.ID, 0, 10); len(left) != 0 {
		t.Fatalf("eventos órfãos: %d", len(left))
	}
}

// Without system mode nothing of the pool is visible to the application role.
func TestPoolTablesNeedSystemMode(t *testing.T) {
	d := testdb.Get(t)
	ctx := context.Background()
	var n int
	if err := d.App.QueryRow(ctx, "SELECT count(*) FROM burst_pool").Scan(&n); err != nil || n != 0 {
		t.Fatalf("burst_pool sem modo sistema: %d %v", n, err)
	}
	if err := db.WithSystemTx(ctx, d.App, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, "SELECT count(*) FROM burst_pool").Scan(&n)
	}); err != nil || n != 1 {
		t.Fatalf("burst_pool em modo sistema: %d %v", n, err)
	}
}
