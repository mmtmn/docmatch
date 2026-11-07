//go:build integration

package projects

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	contracts "github.com/mmtmn/docmatch/packages/contracts/gen"
	"github.com/mmtmn/docmatch/services/api/internal/db"
	"github.com/mmtmn/docmatch/services/api/internal/httpx"
	"github.com/mmtmn/docmatch/services/api/internal/limits"
	"github.com/mmtmn/docmatch/services/api/internal/publicid"
	"github.com/mmtmn/docmatch/services/api/internal/runs/apix"
	"github.com/mmtmn/docmatch/services/api/internal/testdb"
)

func TestMain(m *testing.M) { testdb.Main(m) }

func seed(t *testing.T, org uuid.UUID, role string) apix.Principal {
	t.Helper()
	d := testdb.Get(t)
	user := uuid.New()
	if org == uuid.Nil {
		org = uuid.New()
		if _, err := d.Admin.Exec(t.Context(), `INSERT INTO orgs(id,name,slug) VALUES($1,'Equipe',$2)`, org, org.String()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := d.Admin.Exec(t.Context(), `INSERT INTO users(id,email,email_verified_at) VALUES($1,$2,now())`, user, user.String()+"@example.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Admin.Exec(t.Context(), `INSERT INTO memberships(org_id,user_id,role) VALUES($1,$2,$3)`, org, user, role); err != nil {
		t.Fatal(err)
	}
	return apix.Principal{OrgID: org, UserID: &user, Plan: "starter"}
}
func seededUpload(t *testing.T, p apix.Principal) string {
	t.Helper()
	id := uuid.New()
	d := testdb.Get(t)
	if _, err := d.Admin.Exec(t.Context(), `INSERT INTO uploads(id,org_id,user_id,file_name,size_bytes,storage_key,status,expires_at) VALUES($1,$2,$3,'extrato.csv',100,$4,'clean',now()+interval '30 days')`, id, p.OrgID, p.UserID, id.String()); err != nil {
		t.Fatal(err)
	}
	return publicid.Encode("upl", id)
}
func projectID(t *testing.T, p Project) uuid.UUID {
	t.Helper()
	id, err := publicid.Decode(Prefix, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	return id
}
func requireCode(t *testing.T, err error, code contracts.ErrorCode) {
	t.Helper()
	var e *apix.Error
	if !errors.As(err, &e) || e.Code != code {
		t.Fatalf("want %s: %v", code, err)
	}
}

func TestProjectPrivacyVersionsAndReceipts(t *testing.T) {
	d := testdb.Get(t)
	ctx := t.Context()
	s := New(d.App, nil, nil)
	owner := seed(t, uuid.Nil, "owner")
	uploader := seed(t, owner.OrgID, "uploader")
	full := seed(t, owner.OrgID, "reconciler")
	foreign := seed(t, uuid.Nil, "owner")
	created, err := s.Create(ctx, owner, " Novembro ")
	if err != nil {
		t.Fatal(err)
	}
	id := projectID(t, created)
	if created.Name != "Novembro" || created.Visibility != "private" || created.Revision != 1 {
		t.Fatal(created)
	}
	for _, p := range []apix.Principal{uploader, full, foreign} {
		if _, err := s.Get(ctx, p, id); err == nil {
			t.Fatal("private project exposed")
		}
		items, err := s.List(ctx, p, false, 25, 0)
		if err != nil || len(items) != 0 {
			t.Fatal("private list", items, err)
		}
	}
	if _, err := s.Create(ctx, uploader, "No"); !errors.Is(err, apix.ErrForbidden) {
		t.Fatal("upload-only project creation", err)
	}
	if err := s.Share(ctx, owner, id, created.Version, "private", []uuid.UUID{*uploader.UserID, *full.UserID}); err != nil {
		t.Fatal(err)
	}
	one, err := s.Get(ctx, uploader, id)
	if err != nil || one.Settings != nil {
		t.Fatal("receipt leaked settings", err)
	}
	if _, err := s.Update(ctx, uploader, id, Patch{Version: one.Version, Name: &created.Name}); !errors.Is(err, apix.ErrForbidden) {
		t.Fatal("uploader edit", err)
	}
	ownerUpload, ownUpload := seededUpload(t, owner), seededUpload(t, uploader)
	doc, err := s.Attach(ctx, owner, id, AttachRequest{UploadID: ownerUpload})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Attach(ctx, uploader, id, AttachRequest{UploadID: ownerUpload}); err == nil {
		t.Fatal("other person's upload attached")
	}
	if _, err := s.Attach(ctx, uploader, id, AttachRequest{UploadID: ownUpload}); err != nil {
		t.Fatal(err)
	}
	one, err = s.Get(ctx, uploader, id)
	if err != nil || len(one.Documents) != 1 || one.Documents[0].UploadID != ownUpload {
		t.Fatal("receipts", one.Documents, err)
	}
	all, err := s.Get(ctx, full, id)
	if err != nil || len(all.Documents) != 2 || all.Settings == nil {
		t.Fatal("full access", err)
	}
	revision := all.Revision
	name := "Fechamento de novembro"
	renamed, err := s.Update(ctx, full, id, Patch{Version: all.Version, Name: &name})
	if err != nil || renamed.Revision != revision {
		t.Fatal("rename changed analysis", err)
	}
	_, err = s.Update(ctx, full, id, Patch{Version: all.Version, Name: &name})
	requireCode(t, err, contracts.ErrConflict)
	settings := Settings{UseRules: true, RulesText: "Compare os valores", LLMMode: "off"}
	changed, err := s.Update(ctx, full, id, Patch{Version: renamed.Version, Settings: &settings})
	if err != nil || changed.Revision != revision+1 {
		t.Fatal("settings revision", err)
	}
	// A replaced file stays in history and keeps its original upload identifier.
	replacement := seededUpload(t, full)
	if _, err := s.Attach(ctx, full, id, AttachRequest{UploadID: replacement, ReplacesID: &doc}); err != nil {
		t.Fatal(err)
	}
	all, err = s.Get(ctx, full, id)
	if err != nil || all.DocumentCount != 2 || len(all.Documents) != 3 {
		t.Fatal("immutable history", err)
	}
	var historical bool
	for _, d := range all.Documents {
		if d.ID == doc {
			historical = d.RemovedAt != nil && d.UploadID == ownerUpload
		}
	}
	if !historical {
		t.Fatal("old version lost")
	}
	uploadID, _ := publicid.Decode("upl", ownUpload)
	if _, err := d.Admin.Exec(ctx, `UPDATE uploads SET expires_at=now()-interval '1 day' WHERE id=$1`, uploadID); err != nil {
		t.Fatal(err)
	}
	one, err = s.Get(ctx, uploader, id)
	if err != nil || one.Documents[0].Status != "expired" {
		t.Fatal("expiry", err)
	}
	// Cross-account foreign keys and RLS prevent an application query from bypassing checks.
	err = db.WithOrgTx(ctx, d.App, foreign.OrgID, func(ctx context.Context, tx pgx.Tx) error {
		var count int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM projects WHERE id=$1`, id).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			return errors.New("RLS leak")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Share(ctx, full, id, all.Version, "team", nil); !errors.Is(err, apix.ErrForbidden) {
		t.Fatal("non-manager sharing", err)
	}
	if _, err := d.Admin.Exec(ctx, `DELETE FROM memberships WHERE org_id=$1 AND user_id=$2`, owner.OrgID, full.UserID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, full, id); !errors.Is(err, apix.ErrForbidden) {
		t.Fatal("revoked member read", err)
	}
	if items, err := s.Activity(ctx, uploader, id); err != nil || len(items) != 1 {
		t.Fatal("uploader activity", items, err)
	}
}

func TestConcurrentAttachAndStaleEdits(t *testing.T) {
	d := testdb.Get(t)
	s := New(d.App, nil, nil)
	p := seed(t, uuid.Nil, "owner")
	ctx := t.Context()
	created, err := s.Create(ctx, p, "Retomar")
	if err != nil {
		t.Fatal(err)
	}
	id := projectID(t, created)
	request := AttachRequest{UploadID: seededUpload(t, p)}
	var wg sync.WaitGroup
	ids := make(chan string, 4)
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, err := s.Attach(ctx, p, projectID(t, created), request)
			if err != nil {
				t.Error(err)
				return
			}
			ids <- id
		}()
	}
	wg.Wait()
	close(ids)
	first := ""
	for id := range ids {
		if first != "" && id != first {
			t.Fatal("duplicate attachment")
		}
		first = id
	}
	detail, err := s.Get(ctx, p, id)
	if err != nil || len(detail.Documents) != 1 || detail.Revision != 2 {
		t.Fatal("duplicate revision", detail, err)
	}
	other, err := s.Create(ctx, p, "Outro")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Attach(ctx, p, projectID(t, other), request); err == nil {
		t.Fatal("file in two projects")
	}
	archive := true
	detail, err = s.Update(ctx, p, id, Patch{Version: detail.Version, Archived: &archive})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Attach(ctx, p, id, AttachRequest{UploadID: seededUpload(t, p)}); !errors.Is(err, apix.ErrForbidden) {
		t.Fatal("archived upload", err)
	}
	archived, err := s.List(ctx, p, true, 25, 0)
	if err != nil || len(archived) != 1 {
		t.Fatal("archived list", err)
	}
	archive = false
	detail, err = s.Update(ctx, p, id, Patch{Version: detail.Version, Archived: &archive})
	if err != nil {
		t.Fatal(err)
	}
	doc, _ := publicid.Decode("doc", first)
	if err := s.RemoveDocument(ctx, p, id, doc, detail.Version-1); err == nil {
		t.Fatal("stale removal")
	}
	if err := s.RemoveDocument(ctx, p, id, doc, detail.Version); err != nil {
		t.Fatal(err)
	}
	detail, err = s.Get(ctx, p, id)
	if err != nil || detail.DocumentCount != 0 {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, p, id, detail.Version-1); err == nil {
		t.Fatal("stale delete")
	}
	if err := s.Delete(ctx, p, id, detail.Version); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, p, id); err == nil {
		t.Fatal("deleted project visible")
	}
}

func TestHTTPValidationAndPermissions(t *testing.T) {
	d := testdb.Get(t)
	p := seed(t, uuid.Nil, "owner")
	s := New(d.App, apix.Static(p), nil)
	router := httpx.NewRouter(limits.Must())
	s.Mount(router)
	call := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	w := call("POST", "/v1/projects", `{"name":"Revisão"}`)
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	var pjt Project
	if err := json.Unmarshal(w.Body.Bytes(), &pjt); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		method, path, body string
		status             int
	}{
		{"GET", "/v1/projects", "", 200},
		{"GET", "/v1/projects/" + pjt.ID, "", 200},
		{"GET", "/v1/projects?limit=0", "", 400},
		{"GET", "/v1/projects/invalid", "", 404},
		{"POST", "/v1/projects", `{"name":""}`, 400},
		{"POST", "/v1/projects", `{"unexpected":true}`, 400},
		{"PATCH", "/v1/projects/" + pjt.ID, `{"version":1,"name":"Outro nome"}`, 200},
		{"DELETE", "/v1/projects/" + pjt.ID, "", 400},
		{"PUT", "/v1/projects/" + pjt.ID + "/sharing", `{"version":2,"visibility":"team","user_ids":[]}`, 200},
		{"GET", "/v1/projects/" + pjt.ID + "/activity", "", 200},
		{"POST", "/v1/projects/" + pjt.ID + "/uploads", `{}`, 503},
		{"POST", "/v1/projects/" + pjt.ID + "/uploads/invalid/complete", `{}`, 404},
		{"DELETE", "/v1/projects/" + pjt.ID + "/documents/invalid?version=3", "", 404},
		{"POST", "/v1/projects/" + pjt.ID + "/documents", `{"upload_id":"invalid"}`, 404},
		{"DELETE", "/v1/projects/" + pjt.ID + "?version=3", "", 204},
	} {
		w := call(c.method, c.path, c.body)
		if w.Code != c.status {
			t.Errorf("%s %s: %d want %d %s", c.method, c.path, w.Code, c.status, w.Body.String())
		}
	}
}

func TestReplacementRequiresCleanSameProjectAndCurrentVersion(t *testing.T) {
	ctx := t.Context()
	s := New(testdb.Get(t).App, nil, nil)
	owner := seed(t, uuid.Nil, "owner")
	uploader := seed(t, owner.OrgID, "uploader")
	project, err := s.Create(ctx, owner, "Versões")
	if err != nil {
		t.Fatal(err)
	}
	id := projectID(t, project)
	old, err := s.Attach(ctx, owner, id, AttachRequest{UploadID: seededUpload(t, owner), Role: "bank"})
	if err != nil {
		t.Fatal(err)
	}
	replacementUpload := seededUpload(t, owner)
	next, err := s.Attach(ctx, owner, id, AttachRequest{UploadID: replacementUpload})
	if err != nil {
		t.Fatal(err)
	}
	previous, _ := publicid.Decode("doc", old)
	replacement, _ := publicid.Decode("doc", next)
	detail, err := s.Get(ctx, owner, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Share(ctx, owner, id, detail.Version, "team", nil); err != nil {
		t.Fatal(err)
	}
	detail, err = s.Get(ctx, owner, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceDocument(ctx, uploader, id, previous, replacement, detail.Version); !errors.Is(err, apix.ErrForbidden) {
		t.Fatal("uploader replaced document", err)
	}
	if err := s.ReplaceDocument(ctx, owner, id, previous, replacement, detail.Version-1); err == nil {
		t.Fatal("stale replacement")
	}
	other, err := s.Create(ctx, owner, "Outro")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceDocument(ctx, owner, projectID(t, other), previous, replacement, other.Version); err == nil {
		t.Fatal("cross-project replacement")
	}
	uploadID, _ := publicid.Decode("upl", replacementUpload)
	if _, err := testdb.Get(t).Admin.Exec(ctx, `UPDATE uploads SET status='scanning' WHERE id=$1`, uploadID); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceDocument(ctx, owner, id, previous, replacement, detail.Version); err == nil {
		t.Fatal("unsafe replacement")
	}
	if _, err := testdb.Get(t).Admin.Exec(ctx, `UPDATE uploads SET status='clean' WHERE id=$1`, uploadID); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceDocument(ctx, owner, id, previous, replacement, detail.Version); err != nil {
		t.Fatal(err)
	}
	updated, err := s.Get(ctx, owner, id)
	if err != nil {
		t.Fatal(err)
	}
	if updated.DocumentCount != 1 || updated.Revision != detail.Revision+1 {
		t.Fatal("replacement was not atomic", updated)
	}
	for _, doc := range updated.Documents {
		if doc.ID == old && doc.RemovedAt == nil {
			t.Fatal("old version still active")
		}
		if doc.ID == next && (doc.Role != "bank" || doc.ReplacesID == nil || *doc.ReplacesID != old) {
			t.Fatal("replacement lost lineage or role")
		}
	}
}
