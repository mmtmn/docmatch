package pgstore

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/mmtmn/docmatch/services/api/internal/db/sqlc"
	"github.com/mmtmn/docmatch/services/api/internal/rules"
	"github.com/mmtmn/docmatch/services/api/internal/workflows"
)

// ---------------------------------------------------------------- workflows

func toWorkflow(w sqlc.Workflow) workflows.Workflow {
	return workflows.Workflow{ID: w.ID, OrgID: w.OrgID, Name: w.Name, Config: w.Config, CreatedBy: w.CreatedBy, CreatedAt: w.CreatedAt, UpdatedAt: w.UpdatedAt}
}

// CreateWorkflow implements workflows.Store.
func (s *Store) CreateWorkflow(ctx context.Context, w workflows.Workflow) (workflows.Workflow, error) {
	var out workflows.Workflow
	err := s.org(ctx, w.OrgID, func(ctx context.Context, q *sqlc.Queries, _ pgx.Tx) error {
		row, err := q.InsertWorkflow(ctx, sqlc.InsertWorkflowParams{OrgID: w.OrgID, Name: w.Name, Config: nonEmpty(w.Config), CreatedBy: w.CreatedBy})
		if err != nil {
			return err
		}
		out = toWorkflow(row)
		return nil
	})
	return out, err
}

// GetWorkflow implements workflows.Store.
func (s *Store) GetWorkflow(ctx context.Context, orgID, id uuid.UUID) (workflows.Workflow, bool, error) {
	var out workflows.Workflow
	found := false
	err := s.org(ctx, orgID, func(ctx context.Context, q *sqlc.Queries, _ pgx.Tx) error {
		row, err := q.GetWorkflow(ctx, sqlc.GetWorkflowParams{OrgID: orgID, ID: id})
		if isNoRows(err) {
			return nil
		}
		if err != nil {
			return err
		}
		out, found = toWorkflow(row), true
		return nil
	})
	return out, found, err
}

// ListWorkflows implements workflows.Store.
func (s *Store) ListWorkflows(ctx context.Context, orgID uuid.UUID, limit, offset int) ([]workflows.Workflow, error) {
	out := []workflows.Workflow{}
	err := s.org(ctx, orgID, func(ctx context.Context, q *sqlc.Queries, _ pgx.Tx) error {
		rows, err := q.ListWorkflowsPage(ctx, sqlc.ListWorkflowsPageParams{OrgID: orgID, Limit: int32(min(max(limit, 1), 1000)), Offset: int32(max(offset, 0))}) //nolint:gosec // bounded
		if err != nil {
			return err
		}
		for _, r := range rows {
			out = append(out, toWorkflow(r))
		}
		return nil
	})
	return out, err
}

// UpdateWorkflow implements workflows.Store.
func (s *Store) UpdateWorkflow(ctx context.Context, w workflows.Workflow) (workflows.Workflow, error) {
	var out workflows.Workflow
	err := s.org(ctx, w.OrgID, func(ctx context.Context, q *sqlc.Queries, _ pgx.Tx) error {
		row, err := q.UpdateWorkflowConfig(ctx, sqlc.UpdateWorkflowConfigParams{OrgID: w.OrgID, ID: w.ID, Name: w.Name, Config: nonEmpty(w.Config)})
		if err != nil {
			return err
		}
		out = toWorkflow(row)
		return nil
	})
	return out, err
}

// CountRuns implements workflows.Store.
func (s *Store) CountRuns(ctx context.Context, orgID, workflowID uuid.UUID) (int64, error) {
	var n int64
	err := s.org(ctx, orgID, func(ctx context.Context, q *sqlc.Queries, _ pgx.Tx) error {
		var err error
		n, err = q.CountRunsByWorkflow(ctx, sqlc.CountRunsByWorkflowParams{OrgID: orgID, WorkflowID: &workflowID})
		return err
	})
	return n, err
}

// -------------------------------------------------------------------- rules

