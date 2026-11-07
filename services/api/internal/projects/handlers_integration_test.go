//go:build integration

package projects

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/mmtmn/docmatch/services/api/internal/config"
	"github.com/mmtmn/docmatch/services/api/internal/credits"
	"github.com/mmtmn/docmatch/services/api/internal/httpx"
	"github.com/mmtmn/docmatch/services/api/internal/limits"
	"github.com/mmtmn/docmatch/services/api/internal/publicid"
	"github.com/mmtmn/docmatch/services/api/internal/runs/apix"
	"github.com/mmtmn/docmatch/services/api/internal/testdb"
)

func TestProjectHTTPAnalysisRulesAndPrivateResults(t *testing.T) {
	f := pipeline(t)
	s := f.s
	owner := seed(t, uuid.Nil, "owner")
	uploader := seed(t, owner.OrgID, "uploader")
	colleague := seed(t, owner.OrgID, "reconciler")
	foreign := seed(t, uuid.Nil, "owner")
	call := projectHTTP(t, s)
	created := call(owner, "POST", "/v1/projects", map[string]string{"name": "Fechamento"}, 201)
	id := created["id"].(string)
	path := "/v1/projects/" + id
	call(foreign, "GET", path, nil, 404)
	call(colleague, "GET", path, nil, 404)
	call(owner, "POST", path+"/runs/estimate", "{broken", 400)
	call(owner, "GET", path+"/runs?limit=invalid", nil, 400)
	for _, role := range []string{"protheus", "bank"} {
		upload := seededUpload(t, owner)
		uid, _ := publicid.Decode("upl", upload)
		if _, err := testdb.Get(t).Admin.Exec(t.Context(), `UPDATE uploads SET sha256=$2,store='local',content_type='text/csv',detected_mime='text/csv' WHERE id=$1`, uid, bytes.Repeat([]byte{1}, 32)); err != nil {
			t.Fatal(err)
		}
		call(owner, "POST", path+"/documents", map[string]string{"upload_id": upload, "role": role}, 200)
	}
	current := call(owner, "GET", path, nil, 200)
	// Only explicitly assigned colleagues see this private project.
	shared := call(owner, "PUT", path+"/sharing", map[string]any{"version": current["version"], "visibility": "private", "user_ids": []string{publicid.Encode("usr", *uploader.UserID)}}, 200)
	if len(shared["shared_with"].([]any)) != 1 {
		t.Fatal("missing selected sharing")
	}
	call(uploader, "POST", path+"/runs/estimate", map[string]any{"revision": current["revision"]}, 403)
	call(uploader, "GET", path+"/rules", nil, 403)
	call(uploader, "GET", "/v1/projects/activity", nil, 403)
	call(colleague, "GET", path+"/rules", nil, 404)
	// Rule authorization and writes share one transaction; private rules stay private.
	rule := call(owner, "POST", path+"/rules", map[string]string{"title": "Revisar valores", "body": "Comparar o valor líquido."}, 201)
	rulePath := path + "/rules/" + rule["id"].(string)
	call(owner, "POST", rulePath+"/versions", map[string]string{"body": "Comparar valor líquido e data."}, 201)
	call(owner, "POST", rulePath+"/promote", map[string]string{"to": "trusted"}, 409)
	call(owner, "POST", rulePath+"/promote", map[string]string{"to": "retired"}, 200)
	listed := call(owner, "GET", path+"/rules", nil, 200)
	if len(listed["items"].([]any)) != 1 {
		t.Fatal("rule missing")
	}
	if _, err := f.credits.Grant(t.Context(), owner.OrgID, credits.GrantParams{Kind: credits.GrantBonus, Amount: 1000000, Source: "http-test"}); err != nil {
		t.Fatal(err)
	}
	quote := call(owner, "POST", path+"/runs/estimate", map[string]any{"revision": current["revision"]}, 200)
	q := quote["quote"].(map[string]any)
	run := call(owner, "POST", path+"/runs", map[string]any{"revision": current["revision"], "quote_id": q["id"], "quote_signature": q["signature"]}, 202)
	runID := run["run"].(map[string]any)["id"].(string)
	// A second request with a different idempotency key still returns the same run.
	again := call(owner, "POST", path+"/runs", map[string]any{"revision": current["revision"]}, 202)
	if again["run"].(map[string]any)["id"] != runID {
		t.Fatal("duplicate project run")
	}
	history := call(owner, "GET", path+"/runs", nil, 200)
	if len(history["items"].([]any)) != 1 {
		t.Fatal(history)
	}
	call(owner, "GET", path+"/runs/"+runID, nil, 200)
	call(uploader, "GET", path+"/runs/"+runID, nil, 403)
	call(colleague, "GET", path+"/runs/"+runID, nil, 404)
	call(owner, "GET", "/v1/projects/activity", nil, 200)
	events := call(owner, "GET", path+"/activity", nil, 200)
	if len(events["items"].([]any)) < 3 {
		t.Fatal("missing activity")
	}
	// Upload tickets are attached before completion and cannot be completed elsewhere.
	ticket := call(uploader, "POST", path+"/uploads", map[string]any{"kind": "document", "file_name": "novo.csv", "size_bytes": 10, "mime": "text/csv"}, 201)
	call(foreign, "POST", path+"/uploads/"+ticket["id"].(string)+"/complete", map[string]any{}, 404)
	call(owner, "POST", path+"/uploads/not-an-id/complete", map[string]any{}, 404)
	call(owner, "POST", path+"/uploads/"+ticket["id"].(string)+"/complete", "{broken", 400)
	s.SetRollout(&config.AccountRollout{})
	paused := call(owner, "POST", path+"/runs/"+runID+"/approve-quote", map[string]any{}, 409)
	if paused["code"] != "feature_paused" {
		t.Fatal(paused)
	}
	call(owner, "POST", path+"/runs/"+runID+"/cancel", nil, 200)
}

