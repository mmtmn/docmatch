package runs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	contracts "github.com/mmtmn/docmatch/packages/contracts/gen"
	"github.com/mmtmn/docmatch/services/api/internal/audit"
	"github.com/mmtmn/docmatch/services/api/internal/estimate"
	"github.com/mmtmn/docmatch/services/api/internal/harness"
	pb "github.com/mmtmn/docmatch/services/api/internal/harness/pb"
	"github.com/mmtmn/docmatch/services/api/internal/rules"
	"github.com/mmtmn/docmatch/services/api/internal/runs/apix"
)

// ErrRetry asks River to retry the job (transient harness failure); the
// run stays running and the next attempt resumes from the checkpoint.
var ErrRetry = errors.New("runs: falha transitória; repetir")

var workerActor = audit.Actor{Type: audit.ActorSystem, ID: "worker"}

// Execute is the run job: it streams the harness and persists everything.
// lastAttempt tells it to fail the run instead of asking for a retry.
func (s *Service) Execute(ctx context.Context, orgID, runID uuid.UUID, lastAttempt bool) error {
	r, ok, err := s.d.Store.GetRun(ctx, orgID, runID)
	if err != nil {
		return fmt.Errorf("runs: ler execução: %w", err)
	}
	if !ok || r.Status.Terminal() || r.Status == StatusNeedsApproval {
		if ok && r.Status.Terminal() {
			s.dropRemote(ctx, r, "execução já encerrada")
		}
		return nil
	}
	log := s.d.Logger.With(slog.String("run_id", r.PublicID()))
	resume := r.LastSeq > 0 || r.StartedAt != nil
	a := &attempt{priorMs: r.Timing().ActiveMs}
	p := r.Inputs.Principal(orgID)

	v, err := s.d.Estimates.Validate(p, r.Inputs.Request)
	if err != nil {
		return s.failOnSetup(ctx, &r, err, contracts.ErrValidationFailed)
	}
	loaded, err := s.d.Estimates.LoadSources(ctx, orgID, v)
	if err != nil {
		return s.failOnSetup(ctx, &r, err, contracts.ErrRunSourceUnreadable)
	}
	if r.QuoteID == nil {
		return s.failOnSetup(ctx, &r, errors.New("execução sem orçamento"), contracts.ErrInternalError)
	}
	quoteRec, ok, err := s.d.Estimates.Get(ctx, orgID, *r.QuoteID)
	if err != nil || !ok {
		return s.failOnSetup(ctx, &r, fmt.Errorf("orçamento %s: %w", r.QuoteID, errors.Join(err, errors.New("não encontrado"))), contracts.ErrInternalError)
	}
	resolved, err := s.d.Rules.Resolve(ctx, orgID, r.Inputs.Request.UseRules(), v.RuleIDs)
	if err != nil {
		return s.failOnSetup(ctx, &r, err, contracts.ErrRuleNotFound)
	}
	pbRules := make([]*pb.Rule, 0, len(resolved))
	ruleIDs := make([]uuid.UUID, 0, len(resolved))
	for _, rule := range resolved {
		pbRules = append(pbRules, rules.ToProto(rule))
		ruleIDs = append(ruleIDs, rule.ID)
	}
	workflowID := ""
	if r.WorkflowID != nil {
		workflowID = apix.Encode(apix.PrefixWorkflow, *r.WorkflowID)
	}
	// The deadline scales with the expected time (the larger of the quoted
	// and the main-box time, so a fallback never times out). Deadlines cross
	// processes, so they come from the real clock; s.d.Now is the business
	// clock (timestamps of the run), which tests pin in the past.
	timing := r.Timing()
	budget := s.d.Deadlines.For(timing.Budget())
	deadline := time.Now().Add(budget)
	// On the pool the job may first wait for a worker (Deadlines.Wait).
	window := budget
	if s.d.Remote != nil {
		window += s.d.Deadlines.Wait
	}
	runCtx, cancel := context.WithTimeout(ctx, window)
	defer cancel()
	// A job the pool already has for this run wins over a new decision.
	place, attached, err := s.attachOrPlace(runCtx, &r, timing, budget)
	if err != nil {
		return s.streamFailure(ctx, &r, err, lastAttempt)
	}
	req := &pb.RunRequest{
		RunId: r.PublicID(), TenantId: apix.Encode(apix.PrefixOrg, orgID),
		Sources: append(estimate.BuildSources(r.Inputs.Request.Sources, loaded.Docs, s.d.URI), estimate.BuildSources(r.Inputs.Request.MediaSources, loaded.Media, s.d.URI)...),
		Options: estimate.BuildOptions(p, r.Inputs.Request, v, r.CreditsHeld, &deadline, workflowID),
		Quote:   QuoteToProto(quoteRec.Quote), Rules: pbRules, Resume: resume && sameHarness(timing, place),
	}
	moved := timing.Placement != place.Name() || timing.Reason != place.Reason
	if moved {
		s.setPlacement(&r, place)
	}
	if r.Status == StatusQueued || moved {
		now := s.d.Now().UTC()
		r.Status = StatusRunning
		if r.StartedAt == nil {
			r.StartedAt = &now
		}
		if r, err = s.d.Store.SaveRun(ctx, r, nil); err != nil {
			return fmt.Errorf("runs: marcar em execução: %w", err)
		}
	}
	stream, remote, err := s.open(runCtx, &r, req, place, budget, attached)
	if err != nil {
		return s.streamFailure(ctx, &r, err, lastAttempt)
	}
	stop := func(reason string) {
		if remote != nil {
			remote.Stop(context.WithoutCancel(ctx), reason)
		}
	}
	if err := s.d.Rules.Touch(ctx, orgID, ruleIDs); err != nil {
		log.Warn("registrar uso das regras falhou", slog.String("err", err.Error()))
	}
	log.Info("execução iniciada no harness", slog.Bool("resume", resume), slog.Int("sources", len(req.Sources)), slog.Int("rules", len(pbRules)),
		slog.String("placement", place.Name()), slog.String("reason", place.Reason), slog.Duration("deadline", budget))
	for {
		ev, err := stream.Recv()
		if err != nil {
			s.touchActive(&r, a)
		}
		if errors.Is(err, io.EOF) {
			return s.fail(ctx, &r, contracts.ErrInternalError, "o harness encerrou o fluxo sem um evento final")
		}
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
				stop("prazo esgotado")
				return s.fail(ctx, &r, contracts.ErrRunTimeout, "prazo da execução esgotado")
			}
			if errors.Is(err, ErrRemoteCancelled) {
				log.Info("execução cancelada pelo cliente enquanto aguardava um trabalhador")
				return nil
			}
			if errors.Is(err, ErrInterrupted) {
				return s.requeue(ctx, &r, err)
			}
			if lastAttempt || !harness.IsUnavailable(err) {
				stop("falha do lado principal") // the run fails here: the pool job stops too
			}
			return s.streamFailure(ctx, &r, err, lastAttempt)
		}
		if a.begin.IsZero() {
			a.begin = s.d.Clock()
		}
		if cur, ok, gerr := s.d.Store.GetRun(ctx, orgID, runID); gerr == nil && ok && (cur.CancelRequested || cur.Status == StatusCancelled) {
			log.Info("execução cancelada pelo cliente; fluxo interrompido")
			stop("cancelada pelo cliente")
			return nil
		}
		terminal, err := s.applyEvent(ctx, &r, p, ev, a)
		if err != nil {
			return err
		}
		if terminal {
			return nil
		}
	}
}

