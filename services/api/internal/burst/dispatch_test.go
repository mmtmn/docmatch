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

func newDispatcher(st Store, c *clock, onSleep func()) *Dispatcher {
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

func TestDispatchStreamsWhatTheWorkerPosts(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := NewMemStore()
	c := &clock{now: time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)}
	var job Job
	step := 0
	woke := 0
	d := newDispatcher(st, c, func() {
		// The worker side, one move per poll: claim, two events, the terminal one.
		step++
		switch step {
		case 2:
			var ok bool
			job, ok, _ = st.Claim(ctx, "i-1", []byte("h"), c.Now(), c.Now().Add(time.Minute))
			if !ok {
				t.Error("claim")
			}
		case 3:
			_ = st.AppendEvents(ctx, job, []Event{progress(t, 1), progress(t, 2)})
		case 4:
			_ = st.AppendEvents(ctx, job, []Event{progress(t, 2), completed(t, 3)}) // seq 2 again: ignored
			_, _ = st.Finish(ctx, job.ID, []byte("h"), StateDone, "", c.Now())
		}
	})
	d.Wake = func() { woke++ }
	stream, err := d.Run(ctx, remoteRun(uuid.New(), uuid.New()))
	if err != nil || woke != 1 {
		t.Fatalf("run: %v woke=%d", err, woke)
	}
	var seqs []uint64
	for {
		ev, err := stream.Recv()
		if err != nil {
			t.Fatalf("recv: %v (seqs %v)", err, seqs)
		}
		seqs = append(seqs, ev.GetSeq())
		if ev.GetCompleted() != nil {
			break
		}
	}
	if len(seqs) != 3 || seqs[0] != 1 || seqs[2] != 3 {
		t.Fatalf("seqs = %v", seqs)
	}
	got, _, _ := st.GetJob(ctx, job.ID)
	if got.State != StateDone || got.DeadlineSeconds != 3600 {
		t.Fatalf("job = %+v", got)
	}
	// A done job without a terminal event left requeues the run.
	if _, err := stream.Recv(); !errors.Is(err, runs.ErrInterrupted) {
		t.Fatalf("depois do fim: %v", err)
	}
}

func TestDispatchFallsBackWhenNobodyClaims(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := NewMemStore()
	c := &clock{now: time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)}
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
	// Two scale-ups in a row without a worker cool the pool down.
	status, _ := st.Status(ctx, d.Settings.Query(c.Now()))
	if !status.Unhealthy(c.Now()) {
		t.Fatalf("pool deveria estar em espera: %+v", status)
	}
}

func TestDispatchRequeuesALostWorker(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, end := range []string{"lease", StateInterrupted, StateFailed} {
		st := NewMemStore()
		c := &clock{now: time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)}
		d := newDispatcher(st, c, nil)
		run := uuid.New()
		stream, err := d.Run(ctx, remoteRun(uuid.New(), run))
		if err != nil {
			t.Fatal(err)
		}
		job, _, _ := st.Claim(ctx, "i-1", []byte("h"), c.Now(), c.Now().Add(30*time.Second))
		_ = st.AppendEvents(ctx, job, []Event{progress(t, 1)})
		if end != "lease" {
			_, _ = st.Finish(ctx, job.ID, []byte("h"), end, "aviso Spot", c.Now())
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

func TestDispatchReattachesAndCancels(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := NewMemStore()
	c := &clock{now: time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)}
	d := newDispatcher(st, c, nil)
	org, run := uuid.New(), uuid.New()
	first, err := d.Run(ctx, remoteRun(org, run))
	if err != nil {
		t.Fatal(err)
	}
	// A restarted main worker finds the live job instead of queuing another.
	second, err := d.Run(ctx, remoteRun(org, run))
	if err != nil {
		t.Fatal(err)
	}
	if first.(*jobStream).job.ID != second.(*jobStream).job.ID {
		t.Fatal("deveria reconectar ao mesmo trabalho")
	}
	// The run is cancelled while it waits for a worker.
	rr := remoteRun(org, run)
	rr.Cancelled = func(context.Context) bool { return true }
	third, _ := d.Run(ctx, rr)
	if _, err := third.Recv(); !errors.Is(err, runs.ErrRemoteCancelled) {
		t.Fatalf("cancelada: %v", err)
	}
	if _, ok, _ := st.ActiveJob(ctx, run); ok {
		t.Fatal("cancelar deveria derrubar o trabalho")
	}
	// Stop drops a job the main worker gives up on (deadline, failure).
	run2 := uuid.New()
	s4, _ := d.Run(ctx, remoteRun(org, run2))
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

func (brokenEvents) Events(context.Context, uuid.UUID, int64, int) ([]Event, error) {
	return nil, errors.New("conexão recusada")
}

func TestDispatchStoreFailuresAreTransient(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c := &clock{now: time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)}
	if _, err := newDispatcher(brokenStore{NewMemStore()}, c, nil).Run(ctx, remoteRun(uuid.New(), uuid.New())); !harness.IsUnavailable(err) {
		t.Fatalf("run: %v", err)
	}
	stream, err := newDispatcher(brokenEvents{NewMemStore()}, c, nil).Run(ctx, remoteRun(uuid.New(), uuid.New()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Recv(); !harness.IsUnavailable(err) {
		t.Fatalf("recv: %v", err)
	}
	// A job that vanished requeues the run.
	st := NewMemStore()
	d := newDispatcher(st, c, nil)
	s, _ := d.Run(ctx, remoteRun(uuid.New(), uuid.New()))
	_, _ = st.Cancel(ctx, s.(*jobStream).job.ID, "x", c.Now())
	_, _ = st.Prune(ctx, c.Now().Add(time.Hour))
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
