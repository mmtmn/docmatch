package burst

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"

	"github.com/mmtmn/docmatch/services/api/internal/harness"
	pb "github.com/mmtmn/docmatch/services/api/internal/harness/pb"
	"github.com/mmtmn/docmatch/services/api/internal/runs"
)

// clock is a fake clock that the dispatcher's sleep advances.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func newClock() *clock { return &clock{now: time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)} }

// newDispatcher shares the clock with the store when st is a *MemStore (the
// database clock and the worker's agree, as on one box).
func newDispatcher(st Store, c *clock, onSleep func()) *Dispatcher {
	if m, ok := st.(*MemStore); ok && m.Now == nil {
		m.Now = c.Now
	}
	s := testSettings()
	s.Poll = time.Second
	return &Dispatcher{Store: st, Settings: s, Now: c.Now, Sleep: func(ctx context.Context, d time.Duration) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		c.Advance(d)
		if onSleep != nil {
			onSleep()
		}
		return nil
	}}
}

func event(t *testing.T, seq uint64, ev *pb.RunEvent) Event {
	t.Helper()
	ev.Seq, ev.RunId = seq, "run_x"
	raw, err := proto.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	return Event{Seq: int64(seq), Data: raw} //nolint:gosec // small
}

func progress(t *testing.T, seq uint64) Event {
	return event(t, seq, &pb.RunEvent{Event: &pb.RunEvent_Progress{Progress: &pb.Progress{Done: seq, Total: 10}}})
}

func completed(t *testing.T, seq uint64) Event {
	return event(t, seq, &pb.RunEvent{Event: &pb.RunEvent_Completed{Completed: &pb.Completed{}}})
}

func remoteRun(org, run uuid.UUID) runs.RemoteRun {
	return runs.RemoteRun{OrgID: org, RunID: run, Request: &pb.RunRequest{RunId: "run_x"}, Deadline: time.Hour, Wait: 10 * time.Minute}
}

var noQuota = EventQuota{}

func drain(t *testing.T, s runs.RemoteStream) ([]uint64, error) {
	t.Helper()
	var seqs []uint64
	for {
		ev, err := s.Recv()
		if err != nil {
			return seqs, err
		}
		seqs = append(seqs, ev.GetSeq())
		if ev.GetCompleted() != nil {
			return seqs, nil
		}
	}
}

func TestDispatchStreamsWhatTheWorkerPosts(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := NewMemStore()
	c := newClock()
	var job Job
	step, woke := 0, 0
	d := newDispatcher(st, c, func() {
		// The worker side, one move per poll: claim, two events, the terminal one.
		step++
		switch step {
		case 2:
			var ok bool
			job, ok, _ = st.Claim(ctx, "i-1", []byte("h"), time.Minute)
			if !ok {
				t.Error("claim")
			}
		case 3:
			_ = st.AppendEvents(ctx, job, []Event{progress(t, 1), progress(t, 2)}, noQuota)
		case 4:
			_ = st.AppendEvents(ctx, job, []Event{progress(t, 2), completed(t, 3)}, noQuota) // seq 2 again: ignored
			_, _ = st.Finish(ctx, job.ID, []byte("h"), StateDone, "")
		}
	})
	d.Wake = func() { woke++ }
	stream, err := d.Run(ctx, remoteRun(uuid.New(), uuid.New()))
	if err != nil || woke != 1 {
		t.Fatalf("run: %v woke=%d", err, woke)
	}
	seqs, err := drain(t, stream)
	if err != nil || len(seqs) != 3 || seqs[0] != 1 || seqs[2] != 3 {
		t.Fatalf("seqs = %v %v", seqs, err)
	}
	got, _, _ := st.GetJob(ctx, job.ID)
	if got.State != StateDone || got.DeadlineSeconds != 3600 || got.EventsCount != 3 {
		t.Fatalf("job = %+v", got)
	}
	// A done job without a terminal event left requeues the run.
	if _, err := stream.Recv(); !errors.Is(err, runs.ErrInterrupted) {
		t.Fatalf("depois do fim: %v", err)
	}
}

