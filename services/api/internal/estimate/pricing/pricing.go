// Package pricing parses config/pricing.yaml: the token price, the rates of
// the margin formula, lane multipliers, plan limits and the unit costs the
// estimator uses. Every number the estimator applies comes from here (or
// from the model_prices table); nothing is typed elsewhere.
package pricing

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"os"
	"sort"
	"time"

	"go.yaml.in/yaml/v3"
)

// Pricing is the parsed file.
type Pricing struct {
	TokenBRL        float64            `yaml:"token_brl"`
	TokenUSD        float64            `yaml:"token_usd"`
	Rates           Rates              `yaml:"rates"`
	MinTokensPerRun int64              `yaml:"min_tokens_per_run"`
	QuoteTTLMinutes int                `yaml:"quote_ttl_minutes"`
	QuoteCapPercent float64            `yaml:"quote_cap_percentile"`
	Lanes           map[string]float64 `yaml:"lanes"`
	Plans           map[string]Plan    `yaml:"plans"`
	Rollover        Rollover           `yaml:"rollover"`
	Estimates       Estimates          `yaml:"estimates"`
	Burst           Burst              `yaml:"burst"`
	version         string
}

// Burst is the pricing hook of the pool of on-demand workers (docs/escala.md):
// with ChargeInstanceMinutes, the minutes a heavy run spends on a pool
// instance are priced at estimates.instance_minute_usd and folded into its
// heavy work; off, the instances are paid by the infra share of the formula.
type Burst struct {
	ChargeInstanceMinutes bool `yaml:"charge_instance_minutes"`
}

// Rates are the components of the price formula.
type Rates struct {
	TargetMargin  float64 `yaml:"target_margin"`
	IncomeTax     float64 `yaml:"income_tax"`
	Payment       float64 `yaml:"payment"`
	Infra         float64 `yaml:"infra"`
	OpenRouterFee float64 `yaml:"openrouter_fee"`
}

// Plan is one row of `plans`.
type Plan struct {
	PriceBRL                 float64  `yaml:"price_brl"`
	Tokens                   int64    `yaml:"tokens"`
	DocumentFilesPerRun      int      `yaml:"document_files_per_run"`
	DocumentFileMB           int64    `yaml:"document_file_mb"`
	DocumentRunMB            int64    `yaml:"document_run_mb"`
	MediaFilesPerRun         int      `yaml:"media_files_per_run"`
	MediaRunGB               int64    `yaml:"media_run_gb"`
	MediaRunHours            int64    `yaml:"media_run_hours"`
	ConcurrentRuns           int      `yaml:"concurrent_runs"`
	APIKeys                  int      `yaml:"api_keys"`
	APILanes                 []string `yaml:"api_lanes"`
	AutoRuleLearning         bool     `yaml:"auto_rule_learning"`
	UICustomizationsPerMonth int      `yaml:"ui_customizations_per_month"`
	EscalationModel          bool     `yaml:"escalation_model"`
	RetentionDays            int      `yaml:"retention_days"`
}

// Rollover describes credit validity.
type Rollover struct {
	PlanTokensCycles  int `yaml:"plan_tokens_cycles"`
	TopupTokensMonths int `yaml:"topup_tokens_months"`
}

// Estimates are the USD unit costs of non-model work.
type Estimates struct {
	AudioMinuteUSD      float64 `yaml:"audio_minute_usd"`
	AudioMinuteBatchUSD float64 `yaml:"audio_minute_batch_usd"`
	VideoMinuteUSD      float64 `yaml:"video_minute_usd"`
	FrameUSD            float64 `yaml:"frame_usd"`
	NewToolUSD          float64 `yaml:"new_tool_usd"`
	ScanFileUSD         float64 `yaml:"scan_file_usd"`
	WebSearchRequestUSD float64 `yaml:"web_search_request_usd"`
	// InstanceMinuteUSD is one minute of a pool instance (worst case: the
	// on-demand price of the largest type, its disk and its public IPv4).
	InstanceMinuteUSD float64 `yaml:"instance_minute_usd"`
}

// Overrides replace file rates when set (DM_TARGET_MARGIN etc.).
type Overrides struct {
	TargetMargin  *float64
	IncomeTax     *float64
	Payment       *float64
	Infra         *float64
	OpenRouterFee *float64
}

// Load reads and validates the file, applying overrides.
func Load(path string, o Overrides) (*Pricing, error) {
	data, err := os.ReadFile(path) //nolint:gosec // path comes from DM_PRICING_FILE
	if err != nil {
		return nil, fmt.Errorf("pricing: ler %s: %w", path, err)
	}
	return Parse(data, o)
}

// Parse parses the YAML and applies overrides.
func Parse(data []byte, o Overrides) (*Pricing, error) {
	var p Pricing
	if err := yaml.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("pricing: yaml inválido: %w", err)
	}
	if o.TargetMargin != nil {
		p.Rates.TargetMargin = *o.TargetMargin
	}
	if o.IncomeTax != nil {
		p.Rates.IncomeTax = *o.IncomeTax
	}
	if o.Payment != nil {
		p.Rates.Payment = *o.Payment
	}
	if o.Infra != nil {
		p.Rates.Infra = *o.Infra
	}
	if o.OpenRouterFee != nil {
		p.Rates.OpenRouterFee = *o.OpenRouterFee
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	p.version = "pricing-" + hex.EncodeToString(sum[:8])
	return &p, nil
}

