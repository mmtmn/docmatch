package estimate

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/mmtmn/docmatch/services/api/internal/estimate/throughput"
	"github.com/mmtmn/docmatch/services/api/internal/harness/harnesstest"
	"github.com/mmtmn/docmatch/services/api/internal/runs/apix"
	"github.com/mmtmn/docmatch/services/api/internal/uploads"
)

func repoThroughput(t testing.TB) *throughput.Config {
	t.Helper()
	dir, _ := os.Getwd()
	for i := 0; i < 8; i++ {
		p := filepath.Join(dir, "config", "throughput.yaml")
		if _, err := os.Stat(p); err == nil {
			c, err := throughput.Load(p)
			if err != nil {
				t.Fatal(err)
			}
			return c
		}
		dir = filepath.Dir(dir)
	}
	t.Skip("config/throughput.yaml não encontrado")
	return nil
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

func doc(mime string, size int64) uploads.Upload {
	return uploads.Upload{Kind: uploads.KindDocument, DetectedMime: mime, SizeBytes: size}
}

func media(mime string, size int64, durationMs *int64) uploads.Upload {
	return uploads.Upload{Kind: uploads.KindMedia, DetectedMime: mime, SizeBytes: size, DurationMs: durationMs}
}

func TestMeasureUnits(t *testing.T) {
	t.Parallel()
	cfg := repoThroughput(t)
	minute := int64(90_000)
	docs := []uploads.Upload{
		doc("text/csv", 2<<20), doc("application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", 1<<20),
		doc("application/pdf", 3<<20), doc("message/rfc822", 1<<19), doc("application/zip", 4<<20), doc("text/plain", -5),
	}
	mediaFiles := []uploads.Upload{media("audio/mpeg", 50<<20, &minute), media("video/mp4", 40<<20, nil), media("audio/ogg", 2<<20, nil)}
	items := []Item{
		{Item: ItemLLMTokens, Quantity: 40_000}, {Item: ItemNewTools, Quantity: 1}, {Item: ItemWebRequests, Quantity: 3},
		{Item: ItemVideoMinutes, Quantity: 5}, {Item: ItemAudioMinutes, Quantity: 1}, {Item: ItemDocuments, Quantity: 6},
		{Item: ItemLLMTokens, Quantity: math.Inf(1)}, {Item: ItemNewTools, Quantity: -2},
	}
	u := MeasureUnits(cfg, docs, mediaFiles, items)
	want := Units{
		throughput.KindTable: 3, throughput.KindDocument: 3, throughput.KindEmail: 0.5, throughput.KindArchive: 4,
		// 1.5 min probed + 2 MB at 1 MB/min; the harness' 1 min is smaller.
		throughput.KindAudio: 3.5,
		// 40 MB at 20 MB/min = 2 min; the harness counted 5.
		throughput.KindVideo: 5,
		throughput.KindLLM:   40, throughput.KindNewTools: 1, throughput.KindWebRequests: 3,
	}
	if len(u) != len(want) {
		t.Fatalf("units = %v", u)
	}
	for k, v := range want {
		if !near(u[k], v) {
			t.Errorf("%s = %v, esperado %v", k, u[k], v)
		}
	}
	// Without a configuration, media of unknown duration is not counted.
	if bare := MeasureUnits(nil, nil, []uploads.Upload{media("video/mp4", 40<<20, nil)}, nil); len(bare) != 0 {
		t.Fatalf("sem configuração: %v", bare)
	}
}

// table builds n completed single-kind runs of mb megabytes at rate s/MB.
func table(cfg *throughput.Config, n int, mb, rate float64) []TimingSample {
	out := make([]TimingSample, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, TimingSample{Units: Units{throughput.KindTable: mb}, ActiveSeconds: cfg.OverheadSeconds + mb*rate})
	}
	return out
}

