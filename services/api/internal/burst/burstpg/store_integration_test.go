//go:build integration

package burstpg_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/mmtmn/docmatch/services/api/internal/burst"
	"github.com/mmtmn/docmatch/services/api/internal/burst/burstpg"
	"github.com/mmtmn/docmatch/services/api/internal/db"
	"github.com/mmtmn/docmatch/services/api/internal/testdb"
)

// seedRuns creates an organization and n runs the jobs can reference.
func seedRuns(t *testing.T, d *testdb.DB, n int) (uuid.UUID, []uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	org := uuid.New()
	runs := make([]uuid.UUID, n)
	err := db.WithOrgTx(ctx, d.App, org, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO orgs (id, name, slug, plan_code) VALUES ($1, 'Pool', $2, 'pro')`, org, "org-"+org.String()); err != nil {
			return err
		}
		for i := range runs {
			runs[i] = uuid.New()
			if _, err := tx.Exec(ctx, `INSERT INTO runs (id, org_id, lane, status) VALUES ($1, $2, 'app', 'queued')`, runs[i], org); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return org, runs
}

func newJob(org, run uuid.UUID) burst.Job {
	return burst.Job{ID: uuid.New(), OrgID: org, RunID: run, Request: []byte{1, 2}, DeadlineSeconds: 7200}
}

func TestStoreLifecycleOnPostgres(t *testing.T) {
	d := testdb.Get(t)
	ctx := context.Background()
	st := burstpg.New(d.App)
	org, runs := seedRuns(t, d, 1)
	run := runs[0]

	job, err := st.CreateJob(ctx, newJob(org, run), 2)
	if err != nil || job.State != burst.StateQueued || job.DeadlineSeconds != 7200 {
		t.Fatalf("create = %+v %v", job, err)
	}
	// One live job per run: a restarted main worker re-attaches instead.
	if _, err := st.CreateJob(ctx, newJob(org, run), 2); !errors.Is(err, burst.ErrJobActive) {
		t.Fatalf("segundo trabalho vivo: %v", err)
	}
	if live, ok, err := st.ActiveJob(ctx, run); err != nil || !ok || live.ID != job.ID {
		t.Fatalf("active = %+v %v %v", live, ok, err)
	}
	if latest, ok, _ := st.LatestJob(ctx, run); !ok || latest.ID != job.ID {
		t.Fatalf("latest = %+v", latest)
	}
	q := burst.StatusQuery{MonthStart: burst.MonthStart(time.Now()), LiveWindow: time.Minute, BootSeconds: 180}
	if st0, err := st.Status(ctx, q); err != nil || st0.Queued != 1 || st0.Busy != 0 {
		t.Fatalf("status = %+v %v", st0, err)
	}
	// Abandon waits for the database's clock: not yet after a fresh insert.
	if ok, _ := st.Abandon(ctx, job.ID, time.Hour); ok {
		t.Fatal("abandonou antes da espera")
	}

	// Claim: the oldest queued job, a lease on the database clock, the token hash.
	token, hash, _ := burst.NewToken()
	claimed, ok, err := st.Claim(ctx, "i-0abc", hash, 90*time.Second)
	if err != nil || !ok || claimed.ID != job.ID || claimed.InstanceID != "i-0abc" || !burst.SameToken(token, claimed.TokenHash) || claimed.LeaseExpiresAt == nil {
		t.Fatalf("claim = %+v %v %v", claimed, ok, err)
	}
	if _, ok, _ := st.Claim(ctx, "i-0def", hash, time.Minute); ok {
		t.Fatal("não há outro na fila")
	}
	if ok, err := st.Extend(ctx, job.ID, hash, 3*time.Minute); err != nil || !ok {
		t.Fatalf("extend = %v %v", ok, err)
	}
	if ok, _ := st.Extend(ctx, job.ID, burst.HashToken("outro"), 3*time.Minute); ok {
		t.Fatal("token errado não renova")
	}

	// Events: ordered, idempotent by seq, counted against the quota, paged by
	// rows and by bytes.
	quota := burst.EventQuota{MaxEvents: 4, MaxBytes: 1 << 20}
	ev := []burst.Event{{Seq: 1, Data: []byte("aaaa")}, {Seq: 2, Data: []byte("bbbb")}, {Seq: 3, Data: []byte("cccc")}}
	if err := st.AppendEvents(ctx, claimed, ev, quota); err != nil {
		t.Fatal(err)
	}
	if err := st.AppendEvents(ctx, claimed, []burst.Event{{Seq: 2, Data: []byte("dup")}, {Seq: 4, Data: []byte("dddd")}}, quota); err != nil {
		t.Fatal(err)
	}
	// Over the quota the whole batch is refused.
	if err := st.AppendEvents(ctx, claimed, []burst.Event{{Seq: 5, Data: []byte("e")}}, quota); !errors.Is(err, burst.ErrEventQuota) {
		t.Fatalf("cota: %v", err)
	}
	if j, _, _ := st.GetJob(ctx, job.ID); j.EventsCount != 4 || j.EventsBytes != 16 {
		t.Fatalf("contadores = %d eventos, %d bytes", j.EventsCount, j.EventsBytes)
	}
	got, err := st.Events(ctx, job.ID, 1, 2, 1<<20)
	if err != nil || len(got) != 2 || got[0].Seq != 2 || string(got[0].Data) != "bbbb" || got[1].Seq != 3 {
		t.Fatalf("events = %+v %v", got, err)
	}
	if got, _ := st.Events(ctx, job.ID, 0, 100, 6); len(got) != 1 {
		t.Fatalf("orçamento de bytes: %d eventos (o primeiro sempre vem)", len(got))
	}
	if got, _ := st.Events(ctx, job.ID, 0, 100, 9); len(got) != 2 {
		t.Fatalf("dois cabem em 9 bytes: %d", len(got))
	}

	// Instances: touched from the group (usage), the sign of life, membership.
	if err := st.TouchInstance(ctx, burst.Instance{ID: "i-0abc", Type: "c7g.large", Lifecycle: "spot"}); err != nil {
		t.Fatal(err)
	}
	if err := st.NoteContact(ctx, burst.Contact{InstanceID: "i-0abc", Version: "dev+abc", LastError: "ação falhou"}, false); err != nil {
		t.Fatal(err)
	}
	if seen, _ := st.InstanceSeen(ctx, "i-0abc", time.Minute); !seen {
		t.Fatal("instância vista agora")
	}
	if seen, _ := st.InstanceSeen(ctx, "i-forjada", time.Minute); seen {
		t.Fatal("instância desconhecida")
	}
	if err := st.NoteContact(ctx, burst.Contact{InstanceID: "local-1"}, true); err != nil {
		t.Fatal(err)
	}
	if seen, _ := st.InstanceSeen(ctx, "local-1", time.Minute); !seen {
		t.Fatal("pool manual registra a instância no aviso de vida")
	}
	busy, _ := st.BusyInstances(ctx)
	if len(busy) != 1 || busy[0] != "i-0abc" {
		t.Fatalf("ocupadas = %v", busy)
	}
	st1, err := st.Status(ctx, q)
	if err != nil || st1.Busy != 1 || st1.Live != 2 || st1.Idle != 1 || st1.UsedSeconds < 360 {
		t.Fatalf("status = %+v %v", st1, err)
	}

	// Breaker on the database clock.
	for range 2 {
		if err := st.NoteNoClaim(ctx, 2, time.Hour); err != nil {
			t.Fatal(err)
		}
	}
	if sick, _ := st.Status(ctx, q); !sick.Unhealthy(time.Now()) {
		t.Fatalf("disjuntor: %+v", sick)
	}
	if err := st.NoteClaim(ctx); err != nil {
		t.Fatal(err)
	}
	if healthy, _ := st.Status(ctx, q); healthy.Unhealthy(time.Now()) {
		t.Fatal("um trabalhador apareceu: o disjuntor zera")
	}

	// Finish with a rune-safe reason; then cancel the run's live job.
	if ok, err := st.Finish(ctx, job.ID, hash, burst.StateInterrupted, burst.Sanitize("interrupção Spot")); err != nil || !ok {
		t.Fatalf("finish = %v %v", ok, err)
	}
	again, _ := st.CreateJob(ctx, newJob(org, run), 2)
	if ok, err := st.CancelRun(ctx, run, "cancelada pelo cliente"); err != nil || !ok {
		t.Fatalf("cancel run = %v %v", ok, err)
	}
	if j, _, _ := st.GetJob(ctx, again.ID); j.State != burst.StateCancelled {
		t.Fatalf("estado = %s", j.State)
	}
	if latest, _, _ := st.LatestJob(ctx, run); latest.ID != again.ID {
		t.Fatalf("mais novo = %s", latest.ID)
	}
	if n, err := st.Prune(ctx, time.Hour); err != nil || n != 0 {
		t.Fatalf("prune recente = %d %v", n, err)
	}
}

func TestLeasesFollowTheDatabaseClock(t *testing.T) {
	d := testdb.Get(t)
	ctx := context.Background()
	st := burstpg.New(d.App)
	org, runs := seedRuns(t, d, 1)
	job, _ := st.CreateJob(ctx, newJob(org, runs[0]), 0)
	_, hash, _ := burst.NewToken()
	if _, ok, _ := st.Claim(ctx, "i-0abc", hash, time.Second); !ok {
		t.Fatal("claim")
	}
	time.Sleep(2100 * time.Millisecond)
	// An expired lease is never revived, and the expiry names this job.
	if ok, _ := st.Extend(ctx, job.ID, hash, time.Minute); ok {
		t.Fatal("concessão vencida reviveu")
	}
	lost, err := st.ExpireLeases(ctx)
	if err != nil || len(lost) != 1 || lost[0] != job.ID {
		t.Fatalf("expire = %v %v", lost, err)
	}
	if ok, _ := st.Finish(ctx, job.ID, hash, burst.StateDone, ""); ok {
		t.Fatal("um trabalho perdido não termina como feito")
	}
	// A queued job is abandoned only after its wait.
	queued, _ := st.CreateJob(ctx, newJob(org, runs[0]), 0)
	time.Sleep(1100 * time.Millisecond)
	if ok, err := st.Abandon(ctx, queued.ID, time.Second); err != nil || !ok {
		t.Fatalf("abandon = %v %v", ok, err)
	}
	if ok, _ := st.Cancel(ctx, queued.ID, "x"); ok {
		t.Fatal("abandonado não cancela")
	}
}

// TestCapacityIsReservedAtomically: many dispatches at once never queue more
// jobs than the pool may hold.
func TestCapacityIsReservedAtomically(t *testing.T) {
	d := testdb.Get(t)
	ctx := context.Background()
	st := burstpg.New(d.App)
	org, runs := seedRuns(t, d, 12)
	var wg sync.WaitGroup
	var mu sync.Mutex
	created, full := 0, 0
	for _, run := range runs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := st.CreateJob(ctx, newJob(org, run), 3)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				created++
			case errors.Is(err, burst.ErrPoolFull):
				full++
			default:
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if created != 3 || full != 9 {
		t.Fatalf("criados %d, cheio %d (teto 3)", created, full)
	}
}

// TestConcurrentClaimsTakeEachJobOnce: two workers asking at the same time
// for the one queued job: exactly one gets it.
func TestConcurrentClaimsTakeEachJobOnce(t *testing.T) {
	d := testdb.Get(t)
	ctx := context.Background()
	st := burstpg.New(d.App)
	org, runs := seedRuns(t, d, 1)
	if _, err := st.CreateJob(ctx, newJob(org, runs[0]), 0); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	winners := 0
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, hash, _ := burst.NewToken()
			_, ok, err := st.Claim(ctx, "i-"+string(rune('a'+i)), hash, time.Minute)
			if err != nil {
				t.Error(err)
			}
			if ok {
				mu.Lock()
				winners++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if winners != 1 {
		t.Fatalf("%d trabalhadores pegaram o mesmo trabalho", winners)
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
