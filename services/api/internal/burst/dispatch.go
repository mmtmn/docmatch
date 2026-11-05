package burst

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"

	"github.com/mmtmn/docmatch/services/api/internal/harness"
	pb "github.com/mmtmn/docmatch/services/api/internal/harness/pb"
	"github.com/mmtmn/docmatch/services/api/internal/runs"
)

// eventBatch bounds the rows of one read of a job's events.
const eventBatch = 200

// Idle polls between two reads of the run's cancellation flag, and between
// two lease checks of a claimed job (the database decides the expiry).
const (
	cancelCheckEvery = 4
	expireCheckEvery = 6
)

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

// replayable are the end states whose stored events may still be unread: a
// job that ended while the main worker was away is streamed, not redone.
var replayable = []string{StateDone, StateFailed, StateInterrupted, StateLost}

// Attach implements runs.Remote: the run's live job, or its newest job that
// ended with events past afterSeq nobody applied yet. It runs before any new
// placement, so a retry never starts the run elsewhere while the pool still
// has it.
func (d *Dispatcher) Attach(ctx context.Context, rr runs.RemoteRun, afterSeq int64) (runs.RemoteStream, bool, error) {
	job, ok, err := d.Store.ActiveJob(ctx, rr.RunID)
	if err != nil {
		return nil, false, unavailable(err)
	}
	if ok {
		d.log().Info("execução reconectada ao trabalho vivo no pool", slog.String("job_id", job.ID.String()), slog.String("state", job.State))
		return d.stream(ctx, job, rr, afterSeq), true, nil
	}
	job, ok, err = d.Store.LatestJob(ctx, rr.RunID)
	if err != nil {
		return nil, false, unavailable(err)
	}
	if !ok || !slices.Contains(replayable, job.State) {
		return nil, false, nil
	}
	unread, err := d.Store.Events(ctx, job.ID, afterSeq, 1, 1)
	if err != nil {
		return nil, false, unavailable(err)
	}
	if len(unread) == 0 {
		return nil, false, nil
	}
	d.log().Info("execução retoma os eventos de um trabalho já terminado no pool", slog.String("job_id", job.ID.String()), slog.String("state", job.State), slog.Int64("after_seq", afterSeq))
	return d.stream(ctx, job, rr, afterSeq), true, nil
}

// Run implements runs.Remote: queues a new job, reserving its place in the
// pool atomically (runs.ErrPoolFull when there is none).
func (d *Dispatcher) Run(ctx context.Context, rr runs.RemoteRun) (runs.RemoteStream, error) {
	data, err := proto.Marshal(rr.Request)
	if err != nil {
		return nil, fmt.Errorf("burst: serializar o pedido: %w", err)
	}
	id, err := uuid.NewV7()
	if err != nil {
		return nil, err
	}
	job, err := d.Store.CreateJob(ctx, Job{
		ID: id, OrgID: rr.OrgID, RunID: rr.RunID, State: StateQueued, Request: data,
		DeadlineSeconds: int64(max(rr.Deadline, time.Second) / time.Second),
	}, d.Settings.MaxInstances)
	switch {
	case errors.Is(err, ErrPoolFull):
		return nil, fmt.Errorf("%w: %w", runs.ErrPoolFull, err)
	case errors.Is(err, ErrJobActive):
		// Another attempt queued one between Attach and here: re-attach to it.
		live, ok, aerr := d.Store.ActiveJob(ctx, rr.RunID)
		if aerr != nil {
			return nil, unavailable(aerr)
		}
		if !ok {
			return nil, unavailable(err) // it ended meanwhile: the retry looks again
		}
		return d.stream(ctx, live, rr, 0), nil
	case err != nil:
		return nil, unavailable(err)
	}
	if d.Wake != nil {
		d.Wake()
	}
	d.log().Info("execução enviada ao pool sob demanda", slog.String("job_id", job.ID.String()), slog.Int64("deadline_s", job.DeadlineSeconds))
	return d.stream(ctx, job, rr, 0), nil
}