func TestThroughputFromHistory(t *testing.T) {
	t.Parallel()
	cfg := repoThroughput(t)
	// No history: every default, the default run length.
	tp := ThroughputFrom(cfg, nil)
	for _, k := range throughput.Kinds() {
		if tp.PerUnit[k] != cfg.Default(k) {
			t.Errorf("%s = %v, esperado o padrão %v", k, tp.PerUnit[k], cfg.Default(k))
		}
	}
	if len(tp.Learned) != 0 || tp.RunSeconds != cfg.Queue.DefaultRunSeconds {
		t.Fatalf("sem histórico: %+v", tp)
	}

	// Single-kind runs are exact; a stuck run does not move the median.
	samples := table(cfg, cfg.History.MinRuns, 10, 6)
	samples = append(samples, TimingSample{Units: Units{throughput.KindTable: 10}, ActiveSeconds: 7200})
	tp = ThroughputFrom(cfg, samples)
	if !near(tp.PerUnit[throughput.KindTable], 6) || tp.Learned[throughput.KindTable] != cfg.History.MinRuns+1 {
		t.Fatalf("table = %v (%d amostras)", tp.PerUnit[throughput.KindTable], tp.Learned[throughput.KindTable])
	}
	if tp.PerUnit[throughput.KindDocument] != cfg.Default(throughput.KindDocument) {
		t.Fatal("tipo sem histórico deveria manter o padrão")
	}
	if want := cfg.OverheadSeconds + 60; !near(tp.RunSeconds, want) {
		t.Fatalf("duração típica = %v, esperado %v", tp.RunSeconds, want)
	}

	// Many tiny runs (where the overhead is everything) weigh less than a
	// few large ones: the weighted median follows the large orders.
	tiny := make([]TimingSample, 0, 20)
	for i := 0; i < 20; i++ {
		tiny = append(tiny, TimingSample{Units: Units{throughput.KindTable: 0.01}, ActiveSeconds: 1})
	}
	tp = ThroughputFrom(cfg, append(tiny, table(cfg, 3, 50, 4)...))
	if !near(tp.PerUnit[throughput.KindTable], 4) {
		t.Fatalf("pedidos grandes deveriam dominar: %v", tp.PerUnit[throughput.KindTable])
	}

	// Too few runs keep the default; bad samples are ignored.
	few := append(table(cfg, cfg.History.MinRuns-1, 10, 6), TimingSample{ActiveSeconds: math.NaN()}, TimingSample{ActiveSeconds: -1}, TimingSample{Units: Units{"retired_kind": 3}, ActiveSeconds: 30})
	if tp := ThroughputFrom(cfg, few); tp.PerUnit[throughput.KindTable] != cfg.Default(throughput.KindTable) {
		t.Fatalf("poucas amostras deveriam manter o padrão: %v", tp.PerUnit[throughput.KindTable])
	}

	// A mixed run is split in proportion to quantity × default rate.
	mixed := make([]TimingSample, 0, cfg.History.MinRuns)
	work := 10*cfg.Default(throughput.KindTable) + 20*cfg.Default(throughput.KindLLM)
	for i := 0; i < cfg.History.MinRuns; i++ {
		mixed = append(mixed, TimingSample{Units: Units{throughput.KindTable: 10, throughput.KindLLM: 20}, ActiveSeconds: cfg.OverheadSeconds + 2*work})
	}
	tp = ThroughputFrom(cfg, mixed)
	if !near(tp.PerUnit[throughput.KindTable], 2*cfg.Default(throughput.KindTable)) || !near(tp.PerUnit[throughput.KindLLM], 2*cfg.Default(throughput.KindLLM)) {
		t.Fatalf("repartição proporcional: %v", tp.PerUnit)
	}
}

func TestWeightedMedianAndMedian(t *testing.T) {
	t.Parallel()
	if v := weightedMedian([]observation{{value: 9, weight: 1}, {value: 1, weight: 1}, {value: 5, weight: 10}}); v != 5 {
		t.Fatalf("mediana ponderada = %v", v)
	}
	if v := weightedMedian([]observation{{value: 3, weight: 0}, {value: 7, weight: 0}}); v != 3 {
		t.Fatalf("pesos nulos = %v", v)
	}
	if median([]float64{4, 1, 3, 2}) != 2.5 || median([]float64{3, 1, 2}) != 2 {
		t.Fatal("mediana")
	}
}

func TestQueueWaitAndExpected(t *testing.T) {
	t.Parallel()
	cfg := repoThroughput(t)
	tp := ThroughputFrom(cfg, nil)
	p := int64(cfg.Queue.ParallelRuns)
	run := cfg.Queue.DefaultRunSeconds
	for _, c := range []struct {
		inFlight int64
		rounds   float64
	}{{0, 0}, {p - 1, 0}, {p, 1}, {2*p - 1, 1}, {2 * p, 2}, {2*p + 1, 2}} {
		if got := QueueWait(cfg, tp, c.inFlight); !near(got, c.rounds*run) {
			t.Errorf("fila com %d: %v, esperado %v", c.inFlight, got, c.rounds*run)
		}
	}
	units := Units{throughput.KindTable: 100, throughput.KindAudio: 30, "retired_kind": 1000, throughput.KindLLM: math.NaN()}
	want := cfg.OverheadSeconds + 100*cfg.Default(throughput.KindTable) + 30*cfg.Default(throughput.KindAudio) + 12.5
	if got := ExpectedSeconds(cfg, tp, units, 12.5); got != int64(math.Ceil(want)) {
		t.Fatalf("esperado = %d, queria %v", got, want)
	}
	if got := ExpectedSeconds(cfg, tp, Units{throughput.KindVideo: 1e15}, 0); got != maxExpectedSeconds {
		t.Fatalf("teto = %d", got)
	}
	if got := ExpectedSeconds(cfg, tp, nil, -5); got != int64(math.Ceil(cfg.OverheadSeconds)) {
		t.Fatalf("vazio = %d", got)
	}
}

