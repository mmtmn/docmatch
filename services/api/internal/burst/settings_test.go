package burst

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/mmtmn/docmatch/services/api/internal/config"
	"github.com/mmtmn/docmatch/services/api/internal/estimate"
)

func testSettings() Settings {
	return Settings{
		Enabled: true, MinSeconds: 600, MemoryHeadroomMB: 384, Kinds: []string{"audio", "video"},
		MaxInstances: 2, MonthlyHours: 10, IdleAfter: 10 * time.Minute, ClaimTimeout: 10 * time.Minute,
	}.WithDefaults()
}

func TestFromConfigDefaults(t *testing.T) {
	t.Parallel()
	s := FromConfig(config.Burst{Enabled: true, MemoryHeadroomMB: 384, MaxInstances: 2, MonthlyHours: 60}, 900, 120)
	if s.MinSeconds != 900 || s.BootSeconds != 120 || s.Lease != DefaultLease || s.Heartbeat != DefaultHeartbeat || s.ClaimTimeout != 10*time.Minute || s.IdleAfter != 10*time.Minute {
		t.Fatalf("settings = %+v", s)
	}
	s = FromConfig(config.Burst{MinSeconds: 30}, 900, 0)
	if s.MinSeconds != 30 || s.BootSeconds != DefaultBootSeconds || s.NoClaimThreshold != DefaultNoClaimThreshold {
		t.Fatalf("env ganha do throughput: %+v", s)
	}
	if FromConfig(config.Burst{}, 0, 0).MinSeconds != DefaultMinSeconds {
		t.Fatal("sem throughput usa o padrão")
	}
}

func TestHeavy(t *testing.T) {
	t.Parallel()
	s := testSettings()
	cases := []struct {
		name string
		in   estimate.PlaceInput
		want string
	}{
		{"leve", estimate.PlaceInput{Units: estimate.Units{"table": 1}, LocalSeconds: 30, MemoryMB: 100}, estimate.ReasonLight},
		{"tempo", estimate.PlaceInput{LocalSeconds: 600}, estimate.ReasonTime},
		{"memória", estimate.PlaceInput{LocalSeconds: 30, MemoryMB: 385}, estimate.ReasonMemory},
		{"áudio", estimate.PlaceInput{Units: estimate.Units{"audio": 0.5}}, "kind:audio"},
		{"vídeo", estimate.PlaceInput{Units: estimate.Units{"video": 3}, LocalSeconds: 9999}, "kind:video"},
	}
	for _, c := range cases {
		heavy, why := s.Heavy(c.in)
		if why != c.want || heavy != (c.want != estimate.ReasonLight) {
			t.Errorf("%s: heavy=%v why=%s", c.name, heavy, why)
		}
	}
	off := s
	off.MinSeconds, off.MemoryHeadroomMB, off.Kinds = 0, 0, nil
	if heavy, _ := off.Heavy(estimate.PlaceInput{LocalSeconds: 1 << 30, MemoryMB: 1 << 30}); heavy {
		t.Fatal("limiares zerados não deveriam mandar nada ao pool")
	}
}

type failingStatus struct{ Store }

func (failingStatus) Status(context.Context, StatusQuery) (PoolStatus, error) {
	return PoolStatus{}, errors.New("banco fora")
}

