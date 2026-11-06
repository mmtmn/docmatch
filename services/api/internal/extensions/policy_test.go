package extensions

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/mmtmn/docmatch/services/api/internal/config"
	"github.com/mmtmn/docmatch/services/api/internal/crypto"
	"github.com/mmtmn/docmatch/services/api/internal/publicid"
)

func TestCredentialShapeAndPKCE(t *testing.T) {
	if _, err := New(&config.Config{}, nil); err == nil {
		t.Fatal("short key accepted")
	}
	for _, v := range []string{"", strings.Repeat("a", 42), strings.Repeat("a", 129), strings.Repeat("a", 42) + "!"} {
		if verifierValid(v) {
			t.Fatal("invalid verifier accepted")
		}
	}
	for _, v := range []string{strings.Repeat("a", 43), strings.Repeat("-._~aAZ09", 14)} {
		if !verifierValid(v) {
			t.Fatal("valid verifier rejected")
		}
	}
	// RFC 7636 Appendix B published vector.
	if challenge("dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk") != "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM" {
		t.Fatal("PKCE S256 mismatch")
	}
	for _, raw := range []string{"", accessPrefix + uuid.NewString() + ".short", accessPrefix + "invalid." + crypto.RandomToken(32), accessPrefix + strings.ToUpper(uuid.NewString()) + "." + crypto.RandomToken(32)} {
		if _, err := tokenID(raw, accessPrefix); err == nil {
			t.Fatal("invalid token shape accepted")
		}
	}
	if nullableID(uuid.Nil) != nil {
		t.Fatal("null organization")
	}
}
func TestExtensionOriginExceptionDoesNotBypassCookieCSRF(t *testing.T) {
	for _, tc := range []struct {
		method, path, auth, cookie string
		want                       bool
	}{
		{"POST", "/v1/extensions/token", "", "", true},
		{"GET", "/v1/extensions/token", "", "", false},
		{"POST", "/v1/extensions/authorize", "", "", false},
		{"POST", "/v1/extensions/token", "", "dm_device=browser", false},
		{"POST", "/v1/projects", "Bearer " + accessPrefix + "invalid", "", true},
		{"POST", "/v1/projects", "Bearer " + accessPrefix + "invalid", "dm_session=browser", false},
		{"POST", "/v1/projects", "Bearer api-key", "", false},
	} {
		r := httptest.NewRequest(tc.method, tc.path, nil)
		r.Header.Set("Authorization", tc.auth)
		r.Header.Set("Cookie", tc.cookie)
		if OriginExempt(r) != tc.want {
			t.Fatalf("wrong origin policy for %s %s", tc.method, tc.path)
		}
	}
}
func TestExtensionRouteBoundary(t *testing.T) {
	for _, tc := range []struct {
		method, path string
		want         bool
	}{
		{"GET", "/v1/me", true}, {"DELETE", "/v1/me", false},
		{"GET", "/v1/me/organizations", true}, {"POST", "/v1/me/organizations", false},
		{"GET", "/v1/uploads/" + publicid.Encode("upl", uuid.New()), true}, {"GET", "/v1/uploads/not-id", false},
		{"POST", "/v1/uploads/" + publicid.Encode("upl", uuid.New()), false},
		{"GET", "/v1/projects", true}, {"GET", "/v1/projects-not-actually", false},
		{"GET", "/v1/extensions/revoke", false}, {"POST", "/v1/extensions/revoke", true},
		{"GET", "/v1/team", false}, {"DELETE", "/v1/me/sessions", false},
	} {
		if allowed(httptest.NewRequest(tc.method, tc.path, nil)) != tc.want {
			t.Fatalf("wrong capability for %s %s", tc.method, tc.path)
		}
	}
}