func projectHTTP(t *testing.T, s *Service) func(apix.Principal, string, string, any, int) map[string]any {
	t.Helper()
	// Simulates the principal already resolved by the session/API-key middleware.
	var actor apix.Principal
	s.auth = func(*http.Request) (apix.Principal, error) { return actor, nil }
	router := httpx.NewRouter(limits.Must())
	s.Mount(router)
	return func(p apix.Principal, method, path string, body any, want int) map[string]any {
		t.Helper()
		actor = p
		var raw []byte
		if text, ok := body.(string); ok {
			raw = []byte(text)
		} else if body != nil {
			var err error
			raw, err = json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
		}
		request := httptest.NewRequest(method, path, bytes.NewReader(raw))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Idempotency-Key", uuid.NewString())
		res := httptest.NewRecorder()
		router.ServeHTTP(res, request)
		if res.Code != want {
			t.Fatalf("%s %s: got %d want %d %s", method, path, res.Code, want, res.Body.String())
		}
		out := map[string]any{}
		if res.Body.Len() > 0 {
			if err := json.Unmarshal(res.Body.Bytes(), &out); err != nil {
				t.Fatal(err)
			}
		}
		return out
	}
}

func TestProjectHTTPVersionedDocumentLifecycle(t *testing.T) {
	f := pipeline(t)
	call := projectHTTP(t, f.s)
	owner := seed(t, uuid.Nil, "owner")
	reader := seed(t, owner.OrgID, "reconciler")
	foreign := seed(t, uuid.Nil, "owner")
	created := call(owner, "POST", "/v1/projects", map[string]string{"name": "Extratos"}, 201)
	path := "/v1/projects/" + created["id"].(string)
	call(owner, "PATCH", path, map[string]any{"version": 1, "name": "Janeiro"}, 200)
	call(owner, "PATCH", path, map[string]any{"version": 1, "name": "Nome antigo"}, 409)
	current := call(owner, "GET", path, nil, 200)
	if current["name"] != "Janeiro" || current["revision"] != created["revision"] {
		t.Fatal("stale rename or rename changed inputs", current)
	}
	// An invalid assignee must not partially replace the existing sharing list.
	current = call(owner, "PUT", path+"/sharing", map[string]any{"version": current["version"], "visibility": "private", "user_ids": []string{publicid.Encode("usr", *reader.UserID)}}, 200)
	call(owner, "PUT", path+"/sharing", map[string]any{"version": current["version"], "visibility": "team", "user_ids": []string{publicid.Encode("usr", *foreign.UserID)}}, 403)
	call(reader, "GET", path, nil, 200)
	current = call(owner, "GET", path, nil, 200)
	if current["visibility"] != "private" || len(current["shared_with"].([]any)) != 1 {
		t.Fatal("partial sharing update")
	}
	attach := func() string {
		return call(owner, "POST", path+"/documents", map[string]string{"upload_id": seededUpload(t, owner), "role": "bank"}, 200)["id"].(string)
	}
	old, newDoc := attach(), attach()
	current = call(owner, "GET", path, nil, 200)
	replace := path + "/documents/" + old + "/replace"
	body := map[string]any{"version": current["version"], "replacement_id": newDoc}
	call(foreign, "POST", replace, body, 404)
	call(owner, "POST", replace, map[string]any{"version": 1, "replacement_id": newDoc}, 409)
	current = call(owner, "POST", replace, body, 200)
	if current["document_count"] != float64(1) {
		t.Fatal("replacement should leave one active file", current)
	}
	docs := current["documents"].([]any)
	for _, raw := range docs {
		doc := raw.(map[string]any)
		if doc["id"] == old && doc["removed_at"] == nil {
			t.Fatal("old version lost or still active")
		}
		if doc["id"] == newDoc && doc["replaces_id"] != old {
			t.Fatal("replacement lineage missing")
		}
	}
	// A stale tab cannot delete the new version; a current tab can remove it.
	removal := path + "/documents/" + newDoc
	call(owner, "DELETE", removal+"?version=1", nil, 409)
	call(owner, "DELETE", removal+"?version="+strconv.Itoa(int(current["version"].(float64))), nil, 204)
	current = call(owner, "GET", path, nil, 200)
	if current["document_count"] != float64(0) || len(current["documents"].([]any)) != 2 {
		t.Fatal("removal destroyed history")
	}
	current = call(owner, "PATCH", path, map[string]any{"version": current["version"], "archived": true}, 200)
	archived := call(owner, "GET", "/v1/projects?archived=true", nil, 200)
	if len(archived["items"].([]any)) != 1 {
		t.Fatal("archived project not discoverable")
	}
	call(owner, "POST", path+"/documents", map[string]string{"upload_id": seededUpload(t, owner)}, 403)
	current = call(owner, "PATCH", path, map[string]any{"version": current["version"], "archived": false}, 200)
	call(owner, "DELETE", path+"?version=1", nil, 409)
	call(owner, "DELETE", path+"?version="+strconv.Itoa(int(current["version"].(float64))), nil, 204)
	call(owner, "GET", path, nil, 404)
}

