package jobs

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/riverqueue/river"

	"github.com/mmtmn/docmatch/services/api/internal/audit"
	"github.com/mmtmn/docmatch/services/api/internal/credits"
	"github.com/mmtmn/docmatch/services/api/internal/db"
)

type fakePinger struct{ err error }

func (f fakePinger) Ping(context.Context) error { return f.err }

type fakeExpirer struct {
	results map[uuid.UUID]credits.Expiry
	errs    map[uuid.UUID]error
	calls   int
}

func (f *fakeExpirer) ExpireGrants(_ context.Context, org uuid.UUID, _ time.Time) (credits.Expiry, error) {
	f.calls++
	if err := f.errs[org]; err != nil {
		return credits.Expiry{}, err
	}
	return f.results[org], nil
}

func logger() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(slog.NewJSONHandler(&buf, nil)), &buf
}

func TestHeartbeat(t *testing.T) {
	t.Parallel()
	log, buf := logger()
	w := &HeartbeatWorker{deps: Deps{Logger: log, DB: fakePinger{}, Hostname: "h1", Now: time.Now}, started: time.Now()}
	if err := w.Work(context.Background(), &river.Job[HeartbeatArgs]{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "batimento") || !strings.Contains(buf.String(), `"host":"h1"`) {
		t.Fatalf("log inesperado: %s", buf.String())
	}
	down := &HeartbeatWorker{deps: Deps{Logger: log, DB: fakePinger{err: errors.New("fora")}, Now: time.Now}}
	if err := down.Work(context.Background(), &river.Job[HeartbeatArgs]{}); err == nil || !strings.Contains(err.Error(), "banco") {
		t.Fatalf("banco fora deveria falhar o job: %v", err)
	}
	noDB := &HeartbeatWorker{deps: Deps{Logger: log, Now: time.Now}}
	if err := noDB.Work(context.Background(), &river.Job[HeartbeatArgs]{}); err != nil {
		t.Fatal("sem pinger o batimento só registra")
	}
	if w.Timeout(nil) != 30*time.Second || (HeartbeatArgs{}).Kind() != KindHeartbeat {
		t.Fatal("timeout/kind")
	}
	if opts := (HeartbeatArgs{}).InsertOpts(); opts.UniqueOpts.ByPeriod != HeartbeatInterval {
		t.Fatalf("InsertOpts = %+v", opts)
	}
}

func TestPartitionWorker(t *testing.T) {
	t.Parallel()
	log, buf := logger()
	store := audit.NewMemoryStore()
	rec := audit.New(store)
	var gotAhead, gotBack int
	part := func(_ context.Context, ahead, back int) ([]db.Partition, error) {
		gotAhead, gotBack = ahead, back
		return []db.Partition{{Name: "credit_ledger_y2026m12", Created: true}, {Name: "credit_ledger_y2026m11", Created: false}}, nil
	}
	w := &PartitionWorker{deps: Deps{Logger: log, Partitioner: part, Audit: rec, Hostname: "h1", Now: time.Now}}
	if err := w.Work(context.Background(), &river.Job[PartitionMaintenanceArgs]{Args: PartitionMaintenanceArgs{}}); err != nil {
		t.Fatal(err)
	}
	if gotAhead != PartitionMonthsAhead || gotBack != 0 {
		t.Fatalf("padrões = %d/%d", gotAhead, gotBack)
	}
	if !strings.Contains(buf.String(), `"created":1`) {
		t.Fatalf("log: %s", buf.String())
	}
	entries, _ := store.Range(context.Background(), nil, 1, 10)
	if len(entries) != 1 || entries[0].Action != "partitions.ensure" || !strings.Contains(entries[0].MetaText, "credit_ledger_y2026m12") || entries[0].Actor.ID != "worker@h1" {
		t.Fatalf("auditoria: %+v", entries)
	}
	// Explicit args and negative months_back are honoured/clamped; nothing created means no audit row.
	part2 := func(_ context.Context, ahead, back int) ([]db.Partition, error) {
		gotAhead, gotBack = ahead, back
		return nil, nil
	}
	w2 := &PartitionWorker{deps: Deps{Logger: log, Partitioner: part2, Audit: rec, Now: time.Now}}
	if err := w2.Work(context.Background(), &river.Job[PartitionMaintenanceArgs]{Args: PartitionMaintenanceArgs{MonthsAhead: 5, MonthsBack: -3}}); err != nil {
		t.Fatal(err)
	}
	if gotAhead != 5 || gotBack != 0 || store.Len() != 1 {
		t.Fatalf("args = %d/%d, auditoria = %d", gotAhead, gotBack, store.Len())
	}
	failing := &PartitionWorker{deps: Deps{Logger: log, Partitioner: func(context.Context, int, int) ([]db.Partition, error) {
		return nil, errors.New("ddl falhou")
	}, Now: time.Now}}
	if err := failing.Work(context.Background(), &river.Job[PartitionMaintenanceArgs]{}); err == nil || !strings.Contains(err.Error(), "ddl falhou") {
		t.Fatalf("erro do banco deveria propagar: %v", err)
	}
	none := &PartitionWorker{deps: Deps{Logger: log, Now: time.Now}}
	if err := none.Work(context.Background(), &river.Job[PartitionMaintenanceArgs]{}); err == nil {
		t.Fatal("sem partitioner deveria falhar")
	}
	brokenAudit := &PartitionWorker{deps: Deps{Logger: log, Partitioner: part, Audit: audit.New(nil), Now: time.Now}}
	if err := brokenAudit.Work(context.Background(), &river.Job[PartitionMaintenanceArgs]{}); !errors.Is(err, audit.ErrNilStore) {
		t.Fatalf("falha de auditoria deveria falhar o job: %v", err)
	}
	if w.Timeout(nil) != 2*time.Minute || (PartitionMaintenanceArgs{}).Kind() != KindPartitionMaintenance || !(PartitionMaintenanceArgs{}).InsertOpts().UniqueOpts.ByArgs {
		t.Fatal("timeout/kind/opts")
	}
}

func TestExpireGrantsWorker(t *testing.T) {
	t.Parallel()
	log, buf := logger()
	store := audit.NewMemoryStore()
	a, b, c := uuid.New(), uuid.New(), uuid.New()
	lister := func(context.Context, time.Time) ([]uuid.UUID, error) { return []uuid.UUID{a, b, c}, nil }
	exp := &fakeExpirer{
		results: map[uuid.UUID]credits.Expiry{a: {Expired: 10, Grants: 1}, b: {Expired: 0}},
		errs:    map[uuid.UUID]error{c: errors.New("bloqueada")},
	}
	w := &ExpireGrantsWorker{deps: Deps{Logger: log, Lister: lister, Expirer: exp, Audit: audit.New(store), Hostname: "h1", Now: time.Now}}
	err := w.Work(context.Background(), &river.Job[ExpireGrantsArgs]{})
	if err == nil || !strings.Contains(err.Error(), "1 organizações falharam") || !strings.Contains(err.Error(), "bloqueada") {
		t.Fatalf("erro parcial esperado: %v", err)
	}
	if exp.calls != 3 {
		t.Fatalf("todas as organizações deveriam ser tentadas: %d", exp.calls)
	}
	if !strings.Contains(buf.String(), `"expired":10`) {
		t.Fatalf("log: %s", buf.String())
	}
	entries, _ := store.Range(context.Background(), &a, 1, 10)
	if len(entries) != 1 || entries[0].Action != "credits.expire" || !strings.Contains(entries[0].MetaText, `"expired":10`) {
		t.Fatalf("auditoria da org A: %+v", entries)
	}
	if entries, _ := store.Range(context.Background(), &b, 1, 10); len(entries) != 0 {
		t.Fatal("org sem expiração não gera auditoria")
	}
	ok := &ExpireGrantsWorker{deps: Deps{Logger: log, Lister: lister, Expirer: &fakeExpirer{}, Now: time.Now}}
	if err := ok.Work(context.Background(), &river.Job[ExpireGrantsArgs]{}); err != nil {
		t.Fatalf("sem falhas deveria passar: %v", err)
	}
	listFail := &ExpireGrantsWorker{deps: Deps{Logger: log, Lister: func(context.Context, time.Time) ([]uuid.UUID, error) { return nil, errors.New("lista falhou") }, Expirer: exp, Now: time.Now}}
	if err := listFail.Work(context.Background(), &river.Job[ExpireGrantsArgs]{}); err == nil || !strings.Contains(err.Error(), "lista falhou") {
		t.Fatalf("falha ao listar: %v", err)
	}
	none := &ExpireGrantsWorker{deps: Deps{Logger: log, Now: time.Now}}
	if err := none.Work(context.Background(), &river.Job[ExpireGrantsArgs]{}); err == nil {
		t.Fatal("sem dependências deveria falhar")
	}
	auditFail := &ExpireGrantsWorker{deps: Deps{Logger: log, Lister: lister, Expirer: exp, Audit: audit.New(nil), Now: time.Now}}
	if err := auditFail.Work(context.Background(), &river.Job[ExpireGrantsArgs]{}); err == nil || !strings.Contains(err.Error(), "auditoria") {
		t.Fatalf("falha de auditoria deveria ser reportada: %v", err)
	}
	if w.Timeout(nil) != 10*time.Minute || (ExpireGrantsArgs{}).Kind() != KindExpireGrants || (ExpireGrantsArgs{}).InsertOpts().UniqueOpts.ByPeriod != ExpireGrantsInterval {
		t.Fatal("timeout/kind/opts")
	}
}

func TestWiring(t *testing.T) {
	t.Parallel()
	d := Deps{}
	d.defaults()
	if d.Logger == nil || d.Now == nil {
		t.Fatal("defaults")
	}
	if d.Hostname != "" && !strings.HasPrefix(d.systemActor().ID, "worker@") {
		t.Fatal("ator do sistema")
	}
	if NewWorkers(Deps{}) == nil {
		t.Fatal("NewWorkers")
	}
	jobs := PeriodicJobs()
	if len(jobs) != 3 {
		t.Fatalf("esperava 3 jobs periódicos, veio %d", len(jobs))
	}
	if _, err := NewClient(nil, Deps{}, ClientOptions{}); err == nil {
		t.Fatal("pool nulo deveria falhar")
	}
	if PoolPartitioner(nil) == nil {
		t.Fatal("PoolPartitioner")
	}
}
