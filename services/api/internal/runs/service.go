package runs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	contracts "github.com/mmtmn/docmatch/packages/contracts/gen"
	"github.com/mmtmn/docmatch/services/api/internal/audit"
	"github.com/mmtmn/docmatch/services/api/internal/credits"
	"github.com/mmtmn/docmatch/services/api/internal/email"
	"github.com/mmtmn/docmatch/services/api/internal/estimate"
	"github.com/mmtmn/docmatch/services/api/internal/harness"
	"github.com/mmtmn/docmatch/services/api/internal/httpx"
	"github.com/mmtmn/docmatch/services/api/internal/jobs/jobargs"
	"github.com/mmtmn/docmatch/services/api/internal/limits"
	"github.com/mmtmn/docmatch/services/api/internal/rules"
	"github.com/mmtmn/docmatch/services/api/internal/runs/apix"
	"github.com/mmtmn/docmatch/services/api/internal/sse"
	"github.com/mmtmn/docmatch/services/api/internal/uploads"
)

// DefaultRunDeadline bounds a run on the harness side.
const DefaultRunDeadline = 2 * time.Hour

// CreditService is what the run needs from the ledger (credits.Service).
type CreditService interface {
	Reserve(ctx context.Context, orgID uuid.UUID, p credits.ReserveParams) (credits.Hold, error)
	Settle(ctx context.Context, orgID, holdID uuid.UUID, actual int64) (credits.Settlement, error)
	Release(ctx context.Context, orgID, holdID uuid.UUID) (credits.Hold, error)
}

// Recorder appends audit entries.
type Recorder interface {
	Record(ctx context.Context, orgID *uuid.UUID, actor audit.Actor, action string, target audit.Target, meta any) (audit.Entry, error)
}

// Outbox enqueues outgoing notifications (webhooks, e-mails); nil disables it.
type Outbox interface {
	Enqueue(ctx context.Context, orgID *uuid.UUID, kind string, payload any) error
}

// Mailer writes e-mail inside the transaction that saves a run (emailpg.TxMailer;
// a nil tx comes from the memory store).
type Mailer interface {
	// UserEmail returns the address of an active user, "" when the user is gone.
	UserEmail(ctx context.Context, tx pgx.Tx, userID uuid.UUID) (string, error)
	// EnqueueOnce writes msg to the e-mail outbox unless key was already used.
	EnqueueOnce(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, key string, msg email.Message) error
}

// Deps are the collaborators of the service.
type Deps struct {
	Store     Store
	Uploads   *uploads.Service
	Estimates *estimate.Service
	Rules     *rules.Service
	Credits   CreditService
	Jobs      jobargs.Enqueuer
	Harness   harness.Client
	Broker    *sse.Broker
	Idem      apix.IdemStore
	Limits    *limits.Limits
	Audit     Recorder
	Outbox    Outbox
	// Mailer and ResultsURL send the e-mail at the end of a long run; either
	// nil turns that e-mail off.
	Mailer Mailer
	// ResultsURL renders the results page of a run for the e-mail.
	ResultsURL func(runID string) string
	Logger     *slog.Logger
	// Now is the business clock (timestamps of the run).
	Now func() time.Time
	// Clock is the real clock that measures the active time of a run.
	Clock func() time.Time
	// URI renders the storage URI of an upload for the harness.
	URI func(u uploads.Upload) string
	// RunDeadline fixes the deadline of every run (tests); zero scales it
	// with the expected time through Deadlines.
	RunDeadline time.Duration
	// Deadlines scales the deadline of a run (zero: DefaultDeadlines).
	Deadlines Deadlines
	// Remote runs heavy runs on the pool of on-demand workers; nil keeps
	// every run on the main box.
	Remote Remote
}

// Service implements the run operations.
type Service struct {
	d Deps
}

