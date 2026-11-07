package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

type memoryAppleTokens struct {
	userID          uuid.UUID
	clientID, token string
	err             error
	queued          int
}

func (m *memoryAppleTokens) QueueRevocation(context.Context, string, string) error {
	m.queued++
	return nil
}

func (m *memoryAppleTokens) Save(_ context.Context, userID uuid.UUID, clientID, token string) error {
	if m.err != nil {
		return m.err
	}
	m.userID, m.clientID, m.token = userID, clientID, token
	return nil
}

func TestAppleCodeMustMatchIdentityAndCannotBeReplayed(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	claims := h.apple.claims(h.now, "", nil)
	other := h.apple.claims(h.now, "", map[string]any{"sub": "other-person"})
	request := func(code string) string {
		return `{"identity_token":"` + h.apple.sign(claims) + `","authorization_code":"` + code + `"}`
	}
	if rec := h.post(AppleCallbackPath, request(h.apple.issueCode(other, ""))); rec.Code != 502 {
		t.Fatalf("mismatched identity: %d %s", rec.Code, rec.Body.String())
	}
	code := h.apple.issueCode(claims, "")
	if rec := h.post(AppleCallbackPath, request(code)); rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	saved := h.svc.d.AppleTokens.(*memoryAppleTokens)
	if saved.userID == uuid.Nil || saved.clientID != "apple-client" || saved.token != "test-refresh-token" {
		t.Fatal("grant not captured")
	}
	if rec := h.post(AppleCallbackPath, request(code)); rec.Code != 502 {
		t.Fatalf("replayed code: %d", rec.Code)
	}
	saved.err = errors.New("storage unavailable")
	rec := h.post(AppleCallbackPath, request(h.apple.issueCode(claims, "")))
	if rec.Code != 500 {
		t.Fatal("storage failure", rec.Code)
	}
	for _, cookie := range rec.Result().Cookies() {
		if cookie.MaxAge > 0 {
			t.Fatal("session issued before grant stored")
		}
	}
}

func TestRevokeAppleDoesNotLeakCredentialsOrFollowRedirects(t *testing.T) {
	t.Parallel()
	status := http.StatusOK
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		if r.Form.Get("client_id") != "app-bundle" || r.Form.Get("client_secret") != "signed-app-bundle" || r.Form.Get("token") != "private-refresh" || r.Form.Get("token_type_hint") != "refresh_token" {
			t.Error("incorrect revocation form")
		}
		w.Header().Set("Location", "/never-follow")
		w.WriteHeader(status)
		_, _ = w.Write([]byte("private-refresh signed-app-bundle"))
	}))
	defer srv.Close()
	p := &Provider{Name: ProviderApple, ClientID: "web-client", RevokeURL: srv.URL, Client: srv.Client(), appleClientSecret: func(id string, _ time.Time) (string, error) { return "signed-" + id, nil }}
	if err := p.RevokeApple(t.Context(), "app-bundle", "private-refresh"); err != nil {
		t.Fatal(err)
	}
	for _, code := range []int{500, 400, 307} {
		status = code
		err := p.RevokeApple(t.Context(), "app-bundle", "private-refresh")
		if err == nil || strings.Contains(err.Error(), "private-refresh") || strings.Contains(err.Error(), "signed-app") {
			t.Fatal("unsafe error", err)
		}
	}
	if calls != 4 {
		t.Fatalf("redirect followed: %d calls", calls)
	}
}

func TestAppleLoginDuringDeletionQueuesNewGrantWithoutNewSession(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	claims := h.apple.claims(h.now, "", nil)
	login := func() *httptest.ResponseRecorder {
		return h.post(AppleCallbackPath, `{"identity_token":"`+h.apple.sign(claims)+`","authorization_code":"`+h.apple.issueCode(claims, "")+`"}`)
	}
	if rec := login(); rec.Code != 200 {
		t.Fatal(rec.Code)
	}
	user, _, _ := h.store.UserByEmail(t.Context(), "ana@example.com")
	user.DeletionRequestedAt = &h.now
	h.store.Users[user.ID] = user
	rec := login()
	if rec.Code != 403 || h.svc.d.AppleTokens.(*memoryAppleTokens).queued != 1 {
		t.Fatal("deletion login", rec.Code)
	}
	for _, cookie := range rec.Result().Cookies() {
		if cookie.MaxAge > 0 {
			t.Fatal("pending deletion issued a session")
		}
	}
}
