package estimate

import (
	"math"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/mmtmn/docmatch/services/api/internal/estimate/throughput"
	pb "github.com/mmtmn/docmatch/services/api/internal/harness/pb"
	"github.com/mmtmn/docmatch/services/api/internal/uploads"
)

// Units is the measured input of a run per throughput kind (config/throughput.yaml).
type Units map[string]float64

const (
	bytesPerMB    = 1 << 20
	msPerMinute   = 60_000
	tokensPerKTok = 1_000
)

// MeasureUnits measures the input of a run from what the estimate already
// knows: the megabytes of each document kind, the minutes of audio and video
// (the duration the scanner probed, else the size over mb_per_minute), and
// the model tokens, new tools and web requests of the priced breakdown. The
// harness reports files, not pages or rows, so documents count in megabytes.
// Media minutes the harness reports win when they are larger than ours.
func MeasureUnits(cfg *throughput.Config, docs, media []uploads.Upload, items []Item) Units {
	u := Units{}
	for _, d := range docs {
		u[docKind(d)] += float64(max(d.SizeBytes, 0)) / bytesPerMB
	}
	for _, m := range media {
		kind := mediaKind(m)
		switch {
		case m.DurationMs != nil && *m.DurationMs > 0:
			u[kind] += float64(*m.DurationMs) / msPerMinute
		case cfg != nil && cfg.Kinds[kind].MBPerMinute > 0:
			u[kind] += float64(max(m.SizeBytes, 0)) / bytesPerMB / cfg.Kinds[kind].MBPerMinute
		}
	}
	var audio, video float64
	for _, it := range items {
		q := math.Max(it.Quantity, 0)
		if !finite(q) {
			continue
		}
		switch it.Item {
		case ItemLLMTokens:
			u[throughput.KindLLM] += q / tokensPerKTok
		case ItemNewTools:
			u[throughput.KindNewTools] += q
		case ItemWebRequests:
			u[throughput.KindWebRequests] += q
		case ItemAudioMinutes:
			audio += q
		case ItemVideoMinutes:
			video += q
		}
	}
	u[throughput.KindAudio] = math.Max(u[throughput.KindAudio], audio)
	u[throughput.KindVideo] = math.Max(u[throughput.KindVideo], video)
	for k, v := range u {
		if v <= 0 {
			delete(u, k)
		}
	}
	return u
}

func docKind(u uploads.Upload) string {
	switch SourceKind(u) {
	case pb.SourceKind_SOURCE_KIND_TABLE:
		return throughput.KindTable
	case pb.SourceKind_SOURCE_KIND_EMAIL:
		return throughput.KindEmail
	case pb.SourceKind_SOURCE_KIND_ARCHIVE:
		return throughput.KindArchive
	}
	return throughput.KindDocument
}

func mediaKind(u uploads.Upload) string {
	if strings.HasPrefix(strings.ToLower(u.Mime()), "audio/") {
		return throughput.KindAudio
	}
	return throughput.KindVideo
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

// TimingSample is one completed run of the history.
type TimingSample struct {
	Units         Units
	ActiveSeconds float64
}

// Run outcomes kept with a timing (run_timings.status); only completed runs
// feed the history.
const (
	TimingCompleted = "completed"
	TimingFailed    = "failed"
)

// RunTiming is the measured duration of a finished run (run_timings).
type RunTiming struct {
	RunID           uuid.UUID
	OrgID           uuid.UUID
	Status          string
	Units           Units
	ActiveSeconds   float64
	ExpectedSeconds int64
	FinishedAt      time.Time
}

// Throughput are the seconds per unit a quote is timed with.
type Throughput struct {
	PerUnit map[string]float64
	// Learned maps the kinds whose rate came from history to their sample count.
	Learned map[string]int
	// RunSeconds is the typical active duration of a run (queue model).
	RunSeconds float64
}

type observation struct{ value, weight float64 }

// ThroughputFrom learns the seconds per unit of each kind from completed runs.
// The active seconds of a run, less overhead_seconds, are split between its
// kinds in proportion to quantity × default rate (exact for single-kind runs),
// and each kind takes the weighted median of its runs (weight = quantity), so
// a stuck run cannot move it and tiny runs, where the overhead dominates, do
// not drag it down. A kind with fewer than history.min_runs samples keeps the
// configured default.
func ThroughputFrom(cfg *throughput.Config, samples []TimingSample) Throughput {
	per := map[string][]observation{}
	durations := make([]float64, 0, len(samples))
	for _, s := range samples {
		if s.ActiveSeconds < 0 || !finite(s.ActiveSeconds) {
			continue
		}
		durations = append(durations, s.ActiveSeconds)
		work := math.Max(s.ActiveSeconds-cfg.OverheadSeconds, 0)
		var total float64
		for k, q := range s.Units {
			if q > 0 && finite(q) {
				total += q * cfg.Default(k)
			}
		}
		if total <= 0 {
			continue
		}
		for k, q := range s.Units {
			if q <= 0 || !finite(q) || cfg.Default(k) <= 0 {
				continue
			}
			share := q * cfg.Default(k) / total
			per[k] = append(per[k], observation{value: work * share / q, weight: q})
		}
	}
	out := Throughput{PerUnit: map[string]float64{}, Learned: map[string]int{}, RunSeconds: cfg.Queue.DefaultRunSeconds}
	for _, k := range throughput.Kinds() {
		rate := cfg.Default(k)
		if obs := per[k]; len(obs) >= cfg.History.MinRuns {
			rate = weightedMedian(obs)
			out.Learned[k] = len(obs)
		}
		out.PerUnit[k] = rate
	}
	if len(durations) >= cfg.History.MinRuns {
		out.RunSeconds = median(durations)
	}
	return out
}

// weightedMedian is the smallest value whose cumulative weight reaches half
// of the total.
func weightedMedian(obs []observation) float64 {
	sort.Slice(obs, func(i, j int) bool { return obs[i].value < obs[j].value })
	var total float64
	for _, o := range obs {
		total += o.weight
	}
	var acc float64
	for _, o := range obs {
		acc += o.weight
		if acc >= total/2 {
			return o.value
		}
	}
	return obs[len(obs)-1].value
}

func median(v []float64) float64 {
	sort.Float64s(v)
	n := len(v)
	if n%2 == 1 {
		return v[n/2]
	}
	return (v[n/2-1] + v[n/2]) / 2
}

// QueueWait estimates how long a new run waits for a harness slot: with n
// runs queued or running and p parallel slots, it starts after ⌈(n−p+1)/p⌉
// rounds of a typical run, and right away while a slot is free.
func QueueWait(cfg *throughput.Config, tp Throughput, inFlight int64) float64 {
	p := int64(cfg.Queue.ParallelRuns)
	if p <= 0 || inFlight < p {
		return 0
	}
	rounds := (inFlight - p + 1 + p - 1) / p
	return float64(rounds) * tp.RunSeconds
}

// maxExpectedSeconds caps the estimate (a year) so a corrupt input cannot overflow.
const maxExpectedSeconds = 365 * 24 * 3600

// ExpectedSeconds is overhead + Σ quantity × seconds per unit + queue wait, rounded up.
func ExpectedSeconds(cfg *throughput.Config, tp Throughput, units Units, queueWait float64) int64 {
	total := cfg.OverheadSeconds + math.Max(queueWait, 0)
	for k, q := range units {
		if q > 0 && finite(q) {
			total += q * tp.PerUnit[k]
		}
	}
	if !finite(total) || total > maxExpectedSeconds {
		return maxExpectedSeconds
	}
	return int64(math.Ceil(total))
}