// sameHarness reports whether this segment of the run executes on the
// harness that ran the previous one, the only one holding its checkpoint
// (RunRequest.resume). On the pool every attempt may land on another
// instance, so it is a replay: the harness is deterministic and the events
// already saved are skipped by seq (applyEvent).
func sameHarness(previous Timing, place estimate.Placement) bool {
	return !place.Burst && previous.Placement != estimate.PlacementBurst
}

// streamFailure decides between a retry and a permanent failure.
func (s *Service) streamFailure(ctx context.Context, r *Run, err error, lastAttempt bool) error {
	if harness.IsUnavailable(err) && !lastAttempt {
		s.d.Logger.Warn("harness indisponível; a execução será retomada", slog.String("run_id", r.PublicID()), slog.String("err", err.Error()))
		return fmt.Errorf("%w: %w", ErrRetry, err)
	}
	code := contracts.ErrHarnessUnavailable
	if !harness.IsUnavailable(err) {
		code = contracts.ErrInternalError
	}
	return s.fail(ctx, r, code, err.Error())
}

// failOnSetup fails the run for a reason found before the harness was
// called; apix errors keep their code.
func (s *Service) failOnSetup(ctx context.Context, r *Run, err error, fallback contracts.ErrorCode) error {
	code := fallback
	var ae *apix.Error
	if errors.As(err, &ae) {
		code = ae.Code
	}
	return s.fail(ctx, r, code, err.Error())
}