// New validates the dependencies.
func New(d Deps) (*Service, error) {
	if d.Store == nil || d.Uploads == nil || d.Estimates == nil || d.Rules == nil || d.Credits == nil || d.Jobs == nil || d.Harness == nil {
		return nil, errors.New("runs: store, uploads, estimates, rules, credits, jobs e harness são obrigatórios")
	}
	if d.Broker == nil {
		d.Broker = sse.NewBroker()
	}
	if d.Idem == nil {
		d.Idem = apix.NewMemIdemStore()
	}
	if d.Limits == nil {
		d.Limits = limits.Must()
	}
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Clock == nil {
		d.Clock = time.Now
	}
	if d.URI == nil {
		d.URI = func(u uploads.Upload) string { return estimate.SourceURI(u, d.Uploads.StoreFor(u).URI) }
	}
	if d.RunDeadline > 0 {
		d.Deadlines = Deadlines{Min: d.RunDeadline, Max: d.RunDeadline, Factor: 1, Wait: d.Deadlines.Wait}
	}
	d.Deadlines = d.Deadlines.normalized()
	return &Service{d: d}, nil
}

// Broker exposes the SSE broker (the worker and tests notify through it).
func (s *Service) Broker() *sse.Broker { return s.d.Broker }

// Idem exposes the idempotency store for the handler.
func (s *Service) Idem() apix.IdemStore { return s.d.Idem }

// Now exposes the clock.
func (s *Service) Now() time.Time { return s.d.Now() }

// CreateRequest is openapi RunCreateRequest.
type CreateRequest struct {
	estimate.Request
	QuoteID        string           `json:"quote_id"`
	QuoteSignature string           `json:"quote_signature"`
	Project        *ProjectSnapshot `json:"-"`
}

// ApproveRequest is openapi ApproveQuoteRequest.
type ApproveRequest struct {
	QuoteID        string `json:"quote_id"`
	QuoteSignature string `json:"quote_signature"`
}

// Create verifies the quote, reserves its cap, inserts the run and
// enqueues the job in the same transaction.
func (s *Service) Create(ctx context.Context, p apix.Principal, req CreateRequest) (Run, error) {
	if store, ok := s.d.Store.(interface {
		WithCreationTx(context.Context, uuid.UUID, func(context.Context) error) error
	}); ok {
		var out Run
		err := store.WithCreationTx(ctx, p.OrgID, func(ctx context.Context) error {
			var err error
			out, err = s.create(ctx, p, req)
			return err
		})
		return out, err
	}
	return s.create(ctx, p, req)
}

