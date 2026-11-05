package estimate

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"pgregory.net/rapid"

	contracts "github.com/mmtmn/docmatch/packages/contracts/gen"
	"github.com/mmtmn/docmatch/services/api/internal/estimate/pricing"
	"github.com/mmtmn/docmatch/services/api/internal/harness"
	"github.com/mmtmn/docmatch/services/api/internal/harness/harnesstest"
	pb "github.com/mmtmn/docmatch/services/api/internal/harness/pb"
	"github.com/mmtmn/docmatch/services/api/internal/logx"
	"github.com/mmtmn/docmatch/services/api/internal/runs/apix"
	"github.com/mmtmn/docmatch/services/api/internal/uploads"
)

func repoPricing(t testing.TB) *pricing.Pricing {
	t.Helper()
	dir, _ := os.Getwd()
	for i := 0; i < 8; i++ {
		p := filepath.Join(dir, "config", "pricing.yaml")
		if _, err := os.Stat(p); err == nil {
			pr, err := pricing.Load(p, pricing.Overrides{})
			if err != nil {
				t.Fatal(err)
			}
			return pr
		}
		dir = filepath.Dir(dir)
	}
	t.Skip("config/pricing.yaml não encontrado")
	return nil
}

var itemKinds = []string{ItemDocuments, ItemScanFiles, ItemAudioMinutes, ItemVideoMinutes, ItemFrames, ItemLLMTokens, ItemEmbeddings, ItemNewTools, ItemWebRequests}

// TestPricingMarginProperty: for any cost vector, any lane and any model
// rates, the margin after payment, infra, cost and income tax is at least
// the target margin of the table (30%).
func TestPricingMarginProperty(t *testing.T) {
	t.Parallel()
	p := repoPricing(t)
	pr := Pricer{P: p}
	rapid.Check(t, func(rt *rapid.T) {
		n := rapid.IntRange(0, 12).Draw(rt, "n")
		items := make([]Item, 0, n)
		for i := 0; i < n; i++ {
			items = append(items, Item{Item: rapid.SampledFrom(itemKinds).Draw(rt, "kind"), Quantity: rapid.Float64Range(0, 5_000_000).Draw(rt, "qty"), Unit: UnitTokens})
		}
		lane := rapid.SampledFrom([]string{LaneApp, LaneAPIBatch, LaneAPIStandard, LaneAPIPriority}).Draw(rt, "lane")
		rates := ModelRates{
			InputUSDPerMTok: rapid.Float64Range(0, 100).Draw(rt, "in"), OutputUSDPerMTok: rapid.Float64Range(0, 400).Draw(rt, "out"),
			EmbeddingUSDPerMTok: rapid.Float64Range(0, 5).Draw(rt, "emb"),
		}
		bd, err := pr.Price(items, lane, rates)
		if err != nil {
			rt.Fatal(err)
		}
		var cost float64
		for _, it := range items {
			usd, model := pr.ItemUSD(it, lane, rates)
			if model {
				usd = p.ModelCost(usd)
			}
			cost += usd
		}
		if bd.TotalCredits < bd.MinimumCredits || bd.TotalCredits < bd.SubtotalCredits {
			rt.Fatalf("total %d < mínimo %d ou subtotal %d", bd.TotalCredits, bd.MinimumCredits, bd.SubtotalCredits)
		}
		if cost > 0 {
			if m := MarginAfterTax(p, bd.TotalCredits, cost); m < p.Rates.TargetMargin-1e-9 {
				rt.Fatalf("margem %.4f < %.2f (custo %.4f, créditos %d, faixa %s)", m, p.Rates.TargetMargin, cost, bd.TotalCredits, lane)
			}
		}
		for _, it := range bd.Items {
			if it.Credits < 0 {
				rt.Fatal("créditos negativos")
			}
		}
	})
}

