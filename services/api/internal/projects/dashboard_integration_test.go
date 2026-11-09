//go:build integration

package projects

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/mmtmn/docmatch/services/api/internal/httpx"
	"github.com/mmtmn/docmatch/services/api/internal/limits"
	"github.com/mmtmn/docmatch/services/api/internal/publicid"
	"github.com/mmtmn/docmatch/services/api/internal/runs/apix"
	"github.com/mmtmn/docmatch/services/api/internal/testdb"
)

func dashboardRun(t *testing.T, p apix.Principal, project *Project, status string, created time.Time, spent int64) uuid.UUID {
	t.Helper()
	id := uuid.New()
	summary := `{"lines_total":100,"lines_matched":80,"findings_total":3,"findings_by_severity":{"high":1,"critical":1}}`
	_, err := testdb.Get(t).Admin.Exec(t.Context(), `INSERT INTO runs(id,org_id,user_id,lane,status,created_at,started_at,finished_at,credits_spent,summary,expires_at) VALUES($1,$2,$3,'app',$4,$5::timestamptz,$5::timestamptz,$5::timestamptz+interval '12 seconds',$6,$7,$5::timestamptz+interval '90 days')`, id, p.OrgID, p.UserID, status, created, spent, summary)
	if err != nil {
		t.Fatal(err)
	}
	if project != nil {
		_, err = testdb.Get(t).Admin.Exec(t.Context(), `INSERT INTO project_runs(org_id,project_id,run_id,revision,signature) VALUES($1,$2,$3,1,$4)`, p.OrgID, projectID(t, *project), id, strings.Repeat("a", 64))
		if err != nil {
			t.Fatal(err)
		}
	}
	return id
}

