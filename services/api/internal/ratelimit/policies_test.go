package ratelimit

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"
)

func TestDefaultsMatchBrief(t *testing.T) {
	t.Parallel()
	p := Defaults()
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		class Class
		plan  Plan
		want  Policy
		ok    bool
	}{
		{ClassAuth, PlanAnonymous, Policy{Limit: 5, Period: time.Minute}, true},
		{ClassRead, PlanAnonymous, Policy{Limit: 60, Period: time.Minute}, true},
		{ClassRead, PlanStarter, Policy{Limit: 300, Period: time.Minute}, true},
		{ClassRead, PlanBusiness, Policy{Limit: 300, Period: time.Minute}, true},
		{ClassAPIKey, PlanStarter, Policy{Limit: 600, Period: time.Minute}, true},
		{ClassAPIKey, PlanPro, Policy{Limit: 1500, Period: time.Minute}, true},
		{ClassAPIKey, PlanBusiness, Policy{Limit: 3000, Period: time.Minute}, true},
		{ClassAPIKey, PlanAnonymous, Policy{}, false},
		{ClassEstimate, PlanAnonymous, Policy{Limit: 10, Period: time.Minute}, true},
		{ClassEstimate, PlanStarter, Policy{Limit: 10, Period: time.Minute}, true},
		{ClassEstimate, PlanPro, Policy{Limit: 30, Period: time.Minute}, true},
		{ClassEstimate, PlanBusiness, Policy{Limit: 120, Period: time.Minute}, true},
		{ClassUpload, PlanStarter, Policy{Limit: 60, Period: time.Hour}, true},
		{ClassUpload, PlanAnonymous, Policy{}, false},
		{Class("outra"), PlanPro, Policy{}, false},
	}
	for _, tc := range cases {
		got, ok := p.For(tc.class, tc.plan)
		if ok != tc.ok || got != tc.want {
			t.Errorf("For(%s, %s) = %+v %v, esperado %+v %v", tc.class, tc.plan, got, ok, tc.want, tc.ok)
		}
	}
	if p.ConcurrentRuns[PlanStarter] != 1 || p.ConcurrentRuns[PlanPro] != 3 || p.ConcurrentRuns[PlanBusiness] != 10 {
		t.Fatalf("execuções simultâneas = %v", p.ConcurrentRuns)
	}
}

func TestPoliciesValidateCatchesBadEntries(t *testing.T) {
	t.Parallel()
	p := Defaults()
	p.APIKey[PlanPro] = Policy{}
	if err := p.Validate(); err == nil {
		t.Fatal("política de API inválida deveria falhar")
	}
	p = Defaults()
	p.ConcurrentRuns[PlanPro] = 0
	if err := p.Validate(); err == nil {
		t.Fatal("concorrência zero deveria falhar")
	}
}

// TestConcurrentRunsMatchPricing keeps the per-plan defaults aligned with
// config/pricing.yaml, the file the billing side reads.
func TestConcurrentRunsMatchPricing(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "config", "pricing.yaml"))
	if err != nil {
		t.Skipf("config/pricing.yaml indisponível: %v", err)
	}
	var doc struct {
		Plans map[string]struct {
			ConcurrentRuns int `yaml:"concurrent_runs"`
		} `yaml:"plans"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	p := Defaults()
	for name, plan := range doc.Plans {
		if got := p.ConcurrentRuns[Plan(name)]; got != plan.ConcurrentRuns {
			t.Errorf("plano %s: concurrent_runs = %d no YAML, %d em Defaults", name, plan.ConcurrentRuns, got)
		}
	}
	if len(doc.Plans) != len(p.ConcurrentRuns) {
		t.Errorf("planos no YAML (%d) e em Defaults (%d) divergem", len(doc.Plans), len(p.ConcurrentRuns))
	}
}

func TestIPKey(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"203.0.113.77":             "ip:203.0.113.0/24",
		"203.0.113.1":              "ip:203.0.113.0/24",
		"::ffff:203.0.113.9":       "ip:203.0.113.0/24",
		"2001:db8:1:2:3:4:5:6":     "ip:2001:db8:1:2::/64",
		"2001:db8:1:2:ffff:ffff::": "ip:2001:db8:1:2::/64",
		"2001:db8:1:3::1":          "ip:2001:db8:1:3::/64",
	}
	for in, want := range cases {
		if got := IPKey(netip.MustParseAddr(in)); got != want {
			t.Errorf("IPKey(%s) = %s, esperado %s", in, got, want)
		}
	}
	if IPKey(netip.Addr{}) != "ip:invalid" {
		t.Fatal("endereço inválido")
	}
}

func TestMemoryConcurrency(t *testing.T) {
	t.Parallel()
	c := NewMemoryConcurrency()
	ctx := context.Background()
	rel1, err := c.Acquire(ctx, "org", 2)
	if err != nil {
		t.Fatal(err)
	}
	rel2, err := c.Acquire(ctx, "org", 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Acquire(ctx, "org", 2); !errors.Is(err, ErrConcurrencyLimit) {
		t.Fatalf("terceira deveria falhar: %v", err)
	}
	if n, _ := c.Held(ctx, "org"); n != 2 {
		t.Fatalf("Held = %d", n)
	}
	if _, err := c.Acquire(ctx, "outra", 1); err != nil {
		t.Fatal("chaves são independentes")
	}
	rel1()
	rel1() // idempotent
	if n, _ := c.Held(ctx, "org"); n != 1 {
		t.Fatalf("Held após release = %d", n)
	}
	if _, err := c.Acquire(ctx, "org", 2); err != nil {
		t.Fatal("slot liberado deveria estar disponível")
	}
	rel2()
	if _, err := c.Acquire(ctx, "", 1); !errors.Is(err, ErrEmptyKey) {
		t.Fatal("chave vazia")
	}
	if _, err := c.Acquire(ctx, "org", 0); !errors.Is(err, ErrConcurrencyLimit) {
		t.Fatal("max zero nunca permite")
	}

	// Parallel acquisitions never exceed the cap.
	c2 := NewMemoryConcurrency()
	var wg sync.WaitGroup
	var mu sync.Mutex
	got := 0
	for range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c2.Acquire(ctx, "k", 10); err == nil {
				mu.Lock()
				got++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if got != 10 {
		t.Fatalf("100 aquisições paralelas com teto 10: %d passaram", got)
	}
}