func TestRouterNeverRefusesAndExplains(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	heavy := estimate.PlaceInput{LocalSeconds: 3600}
	place := func(s Settings, st Store, in estimate.PlaceInput) estimate.Placement {
		r := &Router{Settings: s, Store: st, Now: func() time.Time { return now }}
		return r.Place(context.Background(), in)
	}
	mem := NewMemStore()
	if p := place(testSettings(), mem, estimate.PlaceInput{LocalSeconds: 10}); p.Burst || p.Reason != estimate.ReasonLight {
		t.Fatalf("leve: %+v", p)
	}
	off := testSettings()
	off.Enabled = false
	if p := place(off, mem, heavy); p.Burst || p.Reason != estimate.ReasonDisabled {
		t.Fatalf("desligado: %+v", p)
	}
	if p := place(testSettings(), failingStatus{mem}, heavy); p.Burst || p.Reason != estimate.ReasonPoolUnknown {
		t.Fatalf("banco fora: %+v", p)
	}
	if p := place(testSettings(), mem, heavy); !p.Burst || p.Reason != estimate.ReasonTime || p.Warm {
		t.Fatalf("pesada e pool frio: %+v", p)
	}
	// A live idle instance makes the pool warm (no boot in the estimate).
	_ = mem.TouchInstance(context.Background(), Instance{ID: "i-1"}, now.Add(-time.Minute))
	if p := place(testSettings(), mem, heavy); !p.Burst || !p.Warm {
		t.Fatalf("pool quente: %+v", p)
	}
	// Full: every instance the pool may have holds a job.
	full := NewMemStore()
	for i := range 2 {
		_, _ = full.CreateJob(context.Background(), Job{ID: uuid.New(), RunID: uuid.New(), CreatedAt: now.Add(time.Duration(i) * time.Second)})
	}
	if p := place(testSettings(), full, heavy); p.Burst || p.Reason != estimate.ReasonFull {
		t.Fatalf("cheio: %+v", p)
	}
	// Cap: the month's instance hours are used up (10 h here).
	capped := NewMemStore()
	_ = capped.TouchInstance(context.Background(), Instance{ID: "i-old"}, MonthStart(now).Add(time.Hour))
	_ = capped.TouchInstance(context.Background(), Instance{ID: "i-old"}, MonthStart(now).Add(12*time.Hour))
	if p := place(testSettings(), capped, heavy); p.Burst || p.Reason != estimate.ReasonCap {
		t.Fatalf("teto: %+v", p)
	}
	none := testSettings()
	none.MaxInstances = 0
	if p := place(none, NewMemStore(), heavy); p.Burst || p.Reason != estimate.ReasonCap {
		t.Fatalf("sem instâncias: %+v", p)
	}
	// Unhealthy: scale-ups that produced no worker cool the pool down.
	sick := NewMemStore()
	_ = sick.NoteNoClaim(context.Background(), 1, now.Add(time.Hour))
	if p := place(testSettings(), sick, heavy); p.Burst || p.Reason != estimate.ReasonUnhealthy {
		t.Fatalf("em espera: %+v", p)
	}
	_ = sick.NoteClaim(context.Background())
	if p := place(testSettings(), sick, heavy); !p.Burst {
		t.Fatalf("recuperado: %+v", p)
	}
}

type countingStore struct {
	*MemStore
	reads int
}

func (c *countingStore) Status(ctx context.Context, q StatusQuery) (PoolStatus, error) {
	c.reads++
	return c.MemStore.Status(ctx, q)
}

func TestRouterCachesThePoolStatus(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	st := &countingStore{MemStore: NewMemStore()}
	r := &Router{Settings: testSettings(), Store: st, Now: func() time.Time { return now }}
	heavy := estimate.PlaceInput{LocalSeconds: 3600}
	r.Place(context.Background(), heavy)
	r.Place(context.Background(), heavy)
	if st.reads != 1 {
		t.Fatalf("leituras = %d, quer 1 (cache)", st.reads)
	}
	now = now.Add(DefaultStatusTTL)
	r.Place(context.Background(), heavy)
	if st.reads != 2 {
		t.Fatalf("leituras = %d, quer 2 (cache vencido)", st.reads)
	}
	if (&Router{}).now().IsZero() {
		t.Fatal("relógio padrão")
	}
}

func TestUsageAcrossTheMonth(t *testing.T) {
	t.Parallel()
	month := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	now := month.Add(10 * 24 * time.Hour)
	cases := []struct {
		name        string
		first, last time.Time
		want        float64
	}{
		{"dentro do mês, com boot", month.Add(time.Hour), month.Add(3 * time.Hour), 2*3600 + 180},
		{"começou no mês anterior, sem boot", month.Add(-time.Hour), month.Add(time.Hour), 3600},
		{"terminou no mês anterior", month.Add(-3 * time.Hour), month.Add(-time.Hour), 0},
	}
	for _, c := range cases {
		if got := usage(c.first, c.last, month, now, 180); got != c.want {
			t.Errorf("%s: %v, quer %v", c.name, got, c.want)
		}
	}
	if !MonthStart(time.Date(2026, 11, 30, 23, 59, 0, 0, time.FixedZone("BRT", -3*3600))).Equal(time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatal("o mês é UTC")
	}
	st := PoolStatus{UsedSeconds: 7200}
	if st.UsedHours() != 2 || !testSettings().CapReached(PoolStatus{UsedSeconds: 36000}) || testSettings().CapReached(st) {
		t.Fatal("horas e teto")
	}
}
