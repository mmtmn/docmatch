//go:build integration

package projects

import (
	"testing"

	"github.com/google/uuid"

	"github.com/mmtmn/docmatch/services/api/internal/config"
	"github.com/mmtmn/docmatch/services/api/internal/runs/apix"
)

func TestProjectRolloutPausesNewWorkButRetainsReadsAndDeletion(t *testing.T) {
	s := pipeline(t).s
	owner, other := seed(t, uuid.Nil, "owner"), seed(t, uuid.Nil, "owner")
	call := projectHTTP(t, s)
	created := call(owner, "POST", "/v1/projects", map[string]string{"name": "Preservado"}, 201)
	path := "/v1/projects/" + created["id"].(string)
	s.SetRollout(&config.AccountRollout{Accounts: map[uuid.UUID]bool{other.OrgID: true}})
	call(other, "POST", "/v1/projects", map[string]string{"name": "Liberado"}, 201)
	key := uuid.New()
	for _, actor := range []apix.Principal{owner, {OrgID: owner.OrgID, APIKeyID: &key}} {
		got := call(actor, "POST", "/v1/projects", map[string]string{"name": "Bloqueado"}, 409)
		if got["code"] != "feature_paused" {
			t.Fatal(got)
		}
		call(actor, "PATCH", path, map[string]any{"version": 1, "name": "Mudança"}, 409)
		call(actor, "POST", path+"/runs/estimate", map[string]any{"revision": 1}, 409)
		call(actor, "POST", path+"/rules", map[string]string{"title": "Teste", "body": "Regra"}, 409)
		got = call(actor, "GET", path, nil, 200)
		caps := got["capabilities"].(map[string]any)
		if caps["reconcile"] != false || caps["upload"] != false || caps["read_results"] != true || got["name"] != "Preservado" {
			t.Fatal(got)
		}
		call(actor, "GET", path+"/runs", nil, 200)
	}
	call(owner, "DELETE", path+"?version=1", nil, 204)
}
