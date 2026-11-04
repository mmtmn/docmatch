package ratelimit

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"pgregory.net/rapid"
)

// fakeClock is a settable time source.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func TestPolicyBasics(t *testing.T) {
	t.Parallel()
	p := Policy{Limit: 60, Period: time.Minute}
	if p.Interval() != time.Second || p.BurstSize() != 60 || p.String() != "60;w=60" {
		t.Fatalf("política = %v %d %s", p.Interval(), p.BurstSize(), p.String())
	}
	if (Policy{Limit: 10, Period: time.Minute, Burst: 3}).BurstSize() != 3 {
		t.Fatal("burst explícito ignorado")
	}
	for _, bad := range []Policy{{}, {Limit: -1, Period: time.Second}, {Limit: 1, Period: -1}, {Limit: 5, Period: time.Second, Burst: -1}, {Limit: 1000, Period: 10 * time.Nanosecond}} {
		if err := bad.Validate(); err == nil {
			t.Errorf("%+v deveria ser inválida", bad)
		}
	}
}

func TestMemoryBurstDenyRefill(t *testing.T) {
	t.Parallel()
	clock := newFakeClock()
	m := NewMemory(WithClock(clock.Now))
	p := Policy{Limit: 5, Period: time.Minute}
	ctx := context.Background()
	for i := range 5 {
		d, err := m.Allow(ctx, "k", p)
		if err != nil || !d.Allowed {
			t.Fatalf("requisição %d deveria passar: %+v %v", i, d, err)
		}
		if d.Remaining != 4-i || d.Limit != 5 {
			t.Fatalf("requisição %d: remaining = %d, limit = %d", i, d.Remaining, d.Limit)
		}
	}
	d, _ := m.Allow(ctx, "k", p)
	if d.Allowed || d.RetryAfter != 12*time.Second || d.Remaining != 0 || d.ResetAfter != time.Minute {
		t.Fatalf("sexta requisição deveria ser negada com retry 12s: %+v", d)
	}
	clock.Advance(11 * time.Second)
	if d, _ = m.Allow(ctx, "k", p); d.Allowed {
		t.Fatal("antes do intervalo ainda deveria negar")
	}
	clock.Advance(time.Second)
	if d, _ = m.Allow(ctx, "k", p); !d.Allowed || d.Remaining != 0 {
		t.Fatalf("após o intervalo deveria passar: %+v", d)
	}
	clock.Advance(time.Minute)
	if d, _ = m.Allow(ctx, "k", p); !d.Allowed || d.Remaining != 4 {
		t.Fatalf("após um período completo o burst deveria estar cheio: %+v", d)
	}
}

