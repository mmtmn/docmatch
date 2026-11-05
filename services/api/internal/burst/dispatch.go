package burst

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"

	"github.com/mmtmn/docmatch/services/api/internal/harness"
	pb "github.com/mmtmn/docmatch/services/api/internal/harness/pb"
	"github.com/mmtmn/docmatch/services/api/internal/runs"
)

// eventBatch bounds one read of a job's events.
const eventBatch = 200

// cancelCheckEvery is how many idle polls pass between two reads of the run's
// cancellation flag.
const cancelCheckEvery = 4

// Dispatcher hands runs to the pool and streams their events back to the
// main worker (runs.Remote). The run stays owned by the main worker's River
// job: the pool only adds a transport, through Postgres, to a harness on
// another machine.
type Dispatcher struct {
	Store    Store
	Settings Settings
	// Wake nudges the scaler when a job is queued (nil: it notices on its
	// next tick).
	Wake   func()
	Logger *slog.Logger
	Now    func() time.Time
	// Sleep waits between polls (tests replace it).
	Sleep func(ctx context.Context, d time.Duration) error
}

func (d *Dispatcher) now() time.Time {
	if d.Now == nil {
		return time.Now()
	}
	return d.Now()
}

func (d *Dispatcher) log() *slog.Logger {
	if d.Logger == nil {
		return slog.Default()
	}
	return d.Logger
}