func (s *Service) create(ctx context.Context, p apix.Principal, req CreateRequest) (Run, error) {
	if err := s.d.Estimates.CheckAccess(ctx, p, req.Request); err != nil {
		return Run{}, err
	}
	v, err := s.d.Estimates.Validate(p, req.Request)
	if err != nil {
		return Run{}, err
	}
	quoteID, qerr := apix.Decode(apix.PrefixQuote, req.QuoteID)
	if qerr != nil {
		return Run{}, apix.E(contracts.ErrQuoteNotFound, qerr)
	}
	loaded, err := s.d.Estimates.LoadSources(ctx, p.OrgID, v)
	if err != nil {
		return Run{}, err
	}
	rec, err := s.d.Estimates.Verify(ctx, p.OrgID, quoteID, req.QuoteSignature, estimate.Fingerprint(req.Request, loaded.Hashes))
	if err != nil {
		return Run{}, err
	}
	if rec.Kind != estimate.KindInitial || rec.Lane != v.Lane {
		return Run{}, apix.Ef(contracts.ErrQuoteMismatch, "orçamento de outro tipo ou faixa")
	}
	active, err := s.d.Store.CountActiveRuns(ctx, p.OrgID)
	if err != nil {
		return Run{}, fmt.Errorf("runs: contar ativas: %w", err)
	}
	if active >= int64(v.Plan.ConcurrentRuns) {
		return Run{}, apix.Ef(contracts.ErrConcurrentRunsExceeded, "%d execuções ativas (máximo %d)", active, v.Plan.ConcurrentRuns).With(map[string]any{"max": v.Plan.ConcurrentRuns, "retry_after_s": 30})
	}
	id, err := uuid.NewV7()
	if err != nil {
		return Run{}, err
	}
	hold, err := s.d.Credits.Reserve(ctx, p.OrgID, credits.ReserveParams{Amount: rec.Cap, RunID: &id, QuoteID: &quoteID, Reference: "run:" + apix.Encode(apix.PrefixRun, id)})
	if err != nil {
		var ie *credits.InsufficientError
		if errors.As(err, &ie) {
			return Run{}, apix.E(contracts.ErrInsufficientCredits, err).With(map[string]any{"required": ie.Requested, "available": ie.Available})
		}
		return Run{}, fmt.Errorf("runs: reservar créditos: %w", err)
	}
	now := s.d.Now().UTC()
	expires := now.Add(time.Duration(v.Plan.RetentionDays) * 24 * time.Hour)
	run := Run{
		ID: id, OrgID: p.OrgID, WorkflowID: v.WorkflowID, UserID: p.UserID, APIKeyID: p.APIKeyID, QuoteID: &quoteID, HoldID: &hold.ID,
		Lane: v.Lane, Status: StatusQueued, EstimatedCredits: rec.Expected, CreditsHeld: hold.Amount,
		Inputs:    Inputs{Request: req.Request, Plan: p.Plan, UserID: p.UserID, APIKeyID: p.APIKeyID, Lanes: p.Lanes, Project: req.Project},
		ExpiresAt: &expires, CreatedAt: now, UpdatedAt: now,
	}
	// The run keeps the timing of its initial quote: an approval later swaps
	// QuoteID for an additional quote that carries none.
	units := s.d.Estimates.Units(loaded, rec.Quote.Breakdown.Items)
	timing := Timing{ExpectedSeconds: rec.Quote.ExpectedSeconds, Large: rec.Quote.Large, Units: units}
	if rec.Quote.ExpectedSeconds > 0 {
		// The main-box time the run falls back to (the quote shows the time
		// where it will execute); deadlines scale with the larger.
		timing.LocalSeconds = s.d.Estimates.Timing(ctx, units).LocalSeconds
	}
	run.SetTiming(timing)
	created, err := s.d.Store.CreateRun(ctx, run, func(ctx context.Context, tx pgx.Tx) error {
		return s.d.Jobs.EnqueueTx(ctx, tx, jobargs.RunArgs{OrgID: p.OrgID, RunID: id, ExpectedSeconds: timing.Budget()})
	})
	if err != nil {
		if _, rerr := s.d.Credits.Release(ctx, p.OrgID, hold.ID); rerr != nil {
			s.d.Logger.Error("liberar reserva após falha falhou", slog.String("hold_id", hold.ID.String()), slog.String("err", rerr.Error()))
		}
		return Run{}, fmt.Errorf("runs: gravar execução: %w", err)
	}
	if err := s.d.Estimates.Consume(ctx, p.OrgID, quoteID, id); err != nil {
		s.d.Logger.Warn("marcar orçamento como usado falhou", slog.String("err", err.Error()))
	}
	s.record(ctx, p.OrgID, p.Actor(), "run.create", created, map[string]any{"credits_held": created.CreditsHeld, "lane": created.Lane, "quote_id": rec.PublicID()})
	return created, nil
}

// Get returns a run of the organization.
func (s *Service) Get(ctx context.Context, orgID, id uuid.UUID) (Run, error) {
	r, ok, err := s.d.Store.GetRun(ctx, orgID, id)
	if err != nil {
		return Run{}, fmt.Errorf("runs: ler: %w", err)
	}
	if !ok {
		return Run{}, apix.E(contracts.ErrRunNotFound, nil)
	}
	return r, nil
}

// List pages the runs of the organization, newest first.
func (s *Service) List(ctx context.Context, orgID uuid.UUID, f ListFilter, page apix.Page) ([]Run, error) {
	rows, err := s.d.Store.ListRuns(ctx, orgID, f, page.Limit, page.Offset)
	if err != nil {
		return nil, fmt.Errorf("runs: listar: %w", err)
	}
	return rows, nil
}

// View renders a run with its sources and quotes.
func (s *Service) View(ctx context.Context, r Run) View {
	var sources []uploads.View
	if ids := r.SourceIDs(); len(ids) > 0 {
		ups, err := s.d.Uploads.ListReady(ctx, r.OrgID, ids)
		if err != nil {
			// Sources may have been rejected or deleted after the run; show what is still there.
			ups = nil
		}
		for _, u := range ups {
			sources = append(sources, u.View(s.d.Now()))
		}
	}
	quote := s.quote(ctx, r.OrgID, r.QuoteID)
	var pending *estimate.Quote
	if r.Status == StatusNeedsApproval {
		pending = s.quote(ctx, r.OrgID, r.PendingQuoteID)
	}
	return r.View(sources, quote, pending)
}