func TestProjectHTTPRejectsInvalidEditsWithoutChangingSavedWork(t *testing.T) {
	f := pipeline(t)
	call := projectHTTP(t, f.s)
	owner := seed(t, uuid.Nil, "owner")
	created := call(owner, "POST", "/v1/projects", map[string]string{"name": "Salvo"}, 201)
	path := "/v1/projects/" + created["id"].(string)
	doc := publicid.Encode("doc", uuid.New())
	for _, tc := range []struct {
		method, suffix, body string
		status               int
	}{
		{"PATCH", "", "{broken", 400},
		{"PATCH", "", `{"version":1}`, 400},
		{"PATCH", "", `{"version":1,"name":""}`, 400},
		{"PATCH", "", `{"version":1,"settings":{"llm_mode":"unknown"}}`, 400},
		{"PATCH", "", `{"version":1,"settings":{"rule_ids":["invalid"]}}`, 400},
		{"PUT", "/sharing", `{"version":1,"visibility":"public"}`, 400},
		{"PUT", "/sharing", `{"version":1,"visibility":"private","user_ids":["invalid"]}`, 404},
		{"PUT", "/sharing", "{broken", 400},
		{"POST", "/documents", "{broken", 400},
		{"DELETE", "/documents/not-an-id?version=1", "", 404},
		{"DELETE", "/documents/" + doc, "", 400},
		{"DELETE", "/documents/" + doc + "?version=1", "", 404},
		{"POST", "/documents/not-an-id/replace", `{}`, 404},
		{"POST", "/documents/" + doc + "/replace", "{broken", 400},
		{"POST", "/documents/" + doc + "/replace", `{"replacement_id":"bad"}`, 404},
		{"POST", "/runs", "{broken", 400},
		{"POST", "/runs", `{"revision":1}`, 400},
		{"GET", "/runs?cursor=invalid", "", 400},
	} {
		t.Run(tc.method+tc.suffix+tc.body, func(t *testing.T) { call(owner, tc.method, path+tc.suffix, tc.body, tc.status) })
	}
	call(owner, "PATCH", path, map[string]any{"version": 1, "settings": map[string]any{"rules_text": strings.Repeat("a", 10001)}}, 400)
	current := call(owner, "GET", path, nil, 200)
	if current["version"] != float64(1) || current["name"] != "Salvo" {
		t.Fatal("invalid edits changed saved work", current)
	}
	call(owner, "GET", "/v1/projects/activity?cursor=invalid", nil, 400)
	call(apix.Principal{OrgID: owner.OrgID}, "GET", path, nil, 401)
	// Every operation rejects requests without a resolved identity before using its body.
	f.s.auth = apix.Unauthenticated()
	for _, route := range []struct{ method, suffix string }{{"GET", ""}, {"PATCH", ""}, {"DELETE", ""}, {"PUT", "/sharing"}, {"GET", "/activity"}, {"POST", "/documents"}, {"DELETE", "/documents/" + doc}, {"POST", "/documents/" + doc + "/replace"}, {"POST", "/uploads"}, {"POST", "/uploads/invalid/complete"}, {"POST", "/runs/estimate"}, {"POST", "/runs"}, {"GET", "/runs"}} {
		call(owner, route.method, path+route.suffix, nil, 401)
	}
	call(owner, "GET", "/v1/projects", nil, 401)
	call(owner, "POST", "/v1/projects", nil, 401)
	call(owner, "GET", "/v1/projects/activity", nil, 401)
}

