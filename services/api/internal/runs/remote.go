package runs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/mmtmn/docmatch/services/api/internal/estimate"
	"github.com/mmtmn/docmatch/services/api/internal/harness"
	pb "github.com/mmtmn/docmatch/services/api/internal/harness/pb"
)

// Errors of the pool of on-demand workers (docs/escala.md).
var (
	// ErrNoCapacity ends a remote stream when no worker claimed the job in
	// time: nothing ran, and the run falls back to the main box.
	ErrNoCapacity = errors.New("runs: nenhum trabalhador sob demanda assumiu a execução a tempo")
	// ErrInterrupted ends a remote stream when the worker was lost mid-run
	// (Spot interruption, expired lease): the run is requeued from its checkpoint.
	ErrInterrupted = errors.New("runs: o trabalhador sob demanda foi interrompido")
	// ErrRemoteCancelled ends a remote stream when the run was cancelled
	// while it waited for a worker.
	ErrRemoteCancelled = errors.New("runs: execução cancelada enquanto aguardava um trabalhador")
	// ErrRequeue asks the job to run again soon without spending an attempt
	// (the River worker snoozes it).
	ErrRequeue = errors.New("runs: execução devolvida à fila")
)

// MaxInterruptions is how many pool instances a run may lose before it stays
// on the main box (each loss requeues it from its checkpoint).
const MaxInterruptions = 3

// RemoteRun is a run handed to the pool.
type RemoteRun struct {
	OrgID uuid.UUID
	RunID uuid.UUID
	// Request is the harness request, sources with their storage URIs.
	Request *pb.RunRequest
	// Deadline is how long the worker may take once it claims the job.
	Deadline time.Duration
	// Wait is how long the job may wait for a worker before ErrNoCapacity.
	Wait time.Duration
	// Cancelled reports whether the run was cancelled meanwhile (polled while
	// no event arrives); nil never cancels.
	Cancelled func(ctx context.Context) bool
}

// RemoteStream is the event stream of a run on the pool.
type RemoteStream interface {
	harness.EventStream
	// Stop drops the job on the pool (the run was cancelled, timed out or
	// failed on this side); the worker stops at its next call.
	Stop(ctx context.Context, reason string)
}

// Remote runs requests on the pool of on-demand workers. A main-worker
// restart re-attaches to the job already running for the run.
type Remote interface {
	Run(ctx context.Context, run RemoteRun) (RemoteStream, error)
}

// Deadlines scales the deadline of a run with its expected time, on both
// paths: clamp(expected × Factor, Min, Max). Wait is added on the pool path
// (the time a job may wait for a worker before it falls back).
type Deadlines struct {
	Min    time.Duration
	Max    time.Duration
	Factor float64
	Wait   time.Duration
}

// DefaultDeadlines keeps 2 h as the floor (the fixed deadline before it
// scaled) and 24 h as the ceiling.
var DefaultDeadlines = Deadlines{Min: DefaultRunDeadline, Max: 24 * time.Hour, Factor: 3}

// For returns the deadline of a run expected to take expectedSeconds.
func (d Deadlines) For(expectedSeconds int64) time.Duration {
	d = d.normalized()
	scaled := time.Duration(math.Min(float64(max(expectedSeconds, 0))*d.Factor, float64(d.Max/time.Second))) * time.Second
	return min(max(scaled, d.Min), d.Max)
}

// JobTimeout bounds the River job of a run: its deadline, the wait for a
// worker and pad (persistence after the harness is done).
func (d Deadlines) JobTimeout(expectedSeconds int64, pad time.Duration) time.Duration {
	return d.For(expectedSeconds) + max(d.Wait, 0) + pad
}

func (d Deadlines) normalized() Deadlines {
	if d.Min <= 0 {
		d.Min = DefaultDeadlines.Min
	}
	if d.Max < d.Min {
		d.Max = max(DefaultDeadlines.Max, d.Min)
	}
	if d.Factor < 1 || math.IsNaN(d.Factor) || math.IsInf(d.Factor, 0) {
		d.Factor = DefaultDeadlines.Factor
	}
	return d
}

// Deadlines exposes the deadline policy (the River worker sizes its timeout with it).
func (s *Service) Deadlines() Deadlines { return s.d.Deadlines }

