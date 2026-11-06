package auth

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDisposableEmailCannotQueueChallengeOrCreateOAuthAccount(t *testing.T) {
	h := newHarness(t)
	response := h.post("/v1/auth/email/start", `{"email":"someone@mailinator.com"}`)
	if response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), "disposable_email") {
		t.Fatalf("challenge: %d %s", response.Code, response.Body.String())
	}
	for _, provider := range []string{ProviderGoogle, ProviderApple, ProviderEmail} {
		_, created, err := h.svc.signup(t.Context(), httptest.NewRequest(http.MethodPost, "/", nil), Identity{Provider: provider, Email: "someone@sub.mailinator.com", EmailVerified: true})
		if !errors.Is(err, ErrDisposableEmail) || created {
			t.Fatal("signup bypass", provider, err)
		}
	}
}
