//go:build integration

package teams

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/mmtmn/docmatch/services/api/internal/httpx"
	"github.com/mmtmn/docmatch/services/api/internal/limits"
	"github.com/mmtmn/docmatch/services/api/internal/publicid"
	"github.com/mmtmn/docmatch/services/api/internal/session"
	"github.com/mmtmn/docmatch/services/api/internal/testdb"
)

func TestTeamHTTPInvitationConsentAndMembershipLifecycle(t *testing.T) {
	s := fixture(t)
	owner, guest, other := person(t), person(t), person(t)
	router := httpx.NewRouter(limits.Must())
	s.Mount(router)
	handler := httpx.OriginCheckMiddleware([]string{"https://docmatch.example.test"})(router)
	call := func(p *session.Principal, method, path string, body any, origin string, want int) map[string]any {
		t.Helper()
		var raw []byte
		if value, ok := body.(string); ok {
			raw = []byte(value)
		} else if body != nil {
			var err error
			raw, err = json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
		}
		req := httptest.NewRequest(method, path, bytes.NewReader(raw))
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		if p != nil {
			req = req.WithContext(session.ContextWithPrincipal(req.Context(), *p))
		}
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != want {
			t.Fatalf("%s %s: got %d want %d: %s", method, path, res.Code, want, res.Body.String())
		}
		out := map[string]any{}
		if res.Body.Len() > 0 {
			if err := json.Unmarshal(res.Body.Bytes(), &out); err != nil {
				t.Fatal(err)
			}
		}
		return out
	}
	origin := "https://docmatch.example.test"
	call(nil, "GET", "/v1/team", nil, "", 401)
	call(&owner, "POST", "/v1/team", `{"name":"Empresa"}`, "https://foreign.example", 403)
	call(&owner, "POST", "/v1/team", "{broken", origin, 400)
	call(&owner, "POST", "/v1/team", `{"name":""}`, origin, 400)
	team := call(&owner, "POST", "/v1/team", `{"name":"Empresa"}`, origin, 200)
	if team["seat_price"] != float64(0) || team["kind"] != "team" {
		t.Fatal(team)
	}
	call(&owner, "GET", "/v1/team", nil, "", 200)
	email := guest.UserID.String() + "@example.test"
	call(&owner, "POST", "/v1/team/invitations", map[string]any{"email": email, "role": "owner"}, origin, 409)
	call(&owner, "POST", "/v1/team/invitations", map[string]any{"email": email, "role": "uploader"}, origin, 200)
	token := inviteToken(t, owner.OrgID, email)
	call(nil, "POST", "/v1/invitations/preview", map[string]any{"token": token}, origin, 200)
	call(&guest, "GET", "/v1/invitations/respond", nil, "", 405)
	call(&guest, "POST", "/v1/invitations/respond", map[string]any{"token": token}, origin, 400)
	call(&guest, "POST", "/v1/invitations/respond", map[string]any{"token": token, "accept": true}, "https://foreign.example", 403)
	if err := s.GetRole(t.Context(), owner.OrgID, guest.UserID); err == nil {
		t.Fatal("preview or rejected request joined account")
	}
	call(&other, "POST", "/v1/invitations/respond", map[string]any{"token": token, "accept": true}, origin, 409)
	call(&guest, "POST", "/v1/invitations/respond", map[string]any{"token": token, "accept": true}, origin, 200)
	personal := guest.OrgID
	guest.OrgID = owner.OrgID
	restricted := call(&guest, "GET", "/v1/team", nil, "", 200)
	if len(restricted["members"].([]any)) != 0 {
		t.Fatal("uploader read team directory")
	}
	call(&guest, "POST", "/v1/team/invitations", map[string]any{"email": "no@example.test", "role": "uploader"}, origin, 403)
	memberPath := "/v1/team/members/" + publicid.Encode(publicid.PrefixUser, guest.UserID)
	call(&owner, "PATCH", memberPath, `{"role":"reconciler"}`, origin, 204)
	call(&owner, "PATCH", memberPath, `{"role":"owner"}`, origin, 400)
	call(&owner, "POST", "/v1/team/leave", nil, origin, 409)
	call(&owner, "DELETE", memberPath, nil, origin, 204)
	call(&guest, "GET", "/v1/team", nil, "", 403)
	if err := s.GetRole(t.Context(), personal, guest.UserID); err != nil {
		t.Fatal("removal affected personal account", err)
	}
	call(&guest, "POST", "/v1/invitations/respond", map[string]any{"token": token, "accept": true}, origin, 409)
	// Pending invitations can only be managed in their own organization.
	pending := call(&owner, "POST", "/v1/team/invitations", map[string]any{"email": "pending@example.test", "role": "reconciler"}, origin, 200)
	path := "/v1/team/invitations/" + pending["id"].(string)
	call(&other, "DELETE", path, nil, origin, 409)
	call(&owner, "POST", path+"/resend", nil, origin, 409)
	if _, err := testdb.Get(t).Admin.Exec(t.Context(), `UPDATE team_invitations SET sent_at=now()-interval '2 minutes' WHERE org_id=$1 AND status='pending'`, owner.OrgID); err != nil {
		t.Fatal(err)
	}
	call(&owner, "POST", path+"/resend", nil, origin, 204)
	call(&owner, "DELETE", path, nil, origin, 204)
	call(&owner, "DELETE", "/v1/team/invitations/not-an-id", nil, origin, 404)
	// A new accepted full-access member can become the owner; the old owner may leave.
	otherEmail := other.UserID.String() + "@example.test"
	call(&owner, "POST", "/v1/team/invitations", map[string]any{"email": otherEmail, "role": "reconciler"}, origin, 200)
	call(&other, "POST", "/v1/invitations/respond", map[string]any{"token": inviteToken(t, owner.OrgID, otherEmail), "accept": true}, origin, 200)
	call(&owner, "POST", "/v1/team/owner", map[string]any{"user_id": publicid.Encode(publicid.PrefixUser, other.UserID)}, origin, 204)
	call(&owner, "POST", "/v1/team/leave", nil, origin, 204)
	call(&owner, "GET", "/v1/team", nil, "", 403)
}