func TestMemoryKeysAreIndependent(t *testing.T) {
	t.Parallel()
	m := NewMemory(WithClock(newFakeClock().Now))
	p := Policy{Limit: 1, Period: time.Minute}
	ctx := context.Background()
	if d, _ := m.Allow(ctx, "a", p); !d.Allowed {
		t.Fatal("a")
	}
	if d, _ := m.Allow(ctx, "a", p); d.Allowed {
		t.Fatal("a de novo deveria negar")
	}
	if d, _ := m.Allow(ctx, "b", p); !d.Allowed {
		t.Fatal("b não deveria ser afetada por a")
	}
	if m.Len() != 2 {
		t.Fatalf("Len = %d", m.Len())
	}
	if err := m.Reset(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if d, _ := m.Allow(ctx, "a", p); !d.Allowed {
		t.Fatal("após Reset deveria passar")
	}
}

func TestMemoryErrors(t *testing.T) {
	t.Parallel()
	m := NewMemory()
	ctx := context.Background()
	p := Policy{Limit: 3, Period: time.Minute}
	if _, err := m.Allow(ctx, "", p); !errors.Is(err, ErrEmptyKey) {
		t.Fatalf("chave vazia: %v", err)
	}
	if _, err := m.Allow(ctx, "k", Policy{}); err == nil {
		t.Fatal("política inválida deveria falhar")
	}
	if _, err := m.AllowN(ctx, "k", p, 0); !errors.Is(err, ErrInvalidCost) {
		t.Fatalf("custo zero: %v", err)
	}
	if _, err := m.AllowN(ctx, "k", p, 4); !errors.Is(err, ErrInvalidCost) {
		t.Fatalf("custo acima do burst: %v", err)
	}
	d, err := m.AllowN(ctx, "k", p, 3)
	if err != nil || !d.Allowed || d.Remaining != 0 {
		t.Fatalf("custo igual ao burst deveria passar e zerar: %+v %v", d, err)
	}
}

func TestMemorySweepDropsReplenishedKeys(t *testing.T) {
	t.Parallel()
	clock := newFakeClock()
	m := NewMemory(WithClock(clock.Now), WithSweepEvery(4))
	p := Policy{Limit: 10, Period: time.Second}
	ctx := context.Background()
	for _, k := range []string{"a", "b", "c"} {
		_, _ = m.Allow(ctx, k, p)
	}
	if m.Len() != 3 {
		t.Fatalf("Len = %d", m.Len())
	}
	clock.Advance(time.Hour)    // all keys are fully replenished
	_, _ = m.Allow(ctx, "d", p) // fourth op triggers the sweep
	if m.Len() != 1 {
		t.Fatalf("após a varredura só d deveria restar, Len = %d", m.Len())
	}
	// WithSweepEvery ignores non-positive values.
	if NewMemory(WithSweepEvery(0)).sweep != 1024 {
		t.Fatal("sweep padrão deveria ser mantido")
	}
}

func TestMemoryConcurrentAccess(t *testing.T) {
	t.Parallel()
	m := NewMemory()
	p := Policy{Limit: 1000, Period: time.Hour, Burst: 100}
	var wg sync.WaitGroup
	var mu sync.Mutex
	allowed := 0
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 10 {
				d, err := m.Allow(context.Background(), "shared", p)
				if err != nil {
					t.Error(err)
					return
				}
				if d.Allowed {
					mu.Lock()
					allowed++
					mu.Unlock()
				}
			}
		}()
	}
	wg.Wait()
	if allowed > 100 || allowed < 99 {
		t.Fatalf("500 tentativas concorrentes com burst 100: %d permitidas", allowed)
	}
}

// Property: from idle, exactly Burst requests pass instantly, the next one is
// denied with RetryAfter equal to the emission interval, and it passes once
// that interval has elapsed.
func TestPropertyBurstThenInterval(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(rt *rapid.T) {
		limit := rapid.IntRange(1, 200).Draw(rt, "limit")
		period := time.Duration(rapid.Int64Range(int64(limit)*int64(time.Millisecond), int64(time.Hour)).Draw(rt, "period"))
		burst := rapid.IntRange(0, limit).Draw(rt, "burst")
		p := Policy{Limit: limit, Period: period, Burst: burst}
		clock := newFakeClock()
		m := NewMemory(WithClock(clock.Now))
		ctx := context.Background()
		want := p.BurstSize()
		for i := range want {
			d, err := m.Allow(ctx, "k", p)
			if err != nil || !d.Allowed {
				rt.Fatalf("requisição %d/%d negada: %+v %v", i+1, want, d, err)
			}
			if d.Remaining != want-i-1 {
				rt.Fatalf("remaining após %d = %d", i+1, d.Remaining)
			}
		}
		d, _ := m.Allow(ctx, "k", p)
		if d.Allowed {
			rt.Fatalf("requisição %d deveria ser negada", want+1)
		}
		if d.RetryAfter != p.Interval() {
			rt.Fatalf("RetryAfter = %v, esperado %v", d.RetryAfter, p.Interval())
		}
		if d.ResetAfter != p.Interval()*time.Duration(want) {
			rt.Fatalf("ResetAfter = %v, esperado %v", d.ResetAfter, p.Interval()*time.Duration(want))
		}
		clock.Advance(d.RetryAfter - 1)
		if d2, _ := m.Allow(ctx, "k", p); d2.Allowed {
			rt.Fatal("um nanossegundo antes ainda deveria negar")
		}
		clock.Advance(1)
		if d3, _ := m.Allow(ctx, "k", p); !d3.Allowed {
			rt.Fatalf("após RetryAfter deveria passar: %+v", d3)
		}
	})
}

