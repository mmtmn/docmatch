package projects

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	contracts "github.com/mmtmn/docmatch/packages/contracts/gen"
	"github.com/mmtmn/docmatch/services/api/internal/db"
	"github.com/mmtmn/docmatch/services/api/internal/estimate"
	"github.com/mmtmn/docmatch/services/api/internal/httpx"
	"github.com/mmtmn/docmatch/services/api/internal/publicid"
	"github.com/mmtmn/docmatch/services/api/internal/runs"
	"github.com/mmtmn/docmatch/services/api/internal/runs/apix"
	"github.com/mmtmn/docmatch/services/api/internal/tenancy"
)

func (s *Service) importRun(w http.ResponseWriter, r *http.Request) {
	p, _, ok := s.principal(w, r)
	if !ok {
		return
	}
	var body struct {
		RunID string `json:"run_id"`
		Name  string `json:"name"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		apix.Handle(w, r, err)
		return
	}
	id, err := publicid.Decode("run", body.RunID)
	if err != nil {
		apix.Handle(w, r, missing())
		return
	}
	v, err := s.ImportRun(r.Context(), p, id, body.Name)
	respond(w, r, v, err)
}

// ImportRun adds an explicit private project around a terminal legacy run. The
// source row serializes repeated requests, including requests with different names.
// No source bytes, expiry, credit ledger, immutable input or job is changed.
func (s *Service) ImportRun(ctx context.Context, p apix.Principal, runID uuid.UUID, name string) (Project, error) {
	if s.engine == nil {
		return Project{}, apix.E(contracts.ErrServiceUnavailable, nil)
	}
	if !s.rollout.Allows(p.OrgID) {
		return Project{}, apix.E(contracts.ErrFeaturePaused, nil)
	}
	if p.APIKeyID != nil && (!slices.Contains(p.Scopes, "runs:read") || !slices.Contains(p.Scopes, "projects:write")) {
		return Project{}, apix.ErrScopeDenied
	}
	name = cleanName(name)
	if !validName(name) {
		return Project{}, apix.Validation(apix.FieldError{Path: "/name", Code: "invalid", Max: contracts.TextProjectName})
	}
	var out Project
	err := db.WithOrgUnit(ctx, s.pool, p.OrgID, func(ctx context.Context, tx pgx.Tx) error {
		role, err := member(ctx, tx, p)
		if err != nil {
			return err
		}
		caps := tenancy.Permissions(role)
		// Invited reconcilers see shared project history, never unassigned old runs.
		if !caps.CreateProject || role == "reconciler" {
			return apix.ErrForbidden
		}
		var owner *uuid.UUID
		var raw []byte
		var status runs.Status
		err = tx.QueryRow(ctx, `SELECT user_id,inputs,status FROM runs WHERE org_id=$1 AND id=$2 FOR UPDATE`, p.OrgID, runID).Scan(&owner, &raw, &status)
		if errors.Is(err, pgx.ErrNoRows) {
			return missing()
		}
		if err != nil {
			return err
		}
		if !caps.ManageAccount && (p.UserID == nil || owner == nil || *p.UserID != *owner) {
			return missing()
		}
		var existing uuid.UUID
		err = tx.QueryRow(ctx, `SELECT project_id FROM project_runs WHERE org_id=$1 AND run_id=$2`, p.OrgID, runID).Scan(&existing)
		if err == nil {
			out, _, err = access(ctx, tx, p, existing, false)
			return err
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if !status.Terminal() {
			return apix.E(contracts.ErrConflict, nil).With(map[string]any{"reason": "Aguarde esta conciliação terminar antes de salvá-la como projeto."})
		}
		var inputs runs.Inputs
		if err = json.Unmarshal(raw, &inputs); err != nil {
			return err
		}
		if inputs.Project != nil {
			return conflict()
		}
		settings := Settings{RulesText: inputs.Request.RulesText, UseRules: inputs.Request.UseRules()}
		if opts := inputs.Request.Options; opts != nil {
			settings.RuleIDs = opts.RuleIDs
			settings.LLMMode = opts.LlmMode
			settings.WebSearch = opts.WebSearch
		}
		settingsJSON, err := json.Marshal(settings)
		if err != nil {
			return err
		}
		id := uuid.New()
		if _, err = tx.Exec(ctx, `INSERT INTO projects(id,org_id,owner_id,name,settings) VALUES($1,$2,$3,$4,$5)`, id, p.OrgID, p.UserID, name, settingsJSON); err != nil {
			return err
		}
		seen := map[uuid.UUID]bool{}
		sources := append(append([]estimate.SourceRef{}, inputs.Request.Sources...), inputs.Request.MediaSources...)
		// Lock overlapping old uploads in the same order for concurrent imports.
		slices.SortFunc(sources, func(a, b estimate.SourceRef) int { return strings.Compare(a.UploadID, b.UploadID) })
		for _, source := range sources {
			upload, err := publicid.Decode("upl", source.UploadID)
			if err != nil {
				return missing()
			}
			if seen[upload] {
				return apix.E(contracts.ErrConflict, nil).With(map[string]any{"reason": "Esta conciliação usa o mesmo envio mais de uma vez. Crie um projeto e envie cada fonte separadamente."})
			}
			seen[upload] = true
			var allowed bool
			err = tx.QueryRow(ctx, `SELECT COALESCE(($3 OR user_id=$4),false) FROM uploads WHERE org_id=$1 AND id=$2 FOR UPDATE`, p.OrgID, upload, caps.ManageAccount, p.UserID).Scan(&allowed)
			if errors.Is(err, pgx.ErrNoRows) {
				return missing()
			}
			if err != nil {
				return err
			}
			if !allowed {
				return missing()
			}
			_, err = tx.Exec(ctx, `INSERT INTO project_documents(org_id,project_id,upload_id,role,created_by) VALUES($1,$2,$3,$4,$5)`, p.OrgID, id, upload, source.Role, p.UserID)
			if db.IsUniqueViolation(err) {
				return apix.E(contracts.ErrConflict, nil).With(map[string]any{"reason": "Um arquivo desta conciliação já pertence a um projeto. Abra o projeto existente."})
			}
			if err != nil {
				return err
			}
		}
		provenance := sha256.Sum256([]byte("legacy-run:" + runID.String()))
		if _, err = tx.Exec(ctx, `INSERT INTO project_runs(org_id,project_id,run_id,revision,signature) VALUES($1,$2,$3,1,$4)`, p.OrgID, id, runID, hex.EncodeToString(provenance[:])); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO project_imports(org_id,project_id,run_id) VALUES($1,$2,$3)`, p.OrgID, id, runID); err != nil {
			return err
		}
		_, snapshot, snapshotErr := s.snapshot(ctx, tx, p, id, 1, "")
		if snapshotErr == nil {
			if _, err = tx.Exec(ctx, `UPDATE project_imports SET baseline_signature=$3 WHERE org_id=$1 AND project_id=$2`, p.OrgID, id, snapshot.Signature); err != nil {
				return err
			}
		} else if !importUnavailable(snapshotErr) {
			return snapshotErr
		}
		if err = event(ctx, tx, p, id, "project.imported"); err != nil {
			return err
		}
		out, _, err = access(ctx, tx, p, id, false)
		return err
	})
	return out, err
}

func importUnavailable(err error) bool {
	var e *apix.Error
	return errors.As(err, &e) && (e.Code == contracts.ErrUploadNotReady || e.Code == contracts.ErrRuleNotFound)
}

func importedInputs(ctx context.Context, tx pgx.Tx, org, id uuid.UUID) (*runs.Inputs, *string, error) {
	var raw []byte
	var baseline *string
	err := tx.QueryRow(ctx, `SELECT r.inputs,i.baseline_signature FROM project_imports i JOIN runs r ON r.org_id=i.org_id AND r.id=i.run_id WHERE i.org_id=$1 AND i.project_id=$2`, org, id).Scan(&raw, &baseline)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	var inputs runs.Inputs
	if err = json.Unmarshal(raw, &inputs); err != nil {
		return nil, nil, err
	}
	return &inputs, baseline, nil
}
