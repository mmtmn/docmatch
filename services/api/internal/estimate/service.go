package estimate

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	contracts "github.com/mmtmn/docmatch/packages/contracts/gen"
	"github.com/mmtmn/docmatch/services/api/internal/audit"
	"github.com/mmtmn/docmatch/services/api/internal/estimate/pricing"
	"github.com/mmtmn/docmatch/services/api/internal/estimate/throughput"
	"github.com/mmtmn/docmatch/services/api/internal/harness"
	pb "github.com/mmtmn/docmatch/services/api/internal/harness/pb"
	"github.com/mmtmn/docmatch/services/api/internal/httpx"
	"github.com/mmtmn/docmatch/services/api/internal/limits"
	"github.com/mmtmn/docmatch/services/api/internal/runs/apix"
	"github.com/mmtmn/docmatch/services/api/internal/uploads"
)

// ratesTTL bounds how long model rates are cached in memory.
const ratesTTL = 5 * time.Minute

// inFlightFreshness bounds the runs counted ahead in the queue: a queued or
// running run untouched for longer than the run deadline (2 h) plus the job
// pad is stuck, not waiting (a running run is touched by every event).
const inFlightFreshness = 3 * time.Hour

// overrunWindow and overrunLimit select the runs the p99 is computed on.
const (
	overrunWindow = 30 * 24 * time.Hour
	overrunLimit  = 2000
)

// Sources resolves upload ids to ready uploads (uploads.Service).
type Sources interface {
	ListReady(ctx context.Context, orgID uuid.UUID, ids []uuid.UUID) ([]uploads.Upload, error)
}

// Recorder appends audit entries (audit.Service).
type Recorder interface {
	Record(ctx context.Context, orgID *uuid.UUID, actor audit.Actor, action string, target audit.Target, meta any) (audit.Entry, error)
}

// Deps are the collaborators of the service.
type Deps struct {
	Store   Store
	Harness harness.Client
	Pricing *pricing.Pricing
	// Throughput times quotes (config/throughput.yaml); nil leaves quotes
	// without expected_seconds.
	Throughput *throughput.Config
	Signer     *Signer
	Sources    Sources
	Limits     *limits.Limits
	Audit      Recorder
	Logger     *slog.Logger
	Now        func() time.Time
	// Placer routes heavy runs to the pool of on-demand workers; nil keeps
	// every run on the main box.
	Placer Placer
}

// Service builds, signs and verifies quotes.
type Service struct {
	d Deps

	mu      sync.Mutex
	rates   ModelRates
	ratesAt time.Time
	tp      Throughput
	tpAt    time.Time
}

// New validates the dependencies.
func New(d Deps) (*Service, error) {
	if d.Store == nil || d.Harness == nil || d.Pricing == nil || d.Signer == nil || d.Sources == nil {
		return nil, errors.New("estimate: store, harness, pricing, signer e sources são obrigatórios")
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
	return &Service{d: d}, nil
}

// Pricing exposes the parsed table (runs and uploads read plan limits).
func (s *Service) Pricing() *pricing.Pricing { return s.d.Pricing }

// Throughput exposes the timing configuration (nil when timing is off).
func (s *Service) Throughput() *throughput.Config { return s.d.Throughput }

var (
	roleRe   = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)
	llmModes = map[string]pb.LlmMode{"": pb.LlmMode_LLM_MODE_UNSPECIFIED, "openrouter": pb.LlmMode_LLM_MODE_OPENROUTER, "local": pb.LlmMode_LLM_MODE_LOCAL, "off": pb.LlmMode_LLM_MODE_OFF}
	lanes    = map[string]pb.Lane{LaneApp: pb.Lane_LANE_APP, LaneAPIBatch: pb.Lane_LANE_API_BATCH, LaneAPIStandard: pb.Lane_LANE_API_STANDARD, LaneAPIPriority: pb.Lane_LANE_API_PRIORITY}
)

// Validated is a request after Validate: the plan, the lane and the decoded ids.
type Validated struct {
	Plan       pricing.Plan
	Lane       string
	DocIDs     []uuid.UUID
	MediaIDs   []uuid.UUID
	WorkflowID *uuid.UUID
	RuleIDs    []uuid.UUID
	RulesText  string
}

