package runs

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/mmtmn/docmatch/services/api/internal/estimate"
	pb "github.com/mmtmn/docmatch/services/api/internal/harness/pb"
	"github.com/mmtmn/docmatch/services/api/internal/runs/apix"
)

// enumName lowercases a proto enum name without its prefix.
func enumName(name, prefix string) string {
	return strings.ToLower(strings.TrimPrefix(name, prefix))
}

// StageName renders a Stage for the API.
func StageName(s pb.Stage) string {
	return enumName(pb.Stage_name[int32(s)], "STAGE_")
}

// evidenceJSON mirrors finding.json $defs/Evidence.
type evidenceJSON struct {
	SourceID string         `json:"source_id"`
	Locator  map[string]any `json:"locator"`
	Excerpt  string         `json:"excerpt,omitempty"`
	Label    string         `json:"label,omitempty"`
	Values   map[string]any `json:"values,omitempty"`
}

// LocatorJSON renders a Locator per schemas/evidence-locator.json.
func LocatorJSON(l *pb.Locator) map[string]any {
	switch v := l.GetLocator().(type) {
	case *pb.Locator_Row:
		out := map[string]any{"type": "row", "row": max(v.Row.GetRow(), 1)}
		if v.Row.GetSheet() != "" {
			out["sheet"] = v.Row.GetSheet()
		}
		if v.Row.GetRowEnd() >= v.Row.GetRow() && v.Row.GetRowEnd() > 0 {
			out["row_end"] = v.Row.GetRowEnd()
		}
		if v.Row.GetColumn() != "" {
			out["column"] = v.Row.GetColumn()
		}
		return out
	case *pb.Locator_PageBox:
		return map[string]any{"type": "page_box", "page": max(v.PageBox.GetPage(), 1), "box": map[string]any{"x": v.PageBox.GetX(), "y": v.PageBox.GetY(), "w": v.PageBox.GetW(), "h": v.PageBox.GetH()}}
	case *pb.Locator_ByteRange:
		out := map[string]any{"type": "byte_range", "start": v.ByteRange.GetStart(), "end": max(v.ByteRange.GetEnd(), v.ByteRange.GetStart()+1)}
		if v.ByteRange.GetEncoding() != "" {
			out["encoding"] = v.ByteRange.GetEncoding()
		}
		return out
	case *pb.Locator_TimeRange:
		out := map[string]any{"type": "time_range", "start_ms": v.TimeRange.GetStartMs(), "end_ms": max(v.TimeRange.GetEndMs(), v.TimeRange.GetStartMs()+1)}
		if v.TimeRange.GetTrack() != pb.Track_TRACK_UNSPECIFIED {
			out["track"] = enumName(pb.Track_name[int32(v.TimeRange.GetTrack())], "TRACK_")
		}
		if v.TimeRange.GetFrame() > 0 {
			out["frame"] = v.TimeRange.GetFrame()
		}
		return out
	}
	return map[string]any{"type": "row", "row": 1}
}

// EvidenceJSON renders an evidence list.
func EvidenceJSON(evs []*pb.Evidence) json.RawMessage {
	out := make([]evidenceJSON, 0, len(evs))
	for _, e := range evs {
		item := evidenceJSON{SourceID: e.GetSourceId(), Locator: LocatorJSON(e.GetLocator()), Excerpt: e.GetExcerpt(), Label: e.GetLabel()}
		if e.GetValues() != nil {
			item.Values = e.GetValues().AsMap()
		}
		out = append(out, item)
	}
	data, _ := json.Marshal(out)
	return data
}

func decodeOrNew(prefix, s string) uuid.UUID {
	if id, err := apix.Decode(prefix, s); err == nil {
		return id
	}
	id, err := uuid.NewV7()
	if err != nil {
		return uuid.New()
	}
	return id
}

func decodePtr(prefix, s string) *uuid.UUID {
	if s == "" {
		return nil
	}
	id, err := apix.Decode(prefix, s)
	if err != nil {
		return nil
	}
	return &id
}