func toRule(r sqlc.Rule) rules.Rule {
	out := rules.Rule{
		ID: r.ID, OrgID: r.OrgID, Title: r.Title, Body: r.Body, Version: int(r.Version), Status: r.Status, Scope: r.Scope, RunID: r.RunID,
		CreatedBy: r.CreatedBy, Enabled: r.Enabled, UseCount: r.UseCount, LastUsedAt: r.LastUsedAt, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
	if len(r.AppliesTo) > 0 && string(r.AppliesTo) != "{}" {
		_ = json.Unmarshal(r.AppliesTo, &out.AppliesTo)
	}
	if len(r.Compiled) > 0 && string(r.Compiled) != "null" {
		_ = json.Unmarshal(r.Compiled, &out.Compiled)
	}
	if len(r.Stats) > 0 {
		_ = json.Unmarshal(r.Stats, &out.Stats)
	}
	if len(r.Provenance) > 0 {
		_ = json.Unmarshal(r.Provenance, &out.Provenance)
	}
	return out
}

// CreateRule implements rules.Store.
func (s *Store) CreateRule(ctx context.Context, r rules.Rule) (rules.Rule, error) {
	var out rules.Rule
	err := s.org(ctx, r.OrgID, func(ctx context.Context, q *sqlc.Queries, _ pgx.Tx) error {
		source := "manual"
		if r.Provenance.Origin == rules.OriginModel {
			source = "learned"
		}
		appliesTo := jsonOrNil(r.AppliesTo)
		if appliesTo == nil {
			appliesTo = []byte("{}")
		}
		row, err := q.InsertRuleFull(ctx, sqlc.InsertRuleFullParams{
			OrgID: r.OrgID, Title: r.Title, Body: r.Body, Kind: "match", Source: source, CreatedBy: r.CreatedBy, Version: int32(max(r.Version, 1)), //nolint:gosec // small
			Status: r.Status, Scope: r.Scope, AppliesTo: appliesTo, Compiled: jsonOrNil(r.Compiled), Provenance: nonEmpty(jsonOrNil(r.Provenance)), RunID: r.RunID,
		})
		if err != nil {
			return err
		}
		out = toRule(row)
		return nil
	})
	return out, err
}

// GetRule implements rules.Store.
func (s *Store) GetRule(ctx context.Context, orgID, id uuid.UUID) (rules.Rule, bool, error) {
	var out rules.Rule
	found := false
	err := s.org(ctx, orgID, func(ctx context.Context, q *sqlc.Queries, _ pgx.Tx) error {
		row, err := q.GetRule(ctx, sqlc.GetRuleParams{OrgID: orgID, ID: id})
		if isNoRows(err) {
			return nil
		}
		if err != nil {
			return err
		}
		out, found = toRule(row), true
		return nil
	})
	return out, found, err
}

// ListRulesByIDs implements rules.Store.
func (s *Store) ListRulesByIDs(ctx context.Context, orgID uuid.UUID, ids []uuid.UUID) ([]rules.Rule, error) {
	out := []rules.Rule{}
	if len(ids) == 0 {
		return out, nil
	}
	err := s.org(ctx, orgID, func(ctx context.Context, q *sqlc.Queries, _ pgx.Tx) error {
		rows, err := q.ListRulesByIDs(ctx, sqlc.ListRulesByIDsParams{OrgID: orgID, Ids: ids})
		if err != nil {
			return err
		}
		for _, r := range rows {
			out = append(out, toRule(r))
		}
		return nil
	})
	return out, err
}

// SearchRules implements rules.Store.
func (s *Store) SearchRules(ctx context.Context, orgID uuid.UUID, qry rules.Query) ([]rules.Rule, error) {
	out := []rules.Rule{}
	err := s.org(ctx, orgID, func(ctx context.Context, q *sqlc.Queries, _ pgx.Tx) error {
		rows, err := q.SearchRulesPage(ctx, sqlc.SearchRulesPageParams{
			OrgID: orgID, Limit: int32(min(max(qry.Limit, 1), 1000)), Offset: int32(max(qry.Offset, 0)), //nolint:gosec // bounded
			Status: strPtr(qry.Status), Scope: strPtr(qry.Scope), Term: strPtr(qry.Term),
		})
		if err != nil {
			return err
		}
		for _, r := range rows {
			out = append(out, toRule(r))
		}
		return nil
	})
	return out, err
}

// ListTrustedRules implements rules.Store.
func (s *Store) ListTrustedRules(ctx context.Context, orgID uuid.UUID) ([]rules.Rule, error) {
	out := []rules.Rule{}
	err := s.org(ctx, orgID, func(ctx context.Context, q *sqlc.Queries, _ pgx.Tx) error {
		rows, err := q.ListTrustedRules(ctx, orgID)
		if err != nil {
			return err
		}
		for _, r := range rows {
			out = append(out, toRule(r))
		}
		return nil
	})
	return out, err
}

// UpdateRuleVersion implements rules.Store.
func (s *Store) UpdateRuleVersion(ctx context.Context, r rules.Rule) (rules.Rule, error) {
	var out rules.Rule
	err := s.org(ctx, r.OrgID, func(ctx context.Context, q *sqlc.Queries, _ pgx.Tx) error {
		row, err := q.UpdateRuleVersion(ctx, sqlc.UpdateRuleVersionParams{OrgID: r.OrgID, ID: r.ID, Title: r.Title, Body: r.Body, Version: int32(max(r.Version, 1)), Status: r.Status, Compiled: jsonOrNil(r.Compiled)}) //nolint:gosec // small
		if err != nil {
			return err
		}
		out = toRule(row)
		return nil
	})
	return out, err
}

// PromoteRule implements rules.Store.
func (s *Store) PromoteRule(ctx context.Context, orgID, id uuid.UUID, status string) (rules.Rule, error) {
	var out rules.Rule
	err := s.org(ctx, orgID, func(ctx context.Context, q *sqlc.Queries, _ pgx.Tx) error {
		row, err := q.PromoteRule(ctx, sqlc.PromoteRuleParams{OrgID: orgID, ID: id, Status: status})
		if err != nil {
			return err
		}
		out = toRule(row)
		return nil
	})
	return out, err
}

// InsertVersion implements rules.Store.
func (s *Store) InsertVersion(ctx context.Context, v rules.Version) (rules.Version, error) {
	err := s.org(ctx, v.OrgID, func(ctx context.Context, q *sqlc.Queries, _ pgx.Tx) error {
		row, err := q.InsertRuleVersion(ctx, sqlc.InsertRuleVersionParams{OrgID: v.OrgID, RuleID: v.RuleID, Version: int32(max(v.Version, 1)), Title: v.Title, Body: v.Body, Compiled: jsonOrNil(v.Compiled), Status: v.Status, CreatedBy: v.CreatedBy}) //nolint:gosec // small
		if err != nil {
			return err
		}
		v.ID, v.CreatedAt = row.ID, row.CreatedAt
		return nil
	})
	return v, err
}

// TouchRules implements rules.Store.
func (s *Store) TouchRules(ctx context.Context, orgID uuid.UUID, ids []uuid.UUID) error {
	if len(ids) == 0 {
		return nil
	}
	return s.org(ctx, orgID, func(ctx context.Context, q *sqlc.Queries, _ pgx.Tx) error {
		return q.TouchRules(ctx, sqlc.TouchRulesParams{OrgID: orgID, Ids: ids})
	})
}
