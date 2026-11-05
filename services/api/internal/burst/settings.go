package burst

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/mmtmn/docmatch/services/api/internal/config"
	"github.com/mmtmn/docmatch/services/api/internal/estimate"
)

// Settings are the thresholds, caps and timings of the pool. The env
// variables (config.Burst) set the operational ones; the rest are protocol
// constants with defaults that tests shorten.
type Settings struct {
	Enabled bool
	// MinSeconds: a run expected to take this long on the main box is heavy.
	MinSeconds int64
	// MemoryHeadroomMB: a run estimated above it is heavy.
	MemoryHeadroomMB float64
	// Kinds always go to the pool.
	Kinds        []string
	MaxInstances int
	MonthlyHours float64
	IdleAfter    time.Duration
	// ClaimTimeout: a queued job nobody claims by then falls back.
	ClaimTimeout time.Duration
	// Lease is how long a claim lives without a heartbeat or an event.
	Lease time.Duration
	// Heartbeat is how often a worker renews its lease.
	Heartbeat time.Duration
	// ManifestTTL is how long a worker has to start a claimed job.
	ManifestTTL time.Duration
	// LongPoll is how long a claim waits for a job before answering 204.
	LongPoll time.Duration
	// Poll is how often the main worker reads the events of a job.
	Poll time.Duration
	// LiveWindow: an instance heard from within it is live.
	LiveWindow time.Duration
	// BootSeconds are charged once per instance started in the month.
	BootSeconds float64
	// NoClaimThreshold jobs in a row nobody claimed mark the pool unhealthy
	// for Cooldown.
	NoClaimThreshold int
	Cooldown         time.Duration
	// Retention keeps finished jobs and their events for debugging.
	Retention time.Duration
	// StatusTTL caches the pool status read by routing.
	StatusTTL time.Duration
}

// Default protocol timings.
const (
	DefaultLease            = 90 * time.Second
	DefaultHeartbeat        = 15 * time.Second
	DefaultManifestTTL      = 5 * time.Minute
	DefaultLongPoll         = 20 * time.Second
	DefaultPoll             = 500 * time.Millisecond
	DefaultLiveWindow       = 2 * time.Minute
	DefaultBootSeconds      = 180
	DefaultNoClaimThreshold = 2
	DefaultCooldown         = 30 * time.Minute
	DefaultRetention        = 7 * 24 * time.Hour
	DefaultStatusTTL        = 5 * time.Second
	// DefaultMinSeconds applies when neither DM_BURST_MIN_SECONDS nor
	// throughput.yaml say (large_after_seconds of the repo file).
	DefaultMinSeconds = 600
)

// FromConfig builds the settings from the env configuration; largeAfter is
// large_after_seconds of throughput.yaml (0 when timing is off) and
// bootSeconds its burst.boot_seconds.
func FromConfig(b config.Burst, largeAfter int64, bootSeconds float64) Settings {
	s := Settings{
		Enabled: b.Enabled, MinSeconds: b.MinSeconds, MemoryHeadroomMB: float64(b.MemoryHeadroomMB), Kinds: slices.Clone(b.Kinds),
		MaxInstances: b.MaxInstances, MonthlyHours: b.MonthlyHours, IdleAfter: b.IdleAfter, ClaimTimeout: b.ClaimTimeout,
		BootSeconds: bootSeconds,
	}
	if s.MinSeconds <= 0 {
		s.MinSeconds = largeAfter
	}
	return s.WithDefaults()
}