func TestProjectSavedWorkSurvivesUnavailableAnalysisEngine(t *testing.T) {
	s := New(testdb.Get(t).App, nil, nil)
	call := projectHTTP(t, s)
	owner := seed(t, uuid.Nil, "owner")
	created := call(owner, "POST", "/v1/projects", map[string]string{"name": "Retomar depois"}, 201)
	path := "/v1/projects/" + created["id"].(string)
	call(owner, "GET", path, nil, 200)
	for _, route := range []struct {
		method, path string
		body         any
	}{
		{"POST", path + "/runs/estimate", map[string]any{"revision": 1}},
		{"POST", path + "/runs", map[string]any{"revision": 1}},
		{"GET", path + "/runs", nil}, {"GET", "/v1/projects/activity", nil},
	} {
		call(owner, route.method, route.path, route.body, 503)
	}
	updated := call(owner, "PATCH", path, map[string]any{"version": 1, "name": "Continua salvo"}, 200)
	if updated["name"] != "Continua salvo" {
		t.Fatal("engine outage prevented editing saved work")
	}
}

func TestProjectHTTPCompanyKeyStaysInItsAccount(t *testing.T) {
	f := pipeline(t)
	call := projectHTTP(t, f.s)
	owner := seed(t, uuid.Nil, "owner")
	foreign := seed(t, uuid.Nil, "owner")
	keyID := uuid.New()
	// This is the trusted principal produced after checking key scopes and creator membership.
	key := apix.Principal{OrgID: owner.OrgID, APIKeyID: &keyID, Plan: "starter"}
	created := call(key, "POST", "/v1/projects", map[string]string{"name": "Integração"}, 201)
	path := "/v1/projects/" + created["id"].(string)
	call(key, "GET", path, nil, 200)
	call(owner, "GET", path, nil, 200)
	call(foreign, "GET", path, nil, 404)
	call(key, "GET", "/v1/projects/activity", nil, 403)
	call(key, "GET", "/v1/projects?limit=invalid", nil, 400)
	call(key, "PATCH", path, map[string]any{"version": 1, "settings": map[string]any{"rule_ids": strings.Split(strings.Repeat("r,", 501), ",")}}, 400)
	call(key, "DELETE", path+"?version=1", nil, 204)
	call(key, "GET", path, nil, 404)
}