// Validate applies the contract and the plan limits to a request.
func (s *Service) Validate(p apix.Principal, req Request) (Validated, error) {
	plan, ok := s.d.Pricing.Plan(p.Plan)
	if !ok {
		return Validated{}, apix.Ef(contracts.ErrPlanUnknown, "plano %q", p.Plan)
	}
	v := Validated{Plan: plan, Lane: req.Lane()}
	var fields []apix.FieldError
	if len(req.Sources) == 0 {
		fields = append(fields, apix.FieldError{Path: "/sources", Code: "min_items"})
	}
	maxDocs := min(plan.DocumentFilesPerRun, s.d.Limits.IDs.FilesPerRunHardMax)
	if len(req.Sources) > maxDocs {
		return Validated{}, apix.Ef(contracts.ErrTooManyFiles, "%d documentos (máximo %d)", len(req.Sources), maxDocs).With(map[string]any{"max": maxDocs})
	}
	maxMedia := min(plan.MediaFilesPerRun, s.d.Limits.IDs.MediaFilesPerRunHardMax)
	if len(req.MediaSources) > maxMedia {
		return Validated{}, apix.Ef(contracts.ErrTooManyFiles, "%d mídias (máximo %d)", len(req.MediaSources), maxMedia).With(map[string]any{"max": maxMedia})
	}
	decodeRefs := func(path string, refs []SourceRef) []uuid.UUID {
		seen := map[uuid.UUID]bool{}
		ids := make([]uuid.UUID, 0, len(refs))
		for i, r := range refs {
			id, err := apix.Decode(apix.PrefixUpload, r.UploadID)
			if err != nil || seen[id] {
				fields = append(fields, apix.FieldError{Path: fmt.Sprintf("%s/%d/upload_id", path, i), Code: "invalid"})
				continue
			}
			seen[id] = true
			ids = append(ids, id)
			if r.Role != "" && !roleRe.MatchString(r.Role) {
				fields = append(fields, apix.FieldError{Path: fmt.Sprintf("%s/%d/role", path, i), Code: "invalid"})
			}
			if h := r.Hints; h != nil {
				if h.HeaderRow < 0 || (h.DecimalSeparator != "" && h.DecimalSeparator != "," && h.DecimalSeparator != ".") {
					fields = append(fields, apix.FieldError{Path: fmt.Sprintf("%s/%d/hints", path, i), Code: "invalid"})
				}
			}
		}
		return ids
	}
	v.DocIDs = decodeRefs("/sources", req.Sources)
	v.MediaIDs = decodeRefs("/media_sources", req.MediaSources)
	if req.WorkflowID != "" {
		id, err := apix.Decode(apix.PrefixWorkflow, req.WorkflowID)
		if err != nil {
			fields = append(fields, apix.FieldError{Path: "/workflow_id", Code: "invalid"})
		} else {
			v.WorkflowID = &id
		}
	}
	if req.RulesText != "" {
		text, err := httpx.ValidateText(req.RulesText, s.d.Limits.Text.RulesText, false)
		if errors.Is(err, httpx.ErrTooLong) {
			return Validated{}, apix.E(contracts.ErrTextTooLong, err).With(map[string]any{"path": "/rules_text", "max": s.d.Limits.Text.RulesText})
		}
		if err != nil {
			fields = append(fields, apix.FieldError{Path: "/rules_text", Code: "control_characters"})
		}
		v.RulesText = text
	}
	if o := req.Options; o != nil {
		if _, ok := llmModes[o.LlmMode]; !ok {
			fields = append(fields, apix.FieldError{Path: "/options/llm_mode", Code: "invalid"})
		}
		if o.Lane != "" {
			if _, ok := lanes[o.Lane]; !ok {
				fields = append(fields, apix.FieldError{Path: "/options/lane", Code: "invalid"})
			}
		}
		seen := map[uuid.UUID]bool{}
		for i, rid := range o.RuleIDs {
			id, err := apix.Decode(apix.PrefixRule, rid)
			if err != nil || seen[id] {
				fields = append(fields, apix.FieldError{Path: fmt.Sprintf("/options/rule_ids/%d", i), Code: "invalid"})
				continue
			}
			seen[id] = true
			v.RuleIDs = append(v.RuleIDs, id)
		}
	}
	if len(fields) > 0 {
		return Validated{}, apix.Validation(fields...)
	}
	if err := s.checkLane(p, plan, v.Lane); err != nil {
		return Validated{}, err
	}
	if req.Options != nil && req.Options.Escalation && !plan.EscalationModel {
		return Validated{}, apix.Ef(contracts.ErrPlanLimitReached, "escalonamento indisponível no plano %s", p.Plan)
	}
	return v, nil
}