func TestPriceExampleAndCap(t *testing.T) {
	t.Parallel()
	p := repoPricing(t)
	pr := Pricer{P: p}
	rates := ModelRates{InputUSDPerMTok: 3, OutputUSDPerMTok: 15}
	bd, err := pr.Price([]Item{{Item: ItemLLMTokens, Quantity: 1_000_000, Unit: UnitTokens}, {Item: ItemNewTools, Quantity: 1, Unit: UnitTools}, {Item: ItemDocuments, Quantity: 4, Unit: UnitFiles}}, LaneAPIBatch, rates)
	if err != nil {
		t.Fatal(err)
	}
	if bd.Lane != LaneAPIBatch || bd.LaneMultiplier != 2 || bd.PricingVersion == "" || len(bd.Items) != 3 {
		t.Fatalf("breakdown = %+v", bd)
	}
	if bd.Items[0].Item != ItemLLMTokens || bd.Items[2].Credits != 0 {
		t.Fatalf("itens deveriam vir do maior para o menor: %+v", bd.Items)
	}
	if bd.TotalCredits != int64(math.Ceil(float64(bd.SubtotalCredits)*2)) {
		t.Fatalf("total %d ≠ ceil(subtotal %d × 2)", bd.TotalCredits, bd.SubtotalCredits)
	}
	empty, err := pr.Price(nil, LaneApp, rates)
	if err != nil || empty.TotalCredits != p.MinTokensPerRun || len(empty.Items) != 0 {
		t.Fatalf("vazio = %+v, %v", empty, err)
	}
	if _, err := pr.Price(nil, "warp", rates); err == nil {
		t.Fatal("faixa desconhecida deveria falhar")
	}
	if Cap(100, 1.25) != 125 || Cap(100, 0.5) != 100 || Cap(100, math.NaN()) != 100 {
		t.Fatal("Cap")
	}
	samples := make([]Overrun, 0, 100)
	for i := 0; i < 100; i++ {
		samples = append(samples, Overrun{Estimated: 100, Spent: int64(80 + i)})
	}
	ratio, ok := P99Overrun(samples)
	if !ok || ratio < 1.7 || ratio > 1.8 {
		t.Fatalf("p99 = %v, %v", ratio, ok)
	}
	if _, ok := P99Overrun(samples[:5]); ok {
		t.Fatal("poucas amostras não deveriam calibrar")
	}
	r := RatesFrom([]ModelPrice{{Model: "a/x", InputCentsPerMTok: 100, OutputCentsPerMTok: 500}, {Model: "b/y", InputCentsPerMTok: 300, OutputCentsPerMTok: 200}, {Model: "openai/text-embedding-4", InputCentsPerMTok: 2}})
	if r.InputUSDPerMTok != 3 || r.OutputUSDPerMTok != 5 || r.EmbeddingUSDPerMTok != 0.02 {
		t.Fatalf("rates = %+v", r)
	}
}

func TestSigner(t *testing.T) {
	t.Parallel()
	if _, err := NewSigner(""); err == nil {
		t.Fatal("chave vazia deveria falhar")
	}
	if _, err := NewSigner("curta"); err == nil {
		t.Fatal("chave curta deveria falhar")
	}
	old, _ := NewSigner("chave-antiga-com-16-chars")
	rotated, _ := NewSigner("chave-nova-com-16-chars!, chave-antiga-com-16-chars")
	q := Quote{ID: "qte_1", Fingerprint: "abc", ExpectedCredits: 10, CapCredits: 12, ExpiresAt: "2026-11-04T10:00:00Z"}
	sig := old.Sign(q)
	if !strings.HasPrefix(sig, "v1=") || len(sig) != 67 {
		t.Fatalf("assinatura = %s", sig)
	}
	if !old.Verify(q, sig) || !rotated.Verify(q, sig) {
		t.Fatal("assinatura antiga deveria verificar nas duas chaves")
	}
	if rotated.Sign(q) == sig {
		t.Fatal("a chave nova deveria assinar diferente")
	}
	if !rotated.Verify(q, rotated.Sign(q)) || old.Verify(q, rotated.Sign(q)) {
		t.Fatal("rotação: a chave antiga não conhece a nova")
	}
	tampered := q
	tampered.CapCredits = 9999
	if old.Verify(tampered, sig) {
		t.Fatal("teto adulterado deveria invalidar")
	}
	tampered = q
	tampered.ExpiresAt = "2030-01-01T00:00:00Z"
	if old.Verify(tampered, sig) {
		t.Fatal("validade adulterada deveria invalidar")
	}
	if old.Verify(q, "v1="+strings.Repeat("0", 64)) || old.Verify(q, "v2="+sig[3:]) || old.Verify(q, sig[:60]) {
		t.Fatal("assinaturas inválidas aceitas")
	}
}

