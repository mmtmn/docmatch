package projects

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/jackc/pgx/v5"

	contracts "github.com/mmtmn/docmatch/packages/contracts/gen"
	"github.com/mmtmn/docmatch/services/api/internal/db"
	"github.com/mmtmn/docmatch/services/api/internal/httpx"
	"github.com/mmtmn/docmatch/services/api/internal/runs"
	"github.com/mmtmn/docmatch/services/api/internal/runs/apix"
	"github.com/mmtmn/docmatch/services/api/internal/tenancy"
)

func (s *Service) estimate(w http.ResponseWriter, r *http.Request) {
	p, id, ok := s.principal(w, r)
	if !ok {
		return
	}
	var body AnalysisRequest
	if err := httpx.DecodeJSON(r, &body); err != nil {
		apix.Handle(w, r, err)
		return
	}
	v, err := s.Analyze(r.Context(), p, id, body, false)
	respond(w, r, v, err)
}

// accountActivity includes legacy history only for roles that already had
// access to it; newly invited seats see accessible project runs only.
func (s *Service) accountActivity(w http.ResponseWriter, r *http.Request) {
	p, err := s.auth(r)
	if err != nil {
		apix.Handle(w, r, err)
		return
	}
	if s.engine == nil {
		apix.Write(w, r, contracts.ErrServiceUnavailable, nil)
		return
	}
	page, aerr := apix.ParsePage(r)
	if aerr != nil {
		apix.Handle(w, r, aerr)
		return
	}
	items := []runs.View{}
	err = db.WithOrgUnit(r.Context(), s.pool, p.OrgID, func(ctx context.Context, tx pgx.Tx) error {
		if p.UserID == nil {
			return apix.ErrForbidden
		}
		role, err := tenancy.Check(ctx, tx, p.OrgID, *p.UserID)
		if err != nil {
			return err
		}
		caps := tenancy.Permissions(role)
		if !caps.ReadResults {
			return apix.ErrForbidden
		}
		filter := runs.ListFilter{ProjectOnly: role == "reconciler"}
		if !caps.ManageAccount {
			filter.VisibleTo = p.UserID
		}
		rows, err := s.engine.Runs.List(ctx, p.OrgID, filter, page)
		if err != nil {
			return err
		}
		for _, run := range rows {
			view := s.engine.Runs.View(ctx, run)
			view.CanSaveAsProject = s.rollout.Allows(p.OrgID) && caps.CreateProject && role != "reconciler" && run.ProjectID == nil && run.Inputs.Project == nil && run.Status.Terminal() && (caps.ManageAccount || run.UserID != nil && *run.UserID == *p.UserID)
			items = append(items, view)
		}
		return nil
	})
	respond(w, r, runs.ListResponse{Items: items, NextCursor: apix.EncodeCursor(page, len(items))}, err)
}

func (s *Service) reconcile(w http.ResponseWriter, r *http.Request) {
	p, id, ok := s.principal(w, r)
	if !ok {
		return
	}
	if s.engine == nil {
		apix.Write(w, r, contracts.ErrServiceUnavailable, nil)
		return
	}
	// Recheck authorization even when the response is an idempotent replay.
	err := db.WithOrgTx(r.Context(), s.pool, p.OrgID, func(ctx context.Context, tx pgx.Tx) error { _, _, err := access(ctx, tx, p, id, true); return err })
	if err != nil {
		apix.Handle(w, r, err)
		return
	}
	var body AnalysisRequest
	if err := httpx.DecodeJSON(r, &body); err != nil {
		apix.Handle(w, r, err)
		return
	}
	key, aerr := apix.IdemKey(r)
	if aerr != nil {
		apix.Handle(w, r, aerr)
		return
	}
	raw, err := json.Marshal(body)
	if err != nil {
		apix.Handle(w, r, err)
		return
	}
	result, err := apix.Idempotent(r.Context(), s.engine.Runs.Idem(), p.OrgID, key, apix.IdemHash(r, raw), s.engine.Runs.Now(), func() (apix.Result, error) {
		v, err := s.Analyze(r.Context(), p, id, body, true)
		if err != nil {
			return apix.Result{}, err
		}
		return apix.Result{Status: http.StatusAccepted, Body: v}, nil
	})
	if err != nil {
		apix.Handle(w, r, err)
		return
	}
	apix.WriteJSON(w, result.Status, result.Body)
}

func (s *Service) listRuns(w http.ResponseWriter, r *http.Request) {
	p, id, ok := s.principal(w, r)
	if !ok {
		return
	}
	if s.engine == nil {
		apix.Write(w, r, contracts.ErrServiceUnavailable, nil)
		return
	}
	page, aerr := apix.ParsePage(r)
	if aerr != nil {
		apix.Handle(w, r, aerr)
		return
	}
	items := []runs.View{}
	err := db.WithOrgUnit(r.Context(), s.pool, p.OrgID, func(ctx context.Context, tx pgx.Tx) error {
		v, _, err := access(ctx, tx, p, id, false)
		if err != nil {
			return err
		}
		if !v.Capabilities.ReadResults {
			return apix.ErrForbidden
		}
		rows, err := s.engine.Runs.List(ctx, p.OrgID, runs.ListFilter{ProjectID: &id}, page)
		if err != nil {
			return err
		}
		for _, run := range rows {
			items = append(items, s.engine.Runs.View(ctx, run))
		}
		return nil
	})
	respond(w, r, runs.ListResponse{Items: items, NextCursor: apix.EncodeCursor(page, len(items))}, err)
}
