package harnesstest

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"

	pb "github.com/mmtmn/docmatch/services/api/internal/harness/pb"
	"github.com/mmtmn/docmatch/services/api/internal/runs/apix"
)

// Options shape the replay.
type Options struct {
	// PauseAfterFindings emits needs_approval after this many findings on a
	// run that is not a resume (0 disables). The budget of the request
	// (RunOptions.credit_budget) also pauses the run when exceeded.
	PauseAfterFindings int
	// FailAfterFindings emits failed with FailCode after this many findings.
	FailAfterFindings int
	FailCode          string
	// CreditsPerStage is the spend reported by each token_usage event (default 2).
	CreditsPerStage uint64
	// TokensPerSource sizes the llm_tokens item of estimates (default 20000).
	TokensPerSource float64
	// Delay between events (0 in tests).
	Delay time.Duration
	// AdditionalCredits is the expected cost quoted on needs_approval (default 20).
	// The API prices the quote from its token quantity, which is
	// AdditionalCredits × tokensPerAdditionalCredit (20 → 40000 tokens).
	AdditionalCredits uint64
}

// Server implements pb.HarnessServer over a Script.
type Server struct {
	pb.UnimplementedHarnessServer
	Script *Script
	Opts   Options

	mu          sync.Mutex
	checkpoints map[string]checkpoint
	runs        []*pb.RunRequest
	proposals   []*pb.ProposeRulesRequest
	estimates   int
	started     time.Time
}

type checkpoint struct {
	findingsEmitted int
	lastSeq         uint64
	creditsSpent    uint64
	matchesDone     bool
}

// NewServer builds a fake over script.
// tokensPerAdditionalCredit converts AdditionalCredits into the LLM-token
// quantity of the needs_approval quote (the API reprices quantities itself).
const tokensPerAdditionalCredit = 2000

func NewServer(script *Script, opts Options) *Server {
	if opts.CreditsPerStage == 0 {
		opts.CreditsPerStage = 2
	}
	if opts.TokensPerSource == 0 {
		opts.TokensPerSource = 20000
	}
	if opts.AdditionalCredits == 0 {
		opts.AdditionalCredits = 20
	}
	if opts.FailCode == "" {
		opts.FailCode = "model_unavailable"
	}
	return &Server{Script: script, Opts: opts, checkpoints: map[string]checkpoint{}, started: time.Now()}
}

// Runs returns the Run requests received so far (tests).
func (s *Server) Runs() []*pb.RunRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*pb.RunRequest(nil), s.runs...)
}

// Proposals returns the ProposeRules requests received so far (tests).
func (s *Server) Proposals() []*pb.ProposeRulesRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*pb.ProposeRulesRequest(nil), s.proposals...)
}

// Estimates counts Estimate calls (tests).
func (s *Server) Estimates() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.estimates
}

// Estimate implements pb.HarnessServer: a breakdown proportional to the sources.
func (s *Server) Estimate(_ context.Context, req *pb.EstimateRequest) (*pb.Quote, error) {
	s.mu.Lock()
	s.estimates++
	s.mu.Unlock()
	n := float64(len(req.GetSources()))
	if n == 0 {
		n = 1
	}
	items := []*pb.EstimateItem{
		{Item: pb.EstimateItemKind_ESTIMATE_ITEM_KIND_DOCUMENTS, Quantity: n, Unit: pb.EstimateUnit_ESTIMATE_UNIT_FILES},
		{Item: pb.EstimateItemKind_ESTIMATE_ITEM_KIND_SCAN_FILES, Quantity: n, Unit: pb.EstimateUnit_ESTIMATE_UNIT_FILES},
		{Item: pb.EstimateItemKind_ESTIMATE_ITEM_KIND_LLM_TOKENS, Quantity: n * s.Opts.TokensPerSource, Unit: pb.EstimateUnit_ESTIMATE_UNIT_TOKENS, Note: "resíduo após a cascata determinística"},
		{Item: pb.EstimateItemKind_ESTIMATE_ITEM_KIND_EMBEDDINGS, Quantity: n * 2000, Unit: pb.EstimateUnit_ESTIMATE_UNIT_TOKENS},
	}
	for _, src := range req.GetSources() {
		if src.GetKind() == pb.SourceKind_SOURCE_KIND_MEDIA && src.GetDurationMs() > 0 {
			items = append(items, &pb.EstimateItem{Item: pb.EstimateItemKind_ESTIMATE_ITEM_KIND_AUDIO_MINUTES, Quantity: float64(src.GetDurationMs()) / 60000, Unit: pb.EstimateUnit_ESTIMATE_UNIT_MINUTES})
		}
	}
	return &pb.Quote{
		ExpectedCredits: 50, CapCredits: 63,
		Breakdown:        &pb.EstimateBreakdown{Lane: req.GetOptions().GetLane(), LaneMultiplier: 1, Items: items, SubtotalCredits: 50, MinimumCredits: 10, TotalCredits: 50, PricingVersion: "fake"},
		RequiresApproval: false, CreatedAt: timestamppb.Now(),
	}, nil
}