func TestFingerprint(t *testing.T) {
	t.Parallel()
	hashes := map[string]string{"upl_A": "h1", "upl_B": "h2"}
	a := Request{Sources: []SourceRef{{UploadID: "upl_A"}, {UploadID: "upl_B", Role: "bank"}}, Options: &Options{RuleIDs: []string{"rul_2", "rul_1"}}}
	b := Request{Sources: []SourceRef{{UploadID: "upl_B", Role: "bank"}, {UploadID: "upl_A"}}, Options: &Options{RuleIDs: []string{"rul_1", "rul_2"}}}
	if Fingerprint(a, hashes) != Fingerprint(b, hashes) {
		t.Fatal("ordem não deveria mudar a impressão digital")
	}
	c := a
	c.RulesText = "x"
	if Fingerprint(c, hashes) == Fingerprint(a, hashes) {
		t.Fatal("regras avulsas deveriam mudar a impressão digital")
	}
	other := map[string]string{"upl_A": "h1", "upl_B": "h9"}
	if Fingerprint(a, other) == Fingerprint(a, hashes) {
		t.Fatal("hash de conteúdo deveria mudar a impressão digital")
	}
	if len(Fingerprint(a, hashes)) != 64 {
		t.Fatal("tamanho")
	}
	h := &Hints{Sheet: "Títulos", HeaderRow: 2, DecimalSeparator: ","}
	if m := h.Map(); m["sheet"] != "Títulos" || m["header_row"] != "2" || len(m) != 3 {
		t.Fatalf("hints = %v", m)
	}
	var nilHints *Hints
	if nilHints.Map() != nil {
		t.Fatal("hints nulos")
	}
}

// fixture assembles a service over memory stores and the fake harness.
type fixture struct {
	svc     *Service
	store   *MemStore
	uploads *uploads.MemStore
	fake    *harnesstest.Server
	org     uuid.UUID
	now     time.Time
}