func (d *Dispatcher) sleep(ctx context.Context, t time.Duration) error {
	if d.Sleep != nil {
		return d.Sleep(ctx, t)
	}
	timer := time.NewTimer(t)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// unavailable marks a store failure as transient (the River job retries).
func unavailable(err error) error {
	return fmt.Errorf("%w: pool sob demanda: %w", harness.ErrUnavailable, err)
}

// Run implements runs.Remote. A run that already has a live job (the main
// worker restarted mid-run) re-attaches to it instead of queuing another.
func (d *Dispatcher) Run(ctx context.Context, rr runs.RemoteRun) (runs.RemoteStream, error) {
	job, ok, err := d.Store.ActiveJob(ctx, rr.RunID)
	if err != nil {
		return nil, unavailable(err)
	}
	if ok {
		d.log().Info("execução reconectada ao trabalho já no pool", slog.String("job_id", job.ID.String()), slog.String("state", job.State))
		return d.stream(ctx, job, rr), nil
	}
	data, err := proto.Marshal(rr.Request)
	if err != nil {
		return nil, fmt.Errorf("burst: serializar o pedido: %w", err)
	}
	id, err := uuid.NewV7()
	if err != nil {
		return nil, err
	}
	job, err = d.Store.CreateJob(ctx, Job{
		ID: id, OrgID: rr.OrgID, RunID: rr.RunID, State: StateQueued, Request: data,
		DeadlineSeconds: int64(max(rr.Deadline, time.Second) / time.Second), CreatedAt: d.now(),
	})
	if errors.Is(err, ErrJobActive) {
		// Another attempt queued one between the read and the insert.
		if job, ok, err = d.Store.ActiveJob(ctx, rr.RunID); err == nil && ok {
			return d.stream(ctx, job, rr), nil
		}
	}
	if err != nil {
		return nil, unavailable(err)
	}
	if d.Wake != nil {
		d.Wake()
	}
	d.log().Info("execução enviada ao pool sob demanda", slog.String("job_id", job.ID.String()), slog.Int64("deadline_s", job.DeadlineSeconds))
	return d.stream(ctx, job, rr), nil
}

// stream reads the job until ctx (the run's deadline) ends.
func (d *Dispatcher) stream(ctx context.Context, job Job, rr runs.RemoteRun) *jobStream {
	wait := rr.Wait
	if wait <= 0 {
		wait = d.Settings.ClaimTimeout
	}
	return &jobStream{ctx: ctx, d: d, job: job, wait: wait, cancelled: rr.Cancelled}
}

// jobStream reads the events of one job from the store.
type jobStream struct {
	ctx       context.Context //nolint:containedctx // the stream lives as long as the run's context, like a gRPC stream
	d         *Dispatcher
	job       Job
	wait      time.Duration
	cancelled func(context.Context) bool
	cursor    int64
	buf       []*pb.RunEvent
	end       error
	idle      int
}

// Recv implements harness.EventStream. It polls the store: the buffered
// events first, then new events, then the job state, then sleeps.
func (s *jobStream) Recv() (*pb.RunEvent, error) {
	ctx := s.ctx
	for {
		if len(s.buf) > 0 {
			ev := s.buf[0]
			s.buf = s.buf[1:]
			return ev, nil
		}
		if s.end != nil {
			return nil, s.end
		}
		n, err := s.fill(ctx)
		if err != nil {
			return nil, err
		}
		if n > 0 {
			continue
		}
		if err := s.check(ctx); err != nil {
			return nil, err
		}
		if s.end != nil || len(s.buf) > 0 {
			continue
		}
		if err := s.d.sleep(ctx, s.d.Settings.Poll); err != nil {
			return nil, err
		}
	}
}

// fill reads the next events into the buffer.
func (s *jobStream) fill(ctx context.Context) (int, error) {
	evs, err := s.d.Store.Events(ctx, s.job.ID, s.cursor, eventBatch)
	if err != nil {
		return 0, unavailable(err)
	}
	for _, e := range evs {
		ev := &pb.RunEvent{}
		if err := proto.Unmarshal(e.Data, ev); err != nil {
			return 0, fmt.Errorf("burst: evento %d ilegível: %w", e.Seq, err)
		}
		s.buf = append(s.buf, ev)
		s.cursor = e.Seq
	}
	if len(evs) > 0 {
		s.idle = 0
	}
	return len(evs), nil
}

// check looks at the job when no event is pending and decides whether the
// stream ends.
func (s *jobStream) check(ctx context.Context) error {
	job, ok, err := s.d.Store.GetJob(ctx, s.job.ID)
	if err != nil {
		return unavailable(err)
	}
	if !ok {
		s.end = fmt.Errorf("%w: trabalho %s sumiu", runs.ErrInterrupted, s.job.ID)
		return nil
	}
	s.job = job
	now := s.d.now()
	s.idle++
	if s.cancelled != nil && s.idle%cancelCheckEvery == 0 && s.cancelled(ctx) {
		s.Stop(ctx, "cancelada pelo cliente")
		s.end = runs.ErrRemoteCancelled
		return nil
	}
	switch job.State {
	case StateQueued:
		if now.Sub(job.CreatedAt) < s.wait {
			return nil
		}
		abandoned, err := s.d.Store.Abandon(ctx, job.ID, now)
		if err != nil {
			return unavailable(err)
		}
		if !abandoned {
			return nil // claimed in the meantime
		}
		if err := s.d.Store.NoteNoClaim(ctx, s.d.Settings.NoClaimThreshold, now.Add(s.d.Settings.Cooldown)); err != nil {
			s.d.log().Warn("registrar falta de trabalhador falhou", slog.String("err", err.Error()))
		}
		s.end = runs.ErrNoCapacity
	case StateClaimed:
		if job.LeaseExpiresAt != nil && now.After(*job.LeaseExpiresAt) {
			if _, err := s.d.Store.ExpireLeases(ctx, now); err != nil {
				return unavailable(err)
			}
			s.end = fmt.Errorf("%w: concessão do trabalho %s expirou (instância %s)", runs.ErrInterrupted, job.ID, job.InstanceID)
		}
	default:
		// Terminal: drain what is left, then end. A job that finished without
		// the harness's terminal event (that one would have ended the run
		// before this point) is requeued like an interruption.
		if n, err := s.fill(ctx); err != nil || n > 0 {
			return err
		}
		s.end = fmt.Errorf("%w: trabalho %s terminou como %s (%s)", runs.ErrInterrupted, job.ID, job.State, job.Reason)
	}
	return nil
}

// Stop implements runs.RemoteStream.
func (s *jobStream) Stop(ctx context.Context, reason string) {
	if _, err := s.d.Store.Cancel(ctx, s.job.ID, reason, s.d.now()); err != nil {
		s.d.log().Warn("cancelar trabalho no pool falhou", slog.String("job_id", s.job.ID.String()), slog.String("err", err.Error()))
	}
}
