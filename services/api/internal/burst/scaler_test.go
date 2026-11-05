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

// fakeASG records what the scaler asks of the group.
type fakeASG struct {
	mu        sync.Mutex
	group     Group
	sets      []int
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
	f.group.Desired = n
	return nil
}

func (f *fakeASG) Protect(_ context.Context, ids []string, p bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.protected == nil {
		f.protected = map[string]bool{}
	}
	for _, id := range ids {
		f.protected[id] = p
	}
	return nil
}

func queueJobs(t *testing.T, st *MemStore, n int, at time.Time) []Job {
	t.Helper()
	out := make([]Job, 0, n)
	for i := range n {
		j, err := st.CreateJob(context.Background(), Job{ID: uuid.New(), OrgID: uuid.New(), RunID: uuid.New(), CreatedAt: at.Add(time.Duration(i) * time.Second)})
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, j)
	}
	return out
}

func TestScalerFollowsTheWork(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := NewMemStore()
	c := &clock{now: time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)}
	asg := &fakeASG{group: Group{Max: 2}}
	s := NewScaler(st, asg, testSettings(), nil)
	s.Now = c.Now

	// Idle at zero: nothing to do.
	if d, err := s.Tick(ctx); err != nil || d.Target != 0 || len(asg.sets) != 0 {
		t.Fatalf("ocioso: %+v %v %v", d, err, asg.sets)
	}
	// Three heavy runs queued: one instance each, up to the cap of two.
	jobs := queueJobs(t, st, 3, c.Now())
	if d, _ := s.Tick(ctx); d.Target != 2 || asg.group.Desired != 2 {
		t.Fatalf("subida: %+v", d)
	}
	// The instances claim; the busy ones get scale-in protection.
	asg.group.Instances = []GroupInstance{{ID: "i-1", State: "InService"}, {ID: "i-2", State: "InService"}}
	_, _, _ = st.Claim(ctx, "i-1", []byte("a"), c.Now(), c.Now().Add(time.Hour))
	_, _, _ = st.Claim(ctx, "i-2", []byte("b"), c.Now(), c.Now().Add(time.Hour))
	if _, err := s.Tick(ctx); err != nil || !asg.protected["i-1"] || !asg.protected["i-2"] {
		t.Fatalf("proteção: %v %v", err, asg.protected)
	}
	// Work done: protection off, the pool stays warm for IdleAfter...
	for _, j := range jobs {
		_, _ = st.Cancel(ctx, j.ID, "fim", c.Now())
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

func TestScalerCapsNeverDropBusyInstances(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	busyStore := func() *MemStore {
		st := NewMemStore()
		queueJobs(t, st, 2, now)
		_, _, _ = st.Claim(ctx, "i-1", []byte("a"), now, now.Add(time.Hour))
		return st
	}
	// Monthly budget used up: only the busy instance stays, the queued job waits for the fallback.
	st := busyStore()
	_ = st.TouchInstance(ctx, Instance{ID: "i-old"}, MonthStart(now))
	_ = st.TouchInstance(ctx, Instance{ID: "i-old"}, MonthStart(now).Add(11*time.Hour))
	asg := &fakeASG{group: Group{Max: 2, Desired: 2}}
	s := NewScaler(st, asg, testSettings(), nil)
	s.Now = func() time.Time { return now }
	if d, _ := s.Tick(ctx); d.Target != 1 || d.Reason != "orçamento mensal de horas esgotado" {
		t.Fatalf("teto: %+v", d)
	}
	// Cooling down after scale-ups without a worker.
	st = busyStore()
	_ = st.NoteNoClaim(ctx, 1, now.Add(time.Hour))
	s = NewScaler(st, &fakeASG{group: Group{Max: 2}}, testSettings(), nil)
	s.Now = func() time.Time { return now }
	if d, _ := s.Tick(ctx); d.Target != 1 || d.Reason != "pool em espera após subidas sem trabalhador" {
		t.Fatalf("em espera: %+v", d)
	}
	// Pool switched off: the busy instance finishes, nothing new starts.
	off := testSettings()
	off.Enabled = false
	s = NewScaler(busyStore(), &fakeASG{group: Group{Max: 2, Desired: 2}}, off, nil)
	s.Now = func() time.Time { return now }
	if d, _ := s.Tick(ctx); d.Target != 1 || d.Reason != "pool desligado" {
		t.Fatalf("desligado: %+v", d)
	}
	// The group's max_size wins over DM_BURST_MAX_INSTANCES.
	s = NewScaler(busyStore(), &fakeASG{group: Group{Max: 1}}, testSettings(), nil)
	s.Now = func() time.Time { return now }
	if d, _ := s.Tick(ctx); d.Target != 1 {
		t.Fatalf("max do grupo: %+v", d)
	}
}

func TestScalerExpiresLeasesAndRunsWithoutAWS(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := NewMemStore()
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	queueJobs(t, st, 1, now)
	j, _, _ := st.Claim(ctx, "i-1", []byte("a"), now, now.Add(time.Minute))
	_, _ = st.Finish(ctx, uuid.New(), []byte("x"), StateDone, "", now) // unknown job: no-op
	s := NewScaler(st, nil, testSettings(), nil)
	s.Now = func() time.Time { return now.Add(2 * time.Minute) }
	d, err := s.Tick(ctx)
	if err != nil || d.Reason != "pool manual" {
		t.Fatalf("manual: %+v %v", d, err)
	}
	if got, _, _ := st.GetJob(ctx, j.ID); got.State != StateLost {
		t.Fatalf("concessão vencida: %s", got.State)
	}
	// Old finished jobs are pruned on the first tick (and every pruneEvery after).
	if n, _ := st.Prune(ctx, now.Add(8*24*time.Hour)); n != 1 {
		t.Fatalf("prune = %d", n)
	}
	// Describe failing surfaces as an error of the tick.
	s = NewScaler(NewMemStore(), &fakeASG{err: errors.New("acesso negado")}, testSettings(), nil)
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
	queueJobs(t, st, 1, time.Now())
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