func newFixture(t *testing.T, opts harnesstest.Options) *fixture {
	t.Helper()
	p := repoPricing(t)
	signer, _ := NewSigner("chave-de-teste-com-16-chars")
	fake := harnesstest.NewServer(harnesstest.MustLoadGolden(), opts)
	sock, cleanup, err := harnesstest.SocketPath()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	stop, err := harnesstest.Serve(sock, fake)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	client, err := harness.Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	ups := uploads.NewMemStore()
	upSvc := uploadsService(t, ups, p)
	store := NewMemStore()
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	f := &fixture{store: store, uploads: ups, fake: fake, org: uuid.New(), now: now}
	f.svc, err = New(Deps{Store: store, Harness: client, Pricing: p, Throughput: repoThroughput(t), Signer: signer, Sources: upSvc, Logger: logx.Discard(), Now: func() time.Time { return f.now }})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func uploadsService(t *testing.T, store *uploads.MemStore, p *pricing.Pricing) *uploads.Service {
	t.Helper()
	// The estimate service only lists uploads; the stores are never touched.
	svc, err := uploads.New(uploads.Deps{Store: store, Stores: noStores(t), Pricing: p, Jobs: &noJobs{}, Logger: logx.Discard()})
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

func (f *fixture) principal() apix.Principal {
	user := uuid.New()
	return apix.Principal{OrgID: f.org, UserID: &user, Plan: "pro"}
}

func (f *fixture) upload(t *testing.T, kind uploads.Kind, status uploads.Status, size int64) uploads.Upload {
	t.Helper()
	id, _ := uuid.NewV7()
	u := uploads.Upload{ID: id, OrgID: f.org, Kind: kind, Store: "local", FileName: "a.csv", DeclaredMime: "text/csv", DetectedMime: "text/csv", SizeBytes: size, SHA256: make([]byte, 32), StorageKey: "quarantine/k", Status: status, CreatedAt: f.now}
	if kind == uploads.KindMedia {
		u.DeclaredMime, u.DetectedMime = "audio/mpeg", "audio/mpeg"
		d := int64(60000)
		u.DurationMs = &d
	}
	if _, err := f.uploads.CreateUpload(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	return u
}

func TestEstimateFlow(t *testing.T) {
	t.Parallel()
	f := newFixture(t, harnesstest.Options{})
	p := f.principal()
	a := f.upload(t, uploads.KindDocument, uploads.StatusClean, 1000)
	b := f.upload(t, uploads.KindDocument, uploads.StatusClean, 2000)
	req := Request{Sources: []SourceRef{{UploadID: a.PublicID(), Role: "bank"}, {UploadID: b.PublicID()}}, RulesText: "Tarifa até R$ 30 é parte do pagamento"}
	q, err := f.svc.Estimate(context.Background(), p, req, func(u uploads.Upload) string { return "local://b/" + u.StorageKey })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(q.ID, "qte_") || q.ExpectedCredits < 10 || q.CapCredits < q.ExpectedCredits || len(q.Fingerprint) != 64 || q.Breakdown.Lane != LaneApp {
		t.Fatalf("quote = %+v", q)
	}
	if q.Breakdown.Calibration == nil || q.Breakdown.Calibration.P99Overrun != DefaultCapRatio {
		t.Fatalf("calibração padrão esperada: %+v", q.Breakdown.Calibration)
	}
	exp, _ := q.Expiry()
	if exp.Sub(f.now) != 15*time.Minute {
		t.Fatalf("validade = %v", exp)
	}
	if f.fake.Estimates() != 1 {
		t.Fatal("harness não foi chamado")
	}
	// Verify accepts the stored quote and the matching fingerprint.
	id, _ := apix.Decode(apix.PrefixQuote, q.ID)
	hashes := map[string]string{a.PublicID(): strings.Repeat("0", 64), b.PublicID(): strings.Repeat("0", 64)}
	rec, err := f.svc.Verify(context.Background(), f.org, id, q.Signature, Fingerprint(req, hashes))
	if err != nil || rec.Status != StatusOpen {
		t.Fatalf("verify = %+v, %v", rec, err)
	}
	// Tampered signature.
	_, err = f.svc.Verify(context.Background(), f.org, id, "v1="+strings.Repeat("a", 64), rec.Fingerprint)
	assertCode(t, err, contracts.ErrQuoteInvalidSignature)
	// Different request.
	_, err = f.svc.Verify(context.Background(), f.org, id, q.Signature, "other")
	assertCode(t, err, contracts.ErrQuoteMismatch)
	// Another organization cannot see it.
	_, err = f.svc.Verify(context.Background(), uuid.New(), id, q.Signature, rec.Fingerprint)
	assertCode(t, err, contracts.ErrQuoteNotFound)
	// Expired.
	f.now = f.now.Add(16 * time.Minute)
	_, err = f.svc.Verify(context.Background(), f.org, id, q.Signature, rec.Fingerprint)
	assertCode(t, err, contracts.ErrQuoteExpired)
	f.now = f.now.Add(-16 * time.Minute)
	// Used.
	runID := uuid.New()
	if err := f.svc.Consume(context.Background(), f.org, id, runID); err != nil {
		t.Fatal(err)
	}
	_, err = f.svc.Verify(context.Background(), f.org, id, q.Signature, rec.Fingerprint)
	assertCode(t, err, contracts.ErrQuoteMismatch)
	if got, ok, _ := f.svc.Get(context.Background(), f.org, id); !ok || got.RunID == nil || *got.RunID != runID {
		t.Fatal("Get após Consume")
	}
	if err := f.svc.Expire(context.Background(), f.org, id); err != nil {
		t.Fatal(err)
	}
}

func assertCode(t *testing.T, err error, code contracts.ErrorCode) {
	t.Helper()
	var ae *apix.Error
	if !errors.As(err, &ae) || ae.Code != code {
		t.Fatalf("erro = %v, esperado %s", err, code)
	}
}

func TestEstimateValidation(t *testing.T) {
	t.Parallel()
	f := newFixture(t, harnesstest.Options{})
	p := f.principal()
	clean := f.upload(t, uploads.KindDocument, uploads.StatusClean, 1000)
	pending := f.upload(t, uploads.KindDocument, uploads.StatusScanning, 1000)
	media := f.upload(t, uploads.KindMedia, uploads.StatusClean, 1000)
	uri := func(u uploads.Upload) string { return u.StorageKey }
	cases := []struct {
		name string
		p    apix.Principal
		req  Request
		code contracts.ErrorCode
	}{
		{"sem fontes", p, Request{}, contracts.ErrValidationFailed},
		{"id inválido", p, Request{Sources: []SourceRef{{UploadID: "nope"}}}, contracts.ErrValidationFailed},
		{"duplicado", p, Request{Sources: []SourceRef{{UploadID: clean.PublicID()}, {UploadID: clean.PublicID()}}}, contracts.ErrValidationFailed},
		{"não pronto", p, Request{Sources: []SourceRef{{UploadID: pending.PublicID()}}}, contracts.ErrUploadNotReady},
		{"inexistente", p, Request{Sources: []SourceRef{{UploadID: apix.Encode(apix.PrefixUpload, uuid.New())}}}, contracts.ErrUploadNotFound},
		{"mídia como documento", p, Request{Sources: []SourceRef{{UploadID: media.PublicID()}}}, contracts.ErrValidationFailed},
		{"texto longo", p, Request{Sources: []SourceRef{{UploadID: clean.PublicID()}}, RulesText: strings.Repeat("a", 4001)}, contracts.ErrTextTooLong},
		{"faixa api em sessão", p, Request{Sources: []SourceRef{{UploadID: clean.PublicID()}}, Options: &Options{Lane: LaneAPIBatch}}, contracts.ErrLaneNotAllowed},
		{"faixa inválida", p, Request{Sources: []SourceRef{{UploadID: clean.PublicID()}}, Options: &Options{Lane: "warp"}}, contracts.ErrValidationFailed},
		{"llm_mode inválido", p, Request{Sources: []SourceRef{{UploadID: clean.PublicID()}}, Options: &Options{LlmMode: "x"}}, contracts.ErrValidationFailed},
		{"escalonamento fora do plano", p, Request{Sources: []SourceRef{{UploadID: clean.PublicID()}}, Options: &Options{Escalation: true}}, contracts.ErrPlanLimitReached},
		{"plano desconhecido", apix.Principal{OrgID: f.org, Plan: "vip"}, Request{Sources: []SourceRef{{UploadID: clean.PublicID()}}}, contracts.ErrPlanUnknown},
		{"regra inválida", p, Request{Sources: []SourceRef{{UploadID: clean.PublicID()}}, Options: &Options{RuleIDs: []string{"rul_x"}}}, contracts.ErrValidationFailed},
		{"workflow inválido", p, Request{Sources: []SourceRef{{UploadID: clean.PublicID()}}, WorkflowID: "wfl_x"}, contracts.ErrValidationFailed},
		{"hints inválidos", p, Request{Sources: []SourceRef{{UploadID: clean.PublicID(), Hints: &Hints{DecimalSeparator: ";"}}}}, contracts.ErrValidationFailed},
		{"role inválido", p, Request{Sources: []SourceRef{{UploadID: clean.PublicID(), Role: "Bank!"}}}, contracts.ErrValidationFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := f.svc.Estimate(context.Background(), tc.p, tc.req, uri)
			assertCode(t, err, tc.code)
		})
	}
	// Too many files for the plan (pro: 30 documents).
	many := make([]SourceRef, 0, 31)
	for i := 0; i < 31; i++ {
		many = append(many, SourceRef{UploadID: apix.Encode(apix.PrefixUpload, uuid.New())})
	}
	_, err := f.svc.Estimate(context.Background(), p, Request{Sources: many}, uri)
	assertCode(t, err, contracts.ErrTooManyFiles)
	// Run too large: 2 files above the plan's 100 MB.
	big1 := f.upload(t, uploads.KindDocument, uploads.StatusClean, 60<<20)
	big2 := f.upload(t, uploads.KindDocument, uploads.StatusClean, 60<<20)
	_, err = f.svc.Estimate(context.Background(), p, Request{Sources: []SourceRef{{UploadID: big1.PublicID()}, {UploadID: big2.PublicID()}}}, uri)
	assertCode(t, err, contracts.ErrRunTooLarge)
	// API key lanes.
	key := uuid.New()
	apiKey := apix.Principal{OrgID: f.org, APIKeyID: &key, Plan: "pro", Lanes: []string{LaneAPIBatch}}
	if _, err := f.svc.Validate(apiKey, Request{Sources: []SourceRef{{UploadID: clean.PublicID()}}, Options: &Options{Lane: LaneAPIBatch}}); err != nil {
		t.Fatalf("faixa permitida: %v", err)
	}
	_, err = f.svc.Validate(apiKey, Request{Sources: []SourceRef{{UploadID: clean.PublicID()}}, Options: &Options{Lane: LaneAPIPriority}})
	assertCode(t, err, contracts.ErrLaneNotAllowed)
	_, err = f.svc.Validate(apiKey, Request{Sources: []SourceRef{{UploadID: clean.PublicID()}}})
	assertCode(t, err, contracts.ErrLaneNotAllowed)
	// Media sources work and count their duration.
	q, err := f.svc.Estimate(context.Background(), p, Request{Sources: []SourceRef{{UploadID: clean.PublicID()}}, MediaSources: []SourceRef{{UploadID: media.PublicID()}}}, uri)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, it := range q.Breakdown.Items {
		if it.Item == ItemAudioMinutes {
			found = true
		}
	}
	if !found {
		t.Fatalf("item de áudio esperado: %+v", q.Breakdown.Items)
	}
}

func TestHarnessUnavailable(t *testing.T) {
	t.Parallel()
	p := repoPricing(t)
	signer, _ := NewSigner("chave-de-teste-com-16-chars")
	ups := uploads.NewMemStore()
	store := NewMemStore()
	svc, err := New(Deps{Store: store, Harness: harness.Unavailable{}, Pricing: p, Signer: signer, Sources: uploadsService(t, ups, p), Logger: logx.Discard()})
	if err != nil {
		t.Fatal(err)
	}
	org := uuid.New()
	id, _ := uuid.NewV7()
	_, _ = ups.CreateUpload(context.Background(), uploads.Upload{ID: id, OrgID: org, Kind: uploads.KindDocument, DeclaredMime: "text/csv", SizeBytes: 10, SHA256: make([]byte, 32), Status: uploads.StatusClean})
	_, err = svc.Estimate(context.Background(), apix.Principal{OrgID: org, Plan: "starter"}, Request{Sources: []SourceRef{{UploadID: apix.Encode(apix.PrefixUpload, id)}}}, func(uploads.Upload) string { return "x" })
	assertCode(t, err, contracts.ErrHarnessUnavailable)
	if _, err := New(Deps{}); err == nil {
		t.Fatal("deps vazias deveriam falhar")
	}
}

func TestRatesAndCalibration(t *testing.T) {
	t.Parallel()
	f := newFixture(t, harnesstest.Options{})
	ctx := context.Background()
	// Empty table: fallback rates.
	r := f.svc.Rates(ctx)
	if r.InputUSDPerMTok <= 0 || r.OutputUSDPerMTok <= 0 {
		t.Fatalf("rates = %+v", r)
	}
	if err := EnsureModelPrices(ctx, f.store); err != nil {
		t.Fatal(err)
	}
	rows, _ := f.store.LatestModelPrices(ctx)
	if len(rows) != len(FallbackModelPrices()) {
		t.Fatalf("seed = %d linhas", len(rows))
	}
	// Harness calibration wins.
	ratio, cal := f.svc.CapRatio(ctx, &pb.Calibration{SampleRuns: 50, P99Overrun: 1.4})
	if ratio != 1.4 || cal == nil || cal.SampleRuns != 50 {
		t.Fatalf("calibração do harness = %v, %+v", ratio, cal)
	}
	// Recent runs when enough samples.
	for i := 0; i < MinOverrunSamples; i++ {
		f.store.AddOverruns(Overrun{Estimated: 100, Spent: 110})
	}
	ratio, cal = f.svc.CapRatio(ctx, nil)
	if ratio != 1.1 || cal.SampleRuns != MinOverrunSamples {
		t.Fatalf("p99 das execuções = %v, %+v", ratio, cal)
	}
	items := ItemsFromProto(&pb.EstimateBreakdown{Items: []*pb.EstimateItem{{Item: pb.EstimateItemKind_ESTIMATE_ITEM_KIND_WEB_REQUESTS, Quantity: 3, Unit: pb.EstimateUnit_ESTIMATE_UNIT_REQUESTS, Note: "n"}}})
	if len(items) != 1 || items[0].Item != ItemWebRequests || items[0].Unit != UnitRequests || items[0].Note != "n" {
		t.Fatalf("items = %+v", items)
	}
	if ItemsFromProto(nil) != nil {
		t.Fatal("nil breakdown")
	}
	reasons := ApprovalReasons([]pb.ApprovalReason{pb.ApprovalReason_APPROVAL_REASON_NEW_TOOLS, pb.ApprovalReason_APPROVAL_REASON_UNSPECIFIED})
	if len(reasons) != 1 || reasons[0] != "new_tools" {
		t.Fatalf("reasons = %v", reasons)
	}
	if SourceKind(uploads.Upload{Kind: uploads.KindMedia}) != pb.SourceKind_SOURCE_KIND_MEDIA {
		t.Fatal("SourceKind media")
	}
	kinds := []struct {
		mime string
		want pb.SourceKind
	}{
		{"text/csv", pb.SourceKind_SOURCE_KIND_TABLE},
		{"application/vnd.oasis.opendocument.spreadsheet", pb.SourceKind_SOURCE_KIND_TABLE},
		{"message/rfc822", pb.SourceKind_SOURCE_KIND_EMAIL},
		{"application/mbox", pb.SourceKind_SOURCE_KIND_EMAIL},
		{"application/vnd.ms-outlook", pb.SourceKind_SOURCE_KIND_EMAIL},
		{"application/zip", pb.SourceKind_SOURCE_KIND_ARCHIVE},
		{"application/x-7z-compressed", pb.SourceKind_SOURCE_KIND_ARCHIVE},
		{"application/pdf", pb.SourceKind_SOURCE_KIND_DOCUMENT},
		{"application/vnd.openxmlformats-officedocument.wordprocessingml.document", pb.SourceKind_SOURCE_KIND_DOCUMENT},
		{"application/vnd.oasis.opendocument.text", pb.SourceKind_SOURCE_KIND_DOCUMENT},
		{"application/json", pb.SourceKind_SOURCE_KIND_DOCUMENT},
		{"application/xml", pb.SourceKind_SOURCE_KIND_DOCUMENT},
		{"text/html", pb.SourceKind_SOURCE_KIND_DOCUMENT},
	}
	for _, k := range kinds {
		if got := SourceKind(uploads.Upload{DeclaredMime: k.mime}); got != k.want {
			t.Errorf("SourceKind(%s) = %s, esperado %s", k.mime, got, k.want)
		}
	}
	u := uploads.Upload{StorageKey: "quarantine/x", ScanResult: []byte(`{"rebuilt":[{"kind":"extract","bucket":"b","key":"extracts/x"}]}`)}
	if got := SourceURI(u, func(k string) string { return "s3://orig/" + k }); got != "s3://b/extracts/x" {
		t.Fatalf("SourceURI = %s", got)
	}
	if got := SourceURI(uploads.Upload{StorageKey: "k"}, func(k string) string { return "s3://orig/" + k }); got != "s3://orig/k" {
		t.Fatalf("SourceURI original = %s", got)
	}
}