// Health implements pb.HarnessServer.
func (s *Server) Health(context.Context, *pb.HealthRequest) (*pb.HealthResponse, error) {
	return &pb.HealthResponse{Status: pb.ServingStatus_SERVING_STATUS_SERVING, Version: "fake", LlmMode: pb.LlmMode_LLM_MODE_OFF, LlmReachable: true, StartedAt: timestamppb.New(s.started)}, nil
}

// ProposeRules implements pb.HarnessServer: one candidate per confirmed finding.
func (s *Server) ProposeRules(_ context.Context, req *pb.ProposeRulesRequest) (*pb.ProposeRulesResponse, error) {
	s.mu.Lock()
	s.proposals = append(s.proposals, req)
	s.mu.Unlock()
	maxRules := int(req.GetMaxRules())
	if maxRules <= 0 {
		maxRules = 3
	}
	var out []*pb.Rule
	for i, f := range req.GetConfirmedFindings() {
		if i >= maxRules {
			break
		}
		text := "SELECT fact_id FROM fact WHERE predicate = '" + strings.ReplaceAll(f.GetCode(), "'", "") + "'"
		out = append(out, &pb.Rule{
			Title: "Regra aprendida: " + f.GetTitle(), Body: "Quando " + f.GetCode() + " acontecer, tratar como " + f.GetTitle() + ".",
			Version: 1, Status: pb.RuleStatus_RULE_STATUS_CANDIDATE,
			Compiled:   &pb.CompiledRule{Language: "sql", Text: text, Hash: fmt.Sprintf("%064x", i+1), CompiledBy: "fake", CompiledAt: timestamppb.Now()},
			Stats:      &pb.RuleStats{RunsEvaluated: 1, TruePositives: 1, ExplainedAmount: f.GetAmountAtRisk(), Currency: "BRL"},
			Provenance: &pb.RuleProvenance{Origin: pb.RuleOrigin_RULE_ORIGIN_MODEL, Model: "fake/discovery", RunId: req.GetRunId(), ProposedAt: timestamppb.Now()},
		})
	}
	return &pb.ProposeRulesResponse{Rules: out, CreditsSpent: uint64(len(out))}, nil //nolint:gosec // small
}

// Replay implements pb.HarnessServer as a fresh Run.
func (s *Server) Replay(req *pb.ReplayRequest, stream grpc.ServerStreamingServer[pb.RunEvent]) error {
	return s.Run(&pb.RunRequest{RunId: req.GetRunId(), TenantId: req.GetTenantId(), Sources: req.GetSources(), Options: req.GetOptions(), Rules: req.GetRules()}, stream)
}

// emitter streams events with increasing seq.
type emitter struct {
	stream grpc.ServerStreamingServer[pb.RunEvent]
	runID  string
	seq    uint64
	delay  time.Duration
}

func (e *emitter) send(ev *pb.RunEvent) error {
	e.seq++
	ev.RunId, ev.Seq, ev.At = e.runID, e.seq, timestamppb.Now()
	if e.delay > 0 {
		time.Sleep(e.delay)
	}
	return e.stream.Send(ev)
}

