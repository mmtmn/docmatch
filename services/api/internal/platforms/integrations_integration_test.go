//go:build integration

package platforms

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/mmtmn/docmatch/services/api/internal/httpx"
	"github.com/mmtmn/docmatch/services/api/internal/limits"
	"github.com/mmtmn/docmatch/services/api/internal/session"
	"github.com/mmtmn/docmatch/services/api/internal/testdb"
)

func TestIntegrationInterestPersistence(t *testing.T) {
	s := fixture(t)
	d := testdb.Get(t)
	ctx := t.Context()
	must(t, func() error { _, err := d.Admin.Exec(ctx, "TRUNCATE integration_interests"); return err }())
	visitor := uuid.NewString()
	var group sync.WaitGroup
	for range 8 {
		group.Go(func() {
			if err := s.RecordIntegrationInterest(ctx, "omie", visitor); err != nil {
				t.Error(err)
			}
		})
	}
	group.Wait()
	if n := count(t, "SELECT count(*) FROM integration_interests"); n != 1 {
		t.Fatalf("repeat count = %d", n)
	}
	if err := s.RecordIntegrationInterest(ctx, "unknown", visitor); err == nil {
		t.Fatal("unknown integration accepted")
	}
	if err := s.RecordIntegrationInterest(ctx, "omie", "not-a-uuid"); err == nil {
		t.Fatal("invalid visitor accepted")
	}
	must(t, s.RecordIntegrationInterest(ctx, "clio", visitor))
	var visible int
	must(t, d.App.QueryRow(ctx, "SELECT count(*) FROM integration_interests").Scan(&visible))
	if visible != 0 {
		t.Fatal("public interest rows exposed to tenant role")
	}
	user := uuid.New()
	_, err := d.Admin.Exec(ctx, `INSERT INTO users(id,email,email_verified_at) VALUES($1,$2,now())`, user, user.String()+"@example.test")
	must(t, err)
	signed := session.ContextWithPrincipal(ctx, session.Principal{UserID: user})
	must(t, s.RecordIntegrationInterest(signed, "omie", uuid.NewString()))
	must(t, s.RecordIntegrationInterest(signed, "omie", uuid.NewString()))
	if n := count(t, "SELECT count(*) FROM integration_interests WHERE user_id=$1", user); n != 1 {
		t.Fatalf("signed-in dedup count=%d", n)
	}
	_, err = d.Admin.Exec(ctx, "UPDATE integration_interests SET updated_at=now()-interval '366 days' WHERE user_id IS NULL")
	must(t, err)
	must(t, s.Cleanup(ctx))
	if n := count(t, "SELECT count(*) FROM integration_interests"); n != 1 {
		t.Fatalf("retention count=%d", n)
	}
}

func TestIntegrationInterestHTTP(t *testing.T) {
	s := fixture(t)
	r := httpx.NewRouter(limits.Must())
	s.Mount(r)
	var items []struct {
		ID string `json:"id"`
	}
	must(t, json.Unmarshal(integrationsJSON, &items))
	if len(items) != 10 {
		t.Fatalf("catalog size=%d", len(items))
	}
	seen := map[string]bool{}
	for _, item := range items {
		if seen[item.ID] {
			t.Fatal("duplicate integration")
		}
		seen[item.ID] = true
		body, _ := json.Marshal(map[string]string{"integration": item.ID, "visitor_id": uuid.NewString()})
		request := httptest.NewRequest(http.MethodPost, "/v1/integration-interests", bytes.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		result := httptest.NewRecorder()
		r.ServeHTTP(result, request)
		if result.Code != http.StatusAccepted {
			t.Fatalf("%s: %d %s", item.ID, result.Code, result.Body.String())
		}
	}
}
