package estimate

import (
	"context"
	"math"

	"github.com/mmtmn/docmatch/services/api/internal/estimate/throughput"
)

// Placement reasons. A run executes on the pool of on-demand workers only
// when it is heavy and the pool can take it; every other reason keeps it on
// the main box (docs/escala.md).
const (
	ReasonLight       = "light"        // not heavy
	ReasonTime        = "time"         // main-box time at or above the threshold
	ReasonMemory      = "memory"       // estimated memory above the main box's headroom
	ReasonKind        = "kind"         // an input kind that always goes to the pool
	ReasonDisabled    = "disabled"     // the pool is off (DM_BURST_ENABLED=0)
	ReasonCap         = "cap"          // monthly instance-hour budget used up (or no instances allowed)
	ReasonFull        = "full"         // every instance the pool may have is taken
	ReasonUnhealthy   = "unhealthy"    // recent scale-ups produced no worker; cooling down
	ReasonInterrupted = "interrupted"  // the run lost too many instances already
	ReasonPoolUnknown = "pool_unknown" // the pool state could not be read
	ReasonNoCapacity  = "no_capacity"  // queued, but no worker claimed it in time
	ReasonFasterLocal = "faster_local" // heavy by time only, but the pool (boot included) would not be faster
	PlacementLocal    = "local"
	PlacementBurst    = "burst"
)

// PlaceInput is what placement looks at.
type PlaceInput struct {
	Units Units
	// LocalSeconds is the expected time on the main box.
	LocalSeconds int64
	// MemoryMB is the estimated harness memory of the run.
	MemoryMB float64
	// Interruptions counts the burst instances the run already lost.
	Interruptions int
}

// Placement is where a run executes and why.
type Placement struct {
	Burst  bool
	Reason string
	// Warm is true when an idle instance is ready (no boot wait).
	Warm bool
}

// Name is "burst" or "local".
func (p Placement) Name() string {
	if p.Burst {
		return PlacementBurst
	}
	return PlacementLocal
}

// Placer decides the placement of a run (the burst package implements it).
type Placer interface {
	Place(ctx context.Context, in PlaceInput) Placement
}

// LocalPlacer keeps every run on the main box.
type LocalPlacer struct{}

// Place implements Placer.
func (LocalPlacer) Place(context.Context, PlaceInput) Placement {
	return Placement{Reason: ReasonDisabled}
}

// QuoteTiming is the timing of a quote: the expected time where the run will
// execute, the main-box time it falls back to, and the placement.
type QuoteTiming struct {
	// Seconds is the quote's expected_seconds.
	Seconds int64
	// LocalSeconds is the time on the main box (deadlines, fallback).
	LocalSeconds int64
	Large        bool
	MemoryMB     float64
	Placement    Placement
}

// MemoryMB estimates the harness memory of a run: base + Σ units × memory per unit.
func MemoryMB(cfg *throughput.Config, units Units) float64 {
	if cfg == nil {
		return 0
	}
	total := cfg.Burst.BaseMemoryMB
	for k, q := range units {
		if q > 0 && finite(q) {
			total += q * cfg.Kinds[k].MemoryMBPerUnit
		}
	}
	if !finite(total) {
		return math.MaxFloat64
	}
	return total
}

// BurstSeconds is the expected time on the pool: the boot of an instance when
// none is warm, the overhead, and the work at the pool's speed. The queue of
// the main box does not apply (the pool scales per run).
func BurstSeconds(cfg *throughput.Config, tp Throughput, units Units, warm bool) int64 {
	speed := cfg.BurstSpeed()
	total := cfg.OverheadSeconds
	if !warm {
		total += cfg.Burst.BootSeconds
	}
	for k, q := range units {
		if q > 0 && finite(q) {
			total += q * tp.PerUnit[k] * speed
		}
	}
	if !finite(total) || total > maxExpectedSeconds {
		return maxExpectedSeconds
	}
	return int64(math.Ceil(total))
}

// Timing returns the full timing of a run with these units; zero when timing
// is off (no throughput.yaml).
func (s *Service) Timing(ctx context.Context, units Units) QuoteTiming {
	cfg := s.d.Throughput
	if cfg == nil {
		return QuoteTiming{Placement: Placement{Reason: ReasonLight}}
	}
	tp := s.throughputRates(ctx)
	local := s.localSeconds(ctx, cfg, tp, units)
	mem := MemoryMB(cfg, units)
	p := s.decide(ctx, cfg, tp, PlaceInput{Units: units, LocalSeconds: local, MemoryMB: mem})
	seconds := local
	if p.Burst {
		seconds = BurstSeconds(cfg, tp, units, p.Warm)
	}
	return QuoteTiming{Seconds: seconds, LocalSeconds: local, Large: cfg.Large(seconds), MemoryMB: mem, Placement: p}
}

// Place decides the placement of a run created earlier (the worker calls it
// at execution time with the units and main-box time the run kept).
func (s *Service) Place(ctx context.Context, in PlaceInput) Placement {
	cfg := s.d.Throughput
	if cfg == nil {
		return s.placer().Place(ctx, in)
	}
	if in.MemoryMB == 0 {
		in.MemoryMB = MemoryMB(cfg, in.Units)
	}
	return s.decide(ctx, cfg, s.throughputRates(ctx), in)
}

// decide asks the placer and keeps on the main box a run that is heavy only
// by its time when the pool would not finish it sooner (a cold pool pays
// the boot of an instance). Memory and kinds go to the pool regardless.
func (s *Service) decide(ctx context.Context, cfg *throughput.Config, tp Throughput, in PlaceInput) Placement {
	p := s.placer().Place(ctx, in)
	if p.Burst && p.Reason == ReasonTime && BurstSeconds(cfg, tp, in.Units, p.Warm) >= in.LocalSeconds {
		return Placement{Reason: ReasonFasterLocal}
	}
	return p
}

func (s *Service) placer() Placer {
	if s.d.Placer == nil {
		return LocalPlacer{}
	}
	return s.d.Placer
}
