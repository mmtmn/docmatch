package estimate

import (
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"

	"github.com/mmtmn/docmatch/services/api/internal/estimate/pricing"
)

// Token mix assumed when a breakdown reports model tokens without splitting
// prompt and completion: completions are the expensive minority. The blend
// is conservative for typical extraction and adjudication prompts.
const (
	promptShare     = 0.8
	completionShare = 0.2
	tokensPerMTok   = 1_000_000
)

// DefaultCapRatio is the cap/expected ratio used until enough runs exist
// to compute the p99 overrun (and when the harness sends no calibration).
const DefaultCapRatio = 1.25

// MinOverrunSamples is how many finished runs the p99 needs to be trusted.
const MinOverrunSamples = 20

// ModelRates are the USD per million tokens the pricer applies to model
// items. They are the maximum over the latest model_prices rows, so the
// estimate never under-prices a run that escalates to the dearest model.
type ModelRates struct {
	InputUSDPerMTok     float64
	OutputUSDPerMTok    float64
	EmbeddingUSDPerMTok float64
}

// RatesFrom derives conservative rates from the latest price rows.
func RatesFrom(rows []ModelPrice) ModelRates {
	var r ModelRates
	for _, row := range rows {
		in := float64(row.InputCentsPerMTok) / 100
		out := float64(row.OutputCentsPerMTok) / 100
		if strings.Contains(strings.ToLower(row.Model), "embed") {
			r.EmbeddingUSDPerMTok = math.Max(r.EmbeddingUSDPerMTok, in)
			continue
		}
		r.InputUSDPerMTok = math.Max(r.InputUSDPerMTok, in)
		r.OutputUSDPerMTok = math.Max(r.OutputUSDPerMTok, out)
	}
	return r
}

// Pricer applies config/pricing.yaml to a breakdown.
type Pricer struct {
	P *pricing.Pricing
}

// ItemUSD returns the provider cost of one item in USD and whether it is a
// model cost (subject to the OpenRouter fee).
func (pr Pricer) ItemUSD(it Item, lane string, rates ModelRates) (usd float64, model bool) {
	e := pr.P.Estimates
	q := math.Max(it.Quantity, 0)
	switch it.Item {
	case ItemLLMTokens:
		return q / tokensPerMTok * (promptShare*rates.InputUSDPerMTok + completionShare*rates.OutputUSDPerMTok), true
	case ItemEmbeddings:
		return q / tokensPerMTok * rates.EmbeddingUSDPerMTok, true
	case ItemScanFiles:
		return q * e.ScanFileUSD, false
	case ItemAudioMinutes:
		if lane == LaneAPIBatch {
			return q * e.AudioMinuteBatchUSD, false
		}
		return q * e.AudioMinuteUSD, false
	case ItemVideoMinutes:
		return q * e.VideoMinuteUSD, false
	case ItemFrames:
		return q * e.FrameUSD, false
	case ItemNewTools:
		return q * e.NewToolUSD, false
	case ItemWebRequests:
		return q * e.WebSearchRequestUSD, false
	default:
		// documents: deterministic extraction is paid by the infra share of
		// the formula and by min_tokens_per_run.
		return 0, false
	}
}

// Price converts a work breakdown into credits:
//
//	credits(item) = ceil(cost_usd(item) / (divisor · token_usd))
//	subtotal      = Σ credits(item)
//	total         = max(ceil(subtotal · lane_multiplier), min_tokens_per_run)
//
// Model items carry the OpenRouter fee before conversion. Ceiling per item
// and the lane multiplier only ever raise revenue, so the margin of the
// formula holds for every cost vector (see TestPricingMarginProperty).
func (pr Pricer) Price(items []Item, lane string, rates ModelRates) (Breakdown, error) {
	return pr.PriceWithInstance(items, lane, rates, 0)
}

// instanceCarriers are the items that carry the instance minutes of a burst
// run, in order of preference: the heavy work the instance is there for.
var instanceCarriers = []string{ItemDocuments, ItemVideoMinutes, ItemAudioMinutes, ItemFrames}

// PriceWithInstance is Price with the minutes a run spends on a pool
// instance: their cost (estimates.instance_minute_usd) joins the first
// carrier item present (documents, then video, audio, frames, then the first
// item), with a note, so the breakdown keeps its items and its invariants.
func (pr Pricer) PriceWithInstance(items []Item, lane string, rates ModelRates, instanceMinutes float64) (Breakdown, error) {
	mult, ok := pr.P.LaneMultiplier(lane)
	if !ok {
		return Breakdown{}, fmt.Errorf("estimate: faixa desconhecida %q", lane)
	}
	carrier := -1
	if instanceMinutes > 0 && pr.P.Estimates.InstanceMinuteUSD > 0 && len(items) > 0 {
		carrier = 0
		for _, name := range instanceCarriers {
			if i := slices.IndexFunc(items, func(it Item) bool { return it.Item == name }); i >= 0 {
				carrier = i
				break
			}
		}
	}
	out := make([]Item, 0, len(items))
	var subtotal int64
	for i, it := range items {
		usd, model := pr.ItemUSD(it, lane, rates)
		if model {
			usd = pr.P.ModelCost(usd)
		}
		if i == carrier {
			usd += instanceMinutes * pr.P.Estimates.InstanceMinuteUSD
			note := fmt.Sprintf("inclui %.0f min de instância dedicada", math.Ceil(instanceMinutes))
			if it.Note != "" {
				note = it.Note + "; " + note
			}
			it.Note = note
		}
		it.Credits = pr.P.Tokens(usd)
		subtotal += it.Credits
		out = append(out, it)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Credits > out[j].Credits })
	total := int64(math.Ceil(float64(subtotal) * mult))
	if total < pr.P.MinTokensPerRun {
		total = pr.P.MinTokensPerRun
	}
	if out == nil {
		out = []Item{}
	}
	return Breakdown{
		Lane: lane, LaneMultiplier: mult, Items: out, SubtotalCredits: subtotal,
		MinimumCredits: pr.P.MinTokensPerRun, TotalCredits: total, PricingVersion: pr.P.Version(),
	}, nil
}

// Cap is the reserved ceiling: expected × overrun ratio, never below expected.
func Cap(expected int64, ratio float64) int64 {
	if ratio < 1 || math.IsNaN(ratio) || math.IsInf(ratio, 0) {
		ratio = 1
	}
	return int64(math.Ceil(float64(expected) * ratio))
}

// P99Overrun computes the 99th percentile of spent/estimated over finished
// runs; ok=false when there are too few samples.
func P99Overrun(samples []Overrun) (float64, bool) {
	ratios := make([]float64, 0, len(samples))
	for _, s := range samples {
		if s.Estimated > 0 {
			ratios = append(ratios, float64(s.Spent)/float64(s.Estimated))
		}
	}
	if len(ratios) < MinOverrunSamples {
		return 0, false
	}
	sort.Float64s(ratios)
	idx := int(math.Ceil(0.99*float64(len(ratios)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(ratios) {
		idx = len(ratios) - 1
	}
	return math.Max(ratios[idx], 1), true
}

// MarginAfterTax computes the margin of revenue after payment, infra, cost
// and income tax, as a share of revenue (the check of plano-mestre §5).
func MarginAfterTax(p *pricing.Pricing, credits int64, costUSD float64) float64 {
	revenue := float64(credits) * p.TokenUSD
	if revenue <= 0 {
		return 0
	}
	r := p.Rates
	before := revenue*(1-r.Payment-r.Infra) - costUSD
	return before * (1 - r.IncomeTax) / revenue
}