// Drop implements runs.Remote: the run's live job is cancelled (the worker
// stops at its next call).
func (d *Dispatcher) Drop(ctx context.Context, runID uuid.UUID, reason string) error {
	ok, err := d.Store.CancelRun(ctx, runID, reason)
	if err != nil {
		return unavailable(err)
	}
	if ok {
		d.log().Info("trabalho do pool cancelado", slog.String("run_id", runID.String()), slog.String("reason", reason))
	}
	return nil
}

// stream reads the job from cursor until ctx (the run's deadline) ends.
func (d *Dispatcher) stream(ctx context.Context, job Job, rr runs.RemoteRun, cursor int64) *jobStream {
	wait := rr.Wait
	if wait <= 0 {
		wait = d.Settings.ClaimTimeout
	}
	return &jobStream{ctx: ctx, d: d, job: job, wait: wait, cancelled: rr.Cancelled, cursor: cursor}
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

// fill reads the next events into the buffer, bounded in rows and bytes.
func (s *jobStream) fill(ctx context.Context) (int, error) {
	evs, err := s.d.Store.Events(ctx, s.job.ID, s.cursor, eventBatch, s.d.Settings.FillBytes)
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
// stream ends. Ages and leases are decided by the store's clock.
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
	s.idle++
	if s.cancelled != nil && s.idle%cancelCheckEvery == 0 && s.cancelled(ctx) {
		s.Stop(ctx, "cancelada pelo cliente")
		s.end = runs.ErrRemoteCancelled
		return nil
	}
	switch job.State {
	case StateQueued:
		if s.d.now().Sub(job.CreatedAt) < s.wait {
			return nil
		}
		return s.abandon(ctx)
	case StateClaimed:
		if s.idle%expireCheckEvery != 0 {
			return nil
		}
		lost, err := s.d.Store.ExpireLeases(ctx)
		if err != nil {
			return unavailable(err)
		}
		if !slices.Contains(lost, job.ID) {
			return nil // alive (or revived by a renewal the database accepted)
		}
		if n, err := s.fill(ctx); err != nil || n > 0 {
			return err // drain first; the next check sees the job lost
		}
		s.end = fmt.Errorf("%w: concessão do trabalho %s expirou (instância %s)", runs.ErrInterrupted, job.ID, job.InstanceID)
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

// abandon gives up on a job nobody claimed (the store checks the wait with
// its clock) and counts it against the pool unless the pool had no room on
// purpose (off, capped, every instance busy).
func (s *jobStream) abandon(ctx context.Context) error {
	abandoned, err := s.d.Store.Abandon(ctx, s.job.ID, s.wait)
	if err != nil {
		return unavailable(err)
	}
	if !abandoned {
		return nil // claimed meanwhile, or not old enough by the store's clock
	}
	st, err := s.d.Store.Status(ctx, s.d.Settings.Query(s.d.now()))
	switch {
	case err != nil:
		s.d.log().Warn("ler o pool ao abandonar um trabalho falhou", slog.String("err", err.Error()))
	case s.d.Settings.Limited(st):
		s.d.log().Info("trabalho sem lugar no pool (teto ou instâncias ocupadas); vai para a máquina principal", slog.String("job_id", s.job.ID.String()))
	default:
		if err := s.d.Store.NoteNoClaim(ctx, s.d.Settings.NoClaimThreshold, s.d.Settings.Cooldown); err != nil {
			s.d.log().Warn("registrar falta de trabalhador falhou", slog.String("err", err.Error()))
		}
	}
	s.end = runs.ErrNoCapacity
	return nil
}

// Stop implements runs.RemoteStream.
func (s *jobStream) Stop(ctx context.Context, reason string) {
	if _, err := s.d.Store.Cancel(ctx, s.job.ID, reason); err != nil {
		s.d.log().Warn("cancelar trabalho no pool falhou", slog.String("job_id", s.job.ID.String()), slog.String("err", err.Error()))
	}
}
