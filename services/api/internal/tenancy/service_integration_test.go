//go:build integration

package tenancy

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/mmtmn/docmatch/services/api/internal/account"
	"github.com/mmtmn/docmatch/services/api/internal/account/accountpg"
	"github.com/mmtmn/docmatch/services/api/internal/db"
	"github.com/mmtmn/docmatch/services/api/internal/httpx"
	"github.com/mmtmn/docmatch/services/api/internal/limits"
	"github.com/mmtmn/docmatch/services/api/internal/publicid"
	"github.com/mmtmn/docmatch/services/api/internal/session"
	"github.com/mmtmn/docmatch/services/api/internal/testdb"
)

func TestMain(m *testing.M) { testdb.Main(m) }

func seed(t *testing.T, role string) session.Principal {
	t.Helper()
	d := testdb.Get(t)
	p := session.Principal{OrgID: uuid.New(), UserID: uuid.New()}
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO users(id,email,email_verified_at) VALUES($1,$2,now())`, []any{p.UserID, p.UserID.String() + "@example.test"}},
		{`INSERT INTO orgs(id,name,slug) VALUES($1,'Conta',$2)`, []any{p.OrgID, p.OrgID.String()}},
		{`INSERT INTO memberships(org_id,user_id,role) VALUES($1,$2,$3)`, []any{p.OrgID, p.UserID, role}},
	} {
		if _, err := d.Admin.Exec(t.Context(), q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}
	return p
}

func TestSelectedAccountIsolationAndRevocation(t *testing.T) {
	d := testdb.Get(t)
	a, b, other := seed(t, "owner"), seed(t, "owner"), seed(t, "owner")
	if _, err := d.Admin.Exec(t.Context(), `INSERT INTO memberships(org_id,user_id,role) VALUES($1,$2,'uploader')`, b.OrgID, a.UserID); err != nil {
		t.Fatal(err)
	}
	s := New(d.App)
	h := s.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, _ := session.FromContext(r.Context())
		ap := publicid.Encode(publicid.PrefixOrg, p.OrgID)
		_, _ = w.Write([]byte(ap))
	}))
	call := func(p session.Principal, org, path, auth string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r = r.WithContext(session.ContextWithPrincipal(r.Context(), p))
		r.Header.Set(Header, org)
		r.Header.Set("Authorization", auth)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	orgA, orgB := publicid.Encode(publicid.PrefixOrg, a.OrgID), publicid.Encode(publicid.PrefixOrg, b.OrgID)
	if w := call(a, orgB, "/v1/projects", ""); w.Code != 200 || w.Body.String() != orgB {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := call(a, "", "/v1/runs", ""); w.Code != 200 || w.Body.String() != orgA {
		t.Fatal("another tab was redirected", w.Code)
	}
	for _, bad := range []string{"invalid", publicid.Encode(publicid.PrefixOrg, other.OrgID)} {
		if w := call(a, bad, "/v1/projects", ""); w.Code != 403 {
			t.Fatal("foreign account accepted", w.Code)
		}
	}
	for _, auth := range []string{"", "Bearer forged"} {
		if w := call(a, orgB, "/v1/runs", auth); w.Code != 403 {
			t.Fatal("upload-only member read results", w.Code)
		}
	}
	if _, err := d.Admin.Exec(t.Context(), `DELETE FROM memberships WHERE org_id=$1 AND user_id=$2`, b.OrgID, a.UserID); err != nil {
		t.Fatal(err)
	}
	if w := call(a, orgB, "/v1/projects", ""); w.Code != 403 {
		t.Fatal("revoked access", w.Code)
	}
	// The identity endpoint still works if a cookie points at a removed account.
	a.OrgID = b.OrgID
	for _, path := range []string{"/v1/me/sessions", "/v1/me/sessions/ses_example", "/v1/me/password"} {
		if w := call(a, orgB, path, ""); w.Code != http.StatusOK {
			t.Fatalf("identity route unavailable after removal: %s = %d", path, w.Code)
		}
	}
	router := httpx.NewRouter(limits.Must())
	s.Mount(router)
	r := httptest.NewRequest(http.MethodGet, "/v1/me/organizations", nil)
	r = r.WithContext(session.ContextWithPrincipal(r.Context(), a))
	w := httptest.NewRecorder()
	s.Middleware(router).ServeHTTP(w, r)
	var list struct {
		Items []Membership `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || len(list.Items) != 1 || list.Items[0].ID != orgA {
		t.Fatal(w.Code, w.Body.String())
	}
	if !list.Items[0].Capabilities.ManageAccount {
		t.Fatal("missing capability")
	}
	// No session never acquires authority merely by supplying an account header.
	w = httptest.NewRecorder()
	s.Middleware(router).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/me/organizations", nil))
	if w.Code != 401 {
		t.Fatal(w.Code)
	}
}

