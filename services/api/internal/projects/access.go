package projects

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	contracts "github.com/mmtmn/docmatch/packages/contracts/gen"
	"github.com/mmtmn/docmatch/services/api/internal/db"
	"github.com/mmtmn/docmatch/services/api/internal/publicid"
	"github.com/mmtmn/docmatch/services/api/internal/rules"
	"github.com/mmtmn/docmatch/services/api/internal/runs/apix"
)

func (s *Service) withRules(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, id, ok := s.principal(w, r)
		if !ok {
			return
		}
		response := &ruleResponse{header: make(http.Header)}
		err := db.WithOrgUnit(r.Context(), s.pool, p.OrgID, func(ctx context.Context, tx pgx.Tx) error {
			v, _, err := access(ctx, tx, p, id, r.Method != http.MethodGet)
			if err != nil {
				return err
			}
			if !v.Capabilities.ReadResults {
				return apix.ErrForbidden
			}
			next(response, r.WithContext(rules.WithProject(ctx, p.OrgID, id)))
			if response.status >= http.StatusBadRequest {
				return errRuleResponse
			}
			return nil
		})
		if err != nil && !errors.Is(err, errRuleResponse) {
			apix.Handle(w, r, err)
			return
		}
		for key, values := range response.header {
			w.Header()[key] = values
		}
		if response.status != 0 {
			w.WriteHeader(response.status)
		}
		_, _ = w.Write(response.body.Bytes())
	}
}

// Project access, a rule version and its idempotency receipt commit together.
// Buffer the small JSON response until commit succeeds; an error rolls back.
var errRuleResponse = errors.New("project rules handler returned an error")

type ruleResponse struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (w *ruleResponse) Header() http.Header { return w.header }
func (w *ruleResponse) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}
func (w *ruleResponse) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.body.Write(body)
}

// Resolve applies project visibility to result/export/feedback routes, including
// their legacy URLs. Existing links remain useful without making private runs
// visible to every member of the same company.
func (s *Service) Resolve(next apix.Resolver) apix.Resolver {
	return func(r *http.Request) (apix.Principal, error) {
		p, err := next(r)
		if err != nil {
			return p, err
		}
		value := r.PathValue("id")
		if value == "" {
			return p, nil
		}
		isUpload := strings.HasPrefix(r.URL.Path, "/v1/uploads/")
		prefix := "run"
		if isUpload {
			prefix = "upl"
		}
		if !isUpload && !strings.HasPrefix(r.URL.Path, "/v1/runs/") && !strings.HasPrefix(r.URL.Path, "/v1/projects/") {
			return p, nil
		}
		id, err := publicid.Decode(prefix, value)
		if err != nil {
			return p, missing()
		}
		err = db.WithOrgTx(r.Context(), s.pool, p.OrgID, func(ctx context.Context, tx pgx.Tx) error {
			var project uuid.UUID
			var creator *uuid.UUID
			var err error
			if isUpload {
				err = tx.QueryRow(ctx, `SELECT project_id,created_by FROM project_documents WHERE org_id=$1 AND upload_id=$2`, p.OrgID, id).Scan(&project, &creator)
			} else {
				err = tx.QueryRow(ctx, `SELECT project_id FROM project_runs WHERE org_id=$1 AND run_id=$2`, p.OrgID, id).Scan(&project)
			}
			if errors.Is(err, pgx.ErrNoRows) {
				if r.PathValue("project") != "" {
					return missing()
				}
				return nil
			}
			if err != nil {
				return err
			}
			if selected := r.PathValue("project"); selected != "" && selected != publicid.Encode(Prefix, project) {
				return missing()
			}
			v, _, err := access(ctx, tx, p, project, false)
			if err != nil {
				return err
			}
			if isUpload {
				if !v.Capabilities.Upload || !v.Capabilities.ManageAccount && (p.UserID == nil || creator == nil || *p.UserID != *creator) {
					return apix.ErrForbidden
				}
			} else if r.Method == http.MethodGet || r.Method == http.MethodHead {
				if !v.Capabilities.ReadResults {
					return apix.ErrForbidden
				}
			} else if !v.Capabilities.Reconcile {
				return apix.ErrForbidden
			}
			if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodDelete && !strings.HasSuffix(r.URL.Path, "/cancel") && !s.rollout.Allows(p.OrgID) {
				return apix.E(contracts.ErrFeaturePaused, nil)
			}
			if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/approve-quote") {
				if s.engine == nil {
					return apix.ErrForbidden
				}
				return s.paid(ctx, tx, p)
			}
			return nil
		})
		return p, err
	}
}
