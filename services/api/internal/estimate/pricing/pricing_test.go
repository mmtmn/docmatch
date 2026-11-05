package pricing

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// repoFile finds a repo-relative file from the package directory.
func repoFile(t *testing.T, rel string) string {
	t.Helper()
	dir, _ := os.Getwd()
	for i := 0; i < 8; i++ {
		p := filepath.Join(dir, rel)
		if _, err := os.Stat(p); err == nil {
			return p
		}
		dir = filepath.Dir(dir)
	}
	t.Skipf("%s não encontrado", rel)
	return ""
}

func TestLoadRepoPricing(t *testing.T) {
	t.Parallel()
	p, err := Load(repoFile(t, "config/pricing.yaml"), Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	// plano-mestre §5: denominador 0,444253 e 107 tokens para US$ 0,42 + 0,03.
	if d := p.Divisor(); math.Abs(d-0.444253) > 1e-5 {
		t.Fatalf("divisor = %f", d)
	}
	cost := p.ModelCost(0.42) + 0.03
	if got := p.Tokens(cost); got != 107 {
		t.Fatalf("tokens = %d, esperado 107", got)
	}
	if p.Tokens(0) != 0 || p.Tokens(math.NaN()) != 0 || p.Tokens(math.Inf(1)) != 0 {
		t.Fatal("custo inválido deveria dar 0 tokens")
	}
	if m, ok := p.LaneMultiplier("api_priority"); !ok || m != 4.5 {
		t.Fatalf("lane = %v, %v", m, ok)
	}
	if _, ok := p.LaneMultiplier("teleport"); ok {
		t.Fatal("faixa desconhecida")
	}
	if pl, ok := p.Plan("starter"); !ok || pl.ConcurrentRuns != 1 || pl.RetentionDays != 30 || len(pl.APILanes) != 1 {
		t.Fatalf("plano starter = %+v", pl)
	}
	if p.QuoteTTL().Minutes() != 15 || p.MinTokensPerRun != 10 {
		t.Fatalf("ttl/min = %v/%d", p.QuoteTTL(), p.MinTokensPerRun)
	}
	if codes := p.PlanCodes(); strings.Join(codes, ",") != "business,pro,starter" {
		t.Fatalf("planos = %v", codes)
	}
	if !strings.HasPrefix(p.Version(), "pricing-") {
		t.Fatalf("versão = %s", p.Version())
	}
	if p.Estimates.NewToolUSD <= 0 || p.Estimates.ScanFileUSD <= 0 {
		t.Fatal("estimativas não carregadas")
	}
}

func TestOverridesAndValidation(t *testing.T) {
	t.Parallel()
	base := `
token_usd: 0.01
rates: {target_margin: 0.30, income_tax: 0.21, payment: 0.076, infra: 0.10, openrouter_fee: 0.055}
min_tokens_per_run: 10
quote_ttl_minutes: 15
lanes: {app: 1.0}
plans: {starter: {price_usd: 20, tokens: 2000}}
`
	margin := 0.5
	p, err := Parse([]byte(base), Overrides{TargetMargin: &margin})
	if err != nil {
		t.Fatal(err)
	}
	if p.Rates.TargetMargin != 0.5 {
		t.Fatal("override não aplicado")
	}
	bad := []struct {
		name string
		yaml string
	}{
		{"token zero", strings.Replace(base, "token_usd: 0.01", "token_usd: 0", 1)},
		{"taxa fora de 0..1", strings.Replace(base, "payment: 0.076", "payment: 1.5", 1)},
		{"sem margem", strings.Replace(base, "target_margin: 0.30", "target_margin: 0.9", 1)},
		{"ttl zero", strings.Replace(base, "quote_ttl_minutes: 15", "quote_ttl_minutes: 0", 1)},
		{"lane < 1", strings.Replace(base, "app: 1.0", "app: 0.5", 1)},
		{"sem planos", strings.Replace(base, "plans: {starter: {price_usd: 20, tokens: 2000}}", "plans: {}", 1)},
		{"yaml inválido", "token_usd: [}"},
	}
	for _, tc := range bad {
		if _, err := Parse([]byte(tc.yaml), Overrides{}); err == nil {
			t.Errorf("%s deveria falhar", tc.name)
		}
	}
	if _, err := Load(filepath.Join(t.TempDir(), "nope.yaml"), Overrides{}); err == nil {
		t.Fatal("arquivo ausente deveria falhar")
	}
}