func (s *Service) quote(ctx context.Context, orgID uuid.UUID, id *uuid.UUID) *estimate.Quote {
	if id == nil {
		return nil
	}
	rec, ok, err := s.d.Estimates.Get(ctx, orgID, *id)
	if err != nil || !ok {
		return nil
	}
	q := rec.Quote
	return &q
}

// Cancel stops a run: credits spent so far are charged, the rest of the
// hold is released, and a terminal event closes the stream. A running
// harness stream notices the cancellation on its next event.
func (s *Service) Cancel(ctx context.Context, p apix.Principal, id uuid.UUID) (Run, error) {
	r, err := s.Get(ctx, p.OrgID, id)
	if err != nil {
		return Run{}, err
	}
	if r.Status.Terminal() {
		return Run{}, apix.E(contracts.ErrRunAlreadyFinished, nil)
	}
	now := s.d.Now().UTC()
	r.Status, r.CancelRequested, r.FinishedAt = StatusCancelled, true, &now
	r.Error = &RunError{Code: string(contracts.ErrRunCancelled), Message: contracts.ErrRunCancelled.Message(), Retryable: false}
	r.ErrorCode = string(contracts.ErrRunCancelled)
	s.settle(ctx, &r)
	saved, err := s.d.Store.SaveRun(ctx, r, nil)
	if err != nil {
		return Run{}, fmt.Errorf("runs: cancelar: %w", err)
	}
	s.appendEvent(ctx, &saved, EventFailed, failedData{Error: *saved.Error, CreditsSpent: saved.CreditsSpent})
	s.dropRemote(ctx, saved, "cancelada pelo cliente")
	if saved.PendingQuoteID != nil {
		_ = s.d.Estimates.Expire(ctx, p.OrgID, *saved.PendingQuoteID)
	}
	s.record(ctx, p.OrgID, p.Actor(), "run.cancel", saved, map[string]any{"credits_spent": saved.CreditsSpent, "credits_refunded": saved.CreditsRefunded})
	return saved, nil
}

// settle charges what was spent since the last settlement and releases the
// remainder of the current hold.
func (s *Service) settle(ctx context.Context, r *Run) {
	if r.HoldID == nil {
		return
	}
	debit := max(r.CreditsSpent-r.CreditsSettled, 0)
	st, err := s.d.Credits.Settle(ctx, r.OrgID, *r.HoldID, debit)
	if err != nil {
		if errors.Is(err, credits.ErrHoldClosed) {
			return
		}
		s.d.Logger.Error("liquidar reserva falhou", slog.String("run_id", r.PublicID()), slog.String("err", err.Error()))
		return
	}
	r.CreditsSettled += st.Debited
	r.CreditsRefunded += st.Released
}

// release gives the whole current hold back (failure).
func (s *Service) release(ctx context.Context, r *Run) {
	if r.HoldID == nil {
		return
	}
	h, err := s.d.Credits.Release(ctx, r.OrgID, *r.HoldID)
	if err != nil {
		if errors.Is(err, credits.ErrHoldClosed) {
			return
		}
		s.d.Logger.Error("liberar reserva falhou", slog.String("run_id", r.PublicID()), slog.String("err", err.Error()))
		return
	}
	r.CreditsRefunded += h.Amount
}