func protoTime(ts *timestamppb.Timestamp, fallback time.Time) time.Time {
	if ts == nil {
		return fallback
	}
	return ts.AsTime().UTC()
}

// FindingFromProto converts a harness finding into the stored record.
func FindingFromProto(orgID, runID uuid.UUID, f *pb.Finding, now time.Time) Finding {
	out := Finding{
		ID: decodeOrNew(apix.PrefixFinding, f.GetId()), OrgID: orgID, RunID: runID, Code: f.GetCode(),
		Kind: enumName(pb.FindingKind_name[int32(f.GetKind())], "FINDING_KIND_"), Severity: enumName(pb.Severity_name[int32(f.GetSeverity())], "SEVERITY_"),
		AmountAtRisk: max(f.GetAmountAtRisk(), 0), Currency: f.GetCurrency(), Title: f.GetTitle(), Explanation: f.GetExplanation(),
		Confidence: min(max(f.GetConfidence(), 0), 1), Verified: f.GetConfidenceLabel() == pb.ConfidenceLabel_CONFIDENCE_LABEL_CONFIRMED,
		Evidence: EvidenceJSON(f.GetEvidence()), Status: "open", RuleID: decodePtr(apix.PrefixRule, f.GetRuleId()), MatchID: decodePtr(apix.PrefixMatch, f.GetMatchId()),
		SourceIDs: append([]string(nil), f.GetSourceIds()...), CreatedAt: protoTime(f.GetCreatedAt(), now), UpdatedAt: now,
	}
	if out.Code == "" {
		out.Code = "finding"
	}
	if out.Kind == "unspecified" {
		out.Kind = "note"
	}
	if out.Severity == "unspecified" {
		out.Severity = "medium"
	}
	if out.Currency == "" {
		out.Currency = "BRL"
	}
	if out.Title == "" {
		out.Title = out.Code
	}
	if st := f.GetStatus(); st != pb.FindingStatus_FINDING_STATUS_UNSPECIFIED {
		out.Status = enumName(pb.FindingStatus_name[int32(st)], "FINDING_STATUS_")
	}
	if f.GetDetails() != nil {
		out.Details, _ = json.Marshal(f.GetDetails().AsMap())
	}
	return out
}

// MatchFromProto converts a harness match into the stored record.
func MatchFromProto(orgID, runID uuid.UUID, m *pb.Match, now time.Time) Match {
	out := Match{
		ID: decodeOrNew(apix.PrefixMatch, m.GetId()), OrgID: orgID, RunID: runID,
		Kind: enumName(pb.MatchKind_name[int32(m.GetKind())], "MATCH_KIND_"), Left: EvidenceJSON(m.GetLeft()), Right: EvidenceJSON(m.GetRight()),
		Confidence: min(max(m.GetConfidence(), 0), 1), Currency: m.GetCurrency(), RuleID: decodePtr(apix.PrefixRule, m.GetRuleId()),
		Explanation: m.GetExplanation(), CreatedAt: protoTime(m.GetCreatedAt(), now),
	}
	if out.Kind == "unspecified" {
		out.Kind = "exact"
	}
	if m.GetAmount() != 0 || m.GetCurrency() != "" {
		amount := m.GetAmount()
		out.Amount = &amount
	}
	return out
}

// SummaryFromProto converts the completed summary.
func SummaryFromProto(s *pb.RunSummary) *Summary {
	if s == nil {
		return &Summary{FindingsBySeverity: map[string]int{}, Currency: "BRL"}
	}
	by := map[string]int{}
	for k, v := range s.GetFindingsBySeverity() {
		by[k] = int(v)
	}
	out := &Summary{
		FindingsTotal: int(s.GetFindingsTotal()), FindingsBySeverity: by, AmountAtRisk: max(s.GetAmountAtRisk(), 0), Currency: s.GetCurrency(),
		MatchesTotal: int(s.GetMatchesTotal()), LinesTotal: int(s.GetLinesTotal()), LinesMatched: int(s.GetLinesMatched()),
		NewToolIDs: append([]string(nil), s.GetNewToolIds()...), ProposedRuleIDs: append([]string(nil), s.GetProposedRuleIds()...),
		SourceWarnings: sourceWarnings(s.GetSourceWarnings()),
	}
	if out.Currency == "" {
		out.Currency = "BRL"
	}
	return out
}

