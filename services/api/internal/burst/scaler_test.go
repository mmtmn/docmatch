package burst

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

// fakeASG records what the scaler asks of the group, in order.
type fakeASG struct {
	mu        sync.Mutex
	group     Group
	sets      []int
	calls     []string
	protected map[string]bool
	err       error
}

func (f *fakeASG) Describe(context.Context) (Group, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	g := f.group
	g.Instances = nil
	for _, in := range f.group.Instances {
		in.Protected = f.protected[in.ID]
		g.Instances = append(g.Instances, in)
	}
	return g, f.err
}

func (f *fakeASG) SetDesired(_ context.Context, n int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if n > f.group.Max {
		return errors.New("acima do máximo do grupo")
	}
	f.sets = append(f.sets, n)
	f.calls = append(f.calls, "set")
	f.group.Desired = n
	return nil
}

func (f *fakeASG) Protect(_ context.Context, ids []string, p bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.protected == nil {
		f.protected = map[string]bool{}
	}
	for _, in := range f.group.Instances {
		if slices.Contains(ids, in.ID) && in.State == "Terminating" {
			return errors.New("ValidationError: instância saindo do grupo")
		}
	}
	for _, id := range ids {
		f.protected[id] = p
	}
	if p {
		f.calls = append(f.calls, "protect")
	} else {
		f.calls = append(f.calls, "unprotect")
	}
	return nil
}

func (f *fakeASG) setDesiredByHand(n int) {
	f.mu.Lock()
	f.group.Desired = n
	f.mu.Unlock()
}

func queueJobs(t *testing.T, st *MemStore, n int) []Job {
	t.Helper()
	out := make([]Job, 0, n)
	for range n {
		j, err := st.CreateJob(context.Background(), Job{ID: uuid.New(), OrgID: uuid.New(), RunID: uuid.New()}, 0)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, j)
	}
	return out
}

func newScaler(st *MemStore, asg ASG, s Settings, c *clock) *Scaler {
	st.Now = c.Now
	sc := NewScaler(st, asg, s, nil)
	sc.Now = c.Now
	return sc
}

func TestScalerFollowsTheWork(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, c := NewMemStore(), newClock()
	asg := &fakeASG{group: Group{Max: 2}}
	s := newScaler(st, asg, testSettings(), c)

	// Idle at zero: nothing to do.
	if d, err := s.Tick(ctx); err != nil || d.Target != 0 || len(asg.sets) != 0 {
		t.Fatalf("ocioso: %+v %v %v", d, err, asg.sets)
	}
	// Three heavy runs queued: one instance each, up to the cap of two.
	jobs := queueJobs(t, st, 3)
	if d, _ := s.Tick(ctx); d.Target != 2 || asg.group.Desired != 2 {
		t.Fatalf("subida: %+v", d)
	}
	// The instances run; the group's view feeds usage and liveness.
	asg.group.Instances = []GroupInstance{{ID: "i-1", Type: "c7g.large", State: "InService"}, {ID: "i-2", Type: "m7g.medium", State: "InService"}}
	_, _, _ = st.Claim(ctx, "i-1", []byte("a"), time.Hour)
	_, _, _ = st.Claim(ctx, "i-2", []byte("b"), time.Hour)
	if _, err := s.Tick(ctx); err != nil || !asg.protected["i-1"] || !asg.protected["i-2"] {
		t.Fatalf("proteção: %v %v", err, asg.protected)
	}
	if status, _ := st.Status(ctx, s.Settings.Query(c.Now())); status.Live != 2 || status.Busy != 2 {
		t.Fatalf("instâncias do grupo: %+v", status)
	}
	// Work done: protection off, the pool stays warm for IdleAfter...
	for _, j := range jobs {
		_, _ = st.Cancel(ctx, j.ID, "fim")
	}
	c.Advance(time.Minute)
	if d, _ := s.Tick(ctx); d.Target != 2 || asg.protected["i-1"] {
		t.Fatalf("aquecido: %+v %v", d, asg.protected)
	}
	// ...then goes back to zero.
	c.Advance(s.Settings.IdleAfter)
	if d, _ := s.Tick(ctx); d.Target != 0 || asg.group.Desired != 0 || d.Reason != "ocioso" {
		t.Fatalf("de volta a zero: %+v", d)
	}
	if !slices.Equal(asg.sets, []int{2, 0}) {
		t.Fatalf("chamadas = %v", asg.sets)
	}
}

