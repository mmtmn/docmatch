package jobs

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/mmtmn/docmatch/services/api/internal/email"
	"github.com/mmtmn/docmatch/services/api/internal/jobs/jobargs"
	"github.com/mmtmn/docmatch/services/api/internal/runs"
	"github.com/mmtmn/docmatch/services/api/internal/runs/pgstore"
)

type fakeOutbox struct {
	msgs    []pgstore.OutboxMessage
	sent    []uuid.UUID
	retried map[uuid.UUID]time.Time
	err     error
	skipped []string
}

func (f *fakeOutbox) ClaimOutbox(_ context.Context, _ int, skip []string) ([]pgstore.OutboxMessage, error) {
	f.skipped = skip
	return f.msgs, f.err
}

func (f *fakeOutbox) MarkOutboxSent(_ context.Context, id uuid.UUID) error {
	f.sent = append(f.sent, id)
	return nil
}

func (f *fakeOutbox) RetryOutboxLater(_ context.Context, id uuid.UUID, at time.Time, _ string) error {
	if f.retried == nil {
		f.retried = map[uuid.UUID]time.Time{}
	}
	f.retried[id] = at
	return nil
}

type failingDeliverer struct {
	fail map[string]bool
}

func (d failingDeliverer) Deliver(_ context.Context, m pgstore.OutboxMessage) error {
	if d.fail[m.Kind] {
		return errors.New("endpoint fora")
	}
	return nil
}

func TestPipelineWorkersWithoutPipeline(t *testing.T) {
	t.Parallel()
	log, _ := logger()
	d := Deps{Logger: log, Now: time.Now}
	if err := (&ScanWorker{deps: d}).Work(context.Background(), &river.Job[jobargs.ScanArgs]{}); !errors.Is(err, ErrPipelineNotWired) {
		t.Fatalf("scan = %v", err)
	}
	if err := (&RunWorker{deps: d}).Work(context.Background(), &river.Job[jobargs.RunArgs]{JobRow: &rivertype.JobRow{Attempt: 1, MaxAttempts: 3}}); !errors.Is(err, ErrPipelineNotWired) {
		t.Fatalf("run = %v", err)
	}
	if err := (&LearnWorker{deps: d}).Work(context.Background(), &river.Job[jobargs.LearnArgs]{}); !errors.Is(err, ErrPipelineNotWired) {
		t.Fatalf("learn = %v", err)
	}
	if err := (&OutboxWorker{deps: d}).Work(context.Background(), &river.Job[jobargs.OutboxArgs]{}); err != nil {
		t.Fatalf("outbox sem pipeline deveria ser no-op: %v", err)
	}
	if (&ScanWorker{}).Timeout(nil) != ScanTimeout || (&LearnWorker{}).Timeout(nil) != LearnTimeout || (&RunWorker{deps: d}).Timeout(nil) != runs.DefaultRunDeadline+RunTimeoutPad {
		t.Fatal("timeouts")
	}
	custom := Deps{Pipeline: &Pipeline{RunDeadline: time.Hour}}
	if (&RunWorker{deps: custom}).Timeout(nil) != time.Hour+RunTimeoutPad {
		t.Fatal("timeout configurado")
	}
	if (&OutboxWorker{}).Timeout(nil) <= 0 {
		t.Fatal("timeout do outbox")
	}
}