// failingTimings breaks the history and the queue count.
type failingTimings struct{ *MemStore }

var errDown = errors.New("banco fora")

func (failingTimings) RecentTimings(context.Context, time.Time, int) ([]TimingSample, error) {
	return nil, errDown
}
func (failingTimings) RunsInFlight(context.Context) (int64, error) { return 0, errDown }

func TestEstimateTimesTheQuote(t *testing.T) {
	t.Parallel()
	f := newFixture(t, harnesstest.Options{})
	cfg := f.svc.Throughput()
	if cfg == nil {
		t.Fatal("fixture sem throughput")
	}
	small := f.upload(t, uploads.KindDocument, uploads.StatusClean, 1000)
	req := Request{Sources: []SourceRef{{UploadID: small.PublicID()}}}
	uri := func(u uploads.Upload) string { return "local://b/" + u.StorageKey }
	q, err := f.svc.Estimate(context.Background(), f.principal(), req, uri)
	if err != nil {
		t.Fatal(err)
	}
	if q.ExpectedSeconds < int64(cfg.OverheadSeconds) || q.Large {
		t.Fatalf("pedido pequeno: %d s, large=%v", q.ExpectedSeconds, q.Large)
	}
	rec, ok, _ := f.svc.Get(context.Background(), f.org, mustDecodeQuote(t, q.ID))
	if !ok || rec.Quote.ExpectedSeconds != q.ExpectedSeconds {
		t.Fatal("o tempo esperado precisa ficar no orçamento gravado")
	}

	// A busy queue adds rounds of a typical run and can make the order large.
	f.store.SetRunsInFlight(int64(cfg.Queue.ParallelRuns) * 20)
	busy, err := f.svc.Estimate(context.Background(), f.principal(), req, uri)
	if err != nil {
		t.Fatal(err)
	}
	if busy.ExpectedSeconds <= q.ExpectedSeconds || !busy.Large {
		t.Fatalf("fila cheia: %d s (antes %d), large=%v", busy.ExpectedSeconds, q.ExpectedSeconds, busy.Large)
	}
	f.store.SetRunsInFlight(0)

	// History is learned and cached; a big table becomes large with a slow history.
	slow := table(cfg, cfg.History.MinRuns, 10, 100)
	for i, s := range slow {
		_ = f.svc.RecordTiming(context.Background(), RunTiming{RunID: uuid.New(), OrgID: f.org, Status: TimingCompleted, Units: s.Units, ActiveSeconds: s.ActiveSeconds, FinishedAt: f.now.Add(-time.Duration(i+1) * time.Hour)})
	}
	_ = f.svc.RecordTiming(context.Background(), RunTiming{RunID: uuid.New(), OrgID: f.org, Status: TimingFailed, ActiveSeconds: 1, FinishedAt: f.now})
	f.now = f.now.Add(ratesTTL + time.Second)
	big := f.upload(t, uploads.KindDocument, uploads.StatusClean, 20<<20)
	bq, err := f.svc.Estimate(context.Background(), f.principal(), Request{Sources: []SourceRef{{UploadID: big.PublicID()}}}, uri)
	if err != nil {
		t.Fatal(err)
	}
	if bq.ExpectedSeconds < 2000 || !bq.Large {
		t.Fatalf("histórico lento: %d s, large=%v", bq.ExpectedSeconds, bq.Large)
	}
	if n := len(f.store.Timings()); n != cfg.History.MinRuns+1 {
		t.Fatalf("durações gravadas: %d", n)
	}

	// A broken store degrades to defaults and no queue, never to an error.
	f.svc.d.Store = failingTimings{f.store}
	f.now = f.now.Add(ratesTTL + time.Second)
	if _, err := f.svc.Estimate(context.Background(), f.principal(), req, uri); err != nil {
		t.Fatalf("histórico fora não pode quebrar a estimativa: %v", err)
	}
	f.svc.d.Store = f.store

	// Without config/throughput.yaml the quote has no timing.
	f.svc.d.Throughput = nil
	none, err := f.svc.Estimate(context.Background(), f.principal(), req, uri)
	if err != nil || none.ExpectedSeconds != 0 || none.Large {
		t.Fatalf("sem configuração: %+v %v", none, err)
	}
}

func mustDecodeQuote(t *testing.T, id string) uuid.UUID {
	t.Helper()
	u, err := apix.Decode(apix.PrefixQuote, id)
	if err != nil {
		t.Fatal(err)
	}
	return u
}
