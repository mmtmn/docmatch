package ratelimit

import (
	"context"
	"errors"
	"net/netip"
	"testing"
	"time"
)

func TestLockoutPolicyDuration(t *testing.T) {
	t.Parallel()
	p := LockoutPolicy{Base: time.Minute, Max: time.Hour, Forget: 24 * time.Hour}
	want := []time.Duration{0, time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 16 * time.Minute, 32 * time.Minute, time.Hour, time.Hour, time.Hour}
	for strikes, d := range want {
		if got := p.Duration(strikes); got != d {
			t.Errorf("Duration(%d) = %v, esperado %v", strikes, got, d)
		}
	}
	if p.Duration(100) != time.Hour || p.Duration(-1) != 0 {
		t.Fatal("teto/negativo")
	}
	small := LockoutPolicy{Base: 40 * time.Minute, Max: time.Hour}
	if small.Duration(2) != time.Hour {
		t.Fatalf("dobro acima do teto deveria saturar: %v", small.Duration(2))
	}
}

func TestLockoutStrikesDoubleAndForget(t *testing.T) {
	t.Parallel()
	clock := newFakeClock()
	l := NewLockout(LockoutPolicy{Base: time.Minute, Max: 8 * time.Minute, Forget: time.Hour}, clock.Now)
	if l.Remaining("k") != 0 || l.Strikes("k") != 0 {
		t.Fatal("chave nova não deveria estar bloqueada")
	}
	if d := l.Strike("k"); d != time.Minute {
		t.Fatalf("primeiro strike = %v", d)
	}
	if l.Remaining("k") != time.Minute {
		t.Fatalf("Remaining = %v", l.Remaining("k"))
	}
	clock.Advance(30 * time.Second)
	if l.Remaining("k") != 30*time.Second {
		t.Fatalf("Remaining após 30s = %v", l.Remaining("k"))
	}
	clock.Advance(31 * time.Second)
	if l.Remaining("k") != 0 {
		t.Fatal("bloqueio expirado deveria liberar")
	}
	if d := l.Strike("k"); d != 2*time.Minute {
		t.Fatalf("segundo strike = %v", d)
	}
	for _, want := range []time.Duration{4 * time.Minute, 8 * time.Minute, 8 * time.Minute} {
		if d := l.Strike("k"); d != want {
			t.Fatalf("strike = %v, esperado %v", d, want)
		}
	}
	if l.Strikes("k") != 5 {
		t.Fatalf("Strikes = %d", l.Strikes("k"))
	}
	clock.Advance(2 * time.Hour) // beyond Forget
	if l.Remaining("k") != 0 {
		t.Fatal("após Forget não há bloqueio")
	}
	if d := l.Strike("k"); d != time.Minute {
		t.Fatalf("após Forget a contagem recomeça: %v", d)
	}
	l.Clear("k")
	if l.Strikes("k") != 0 || l.Remaining("k") != 0 {
		t.Fatal("Clear deveria esquecer a chave")
	}
	// Strike after Forget via Strike path (entry exists but is stale).
	l.Strike("z")
	clock.Advance(2 * time.Hour)
	if d := l.Strike("z"); d != time.Minute {
		t.Fatalf("entrada antiga deveria recomeçar: %v", d)
	}
	if NewLockout(DefaultLockoutPolicy(), nil).now == nil {
		t.Fatal("relógio padrão ausente")
	}
}

func TestAuthGuard(t *testing.T) {
	t.Parallel()
	clock := newFakeClock()
	limiter := NewMemory(WithClock(clock.Now))
	lock := NewLockout(LockoutPolicy{Base: time.Minute, Max: time.Hour, Forget: 24 * time.Hour}, clock.Now)
	g := NewAuthGuard(limiter, lock, Policy{Limit: 5, Period: time.Minute})
	ctx := context.Background()
	key := AuthKey(netip.MustParseAddr("203.0.113.5"), " Maria@Example.com ")
	if key != AuthKey(netip.MustParseAddr("203.0.113.200"), "maria@example.com") {
		t.Fatal("chave deveria normalizar e-mail e agrupar o /24")
	}
	if key == AuthKey(netip.MustParseAddr("203.0.114.5"), "maria@example.com") {
		t.Fatal("outro /24 deveria gerar outra chave")
	}
	for i := range 5 {
		d, err := g.Allow(ctx, key)
		if err != nil || !d.Allowed {
			t.Fatalf("tentativa %d: %+v %v", i, d, err)
		}
	}
	d, _ := g.Allow(ctx, key)
	if d.Allowed || d.RetryAfter != time.Minute || lock.Strikes(key) != 1 {
		t.Fatalf("sexta tentativa deveria bloquear por 1min: %+v strikes=%d", d, lock.Strikes(key))
	}
	// While locked, attempts are refused without new strikes.
	clock.Advance(30 * time.Second)
	d, _ = g.Allow(ctx, key)
	if d.Allowed || d.RetryAfter != 30*time.Second || lock.Strikes(key) != 1 || d.Remaining != 0 {
		t.Fatalf("durante o bloqueio: %+v strikes=%d", d, lock.Strikes(key))
	}
	// After the lock, the rate still applies; a burst of violations doubles it.
	clock.Advance(31 * time.Second)
	for range 5 {
		_, _ = g.Allow(ctx, key)
	}
	d, _ = g.Allow(ctx, key)
	if d.Allowed || d.RetryAfter != 2*time.Minute || lock.Strikes(key) != 2 {
		t.Fatalf("segundo bloqueio deveria durar 2min: %+v strikes=%d", d, lock.Strikes(key))
	}
	if err := g.Succeed(ctx, key); err != nil {
		t.Fatal(err)
	}
	if d, _ = g.Allow(ctx, key); !d.Allowed || d.Remaining != 4 {
		t.Fatalf("após login bem-sucedido tudo zera: %+v", d)
	}
}

func TestAuthGuardLimiterError(t *testing.T) {
	t.Parallel()
	g := NewAuthGuard(failingLimiter{}, NewLockout(DefaultLockoutPolicy(), nil), Policy{Limit: 5, Period: time.Minute})
	if _, err := g.Allow(context.Background(), "k"); !errors.Is(err, errLimiter) {
		t.Fatalf("erro do limitador deveria propagar: %v", err)
	}
}

var errLimiter = errors.New("valkey fora do ar")

type failingLimiter struct{}

func (failingLimiter) Allow(context.Context, string, Policy) (Decision, error) {
	return Decision{}, errLimiter
}

func (failingLimiter) AllowN(context.Context, string, Policy, int) (Decision, error) {
	return Decision{}, errLimiter
}

func (failingLimiter) Reset(context.Context, string) error { return nil }
