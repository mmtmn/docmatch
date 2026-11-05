// Package workflows stores reusable reconciliation configurations
// (schemas/workflow-config.json) and serves their CRUD. A run created with
// workflow_id inherits the configuration; the harness receives it through
// RunOptions.workflow_id and the resolved rules.
package workflows

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/google/uuid"

	contracts "github.com/mmtmn/docmatch/packages/contracts/gen"
	"github.com/mmtmn/docmatch/services/api/internal/estimate"
	"github.com/mmtmn/docmatch/services/api/internal/httpx"
	"github.com/mmtmn/docmatch/services/api/internal/limits"
	"github.com/mmtmn/docmatch/services/api/internal/runs/apix"
)

// Enumerations of the schema.
var (
	sourceKinds   = set("document", "table", "media", "email", "archive", "any")
	matchingKeys  = set("amount", "date", "document_number", "counterparty", "description", "reference", "tax_id", "account", "period")
	strategies    = set("exact", "tolerance", "sum", "fuzzy", "rules", "model")
	llmModes      = set("openrouter", "local", "off")
	lanes         = set("app", "api_batch", "api_standard", "api_priority")
	outputFormats = set("xlsx", "csv", "pdf")
	roleRe        = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)
	acceptRe      = regexp.MustCompile(`^([a-z0-9!#$&^_.+-]+/[a-z0-9!#$&^_.+*-]+|\.[a-z0-9]{1,10})$`)
	manifestRe    = regexp.MustCompile(`^cst_[0-9A-HJKMNP-TV-Z]{26}$`)
)

func set(vals ...string) map[string]bool {
	m := make(map[string]bool, len(vals))
	for _, v := range vals {
		m[v] = true
	}
	return m
}

// SourceRole mirrors $defs/SourceRole.
type SourceRole struct {
	Role     string          `json:"role"`
	Label    string          `json:"label,omitempty"`
	Kind     string          `json:"kind"`
	Required *bool           `json:"required,omitempty"`
	Multiple *bool           `json:"multiple,omitempty"`
	Accept   []string        `json:"accept,omitempty"`
	Hints    *estimate.Hints `json:"hints,omitempty"`
}

// Tolerances mirrors matching.tolerances.
type Tolerances struct {
	AmountCentavos *int64   `json:"amount_centavos,omitempty"`
	AmountPercent  *float64 `json:"amount_percent,omitempty"`
	Days           *int     `json:"days,omitempty"`
	TextSimilarity *float64 `json:"text_similarity,omitempty"`
}

// Matching mirrors $defs/Matching.
type Matching struct {
	Keys       []string    `json:"keys"`
	Tolerances *Tolerances `json:"tolerances,omitempty"`
	Strategies []string    `json:"strategies,omitempty"`
	OneToMany  *bool       `json:"one_to_many,omitempty"`
}

// Rules mirrors $defs/Rules.
type Rules struct {
	RuleIDs      []string `json:"rule_ids,omitempty"`
	Text         string   `json:"text,omitempty"`
	AutoLearning *bool    `json:"auto_learning,omitempty"`
}

// LLM mirrors properties.llm.
type LLM struct {
	Mode      string `json:"mode,omitempty"`
	WebSearch *bool  `json:"web_search,omitempty"`
}

// Budget mirrors properties.budget.
type Budget struct {
	DefaultCredits         *int64 `json:"default_credits,omitempty"`
	AutoApproveUpToCredits *int64 `json:"auto_approve_up_to_credits,omitempty"`
	Lane                   string `json:"lane,omitempty"`
}

// Outputs mirrors properties.outputs.
type Outputs struct {
	Formats      []string `json:"formats,omitempty"`
	UIManifestID string   `json:"ui_manifest_id,omitempty"`
}

// Config mirrors schemas/workflow-config.json.
type Config struct {
	Name     string       `json:"name"`
	Sources  []SourceRole `json:"sources"`
	Matching *Matching    `json:"matching"`
	Rules    *Rules       `json:"rules,omitempty"`
	LLM      *LLM         `json:"llm,omitempty"`
	Budget   *Budget      `json:"budget,omitempty"`
	Outputs  *Outputs     `json:"outputs,omitempty"`
	Version  *int         `json:"version,omitempty"`
}

