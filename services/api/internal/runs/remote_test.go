package runs

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/mmtmn/docmatch/services/api/internal/estimate"
	"github.com/mmtmn/docmatch/services/api/internal/harness"
	"github.com/mmtmn/docmatch/services/api/internal/harness/harnesstest"
	pb "github.com/mmtmn/docmatch/services/api/internal/harness/pb"
	"github.com/mmtmn/docmatch/services/api/internal/logx"
	"github.com/mmtmn/docmatch/services/api/internal/runs/apix"
)

// burstPlacer sends every run to the pool (by memory: the speed check of
// time-heavy runs does not apply).
type burstPlacer struct{ reason string }

func (p burstPlacer) Place(context.Context, estimate.PlaceInput) estimate.Placement {
	if p.reason != "" {
		return estimate.Placement{Reason: p.reason}
	}
	return estimate.Placement{Burst: true, Reason: estimate.ReasonMemory}
}

// fakeRemote runs the request on the fixture's harness, as a pool worker
// would, and injects the pool's failures.
type fakeRemote struct {
	h harness.Client
	// mode: "" runs, "nocap" reports no capacity, "cancelled", "error"
	// fails the dispatch, "full" has no place; interruptAfter > 0 loses the
	// worker after N events.
	mode           string
	interruptAfter int
	// live is a job the pool already has for the run (Attach streams it on
	// liveHarness); attachErr fails Attach.
	live        bool
	liveHarness harness.Client
	attachErr   error

	mu       sync.Mutex
	runs     []RemoteRun
	attaches int
	stops    []string
	drops    []string
}

func (r *fakeRemote) Attach(ctx context.Context, rr RemoteRun, afterSeq int64) (RemoteStream, bool, error) {
	r.mu.Lock()
	r.attaches++
	r.mu.Unlock()
	if r.attachErr != nil {
		return nil, false, r.attachErr
	}
	if !r.live {
		return nil, false, nil
	}
	req := &pb.RunRequest{RunId: rr.RunID.String()}
	if rr.Request != nil {
		req = rr.Request
	}
	inner, err := r.liveHarness.Run(ctx, attachRequest(req, rr))
	if err != nil {
		return nil, false, err
	}
	return &fakeRemoteStream{r: r, inner: inner}, true, nil
}

// attachRequest is the request the live job was queued with: the run's id.
func attachRequest(req *pb.RunRequest, rr RemoteRun) *pb.RunRequest {
	if req.GetRunId() == "" || req.GetRunId() == rr.RunID.String() {
		return &pb.RunRequest{RunId: apix.Encode(apix.PrefixRun, rr.RunID), Sources: []*pb.Source{{Id: "upl_1", Uri: "local://t/x"}}}
	}
	return req
}

func (r *fakeRemote) Drop(_ context.Context, runID uuid.UUID, reason string) error {
	r.mu.Lock()
	r.drops = append(r.drops, reason+":"+runID.String())
	r.mu.Unlock()
	return nil
}

func (r *fakeRemote) Run(ctx context.Context, rr RemoteRun) (RemoteStream, error) {
	r.mu.Lock()
	r.runs = append(r.runs, rr)
	r.mu.Unlock()
	switch r.mode {
	case "error":
		return nil, fmt.Errorf("%w: pool fora", harness.ErrUnavailable)
	case "full":
		return nil, ErrPoolFull
	}
	s := &fakeRemoteStream{r: r}
	switch r.mode {
	case "nocap":
		s.err = ErrNoCapacity
	case "cancelled":
		s.err = ErrRemoteCancelled
	default:
		inner, err := r.h.Run(ctx, rr.Request)
		if err != nil {
			return nil, err
		}
		s.inner = inner
	}
	return s, nil
}

type fakeRemoteStream struct {
	r     *fakeRemote
	inner harness.EventStream
	err   error
	n     int
}

func (s *fakeRemoteStream) Recv() (*pb.RunEvent, error) {
	if s.err != nil {
		return nil, s.err
	}
	if s.r.interruptAfter > 0 && s.n >= s.r.interruptAfter {
		return nil, ErrInterrupted
	}
	s.n++
	return s.inner.Recv()
}

func (s *fakeRemoteStream) Stop(_ context.Context, reason string) {
	s.r.mu.Lock()
	s.r.stops = append(s.r.stops, reason)
	s.r.mu.Unlock()
}

