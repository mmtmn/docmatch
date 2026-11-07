//go:build integration

package projects

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	contracts "github.com/mmtmn/docmatch/packages/contracts/gen"
	"github.com/mmtmn/docmatch/services/api/internal/config"
	"github.com/mmtmn/docmatch/services/api/internal/db"
	"github.com/mmtmn/docmatch/services/api/internal/estimate"
	"github.com/mmtmn/docmatch/services/api/internal/publicid"
	"github.com/mmtmn/docmatch/services/api/internal/runs"
	"github.com/mmtmn/docmatch/services/api/internal/runs/apix"
	"github.com/mmtmn/docmatch/services/api/internal/testdb"
)

func legacyRun(t *testing.T, p apix.Principal) (uuid.UUID, []byte) {
	t.Helper()
	d := testdb.Get(t)
	sources := make([]estimate.SourceRef, 0, 2)
	for _, role := range []string{"bank", "ledger"} {
		source := seededUpload(t, p)
		id, _ := publicid.Decode("upl", source)
		if _, err := d.Admin.Exec(t.Context(), `UPDATE uploads SET sha256=$2,store='local',content_type='text/csv',detected_mime='text/csv' WHERE id=$1`, id, bytes.Repeat([]byte{byte(len(sources) + 1)}, 32)); err != nil {
			t.Fatal(err)
		}
		sources = append(sources, estimate.SourceRef{UploadID: source, Role: role, Hints: &estimate.Hints{Delimiter: ";", DateFormat: "DD/MM/YYYY"}})
	}
	disabled := false
	request := runs.Inputs{Request: estimate.Request{Sources: sources, RulesText: "Preserve original amounts", Options: &estimate.Options{UseRules: &disabled, LlmMode: "off"}}, Plan: "starter", UserID: p.UserID}
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	id := uuid.New()
	if _, err := d.Admin.Exec(t.Context(), `INSERT INTO runs(id,org_id,user_id,lane,status,inputs,finished_at,expires_at) VALUES($1,$2,$3,'app','completed',$4,now(),now()+interval '30 days')`, id, p.OrgID, p.UserID, raw); err != nil {
		t.Fatal(err)
	}
	return id, raw
}