// QuoteToProto renders a signed quote for the harness.
func QuoteToProto(q estimate.Quote) *pb.Quote {
	out := &pb.Quote{
		Id: q.ID, ExpectedCredits: uint64(max(q.ExpectedCredits, 0)), CapCredits: uint64(max(q.CapCredits, 0)), //nolint:gosec // non-negative
		Signature: q.Signature, Fingerprint: q.Fingerprint, RequiresApproval: q.RequiresApproval,
		Breakdown: &pb.EstimateBreakdown{
			LaneMultiplier: q.Breakdown.LaneMultiplier, SubtotalCredits: uint64(max(q.Breakdown.SubtotalCredits, 0)), //nolint:gosec // non-negative
			MinimumCredits: uint64(max(q.Breakdown.MinimumCredits, 0)), TotalCredits: uint64(max(q.Breakdown.TotalCredits, 0)), //nolint:gosec // non-negative
			PricingVersion: q.Breakdown.PricingVersion,
		},
	}
	if q.RunID != nil {
		out.RunId = *q.RunID
	}
	if t, err := time.Parse(time.RFC3339, q.ExpiresAt); err == nil {
		out.ExpiresAt = timestamppb.New(t)
	}
	if t, err := time.Parse(time.RFC3339, q.CreatedAt); err == nil {
		out.CreatedAt = timestamppb.New(t)
	}
	return out
}

// Event data documents (openapi RunEvent* schemas).

type stageStartedData struct {
	Stage      string `json:"stage"`
	Message    string `json:"message,omitempty"`
	TotalSteps uint64 `json:"total_steps,omitempty"`
}

type progressData struct {
	Stage        string `json:"stage"`
	Done         uint64 `json:"done"`
	Total        uint64 `json:"total"`
	Message      string `json:"message,omitempty"`
	CreditsSpent uint64 `json:"credits_spent,omitempty"`
}

type tokenUsageData struct {
	Role              string `json:"role"`
	Model             string `json:"model"`
	PromptTokens      uint64 `json:"prompt_tokens"`
	CompletionTokens  uint64 `json:"completion_tokens"`
	CachedTokens      uint64 `json:"cached_tokens,omitempty"`
	Credits           uint64 `json:"credits"`
	CreditsSpentTotal uint64 `json:"credits_spent_total"`
	Cached            bool   `json:"cached,omitempty"`
}

type findingData struct {
	Finding FindingView `json:"finding"`
}

type matchData struct {
	Match MatchView `json:"match"`
}

type needsApprovalData struct {
	Quote             estimate.Quote `json:"quote"`
	Reasons           []string       `json:"reasons"`
	Message           string         `json:"message,omitempty"`
	CreditsSpentSoFar int64          `json:"credits_spent_so_far"`
}

type completedData struct {
	Summary         Summary `json:"summary"`
	CreditsSpent    int64   `json:"credits_spent"`
	CreditsRefunded int64   `json:"credits_refunded"`
}

type failedData struct {
	Error        RunError `json:"error"`
	CreditsSpent int64    `json:"credits_spent"`
}

func mustJSON(v any) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("{}")
	}
	return data
}

// Bounds of the warnings a run keeps: enough to name every skipped file of a
// large order, never an unbounded payload.
const (
	maxSourceWarnings     = 100
	maxSourceWarningRunes = 500
)

// sourceWarnings copies the harness's per-file warnings within those bounds.
func sourceWarnings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, min(len(in), maxSourceWarnings))
	for _, w := range in {
		if len(out) == maxSourceWarnings {
			break
		}
		w = strings.TrimSpace(w)
		if w == "" {
			continue
		}
		if r := []rune(w); len(r) > maxSourceWarningRunes {
			w = string(r[:maxSourceWarningRunes-1]) + "…"
		}
		out = append(out, w)
	}
	return out
}