// checkLane enforces the lane policy: sessions run in the app lane; API
// keys use only the lanes granted to the key and allowed by the plan.
func (s *Service) checkLane(p apix.Principal, plan pricing.Plan, lane string) error {
	if !p.IsAPIKey() {
		if lane != LaneApp {
			return apix.Ef(contracts.ErrLaneNotAllowed, "sessão só usa a faixa app")
		}
		return nil
	}
	if lane == LaneApp || !contains(p.Lanes, lane) || !contains(plan.APILanes, lane) {
		return apix.Ef(contracts.ErrLaneNotAllowed, "faixa %s não permitida", lane)
	}
	return nil
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

// Loaded are the resolved sources of a request.
type Loaded struct {
	Docs   []uploads.Upload
	Media  []uploads.Upload
	Hashes map[string]string
}

// LoadSources resolves the uploads and applies the per-run size limits.
func (s *Service) LoadSources(ctx context.Context, orgID uuid.UUID, v Validated) (Loaded, error) {
	docs, err := s.d.Sources.ListReady(ctx, orgID, v.DocIDs)
	if err != nil {
		return Loaded{}, err
	}
	media, err := s.d.Sources.ListReady(ctx, orgID, v.MediaIDs)
	if err != nil {
		return Loaded{}, err
	}
	hashes := map[string]string{}
	var docBytes, mediaBytes, mediaMs int64
	for _, u := range docs {
		if u.Kind != uploads.KindDocument {
			return Loaded{}, apix.Validation(apix.FieldError{Path: "/sources", Code: "not_a_document"})
		}
		hashes[u.PublicID()] = hex.EncodeToString(u.SHA256)
		docBytes += u.SizeBytes
	}
	for _, u := range media {
		if u.Kind != uploads.KindMedia {
			return Loaded{}, apix.Validation(apix.FieldError{Path: "/media_sources", Code: "not_media"})
		}
		hashes[u.PublicID()] = hex.EncodeToString(u.SHA256)
		mediaBytes += u.SizeBytes
		if u.DurationMs != nil {
			mediaMs += *u.DurationMs
		}
	}
	maxDocBytes := min(v.Plan.DocumentRunMB<<20, s.d.Limits.Files.DocumentRunMax())
	if docBytes > maxDocBytes {
		return Loaded{}, apix.Ef(contracts.ErrRunTooLarge, "%d bytes de documentos (máximo %d)", docBytes, maxDocBytes).With(map[string]any{"max_bytes": maxDocBytes})
	}
	if maxMediaBytes := v.Plan.MediaRunGB << 30; mediaBytes > maxMediaBytes {
		return Loaded{}, apix.Ef(contracts.ErrRunTooLarge, "%d bytes de mídia (máximo %d)", mediaBytes, maxMediaBytes).With(map[string]any{"max_bytes": maxMediaBytes})
	}
	if maxMs := v.Plan.MediaRunHours * int64(time.Hour/time.Millisecond); mediaMs > maxMs {
		return Loaded{}, apix.Ef(contracts.ErrMediaTooLong, "%d ms de mídia (máximo %d)", mediaMs, maxMs).With(map[string]any{"max_hours": v.Plan.MediaRunHours})
	}
	return Loaded{Docs: docs, Media: media, Hashes: hashes}, nil
}

// SourceKind classifies an upload for the harness.
func SourceKind(u uploads.Upload) pb.SourceKind {
	if u.Kind == uploads.KindMedia {
		return pb.SourceKind_SOURCE_KIND_MEDIA
	}
	m := strings.ToLower(u.Mime())
	switch {
	case strings.Contains(m, "csv"), strings.Contains(m, "tab-separated"), strings.Contains(m, "spreadsheet"), strings.Contains(m, "ms-excel"):
		return pb.SourceKind_SOURCE_KIND_TABLE
	case strings.Contains(m, "rfc822"), strings.Contains(m, "outlook"), strings.Contains(m, "mbox"):
		return pb.SourceKind_SOURCE_KIND_EMAIL
	case strings.Contains(m, "zip"), strings.Contains(m, "7z-compressed"):
		return pb.SourceKind_SOURCE_KIND_ARCHIVE
	}
	return pb.SourceKind_SOURCE_KIND_DOCUMENT
}

// SourceURI is the object the harness reads: the canonical extract written
// by the scanner when there is one, else the original.
func SourceURI(u uploads.Upload, uri func(key string) string) string {
	if len(u.ScanResult) > 0 {
		var v uploads.Verdict
		if json.Unmarshal(u.ScanResult, &v) == nil {
			for _, r := range v.Rebuilt {
				if r.Kind == "extract" && r.Bucket != "" && r.Key != "" {
					return "s3://" + r.Bucket + "/" + r.Key
				}
			}
		}
	}
	return uri(u.StorageKey)
}

// BuildSources renders the harness sources in request order.
func BuildSources(refs []SourceRef, ups []uploads.Upload, uri func(u uploads.Upload) string) []*pb.Source {
	byID := map[string]uploads.Upload{}
	for _, u := range ups {
		byID[u.PublicID()] = u
	}
	out := make([]*pb.Source, 0, len(refs))
	for _, r := range refs {
		u, ok := byID[r.UploadID]
		if !ok {
			continue
		}
		src := &pb.Source{
			Id: u.PublicID(), Kind: SourceKind(u), Uri: uri(u), Sha256: hex.EncodeToString(u.SHA256), Mime: u.Mime(),
			SizeBytes: uint64(max(u.SizeBytes, 0)), FileName: u.FileName, Role: r.Role, LocatorHints: r.Hints.Map(), //nolint:gosec // size is non-negative
		}
		if src.Role == "" {
			src.Role = u.Role
		}
		if u.DurationMs != nil && *u.DurationMs > 0 {
			src.DurationMs = uint64(*u.DurationMs) //nolint:gosec // checked positive
		}
		out = append(out, src)
	}
	return out
}

// BuildOptions renders the harness run options.
func BuildOptions(p apix.Principal, req Request, v Validated, budget int64, deadline *time.Time, workflowID string) *pb.RunOptions {
	o := &pb.RunOptions{
		CreditBudget: uint64(max(budget, 0)), //nolint:gosec // non-negative
		UseRules:     req.UseRules(), TenantId: apix.Encode(apix.PrefixOrg, p.OrgID), WorkflowId: workflowID,
		Lane: lanes[v.Lane], RulesText: v.RulesText, EscalationAllowed: v.Plan.EscalationModel,
		MaxNewTools: 3, AllowCandidateTools: false,
	}
	if deadline != nil {
		o.Deadline = timestamppb.New(*deadline)
	}
	if req.Options != nil {
		o.LlmMode = llmModes[req.Options.LlmMode]
		o.WebSearch = req.Options.WebSearch
		o.RuleIds = append([]string(nil), req.Options.RuleIDs...)
		o.EscalationAllowed = req.Options.Escalation && v.Plan.EscalationModel
	}
	return o
}

// Estimate is POST /runs/estimate.
func (s *Service) Estimate(ctx context.Context, p apix.Principal, req Request, uri func(u uploads.Upload) string) (Quote, error) {
	v, err := s.Validate(p, req)
	if err != nil {
		return Quote{}, err
	}
	loaded, err := s.LoadSources(ctx, p.OrgID, v)
	if err != nil {
		return Quote{}, err
	}
	workflowID := ""
	if v.WorkflowID != nil {
		workflowID = apix.Encode(apix.PrefixWorkflow, *v.WorkflowID)
	}
	sources := append(BuildSources(req.Sources, loaded.Docs, uri), BuildSources(req.MediaSources, loaded.Media, uri)...)
	hreq := &pb.EstimateRequest{TenantId: apix.Encode(apix.PrefixOrg, p.OrgID), Sources: sources, Options: BuildOptions(p, req, v, 0, nil, workflowID)}
	pbq, err := s.d.Harness.Estimate(ctx, hreq)
	if err != nil {
		if harness.IsUnavailable(err) {
			return Quote{}, apix.E(contracts.ErrHarnessUnavailable, err)
		}
		return Quote{}, fmt.Errorf("estimate: harness: %w", err)
	}
	qt := s.Timing(ctx, s.Units(loaded, ItemsFromProto(pbq.GetBreakdown())))
	q, err := s.priceQuote(ctx, pbq, v.Lane, s.instanceMinutes(qt))
	if err != nil {
		return Quote{}, err
	}
	q.ExpectedSeconds, q.Large = qt.Seconds, qt.Large
	rawReq, _ := json.Marshal(req)
	rec, err := s.Issue(ctx, p, v.Lane, KindInitial, rawReq, Fingerprint(req, loaded.Hashes), q, nil)
	if err != nil {
		return Quote{}, err
	}
	return rec.Quote, nil
}

// PriceQuote re-prices the harness breakdown with the current table.
func (s *Service) PriceQuote(ctx context.Context, pbq *pb.Quote, lane string) (Quote, error) {
	return s.priceQuote(ctx, pbq, lane, 0)
}

// priceQuote prices the breakdown, folding instanceMinutes of the burst pool
// into the heavy work when pricing.yaml charges them (InstanceMinutes).
func (s *Service) priceQuote(ctx context.Context, pbq *pb.Quote, lane string, instanceMinutes float64) (Quote, error) {
	items := ItemsFromProto(pbq.GetBreakdown())
	bd, err := Pricer{P: s.d.Pricing}.PriceWithInstance(items, lane, s.Rates(ctx), instanceMinutes)
	if err != nil {
		return Quote{}, err
	}
	var cal *pb.Calibration
	if pbq.GetBreakdown() != nil {
		cal = pbq.GetBreakdown().GetCalibration()
	}
	ratio, calibration := s.CapRatio(ctx, cal)
	bd.Calibration = calibration
	q := Quote{
		ExpectedCredits: bd.TotalCredits, CapCredits: Cap(bd.TotalCredits, ratio), Breakdown: bd,
		RequiresApproval: pbq.GetRequiresApproval(), ApprovalReasons: ApprovalReasons(pbq.GetApprovalReasons()),
	}
	return q, nil
}

// Issue assigns id and expiry, signs and stores a priced quote.
func (s *Service) Issue(ctx context.Context, p apix.Principal, lane, kind string, rawReq json.RawMessage, fingerprint string, q Quote, runID *uuid.UUID) (Record, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return Record{}, err
	}
	now := s.d.Now().UTC()
	expires := now.Add(s.d.Pricing.QuoteTTL())
	q.ID = apix.Encode(apix.PrefixQuote, id)
	q.Fingerprint = fingerprint
	q.ExpiresAt = expires.Format(time.RFC3339)
	q.CreatedAt = now.Format(time.RFC3339)
	q.RunID = apix.EncodePtr(apix.PrefixRun, runID)
	q.Signature = s.d.Signer.Sign(q)
	if rawReq == nil {
		rawReq = json.RawMessage("{}")
	}
	rec := Record{
		ID: id, OrgID: p.OrgID, UserID: p.UserID, Lane: lane, Expected: q.ExpectedCredits, Cap: q.CapCredits,
		Request: rawReq, Signature: q.Signature, Fingerprint: fingerprint, Quote: q, RunID: runID, Kind: kind,
		Status: StatusOpen, ExpiresAt: expires, CreatedAt: now,
	}
	if err := s.d.Store.InsertQuote(ctx, rec); err != nil {
		return Record{}, fmt.Errorf("estimate: gravar orçamento: %w", err)
	}
	s.record(ctx, p, "quote.create", rec, map[string]any{"expected_credits": q.ExpectedCredits, "cap_credits": q.CapCredits, "lane": lane, "kind": kind})
	return rec, nil
}