// Validate checks the numbers make a workable formula.
func (p *Pricing) Validate() error {
	var problems []error
	if p.TokenUSD <= 0 {
		problems = append(problems, errors.New("token_usd precisa ser positivo"))
	}
	if p.TokenBRL <= 0 || math.IsNaN(p.TokenBRL) || math.IsInf(p.TokenBRL, 0) || math.Abs(p.TokenBRL*100-math.Round(p.TokenBRL*100)) > 1e-8 {
		problems = append(problems, errors.New("token_brl precisa ser positivo em centavos inteiros"))
	}
	for code, plan := range p.Plans {
		if plan.PriceBRL <= 0 || math.IsNaN(plan.PriceBRL) || math.IsInf(plan.PriceBRL, 0) || math.Abs(plan.PriceBRL*100-math.Round(plan.PriceBRL*100)) > 1e-8 {
			problems = append(problems, fmt.Errorf("plans.%s.price_brl precisa ser positivo em centavos inteiros", code))
		}
	}
	r := p.Rates
	for name, v := range map[string]float64{"target_margin": r.TargetMargin, "income_tax": r.IncomeTax, "payment": r.Payment, "infra": r.Infra, "openrouter_fee": r.OpenRouterFee} {
		if v < 0 || v >= 1 || math.IsNaN(v) {
			problems = append(problems, fmt.Errorf("rates.%s precisa estar entre 0 e 1", name))
		}
	}
	if p.Estimates.InstanceMinuteUSD < 0 || math.IsNaN(p.Estimates.InstanceMinuteUSD) {
		problems = append(problems, errors.New("estimates.instance_minute_usd precisa ser >= 0"))
	}
	if p.Burst.ChargeInstanceMinutes && p.Estimates.InstanceMinuteUSD <= 0 {
		problems = append(problems, errors.New("burst.charge_instance_minutes precisa de estimates.instance_minute_usd positivo"))
	}
	if p.Divisor() <= 0 {
		problems = append(problems, errors.New("as taxas somadas não deixam margem (divisor <= 0)"))
	}
	if p.MinTokensPerRun < 0 {
		problems = append(problems, errors.New("min_tokens_per_run negativo"))
	}
	if p.QuoteTTLMinutes <= 0 {
		problems = append(problems, errors.New("quote_ttl_minutes precisa ser positivo"))
	}
	if len(p.Lanes) == 0 {
		problems = append(problems, errors.New("lanes vazio"))
	}
	for lane, m := range p.Lanes {
		if m < 1 {
			problems = append(problems, fmt.Errorf("lanes.%s precisa ser >= 1", lane))
		}
	}
	if len(p.Plans) == 0 {
		problems = append(problems, errors.New("plans vazio"))
	}
	if len(problems) > 0 {
		return fmt.Errorf("pricing: %w", errors.Join(problems...))
	}
	return nil
}

// Version identifies the table for audit (estimate-breakdown pricing_version).
func (p *Pricing) Version() string { return p.version }

// Divisor is the share of revenue left for model and compute costs:
//
//	1 − margin/(1 − tax) − payment − infra
//
// Revenue R = cost/Divisor guarantees (R(1−payment−infra) − cost)(1−tax) ≥ margin·R.
func (p *Pricing) Divisor() float64 {
	r := p.Rates
	return 1 - r.TargetMargin/(1-r.IncomeTax) - r.Payment - r.Infra
}

// Tokens converts a cost in USD (model cost already includes the OpenRouter
// fee; see ModelCost) into docmatch tokens: ceil(cost / (divisor · token_usd)).
func (p *Pricing) Tokens(costUSD float64) int64 {
	if costUSD <= 0 || math.IsNaN(costUSD) || math.IsInf(costUSD, 0) {
		return 0
	}
	return int64(math.Ceil(costUSD / (p.Divisor() * p.TokenUSD)))
}

// ModelCost applies the OpenRouter fee to a provider cost.
func (p *Pricing) ModelCost(providerUSD float64) float64 {
	return providerUSD * (1 + p.Rates.OpenRouterFee)
}

// LaneMultiplier returns the multiplier of lane (ok=false for unknown lanes).
func (p *Pricing) LaneMultiplier(lane string) (float64, bool) {
	m, ok := p.Lanes[lane]
	return m, ok
}

// Plan returns the limits of a plan code.
func (p *Pricing) Plan(code string) (Plan, bool) {
	pl, ok := p.Plans[code]
	return pl, ok
}

// QuoteTTL is the validity of a quote.
func (p *Pricing) QuoteTTL() time.Duration { return time.Duration(p.QuoteTTLMinutes) * time.Minute }

// PlanCodes lists the plans, sorted.
func (p *Pricing) PlanCodes() []string {
	out := make([]string, 0, len(p.Plans))
	for c := range p.Plans {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}