// fail marks the run failed, releases the hold and emits the terminal event.
func (s *Service) fail(ctx context.Context, r *Run, code contracts.ErrorCode, detail string) error {
	now := s.d.Now().UTC()
	r.Status, r.FinishedAt, r.ErrorCode = StatusFailed, &now, string(code)
	r.Error = &RunError{Code: string(code), Message: code.Message(), Retryable: code.Retryable()}
	s.release(ctx, r)
	saved, err := s.d.Store.SaveRun(ctx, *r, s.finishedHook(*r))
	if err != nil {
		return fmt.Errorf("runs: gravar falha: %w", err)
	}
	*r = saved
	s.recordTiming(ctx, *r)
	s.appendEvent(ctx, r, EventFailed, failedData{Error: *r.Error, CreditsSpent: r.CreditsSpent})
	s.d.Logger.Error("execução falhou", slog.String("run_id", r.PublicID()), slog.String("code", string(code)), slog.String("detail", detail))
	s.record(ctx, r.OrgID, workerActor, "run.failed", *r, map[string]any{"code": code, "credits_spent": r.CreditsSpent})
	s.notify(ctx, *r, "run.failed", map[string]any{"run_id": r.PublicID(), "error": r.Error})
	return nil
}

// applyEvent persists one harness event and its side effects; a carries the
// active time of the attempt, saved with every event so a retry keeps it.
func (s *Service) applyEvent(ctx context.Context, r *Run, p apix.Principal, ev *pb.RunEvent, a *attempt) (bool, error) {
	seq := int64(min(ev.GetSeq(), 1<<62)) //nolint:gosec // bounded
	if seq <= r.LastSeq {
		return false, nil // replayed after a resume
	}
	now := s.d.Now().UTC()
	var (
		name     string
		data     any
		terminal bool
	)
	switch e := ev.GetEvent().(type) {
	case *pb.RunEvent_StageStarted:
		name = EventStageStarted
		r.Progress = &Progress{Stage: StageName(e.StageStarted.GetStage()), Percent: 0, Message: e.StageStarted.GetMessage()}
		data = stageStartedData{Stage: r.Progress.Stage, Message: e.StageStarted.GetMessage(), TotalSteps: e.StageStarted.GetTotalSteps()}
	case *pb.RunEvent_Progress:
		name = EventProgress
		pr := e.Progress
		percent := 0
		if pr.GetTotal() > 0 {
			percent = int(min(pr.GetDone()*100/pr.GetTotal(), 100)) //nolint:gosec // bounded
		}
		r.Progress = &Progress{Stage: StageName(pr.GetStage()), Percent: percent, Message: pr.GetMessage()}
		if spent := int64(min(pr.GetCreditsSpent(), 1<<62)); spent > r.CreditsSpent { //nolint:gosec // bounded
			r.CreditsSpent = spent
		}
		data = progressData{Stage: r.Progress.Stage, Done: pr.GetDone(), Total: pr.GetTotal(), Message: pr.GetMessage(), CreditsSpent: pr.GetCreditsSpent()}
	case *pb.RunEvent_TokenUsage:
		name = EventTokenUsage
		u := e.TokenUsage
		if spent := int64(min(u.GetCreditsSpentTotal(), 1<<62)); spent > r.CreditsSpent { //nolint:gosec // bounded
			r.CreditsSpent = spent
		}
		data = tokenUsageData{Role: u.GetRole(), Model: u.GetModel(), PromptTokens: u.GetPromptTokens(), CompletionTokens: u.GetCompletionTokens(), CachedTokens: u.GetCachedTokens(), Credits: u.GetCredits(), CreditsSpentTotal: u.GetCreditsSpentTotal(), Cached: u.GetCached()}
	case *pb.RunEvent_Finding:
		name = EventFinding
		f, err := s.d.Store.InsertFinding(ctx, FindingFromProto(r.OrgID, r.ID, e.Finding, now))
		if err != nil {
			return false, fmt.Errorf("runs: gravar achado: %w", err)
		}
		data = findingData{Finding: f.View()}
	case *pb.RunEvent_Match:
		name = EventMatch
		m, err := s.d.Store.InsertMatch(ctx, MatchFromProto(r.OrgID, r.ID, e.Match, now))
		if err != nil {
			return false, fmt.Errorf("runs: gravar casamento: %w", err)
		}
		data = matchData{Match: m.View()}
	case *pb.RunEvent_NeedsApproval:
		name, terminal = EventNeedsApproval, true
		na := e.NeedsApproval
		if spent := int64(min(na.GetCreditsSpentSoFar(), 1<<62)); spent > r.CreditsSpent { //nolint:gosec // bounded
			r.CreditsSpent = spent
		}
		q, err := s.d.Estimates.PriceQuote(ctx, na.GetQuote(), r.Lane)
		if err != nil {
			return false, fmt.Errorf("runs: precificar orçamento adicional: %w", err)
		}
		q.RequiresApproval = true
		rec, err := s.d.Estimates.Issue(ctx, p, r.Lane, estimate.KindAdditional, nil, "", q, &r.ID)
		if err != nil {
			return false, err
		}
		r.PendingQuoteID, r.Status = &rec.ID, StatusNeedsApproval
		data = needsApprovalData{Quote: rec.Quote, Reasons: estimate.ApprovalReasons(na.GetReasons()), Message: na.GetMessage(), CreditsSpentSoFar: r.CreditsSpent}
	case *pb.RunEvent_Completed:
		name, terminal = EventCompleted, true
		c := e.Completed
		if spent := int64(min(c.GetCreditsSpent(), 1<<62)); spent > r.CreditsSpent { //nolint:gosec // bounded
			r.CreditsSpent = spent
		}
		r.Summary = SummaryFromProto(c.GetSummary())
		r.Status, r.FinishedAt = StatusCompleted, &now
		r.Progress = &Progress{Stage: StageName(pb.Stage_STAGE_REPORT), Percent: 100, Message: "Concluída"}
		s.settle(ctx, r)
		data = completedData{Summary: *r.Summary, CreditsSpent: r.CreditsSpent, CreditsRefunded: r.CreditsRefunded}
	case *pb.RunEvent_Failed:
		name, terminal = EventFailed, true
		f := e.Failed
		if spent := int64(min(f.GetCreditsSpent(), 1<<62)); spent > r.CreditsSpent { //nolint:gosec // bounded
			r.CreditsSpent = spent
		}
		code := contracts.ErrorCode(f.GetCode())
		if !code.Known() {
			code = contracts.ErrInternalError
		}
		r.Error = &RunError{Code: string(code), Message: code.Message(), Retryable: f.GetRetryable()}
		if f.GetDetails() != nil {
			r.Error.Details = f.GetDetails().AsMap()
		}
		r.ErrorCode, r.Status, r.FinishedAt = string(code), StatusFailed, &now
		s.release(ctx, r)
		data = failedData{Error: *r.Error, CreditsSpent: r.CreditsSpent}
	default:
		return false, nil
	}
	if err := s.d.Store.AppendEvent(ctx, Event{OrgID: r.OrgID, RunID: r.ID, Seq: seq, Name: name, Data: mustJSON(data), CreatedAt: now}); err != nil {
		return false, fmt.Errorf("runs: gravar evento %d: %w", seq, err)
	}
	r.LastSeq = seq
	s.touchActive(r, a)
	var hook TxHook
	if name == EventCompleted || name == EventFailed {
		hook = s.finishedHook(*r)
	}
	saved, err := s.d.Store.SaveRun(ctx, *r, hook)
	if err != nil {
		return false, fmt.Errorf("runs: gravar estado: %w", err)
	}
	*r = saved
	s.d.Broker.Notify(r.ID)
	if terminal {
		s.recordTiming(ctx, *r)
		s.afterTerminal(ctx, *r, name)
	}
	return terminal, nil
}

