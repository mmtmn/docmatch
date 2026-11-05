// Package rules stores reconciliation rules as executable hypotheses
// (schemas/rule.json): natural-language body, versions, the lifecycle
// draft → candidate → verified → trusted → retired, trigram search over
// title and body for the picker, and the conversion to the harness
// message so runs receive the resolved rules.
package rules

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	pb "github.com/mmtmn/docmatch/services/api/internal/harness/pb"
	"github.com/mmtmn/docmatch/services/api/internal/runs/apix"
)

// Statuses (openapi RuleStatus).
const (
	StatusDraft     = "draft"
	StatusCandidate = "candidate"
	StatusVerified  = "verified"
	StatusTrusted   = "trusted"
	StatusRetired   = "retired"
)

// Scopes (openapi RuleScope).
const (
	ScopeTenant = "tenant"
	ScopeGlobal = "global"
)

// Origins (rule.json provenance.origin).
const (
	OriginUser   = "user"
	OriginModel  = "model"
	OriginImport = "import"
	OriginAdmin  = "admin"
)

// Stats mirrors $defs/Stats.
type Stats struct {
	RunsEvaluated   int64  `json:"runs_evaluated"`
	TruePositives   int64  `json:"true_positives"`
	FalsePositives  int64  `json:"false_positives"`
	ExplainedAmount int64  `json:"explained_amount"`
	Currency        string `json:"currency,omitempty"`
	LastEvaluatedAt string `json:"last_evaluated_at,omitempty"`
}

// Provenance mirrors $defs/Provenance.
type Provenance struct {
	Origin     string `json:"origin"`
	Model      string `json:"model,omitempty"`
	RunID      string `json:"run_id,omitempty"`
	ProposedAt string `json:"proposed_at"`
	AcceptedBy string `json:"accepted_by,omitempty"`
	AcceptedAt string `json:"accepted_at,omitempty"`
}

// Compiled mirrors $defs/Compiled.
type Compiled struct {
	Language   string         `json:"language"`
	Text       string         `json:"text"`
	Hash       string         `json:"hash"`
	Parameters map[string]any `json:"parameters,omitempty"`
	CompiledBy string         `json:"compiled_by,omitempty"`
	CompiledAt string         `json:"compiled_at"`
}

// AppliesTo mirrors properties.applies_to.
type AppliesTo struct {
	SourceKinds []string `json:"source_kinds,omitempty"`
	WorkflowIDs []string `json:"workflow_ids,omitempty"`
}