// Verify loads a quote for a run: it must exist, be open, unexpired, carry
// the given signature and match the request fingerprint.
func (s *Service) Verify(ctx context.Context, orgID, quoteID uuid.UUID, signature, fingerprint string) (Record, error) {
	rec, ok, err := s.d.Store.GetQuote(ctx, orgID, quoteID)
	if err != nil {
		return Record{}, fmt.Errorf("estimate: ler orçamento: %w", err)
	}
	if !ok {
		return Record{}, apix.E(contracts.ErrQuoteNotFound, nil)
	}
	if !s.d.Signer.Verify(rec.Quote, signature) || signature != rec.Signature {
		return Record{}, apix.Ef(contracts.ErrQuoteInvalidSignature, "assinatura não confere")
	}
	if s.d.Now().After(rec.ExpiresAt) || rec.Status == StatusExpired {
		return Record{}, apix.E(contracts.ErrQuoteExpired, nil)
	}
	if rec.Status != StatusOpen {
		return Record{}, apix.Ef(contracts.ErrQuoteMismatch, "orçamento já %s", rec.Status)
	}
	if fingerprint != "" && rec.Fingerprint != fingerprint {
		return Record{}, apix.Ef(contracts.ErrQuoteMismatch, "fontes ou opções diferentes do orçamento")
	}
	return rec, nil
}