func TestLegacySaveIsPrivateAtomicAndDoesNotReconcile(t *testing.T) {
	f := pipeline(t)
	p := seed(t, uuid.Nil, "owner")
	d := testdb.Get(t)
	ctx := t.Context()
	run, raw := legacyRun(t, p)
	results := make(chan Project, 6)
	errs := make(chan error, 6)
	var wg sync.WaitGroup
	for range 6 {
		wg.Go(func() { v, err := f.s.ImportRun(ctx, p, run, "Saved month"); results <- v; errs <- err })
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var saved Project
	for result := range results {
		if saved.ID != "" && saved.ID != result.ID {
			t.Fatal("duplicate project")
		}
		saved = result
	}
	if saved.Visibility != "private" || saved.ImportedRunID != publicid.Encode("run", run) {
		t.Fatal(saved)
	}
	var original, after any
	if err := json.Unmarshal(raw, &original); err != nil {
		t.Fatal(err)
	}
	var persisted []byte
	if err := d.Admin.QueryRow(ctx, `SELECT inputs FROM runs WHERE id=$1`, run).Scan(&persisted); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(persisted, &after); err != nil {
		t.Fatal(err)
	}
	oldJSON, _ := json.Marshal(original)
	newJSON, _ := json.Marshal(after)
	if !bytes.Equal(oldJSON, newJSON) {
		t.Fatal("historical inputs changed")
	}
	var count int
	if err := d.Admin.QueryRow(ctx, `SELECT count(*) FROM runs WHERE org_id=$1`, p.OrgID).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	id := projectID(t, saved)
	detail, err := f.s.Get(ctx, p, id)
	if err != nil || len(detail.Documents) != 2 {
		t.Fatal(detail, err)
	}
	for range 2 {
		result, err := f.s.Analyze(ctx, p, id, AnalysisRequest{Revision: detail.Revision}, true)
		if err != nil || result.State != "unchanged" || !result.Historical || result.Run == nil || result.Run.ID != saved.ImportedRunID || result.Run.ProjectID != saved.ID {
			t.Fatal(result, err)
		}
	}
	name := "Renamed"
	detail, err = f.s.Update(ctx, p, id, Patch{Version: detail.Version, Name: &name})
	if err != nil {
		t.Fatal(err)
	}
	result, err := f.s.Analyze(ctx, p, id, AnalysisRequest{Revision: detail.Revision}, false)
	if err != nil || result.State != "unchanged" {
		t.Fatal(result, err)
	}
	settings := *detail.Settings
	settings.RulesText = "New reconciliation rule"
	detail, err = f.s.Update(ctx, p, id, Patch{Version: detail.Version, Settings: &settings})
	if err != nil {
		t.Fatal(err)
	}
	result, err = f.s.Analyze(ctx, p, id, AnalysisRequest{Revision: detail.Revision}, false)
	if err != nil || result.State != "ready" || result.Quote == nil || result.Historical {
		t.Fatal(result, err)
	}
	for _, table := range []string{"runs", "projects"} {
		if err := d.Admin.QueryRow(ctx, `SELECT count(*) FROM `+table+` WHERE org_id=$1`, p.OrgID).Scan(&count); err != nil || count != 1 {
			t.Fatal(table, count, err)
		}
	}
	if err := d.Admin.QueryRow(ctx, `SELECT count(*) FROM credit_holds WHERE org_id=$1`, p.OrgID).Scan(&count); err != nil || count != 0 {
		t.Fatal("credit reservation created", count, err)
	}
	if err := d.Admin.QueryRow(ctx, `SELECT count(*) FROM river_job WHERE args->>'org_id'=$1`, p.OrgID.String()).Scan(&count); err != nil || count != 0 {
		t.Fatal("job created", count, err)
	}
	for _, role := range []string{"uploader", "reconciler", "viewer", "member"} {
		other := seed(t, p.OrgID, role)
		if _, err := f.s.ImportRun(ctx, other, run, "Stolen"); err == nil {
			t.Fatal("role imported someone else's run", role)
		}
	}
	foreign := seed(t, uuid.Nil, "owner")
	if _, err := f.s.ImportRun(ctx, foreign, run, "Wrong account"); err == nil {
		t.Fatal("cross-account import")
	}
	key := uuid.New()
	limited := apix.Principal{OrgID: p.OrgID, APIKeyID: &key, Scopes: []string{"projects:write"}}
	if _, err := f.s.ImportRun(ctx, limited, run, "Limited"); !errors.Is(err, apix.ErrScopeDenied) {
		t.Fatal("limited key", err)
	}
}

func TestLegacyExpiredEvidenceCanBeSavedWithoutExtendingRetention(t *testing.T) {
	f := pipeline(t)
	p := seed(t, uuid.Nil, "owner")
	d := testdb.Get(t)
	ctx := t.Context()
	run, _ := legacyRun(t, p)
	if _, err := d.Admin.Exec(ctx, `UPDATE uploads SET expires_at=now()-interval '1 day',deleted_at=now() WHERE org_id=$1`, p.OrgID); err != nil {
		t.Fatal(err)
	}
	saved, err := f.s.ImportRun(ctx, p, run, "Old records")
	if err != nil {
		t.Fatal(err)
	}
	detail, err := f.s.Get(ctx, p, projectID(t, saved))
	if err != nil {
		t.Fatal(err)
	}
	for _, doc := range detail.Documents {
		if doc.Status != "expired" {
			t.Fatal("expired file revived")
		}
	}
	result, err := f.s.Analyze(ctx, p, projectID(t, saved), AnalysisRequest{Revision: detail.Revision}, true)
	if err != nil || result.State != "unchanged" {
		t.Fatal(result, err)
	}
	if _, err := d.Admin.Exec(ctx, `UPDATE runs SET expires_at=now()-interval '1 day' WHERE id=$1`, run); err != nil {
		t.Fatal(err)
	}
	result, err = f.s.Analyze(ctx, p, projectID(t, saved), AnalysisRequest{Revision: detail.Revision}, true)
	if err != nil || result.State != "expired" {
		t.Fatal(result, err)
	}
	_, err = f.s.Analyze(ctx, p, projectID(t, saved), AnalysisRequest{Revision: detail.Revision, ReprocessReason: "Need another report"}, true)
	requireCode(t, err, contracts.ErrUploadNotReady)
}

func TestLegacySaveRollsBackWhenAnOriginalFileIsAlreadyPrivate(t *testing.T) {
	f := pipeline(t)
	p := seed(t, uuid.Nil, "owner")
	d := testdb.Get(t)
	ctx := t.Context()
	run, raw := legacyRun(t, p)
	var inputs runs.Inputs
	if err := json.Unmarshal(raw, &inputs); err != nil {
		t.Fatal(err)
	}
	existing, err := f.s.Create(ctx, p, "Keep private")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Attach(ctx, p, projectID(t, existing), AttachRequest{UploadID: inputs.Request.Sources[1].UploadID}); err != nil {
		t.Fatal(err)
	}
	_, err = f.s.ImportRun(ctx, p, run, "Must rollback")
	requireCode(t, err, contracts.ErrConflict)
	var projects, links, documents int
	if err := d.Admin.QueryRow(ctx, `SELECT (SELECT count(*) FROM projects WHERE org_id=$1),(SELECT count(*) FROM project_runs WHERE org_id=$1),(SELECT count(*) FROM project_documents WHERE org_id=$1)`, p.OrgID).Scan(&projects, &links, &documents); err != nil {
		t.Fatal(err)
	}
	if projects != 1 || links != 0 || documents != 1 {
		t.Fatalf("partial import: projects=%d links=%d documents=%d", projects, links, documents)
	}
}

func TestLegacyImportHTTPGuardsAndHistoryAssociation(t *testing.T) {
	f := pipeline(t)
	owner := seed(t, uuid.Nil, "owner")
	run, _ := legacyRun(t, owner)
	runID := publicid.Encode("run", run)
	call := projectHTTP(t, f.s)
	activity := call(owner, "GET", "/v1/projects/activity", nil, 200)
	if activity["items"].([]any)[0].(map[string]any)["can_save_as_project"] != true {
		t.Fatal(activity)
	}
	body := map[string]string{"run_id": runID, "name": "Saved history"}
	call(owner, "POST", "/v1/projects/from-run", "{broken", 400)
	call(owner, "POST", "/v1/projects/from-run", map[string]string{"run_id": "invalid", "name": "Saved"}, 404)
	call(owner, "POST", "/v1/projects/from-run", map[string]string{"run_id": runID, "name": ""}, 400)
	keyID := uuid.New()
	key := apix.Principal{OrgID: owner.OrgID, APIKeyID: &keyID, Scopes: []string{"projects:write"}}
	call(key, "POST", "/v1/projects/from-run", body, 403)
	f.s.SetRollout(&config.AccountRollout{})
	call(owner, "POST", "/v1/projects/from-run", body, 409)
	activity = call(owner, "GET", "/v1/projects/activity", nil, 200)
	if activity["items"].([]any)[0].(map[string]any)["can_save_as_project"] == true {
		t.Fatal("paused import advertised")
	}
	f.s.SetRollout(&config.AccountRollout{All: true})
	saved := call(owner, "POST", "/v1/projects/from-run", body, 200)
	path := "/v1/projects/" + saved["id"].(string)
	call(owner, "GET", path+"/runs/"+runID, nil, 200)
	activity = call(owner, "GET", "/v1/projects/activity", nil, 200)
	item := activity["items"].([]any)[0].(map[string]any)
	if item["project_id"] != saved["id"] || item["can_save_as_project"] == true {
		t.Fatal(item)
	}
	reader := seed(t, owner.OrgID, "reconciler")
	uploader := seed(t, owner.OrgID, "uploader")
	call(reader, "GET", path+"/runs/"+runID, nil, 404)
	call(owner, "PUT", path+"/sharing", map[string]any{"version": saved["version"], "visibility": "team", "user_ids": []string{}}, 200)
	call(reader, "GET", path+"/runs/"+runID, nil, 200)
	call(uploader, "GET", path+"/runs/"+runID, nil, 403)
	detail := call(uploader, "GET", path, nil, 200)
	if detail["imported_run_id"] != nil {
		t.Fatal("original result disclosed to uploader", detail)
	}
	// A valid automation key may import its own account's old runs, never a foreign run.
	second, _ := legacyRun(t, owner)
	key.Scopes = append(key.Scopes, "runs:read")
	call(key, "POST", "/v1/projects/from-run", map[string]string{"run_id": publicid.Encode("run", second), "name": "API history"}, 200)
}

func TestLegacyImportPreservesAdvancedInputsAndRejectsActiveOrRepeatedSources(t *testing.T) {
	f := pipeline(t)
	owner := seed(t, uuid.Nil, "owner")
	run, raw := legacyRun(t, owner)
	var inputs runs.Inputs
	if err := json.Unmarshal(raw, &inputs); err != nil {
		t.Fatal(err)
	}
	inputs.Request.WorkflowID = "historic-workflow"
	inputs.Request.Options.Escalation = true
	raw, err := json.Marshal(inputs)
	if err != nil {
		t.Fatal(err)
	}
	d := testdb.Get(t)
	if _, err = d.Admin.Exec(t.Context(), `UPDATE runs SET inputs=$2,status='running' WHERE id=$1`, run, raw); err != nil {
		t.Fatal(err)
	}
	_, err = f.s.ImportRun(t.Context(), owner, run, "Wait")
	requireCode(t, err, contracts.ErrConflict)
	if _, err = d.Admin.Exec(t.Context(), `UPDATE runs SET status='completed' WHERE id=$1`, run); err != nil {
		t.Fatal(err)
	}
	saved, err := f.s.ImportRun(t.Context(), owner, run, "Keep input options")
	if err != nil {
		t.Fatal(err)
	}
	err = db.WithOrgUnit(t.Context(), d.App, owner.OrgID, func(ctx context.Context, tx pgx.Tx) error {
		request, _, err := f.s.snapshot(ctx, tx, owner, projectID(t, saved), saved.Revision, "")
		if err != nil {
			return err
		}
		if request.WorkflowID != inputs.Request.WorkflowID || !request.Options.Escalation || len(request.Sources) != 2 {
			t.Fatal(request)
		}
		for _, source := range request.Sources {
			if source.Hints == nil || source.Hints.Delimiter != ";" || source.Hints.DateFormat != "DD/MM/YYYY" {
				t.Fatal(source)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	repeated, raw := legacyRun(t, owner)
	if err = json.Unmarshal(raw, &inputs); err != nil {
		t.Fatal(err)
	}
	inputs.Request.Sources = append(inputs.Request.Sources, inputs.Request.Sources[0])
	raw, err = json.Marshal(inputs)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = d.Admin.Exec(t.Context(), `UPDATE runs SET inputs=$2 WHERE id=$1`, repeated, raw); err != nil {
		t.Fatal(err)
	}
	_, err = f.s.ImportRun(t.Context(), owner, repeated, "Do not drop duplicates")
	requireCode(t, err, contracts.ErrConflict)
}