func TestDashboardTotalsWindowsPrivacyAndEmpty(t *testing.T) {
	s := New(testdb.Get(t).App, nil, nil)
	owner := seed(t, uuid.Nil, "owner")
	seat := seed(t, owner.OrgID, "reconciler")
	uploader := seed(t, owner.OrgID, "uploader")
	foreign := seed(t, uuid.Nil, "owner")
	now := time.Date(2026, 11, 9, 15, 0, 0, 0, time.UTC)
	private, err := s.Create(t.Context(), owner, "Privado")
	if err != nil {
		t.Fatal(err)
	}
	shared, err := s.Create(t.Context(), owner, "Compartilhado")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Share(t.Context(), owner, projectID(t, shared), shared.Version, "private", []uuid.UUID{*seat.UserID}); err != nil {
		t.Fatal(err)
	}
	dashboardRun(t, owner, &private, "completed", now.Add(-time.Hour), 900)
	dashboardRun(t, owner, &shared, "completed", now.Add(-2*time.Hour), 100)
	dashboardRun(t, owner, &shared, "failed", now.Add(-time.Hour), 40)
	dashboardRun(t, owner, nil, "completed", now.Add(-3*time.Hour), 60)
	dashboardRun(t, owner, &shared, "completed", now.AddDate(0, 0, -8), 30)
	// Half-open periods: the lower bound belongs to current; now is excluded.
	dashboardRun(t, owner, &shared, "queued", now.AddDate(0, 0, -7), 0)
	dashboardRun(t, owner, &shared, "completed", now, 1000)
	dashboardRun(t, owner, &shared, "completed", now.AddDate(0, 0, -15), 2000)
	upload := seededUpload(t, owner)
	if _, err = s.Attach(t.Context(), owner, projectID(t, shared), AttachRequest{UploadID: upload}); err != nil {
		t.Fatal(err)
	}
	got, err := s.dashboardAt(t.Context(), owner, 7, now)
	if err != nil {
		t.Fatal(err)
	}
	if got.Current.Runs != 5 || got.Current.Completed != 3 || got.Current.Failed != 1 || got.Current.Queued != 1 || got.Current.CreditsSpent != 1100 || got.Current.LinesTotal != 300 || got.Current.LinesMatched != 240 || got.Current.Findings != 9 || got.Current.HighFindings != 6 {
		t.Fatalf("wrong totals: %+v", got.Current)
	}
	if got.Previous.Runs != 1 || got.Previous.CreditsSpent != 30 || got.Current.MedianSeconds == nil || *got.Current.MedianSeconds != 12 {
		t.Fatalf("comparison/duration: %+v", got)
	}
	if got.Portfolio.Active != 2 || got.Portfolio.Documents != 1 || len(got.Series) != 8 || got.Series[0].Runs != 1 || got.Series[7].Runs != 4 {
		t.Fatalf("portfolio/series: %+v", got)
	}
	if !strings.HasPrefix(got.Recent[0].ID, "run_") || got.GeneratedAt != now {
		t.Fatal("invalid public response", got)
	}
	restricted, err := s.dashboardAt(t.Context(), seat, 7, now)
	if err != nil || restricted.Current.Runs != 3 || restricted.Current.CreditsSpent != 140 || restricted.Portfolio.Active != 1 {
		t.Fatalf("private/legacy exposure: %+v %v", restricted, err)
	}
	raw, _ := json.Marshal(restricted)
	if strings.Contains(string(raw), "Privado") {
		t.Fatal("private project name exposed")
	}
	// A newer result supersedes the failed run, so it is not an action item.
	if len(restricted.Attention) != 0 {
		t.Fatal("superseded failure shown", restricted.Attention)
	}
	empty, err := s.dashboardAt(t.Context(), foreign, 30, now)
	if err != nil || empty.Current.Runs != 0 || empty.Current.MedianSeconds != nil || empty.Portfolio.Active != 0 || len(empty.Recent) != 0 || len(empty.Series) != 31 {
		t.Fatalf("empty tenant: %+v %v", empty, err)
	}
	if _, err = s.dashboardAt(t.Context(), uploader, 7, now); !errors.Is(err, apix.ErrForbidden) {
		t.Fatal("uploader saw analytics", err)
	}
	if _, err = s.dashboardAt(t.Context(), apix.Principal{OrgID: owner.OrgID, APIKeyID: ptrUUID(uuid.New())}, 7, now); !errors.Is(err, apix.ErrForbidden) {
		t.Fatal("API key dashboard", err)
	}
	if _, err = testdb.Get(t).Admin.Exec(t.Context(), `DELETE FROM memberships WHERE org_id=$1 AND user_id=$2`, owner.OrgID, seat.UserID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.dashboardAt(t.Context(), seat, 7, now); err == nil {
		t.Fatal("removed seat kept access")
	}
}

func ptrUUID(v uuid.UUID) *uuid.UUID { return &v }

func TestDashboardAttentionExpiryDeletionAndHTTP(t *testing.T) {
	s := New(testdb.Get(t).App, nil, nil)
	owner := seed(t, uuid.Nil, "owner")
	now := time.Now().UTC()
	project, err := s.Create(t.Context(), owner, "Operação")
	if err != nil {
		t.Fatal(err)
	}
	failed := dashboardRun(t, owner, &project, "failed", now.Add(-time.Hour), 2)
	approval := dashboardRun(t, owner, nil, "needs_approval", now.Add(-time.Hour), 0)
	dashboardRun(t, owner, nil, "cancelled", now.Add(-time.Hour), 0)
	dashboardRun(t, owner, nil, "running", now.Add(-time.Hour), 1)
	got, err := s.dashboardAt(t.Context(), owner, 90, now)
	if err != nil || len(got.Attention) != 2 || got.Attention[0].ID != publicid.Encode("run", approval) || got.Current.Cancelled != 1 || got.Current.Running != 1 {
		t.Fatalf("attention: %+v %v", got, err)
	}
	if _, err = testdb.Get(t).Admin.Exec(t.Context(), `UPDATE runs SET expires_at=$2 WHERE id=$1`, failed, now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	got, err = s.dashboardAt(t.Context(), owner, 90, now)
	if err != nil || len(got.Attention) != 1 {
		t.Fatal("expired action", got, err)
	}
	if _, err = testdb.Get(t).Admin.Exec(t.Context(), `UPDATE projects SET deleted_at=now() WHERE id=$1`, projectID(t, project)); err != nil {
		t.Fatal(err)
	}
	got, err = s.dashboardAt(t.Context(), owner, 90, now)
	if err != nil || got.Current.Runs != 3 || got.Portfolio.Active != 0 {
		t.Fatal("deleted project exposure", got, err)
	}
	s.auth = func(*http.Request) (apix.Principal, error) { return owner, nil }
	router := httpx.NewRouter(limits.Must())
	s.Mount(router)
	for _, tc := range []struct {
		query  string
		status int
	}{{"", 200}, {"?days=7", 200}, {"?days=90", 200}, {"?days=0", 400}, {"?days=31", 400}, {"?days=no", 400}, {"?days=", 400}, {"?days=7&days=90", 400}} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/projects/dashboard"+tc.query, nil))
		if response.Code != tc.status {
			t.Fatalf("%s: %d %s", tc.query, response.Code, response.Body.String())
		}
		if tc.status == 200 && !strings.Contains(response.Header().Get("Cache-Control"), "no-store") {
			t.Fatal("dashboard can be cached")
		}
	}
	s.auth = apix.Unauthenticated()
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/projects/dashboard", nil))
	if response.Code != 401 {
		t.Fatal("anonymous dashboard", response.Code)
	}
}
