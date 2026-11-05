package estimate

import (
	"context"
	"strings"
	"testing"

	"github.com/mmtmn/docmatch/services/api/internal/estimate/pricing"
	"github.com/mmtmn/docmatch/services/api/internal/estimate/throughput"
	"github.com/mmtmn/docmatch/services/api/internal/harness/harnesstest"
	"github.com/mmtmn/docmatch/services/api/internal/uploads"
)

// placeAll sends heavy runs (by main-box time) to the pool, warm or cold.
type placeAll struct {
	warm bool
	seen []PlaceInput
}

func (p *placeAll) Place(_ context.Context, in PlaceInput) Placement {
	p.seen = append(p.seen, in)
	if in.LocalSeconds < 60 {
		return Placement{Reason: ReasonLight}
	}
	return Placement{Burst: true, Reason: ReasonTime, Warm: p.warm}
}

func TestQuoteShowsTheTimeWhereTheRunWillExecute(t *testing.T) {
	t.Parallel()
	f := newFixture(t, harnesstest.Options{})
	cfg := f.svc.Throughput()
	placer := &placeAll{}
	f.svc.d.Placer = placer
	uri := func(u uploads.Upload) string { return "local://b/" + u.StorageKey }

	small := f.upload(t, uploads.KindDocument, uploads.StatusClean, 1000)
	q, err := f.svc.Estimate(context.Background(), f.principal(), Request{Sources: []SourceRef{{UploadID: small.PublicID()}}}, uri)
	if err != nil {
		t.Fatal(err)
	}
	light := f.svc.Timing(context.Background(), placer.seen[0].Units)
	if q.ExpectedSeconds != light.LocalSeconds || light.Placement.Burst {
		t.Fatalf("leve fica local: %d vs %+v", q.ExpectedSeconds, light)
	}

	big := f.upload(t, uploads.KindDocument, uploads.StatusClean, 50<<20)
	req := Request{Sources: []SourceRef{{UploadID: big.PublicID()}}}
	placer.warm = true
	cold, err := f.svc.Estimate(context.Background(), f.principal(), req, uri)
	if err != nil {
		t.Fatal(err)
	}
	units := placer.seen[len(placer.seen)-1].Units
	tm := f.svc.Timing(context.Background(), units)
	if !tm.Placement.Burst || cold.ExpectedSeconds != tm.Seconds || tm.Seconds >= tm.LocalSeconds {
		t.Fatalf("pesada no pool quente: cotado %d, timing %+v", cold.ExpectedSeconds, tm)
	}
	// Cold pool: the boot of an instance makes this run slower than the
	// main box, so a run heavy only by its time stays local.
	placer.warm = false
	coldTiming := f.svc.Timing(context.Background(), units)
	if coldTiming.Placement.Burst || coldTiming.Placement.Reason != ReasonFasterLocal || coldTiming.Seconds != tm.LocalSeconds {
		t.Fatalf("pool frio: %+v", coldTiming)
	}
	if BurstSeconds(cfg, ThroughputFrom(cfg, nil), units, false)-BurstSeconds(cfg, ThroughputFrom(cfg, nil), units, true) != int64(cfg.Burst.BootSeconds) {
		t.Fatal("o boot entra no tempo do pool frio")
	}
	if p := f.svc.Place(context.Background(), PlaceInput{Units: units, LocalSeconds: tm.LocalSeconds}); p.Reason != ReasonFasterLocal {
		t.Fatalf("a execução decide igual ao orçamento: %+v", p)
	}
	placer.warm = true
	// Past the cap (the placer says local), the quote shows the slower main-box time.
	f.svc.d.Placer = LocalPlacer{}
	capped, err := f.svc.Estimate(context.Background(), f.principal(), req, uri)
	if err != nil {
		t.Fatal(err)
	}
	if capped.ExpectedSeconds != tm.LocalSeconds || capped.ExpectedSeconds <= cold.ExpectedSeconds {
		t.Fatalf("sem pool: %d s (no pool %d)", capped.ExpectedSeconds, cold.ExpectedSeconds)
	}
	if p := f.svc.Place(context.Background(), PlaceInput{Units: units, LocalSeconds: 10}); p.Burst || p.Name() != PlacementLocal {
		t.Fatalf("place = %+v", p)
	}
	if (Placement{Burst: true}).Name() != PlacementBurst {
		t.Fatal("nome")
	}
	f.svc.d.Throughput = nil
	if off := f.svc.Timing(context.Background(), units); off.Seconds != 0 || off.Placement.Burst {
		t.Fatalf("sem throughput: %+v", off)
	}
}