// Run implements pb.HarnessServer: replays the twin, pausing on budget or
// on PauseAfterFindings and resuming from the checkpoint when asked.
func (s *Server) Run(req *pb.RunRequest, stream grpc.ServerStreamingServer[pb.RunEvent]) error {
	s.mu.Lock()
	s.runs = append(s.runs, req)
	cp := s.checkpoints[req.GetRunId()]
	s.mu.Unlock()
	if !req.GetResume() {
		cp = checkpoint{}
	}
	e := &emitter{stream: stream, runID: req.GetRunId(), seq: cp.lastSeq, delay: s.Opts.Delay}
	budget := req.GetOptions().GetCreditBudget()
	if q := req.GetQuote(); q != nil && q.GetCapCredits() > 0 {
		budget = q.GetCapCredits()
	}
	spent := cp.creditsSpent
	sources := s.sourceIDs(req.GetSources())
	save := func() {
		s.mu.Lock()
		s.checkpoints[req.GetRunId()] = checkpoint{findingsEmitted: cp.findingsEmitted, lastSeq: e.seq, creditsSpent: spent, matchesDone: cp.matchesDone}
		s.mu.Unlock()
	}
	usage := func(role, model string) error {
		spent += s.Opts.CreditsPerStage
		return e.send(&pb.RunEvent{Event: &pb.RunEvent_TokenUsage{TokenUsage: &pb.TokenUsage{
			Role: role, Model: model, PromptTokens: 1200, CompletionTokens: 300, CostMicroUsd: 1500, Credits: s.Opts.CreditsPerStage, CreditsSpentTotal: spent,
		}}})
	}
	stage := func(st pb.Stage, msg string, total uint64) error {
		if err := e.send(&pb.RunEvent{Event: &pb.RunEvent_StageStarted{StageStarted: &pb.StageStarted{Stage: st, Message: msg, TotalSteps: total}}}); err != nil {
			return err
		}
		return e.send(&pb.RunEvent{Event: &pb.RunEvent_Progress{Progress: &pb.Progress{Stage: st, Done: total, Total: total, Message: msg, CreditsSpent: spent}}})
	}
	pause := func(reason pb.ApprovalReason, msg string) error {
		save()
		return e.send(&pb.RunEvent{Event: &pb.RunEvent_NeedsApproval{NeedsApproval: &pb.NeedsApproval{
			Quote: &pb.Quote{
				RunId: req.GetRunId(), ExpectedCredits: s.Opts.AdditionalCredits, CapCredits: s.Opts.AdditionalCredits + s.Opts.AdditionalCredits/4,
				Breakdown:        &pb.EstimateBreakdown{Lane: req.GetOptions().GetLane(), LaneMultiplier: 1, Items: []*pb.EstimateItem{{Item: pb.EstimateItemKind_ESTIMATE_ITEM_KIND_LLM_TOKENS, Quantity: float64(s.Opts.AdditionalCredits) * tokensPerAdditionalCredit, Unit: pb.EstimateUnit_ESTIMATE_UNIT_TOKENS}}, SubtotalCredits: s.Opts.AdditionalCredits, MinimumCredits: 10, TotalCredits: s.Opts.AdditionalCredits, PricingVersion: "fake"},
				RequiresApproval: true, ApprovalReasons: []pb.ApprovalReason{reason}, CreatedAt: timestamppb.Now(),
			},
			Reasons: []pb.ApprovalReason{reason}, Message: msg, CreditsSpentSoFar: spent,
		}}})
	}

	if !req.GetResume() {
		if err := stage(pb.Stage_STAGE_SCAN, "Verificando arquivos", uint64(len(req.GetSources()))); err != nil { //nolint:gosec // small
			return err
		}
		if err := stage(pb.Stage_STAGE_INGEST, "Lendo os extratos", uint64(len(req.GetSources()))); err != nil { //nolint:gosec // small
			return err
		}
		if err := usage("extraction", "fake/extraction"); err != nil {
			return err
		}
		if err := stage(pb.Stage_STAGE_NORMALIZE, "Normalizando valores e datas", 1); err != nil {
			return err
		}
	}
	if !cp.matchesDone {
		if err := stage(pb.Stage_STAGE_MATCH, "Casando lançamentos", uint64(len(s.Script.BankLines))); err != nil { //nolint:gosec // small
			return err
		}
		for _, line := range s.Script.BankLines {
			if len(line.Matched) == 0 {
				continue
			}
			if err := e.send(&pb.RunEvent{Event: &pb.RunEvent_Match{Match: s.match(req.GetRunId(), line, sources)}}); err != nil {
				return err
			}
		}
		cp.matchesDone = true
		if err := stage(pb.Stage_STAGE_DISCOVER, "Procurando regras no histórico", 1); err != nil {
			return err
		}
		if err := usage("discovery", "fake/discovery"); err != nil {
			return err
		}
	}
	if err := stage(pb.Stage_STAGE_ADJUDICATE, "Analisando o resíduo", uint64(len(s.Script.Findings))); err != nil { //nolint:gosec // small
		return err
	}
	for i := cp.findingsEmitted; i < len(s.Script.Findings); i++ {
		if s.Opts.FailAfterFindings > 0 && i >= s.Opts.FailAfterFindings {
			save()
			return e.send(&pb.RunEvent{Event: &pb.RunEvent_Failed{Failed: &pb.Failed{Code: s.Opts.FailCode, Message: "O modelo de linguagem está indisponível. Tente de novo em instantes.", Retryable: true, CreditsSpent: spent}}})
		}
		if s.Opts.PauseAfterFindings > 0 && !req.GetResume() && i >= s.Opts.PauseAfterFindings {
			return pause(pb.ApprovalReason_APPROVAL_REASON_NEW_TOOLS, "Esta execução precisa de ferramentas novas; aprove o orçamento adicional para continuar.")
		}
		if budget > 0 && spent+s.Opts.CreditsPerStage > budget {
			return pause(pb.ApprovalReason_APPROVAL_REASON_OVER_DEFAULT_BUDGET, "Esta execução vai passar do orçamento; aprove o orçamento adicional para continuar.")
		}
		if i%4 == 0 {
			if err := usage("adjudicator", "fake/adjudicator"); err != nil {
				return err
			}
		}
		if err := e.send(&pb.RunEvent{Event: &pb.RunEvent_Finding{Finding: s.finding(req.GetRunId(), s.Script.Findings[i], sources)}}); err != nil {
			return err
		}
		cp.findingsEmitted = i + 1
	}
	if err := stage(pb.Stage_STAGE_VERIFY, "Reconferindo cada achado", uint64(len(s.Script.Findings))); err != nil { //nolint:gosec // small
		return err
	}
	if err := usage("verifier", "fake/verifier"); err != nil {
		return err
	}
	if err := stage(pb.Stage_STAGE_REPORT, "Montando o relatório", 1); err != nil {
		return err
	}
	save()
	return e.send(&pb.RunEvent{Event: &pb.RunEvent_Completed{Completed: &pb.Completed{Summary: s.summary(), CreditsSpent: spent, CreditsRefunded: 0}}})
}

