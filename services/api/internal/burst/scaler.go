package burst

import (
	"context"
	"log/slog"
	"slices"
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
	// State is the ASG lifecycle state (Pending, InService, Terminating...).
	State     string
	Protected bool
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
// zero. It runs in the main worker; without an ASG (manual pool) it only
// expires leases and prunes.
type Scaler struct {
	Store    Store
	ASG      ASG
	Settings Settings
	Logger   *slog.Logger
	Now      func() time.Time
	Interval time.Duration

	wake     chan struct{}
	lastWork time.Time
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

// Tick expires leases, decides the size of the pool and applies it.
func (s *Scaler) Tick(ctx context.Context) (Decision, error) {
	now := s.now()
	if lost, err := s.Store.ExpireLeases(ctx, now); err != nil {
		return Decision{}, err
	} else if len(lost) > 0 {
		s.log().Warn("trabalhos do pool sem notícia do trabalhador; serão retomados", slog.Int("jobs", len(lost)))
	}
	s.ticks++
	if s.ticks%pruneEvery == 1 {
		if n, err := s.Store.Prune(ctx, now.Add(-s.Settings.Retention)); err != nil {
			s.log().Warn("limpar trabalhos antigos do pool falhou", slog.String("err", err.Error()))
		} else if n > 0 {
			s.log().Info("trabalhos antigos do pool removidos", slog.Int64("jobs", n))
		}
	}
	st, err := s.Store.Status(ctx, s.Settings.Query(now))
	if err != nil {
		return Decision{}, err
	}
	if st.Queued+st.Busy > 0 || s.lastWork.IsZero() {
		s.lastWork = now
	}
	if s.ASG == nil {
		return Decision{Status: st, Reason: "pool manual"}, nil
	}
	g, err := s.ASG.Describe(ctx)
	if err != nil {
		return Decision{}, err
	}
	d := s.decide(st, g, now)
	if d.Target != g.Desired {
		if err := s.ASG.SetDesired(ctx, d.Target); err != nil {
			return d, err
		}
		s.log().Info("pool sob demanda redimensionado", slog.Int("from", g.Desired), slog.Int("to", d.Target), slog.String("reason", d.Reason),
			slog.Int("queued", st.Queued), slog.Int("busy", st.Busy), slog.Float64("hours_used", st.UsedHours()))
	}
	return d, s.protect(ctx, g)
}

// decide sizes the pool: work = queued + busy jobs (one per instance),
// bounded by the cap and the group's max; never below the busy ones; warm
// for IdleAfter after the last work unless the monthly budget is used up.
func (s *Scaler) decide(st PoolStatus, g Group, now time.Time) Decision {
	limit := max(min(s.Settings.MaxInstances, g.Max), 0)
	d := Decision{Status: st, Current: g.Desired, Target: st.Queued + st.Busy, Reason: "trabalho na fila"}
	capped := s.Settings.CapReached(st)
	switch {
	case !s.Settings.Enabled:
		d.Target, d.Reason = st.Busy, "pool desligado"
	case capped:
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

// protect keeps scale-in protection on exactly the instances holding a job.
func (s *Scaler) protect(ctx context.Context, g Group) error {
	busy, err := s.Store.BusyInstances(ctx)
	if err != nil {
		return err
	}
	var on, off []string
	for _, in := range g.Instances {
		want := slices.Contains(busy, in.ID)
		switch {
		case want && !in.Protected:
			on = append(on, in.ID)
		case !want && in.Protected:
			off = append(off, in.ID)
		}
	}
	if len(on) > 0 {
		if err := s.ASG.Protect(ctx, on, true); err != nil {
			return err
		}
	}
	if len(off) > 0 {
		return s.ASG.Protect(ctx, off, false)
	}
	return nil
}
