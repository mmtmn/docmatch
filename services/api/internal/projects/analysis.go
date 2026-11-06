package projects

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	contracts "github.com/mmtmn/docmatch/packages/contracts/gen"
	"github.com/mmtmn/docmatch/services/api/internal/config"
	"github.com/mmtmn/docmatch/services/api/internal/db"
	"github.com/mmtmn/docmatch/services/api/internal/entitlement"
	"github.com/mmtmn/docmatch/services/api/internal/estimate"
	"github.com/mmtmn/docmatch/services/api/internal/publicid"
	"github.com/mmtmn/docmatch/services/api/internal/rules"
	"github.com/mmtmn/docmatch/services/api/internal/runs"
	"github.com/mmtmn/docmatch/services/api/internal/runs/apix"
	"github.com/mmtmn/docmatch/services/api/internal/uploads"
)

type Engine struct {
	Config    *config.Config
	Estimates *estimate.Service
	Runs      *runs.Service
	Rules     *rules.Service
	Uploads   *uploads.Service
}

// SetEngine binds the pipeline before the API starts accepting requests.
func (s *Service) SetEngine(e Engine) {
	s.engine = &e
	s.uploads = e.Uploads
	e.Estimates.SetAccessCheck(s.CheckAnalysisAccess)
}

type analysisKey struct{}
type analysisContext struct {
	org, project uuid.UUID
	signature    string
	revision     int64
}

