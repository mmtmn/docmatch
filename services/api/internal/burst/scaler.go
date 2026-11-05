package burst

import (
	"context"
	"log/slog"
	"slices"
	"strings"
	"time"
)

// Group is the Auto Scaling group of the pool as the scaler sees it.
type Group struct {
	Desired   int
	Max       int
	Instances []GroupInstance
}

// GroupInstance is one instance of the group.
type GroupInstance struct {
	ID string
	// Type is the EC2 instance type.
	Type string
	// State is the ASG lifecycle state (Pending, InService, Terminating...).
	State     string
	Protected bool
}

// alive reports whether the instance runs (and is billed): not yet or no
// longer terminated.
func (g GroupInstance) alive() bool {
	return g.State != "" && !strings.HasPrefix(g.State, "Terminated")
}

// protectable reports whether scale-in protection may change on it now
// (AWS refuses the call for instances on their way out).
func (g GroupInstance) protectable() bool {
	return g.State == "InService" || strings.HasPrefix(g.State, "Pending")
}

// ASG is what the scaler needs from AWS Auto Scaling, scoped by IAM to the
// pool's group (infra/terraform/modules/compute). burstaws implements it.
type ASG interface {
	Describe(ctx context.Context) (Group, error)
	SetDesired(ctx context.Context, n int) error
	// Protect sets scale-in protection, so scaling down never takes an
	// instance that holds a job.
	Protect(ctx context.Context, ids []string, protected bool) error
}

// DefaultScaleInterval is how often the scaler reconciles without a nudge.
const DefaultScaleInterval = 15 * time.Second

// pruneEvery is how many ticks pass between two prunes of old jobs.
const pruneEvery = 240

// Scaler keeps the pool at the size the work needs: one instance per queued
// or running job up to the cap, warm for IdleAfter once the work is gone, then
// zero. It runs in the main worker. With the pool switched off it drains the
// group (desired 0, protection off as each instance ends its job). Without an
// ASG (manual pool) it only expires leases and prunes.
type Scaler struct {
	Store    Store
	ASG      ASG
	Settings Settings
	Logger   *slog.Logger
	Now      func() time.Time
	Interval time.Duration

	wake     chan struct{}
	lastWork time.Time
	desired  int
	seen     bool
	ticks    int
}

// NewScaler returns a scaler ready to Run.
func NewScaler(store Store, asg ASG, s Settings, log *slog.Logger) *Scaler {
	return &Scaler{Store: store, ASG: asg, Settings: s, Logger: log, wake: make(chan struct{}, 1)}
}

// Wake asks for a reconcile now (a job was queued).
func (s *Scaler) Wake() {
	if s.wake == nil {
		return
	}
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Scaler) now() time.Time {
	if s.Now == nil {
		return time.Now()
	}
	return s.Now()
}

func (s *Scaler) log() *slog.Logger {
	if s.Logger == nil {
		return slog.Default()
	}
	return s.Logger
}

// Run reconciles until ctx ends.
func (s *Scaler) Run(ctx context.Context) error {
	if s.wake == nil {
		s.wake = make(chan struct{}, 1)
	}
	interval := s.Interval
	if interval <= 0 {
		interval = DefaultScaleInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if _, err := s.Tick(ctx); err != nil && ctx.Err() == nil {
			s.log().Warn("ajuste do pool sob demanda falhou", slog.String("err", err.Error()))
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		case <-s.wake:
		}
	}
}

// Decision is what one tick decided (logged and tested).
type Decision struct {
	Status  PoolStatus
	Current int
	Target  int
	Reason  string
}