// poolFixture is newFixture with routing to a (fake) pool.
func poolFixture(t *testing.T, placer estimate.Placer, remote *fakeRemote) *fixture {
	t.Helper()
	f := newFixture(t, harnesstest.Options{}, nil)
	signer, _ := estimate.NewSigner("chave-de-teste-com-16-chars")
	est, err := estimate.New(estimate.Deps{
		Store: f.estStore, Harness: f.svc.d.Harness, Pricing: repoPricing(t), Throughput: repoThroughput(t), Signer: signer,
		Sources: f.svc.d.Uploads, Logger: logx.Discard(), Now: func() time.Time { return f.now }, Placer: placer,
	})
	if err != nil {
		t.Fatal(err)
	}
	remote.h = f.svc.d.Harness
	d := f.svc.d
	d.Estimates, d.Remote, d.Deadlines, d.RunDeadline = est, remote, Deadlines{Wait: time.Minute}, 0
	f.est = est
	if f.svc, err = New(d); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestRunOnThePool(t *testing.T) {
	t.Parallel()
	remote := &fakeRemote{}
	f := poolFixture(t, burstPlacer{}, remote)
	ctx := context.Background()
	f.grant(t, 1000)
	run, _ := f.create(t)
	if err := f.svc.Execute(ctx, f.org, run.ID, false); err != nil {
		t.Fatal(err)
	}
	done, _ := f.svc.Get(ctx, f.org, run.ID)
	if done.Status != StatusCompleted || done.Summary.FindingsTotal != 23 {
		t.Fatalf("run = %s %+v", done.Status, done.Summary)
	}
	tm := done.Timing()
	if tm.Placement != estimate.PlacementBurst || tm.Reason != estimate.ReasonMemory {
		t.Fatalf("timing = %+v", tm)
	}
	if len(remote.runs) != 1 || remote.runs[0].Wait != time.Minute || remote.runs[0].Deadline != f.svc.Deadlines().For(tm.Budget()) || remote.runs[0].Cancelled(ctx) {
		t.Fatalf("pedido ao pool = %+v", remote.runs)
	}
	if len(f.fake.Runs()) != 1 {
		t.Fatal("o harness (do pool) deveria rodar uma vez")
	}
}

func TestNoCapacityFallsBackToTheMainBox(t *testing.T) {
	t.Parallel()
	for mode, want := range map[string]string{"nocap": estimate.ReasonNoCapacity, "full": estimate.ReasonFull} {
		remote := &fakeRemote{mode: mode}
		f := poolFixture(t, burstPlacer{}, remote)
		ctx := context.Background()
		f.grant(t, 1000)
		run, _ := f.create(t)
		if err := f.svc.Execute(ctx, f.org, run.ID, false); err != nil {
			t.Fatalf("%s: %v", mode, err)
		}
		done, _ := f.svc.Get(ctx, f.org, run.ID)
		tm := done.Timing()
		if done.Status != StatusCompleted || tm.Placement != estimate.PlacementLocal || tm.Reason != want {
			t.Fatalf("%s: run = %s, timing = %+v", mode, done.Status, tm)
		}
		// The fallback gets a fresh deadline: the wait did not eat the run's time.
		dl := f.fake.Runs()[0].GetOptions().GetDeadline().AsTime()
		if time.Until(dl) < f.svc.Deadlines().For(tm.Budget())-time.Minute {
			t.Fatalf("%s: prazo do fallback curto: %v", mode, time.Until(dl))
		}
	}
}

func TestAPoolFailureIsRetriedNeverRunTwice(t *testing.T) {
	t.Parallel()
	for name, remote := range map[string]*fakeRemote{
		"despacho":  {mode: "error"},
		"reconexão": {attachErr: fmt.Errorf("%w: banco fora", harness.ErrUnavailable)},
	} {
		f := poolFixture(t, burstPlacer{}, remote)
		ctx := context.Background()
		f.grant(t, 1000)
		run, _ := f.create(t)
		// Not knowing whether the pool has the run, the main box does not start
		// it: the job is retried.
		if err := f.svc.Execute(ctx, f.org, run.ID, false); !errors.Is(err, ErrRetry) {
			t.Fatalf("%s: err = %v", name, err)
		}
		if n := len(f.fake.Runs()); n != 0 {
			t.Fatalf("%s: a máquina principal rodou %d vez(es)", name, n)
		}
	}
}

// TestALiveJobWinsOverANewPlacement: the River job is retried (deploy, a
// database blip) while the pool still runs the run, and the pool would now
// say "main box" (full, cap, unhealthy): the run follows the live job, and
// the main box never starts a second execution.
func TestALiveJobWinsOverANewPlacement(t *testing.T) {
	t.Parallel()
	pool, poolFake := freshHarness(t)
	remote := &fakeRemote{live: true, liveHarness: pool}
	f := poolFixture(t, burstPlacer{reason: estimate.ReasonFull}, remote)
	ctx := context.Background()
	f.grant(t, 1000)
	run, _ := f.create(t)
	if err := f.svc.Execute(ctx, f.org, run.ID, false); err != nil {
		t.Fatal(err)
	}
	done, _ := f.svc.Get(ctx, f.org, run.ID)
	if done.Status != StatusCompleted || done.Summary.FindingsTotal != 23 {
		t.Fatalf("run = %s", done.Status)
	}
	if n := len(f.fake.Runs()); n != 0 {
		t.Fatalf("a máquina principal rodou %d vez(es) com o trabalho vivo no pool", n)
	}
	if len(poolFake.Runs()) != 1 || len(remote.runs) != 0 {
		t.Fatalf("o pool deveria seguir com o trabalho que já tinha: %d execuções, %d despachos", len(poolFake.Runs()), len(remote.runs))
	}
	if tm := done.Timing(); tm.Placement != estimate.PlacementBurst || tm.Reason != estimate.ReasonReattached {
		t.Fatalf("timing = %+v", tm)
	}
	// Without a live job, the same placer keeps the run local.
	remote.live = false
	run2, _ := f.create(t)
	if err := f.svc.Execute(ctx, f.org, run2.ID, false); err != nil {
		t.Fatal(err)
	}
	if len(f.fake.Runs()) != 1 || len(remote.runs) != 0 {
		t.Fatalf("sem trabalho vivo: %d locais, %d despachos", len(f.fake.Runs()), len(remote.runs))
	}
}

func TestCancelAndFinishedRunsDropThePoolJob(t *testing.T) {
	t.Parallel()
	remote := &fakeRemote{}
	f := poolFixture(t, burstPlacer{}, remote)
	ctx := context.Background()
	f.grant(t, 1000)
	run, _ := f.create(t)
	if _, err := f.svc.Cancel(ctx, f.principal(), run.ID); err != nil {
		t.Fatal(err)
	}
	if len(remote.drops) != 1 || !strings.HasSuffix(remote.drops[0], run.ID.String()) {
		t.Fatalf("cancelar deveria derrubar o trabalho do pool: %v", remote.drops)
	}
	// A retry that finds the run already finished drops what the pool still has.
	if err := f.svc.Execute(ctx, f.org, run.ID, false); err != nil {
		t.Fatal(err)
	}
	if len(remote.drops) != 2 || remote.attaches != 0 {
		t.Fatalf("execução encerrada: drops %v, reconexões %d", remote.drops, remote.attaches)
	}
}

func TestInterruptionRequeuesFromTheLastEvent(t *testing.T) {
	t.Parallel()
	remote := &fakeRemote{interruptAfter: 10}
	f := poolFixture(t, burstPlacer{}, remote)
	ctx := context.Background()
	f.grant(t, 1000)
	run, _ := f.create(t)
	err := f.svc.Execute(ctx, f.org, run.ID, true) // even on the last attempt: snoozed, not failed
	if !errors.Is(err, ErrRequeue) || !errors.Is(err, ErrInterrupted) {
		t.Fatalf("err = %v", err)
	}
	mid, _ := f.svc.Get(ctx, f.org, run.ID)
	if mid.Status != StatusRunning || mid.LastSeq != 10 || mid.Timing().Interruptions != 1 {
		t.Fatalf("depois da interrupção: %s seq=%d %+v", mid.Status, mid.LastSeq, mid.Timing())
	}
	// The next attempt resumes on another instance (a harness without any
	// state of this run): it replays and the events up to the checkpoint
	// are skipped by seq.
	other, otherFake := freshHarness(t)
	remote.interruptAfter, remote.h = 0, other
	if err := f.svc.Execute(ctx, f.org, run.ID, false); err != nil {
		t.Fatal(err)
	}
	done, _ := f.svc.Get(ctx, f.org, run.ID)
	findings, _ := f.store.ListAllFindings(ctx, f.org, run.ID)
	if done.Status != StatusCompleted || len(findings) != 23 || done.Summary.FindingsTotal != 23 {
		t.Fatalf("retomada: %s, %d achados", done.Status, len(findings))
	}
	// The other instance holds no checkpoint of this run: it gets a replay.
	if first, second := f.fake.Runs(), otherFake.Runs(); len(first) != 1 || first[0].GetResume() || len(second) != 1 || second[0].GetResume() {
		t.Fatalf("a segunda tentativa no pool deveria ser um replay: %d e %d pedidos", len(first), len(second))
	}
}

// freshHarness starts another fake harness (another pool instance).
func freshHarness(t *testing.T) (harness.Client, *harnesstest.Server) {
	t.Helper()
	srv := harnesstest.NewServer(harnesstest.MustLoadGolden(), harnesstest.Options{})
	sock, cleanup, err := harnesstest.SocketPath()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	stop, err := harnesstest.Serve(sock, srv)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	c, err := harness.Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c, srv
}

func TestTooManyInterruptionsStayOnTheMainBox(t *testing.T) {
	t.Parallel()
	remote := &fakeRemote{interruptAfter: 3}
	f := poolFixture(t, burstPlacer{}, remote)
	ctx := context.Background()
	f.grant(t, 1000)
	run, _ := f.create(t)
	for i := range MaxInterruptions {
		remote.h, _ = freshHarness(t)
		if err := f.svc.Execute(ctx, f.org, run.ID, false); !errors.Is(err, ErrRequeue) {
			t.Fatalf("%d: %v", i, err)
		}
	}
	if err := f.svc.Execute(ctx, f.org, run.ID, false); err != nil {
		t.Fatal(err)
	}
	done, _ := f.svc.Get(ctx, f.org, run.ID)
	if done.Status != StatusCompleted || done.Timing().Reason != estimate.ReasonInterrupted || len(remote.runs) != MaxInterruptions {
		t.Fatalf("run = %s %+v, %d despachos", done.Status, done.Timing(), len(remote.runs))
	}
}

func TestCancelWhileWaitingForAWorker(t *testing.T) {
	t.Parallel()
	remote := &fakeRemote{mode: "cancelled"}
	f := poolFixture(t, burstPlacer{}, remote)
	ctx := context.Background()
	f.grant(t, 1000)
	run, _ := f.create(t)
	if _, err := f.svc.Cancel(ctx, f.principal(), run.ID); err != nil {
		t.Fatal(err)
	}
	// Cancelled before the job started: Execute sees a terminal run.
	if err := f.svc.Execute(ctx, f.org, run.ID, false); err != nil {
		t.Fatal(err)
	}
	run2, _ := f.create(t)
	if err := f.svc.Execute(ctx, f.org, run2.ID, false); err != nil {
		t.Fatalf("cancelada esperando: %v", err)
	}
	if len(remote.runs) != 1 {
		t.Fatalf("despachos = %d", len(remote.runs))
	}
}

func TestLightRunsStayLocalWithAPool(t *testing.T) {
	t.Parallel()
	remote := &fakeRemote{}
	f := poolFixture(t, burstPlacer{reason: estimate.ReasonLight}, remote)
	ctx := context.Background()
	f.grant(t, 1000)
	run, _ := f.create(t)
	if err := f.svc.Execute(ctx, f.org, run.ID, false); err != nil {
		t.Fatal(err)
	}
	done, _ := f.svc.Get(ctx, f.org, run.ID)
	if done.Status != StatusCompleted || len(remote.runs) != 0 || done.Timing().Placement != estimate.PlacementLocal {
		t.Fatalf("run = %s %+v", done.Status, done.Timing())
	}
}

func TestDeadlinesScaleWithTheExpectedTime(t *testing.T) {
	t.Parallel()
	d := DefaultDeadlines
	cases := map[int64]time.Duration{
		0:         2 * time.Hour,  // floor
		600:       2 * time.Hour,  // 30 min ×3 still under the floor
		3600:      3 * time.Hour,  // 1 h ×3
		5 * 3600:  15 * time.Hour, // 5 h ×3
		20 * 3600: 24 * time.Hour, // ceiling
		-5:        2 * time.Hour,
		1 << 62:   24 * time.Hour,
	}
	for expected, want := range cases {
		if got := d.For(expected); got != want {
			t.Errorf("For(%d) = %v, quer %v", expected, got, want)
		}
	}
	withWait := Deadlines{Min: time.Hour, Max: 4 * time.Hour, Factor: 2, Wait: 10 * time.Minute}
	if got := withWait.JobTimeout(3600, 15*time.Minute); got != 2*time.Hour+25*time.Minute {
		t.Fatalf("JobTimeout = %v", got)
	}
	// Broken bounds fall back to the defaults instead of a zero deadline.
	if got := (Deadlines{Max: time.Minute, Factor: 0.1}).For(3600); got != 3*time.Hour {
		t.Fatalf("limites inválidos: %v", got)
	}
	if (Timing{ExpectedSeconds: 100, LocalSeconds: 900}).Budget() != 900 || (Timing{ExpectedSeconds: 100}).Budget() != 100 {
		t.Fatal("Budget")
	}
}

func TestTheHarnessGetsTheScaledDeadline(t *testing.T) {
	t.Parallel()
	f := newFixture(t, harnesstest.Options{}, nil)
	ctx := context.Background()
	f.grant(t, 1000)
	run, _ := f.create(t)
	stored, _ := f.svc.Get(ctx, f.org, run.ID)
	tm := stored.Timing()
	tm.ExpectedSeconds, tm.LocalSeconds = 5*3600, 5*3600
	stored.SetTiming(tm)
	if _, err := f.store.SaveRun(ctx, stored, nil); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Execute(ctx, f.org, run.ID, false); err != nil {
		t.Fatal(err)
	}
	dl := f.fake.Runs()[0].GetOptions().GetDeadline().AsTime()
	if left := time.Until(dl); left < 14*time.Hour || left > 15*time.Hour {
		t.Fatalf("prazo do harness = %v, quer ~15 h (5 h × 3)", left)
	}
}
