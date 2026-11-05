// Package runs owns the lifecycle of a reconciliation run: creation from a
// signed quote (credits reserved and the job enqueued), the worker side
// that streams the harness and persists events, findings and matches,
// approval of additional budget, cancellation, the SSE feed, findings with
// feedback and exports. Status machine:
//
//	queued → running → completed | failed | cancelled
//	           ↓            ↑
//	    needs_approval ─────┘ (approve-quote re-queues; cancel settles)
package runs

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/mmtmn/docmatch/services/api/internal/estimate"
	"github.com/mmtmn/docmatch/services/api/internal/runs/apix"
	"github.com/mmtmn/docmatch/services/api/internal/sse"
	"github.com/mmtmn/docmatch/services/api/internal/uploads"
)

// Status is openapi RunStatus (and runs.status).
type Status string

// Statuses.
const (
	StatusQueued        Status = "queued"
	StatusRunning       Status = "running"
	StatusNeedsApproval Status = "needs_approval"
	StatusCompleted     Status = "completed"
	StatusFailed        Status = "failed"
	StatusCancelled     Status = "cancelled"
)

// Terminal reports whether the run can no longer change.
func (s Status) Terminal() bool {
	return s == StatusCompleted || s == StatusFailed || s == StatusCancelled
}

// Valid reports whether s is a known status.
func (s Status) Valid() bool {
	switch s {
	case StatusQueued, StatusRunning, StatusNeedsApproval, StatusCompleted, StatusFailed, StatusCancelled:
		return true
	}
	return false
}

// Event names (openapi RunEvent.event).
const (
	EventStageStarted  = "stage_started"
	EventProgress      = "progress"
	EventTokenUsage    = "token_usage"
	EventFinding       = "finding"
	EventMatch         = "match"
	EventNeedsApproval = "needs_approval"
	EventCompleted     = "completed"
	EventFailed        = "failed"
)

// Inputs is what the worker needs to rebuild the harness request; stored
// as runs.inputs.
type Inputs struct {
	Request  estimate.Request `json:"request"`
	Plan     string           `json:"plan"`
	UserID   *uuid.UUID       `json:"user_id,omitempty"`
	APIKeyID *uuid.UUID       `json:"api_key_id,omitempty"`
	Lanes    []string         `json:"lanes,omitempty"`
}

// Principal rebuilds the caller for the worker.
func (in Inputs) Principal(orgID uuid.UUID) apix.Principal {
	return apix.Principal{OrgID: orgID, UserID: in.UserID, APIKeyID: in.APIKeyID, Plan: in.Plan, Lanes: in.Lanes}
}

// Progress is openapi RunProgress.
type Progress struct {
	Stage   string `json:"stage"`
	Percent int    `json:"percent"`
	Message string `json:"message,omitempty"`
}

// Summary is openapi RunSummary.
type Summary struct {
	FindingsTotal      int            `json:"findings_total"`
	FindingsBySeverity map[string]int `json:"findings_by_severity"`
	AmountAtRisk       int64          `json:"amount_at_risk"`
	Currency           string         `json:"currency"`
	MatchesTotal       int            `json:"matches_total"`
	LinesTotal         int            `json:"lines_total"`
	LinesMatched       int            `json:"lines_matched"`
	NewToolIDs         []string       `json:"new_tool_ids,omitempty"`
	ProposedRuleIDs    []string       `json:"proposed_rule_ids,omitempty"`
}

// RunError is openapi RunError.
type RunError struct {
	Code      string         `json:"code"`
	Message   string         `json:"message"`
	Retryable bool           `json:"retryable"`
	Details   map[string]any `json:"details,omitempty"`
}