// Approve accepts the additional quote of a paused run: it charges what was
// spent so far on the old hold, reserves the new cap and re-queues the run
// from its checkpoint. An expired quote cancels the run (contract: 410).
func (s *Service) Approve(ctx context.Context, p apix.Principal, id uuid.UUID, req ApproveRequest) (Run, error) {
	if store, ok := s.d.Store.(interface {
		WithCreationTx(context.Context, uuid.UUID, func(context.Context) error) error
	}); ok {
		var out Run
		err := store.WithCreationTx(ctx, p.OrgID, func(ctx context.Context) error {
			var err error
			out, err = s.approve(ctx, p, id, req)
			return err
		})
		// The failed transaction rolls back its cancellation too. Preserve the
		// existing expiry behavior after the approval unit has rolled back.
		var apiErr *apix.Error
		if errors.As(err, &apiErr) && apiErr.Code == contracts.ErrQuoteExpired {
			if _, cancelErr := s.Cancel(ctx, p, id); cancelErr != nil {
				s.d.Logger.Warn("cancelar orçamento expirado falhou", slog.String("err", cancelErr.Error()))
			}
		}
		return out, err
	}
	return s.approve(ctx, p, id, req)
}

func (s *Service) approve(ctx context.Context, p apix.Principal, id uuid.UUID, req ApproveRequest) (Run, error) {
	r, err := s.Get(ctx, p.OrgID, id)
	if err != nil {
		return Run{}, err
	}
	if r.Status != StatusNeedsApproval || r.PendingQuoteID == nil {
		return Run{}, apix.E(contracts.ErrRunNotAwaitingApproval, nil)
	}
	quoteID, qerr := apix.Decode(apix.PrefixQuote, req.QuoteID)
	if qerr != nil {
		return Run{}, apix.E(contracts.ErrQuoteNotFound, qerr)
	}
	if quoteID != *r.PendingQuoteID {
		return Run{}, apix.Ef(contracts.ErrQuoteMismatch, "orçamento não é o pendente desta execução")
	}
	rec, err := s.d.Estimates.Verify(ctx, p.OrgID, quoteID, req.QuoteSignature, "")
	if err != nil {
		var ae *apix.Error
		if errors.As(err, &ae) && ae.Code == contracts.ErrQuoteExpired {
			if _, cerr := s.Cancel(ctx, p, id); cerr != nil {
				s.d.Logger.Error("cancelar após orçamento expirado falhou", slog.String("err", cerr.Error()))
			}
		}
		return Run{}, err
	}
	s.settle(ctx, &r)
	hold, err := s.d.Credits.Reserve(ctx, p.OrgID, credits.ReserveParams{Amount: rec.Cap, RunID: &id, QuoteID: &quoteID, Reference: "run:" + r.PublicID() + ":additional"})
	if err != nil {
		var ie *credits.InsufficientError
		if errors.As(err, &ie) {
			// The old hold is settled; persist that before answering.
			if _, serr := s.d.Store.SaveRun(ctx, r, nil); serr != nil {
				s.d.Logger.Error("gravar liquidação falhou", slog.String("err", serr.Error()))
			}
			return Run{}, apix.E(contracts.ErrInsufficientCredits, err).With(map[string]any{"required": ie.Requested, "available": ie.Available})
		}
		return Run{}, fmt.Errorf("runs: reservar créditos adicionais: %w", err)
	}
	r.HoldID, r.CreditsHeld = &hold.ID, hold.Amount
	r.QuoteID, r.PendingQuoteID = &quoteID, nil
	r.Status = StatusQueued
	saved, err := s.d.Store.SaveRun(ctx, r, func(ctx context.Context, tx pgx.Tx) error {
		return s.d.Jobs.EnqueueTx(ctx, tx, jobargs.RunArgs{OrgID: p.OrgID, RunID: id, Resume: true, ExpectedSeconds: r.Timing().Budget()})
	})
	if err != nil {
		if _, rerr := s.d.Credits.Release(ctx, p.OrgID, hold.ID); rerr != nil {
			s.d.Logger.Error("liberar reserva após falha falhou", slog.String("err", rerr.Error()))
		}
		return Run{}, fmt.Errorf("runs: retomar: %w", err)
	}
	if err := s.d.Estimates.Consume(ctx, p.OrgID, quoteID, id); err != nil {
		s.d.Logger.Warn("marcar orçamento como usado falhou", slog.String("err", err.Error()))
	}
	s.record(ctx, p.OrgID, p.Actor(), "run.approve_quote", saved, map[string]any{"quote_id": rec.PublicID(), "credits_held": saved.CreditsHeld})
	return saved, nil
}