func TestOutboxWorker(t *testing.T) {
	t.Parallel()
	log, buf := logger()
	ok, bad, dead := uuid.New(), uuid.New(), uuid.New()
	box := &fakeOutbox{msgs: []pgstore.OutboxMessage{
		{ID: ok, Kind: "run.completed", Attempts: 1},
		{ID: bad, Kind: "run.failed", Attempts: 3},
		{ID: dead, Kind: "run.failed", Attempts: OutboxMaxAttempts + 1},
	}}
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	d := Deps{Logger: log, Now: func() time.Time { return now }, Pipeline: &Pipeline{Outbox: box, Deliverer: failingDeliverer{fail: map[string]bool{"run.failed": true}}}}
	if err := (&OutboxWorker{deps: d}).Work(context.Background(), &river.Job[jobargs.OutboxArgs]{}); err != nil {
		t.Fatal(err)
	}
	if len(box.sent) != 1 || box.sent[0] != ok {
		t.Fatalf("enviadas = %v", box.sent)
	}
	if at, retried := box.retried[bad]; !retried || at.Sub(now) != 9*OutboxInterval {
		t.Fatalf("reagendamento = %v %v", at, retried)
	}
	if _, retried := box.retried[dead]; retried {
		t.Fatal("mensagem abandonada não deveria ser reagendada")
	}
	if buf.Len() == 0 {
		t.Fatal("drenagem deveria ser registrada")
	}
	// Default deliverer only logs; claim errors propagate.
	d.Pipeline.Deliverer = nil
	box.retried = nil
	if err := (&OutboxWorker{deps: d}).Work(context.Background(), &river.Job[jobargs.OutboxArgs]{}); err != nil || len(box.sent) != 3 {
		t.Fatalf("entregador padrão = %v (%d enviadas)", err, len(box.sent))
	}
	box.err = errors.New("banco fora")
	if err := (&OutboxWorker{deps: d}).Work(context.Background(), &river.Job[jobargs.OutboxArgs]{}); err == nil {
		t.Fatal("erro ao reivindicar deveria falhar o job")
	}
	if err := (LogDeliverer{}).Deliver(context.Background(), pgstore.OutboxMessage{Kind: "x"}); err != nil {
		t.Fatal(err)
	}
}

func TestLazyEnqueuerAndPeriodic(t *testing.T) {
	t.Parallel()
	lazy := &jobargs.LazyEnqueuer{}
	if err := lazy.Enqueue(context.Background(), jobargs.OutboxArgs{}); err == nil {
		t.Fatal("sem alvo deveria falhar")
	}
	if err := lazy.EnqueueTx(context.Background(), nil, jobargs.OutboxArgs{}); err == nil {
		t.Fatal("sem alvo deveria falhar")
	}
	rec := &jobargs.Recorder{}
	lazy.Set(rec)
	if err := lazy.Enqueue(context.Background(), jobargs.ScanArgs{}); err != nil || len(rec.Kinds()) != 1 {
		t.Fatal("alvo ligado")
	}
	if err := lazy.EnqueueTx(context.Background(), nil, jobargs.RunArgs{}); err != nil || rec.Kinds()[1] != jobargs.KindRun {
		t.Fatal("alvo ligado (tx)")
	}
	if (jobargs.RiverEnqueuer{}).Enqueue(context.Background(), jobargs.OutboxArgs{}) == nil || (jobargs.RiverEnqueuer{}).EnqueueTx(context.Background(), nil, jobargs.OutboxArgs{}) == nil {
		t.Fatal("River sem cliente deveria falhar")
	}
	rec.Err = errors.New("x")
	if rec.Enqueue(context.Background(), jobargs.OutboxArgs{}) == nil {
		t.Fatal("Recorder.Err")
	}
	for _, args := range []river.JobArgs{jobargs.ScanArgs{}, jobargs.RunArgs{}, jobargs.LearnArgs{}, jobargs.OutboxArgs{}} {
		opts := args.(river.JobArgsWithInsertOpts).InsertOpts()
		if opts.MaxAttempts == 0 {
			t.Fatalf("%s sem MaxAttempts", args.Kind())
		}
	}
	found := false
	for _, pj := range PeriodicJobs() {
		if pj != nil {
			found = true
		}
	}
	if !found || len(PeriodicJobs()) != 4 {
		t.Fatal("agenda periódica")
	}
	workers := NewWorkers(Deps{})
	if workers == nil {
		t.Fatal("workers")
	}
}

func TestOutboxSkipsKindsOwnedByDedicatedWorkers(t *testing.T) {
	if !slices.Contains(jobargs.DedicatedOutboxKinds, email.OutboxKind) {
		t.Fatalf("o job genérico precisa pular %q (dono: e-mail)", email.OutboxKind)
	}
	f := &fakeOutbox{}
	w := &OutboxWorker{deps: Deps{Pipeline: &Pipeline{Outbox: f}}}
	w.deps.defaults()
	if err := w.Work(context.Background(), &river.Job[jobargs.OutboxArgs]{}); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(f.skipped, email.OutboxKind) {
		t.Fatalf("ClaimOutbox recebeu skip=%v", f.skipped)
	}
}