func TestDispatchReservesThePlaceAtomically(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := NewMemStore()
	d := newDispatcher(st, newClock(), nil)
	for range 2 {
		if _, err := d.Run(ctx, remoteRun(uuid.New(), uuid.New())); err != nil {
			t.Fatal(err)
		}
	}
	// Two places (MaxInstances): the third run is told the pool is full,
	// whatever a cached status said.
	if _, err := d.Run(ctx, remoteRun(uuid.New(), uuid.New())); !errors.Is(err, runs.ErrPoolFull) {
		t.Fatalf("terceiro: %v", err)
	}
}

func TestDispatchFallsBackWhenNobodyClaims(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := NewMemStore()
	c := newClock()
	d := newDispatcher(st, c, nil)
	for i := range DefaultNoClaimThreshold {
		run := uuid.New()
		stream, err := d.Run(ctx, remoteRun(uuid.New(), run))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := stream.Recv(); !errors.Is(err, runs.ErrNoCapacity) {
			t.Fatalf("%d: err = %v", i, err)
		}
		if _, ok, _ := st.ActiveJob(ctx, run); ok {
			t.Fatal("o trabalho abandonado não pode ficar vivo")
		}
	}
	// Two jobs in a row nobody claimed while the pool had room: cool down.
	status, _ := st.Status(ctx, d.Settings.Query(c.Now()))
	if !status.Unhealthy(c.Now()) {
		t.Fatalf("pool deveria estar em espera: %+v", status)
	}
}

func TestDispatchDoesNotBlameAPoolWithoutRoom(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := NewMemStore()
	c := newClock()
	d := newDispatcher(st, c, nil)
	// The month's hours are used up: the scaler adds nobody on purpose.
	_ = st.TouchInstance(ctx, Instance{ID: "i-old"})
	c.Advance(11 * time.Hour)
	_ = st.TouchInstance(ctx, Instance{ID: "i-old"})
	for range DefaultNoClaimThreshold + 1 {
		stream, err := d.Run(ctx, remoteRun(uuid.New(), uuid.New()))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := stream.Recv(); !errors.Is(err, runs.ErrNoCapacity) {
			t.Fatalf("err = %v", err)
		}
	}
	if status, _ := st.Status(ctx, d.Settings.Query(c.Now())); status.Unhealthy(c.Now()) {
		t.Fatal("teto de propósito não é falha do pool: o disjuntor não pode abrir")
	}
}

func TestDispatchRequeuesALostWorker(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, end := range []string{"lease", StateInterrupted, StateFailed} {
		st := NewMemStore()
		c := newClock()
		d := newDispatcher(st, c, nil)
		stream, err := d.Run(ctx, remoteRun(uuid.New(), uuid.New()))
		if err != nil {
			t.Fatal(err)
		}
		job, _, _ := st.Claim(ctx, "i-1", []byte("h"), 30*time.Second)
		_ = st.AppendEvents(ctx, job, []Event{progress(t, 1)}, noQuota)
		if end != "lease" {
			_, _ = st.Finish(ctx, job.ID, []byte("h"), end, "aviso Spot")
		}
		if ev, err := stream.Recv(); err != nil || ev.GetSeq() != 1 {
			t.Fatalf("%s: primeiro evento: %v %v", end, ev, err)
		}
		if _, err := stream.Recv(); !errors.Is(err, runs.ErrInterrupted) {
			t.Fatalf("%s: err = %v", end, err)
		}
		got, _, _ := st.GetJob(ctx, job.ID)
		if end == "lease" && got.State != StateLost {
			t.Fatalf("concessão vencida deveria marcar lost: %s", got.State)
		}
	}
}

// TestDispatchLeaseIsTheDatabasesCall: the worker's clock says the lease is
// over, the database's does not (a renewal it accepted): the stream goes on,
// and ends only when the database expires this very job.
func TestDispatchLeaseIsTheDatabasesCall(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dbClock, workerClock := newClock(), newClock()
	st := NewMemStore()
	st.Now = dbClock.Now
	polls := 0
	d := newDispatcher(st, workerClock, func() {
		polls++
		if polls == 30 {
			dbClock.Advance(2 * time.Minute) // only now does the database see the lease over
		}
	})
	stream, err := d.Run(ctx, remoteRun(uuid.New(), uuid.New()))
	if err != nil {
		t.Fatal(err)
	}
	job, _, _ := st.Claim(ctx, "i-1", []byte("h"), time.Minute)
	if _, err := stream.Recv(); !errors.Is(err, runs.ErrInterrupted) {
		t.Fatalf("err = %v", err)
	}
	if polls < 30 {
		t.Fatalf("encerrou com %d leituras, antes de o banco vencer a concessão", polls)
	}
	if got, _, _ := st.GetJob(ctx, job.ID); got.State != StateLost {
		t.Fatalf("estado = %s", got.State)
	}
	// An expired lease is not renewed.
	_, _ = st.CreateJob(ctx, Job{ID: uuid.New(), RunID: uuid.New()}, 0)
	claimed, _, _ := st.Claim(ctx, "i-2", []byte("k"), time.Second)
	dbClock.Advance(2 * time.Second)
	if ok, _ := st.Extend(ctx, claimed.ID, []byte("k"), time.Minute); ok {
		t.Fatal("concessão vencida não pode reviver")
	}
}