// appendEvent persists an API-originated event (seq = last + 1) and wakes
// the streams. Harness events keep their own seq (see worker.go).
func (s *Service) appendEvent(ctx context.Context, r *Run, name string, data any) {
	r.LastSeq++
	ev := Event{OrgID: r.OrgID, RunID: r.ID, Seq: r.LastSeq, Name: name, Data: mustJSON(data), CreatedAt: s.d.Now().UTC()}
	if err := s.d.Store.AppendEvent(ctx, ev); err != nil {
		s.d.Logger.Error("gravar evento falhou", slog.String("run_id", r.PublicID()), slog.String("err", err.Error()))
		return
	}
	if _, err := s.d.Store.SaveRun(ctx, *r, nil); err != nil {
		s.d.Logger.Error("gravar sequência falhou", slog.String("run_id", r.PublicID()), slog.String("err", err.Error()))
	}
	s.d.Broker.Notify(r.ID)
}

// Findings pages the findings of a run.
func (s *Service) Findings(ctx context.Context, orgID, runID uuid.UUID, f FindingFilter, page apix.Page) ([]Finding, error) {
	if _, err := s.Get(ctx, orgID, runID); err != nil {
		return nil, err
	}
	rows, err := s.d.Store.ListFindings(ctx, orgID, runID, f, page.Limit, page.Offset)
	if err != nil {
		return nil, fmt.Errorf("runs: listar achados: %w", err)
	}
	return rows, nil
}

// FeedbackRequest is openapi FindingFeedbackRequest.
type FeedbackRequest struct {
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
	Note   string `json:"note,omitempty"`
}

var (
	feedbackStatuses = map[string]bool{"confirmed": true, "dismissed": true, "resolved": true}
	feedbackReasons  = map[string]bool{"false_positive": true, "already_known": true, "wrong_amount": true, "duplicate": true, "other": true}
)

// GiveFeedback records the user's verdict on a finding. Repeating the same
// verdict changes nothing; a different one appends a new feedback row and
// updates the finding. Plans with learning get a rule-proposal job.
func (s *Service) GiveFeedback(ctx context.Context, p apix.Principal, runID, findingID uuid.UUID, req FeedbackRequest) (Finding, error) {
	var fields []apix.FieldError
	if !feedbackStatuses[req.Status] {
		fields = append(fields, apix.FieldError{Path: "/status", Code: "invalid"})
	}
	if req.Reason != "" && !feedbackReasons[req.Reason] {
		fields = append(fields, apix.FieldError{Path: "/reason", Code: "invalid"})
	}
	note := ""
	if req.Note != "" {
		n, err := httpx.ValidateText(req.Note, s.d.Limits.Text.FindingFeedbackNote, false)
		if errors.Is(err, httpx.ErrTooLong) {
			return Finding{}, apix.E(contracts.ErrTextTooLong, err).With(map[string]any{"path": "/note", "max": s.d.Limits.Text.FindingFeedbackNote})
		}
		if err != nil {
			fields = append(fields, apix.FieldError{Path: "/note", Code: "control_characters"})
		}
		note = n
	}
	if len(fields) > 0 {
		return Finding{}, apix.Validation(fields...)
	}
	r, err := s.Get(ctx, p.OrgID, runID)
	if err != nil {
		return Finding{}, err
	}
	f, ok, err := s.d.Store.GetFinding(ctx, p.OrgID, runID, findingID)
	if err != nil {
		return Finding{}, fmt.Errorf("runs: ler achado: %w", err)
	}
	if !ok {
		return Finding{}, apix.E(contracts.ErrFindingNotFound, nil)
	}
	if f.Feedback != nil && f.Feedback.Status == req.Status && f.Feedback.Reason == req.Reason && f.Feedback.Note == note {
		return f, nil
	}
	now := s.d.Now().UTC()
	if err := s.d.Store.InsertFeedback(ctx, Feedback{OrgID: p.OrgID, UserID: p.UserID, RunID: runID, FindingID: f.ID, FindingCreatedAt: f.CreatedAt, Status: req.Status, Reason: req.Reason, Note: note, CreatedAt: now}); err != nil {
		return Finding{}, fmt.Errorf("runs: gravar feedback: %w", err)
	}
	fb := &FeedbackView{Status: req.Status, Reason: req.Reason, Note: note, At: now.Format(time.RFC3339)}
	if p.UserID != nil {
		fb.By = apix.Encode(apix.PrefixUser, *p.UserID)
	}
	updated, err := s.d.Store.UpdateFindingFeedback(ctx, p.OrgID, runID, f.ID, req.Status, fb)
	if err != nil {
		return Finding{}, fmt.Errorf("runs: atualizar achado: %w", err)
	}
	if plan, ok := s.d.Estimates.Pricing().Plan(p.Plan); ok && plan.AutoRuleLearning {
		if err := s.d.Jobs.Enqueue(ctx, jobargs.LearnArgs{OrgID: p.OrgID, RunID: runID}); err != nil {
			s.d.Logger.Warn("enfileirar aprendizado falhou", slog.String("err", err.Error()))
		}
	}
	s.record(ctx, p.OrgID, p.Actor(), "finding.feedback", r, map[string]any{"finding_id": updated.PublicID(), "status": req.Status, "reason": req.Reason})
	return updated, nil
}