// WithDefaults fills the zero timings.
func (s Settings) WithDefaults() Settings {
	set := func(d *time.Duration, def time.Duration) {
		if *d <= 0 {
			*d = def
		}
	}
	set(&s.Lease, DefaultLease)
	set(&s.Heartbeat, DefaultHeartbeat)
	set(&s.ManifestTTL, DefaultManifestTTL)
	set(&s.LongPoll, DefaultLongPoll)
	set(&s.Poll, DefaultPoll)
	set(&s.LiveWindow, DefaultLiveWindow)
	set(&s.Cooldown, DefaultCooldown)
	set(&s.Retention, DefaultRetention)
	set(&s.StatusTTL, DefaultStatusTTL)
	set(&s.IdleAfter, time.Duration(config.DefaultBurstIdleMinutes)*time.Minute)
	set(&s.ClaimTimeout, time.Duration(config.DefaultBurstClaimSeconds)*time.Second)
	if s.MinSeconds <= 0 {
		s.MinSeconds = DefaultMinSeconds
	}
	if s.BootSeconds <= 0 {
		s.BootSeconds = DefaultBootSeconds
	}
	if s.NoClaimThreshold <= 0 {
		s.NoClaimThreshold = DefaultNoClaimThreshold
	}
	return s
}

// Heavy decides whether a run needs the pool, and why.
func (s Settings) Heavy(in estimate.PlaceInput) (bool, string) {
	for _, k := range s.Kinds {
		if in.Units[k] > 0 {
			return true, estimate.ReasonKind + ":" + k
		}
	}
	if s.MinSeconds > 0 && in.LocalSeconds >= s.MinSeconds {
		return true, estimate.ReasonTime
	}
	if s.MemoryHeadroomMB > 0 && in.MemoryMB > s.MemoryHeadroomMB {
		return true, estimate.ReasonMemory
	}
	return false, estimate.ReasonLight
}

// Query is the status query for now.
func (s Settings) Query(now time.Time) StatusQuery {
	return StatusQuery{Now: now, MonthStart: MonthStart(now), LiveSince: now.Add(-s.LiveWindow), BootSeconds: s.BootSeconds}
}

// CapReached reports whether the month's instance hours are used up.
func (s Settings) CapReached(st PoolStatus) bool {
	return s.MaxInstances <= 0 || st.UsedHours() >= s.MonthlyHours
}

// Router places runs (estimate.Placer): heavy runs go to the pool unless it
// is off, capped, full or cooling down. It never refuses a run: every "no"
// is the main box.
type Router struct {
	Settings Settings
	Store    Store
	Now      func() time.Time

	mu     sync.Mutex
	cached PoolStatus
	at     time.Time
	err    error
}

// Place implements estimate.Placer.
func (r *Router) Place(ctx context.Context, in estimate.PlaceInput) estimate.Placement {
	heavy, why := r.Settings.Heavy(in)
	switch {
	case !heavy:
		return estimate.Placement{Reason: why}
	case !r.Settings.Enabled:
		return estimate.Placement{Reason: estimate.ReasonDisabled}
	}
	now := r.now()
	st, err := r.status(ctx, now)
	switch {
	case err != nil:
		return estimate.Placement{Reason: estimate.ReasonPoolUnknown}
	case st.Unhealthy(now):
		return estimate.Placement{Reason: estimate.ReasonUnhealthy}
	case r.Settings.CapReached(st):
		return estimate.Placement{Reason: estimate.ReasonCap}
	case st.Busy+st.Queued >= r.Settings.MaxInstances:
		return estimate.Placement{Reason: estimate.ReasonFull}
	}
	return estimate.Placement{Burst: true, Reason: why, Warm: st.Idle > st.Queued}
}

func (r *Router) now() time.Time {
	if r.Now == nil {
		return time.Now()
	}
	return r.Now()
}

// status reads the pool, cached for StatusTTL (a quote must stay cheap).
func (r *Router) status(ctx context.Context, now time.Time) (PoolStatus, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.at.IsZero() && now.Sub(r.at) < r.Settings.StatusTTL && now.After(r.at.Add(-time.Second)) {
		return r.cached, r.err
	}
	r.cached, r.err = r.Store.Status(ctx, r.Settings.Query(now))
	r.at = now
	return r.cached, r.err
}