func TestMemoryAndBurstSeconds(t *testing.T) {
	t.Parallel()
	cfg := repoThroughput(t)
	units := Units{throughput.KindTable: 10, throughput.KindDocument: 5, throughput.KindLLM: 100}
	want := cfg.Burst.BaseMemoryMB + 10*cfg.Kinds[throughput.KindTable].MemoryMBPerUnit + 5*cfg.Kinds[throughput.KindDocument].MemoryMBPerUnit
	if got := MemoryMB(cfg, units); !near(got, want) {
		t.Fatalf("memória = %v, quer %v", got, want)
	}
	if MemoryMB(nil, units) != 0 {
		t.Fatal("sem configuração")
	}
	tp := ThroughputFrom(cfg, nil)
	local := ExpectedSeconds(cfg, tp, units, 0)
	warm := BurstSeconds(cfg, tp, units, true)
	work := float64(local) - cfg.OverheadSeconds
	if diff := float64(warm) - (cfg.OverheadSeconds + work*cfg.BurstSpeed()); diff < 0 || diff > 1 {
		t.Fatalf("pool quente: %d (local %d, fator %v)", warm, local, cfg.BurstSpeed())
	}
	if BurstSeconds(cfg, tp, Units{throughput.KindTable: 1e300}, true) != maxExpectedSeconds {
		t.Fatal("teto do tempo")
	}
	noBurst := *cfg
	noBurst.Burst = throughput.Burst{}
	if noBurst.BurstSpeed() != 1 {
		t.Fatal("sem seção burst o pool não acelera")
	}
}

func TestInstanceMinutesPricingHook(t *testing.T) {
	t.Parallel()
	p := repoPricing(t)
	if p.Burst.ChargeInstanceMinutes {
		t.Fatal("o padrão do repositório é não cobrar os minutos à parte")
	}
	pr := Pricer{P: p}
	items := []Item{{Item: ItemLLMTokens, Quantity: 100_000, Unit: "tokens"}, {Item: ItemDocuments, Quantity: 3, Unit: "files", Note: "3 planilhas"}}
	base, err := pr.Price(items, LaneApp, ModelRates{InputUSDPerMTok: 3, OutputUSDPerMTok: 15})
	if err != nil {
		t.Fatal(err)
	}
	with, err := pr.PriceWithInstance(items, LaneApp, ModelRates{InputUSDPerMTok: 3, OutputUSDPerMTok: 15}, 30)
	if err != nil {
		t.Fatal(err)
	}
	extra := p.Tokens(30 * p.Estimates.InstanceMinuteUSD)
	var doc Item
	for _, it := range with.Items {
		if it.Item == ItemDocuments {
			doc = it
		}
	}
	if doc.Credits != extra || !strings.Contains(doc.Note, "3 planilhas; inclui 30 min de instância dedicada") {
		t.Fatalf("item de documentos = %+v (extra %d)", doc, extra)
	}
	if with.SubtotalCredits != base.SubtotalCredits+extra || with.TotalCredits < base.TotalCredits+extra {
		t.Fatalf("totais: antes %+v depois %+v", base, with)
	}
	// Media-only runs carry it on the video/audio item; no item, no charge.
	media, _ := pr.PriceWithInstance([]Item{{Item: ItemAudioMinutes, Quantity: 10}}, LaneApp, ModelRates{}, 5)
	if !strings.Contains(media.Items[0].Note, "5 min") {
		t.Fatalf("mídia: %+v", media.Items)
	}
	if empty, _ := pr.PriceWithInstance(nil, LaneApp, ModelRates{}, 5); len(empty.Items) != 0 {
		t.Fatal("sem itens")
	}

	// Switched on in pricing.yaml, a quote routed to the pool includes the minutes.
	f := newFixture(t, harnesstest.Options{})
	f.svc.d.Pricing.Burst.ChargeInstanceMinutes = true
	f.svc.d.Placer = &placeAll{warm: true}
	big := f.upload(t, uploads.KindDocument, uploads.StatusClean, 50<<20)
	q, err := f.svc.Estimate(context.Background(), f.principal(), Request{Sources: []SourceRef{{UploadID: big.PublicID()}}}, func(u uploads.Upload) string { return "local://b/" + u.StorageKey })
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, it := range q.Breakdown.Items {
		found = found || strings.Contains(it.Note, "instância dedicada")
	}
	if !found {
		t.Fatalf("orçamento sem os minutos de instância: %+v", q.Breakdown.Items)
	}
}

func TestPricingValidatesTheHook(t *testing.T) {
	t.Parallel()
	p := repoPricing(t)
	bad := *p
	bad.Estimates.InstanceMinuteUSD = -1
	if err := bad.Validate(); err == nil {
		t.Fatal("custo negativo")
	}
	bad = *p
	bad.Estimates.InstanceMinuteUSD = 0
	bad.Burst = pricing.Burst{ChargeInstanceMinutes: true}
	if err := bad.Validate(); err == nil {
		t.Fatal("cobrar sem preço")
	}
}