// Validate applies the schema's constraints and the text limits. It
// returns the decoded rule ids referenced by the config.
func (c *Config) Validate(l *limits.Limits) ([]uuid.UUID, *apix.Error) {
	var fields []apix.FieldError
	add := func(path, code string) { fields = append(fields, apix.FieldError{Path: path, Code: code}) }

	name, err := httpx.ValidateText(c.Name, l.Text.WorkflowName, true)
	switch {
	case err != nil:
		add("/name", "invalid")
	case name == "":
		add("/name", "required")
	default:
		c.Name = name
	}
	if len(c.Sources) == 0 {
		add("/sources", "min_items")
	}
	roles := map[string]bool{}
	for i, s := range c.Sources {
		p := fmt.Sprintf("/sources/%d", i)
		if !roleRe.MatchString(s.Role) {
			add(p+"/role", "invalid")
		} else if roles[s.Role] {
			add(p+"/role", "duplicate")
		}
		roles[s.Role] = true
		if !sourceKinds[s.Kind] {
			add(p+"/kind", "invalid")
		}
		for j, a := range s.Accept {
			if !acceptRe.MatchString(a) {
				add(fmt.Sprintf("%s/accept/%d", p, j), "invalid")
			}
		}
		if s.Label != "" {
			if label, err := httpx.ValidateText(s.Label, l.Text.WorkflowName, true); err != nil {
				add(p+"/label", "invalid")
			} else {
				c.Sources[i].Label = label
			}
		}
	}
	if c.Matching == nil || len(c.Matching.Keys) == 0 {
		add("/matching/keys", "min_items")
	} else {
		seen := map[string]bool{}
		for i, k := range c.Matching.Keys {
			if !matchingKeys[k] || seen[k] {
				add(fmt.Sprintf("/matching/keys/%d", i), "invalid")
			}
			seen[k] = true
		}
		for i, s := range c.Matching.Strategies {
			if !strategies[s] {
				add(fmt.Sprintf("/matching/strategies/%d", i), "invalid")
			}
		}
		if t := c.Matching.Tolerances; t != nil {
			if t.AmountCentavos != nil && *t.AmountCentavos < 0 {
				add("/matching/tolerances/amount_centavos", "min")
			}
			if t.AmountPercent != nil && (*t.AmountPercent < 0 || *t.AmountPercent > 100) {
				add("/matching/tolerances/amount_percent", "out_of_range")
			}
			if t.Days != nil && *t.Days < 0 {
				add("/matching/tolerances/days", "min")
			}
			if t.TextSimilarity != nil && (*t.TextSimilarity < 0 || *t.TextSimilarity > 1) {
				add("/matching/tolerances/text_similarity", "out_of_range")
			}
		}
	}
	var ruleIDs []uuid.UUID
	if r := c.Rules; r != nil {
		seen := map[uuid.UUID]bool{}
		for i, id := range r.RuleIDs {
			u, err := apix.Decode(apix.PrefixRule, id)
			if err != nil || seen[u] {
				add(fmt.Sprintf("/rules/rule_ids/%d", i), "invalid")
				continue
			}
			seen[u] = true
			ruleIDs = append(ruleIDs, u)
		}
		if r.Text != "" {
			text, err := httpx.ValidateText(r.Text, l.Text.RulesText, false)
			if err != nil {
				if errors.Is(err, httpx.ErrTooLong) {
					return nil, apix.E(contracts.ErrTextTooLong, err).With(map[string]any{"path": "/rules/text", "max": l.Text.RulesText})
				}
				add("/rules/text", "invalid")
			} else {
				c.Rules.Text = text
			}
		}
	}
	if c.LLM != nil && c.LLM.Mode != "" && !llmModes[c.LLM.Mode] {
		add("/llm/mode", "invalid")
	}
	if b := c.Budget; b != nil {
		if b.DefaultCredits != nil && *b.DefaultCredits < 0 {
			add("/budget/default_credits", "min")
		}
		if b.AutoApproveUpToCredits != nil && *b.AutoApproveUpToCredits < 0 {
			add("/budget/auto_approve_up_to_credits", "min")
		}
		if b.Lane != "" && !lanes[b.Lane] {
			add("/budget/lane", "invalid")
		}
	}
	if o := c.Outputs; o != nil {
		seen := map[string]bool{}
		for i, f := range o.Formats {
			if !outputFormats[f] || seen[f] {
				add(fmt.Sprintf("/outputs/formats/%d", i), "invalid")
			}
			seen[f] = true
		}
		if o.UIManifestID != "" && !manifestRe.MatchString(o.UIManifestID) {
			add("/outputs/ui_manifest_id", "invalid")
		}
	}
	if c.Version != nil && *c.Version < 1 {
		add("/version", "min")
	}
	if len(fields) > 0 {
		return nil, apix.Validation(fields...)
	}
	return ruleIDs, nil
}

