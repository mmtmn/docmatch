package burst

import (
	"context"
	"errors"
	"strings"
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
	if s.MinSeconds != 900 || s.BootSeconds != 120 || s.Lease != DefaultLease || s.Heartbeat != DefaultHeartbeat || s.ClaimTimeout != 10*time.Minute ||
		s.IdleAfter != 10*time.Minute || s.Managed || s.Quota.MaxEvents != DefaultMaxJobEvents || s.ClaimSlots != DefaultClaimSlots {
		t.Fatalf("settings = %+v", s)
	}
	s = FromConfig(config.Burst{MinSeconds: 30}, 900, 0)
	if s.MinSeconds != 30 || s.BootSeconds != DefaultBootSeconds || s.NoClaimThreshold != DefaultNoClaimThreshold {
		t.Fatalf("env ganha do throughput: %+v", s)
	}
	if FromConfig(config.Burst{}, 0, 0).MinSeconds != DefaultMinSeconds {
		t.Fatal("sem throughput usa o padrão")
	}
	// With an Auto Scaling group the scaler measures the instances from
	// Pending on: no extra boot charge.
	if m := FromConfig(config.Burst{ASGName: "docmatch-test-worker"}, 600, 180); !m.Managed || m.BootSeconds != 0 {
		t.Fatalf("pool gerenciado: %+v", m)
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

func storeAt(now time.Time) *MemStore {
	st := NewMemStore()
	st.Now = func() time.Time { return now }
	return st
}

func TestRouterNeverRefusesAndExplains(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	heavy := estimate.PlaceInput{LocalSeconds: 3600}
	place := func(s Settings, st Store, in estimate.PlaceInput) estimate.Placement {
		r := &Router{Settings: s, Store: st, Now: func() time.Time { return now }}
		return r.Place(context.Background(), in)
	}
	mem := storeAt(now)
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
	_ = mem.TouchInstance(context.Background(), Instance{ID: "i-1"})
	if p := place(testSettings(), mem, heavy); !p.Burst || !p.Warm {
		t.Fatalf("pool quente: %+v", p)
	}
	// Full: every instance the pool may have holds a job.
	full := storeAt(now)
	for range 2 {
		_, _ = full.CreateJob(context.Background(), Job{ID: uuid.New(), RunID: uuid.New()}, 0)
	}
	if p := place(testSettings(), full, heavy); p.Burst || p.Reason != estimate.ReasonFull {
		t.Fatalf("cheio: %+v", p)
	}
	// Cap: the month's instance hours are used up (10 h here).
	clock := MonthStart(now).Add(time.Hour)
	capped := NewMemStore()
	capped.Now = func() time.Time { return clock }
	_ = capped.TouchInstance(context.Background(), Instance{ID: "i-old"})
	clock = clock.Add(11 * time.Hour)
	_ = capped.TouchInstance(context.Background(), Instance{ID: "i-old"})
	if p := place(testSettings(), capped, heavy); p.Burst || p.Reason != estimate.ReasonCap {
		t.Fatalf("teto: %+v", p)
	}
	none := testSettings()
	none.MaxInstances = 0
	if p := place(none, storeAt(now), heavy); p.Burst || p.Reason != estimate.ReasonCap {
		t.Fatalf("sem instâncias: %+v", p)
	}
	// Unhealthy: scale-ups that produced no worker cool the pool down.
	sick := storeAt(now)
	_ = sick.NoteNoClaim(context.Background(), 1, time.Hour)
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
	// Quotes, run creation and execution all ask: one read per StatusTTL. The
	// answers are a hint (the dispatcher reserves the place atomically), so a
	// quote never takes a place a run then misses.
	for range 3 {
		if p := r.Place(context.Background(), heavy); !p.Burst {
			t.Fatalf("o cache não reserva lugar: %+v", p)
		}
	}
	if st.reads != 1 {
		t.Fatalf("leituras = %d, quer 1 (cache)", st.reads)
	}
	now = now.Add(DefaultStatusTTL)
	if p := r.Place(context.Background(), heavy); !p.Burst || st.reads != 2 {
		t.Fatalf("cache vencido: %+v, leituras %d", p, st.reads)
	}
	if (&Router{}).now().IsZero() {
		t.Fatal("relógio padrão")
	}
}

func TestLimitedAndUsage(t *testing.T) {
	t.Parallel()
	s := testSettings()
	if s.Limited(PoolStatus{Busy: 1}) || !s.Limited(PoolStatus{Busy: 2}) || !s.Limited(PoolStatus{UsedSeconds: 11 * 3600}) {
		t.Fatal("Limited")
	}
	off := s
	off.Enabled = false
	if !off.Limited(PoolStatus{}) {
		t.Fatal("desligado é limitado")
	}
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
	if st.UsedHours() != 2 || !s.CapReached(PoolStatus{UsedSeconds: 36000}) || s.CapReached(st) {
		t.Fatal("horas e teto")
	}
}

func TestTruncateAndSanitize(t *testing.T) {
	t.Parallel()
	// "ção" is c + ç(2 bytes) + ã(2 bytes) + o: a cut at 2 must not split ç.
	if got := Truncate("ação", 2); got != "a" {
		t.Fatalf("Truncate = %q", got)
	}
	if got := Truncate("abc", 10); got != "abc" {
		t.Fatalf("Truncate curto = %q", got)
	}
	long := strings.Repeat("é", 400) // 800 bytes
	if got := Sanitize(long); len(got) > reasonMax || !strings.HasSuffix(got, "é") {
		t.Fatalf("Sanitize cortou no meio de um caractere: %d bytes", len(got))
	}
	msg := `harness: abrir a fonte upl_1: Get "https://uploads.s3.amazonaws.com/quarantine/x?X-Amz-Signature=abc&X-Amz-Credential=k": EOF`
	if got := Sanitize(msg); strings.Contains(got, "X-Amz") || !strings.Contains(got, "https://uploads.s3.amazonaws.com/quarantine/x") {
		t.Fatalf("Sanitize deixou a assinatura: %s", got)
	}
	if Seconds(0) != 1 || Seconds(90*time.Second) != 90 || Seconds(1<<62) != 1<<30 {
		t.Fatal("Seconds")
	}
}
