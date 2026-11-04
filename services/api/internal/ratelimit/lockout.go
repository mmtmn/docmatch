package ratelimit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/netip"
	"strings"
	"sync"
	"time"
)

// LockoutPolicy shapes the exponential lockout: the first strike locks for
// Base, each further strike doubles it up to Max, and strikes are forgotten
// after Forget without new ones.
type LockoutPolicy struct {
	Base   time.Duration
	Max    time.Duration
	Forget time.Duration
}

// DefaultLockoutPolicy locks 1 min, 2 min, 4 min ... up to 1 h and forgets
// after 24 h of calm.
func DefaultLockoutPolicy() LockoutPolicy {
	return LockoutPolicy{Base: time.Minute, Max: time.Hour, Forget: 24 * time.Hour}
}

// Duration returns the lock length for the n-th strike (n ≥ 1).
func (p LockoutPolicy) Duration(strikes int) time.Duration {
	if strikes < 1 {
		return 0
	}
	d := p.Base
	for i := 1; i < strikes; i++ {
		if d >= p.Max/2 {
			return p.Max
		}
		d *= 2
	}
	return min(d, p.Max)
}

type lockEntry struct {
	strikes    int
	until      time.Time
	lastStrike time.Time
}

// Lockout tracks strikes per key with exponential lock durations. The in-
// memory version is enough for one node; the Valkey version mirrors it.
type Lockout struct {
	policy  LockoutPolicy
	now     Clock
	mu      sync.Mutex
	entries map[string]*lockEntry
}

// NewLockout returns an empty tracker.
func NewLockout(p LockoutPolicy, clock Clock) *Lockout {
	if clock == nil {
		clock = time.Now
	}
	return &Lockout{policy: p, now: clock, entries: map[string]*lockEntry{}}
}

// Remaining returns how long key stays locked (zero when not locked).
func (l *Lockout) Remaining(key string) time.Duration {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.entries[key]
	if e == nil {
		return 0
	}
	if now.Sub(e.lastStrike) > l.policy.Forget {
		delete(l.entries, key)
		return 0
	}
	if e.until.After(now) {
		return e.until.Sub(now)
	}
	return 0
}

// Strike records one violation and returns the resulting lock duration.
func (l *Lockout) Strike(key string) time.Duration {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.entries[key]
	if e == nil || now.Sub(e.lastStrike) > l.policy.Forget {
		e = &lockEntry{}
		l.entries[key] = e
	}
	e.strikes++
	e.lastStrike = now
	d := l.policy.Duration(e.strikes)
	e.until = now.Add(d)
	return d
}

// Strikes returns the strike count for key.
func (l *Lockout) Strikes(key string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	if e := l.entries[key]; e != nil {
		return e.strikes
	}
	return 0
}

// Clear forgets key (successful authentication).
func (l *Lockout) Clear(key string) {
	l.mu.Lock()
	delete(l.entries, key)
	l.mu.Unlock()
}

// AuthGuard combines the auth rate policy (5/min per IP+email) with the
// exponential lockout: exceeding the rate adds a strike and locks the pair
// for Base·2^(strikes-1), so password guessing slows down geometrically.
type AuthGuard struct {
	limiter Limiter
	lockout *Lockout
	policy  Policy
}

// NewAuthGuard wires a limiter and a lockout with the auth policy.
func NewAuthGuard(l Limiter, lock *Lockout, p Policy) *AuthGuard {
	return &AuthGuard{limiter: l, lockout: lock, policy: p}
}

// AuthKey derives the key for an IP (bucketed by prefix) and an e-mail. The
// pair is hashed so raw e-mails never sit in limiter memory or in Valkey.
func AuthKey(ip netip.Addr, email string) string {
	sum := sha256.Sum256([]byte(IPKey(ip) + "|" + strings.ToLower(strings.TrimSpace(email))))
	return "auth:" + hex.EncodeToString(sum[:16])
}

// Allow checks the lock first, then the rate. A rate violation adds a strike.
func (g *AuthGuard) Allow(ctx context.Context, key string) (Decision, error) {
	if rem := g.lockout.Remaining(key); rem > 0 {
		return Decision{Allowed: false, Limit: g.policy.BurstSize(), Remaining: 0, RetryAfter: rem, ResetAfter: rem}, nil
	}
	d, err := g.limiter.Allow(ctx, key, g.policy)
	if err != nil {
		return d, err
	}
	if !d.Allowed {
		lock := g.lockout.Strike(key)
		d.RetryAfter = max(d.RetryAfter, lock)
		d.ResetAfter = max(d.ResetAfter, lock)
	}
	return d, nil
}

// Succeed clears the lock and the rate state after a successful login.
func (g *AuthGuard) Succeed(ctx context.Context, key string) error {
	g.lockout.Clear(key)
	return g.limiter.Reset(ctx, key)
}