// Tick expires leases, records the group's instances, protects the busy
// ones, decides the size of the pool and applies it.
func (s *Scaler) Tick(ctx context.Context) (Decision, error) {
	now := s.now()
	if lost, err := s.Store.ExpireLeases(ctx); err != nil {
		return Decision{}, err
	} else if len(lost) > 0 {
		s.log().Warn("trabalhos do pool sem notícia do trabalhador; serão retomados", slog.Int("jobs", len(lost)))
	}
	s.ticks++
	if s.ticks%pruneEvery == 1 {
		if n, err := s.Store.Prune(ctx, s.Settings.Retention); err != nil {
			s.log().Warn("limpar trabalhos antigos do pool falhou", slog.String("err", err.Error()))
		} else if n > 0 {
			s.log().Info("trabalhos antigos do pool removidos", slog.Int64("jobs", n))
		}
	}
	if s.ASG == nil {
		st, err := s.Store.Status(ctx, s.Settings.Query(now))
		if err != nil {
			return Decision{}, err
		}
		return Decision{Status: st, Reason: "pool manual"}, nil
	}
	g, err := s.ASG.Describe(ctx)
	if err != nil {
		return Decision{}, err
	}
	// The group is the truth of what runs and bills: usage and liveness come
	// from it, never from what a caller says about itself.
	for _, in := range g.Instances {
		if in.alive() {
			if err := s.Store.TouchInstance(ctx, Instance{ID: in.ID, Type: in.Type, Lifecycle: "spot"}); err != nil {
				return Decision{}, err
			}
		}
	}
	st, err := s.Store.Status(ctx, s.Settings.Query(now))
	if err != nil {
		return Decision{}, err
	}
	if st.Queued+st.Busy > 0 || s.lastWork.IsZero() || (s.seen && g.Desired > s.desired) {
		// Work, a fresh start, or someone raised the group by hand
		// (burst.sh up): the idle window starts now.
		s.lastWork = now
	}
	// Busy instances are protected before any scale-in can pick them.
	if err := s.protect(ctx, g); err != nil {
		return Decision{}, err
	}
	d := s.decide(st, g, now)
	s.seen, s.desired = true, g.Desired
	if d.Target != g.Desired {
		if err := s.ASG.SetDesired(ctx, d.Target); err != nil {
			return d, err
		}
		s.desired = d.Target
		s.log().Info("pool sob demanda redimensionado", slog.Int("from", g.Desired), slog.Int("to", d.Target), slog.String("reason", d.Reason),
			slog.Int("queued", st.Queued), slog.Int("busy", st.Busy), slog.Float64("hours_used", st.UsedHours()))
	}
	return d, nil
}

// decide sizes the pool: work = queued + busy jobs (one per instance),
// bounded by the cap and the group's max; never below the busy ones; warm
// for IdleAfter after the last work unless the monthly budget is used up.
// Switched off, the target is zero: busy instances stay protected until
// their job ends, then the group lets them go.
func (s *Scaler) decide(st PoolStatus, g Group, now time.Time) Decision {
	limit := max(min(s.Settings.MaxInstances, g.Max), 0)
	d := Decision{Status: st, Current: g.Desired, Target: st.Queued + st.Busy, Reason: "trabalho na fila"}
	switch {
	case !s.Settings.Enabled:
		d.Target, d.Reason = 0, "pool desligado: drenando"
		return d
	case s.Settings.CapReached(st):
		d.Target, d.Reason = st.Busy, "orçamento mensal de horas esgotado"
	case st.Unhealthy(now):
		d.Target, d.Reason = st.Busy, "pool em espera após subidas sem trabalhador"
	case d.Target < g.Desired && now.Sub(s.lastWork) < s.Settings.IdleAfter:
		d.Target, d.Reason = g.Desired, "aquecido até o fim do período ocioso"
	}
	d.Target = max(min(d.Target, limit), min(st.Busy, g.Max))
	if d.Target == 0 && g.Desired > 0 && d.Reason == "trabalho na fila" {
		d.Reason = "ocioso"
	}
	return d
}

// protect keeps scale-in protection on exactly the instances holding a job,
// touching only instances whose protection AWS lets change (InService,
// Pending): one on its way out never fails the whole call.
func (s *Scaler) protect(ctx context.Context, g Group) error {
	busy, err := s.Store.BusyInstances(ctx)
	if err != nil {
		return err
	}
	var on, off []string
	for _, in := range g.Instances {
		if !in.protectable() {
			continue
		}
		want := slices.Contains(busy, in.ID)
		switch {
		case want && !in.Protected:
			on = append(on, in.ID)
		case !want && in.Protected:
			off = append(off, in.ID)
		}
	}
	var firstErr error
	if len(on) > 0 {
		if err := s.ASG.Protect(ctx, on, true); err != nil {
			firstErr = err
		}
	}
	if len(off) > 0 {
		if err := s.ASG.Protect(ctx, off, false); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