// Consume marks a quote as used by a run.
func (s *Service) Consume(ctx context.Context, orgID, quoteID, runID uuid.UUID) error {
	return s.d.Store.SetQuoteStatus(ctx, orgID, quoteID, StatusUsed, &runID)
}

// Expire marks a quote as expired (approval not given in time).
func (s *Service) Expire(ctx context.Context, orgID, quoteID uuid.UUID) error {
	return s.d.Store.SetQuoteStatus(ctx, orgID, quoteID, StatusExpired, nil)
}

// Get returns a stored quote.
func (s *Service) Get(ctx context.Context, orgID, quoteID uuid.UUID) (Record, bool, error) {
	return s.d.Store.GetQuote(ctx, orgID, quoteID)
}

// Rates returns the cached model rates, refreshing them from the store.
func (s *Service) Rates(ctx context.Context) ModelRates {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.d.Now()
	if !s.ratesAt.IsZero() && now.Sub(s.ratesAt) < ratesTTL {
		return s.rates
	}
	rows, err := s.d.Store.LatestModelPrices(ctx)
	if err != nil || len(rows) == 0 {
		if err != nil {
			s.d.Logger.Warn("preços de modelo indisponíveis; usando a tabela estática", slog.String("err", err.Error()))
		}
		rows = FallbackModelPrices()
	}
	s.rates, s.ratesAt = RatesFrom(rows), now
	return s.rates
}