func (s *Service) afterTerminal(ctx context.Context, r Run, name string) {
	switch name {
	case EventCompleted:
		s.record(ctx, r.OrgID, workerActor, "run.completed", r, map[string]any{"credits_spent": r.CreditsSpent, "credits_refunded": r.CreditsRefunded, "findings": summaryCount(r)})
		s.notify(ctx, r, "run.completed", map[string]any{"run_id": r.PublicID(), "summary": r.Summary, "credits_spent": r.CreditsSpent})
	case EventFailed:
		s.record(ctx, r.OrgID, workerActor, "run.failed", r, map[string]any{"code": r.ErrorCode, "credits_spent": r.CreditsSpent})
		s.notify(ctx, r, "run.failed", map[string]any{"run_id": r.PublicID(), "error": r.Error})
	case EventNeedsApproval:
		s.record(ctx, r.OrgID, workerActor, "run.needs_approval", r, map[string]any{"credits_spent": r.CreditsSpent, "pending_quote_id": apix.EncodePtr(apix.PrefixQuote, r.PendingQuoteID)})
		s.notify(ctx, r, "run.needs_approval", map[string]any{"run_id": r.PublicID(), "pending_quote_id": apix.EncodePtr(apix.PrefixQuote, r.PendingQuoteID)})
	}
}

func summaryCount(r Run) int {
	if r.Summary == nil {
		return 0
	}
	return r.Summary.FindingsTotal
}