func TestScalerProtectsBeforeScalingIn(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, c := NewMemStore(), newClock()
	asg := &fakeASG{group: Group{Max: 2, Desired: 2, Instances: []GroupInstance{
		{ID: "i-busy", State: "InService"}, {ID: "i-idle", State: "InService"}, {ID: "i-going", State: "Terminating"},
	}}}
	asg.protected = map[string]bool{"i-idle": true, "i-going": true}
	queueJobs(t, st, 1)
	_, _, _ = st.Claim(ctx, "i-busy", []byte("a"), time.Hour)
	off := testSettings()
	off.Enabled = false // drain: target zero
	s := newScaler(st, asg, off, c)
	d, err := s.Tick(ctx)
	if err != nil {
		t.Fatalf("um Terminating não pode derrubar a chamada: %v", err)
	}
	// The busy one is protected before the group is told to shrink, the idle
	// one is unprotected, the terminating one is left alone.
	if !slices.Equal(asg.calls, []string{"protect", "unprotect", "set"}) || !asg.protected["i-busy"] || asg.protected["i-idle"] {
		t.Fatalf("ordem = %v, proteção = %v", asg.calls, asg.protected)
	}
	if d.Target != 0 || d.Reason != "pool desligado: drenando" {
		t.Fatalf("drenagem: %+v", d)
	}
}

func TestScalerDrainsAPoolSwitchedOff(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, c := NewMemStore(), newClock()
	asg := &fakeASG{group: Group{Max: 2, Desired: 1, Instances: []GroupInstance{{ID: "i-1", State: "InService"}}}}
	jobs := queueJobs(t, st, 1)
	_, _, _ = st.Claim(ctx, "i-1", []byte("a"), time.Hour)
	off := testSettings()
	off.Enabled = false
	s := newScaler(st, asg, off, c)
	if d, _ := s.Tick(ctx); d.Target != 0 || !asg.protected["i-1"] {
		t.Fatalf("desligado com trabalho: %+v %v", d, asg.protected)
	}
	// The job ends: protection comes off and the group lets the instance go.
	_, _ = st.Cancel(ctx, jobs[0].ID, "fim")
	if _, err := s.Tick(ctx); err != nil || asg.protected["i-1"] || asg.group.Desired != 0 {
		t.Fatalf("drenado: %v %v desejado=%d", err, asg.protected, asg.group.Desired)
	}
}

func TestScalerCountsAManualRaiseAsWork(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, c := NewMemStore(), newClock()
	asg := &fakeASG{group: Group{Max: 2}}
	s := newScaler(st, asg, testSettings(), c)
	_, _ = s.Tick(ctx)
	c.Advance(2 * s.Settings.IdleAfter) // long idle
	_, _ = s.Tick(ctx)
	// burst.sh up 2: the idle window starts when the scaler sees it.
	asg.setDesiredByHand(2)
	c.Advance(DefaultScaleInterval)
	if d, _ := s.Tick(ctx); d.Target != 2 || asg.group.Desired != 2 {
		t.Fatalf("o escalonador desfez o up: %+v", d)
	}
	c.Advance(s.Settings.IdleAfter / 2)
	if d, _ := s.Tick(ctx); d.Target != 2 {
		t.Fatalf("ainda dentro da janela: %+v", d)
	}
	c.Advance(s.Settings.IdleAfter)
	if d, _ := s.Tick(ctx); d.Target != 0 {
		t.Fatalf("fim da janela: %+v", d)
	}
}