// Rule is the stored record (current version).
type Rule struct {
	ID         uuid.UUID
	OrgID      uuid.UUID
	Title      string
	Body       string
	Version    int
	Status     string
	Scope      string
	AppliesTo  *AppliesTo
	Compiled   *Compiled
	Stats      Stats
	Provenance Provenance
	RunID      *uuid.UUID
	CreatedBy  *uuid.UUID
	Enabled    bool
	UseCount   int64
	LastUsedAt *time.Time
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// PublicID renders the API identifier.
func (r Rule) PublicID() string { return apix.Encode(apix.PrefixRule, r.ID) }

// View is openapi Rule.
type View struct {
	ID         string     `json:"id"`
	TenantID   *string    `json:"tenant_id"`
	Scope      string     `json:"scope"`
	Title      string     `json:"title"`
	Body       string     `json:"body"`
	Version    int        `json:"version"`
	Status     string     `json:"status"`
	Compiled   *Compiled  `json:"compiled"`
	AppliesTo  *AppliesTo `json:"applies_to,omitempty"`
	Stats      Stats      `json:"stats"`
	Provenance Provenance `json:"provenance"`
	CreatedAt  string     `json:"created_at"`
	UpdatedAt  string     `json:"updated_at"`
}

// View renders the rule.
func (r Rule) View() View {
	v := View{
		ID: r.PublicID(), Scope: r.Scope, Title: r.Title, Body: r.Body, Version: r.Version, Status: r.Status,
		Compiled: r.Compiled, AppliesTo: r.AppliesTo, Stats: r.Stats, Provenance: r.Provenance,
		CreatedAt: r.CreatedAt.UTC().Format(time.RFC3339), UpdatedAt: r.UpdatedAt.UTC().Format(time.RFC3339),
	}
	if r.Scope != ScopeGlobal {
		t := apix.Encode(apix.PrefixOrg, r.OrgID)
		v.TenantID = &t
	}
	if v.Stats.Currency == "" {
		v.Stats.Currency = "BRL"
	}
	if v.Provenance.Origin == "" {
		v.Provenance.Origin = OriginUser
	}
	if v.Provenance.ProposedAt == "" {
		v.Provenance.ProposedAt = v.CreatedAt
	}
	return v
}

// Version is one row of rule_versions.
type Version struct {
	ID        uuid.UUID
	OrgID     uuid.UUID
	RuleID    uuid.UUID
	Version   int
	Title     string
	Body      string
	Compiled  *Compiled
	Status    string
	CreatedBy *uuid.UUID
	CreatedAt time.Time
}

// Query filters a search.
type Query struct {
	Term   string
	Status string
	Scope  string
	Limit  int
	Offset int
}

// Store is the persistence boundary.
type Store interface {
	CreateRule(ctx context.Context, r Rule) (Rule, error)
	GetRule(ctx context.Context, orgID, id uuid.UUID) (Rule, bool, error)
	ListRulesByIDs(ctx context.Context, orgID uuid.UUID, ids []uuid.UUID) ([]Rule, error)
	SearchRules(ctx context.Context, orgID uuid.UUID, q Query) ([]Rule, error)
	ListTrustedRules(ctx context.Context, orgID uuid.UUID) ([]Rule, error)
	UpdateRuleVersion(ctx context.Context, r Rule) (Rule, error)
	PromoteRule(ctx context.Context, orgID, id uuid.UUID, status string) (Rule, error)
	InsertVersion(ctx context.Context, v Version) (Version, error)
	TouchRules(ctx context.Context, orgID uuid.UUID, ids []uuid.UUID) error
}

// ToProto renders a rule for the harness.
func ToProto(r Rule) *pb.Rule {
	out := &pb.Rule{
		Id: r.PublicID(), TenantId: apix.Encode(apix.PrefixOrg, r.OrgID), Scope: scopeProto(r.Scope), Title: r.Title, Body: r.Body,
		Version: uint32(max(r.Version, 0)), Status: statusProto(r.Status), //nolint:gosec // version is small and positive
		Stats: &pb.RuleStats{
			RunsEvaluated: uint64(max(r.Stats.RunsEvaluated, 0)), TruePositives: uint64(max(r.Stats.TruePositives, 0)), //nolint:gosec // non-negative
			FalsePositives: uint64(max(r.Stats.FalsePositives, 0)), ExplainedAmount: r.Stats.ExplainedAmount, Currency: r.Stats.Currency, //nolint:gosec // non-negative
		},
		Provenance: &pb.RuleProvenance{Origin: originProto(r.Provenance.Origin), Model: r.Provenance.Model, RunId: r.Provenance.RunID, AcceptedBy: r.Provenance.AcceptedBy},
		CreatedAt:  timestamppb.New(r.CreatedAt), UpdatedAt: timestamppb.New(r.UpdatedAt),
	}
	if c := r.Compiled; c != nil {
		out.Compiled = &pb.CompiledRule{Language: c.Language, Text: c.Text, Hash: c.Hash, CompiledBy: c.CompiledBy}
		if len(c.Parameters) > 0 {
			if s, err := structpb.NewStruct(c.Parameters); err == nil {
				out.Compiled.Parameters = s
			}
		}
		if t, err := time.Parse(time.RFC3339, c.CompiledAt); err == nil {
			out.Compiled.CompiledAt = timestamppb.New(t)
		}
	}
	return out
}

// FromProto converts a rule proposed by the harness into a candidate of
// the organization (ids and timestamps are assigned by the store).
func FromProto(orgID uuid.UUID, runID *uuid.UUID, now time.Time, r *pb.Rule) Rule {
	out := Rule{
		OrgID: orgID, Title: r.GetTitle(), Body: r.GetBody(), Version: 1, Status: StatusCandidate, Scope: ScopeTenant,
		Enabled: true, RunID: runID, CreatedAt: now, UpdatedAt: now,
		Provenance: Provenance{Origin: OriginModel, Model: r.GetProvenance().GetModel(), ProposedAt: now.UTC().Format(time.RFC3339)},
	}
	if runID != nil {
		out.Provenance.RunID = apix.Encode(apix.PrefixRun, *runID)
	}
	if st := r.GetStats(); st != nil {
		out.Stats = Stats{
			RunsEvaluated: int64(min(st.GetRunsEvaluated(), 1<<62)), TruePositives: int64(min(st.GetTruePositives(), 1<<62)), //nolint:gosec // bounded
			FalsePositives: int64(min(st.GetFalsePositives(), 1<<62)), ExplainedAmount: st.GetExplainedAmount(), Currency: st.GetCurrency(), //nolint:gosec // bounded
		}
	}
	if c := r.GetCompiled(); c != nil && c.GetText() != "" {
		out.Compiled = &Compiled{Language: "sql", Text: c.GetText(), Hash: c.GetHash(), CompiledBy: c.GetCompiledBy(), CompiledAt: now.UTC().Format(time.RFC3339)}
		if c.GetLanguage() != "" {
			out.Compiled.Language = c.GetLanguage()
		}
		if c.GetParameters() != nil {
			out.Compiled.Parameters = c.GetParameters().AsMap()
		}
		if c.GetCompiledAt() != nil {
			out.Compiled.CompiledAt = c.GetCompiledAt().AsTime().UTC().Format(time.RFC3339)
		}
	} else {
		out.Status = StatusDraft
	}
	return out
}

func scopeProto(s string) pb.RuleScope {
	if s == ScopeGlobal {
		return pb.RuleScope_RULE_SCOPE_GLOBAL
	}
	return pb.RuleScope_RULE_SCOPE_TENANT
}

func statusProto(s string) pb.RuleStatus {
	switch s {
	case StatusDraft:
		return pb.RuleStatus_RULE_STATUS_DRAFT
	case StatusCandidate:
		return pb.RuleStatus_RULE_STATUS_CANDIDATE
	case StatusVerified:
		return pb.RuleStatus_RULE_STATUS_VERIFIED
	case StatusTrusted:
		return pb.RuleStatus_RULE_STATUS_TRUSTED
	case StatusRetired:
		return pb.RuleStatus_RULE_STATUS_RETIRED
	}
	return pb.RuleStatus_RULE_STATUS_UNSPECIFIED
}

func originProto(o string) pb.RuleOrigin {
	switch o {
	case OriginModel:
		return pb.RuleOrigin_RULE_ORIGIN_MODEL
	case OriginImport:
		return pb.RuleOrigin_RULE_ORIGIN_IMPORT
	case OriginAdmin:
		return pb.RuleOrigin_RULE_ORIGIN_ADMIN
	}
	return pb.RuleOrigin_RULE_ORIGIN_USER
}

// MarshalJSONB renders optional JSON documents for the store (nil → null).
func MarshalJSONB(v any) []byte {
	if v == nil {
		return nil
	}
	data, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return data
}