// Run is the stored record.
type Run struct {
	ID               uuid.UUID
	OrgID            uuid.UUID
	WorkflowID       *uuid.UUID
	UserID           *uuid.UUID
	APIKeyID         *uuid.UUID
	QuoteID          *uuid.UUID
	HoldID           *uuid.UUID
	PendingQuoteID   *uuid.UUID
	Lane             string
	Status           Status
	EstimatedCredits int64
	CreditsHeld      int64
	CreditsSpent     int64
	CreditsSettled   int64
	CreditsRefunded  int64
	LastSeq          int64
	Inputs           Inputs
	Progress         *Progress
	Summary          *Summary
	Error            *RunError
	ErrorCode        string
	CancelRequested  bool
	Stats            json.RawMessage
	StartedAt        *time.Time
	FinishedAt       *time.Time
	ExpiresAt        *time.Time
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// PublicID renders the API identifier.
func (r Run) PublicID() string { return apix.Encode(apix.PrefixRun, r.ID) }

// SourceIDs lists every upload referenced by the run, documents first.
func (r Run) SourceIDs() []uuid.UUID {
	var out []uuid.UUID
	for _, refs := range [][]estimate.SourceRef{r.Inputs.Request.Sources, r.Inputs.Request.MediaSources} {
		for _, s := range refs {
			if id, err := apix.Decode(apix.PrefixUpload, s.UploadID); err == nil {
				out = append(out, id)
			}
		}
	}
	return out
}

// Credits is openapi RunCredits.
type Credits struct {
	Reserved int64 `json:"reserved"`
	Spent    int64 `json:"spent"`
	Refunded int64 `json:"refunded"`
}

// View is openapi Run.
type View struct {
	ID           string          `json:"id"`
	Status       Status          `json:"status"`
	WorkflowID   *string         `json:"workflow_id"`
	Lane         string          `json:"lane"`
	Sources      []uploads.View  `json:"sources"`
	Quote        *estimate.Quote `json:"quote,omitempty"`
	PendingQuote *estimate.Quote `json:"pending_quote,omitempty"`
	Credits      Credits         `json:"credits"`
	Progress     *Progress       `json:"progress,omitempty"`
	Summary      *Summary        `json:"summary,omitempty"`
	Error        *RunError       `json:"error,omitempty"`
	CreatedAt    string          `json:"created_at"`
	StartedAt    *string         `json:"started_at,omitempty"`
	FinishedAt   *string         `json:"finished_at,omitempty"`
	ExpiresAt    string          `json:"expires_at"`
}

func rfc3339(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.UTC().Format(time.RFC3339)
	return &s
}

// View renders the run with its sources and quotes.
func (r Run) View(sources []uploads.View, quote, pending *estimate.Quote) View {
	v := View{
		ID: r.PublicID(), Status: r.Status, WorkflowID: apix.EncodePtr(apix.PrefixWorkflow, r.WorkflowID), Lane: r.Lane, Sources: sources,
		Quote: quote, PendingQuote: pending,
		Credits:  Credits{Reserved: r.CreditsHeld, Spent: r.CreditsSpent, Refunded: r.CreditsRefunded},
		Progress: r.Progress, Summary: r.Summary, Error: r.Error,
		CreatedAt: r.CreatedAt.UTC().Format(time.RFC3339), StartedAt: rfc3339(r.StartedAt), FinishedAt: rfc3339(r.FinishedAt),
		ExpiresAt: r.CreatedAt.UTC().Format(time.RFC3339),
	}
	if r.ExpiresAt != nil {
		v.ExpiresAt = r.ExpiresAt.UTC().Format(time.RFC3339)
	}
	if v.Sources == nil {
		v.Sources = []uploads.View{}
	}
	if r.Status != StatusNeedsApproval {
		v.PendingQuote = nil
	}
	return v
}

// FeedbackView is finding.json $defs/Feedback.
type FeedbackView struct {
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
	Note   string `json:"note,omitempty"`
	By     string `json:"by,omitempty"`
	At     string `json:"at"`
}

// Finding is the stored record.
type Finding struct {
	ID           uuid.UUID
	OrgID        uuid.UUID
	RunID        uuid.UUID
	Code         string
	Kind         string
	Severity     string
	AmountAtRisk int64
	Currency     string
	Title        string
	Explanation  string
	Confidence   float64
	Verified     bool
	Evidence     json.RawMessage
	Status       string
	RuleID       *uuid.UUID
	MatchID      *uuid.UUID
	SourceIDs    []string
	Details      json.RawMessage
	Feedback     *FeedbackView
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// PublicID renders the API identifier.
func (f Finding) PublicID() string { return apix.Encode(apix.PrefixFinding, f.ID) }

// ConfidenceLabel maps confidence to the pt-BR label of finding.json.
func ConfidenceLabel(confidence float64, verified bool) string {
	switch {
	case confidence >= 0.9 && verified:
		return "Confirmado"
	case confidence >= 0.6:
		return "Provável"
	}
	return "Precisa de você"
}

// FindingView is finding.json.
type FindingView struct {
	ID              string          `json:"id"`
	RunID           string          `json:"run_id"`
	Code            string          `json:"code"`
	Kind            string          `json:"kind"`
	Severity        string          `json:"severity"`
	AmountAtRisk    int64           `json:"amount_at_risk"`
	Currency        string          `json:"currency"`
	Title           string          `json:"title"`
	Explanation     string          `json:"explanation"`
	Confidence      float64         `json:"confidence"`
	ConfidenceLabel string          `json:"confidence_label"`
	Evidence        json.RawMessage `json:"evidence"`
	Status          string          `json:"status"`
	RuleID          *string         `json:"rule_id,omitempty"`
	MatchID         *string         `json:"match_id,omitempty"`
	SourceIDs       []string        `json:"source_ids,omitempty"`
	Details         json.RawMessage `json:"details,omitempty"`
	Feedback        *FeedbackView   `json:"feedback,omitempty"`
	CreatedAt       string          `json:"created_at"`
	UpdatedAt       string          `json:"updated_at,omitempty"`
}

// View renders the finding.
func (f Finding) View() FindingView {
	ev := f.Evidence
	if len(ev) == 0 {
		ev = json.RawMessage("[]")
	}
	v := FindingView{
		ID: f.PublicID(), RunID: apix.Encode(apix.PrefixRun, f.RunID), Code: f.Code, Kind: f.Kind, Severity: f.Severity,
		AmountAtRisk: f.AmountAtRisk, Currency: f.Currency, Title: f.Title, Explanation: f.Explanation, Confidence: f.Confidence,
		ConfidenceLabel: ConfidenceLabel(f.Confidence, f.Verified), Evidence: ev, Status: f.Status,
		RuleID: apix.EncodePtr(apix.PrefixRule, f.RuleID), MatchID: apix.EncodePtr(apix.PrefixMatch, f.MatchID), SourceIDs: f.SourceIDs,
		Feedback: f.Feedback, CreatedAt: f.CreatedAt.UTC().Format(time.RFC3339),
	}
	if len(f.Details) > 0 && string(f.Details) != "null" {
		v.Details = f.Details
	}
	if !f.UpdatedAt.IsZero() {
		v.UpdatedAt = f.UpdatedAt.UTC().Format(time.RFC3339)
	}
	if v.Explanation == "" {
		v.Explanation = v.Title
	}
	return v
}

// Match is the stored record.
type Match struct {
	ID          uuid.UUID
	OrgID       uuid.UUID
	RunID       uuid.UUID
	Kind        string
	Left        json.RawMessage
	Right       json.RawMessage
	Confidence  float64
	Amount      *int64
	Currency    string
	RuleID      *uuid.UUID
	Explanation string
	CreatedAt   time.Time
}

// PublicID renders the API identifier.
func (m Match) PublicID() string { return apix.Encode(apix.PrefixMatch, m.ID) }

// MatchView is openapi Match.
type MatchView struct {
	ID          string          `json:"id"`
	RunID       string          `json:"run_id"`
	Kind        string          `json:"kind"`
	Left        json.RawMessage `json:"left"`
	Right       json.RawMessage `json:"right"`
	Confidence  float64         `json:"confidence"`
	Amount      *int64          `json:"amount,omitempty"`
	Currency    string          `json:"currency,omitempty"`
	RuleID      *string         `json:"rule_id,omitempty"`
	Explanation string          `json:"explanation,omitempty"`
	CreatedAt   string          `json:"created_at"`
}

// View renders the match.
func (m Match) View() MatchView {
	left, right := m.Left, m.Right
	if len(left) == 0 {
		left = json.RawMessage("[]")
	}
	if len(right) == 0 {
		right = json.RawMessage("[]")
	}
	return MatchView{
		ID: m.PublicID(), RunID: apix.Encode(apix.PrefixRun, m.RunID), Kind: m.Kind, Left: left, Right: right, Confidence: m.Confidence,
		Amount: m.Amount, Currency: m.Currency, RuleID: apix.EncodePtr(apix.PrefixRule, m.RuleID), Explanation: m.Explanation,
		CreatedAt: m.CreatedAt.UTC().Format(time.RFC3339),
	}
}

// Event is one persisted run event.
type Event struct {
	OrgID     uuid.UUID
	RunID     uuid.UUID
	Seq       int64
	Name      string
	Data      json.RawMessage
	CreatedAt time.Time
}

// Feedback is one feedback row (append-only).
type Feedback struct {
	OrgID            uuid.UUID
	UserID           *uuid.UUID
	RunID            uuid.UUID
	FindingID        uuid.UUID
	FindingCreatedAt time.Time
	Status           string
	Reason           string
	Note             string
	CreatedAt        time.Time
}

// ListFilter filters GET /runs.
type ListFilter struct {
	Status     Status
	WorkflowID *uuid.UUID
}

// FindingFilter filters GET /runs/{id}/findings.
type FindingFilter struct {
	Severities []string
	Status     string
}

// TxHook runs inside the store's transaction (nil tx for memory stores).
type TxHook func(ctx context.Context, tx pgx.Tx) error

// Store is the persistence boundary.
type Store interface {
	CreateRun(ctx context.Context, r Run, hook TxHook) (Run, error)
	GetRun(ctx context.Context, orgID, id uuid.UUID) (Run, bool, error)
	ListRuns(ctx context.Context, orgID uuid.UUID, f ListFilter, limit, offset int) ([]Run, error)
	SaveRun(ctx context.Context, r Run, hook TxHook) (Run, error)
	CountActiveRuns(ctx context.Context, orgID uuid.UUID) (int64, error)

	AppendEvent(ctx context.Context, e Event) error
	EventsAfter(ctx context.Context, orgID, runID uuid.UUID, afterSeq int64, limit int) ([]sse.Event, error)

	InsertFinding(ctx context.Context, f Finding) (Finding, error)
	GetFinding(ctx context.Context, orgID, runID, id uuid.UUID) (Finding, bool, error)
	ListFindings(ctx context.Context, orgID, runID uuid.UUID, f FindingFilter, limit, offset int) ([]Finding, error)
	ListAllFindings(ctx context.Context, orgID, runID uuid.UUID) ([]Finding, error)
	ListFindingsWithFeedback(ctx context.Context, orgID, runID uuid.UUID) ([]Finding, error)
	UpdateFindingFeedback(ctx context.Context, orgID, runID, id uuid.UUID, status string, fb *FeedbackView) (Finding, error)

	InsertMatch(ctx context.Context, m Match) (Match, error)
	ListAllMatches(ctx context.Context, orgID, runID uuid.UUID) ([]Match, error)

	InsertFeedback(ctx context.Context, f Feedback) error
}