// sourceIDs maps the twin's logical sources to the request's source ids,
// by role when the roles match the twin, else by position.
func (s *Server) sourceIDs(sources []*pb.Source) map[string]string {
	order := []string{"protheus", "omie", "bank", "ap_inbox"}
	roles := map[string]string{"protheus": "protheus", "omie": "omie", "bank": "bank", "bank_statement": "bank", "ap_inbox": "ap_inbox", "email": "ap_inbox", "ledger": "protheus"}
	out := map[string]string{}
	for i, src := range sources {
		if name, ok := roles[src.GetRole()]; ok && out[name] == "" {
			out[name] = src.GetId()
			continue
		}
		if i < len(order) && out[order[i]] == "" {
			out[order[i]] = src.GetId()
		}
	}
	if len(sources) > 0 {
		for _, name := range order {
			if out[name] == "" {
				out[name] = sources[0].GetId()
			}
		}
	}
	return out
}

func newID(prefix string) string {
	id, err := uuid.NewV7()
	if err != nil {
		id = uuid.New()
	}
	return apix.Encode(prefix, id)
}

func (s *Server) evidence(ref string, sources map[string]string) *pb.Evidence {
	kind, id, _ := strings.Cut(ref, ":")
	row := uint32(1)
	switch kind {
	case "bank":
		var n int
		_, _ = fmt.Sscanf(id, "%d", &n)
		row = uint32(n + 1) //nolint:gosec // small
	case "title":
		for i, t := range s.Script.Titles {
			if t.ID == id {
				row = uint32(i + 2) //nolint:gosec // small
			}
		}
	case "email":
		var n int
		_, _ = fmt.Sscanf(id, "%d", &n)
		row = uint32(n) //nolint:gosec // small
	}
	return &pb.Evidence{
		SourceId: sources[s.Script.sourceOfRef(ref)], Locator: &pb.Locator{Locator: &pb.Locator_Row{Row: &pb.RowLocator{Row: row}}},
		Excerpt: ref, Label: kind,
	}
}