// Units measures the input of a run (MeasureUnits with this configuration).
func (s *Service) Units(loaded Loaded, items []Item) Units {
	return MeasureUnits(s.d.Throughput, loaded.Docs, loaded.Media, items)
}

// Time returns the expected seconds of a run with these units (where it will
// execute) and whether that makes a large order; (0, false) when timing is off.
func (s *Service) Time(ctx context.Context, units Units) (int64, bool) {
	t := s.Timing(ctx, units)
	return t.Seconds, t.Large
}

// localSeconds is the expected time on the main box, its queue included.
func (s *Service) localSeconds(ctx context.Context, cfg *throughput.Config, tp Throughput, units Units) int64 {
	inFlight, err := s.d.Store.RunsInFlight(ctx, s.d.Now().Add(-inFlightFreshness))
	if err != nil {
		s.d.Logger.Warn("fila de execuções indisponível; estimativa sem espera", slog.String("err", err.Error()))
		inFlight = 0
	}
	return ExpectedSeconds(cfg, tp, units, QueueWait(cfg, tp, inFlight))
}

// instanceMinutes is what a burst run adds to the price: its minutes on the
// pool when pricing.yaml charges them (burst.charge_instance_minutes), else 0.
func (s *Service) instanceMinutes(t QuoteTiming) float64 {
	if !t.Placement.Burst || !s.d.Pricing.Burst.ChargeInstanceMinutes {
		return 0
	}
	return float64(t.Seconds) / 60
}