func TestRoleMatrixAndUnknownRoutes(t *testing.T) {
	for _, role := range []string{"owner", "admin", "member", "viewer", "uploader", "reconciler", "unknown"} {
		for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
			for _, path := range []string{"/v1/runs", "/v1/runs/run_example", "/v1/uploads", "/v1/rules/rule_example", "/v1/workflows/wf_example"} {
				want := role == "owner" || role == "admin" || role == "member" || role == "viewer" && method == http.MethodGet
				if got := routeAllowed(role, httptest.NewRequest(method, path, nil)); got != want {
					t.Errorf("%s %s %s = %t", role, method, path, got)
				}
			}
			for _, path := range []string{"/v1/api-keys", "/v1/billing/status", "/v1/me/export", "/v1/customizations/123", "/v1/workspace/rollback", "/v1/future-sensitive-route"} {
				want := role == "owner" || role == "admin"
				if got := routeAllowed(role, httptest.NewRequest(method, path, nil)); got != want {
					t.Errorf("%s %s %s = %t", role, method, path, got)
				}
			}
		}
	}
	if Permissions("uploader").Reconcile || !Permissions("uploader").Upload || !Permissions("reconciler").CreateProject || Permissions("viewer").Upload {
		t.Fatal("capabilities")
	}
}

func TestLastOwnerCannotBeRemovedOrDeleted(t *testing.T) {
	d := testdb.Get(t)
	p := seed(t, "owner")
	if _, err := d.Admin.Exec(t.Context(), `UPDATE orgs SET kind='team' WHERE id=$1`, p.OrgID); err != nil {
		t.Fatal(err)
	}
	store := accountpg.New(d.App, nil)
	if _, err := store.RequestDeletion(t.Context(), p.UserID, time.Now()); !errors.Is(err, account.ErrOwnerTransfer) {
		t.Fatal("last owner deletion", err)
	}
	if _, err := d.Admin.Exec(t.Context(), `DELETE FROM memberships WHERE org_id=$1 AND user_id=$2`, p.OrgID, p.UserID); !db.IsCheckViolation(err) {
		t.Fatal("last owner removal", err)
	}
	second := seed(t, "owner")
	if _, err := d.Admin.Exec(t.Context(), `INSERT INTO memberships(org_id,user_id,role) VALUES($1,$2,'owner')`, p.OrgID, second.UserID); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, u := range []uuid.UUID{p.UserID, second.UserID} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- db.WithOrgTx(t.Context(), d.App, p.OrgID, func(ctx context.Context, tx pgx.Tx) error {
				_, err := tx.Exec(ctx, `UPDATE memberships SET role='reconciler' WHERE org_id=$1 AND user_id=$2`, p.OrgID, u)
				return err
			})
		}()
	}
	wg.Wait()
	close(results)
	succeeded := 0
	for err := range results {
		if err == nil {
			succeeded++
		} else if !db.IsCheckViolation(err) && !strings.Contains(err.Error(), "deadlock") {
			t.Fatal(err)
		}
	}
	if succeeded != 1 {
		t.Fatal("concurrent owner transfers", succeeded)
	}
	var owners int
	if err := d.Admin.QueryRow(t.Context(), `SELECT count(*) FROM memberships WHERE org_id=$1 AND role='owner'`, p.OrgID).Scan(&owners); err != nil || owners != 1 {
		t.Fatal(owners, err)
	}
}