func TestScalerCapsNeverDropBusyInstances(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	busyStore := func(c *clock) *MemStore {
		st := NewMemStore()
		st.Now = c.Now
		queueJobs(t, st, 2)
		_, _, _ = st.Claim(ctx, "i-1", []byte("a"), 48*time.Hour) // outlives the 11 h below
		return st
	}
	// Monthly budget used up: only the busy instance stays, the queued job waits for the fallback.
	c := newClock()
	st := busyStore(c)
	_ = st.TouchInstance(ctx, Instance{ID: "i-old"})
	c.Advance(11 * time.Hour)
	_ = st.TouchInstance(ctx, Instance{ID: "i-old"})
	asg := &fakeASG{group: Group{Max: 2, Desired: 2}}
	s := newScaler(st, asg, testSettings(), c)
	if d, _ := s.Tick(ctx); d.Target != 1 || d.Reason != "orçamento mensal de horas esgotado" {
		t.Fatalf("teto: %+v", d)
	}
	// Cooling down after scale-ups without a worker.
	c = newClock()
	st = busyStore(c)
	_ = st.NoteNoClaim(ctx, 1, time.Hour)
	s = newScaler(st, &fakeASG{group: Group{Max: 2}}, testSettings(), c)
	if d, _ := s.Tick(ctx); d.Target != 1 || d.Reason != "pool em espera após subidas sem trabalhador" {
		t.Fatalf("em espera: %+v", d)
	}
	// The group's max_size wins over DM_BURST_MAX_INSTANCES.
	c = newClock()
	s = newScaler(busyStore(c), &fakeASG{group: Group{Max: 1}}, testSettings(), c)
	if d, _ := s.Tick(ctx); d.Target != 1 {
		t.Fatalf("max do grupo: %+v", d)
	}
}

func TestScalerExpiresLeasesAndRunsWithoutAWS(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, c := NewMemStore(), newClock()
	st.Now = c.Now
	queueJobs(t, st, 1)
	j, _, _ := st.Claim(ctx, "i-1", []byte("a"), time.Minute)
	s := newScaler(st, nil, testSettings(), c)
	c.Advance(2 * time.Minute)
	d, err := s.Tick(ctx)
	if err != nil || d.Reason != "pool manual" {
		t.Fatalf("manual: %+v %v", d, err)
	}
	if got, _, _ := st.GetJob(ctx, j.ID); got.State != StateLost {
		t.Fatalf("concessão vencida: %s", got.State)
	}
	// Old finished jobs are pruned on the first tick (and every pruneEvery after).
	c.Advance(8 * 24 * time.Hour)
	if n, _ := st.Prune(ctx, 7*24*time.Hour); n != 1 {
		t.Fatalf("prune = %d", n)
	}
	// Describe failing surfaces as an error of the tick.
	s = newScaler(NewMemStore(), &fakeASG{err: errors.New("acesso negado")}, testSettings(), c)
	if _, err := s.Tick(ctx); err == nil {
		t.Fatal("erro do grupo deveria subir")
	}
}

func TestScalerRunWakesAndStops(t *testing.T) {
	t.Parallel()
	asg := &fakeASG{group: Group{Max: 2}}
	st := NewMemStore()
	s := NewScaler(st, asg, testSettings(), nil)
	s.Interval = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	queueJobs(t, st, 1)
	deadline := time.Now().Add(5 * time.Second)
	for {
		s.Wake()
		g, _ := asg.Describe(ctx)
		if g.Desired == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Wake não fez o escalonador agir")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	(&Scaler{}).Wake() // no channel yet: harmless
	if (&Scaler{}).now().IsZero() || (&Scaler{}).log() == nil {
		t.Fatal("padrões")
	}
}