// throughputRates returns the cached per-kind rates, relearning them from
// the history every ratesTTL.
func (s *Service) throughputRates(ctx context.Context) Throughput {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.d.Now()
	if !s.tpAt.IsZero() && now.Sub(s.tpAt) < ratesTTL {
		return s.tp
	}
	cfg := s.d.Throughput
	samples, err := s.d.Store.RecentTimings(ctx, now.Add(-cfg.Window()), cfg.History.MaxRuns)
	if err != nil {
		s.d.Logger.Warn("histórico de durações indisponível; usando os padrões", slog.String("err", err.Error()))
		samples = nil
	}
	s.tp, s.tpAt = ThroughputFrom(cfg, samples), now
	return s.tp
}

// RecordTiming stores the measured duration of a finished run for the history.
func (s *Service) RecordTiming(ctx context.Context, t RunTiming) error {
	if t.Units == nil {
		t.Units = Units{}
	}
	return s.d.Store.InsertTiming(ctx, t)
}

// CapRatio picks the cap multiplier: the harness calibration when it sends
// one, else the p99 overrun of recent runs, else DefaultCapRatio.
func (s *Service) CapRatio(ctx context.Context, cal *pb.Calibration) (float64, *Calibration) {
	if cal != nil && cal.GetP99Overrun() >= 1 {
		c := &Calibration{SampleRuns: int64(min(cal.GetSampleRuns(), 1<<62)), P99Overrun: cal.GetP99Overrun()} //nolint:gosec // bounded
		if cal.GetCalibratedAt() != nil {
			c.CalibratedAt = cal.GetCalibratedAt().AsTime().UTC().Format(time.RFC3339)
		}
		return cal.GetP99Overrun(), c
	}
	samples, err := s.d.Store.RecentOverruns(ctx, s.d.Now().Add(-overrunWindow), overrunLimit)
	if err != nil {
		s.d.Logger.Warn("histórico de execuções indisponível para o teto", slog.String("err", err.Error()))
	}
	if ratio, ok := P99Overrun(samples); ok {
		return ratio, &Calibration{SampleRuns: int64(len(samples)), P99Overrun: ratio, CalibratedAt: s.d.Now().UTC().Format(time.RFC3339)}
	}
	return DefaultCapRatio, &Calibration{SampleRuns: int64(len(samples)), P99Overrun: DefaultCapRatio}
}

// ItemsFromProto converts the harness breakdown items.
func ItemsFromProto(bd *pb.EstimateBreakdown) []Item {
	if bd == nil {
		return nil
	}
	out := make([]Item, 0, len(bd.GetItems()))
	for _, it := range bd.GetItems() {
		out = append(out, Item{
			Item: enumName(pb.EstimateItemKind_name[int32(it.GetItem())], "ESTIMATE_ITEM_KIND_"), Quantity: it.GetQuantity(),
			Unit: enumName(pb.EstimateUnit_name[int32(it.GetUnit())], "ESTIMATE_UNIT_"), Note: it.GetNote(),
		})
	}
	return out
}

// ApprovalReasons converts the enum list to the contract's strings.
func ApprovalReasons(rs []pb.ApprovalReason) []string {
	out := make([]string, 0, len(rs))
	for _, r := range rs {
		if r == pb.ApprovalReason_APPROVAL_REASON_UNSPECIFIED {
			continue
		}
		out = append(out, enumName(pb.ApprovalReason_name[int32(r)], "APPROVAL_REASON_"))
	}
	return out
}

func enumName(name, prefix string) string {
	return strings.ToLower(strings.TrimPrefix(name, prefix))
}

func (s *Service) record(ctx context.Context, p apix.Principal, action string, rec Record, meta map[string]any) {
	if s.d.Audit == nil {
		return
	}
	org := p.OrgID
	if _, err := s.d.Audit.Record(ctx, &org, p.Actor(), action, audit.Target{Type: "quote", ID: rec.PublicID()}, meta); err != nil {
		s.d.Logger.Error("auditoria falhou", slog.String("action", action), slog.String("err", err.Error()))
	}
}
