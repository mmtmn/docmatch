// Package ratelimit implements the generic cell rate algorithm (GCRA) behind
// a small interface, the route classes and per-plan defaults of the API, an
// exponential lockout for authentication, a concurrency limiter for runs and
// the HTTP middleware that emits RateLimit-* headers.
//
// GCRA keeps one timestamp per key, the theoretical arrival time (TAT). A
// policy of Limit requests per Period has an emission interval T =
// Period/Limit; a key may burst Burst requests instantly and then sustains
// one request per T. State is a single time value, which is why a Valkey
// implementation (plano, fase de escala) can replace the in-memory one with
// a Lua script and no change elsewhere.
package ratelimit

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Policy is a rate: Limit requests per Period with an instantaneous Burst.
type Policy struct {
	Limit  int
	Period time.Duration
	// Burst is how many requests pass instantly from idle; zero means Limit.
	Burst int
}

// Validate checks the policy is usable.
func (p Policy) Validate() error {
	if p.Limit <= 0 || p.Period <= 0 || p.Burst < 0 {
		return fmt.Errorf("ratelimit: política inválida %+v", p)
	}
	if p.Period/time.Duration(p.Limit) <= 0 {
		return fmt.Errorf("ratelimit: período %v curto demais para %d requisições", p.Period, p.Limit)
	}
	return nil
}

// Interval is the emission interval T = Period / Limit.
func (p Policy) Interval() time.Duration { return p.Period / time.Duration(p.Limit) }

// BurstSize returns the effective burst.
func (p Policy) BurstSize() int {
	if p.Burst > 0 {
		return p.Burst
	}
	return p.Limit
}

// String renders the policy as the RateLimit-Policy header value, e.g. "60;w=60".
func (p Policy) String() string {
	return fmt.Sprintf("%d;w=%d", p.Limit, int(p.Period/time.Second))
}

// Decision is the outcome of one Allow call.
type Decision struct {
	Allowed bool
	// Limit is the burst capacity reported as RateLimit-Limit.
	Limit int
	// Remaining is how many requests would pass right now.
	Remaining int
	// RetryAfter is how long until one request passes (zero when allowed).
	RetryAfter time.Duration
	// ResetAfter is how long until the key is fully replenished.
	ResetAfter time.Duration
}

// Errors returned by limiters.
var (
	ErrInvalidCost = errors.New("ratelimit: custo precisa ser positivo e no máximo o burst")
	ErrEmptyKey    = errors.New("ratelimit: chave vazia")
)

// Limiter decides whether a request identified by key may proceed under p.
// Implementations must be safe for concurrent use.
type Limiter interface {
	// Allow consumes one unit for key.
	Allow(ctx context.Context, key string, p Policy) (Decision, error)
	// AllowN consumes n units for key (n ≤ burst).
	AllowN(ctx context.Context, key string, p Policy, n int) (Decision, error)
	// Reset forgets key (successful login, manual unblock).
	Reset(ctx context.Context, key string) error
}

// Clock returns the current time; tests substitute a fake.
type Clock func() time.Time

// gcra applies the algorithm to a stored TAT and returns the new TAT with the
// decision. It is pure so any backend can reuse it.
func gcra(tat, now time.Time, p Policy, n int) (time.Time, Decision) {
	t := p.Interval()
	burst := p.BurstSize()
	tau := t * time.Duration(burst)
	if tat.Before(now) {
		tat = now
	}
	d := Decision{Limit: burst}
	candidate := tat.Add(t * time.Duration(n))
	allowAt := candidate.Add(-tau)
	if allowAt.After(now) {
		d.RetryAfter = allowAt.Sub(now)
		d.Remaining = max(0, int(now.Add(tau).Sub(tat)/t))
		d.ResetAfter = tat.Sub(now)
		return tat, d
	}
	d.Allowed = true
	d.Remaining = int(now.Add(tau).Sub(candidate) / t)
	d.ResetAfter = candidate.Sub(now)
	return candidate, d
}

// Memory is the in-process Limiter: one TAT per key under a mutex, with
// opportunistic sweeping of keys that are fully replenished.
type Memory struct {
	now   Clock
	mu    sync.Mutex
	tat   map[string]time.Time
	ops   int
	sweep int
}

// MemoryOption configures NewMemory.
type MemoryOption func(*Memory)

// WithClock substitutes the time source (tests).
func WithClock(c Clock) MemoryOption { return func(m *Memory) { m.now = c } }

// WithSweepEvery sets how many operations happen between sweeps (default 1024).
func WithSweepEvery(n int) MemoryOption {
	return func(m *Memory) {
		if n > 0 {
			m.sweep = n
		}
	}
}

// NewMemory returns an empty in-memory limiter.
func NewMemory(opts ...MemoryOption) *Memory {
	m := &Memory{now: time.Now, tat: map[string]time.Time{}, sweep: 1024}
	for _, o := range opts {
		o(m)
	}
	return m
}

// Allow implements Limiter.
func (m *Memory) Allow(ctx context.Context, key string, p Policy) (Decision, error) {
	return m.AllowN(ctx, key, p, 1)
}

// AllowN implements Limiter.
func (m *Memory) AllowN(_ context.Context, key string, p Policy, n int) (Decision, error) {
	if key == "" {
		return Decision{}, ErrEmptyKey
	}
	if err := p.Validate(); err != nil {
		return Decision{}, err
	}
	if n <= 0 || n > p.BurstSize() {
		return Decision{}, ErrInvalidCost
	}
	now := m.now()
	m.mu.Lock()
	defer m.mu.Unlock()
	tat, d := gcra(m.tat[key], now, p, n)
	if d.Allowed {
		m.tat[key] = tat
	}
	m.ops++
	if m.ops >= m.sweep {
		m.ops = 0
		for k, v := range m.tat {
			if !v.After(now) {
				delete(m.tat, k)
			}
		}
	}
	return d, nil
}

// Reset implements Limiter.
func (m *Memory) Reset(_ context.Context, key string) error {
	m.mu.Lock()
	delete(m.tat, key)
	m.mu.Unlock()
	return nil
}

// Len reports how many keys are tracked (tests and metrics).
func (m *Memory) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.tat)
}