// Patch mirrors openapi WorkflowPatch: present sections replace the current ones.
type Patch struct {
	Name     *string       `json:"name,omitempty"`
	Sources  *[]SourceRole `json:"sources,omitempty"`
	Matching *Matching     `json:"matching,omitempty"`
	Rules    *Rules        `json:"rules,omitempty"`
	LLM      *LLM          `json:"llm,omitempty"`
	Budget   *Budget       `json:"budget,omitempty"`
	Outputs  *Outputs      `json:"outputs,omitempty"`
}

// Empty reports whether no section was sent (minProperties: 1).
func (p Patch) Empty() bool {
	return p.Name == nil && p.Sources == nil && p.Matching == nil && p.Rules == nil && p.LLM == nil && p.Budget == nil && p.Outputs == nil
}

// Apply replaces the sent sections of c.
func (p Patch) Apply(c *Config) {
	if p.Name != nil {
		c.Name = *p.Name
	}
	if p.Sources != nil {
		c.Sources = *p.Sources
	}
	if p.Matching != nil {
		c.Matching = p.Matching
	}
	if p.Rules != nil {
		c.Rules = p.Rules
	}
	if p.LLM != nil {
		c.LLM = p.LLM
	}
	if p.Budget != nil {
		c.Budget = p.Budget
	}
	if p.Outputs != nil {
		c.Outputs = p.Outputs
	}
}

// Workflow is the stored record.
type Workflow struct {
	ID        uuid.UUID
	OrgID     uuid.UUID
	Name      string
	Config    json.RawMessage
	CreatedBy *uuid.UUID
	CreatedAt time.Time
	UpdatedAt time.Time
}

// PublicID renders the API identifier.
func (w Workflow) PublicID() string { return apix.Encode(apix.PrefixWorkflow, w.ID) }

// View is openapi Workflow.
type View struct {
	ID        string          `json:"id"`
	OrgID     string          `json:"org_id"`
	Config    json.RawMessage `json:"config"`
	RunsCount int64           `json:"runs_count"`
	CreatedAt string          `json:"created_at"`
	UpdatedAt string          `json:"updated_at"`
}

// View renders the workflow.
func (w Workflow) View(runs int64) View {
	return View{
		ID: w.PublicID(), OrgID: apix.Encode(apix.PrefixOrg, w.OrgID), Config: w.Config, RunsCount: runs,
		CreatedAt: w.CreatedAt.UTC().Format(time.RFC3339), UpdatedAt: w.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

// Store is the persistence boundary.
type Store interface {
	CreateWorkflow(ctx context.Context, w Workflow) (Workflow, error)
	GetWorkflow(ctx context.Context, orgID, id uuid.UUID) (Workflow, bool, error)
	ListWorkflows(ctx context.Context, orgID uuid.UUID, limit, offset int) ([]Workflow, error)
	UpdateWorkflow(ctx context.Context, w Workflow) (Workflow, error)
	CountRuns(ctx context.Context, orgID, workflowID uuid.UUID) (int64, error)
}