func TestDispatchAttachesBeforeAnyNewDecision(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := NewMemStore()
	c := newClock()
	d := newDispatcher(st, c, nil)
	org, run := uuid.New(), uuid.New()
	if _, ok, err := d.Attach(ctx, remoteRun(org, run), 0); ok || err != nil {
		t.Fatalf("sem trabalho: %v %v", ok, err)
	}
	first, err := d.Run(ctx, remoteRun(org, run))
	if err != nil {
		t.Fatal(err)
	}
	// A restarted main worker finds the live job.
	again, ok, err := d.Attach(ctx, remoteRun(org, run), 0)
	if err != nil || !ok || again.(*jobStream).job.ID != first.(*jobStream).job.ID {
		t.Fatalf("reconexão: %v %v", ok, err)
	}
	// The job ends while nobody reads it: events 1..3, terminal included.
	job, _, _ := st.Claim(ctx, "i-1", []byte("h"), time.Minute)
	_ = st.AppendEvents(ctx, job, []Event{progress(t, 1), progress(t, 2), completed(t, 3)}, noQuota)
	_, _ = st.Finish(ctx, job.ID, []byte("h"), StateDone, "")
	// The run had applied up to seq 1: it streams 2 and 3 instead of redoing the work.
	done, ok, err := d.Attach(ctx, remoteRun(org, run), 1)
	if err != nil || !ok {
		t.Fatalf("trabalho terminado com eventos não lidos: %v %v", ok, err)
	}
	if seqs, err := drain(t, done); err != nil || len(seqs) != 2 || seqs[0] != 2 {
		t.Fatalf("eventos = %v %v", seqs, err)
	}
	// Everything applied: nothing to attach, a new decision is due.
	if _, ok, _ := d.Attach(ctx, remoteRun(org, run), 3); ok {
		t.Fatal("nada a ler: não deveria reconectar")
	}
	// A job dropped on purpose (cancelled) is never replayed.
	run2 := uuid.New()
	s2, _ := d.Run(ctx, remoteRun(org, run2))
	j2 := s2.(*jobStream).job
	_ = st.AppendEvents(ctx, j2, []Event{progress(t, 1)}, noQuota)
	_, _ = st.Cancel(ctx, j2.ID, "prazo esgotado")
	if _, ok, _ := d.Attach(ctx, remoteRun(org, run2), 0); ok {
		t.Fatal("trabalho cancelado não volta")
	}
}

func TestDispatchDropAndCancel(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := NewMemStore()
	c := newClock()
	d := newDispatcher(st, c, nil)
	org, run := uuid.New(), uuid.New()
	s, _ := d.Run(ctx, remoteRun(org, run))
	if err := d.Drop(ctx, run, "cancelada pelo cliente"); err != nil {
		t.Fatal(err)
	}
	if j, _, _ := st.GetJob(ctx, s.(*jobStream).job.ID); j.State != StateCancelled || j.Reason != "cancelada pelo cliente" {
		t.Fatalf("drop: %+v", j)
	}
	if err := d.Drop(ctx, uuid.New(), "nada"); err != nil {
		t.Fatal("sem trabalho vivo: no-op")
	}
	// The run is cancelled while it waits for a worker.
	rr := remoteRun(org, uuid.New())
	rr.Cancelled = func(context.Context) bool { return true }
	waiting, _ := d.Run(ctx, rr)
	if _, err := waiting.Recv(); !errors.Is(err, runs.ErrRemoteCancelled) {
		t.Fatalf("cancelada: %v", err)
	}
	// Stop drops a job the main worker gives up on (deadline, failure).
	s4, _ := d.Run(ctx, remoteRun(org, uuid.New()))
	s4.Stop(ctx, "prazo esgotado")
	if j, _, _ := st.GetJob(ctx, s4.(*jobStream).job.ID); j.State != StateCancelled || j.Reason != "prazo esgotado" {
		t.Fatalf("stop: %+v", j)
	}
	// The run's context bounds the stream (deadline of the run).
	cctx, cancel := context.WithCancel(ctx)
	s5, _ := d.Run(cctx, remoteRun(org, uuid.New()))
	cancel()
	if _, err := s5.Recv(); err == nil {
		t.Fatal("contexto cancelado deveria encerrar o fluxo")
	}
}

