package pgstore

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/mmtmn/docmatch/services/api/internal/db/sqlc"
	"github.com/mmtmn/docmatch/services/api/internal/runs"
	"github.com/mmtmn/docmatch/services/api/internal/sse"
)

func toRun(r sqlc.Run) (runs.Run, error) {
	out := runs.Run{
		ID: r.ID, OrgID: r.OrgID, WorkflowID: r.WorkflowID, UserID: r.UserID, APIKeyID: r.ApiKeyID, QuoteID: r.QuoteID, HoldID: r.HoldID,
		PendingQuoteID: r.PendingQuoteID, Lane: r.Lane, Status: runs.Status(r.Status), EstimatedCredits: r.EstimatedCredits,
		CreditsHeld: r.CreditsHeld, CreditsSpent: r.CreditsSpent, CreditsSettled: r.CreditsSettled, CreditsRefunded: r.CreditsRefunded,
		LastSeq: r.LastSeq, ErrorCode: strVal(r.ErrorCode), CancelRequested: r.CancelRequested, Stats: r.Stats,
		StartedAt: r.StartedAt, FinishedAt: r.FinishedAt, ExpiresAt: r.ExpiresAt, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
	if len(r.Inputs) > 0 {
		if err := json.Unmarshal(r.Inputs, &out.Inputs); err != nil {
			return runs.Run{}, err
		}
	}
	if len(r.Progress) > 0 {
		_ = json.Unmarshal(r.Progress, &out.Progress)
	}
	if len(r.Summary) > 0 {
		_ = json.Unmarshal(r.Summary, &out.Summary)
	}
	if len(r.Error) > 0 {
		_ = json.Unmarshal(r.Error, &out.Error)
	}
	return out, nil
}

// CreateRun implements runs.Store: the row and the hook (the job insert)
// commit together.
func (s *Store) CreateRun(ctx context.Context, r runs.Run, hook runs.TxHook) (runs.Run, error) {
	var out runs.Run
	err := s.org(ctx, r.OrgID, func(ctx context.Context, q *sqlc.Queries, tx pgx.Tx) error {
		row, err := q.InsertRun(ctx, sqlc.InsertRunParams{
			ID: r.ID, OrgID: r.OrgID, WorkflowID: r.WorkflowID, UserID: r.UserID, ApiKeyID: r.APIKeyID, QuoteID: r.QuoteID, HoldID: r.HoldID,
			Lane: r.Lane, EstimatedCredits: r.EstimatedCredits, CreditsHeld: r.CreditsHeld, Inputs: nonEmpty(jsonOrNil(r.Inputs)), ExpiresAt: r.ExpiresAt,
		})
		if err != nil {
			return err
		}
		if hook != nil {
			if err := hook(ctx, tx); err != nil {
				return err
			}
		}
		out, err = toRun(row)
		return err
	})
	return out, err
}

// GetRun implements runs.Store.
func (s *Store) GetRun(ctx context.Context, orgID, id uuid.UUID) (runs.Run, bool, error) {
	var out runs.Run
	found := false
	err := s.org(ctx, orgID, func(ctx context.Context, q *sqlc.Queries, _ pgx.Tx) error {
		row, err := q.GetRun(ctx, sqlc.GetRunParams{OrgID: orgID, ID: id})
		if isNoRows(err) {
			return nil
		}
		if err != nil {
			return err
		}
		out, err = toRun(row)
		found = err == nil
		return err
	})
	return out, found, err
}

// ListRuns implements runs.Store.
func (s *Store) ListRuns(ctx context.Context, orgID uuid.UUID, f runs.ListFilter, limit, offset int) ([]runs.Run, error) {
	out := []runs.Run{}
	err := s.org(ctx, orgID, func(ctx context.Context, q *sqlc.Queries, _ pgx.Tx) error {
		var status *string
		if f.Status != "" {
			st := string(f.Status)
			status = &st
		}
		rows, err := q.ListRunsPage(ctx, sqlc.ListRunsPageParams{OrgID: orgID, Limit: int32(min(max(limit, 1), 1000)), Offset: int32(max(offset, 0)), Status: status, WorkflowID: f.WorkflowID}) //nolint:gosec // bounded
		if err != nil {
			return err
		}
		for _, r := range rows {
			run, err := toRun(r)
			if err != nil {
				return err
			}
			out = append(out, run)
		}
		return nil
	})
	return out, err
}

// SaveRun implements runs.Store.
func (s *Store) SaveRun(ctx context.Context, r runs.Run, hook runs.TxHook) (runs.Run, error) {
	var out runs.Run
	err := s.org(ctx, r.OrgID, func(ctx context.Context, q *sqlc.Queries, tx pgx.Tx) error {
		row, err := q.SaveRunState(ctx, sqlc.SaveRunStateParams{
			OrgID: r.OrgID, ID: r.ID, Status: string(r.Status), Progress: jsonOrNil(r.Progress), Summary: jsonOrNil(r.Summary), Error: jsonOrNil(r.Error),
			ErrorCode: strPtr(r.ErrorCode), CreditsHeld: r.CreditsHeld, CreditsSpent: r.CreditsSpent, CreditsSettled: r.CreditsSettled, CreditsRefunded: r.CreditsRefunded,
			LastSeq: r.LastSeq, PendingQuoteID: r.PendingQuoteID, HoldID: r.HoldID, CancelRequested: r.CancelRequested, StartedAt: r.StartedAt, FinishedAt: r.FinishedAt,
			Stats: nonEmpty(r.Stats),
		})
		if err != nil {
			return err
		}
		if hook != nil {
			if err := hook(ctx, tx); err != nil {
				return err
			}
		}
		out, err = toRun(row)
		return err
	})
	return out, err
}

// CountActiveRuns implements runs.Store.
func (s *Store) CountActiveRuns(ctx context.Context, orgID uuid.UUID) (int64, error) {
	var n int64
	err := s.org(ctx, orgID, func(ctx context.Context, q *sqlc.Queries, _ pgx.Tx) error {
		var err error
		n, err = q.CountActiveRuns(ctx, orgID)
		return err
	})
	return n, err
}

// AppendEvent implements runs.Store (the table trigger NOTIFYs).
func (s *Store) AppendEvent(ctx context.Context, e runs.Event) error {
	return s.org(ctx, e.OrgID, func(ctx context.Context, q *sqlc.Queries, _ pgx.Tx) error {
		_, err := q.InsertRunEvent(ctx, sqlc.InsertRunEventParams{OrgID: e.OrgID, RunID: e.RunID, Seq: e.Seq, Event: e.Name, Data: nonEmpty(e.Data)})
		return err
	})
}

// EventsAfter implements runs.Store and sse.Source.
func (s *Store) EventsAfter(ctx context.Context, orgID, runID uuid.UUID, afterSeq int64, limit int) ([]sse.Event, error) {
	out := []sse.Event{}
	err := s.org(ctx, orgID, func(ctx context.Context, q *sqlc.Queries, _ pgx.Tx) error {
		rows, err := q.ListRunEventsAfter(ctx, sqlc.ListRunEventsAfterParams{OrgID: orgID, RunID: runID, Seq: afterSeq, Limit: int32(min(max(limit, 1), 1000))}) //nolint:gosec // bounded
		if err != nil {
			return err
		}
		for _, r := range rows {
			out = append(out, sse.Event{Seq: r.Seq, Name: r.Event, Data: r.Data})
		}
		return nil
	})
	return out, err
}

func toFinding(f sqlc.Finding) runs.Finding {
	out := runs.Finding{
		ID: f.ID, OrgID: f.OrgID, RunID: f.RunID, Code: f.Code, Kind: f.Kind, Severity: f.Severity, AmountAtRisk: f.AmountAtRisk, Currency: f.Currency,
		Title: f.Title, Explanation: strVal(f.Explanation), Confidence: float64(f.Confidence), Verified: f.Verified, Evidence: f.Evidence, Status: f.Status,
		RuleID: f.RuleID, MatchID: f.MatchID, Details: f.Details, CreatedAt: f.CreatedAt, UpdatedAt: f.UpdatedAt,
	}
	if len(f.SourceIds) > 0 {
		_ = json.Unmarshal(f.SourceIds, &out.SourceIDs)
	}
	if len(f.Feedback) > 0 {
		_ = json.Unmarshal(f.Feedback, &out.Feedback)
	}
	return out
}

// InsertFinding implements runs.Store.
func (s *Store) InsertFinding(ctx context.Context, f runs.Finding) (runs.Finding, error) {
	var out runs.Finding
	err := s.org(ctx, f.OrgID, func(ctx context.Context, q *sqlc.Queries, _ pgx.Tx) error {
		srcIDs := f.SourceIDs
		if srcIDs == nil {
			srcIDs = []string{}
		}
		row, err := q.InsertFindingFull(ctx, sqlc.InsertFindingFullParams{
			ID: f.ID, OrgID: f.OrgID, RunID: f.RunID, Code: f.Code, Kind: f.Kind, Severity: f.Severity, AmountAtRisk: f.AmountAtRisk, Currency: f.Currency,
			Title: f.Title, Explanation: strPtr(f.Explanation), Confidence: float32(f.Confidence), Evidence: nonEmptyList(f.Evidence), Verified: f.Verified,
			Status: f.Status, RuleID: f.RuleID, MatchID: f.MatchID, SourceIds: jsonOrNil(srcIDs), Details: f.Details, CreatedAt: f.CreatedAt,
		})
		if err != nil {
			return err
		}
		out = toFinding(row)
		return nil
	})
	return out, err
}

func nonEmptyList(raw json.RawMessage) []byte {
	if len(raw) == 0 {
		return []byte("[]")
	}
	return raw
}

// GetFinding implements runs.Store.
func (s *Store) GetFinding(ctx context.Context, orgID, runID, id uuid.UUID) (runs.Finding, bool, error) {
	var out runs.Finding
	found := false
	err := s.org(ctx, orgID, func(ctx context.Context, q *sqlc.Queries, _ pgx.Tx) error {
		row, err := q.GetFindingByID(ctx, sqlc.GetFindingByIDParams{OrgID: orgID, RunID: runID, ID: id})
		if isNoRows(err) {
			return nil
		}
		if err != nil {
			return err
		}
		out, found = toFinding(row), true
		return nil
	})
	return out, found, err
}

// ListFindings implements runs.Store.
func (s *Store) ListFindings(ctx context.Context, orgID, runID uuid.UUID, f runs.FindingFilter, limit, offset int) ([]runs.Finding, error) {
	out := []runs.Finding{}
	err := s.org(ctx, orgID, func(ctx context.Context, q *sqlc.Queries, _ pgx.Tx) error {
		sev := f.Severities
		if sev == nil {
			sev = []string{}
		}
		rows, err := q.ListFindingsPage(ctx, sqlc.ListFindingsPageParams{OrgID: orgID, RunID: runID, Limit: int32(min(max(limit, 1), 1000)), Offset: int32(max(offset, 0)), Severities: sev, Status: strPtr(f.Status)}) //nolint:gosec // bounded
		if err != nil {
			return err
		}
		for _, r := range rows {
			out = append(out, toFinding(r))
		}
		return nil
	})
	return out, err
}

// ListAllFindings implements runs.Store.
func (s *Store) ListAllFindings(ctx context.Context, orgID, runID uuid.UUID) ([]runs.Finding, error) {
	out := []runs.Finding{}
	err := s.org(ctx, orgID, func(ctx context.Context, q *sqlc.Queries, _ pgx.Tx) error {
		rows, err := q.ListAllFindingsByRun(ctx, sqlc.ListAllFindingsByRunParams{OrgID: orgID, RunID: runID})
		if err != nil {
			return err
		}
		for _, r := range rows {
			out = append(out, toFinding(r))
		}
		return nil
	})
	return out, err
}

// ListFindingsWithFeedback implements runs.Store.
func (s *Store) ListFindingsWithFeedback(ctx context.Context, orgID, runID uuid.UUID) ([]runs.Finding, error) {
	out := []runs.Finding{}
	err := s.org(ctx, orgID, func(ctx context.Context, q *sqlc.Queries, _ pgx.Tx) error {
		rows, err := q.ListFindingsWithFeedback(ctx, sqlc.ListFindingsWithFeedbackParams{OrgID: orgID, RunID: runID})
		if err != nil {
			return err
		}
		for _, r := range rows {
			out = append(out, toFinding(r))
		}
		return nil
	})
	return out, err
}

// UpdateFindingFeedback implements runs.Store.
func (s *Store) UpdateFindingFeedback(ctx context.Context, orgID, runID, id uuid.UUID, status string, fb *runs.FeedbackView) (runs.Finding, error) {
	var out runs.Finding
	err := s.org(ctx, orgID, func(ctx context.Context, q *sqlc.Queries, _ pgx.Tx) error {
		row, err := q.UpdateFindingFeedback(ctx, sqlc.UpdateFindingFeedbackParams{OrgID: orgID, RunID: runID, ID: id, Status: status, Feedback: jsonOrNil(fb)})
		if err != nil {
			return err
		}
		out = toFinding(row)
		return nil
	})
	return out, err
}

func toMatch(m sqlc.Match) runs.Match {
	return runs.Match{
		ID: m.ID, OrgID: m.OrgID, RunID: m.RunID, Kind: m.Kind, Left: m.LeftEvidence, Right: m.RightEvidence, Confidence: float64(m.Confidence),
		Amount: m.Amount, Currency: m.Currency, RuleID: m.RuleID, Explanation: strVal(m.Explanation), CreatedAt: m.CreatedAt,
	}
}

// firstSource summarizes an evidence list for the legacy left_ref/right_ref columns.
func firstSource(raw json.RawMessage) string {
	var items []struct {
		SourceID string `json:"source_id"`
	}
	if json.Unmarshal(raw, &items) == nil && len(items) > 0 {
		return items[0].SourceID
	}
	return "-"
}

// InsertMatch implements runs.Store.
func (s *Store) InsertMatch(ctx context.Context, m runs.Match) (runs.Match, error) {
	var out runs.Match
	err := s.org(ctx, m.OrgID, func(ctx context.Context, q *sqlc.Queries, _ pgx.Tx) error {
		row, err := q.InsertMatchFull(ctx, sqlc.InsertMatchFullParams{
			ID: m.ID, OrgID: m.OrgID, RunID: m.RunID, Kind: m.Kind, LeftRef: firstSource(m.Left), RightRef: firstSource(m.Right),
			LeftEvidence: nonEmptyList(m.Left), RightEvidence: nonEmptyList(m.Right), RuleID: m.RuleID, Confidence: float32(m.Confidence),
			Amount: m.Amount, Currency: m.Currency, Explanation: strPtr(m.Explanation), Meta: []byte("{}"), CreatedAt: m.CreatedAt,
		})
		if err != nil {
			return err
		}
		out = toMatch(row)
		return nil
	})
	return out, err
}

// ListAllMatches implements runs.Store.
func (s *Store) ListAllMatches(ctx context.Context, orgID, runID uuid.UUID) ([]runs.Match, error) {
	out := []runs.Match{}
	err := s.org(ctx, orgID, func(ctx context.Context, q *sqlc.Queries, _ pgx.Tx) error {
		rows, err := q.ListAllMatchesByRun(ctx, sqlc.ListAllMatchesByRunParams{OrgID: orgID, RunID: runID})
		if err != nil {
			return err
		}
		for _, r := range rows {
			out = append(out, toMatch(r))
		}
		return nil
	})
	return out, err
}

// InsertFeedback implements runs.Store.
func (s *Store) InsertFeedback(ctx context.Context, f runs.Feedback) error {
	return s.org(ctx, f.OrgID, func(ctx context.Context, q *sqlc.Queries, _ pgx.Tx) error {
		_, err := q.InsertFindingFeedback(ctx, sqlc.InsertFindingFeedbackParams{
			OrgID: f.OrgID, UserID: f.UserID, RunID: f.RunID, FindingID: f.FindingID, FindingCreatedAt: f.FindingCreatedAt,
			Verdict: f.Status, Reason: strPtr(f.Reason), Note: strPtr(f.Note),
		})
		return err
	})
}