// EventsAfter implements sse.Source for the stream handler.
func (s *Service) EventsAfter(ctx context.Context, orgID, runID uuid.UUID, afterSeq int64, limit int) ([]sse.Event, error) {
	return s.d.Store.EventsAfter(ctx, orgID, runID, afterSeq, limit)
}

// Learn is the rule-learning job: it sends the confirmed and dismissed
// findings of a run to the harness and stores the proposed rules.
func (s *Service) Learn(ctx context.Context, orgID, runID uuid.UUID) error {
	r, err := s.Get(ctx, orgID, runID)
	if err != nil {
		var ae *apix.Error
		if errors.As(err, &ae) && ae.Code == contracts.ErrRunNotFound {
			return nil
		}
		return err
	}
	findings, err := s.d.Store.ListFindingsWithFeedback(ctx, orgID, runID)
	if err != nil {
		return fmt.Errorf("runs: achados com feedback: %w", err)
	}
	req := proposeRequest(orgID, r, findings)
	if len(req.ConfirmedFindings) == 0 && len(req.DismissedFindings) == 0 {
		return nil
	}
	resp, err := s.d.Harness.ProposeRules(ctx, req)
	if err != nil {
		return fmt.Errorf("runs: aprender regras: %w", err)
	}
	ruleCtx := ctx
	if r.Inputs.Project != nil {
		ruleCtx = rules.WithProject(ctx, orgID, r.Inputs.Project.ID)
	}
	proposed, err := s.d.Rules.Propose(ruleCtx, orgID, &runID, resp.GetRules())
	if err != nil {
		return err
	}
	if len(proposed) > 0 && r.Summary != nil {
		for _, rule := range proposed {
			r.Summary.ProposedRuleIDs = append(r.Summary.ProposedRuleIDs, rule.PublicID())
		}
		if _, err := s.d.Store.SaveRun(ctx, r, nil); err != nil {
			s.d.Logger.Warn("gravar regras propostas no resumo falhou", slog.String("err", err.Error()))
		}
	}
	s.d.Logger.Info("regras propostas", slog.String("run_id", r.PublicID()), slog.Int("rules", len(proposed)))
	return nil
}

func (s *Service) record(ctx context.Context, orgID uuid.UUID, actor audit.Actor, action string, r Run, meta map[string]any) {
	if s.d.Audit == nil {
		return
	}
	org := orgID
	if _, err := s.d.Audit.Record(ctx, &org, actor, action, audit.Target{Type: "run", ID: r.PublicID()}, meta); err != nil {
		s.d.Logger.Error("auditoria falhou", slog.String("action", action), slog.String("err", err.Error()))
	}
}

// notify publishes an outbox event when an outbox is configured.
func (s *Service) notify(ctx context.Context, r Run, kind string, payload any) {
	if s.d.Outbox == nil {
		return
	}
	org := r.OrgID
	if err := s.d.Outbox.Enqueue(ctx, &org, kind, payload); err != nil {
		s.d.Logger.Warn("enfileirar notificação falhou", slog.String("kind", kind), slog.String("err", err.Error()))
	}
}