// proposeRequest builds the learning request from findings with feedback.
func proposeRequest(orgID uuid.UUID, r Run, findings []Finding) *pb.ProposeRulesRequest {
	req := &pb.ProposeRulesRequest{TenantId: apix.Encode(apix.PrefixOrg, orgID), RunId: r.PublicID(), MaxRules: 5}
	if o := r.Inputs.Request.Options; o != nil {
		switch o.LlmMode {
		case "openrouter":
			req.LlmMode = pb.LlmMode_LLM_MODE_OPENROUTER
		case "local":
			req.LlmMode = pb.LlmMode_LLM_MODE_LOCAL
		case "off":
			req.LlmMode = pb.LlmMode_LLM_MODE_OFF
		}
	}
	for _, f := range findings {
		switch f.Status {
		case "confirmed", "resolved":
			req.ConfirmedFindings = append(req.ConfirmedFindings, FindingToProto(f))
		case "dismissed":
			req.DismissedFindings = append(req.DismissedFindings, FindingToProto(f))
		}
	}
	return req
}

// FindingToProto renders a stored finding for the harness.
func FindingToProto(f Finding) *pb.Finding {
	out := &pb.Finding{
		Id: f.PublicID(), RunId: apix.Encode(apix.PrefixRun, f.RunID), Code: f.Code,
		Kind: pb.FindingKind(pb.FindingKind_value["FINDING_KIND_"+strings.ToUpper(f.Kind)]), Severity: pb.Severity(pb.Severity_value["SEVERITY_"+strings.ToUpper(f.Severity)]),
		AmountAtRisk: f.AmountAtRisk, Currency: f.Currency, Title: f.Title, Explanation: f.Explanation, Confidence: f.Confidence,
		Status:    pb.FindingStatus(pb.FindingStatus_value["FINDING_STATUS_"+strings.ToUpper(f.Status)]),
		SourceIds: f.SourceIDs, CreatedAt: timestamppb.New(f.CreatedAt), Evidence: EvidenceFromJSON(f.Evidence),
	}
	if f.RuleID != nil {
		out.RuleId = apix.Encode(apix.PrefixRule, *f.RuleID)
	}
	if f.MatchID != nil {
		out.MatchId = apix.Encode(apix.PrefixMatch, *f.MatchID)
	}
	if len(f.Details) > 0 {
		var m map[string]any
		if json.Unmarshal(f.Details, &m) == nil {
			if st, err := structpb.NewStruct(m); err == nil {
				out.Details = st
			}
		}
	}
	return out
}

// EvidenceFromJSON parses the stored evidence list back into the proto.
func EvidenceFromJSON(raw json.RawMessage) []*pb.Evidence {
	var items []evidenceJSON
	if len(raw) == 0 || json.Unmarshal(raw, &items) != nil {
		return nil
	}
	out := make([]*pb.Evidence, 0, len(items))
	for _, it := range items {
		e := &pb.Evidence{SourceId: it.SourceID, Excerpt: it.Excerpt, Label: it.Label, Locator: locatorFromJSON(it.Locator)}
		if len(it.Values) > 0 {
			if st, err := structpb.NewStruct(it.Values); err == nil {
				e.Values = st
			}
		}
		out = append(out, e)
	}
	return out
}

func num(m map[string]any, key string) float64 {
	switch v := m[key].(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case int64:
		return float64(v)
	}
	return 0
}

func str(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}

func locatorFromJSON(m map[string]any) *pb.Locator {
	switch str(m, "type") {
	case "page_box":
		box, _ := m["box"].(map[string]any)
		return &pb.Locator{Locator: &pb.Locator_PageBox{PageBox: &pb.PageBoxLocator{Page: uint32(num(m, "page")), X: num(box, "x"), Y: num(box, "y"), W: num(box, "w"), H: num(box, "h")}}} //nolint:gosec // small
	case "byte_range":
		return &pb.Locator{Locator: &pb.Locator_ByteRange{ByteRange: &pb.ByteRangeLocator{Start: uint64(num(m, "start")), End: uint64(num(m, "end")), Encoding: str(m, "encoding")}}} //nolint:gosec // non-negative
	case "time_range":
		return &pb.Locator{Locator: &pb.Locator_TimeRange{TimeRange: &pb.TimeRangeLocator{StartMs: uint64(num(m, "start_ms")), EndMs: uint64(num(m, "end_ms")), Frame: uint32(num(m, "frame"))}}} //nolint:gosec // non-negative
	default:
		return &pb.Locator{Locator: &pb.Locator_Row{Row: &pb.RowLocator{Sheet: str(m, "sheet"), Row: uint32(num(m, "row")), RowEnd: uint32(num(m, "row_end")), Column: str(m, "column")}}} //nolint:gosec // small
	}
}