var kinds = map[string]pb.FindingKind{
	"one_payment_many_titles": pb.FindingKind_FINDING_KIND_NOTE, "early_payment": pb.FindingKind_FINDING_KIND_NOTE,
	"partial_payment": pb.FindingKind_FINDING_KIND_PARTIAL, "duplicate_title_across_erps": pb.FindingKind_FINDING_KIND_DUPLICATE,
	"customer_master_duplicate": pb.FindingKind_FINDING_KIND_DUPLICATE, "unapplied_cash": pb.FindingKind_FINDING_KIND_MISSING,
	"unbooked_bank_fees": pb.FindingKind_FINDING_KIND_MISSING, "overdue_open": pb.FindingKind_FINDING_KIND_MISSING,
	"ap_invoice_already_paid": pb.FindingKind_FINDING_KIND_CONTRADICTION, "ap_sender_or_bank_details_changed": pb.FindingKind_FINDING_KIND_CONTRADICTION,
	"ap_boleto_invalid": pb.FindingKind_FINDING_KIND_ANOMALY, "withholding_suspected": pb.FindingKind_FINDING_KIND_MISMATCH,
	"overpayment_fine_received": pb.FindingKind_FINDING_KIND_MISMATCH, "ap_three_way_match_failed": pb.FindingKind_FINDING_KIND_MISMATCH,
}

var severities = map[string]pb.Severity{
	"info": pb.Severity_SEVERITY_INFO, "low": pb.Severity_SEVERITY_LOW, "medium": pb.Severity_SEVERITY_MEDIUM,
	"high": pb.Severity_SEVERITY_HIGH, "critical": pb.Severity_SEVERITY_CRITICAL,
}

func (s *Server) finding(runID string, f ScriptFinding, sources map[string]string) *pb.Finding {
	kind, ok := kinds[f.Kind]
	if !ok {
		kind = pb.FindingKind_FINDING_KIND_ANOMALY
		if f.Severity == "info" {
			kind = pb.FindingKind_FINDING_KIND_NOTE
		}
	}
	ev := make([]*pb.Evidence, 0, len(f.Evidence))
	seen := map[string]bool{}
	var srcIDs []string
	for _, ref := range f.Evidence {
		e := s.evidence(ref, sources)
		ev = append(ev, e)
		if !seen[e.SourceId] {
			seen[e.SourceId] = true
			srcIDs = append(srcIDs, e.SourceId)
		}
	}
	return &pb.Finding{
		Id: newID(apix.PrefixFinding), RunId: runID, Code: "twin." + strings.ToLower(f.Code), Kind: kind, Severity: severities[f.Severity],
		AmountAtRisk: Centavos(f.Amount), Currency: "BRL", Title: f.Title, Explanation: f.Title + " (" + f.Kind + ").",
		Confidence: 0.92, ConfidenceLabel: pb.ConfidenceLabel_CONFIDENCE_LABEL_CONFIRMED, Evidence: ev, Status: pb.FindingStatus_FINDING_STATUS_OPEN,
		SourceIds: srcIDs, CreatedAt: timestamppb.Now(),
	}
}