// CheckAnalysisAccess also protects legacy endpoints. Project documents must
// always pass the coordinator; otherwise an old SDK could bypass deduplication
// or create a result from someone else's private document.
func (s *Service) CheckAnalysisAccess(ctx context.Context, p apix.Principal, req estimate.Request) error {
	if a, ok := ctx.Value(analysisKey{}).(analysisContext); ok {
		if a.org == p.OrgID && req.ProjectID == publicid.Encode(Prefix, a.project) && req.AnalysisSignature == a.signature && req.ProjectRevision == a.revision {
			return nil
		}
		return apix.ErrForbidden
	}
	if req.ProjectID != "" || req.AnalysisSignature != "" || req.ProjectRevision != 0 {
		return apix.ErrForbidden
	}
	ids := []uuid.UUID{}
	for _, sources := range [][]estimate.SourceRef{req.Sources, req.MediaSources} {
		for _, source := range sources {
			if id, err := publicid.Decode("upl", source.UploadID); err == nil {
				ids = append(ids, id)
			}
		}
	}
	return db.WithOrgTx(ctx, s.pool, p.OrgID, func(ctx context.Context, tx pgx.Tx) error {
		var protected bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM project_documents WHERE org_id=$1 AND upload_id=ANY($2::uuid[]))`, p.OrgID, ids).Scan(&protected); err != nil {
			return err
		}
		if protected {
			return apix.E(contracts.ErrConflict, nil).With(map[string]any{"reason": "Concilie estes documentos pelo projeto em que foram salvos."})
		}
		return nil
	})
}

type analysisSource struct {
	Hash string `json:"hash"`
	Kind string `json:"kind"`
	Role string `json:"role"`
}
type analysisRule struct {
	ID        uuid.UUID        `json:"id"`
	Version   int              `json:"version"`
	Body      string           `json:"body"`
	Compiled  *rules.Compiled  `json:"compiled"`
	AppliesTo *rules.AppliesTo `json:"applies_to"`
}

func signature(sources []analysisSource, settings Settings, resolved []rules.Rule) (string, error) {
	// Sort by content, preserving intentional duplicates. IDs and display order
	// are not evidence of a new analysis; rule usage statistics are not either.
	sort.Slice(sources, func(i, j int) bool {
		a, b := sources[i], sources[j]
		if a.Hash != b.Hash {
			return a.Hash < b.Hash
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.Role < b.Role
	})
	ruleVersions := make([]analysisRule, 0, len(resolved))
	for _, r := range resolved {
		ruleVersions = append(ruleVersions, analysisRule{r.ID, r.Version, r.Body, r.Compiled, r.AppliesTo})
	}
	sort.Slice(ruleVersions, func(i, j int) bool { return ruleVersions[i].ID.String() < ruleVersions[j].ID.String() })
	settings.RuleIDs = nil // effective rules above, not their visual selection order
	data, err := json.Marshal(struct {
		Version  int              `json:"version"`
		Sources  []analysisSource `json:"sources"`
		Settings Settings         `json:"settings"`
		Rules    []analysisRule   `json:"rules"`
	}{1, sources, settings, ruleVersions})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func (s *Service) snapshot(ctx context.Context, tx pgx.Tx, p apix.Principal, id uuid.UUID, revision int64, lane string) (estimate.Request, *runs.ProjectSnapshot, error) {
	v, _, err := access(ctx, tx, p, id, true)
	if err != nil {
		return estimate.Request{}, nil, err
	}
	if revision != v.Revision {
		return estimate.Request{}, nil, conflict()
	}
	settings := *v.Settings
	useRules := settings.UseRules
	req := estimate.Request{Sources: []estimate.SourceRef{}, RulesText: settings.RulesText, ProjectID: v.ID, ProjectRevision: v.Revision, Options: &estimate.Options{UseRules: &useRules, RuleIDs: settings.RuleIDs, LlmMode: settings.LLMMode, WebSearch: settings.WebSearch, Lane: lane}}
	rows, err := tx.Query(ctx, `SELECT u.id,u.sha256,u.kind,COALESCE(NULLIF(d.role,''),u.role,''),
 u.status='clean' AND u.deleted_at IS NULL AND (u.expires_at IS NULL OR u.expires_at>now())
 FROM project_documents d JOIN uploads u ON u.org_id=d.org_id AND u.id=d.upload_id
 WHERE d.org_id=$1 AND d.project_id=$2 AND d.removed_at IS NULL AND d.pending_replacement_id IS NULL ORDER BY d.created_at,d.id`, p.OrgID, id)
	if err != nil {
		return req, nil, err
	}
	content := []analysisSource{}
	for rows.Next() {
		var upload uuid.UUID
		var hash []byte
		var kind, role string
		var ready bool
		if err := rows.Scan(&upload, &hash, &kind, &role, &ready); err != nil {
			rows.Close()
			return req, nil, err
		}
		if !ready || len(hash) != sha256.Size {
			rows.Close()
			return req, nil, apix.E(contracts.ErrUploadNotReady, nil).With(map[string]any{"upload_id": publicid.Encode("upl", upload)})
		}
		source := estimate.SourceRef{UploadID: publicid.Encode("upl", upload), Role: role}
		if kind == string(uploads.KindMedia) {
			req.MediaSources = append(req.MediaSources, source)
		} else {
			req.Sources = append(req.Sources, source)
		}
		content = append(content, analysisSource{hex.EncodeToString(hash), kind, role})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return req, nil, err
	}
	ids := []uuid.UUID{}
	for _, value := range settings.RuleIDs {
		rid, err := publicid.Decode("rul", value)
		if err != nil {
			return req, nil, err
		}
		ids = append(ids, rid)
	}
	resolved, err := s.engine.Rules.Resolve(rules.WithProject(ctx, p.OrgID, id), p.OrgID, settings.UseRules, ids)
	if err != nil {
		return req, nil, err
	}
	digest, err := signature(content, settings, resolved)
	if err != nil {
		return req, nil, err
	}
	req.AnalysisSignature = digest
	return req, &runs.ProjectSnapshot{ID: id, Revision: v.Revision, Signature: digest, Rules: resolved}, nil
}

func (s *Service) existing(ctx context.Context, tx pgx.Tx, p apix.Principal, snapshot *runs.ProjectSnapshot) (*runs.Run, bool, error) {
	var id uuid.UUID
	var expired bool
	err := tx.QueryRow(ctx, `SELECT r.id,COALESCE(r.expires_at<=now(),false) FROM project_runs pr JOIN runs r ON r.org_id=pr.org_id AND r.id=pr.run_id
 WHERE pr.org_id=$1 AND pr.project_id=$2 AND pr.signature=$3 AND r.status IN ('queued','running','needs_approval','completed')
 ORDER BY pr.created_at DESC,r.id DESC LIMIT 1`, p.OrgID, snapshot.ID, snapshot.Signature).Scan(&id, &expired)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	r, err := s.engine.Runs.Get(ctx, p.OrgID, id)
	if err != nil {
		return nil, false, err
	}
	return &r, expired && r.Status == runs.StatusCompleted, nil
}

type AnalysisRequest struct {
	Revision        int64  `json:"revision"`
	Lane            string `json:"lane,omitempty"`
	ReprocessReason string `json:"reprocess_reason,omitempty"`
	QuoteID         string `json:"quote_id,omitempty"`
	QuoteSignature  string `json:"quote_signature,omitempty"`
}
type Analysis struct {
	State    string          `json:"state"`
	Revision int64           `json:"revision"`
	Quote    *estimate.Quote `json:"quote,omitempty"`
	Run      *runs.View      `json:"run,omitempty"`
}

func (s *Service) paid(ctx context.Context, tx pgx.Tx, p apix.Principal) error {
	_, eligible, err := entitlement.Paid(ctx, tx, s.engine.Config, p.OrgID)
	if err != nil {
		return err
	}
	if !eligible {
		return apix.E(contracts.ErrSubscriptionRequired, nil)
	}
	return nil
}

// Analyze estimates or creates using a single account transaction. The project
// lock precedes the existing-result check, and the credit reservation, run and
// queue job all join that same transaction. A crash cannot leave a second hold.
func (s *Service) Analyze(ctx context.Context, p apix.Principal, id uuid.UUID, in AnalysisRequest, create bool) (Analysis, error) {
	if s.engine == nil {
		return Analysis{}, apix.E(contracts.ErrServiceUnavailable, nil)
	}
	var out Analysis
	err := db.WithOrgUnit(ctx, s.pool, p.OrgID, func(ctx context.Context, tx pgx.Tx) error {
		req, snapshot, err := s.snapshot(ctx, tx, p, id, in.Revision, in.Lane)
		if err != nil {
			return err
		}
		out.Revision = snapshot.Revision
		previous, expired, err := s.existing(ctx, tx, p, snapshot)
		if err != nil {
			return err
		}
		if previous != nil && !expired {
			view := s.engine.Runs.View(ctx, *previous)
			out.Run = &view
			out.State = string(previous.Status)
			if previous.Status == runs.StatusCompleted {
				out.State = "unchanged"
			}
			return nil
		}
		if expired && strings.TrimSpace(in.ReprocessReason) == "" {
			out.State = "expired"
			return nil
		}
		if utf8.RuneCountInString(in.ReprocessReason) > contracts.TextReprocessReason {
			return apix.Validation(apix.FieldError{Path: "/reprocess_reason", Code: "too_long", Max: contracts.TextReprocessReason})
		}
		if expired {
			snapshot.ReprocessReason = strings.TrimSpace(in.ReprocessReason)
		}
		if err := s.paid(ctx, tx, p); err != nil {
			return err
		}
		ctx = context.WithValue(ctx, analysisKey{}, analysisContext{p.OrgID, id, snapshot.Signature, snapshot.Revision})
		if !create {
			q, err := s.engine.Estimates.Estimate(ctx, p, req, func(u uploads.Upload) string { return estimate.SourceURI(u, s.uploads.StoreFor(u).URI) })
			if err != nil {
				return err
			}
			out.State = "ready"
			out.Quote = &q
			return nil
		}
		run, err := s.engine.Runs.Create(ctx, p, runs.CreateRequest{Request: req, QuoteID: in.QuoteID, QuoteSignature: in.QuoteSignature, Project: snapshot})
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO project_runs(org_id,project_id,run_id,revision,signature) VALUES($1,$2,$3,$4,$5)`, p.OrgID, id, run.ID, snapshot.Revision, snapshot.Signature); err != nil {
			return err
		}
		if err := event(ctx, tx, p, id, "analysis.created"); err != nil {
			return err
		}
		view := s.engine.Runs.View(ctx, run)
		out.Run = &view
		out.State = string(run.Status)
		return nil
	})
	return out, err
}