type brokenStore struct{ *MemStore }

func (brokenStore) ActiveJob(context.Context, uuid.UUID) (Job, bool, error) {
	return Job{}, false, errors.New("conexão recusada")
}

type brokenEvents struct{ *MemStore }

func (brokenEvents) Events(context.Context, uuid.UUID, int64, int, int64) ([]Event, error) {
	return nil, errors.New("conexão recusada")
}

// racyStore loses the creation race and then finds no live job either.
type racyStore struct{ *MemStore }

func (racyStore) CreateJob(context.Context, Job, int) (Job, error) { return Job{}, ErrJobActive }

func TestDispatchStoreFailuresAreTransient(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c := newClock()
	if _, _, err := newDispatcher(brokenStore{NewMemStore()}, c, nil).Attach(ctx, remoteRun(uuid.New(), uuid.New()), 0); !harness.IsUnavailable(err) {
		t.Fatalf("attach: %v", err)
	}
	stream, err := newDispatcher(brokenEvents{NewMemStore()}, c, nil).Run(ctx, remoteRun(uuid.New(), uuid.New()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Recv(); !harness.IsUnavailable(err) {
		t.Fatalf("recv: %v", err)
	}
	// Lost the creation race and the winner already ended: retry, never a
	// stream of a nil job.
	if _, err := newDispatcher(racyStore{NewMemStore()}, c, nil).Run(ctx, remoteRun(uuid.New(), uuid.New())); !harness.IsUnavailable(err) || !errors.Is(err, ErrJobActive) {
		t.Fatalf("corrida: %v", err)
	}
	// A job that vanished requeues the run.
	st := NewMemStore()
	d := newDispatcher(st, c, nil)
	s, _ := d.Run(ctx, remoteRun(uuid.New(), uuid.New()))
	_, _ = st.Cancel(ctx, s.(*jobStream).job.ID, "x")
	c.Advance(8 * 24 * time.Hour)
	_, _ = st.Prune(ctx, time.Hour)
	if _, err := s.Recv(); !errors.Is(err, runs.ErrInterrupted) {
		t.Fatalf("sumiu: %v", err)
	}
	if (&Dispatcher{}).now().IsZero() || (&Dispatcher{}).log() == nil {
		t.Fatal("padrões")
	}
	if err := (&Dispatcher{}).sleep(ctx, time.Millisecond); err != nil {
		t.Fatal(err)
	}
}

func TestEventsQuotaAndByteBudget(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := NewMemStore()
	job, _ := st.CreateJob(ctx, Job{ID: uuid.New(), RunID: uuid.New()}, 0)
	big := make([]byte, 600)
	evs := []Event{{Seq: 1, Data: big}, {Seq: 2, Data: big}, {Seq: 3, Data: big}}
	if err := st.AppendEvents(ctx, job, evs, EventQuota{MaxEvents: 2}); !errors.Is(err, ErrEventQuota) {
		t.Fatalf("cota de eventos: %v", err)
	}
	if err := st.AppendEvents(ctx, job, evs, EventQuota{MaxBytes: 1000}); !errors.Is(err, ErrEventQuota) {
		t.Fatalf("cota de bytes: %v", err)
	}
	if err := st.AppendEvents(ctx, job, evs, EventQuota{MaxEvents: 10, MaxBytes: 10_000}); err != nil {
		t.Fatal(err)
	}
	// The reader stops at its byte budget, but always takes the first row.
	got, _ := st.Events(ctx, job.ID, 0, 10, 1000)
	if len(got) != 1 {
		t.Fatalf("orçamento de bytes: %d eventos", len(got))
	}
	got, _ = st.Events(ctx, job.ID, 0, 10, 1300)
	if len(got) != 2 {
		t.Fatalf("dois cabem: %d", len(got))
	}
}