func (s *Server) match(runID string, line ScriptBankLine, sources map[string]string) *pb.Match {
	kind := pb.MatchKind_MATCH_KIND_EXACT
	switch {
	case strings.Contains(line.Rule, "subset_sum"):
		kind = pb.MatchKind_MATCH_KIND_SUM
	case strings.Contains(line.Rule, "fuzzy"):
		kind = pb.MatchKind_MATCH_KIND_FUZZY
	case strings.Contains(line.Rule, "amount_only"):
		kind = pb.MatchKind_MATCH_KIND_MODEL
	case strings.Contains(line.Rule, "partial"), strings.Contains(line.Rule, "withholding"), strings.Contains(line.Rule, "fine"):
		kind = pb.MatchKind_MATCH_KIND_TOLERANCE
	case strings.Contains(line.Rule, "name"), strings.Contains(line.Rule, "document"), strings.Contains(line.Rule, "tax_id"):
		kind = pb.MatchKind_MATCH_KIND_RULE
	}
	right := make([]*pb.Evidence, 0, len(line.Matched))
	for _, m := range line.Matched {
		ref := "title:" + m
		if strings.HasPrefix(m, "AP-") {
			ref = "email:1"
		}
		right = append(right, s.evidence(ref, sources))
	}
	confidence := 0.95
	if line.Proposed {
		confidence = 0.6
	}
	return &pb.Match{
		Id: newID(apix.PrefixMatch), RunId: runID, Kind: kind, Left: []*pb.Evidence{s.evidence(fmt.Sprintf("bank:%d", line.N), sources)}, Right: right,
		Confidence: confidence, Amount: Centavos(line.Amount), Currency: "BRL", Explanation: line.Rule, CreatedAt: timestamppb.Now(),
	}
}

func (s *Server) summary() *pb.RunSummary {
	bySev := map[string]uint32{}
	for _, f := range s.Script.Findings {
		bySev[f.Severity]++
	}
	total := len(s.Script.BankLines)
	matched := s.Script.MatchedLines()
	return &pb.RunSummary{
		FindingsTotal: uint32(len(s.Script.Findings)), FindingsBySeverity: bySev, AmountAtRisk: s.Script.TotalAtRisk(), Currency: "BRL", //nolint:gosec // small
		MatchesTotal: uint32(matched), LinesTotal: uint32(total), LinesMatched: uint32(matched), MatchedRatio: float64(matched) / float64(max(total, 1)), //nolint:gosec // small
	}
}

// Serve starts a gRPC server on the Unix socket at path.
func Serve(path string, srv *Server) (stop func(), err error) {
	_ = os.Remove(path)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, err
	}
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "unix", path)
	if err != nil {
		return nil, fmt.Errorf("harnesstest: escutar em %s: %w", path, err)
	}
	gs := grpc.NewServer()
	pb.RegisterHarnessServer(gs, srv)
	go func() { _ = gs.Serve(ln) }()
	return func() {
		gs.Stop()
		_ = os.Remove(path)
	}, nil
}

// SocketPath returns a short socket path under a fresh temp directory.
// Unix socket paths are limited to ~104 bytes, so when the temp dir is a
// long path (sandboxed TMPDIR) it falls back to the system default.
func SocketPath() (string, func(), error) {
	dir, err := os.MkdirTemp("", "dmh")
	if err != nil {
		return "", nil, err
	}
	if len(dir) > 80 {
		_ = os.RemoveAll(dir)
		dir, err = os.MkdirTemp("/tmp", "dmh")
		if err != nil {
			return "", nil, err
		}
	}
	return filepath.Join(dir, "h.sock"), func() { _ = os.RemoveAll(dir) }, nil
}