// place decides where the run executes now; without a Remote every run stays
// on the main box.
func (s *Service) place(ctx context.Context, t Timing) estimate.Placement {
	if s.d.Remote == nil {
		return estimate.Placement{Reason: estimate.ReasonDisabled}
	}
	if t.Interruptions >= MaxInterruptions {
		return estimate.Placement{Reason: estimate.ReasonInterrupted}
	}
	local := t.LocalSeconds
	if local == 0 {
		local = t.ExpectedSeconds // runs created before placement kept only the quoted time
	}
	return s.d.Estimates.Place(ctx, estimate.PlaceInput{Units: t.Units, LocalSeconds: local, Interruptions: t.Interruptions})
}

// open starts the harness stream where the placement says. On the pool, a
// job nobody claims in time falls back to the main box (routedStream).
func (s *Service) open(ctx context.Context, r *Run, req *pb.RunRequest, place estimate.Placement, budget time.Duration) (harness.EventStream, RemoteStream, error) {
	if !place.Burst {
		stream, err := s.d.Harness.Run(ctx, req)
		return stream, nil, err
	}
	orgID, runID := r.OrgID, r.ID
	remote, err := s.d.Remote.Run(ctx, RemoteRun{
		OrgID: orgID, RunID: runID, Request: req, Deadline: budget, Wait: s.d.Deadlines.Wait,
		Cancelled: func(ctx context.Context) bool {
			cur, ok, gerr := s.d.Store.GetRun(ctx, orgID, runID)
			return gerr == nil && ok && (cur.CancelRequested || cur.Status == StatusCancelled)
		},
	})
	if err != nil {
		s.d.Logger.Warn("pool sob demanda indisponível; execução na máquina principal", slog.String("run_id", r.PublicID()), slog.String("err", err.Error()))
		s.setPlacement(r, estimate.Placement{Reason: estimate.ReasonPoolUnknown})
		stream, lerr := s.d.Harness.Run(ctx, withDeadline(req, budget))
		return stream, nil, lerr
	}
	rs := &routedStream{cur: remote, fallback: func() (harness.EventStream, error) {
		s.d.Logger.Warn("nenhum trabalhador sob demanda a tempo; execução na máquina principal", slog.String("run_id", r.PublicID()))
		s.setPlacement(r, estimate.Placement{Reason: estimate.ReasonNoCapacity})
		return s.d.Harness.Run(ctx, withDeadline(req, budget))
	}}
	return rs, remote, nil
}

// setPlacement records where the run executes in its timing (saved with the
// next event).
func (s *Service) setPlacement(r *Run, p estimate.Placement) {
	t := r.Timing()
	t.Placement, t.Reason = p.Name(), p.Reason
	r.SetTiming(t)
}

// withDeadline copies req with a deadline budget from now (a fallback starts
// after the wait for a worker).
func withDeadline(req *pb.RunRequest, budget time.Duration) *pb.RunRequest {
	c, ok := proto.Clone(req).(*pb.RunRequest)
	if !ok {
		return req
	}
	if c.Options == nil {
		c.Options = &pb.RunOptions{}
	}
	c.Options.Deadline = timestamppb.New(time.Now().Add(budget))
	return c
}

// routedStream reads the remote stream and switches to the main box when the
// pool reports ErrNoCapacity before any event.
type routedStream struct {
	cur      harness.EventStream
	fallback func() (harness.EventStream, error)
	started  bool
	local    bool
}

// Recv implements harness.EventStream.
func (r *routedStream) Recv() (*pb.RunEvent, error) {
	ev, err := r.cur.Recv()
	if err != nil && !r.started && !r.local && errors.Is(err, ErrNoCapacity) {
		r.local = true
		next, ferr := r.fallback()
		if ferr != nil {
			return nil, ferr
		}
		r.cur = next
		ev, err = r.cur.Recv()
	}
	if err == nil {
		r.started = true
	}
	return ev, err
}

// requeue counts the lost instance and hands the run back to the queue: the
// next attempt resumes from the last event saved (Resume, LastSeq).
func (s *Service) requeue(ctx context.Context, r *Run, cause error) error {
	t := r.Timing()
	t.Interruptions++
	r.SetTiming(t)
	saved, err := s.d.Store.SaveRun(ctx, *r, nil)
	if err != nil {
		return fmt.Errorf("runs: gravar interrupção: %w", err)
	}
	*r = saved
	s.d.Logger.Warn("trabalhador sob demanda interrompido; execução volta à fila a partir do último evento",
		slog.String("run_id", r.PublicID()), slog.Int64("last_seq", r.LastSeq), slog.Int("interruptions", t.Interruptions), slog.String("cause", cause.Error()))
	s.record(ctx, r.OrgID, workerActor, "run.requeued", *r, map[string]any{"interruptions": t.Interruptions, "last_seq": r.LastSeq})
	return fmt.Errorf("%w: %w", ErrRequeue, cause)
}