// Property: over any span D from idle, no more than Burst + floor(D/T)
// requests pass (the sustained rate is never exceeded), and with attempts at
// least every T/2 the long-run throughput refills as expected.
func TestPropertyRateConformance(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(rt *rapid.T) {
		limit := rapid.IntRange(1, 50).Draw(rt, "limit")
		period := time.Duration(rapid.Int64Range(int64(limit)*int64(time.Millisecond), int64(10*time.Minute)).Draw(rt, "period"))
		burst := rapid.IntRange(0, limit).Draw(rt, "burst")
		p := Policy{Limit: limit, Period: period, Burst: burst}
		clock := newFakeClock()
		m := NewMemory(WithClock(clock.Now))
		ctx := context.Background()
		interval := p.Interval()

		steps := rapid.IntRange(1, 400).Draw(rt, "steps")
		maxGap := int64(interval / 2)
		var elapsed time.Duration
		allowed := 0
		for range steps {
			gap := time.Duration(rapid.Int64Range(0, maxGap).Draw(rt, "gap"))
			clock.Advance(gap)
			elapsed += gap
			d, err := m.Allow(ctx, "k", p)
			if err != nil {
				rt.Fatal(err)
			}
			if d.Allowed {
				allowed++
			}
			if d.Remaining < 0 || d.Remaining > p.BurstSize() {
				rt.Fatalf("remaining fora da faixa: %d", d.Remaining)
			}
			upper := p.BurstSize() + int(elapsed/interval)
			if allowed > upper {
				rt.Fatalf("permitidas %d > teto %d após %v", allowed, upper, elapsed)
			}
		}
	})
}

// Property: refill is exact in steady state. After the burst, attempts spaced
// T/k apart see exactly one in k allowed, so N attempts yield floor(N/k).
// A spacing of T or more means every attempt passes (k = 1).
func TestPropertySteadyStateThroughput(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(rt *rapid.T) {
		base := time.Duration(rapid.Int64Range(int64(time.Microsecond), int64(time.Second)).Draw(rt, "base"))
		k := rapid.IntRange(1, 10).Draw(rt, "k")
		limit := rapid.IntRange(1, 100).Draw(rt, "limit")
		burst := rapid.IntRange(0, limit).Draw(rt, "burst")
		interval := base * time.Duration(k)
		p := Policy{Limit: limit, Period: interval * time.Duration(limit), Burst: burst}
		clock := newFakeClock()
		m := NewMemory(WithClock(clock.Now))
		ctx := context.Background()
		for i := range p.BurstSize() {
			if d, _ := m.Allow(ctx, "k", p); !d.Allowed {
				rt.Fatalf("burst %d negado", i)
			}
		}
		n := rapid.IntRange(1, 500).Draw(rt, "n")
		allowed := 0
		for range n {
			clock.Advance(base)
			if d, _ := m.Allow(ctx, "k", p); d.Allowed {
				allowed++
			}
		}
		if allowed != n/k {
			rt.Fatalf("com espaçamento T/%d, %d tentativas deveriam render %d permitidas, não %d", k, n, n/k, allowed)
		}
	})
}

// Property: Remaining is exactly the number of further instant requests that
// will pass, for any key state.
func TestPropertyRemainingIsExact(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(rt *rapid.T) {
		limit := rapid.IntRange(1, 100).Draw(rt, "limit")
		burst := rapid.IntRange(1, limit).Draw(rt, "burst")
		p := Policy{Limit: limit, Period: time.Minute, Burst: burst}
		clock := newFakeClock()
		m := NewMemory(WithClock(clock.Now))
		ctx := context.Background()
		used := rapid.IntRange(0, burst).Draw(rt, "used")
		for range used {
			_, _ = m.Allow(ctx, "k", p)
		}
		clock.Advance(time.Duration(rapid.Int64Range(0, int64(p.Interval())*int64(burst)).Draw(rt, "advance")))
		d, _ := m.Allow(ctx, "k", p)
		if !d.Allowed {
			if d.Remaining != 0 {
				rt.Fatalf("negada com remaining %d", d.Remaining)
			}
			return
		}
		for i := 0; i < d.Remaining; i++ {
			if dd, _ := m.Allow(ctx, "k", p); !dd.Allowed {
				rt.Fatalf("remaining prometia %d, falhou na %d", d.Remaining, i+1)
			}
		}
		if dd, _ := m.Allow(ctx, "k", p); dd.Allowed {
			rt.Fatalf("após esgotar remaining=%d ainda passou", d.Remaining)
		}
	})
}
