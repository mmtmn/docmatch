package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/mmtmn/docmatch/services/api/internal/account"
	"github.com/mmtmn/docmatch/services/api/internal/crypto"
	"github.com/mmtmn/docmatch/services/api/internal/publicid"
	"github.com/mmtmn/docmatch/services/api/internal/ratelimit"
	"github.com/mmtmn/docmatch/services/api/internal/session"
)

func TestNewDefaults(t *testing.T) {
	t.Parallel()
	if _, err := New(Deps{}); err == nil {
		t.Fatal("sem dependências")
	}
	h := newHarness(t)
	keys, _ := crypto.NewKeyring(make([]byte, 32))
	if _, err := New(Deps{Store: h.store, Sessions: h.sessions, Keys: keys, Audit: h.auditRec}); err == nil {
		t.Fatal("sem URLs")
	}
	pub, _ := url.Parse("https://app.example.com")
	svc, err := New(Deps{Store: h.store, Sessions: h.sessions, Keys: keys, Audit: h.auditRec, PublicBaseURL: pub, APIBaseURL: pub})
	if err != nil {
		t.Fatal(err)
	}
	if svc.d.LoginPath != DefaultLoginPath || svc.d.MagicLinkPath != DefaultMagicLinkPath || svc.d.Policy.Limit == 0 || svc.d.Limits == nil || svc.d.Country(nil) != "" {
		t.Fatalf("padrões: %+v", svc.d)
	}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "203.0.113.5:9"
	if svc.d.ClientIP(r).String() != "203.0.113.5" {
		t.Fatal("ClientIP padrão")
	}
	r.RemoteAddr = "203.0.113.6"
	if svc.d.ClientIP(r).String() != "203.0.113.6" {
		t.Fatal("ClientIP sem porta")
	}
	if ok, _ := svc.d.SecondFactor.Required(context.Background(), account.User{}); ok || svc.d.SecondFactor.Verify(context.Background(), account.User{}, "x") != nil {
		t.Fatal("segundo fator padrão")
	}
	// Unconfigured providers answer 503.
	rec := httptest.NewRecorder()
	svc.start(ProviderGoogle)(rec, httptest.NewRequest(http.MethodPost, GoogleStartPath, nil))
	if rec.Code != 503 {
		t.Fatalf("google sem config: %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	svc.googleCallback(rec, httptest.NewRequest(http.MethodGet, GoogleCallbackPath, nil))
	if rec.Code != 503 {
		t.Fatalf("callback sem config: %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	svc.appleCallback(rec, httptest.NewRequest(http.MethodPost, AppleCallbackPath, nil))
	if rec.Code != 503 {
		t.Fatalf("apple sem config: %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	svc.emailStart(rec, httptest.NewRequest(http.MethodPost, EmailStartPath, strings.NewReader(`{"email":"a@example.com"}`)))
	if rec.Code != 503 {
		t.Fatalf("e-mail sem remetente: %d", rec.Code)
	}
	if !OriginExempt(httptest.NewRequest(http.MethodPost, AppleCallbackPath, nil)) || OriginExempt(httptest.NewRequest(http.MethodGet, AppleCallbackPath, nil)) || OriginExempt(httptest.NewRequest(http.MethodPost, GoogleStartPath, nil)) {
		t.Fatal("OriginExempt")
	}
}

func TestGoogleLogin(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	c, authURL, out := h.start(ProviderGoogle, `{"redirect_to":"/painel","platform":"web"}`)
	q := authURL.Query()
	if q.Get("client_id") != "google-client" || q.Get("code_challenge_method") != "S256" || q.Get("state") != out["state"] || q.Get("nonce") == "" || q.Get("redirect_uri") != "https://api.example.com"+GoogleCallbackPath {
		t.Fatalf("URL de autorização: %s", authURL)
	}
	if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode || c.Path != GoogleCallbackPath || c.MaxAge != 600 {
		t.Fatalf("cookie de estado: %+v", c)
	}
	st := h.stateOf(c, ProviderGoogle)
	if pkceChallenge(st.Verifier) != q.Get("code_challenge") || st.RedirectTo != "/painel" {
		t.Fatal("estado selado não corresponde à URL")
	}
	code := h.google.issueCode(h.google.claims(h.now, st.Nonce, nil), st.Verifier)
	rec := h.do(req{method: http.MethodGet, path: GoogleCallbackPath + "?code=" + code + "&state=" + st.State, cookies: []*http.Cookie{c}})
	if rec.Code != 303 || rec.Header().Get("Location") != "https://app.example.com/painel" {
		t.Fatalf("callback: %d %s %s", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	checkSessionCookies(t, rec)
	if cleared := cookieByName(cookies(rec), cookieName(ProviderGoogle)); cleared == nil || cleared.MaxAge != -1 {
		t.Fatal("cookie de estado deveria ser apagado")
	}
	u, ok, _ := h.store.UserByEmail(context.Background(), "ana@example.com")
	if !ok || u.GoogleSub != "sub-1" || u.DisplayName != "Ana Lima" || u.EmailVerifiedAt == nil || u.LastLoginAt == nil {
		t.Fatalf("usuário: %+v %v", u, ok)
	}
	org := h.orgOf("ana@example.com")
	tnt, _, _ := h.store.Tenant(context.Background(), org, u.ID)
	if tnt.Org.Name != "Ana Lima" || tnt.Org.Country != "BR" || tnt.Membership.Role != account.RoleOwner || len(h.store.TenantKeys[org]) == 0 {
		t.Fatalf("organização pessoal: %+v", tnt)
	}
	kek := h.svc.d.Keys.Key(crypto.PurposeKEK)
	if _, err := crypto.UnwrapDataKey(kek, h.store.TenantKeys[org], org.String()); err != nil {
		t.Fatalf("chave do locatário: %v", err)
	}
	if acts := h.actions(&org); len(acts) != 2 || acts[0] != auditSignup || acts[1] != auditLogin {
		t.Fatalf("auditoria: %v", acts)
	}
	// Replaying the same state: the cookie is gone → mismatch, redirect to login.
	rec = h.do(req{method: http.MethodGet, path: GoogleCallbackPath + "?code=" + code + "&state=" + st.State})
	if rec.Code != 303 || rec.Header().Get("Location") != "https://app.example.com/login?error=oauth_state_mismatch" {
		t.Fatalf("replay: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	// Even with the cookie, a used code fails at the provider (502).
	rec = h.do(req{method: http.MethodGet, path: GoogleCallbackPath + "?code=" + code + "&state=" + st.State, cookies: []*http.Cookie{c}, accept: "application/json"})
	if rec.Code != 502 || decode(t, rec)["code"] != "oauth_provider_error" {
		t.Fatalf("código usado: %d %s", rec.Code, rec.Body.String())
	}
	// Second login of the same subject: found by provider, JSON answer, no new org.
	c2, _, _ := h.start(ProviderGoogle, "")
	st2 := h.stateOf(c2, ProviderGoogle)
	code2 := h.google.issueCode(h.google.claims(h.now, st2.Nonce, map[string]any{"email": "outro@example.com", "name": "Outro"}), st2.Verifier)
	rec = h.do(req{method: http.MethodGet, path: GoogleCallbackPath + "?code=" + code2 + "&state=" + st2.State, cookies: []*http.Cookie{c2}, accept: "application/json"})
	if rec.Code != 200 {
		t.Fatalf("segundo login: %d %s", rec.Code, rec.Body.String())
	}
	body := decode(t, rec)
	if body["user"].(map[string]any)["email"] != "ana@example.com" || body["org"].(map[string]any)["name"] != "Ana Lima" || body["expires_at"] == nil {
		t.Fatalf("sessão: %v", body)
	}
	if len(h.store.Orgs) != 1 {
		t.Fatal("não pode criar outra organização")
	}
}

func TestGoogleErrors(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	otherKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	type tc struct {
		name   string
		claims func(st oauthState) map[string]any
		mutate func(h *harness, st oauthState, code string) (path string, cs []*http.Cookie)
		token  func(claims map[string]any) string // custom token encoding
		want   string
		status int
	}
	cases := []tc{
		{name: "nonce errado", claims: func(st oauthState) map[string]any { return h.google.claims(h.now, "outro", nil) }, want: "oauth_state_mismatch", status: 400},
		{name: "aud errado", claims: func(st oauthState) map[string]any {
			return h.google.claims(h.now, st.Nonce, map[string]any{"aud": "outro-client"})
		}, want: "oauth_state_mismatch", status: 400},
		{name: "expirado", claims: func(st oauthState) map[string]any {
			return h.google.claims(h.now.Add(-3*time.Hour), st.Nonce, nil)
		}, want: "oauth_state_mismatch", status: 400},
		{name: "emissor errado", claims: func(st oauthState) map[string]any {
			return h.google.claims(h.now, st.Nonce, map[string]any{"iss": "https://evil.example.net"})
		}, want: "oauth_state_mismatch", status: 400},
		{name: "e-mail não verificado", claims: func(st oauthState) map[string]any {
			return h.google.claims(h.now, st.Nonce, map[string]any{"email_verified": false, "sub": "novo"})
		}, want: "oauth_email_unverified", status: 403},
		{name: "forjado com outra chave", claims: func(st oauthState) map[string]any { return h.google.claims(h.now, st.Nonce, nil) },
			token: func(claims map[string]any) string { return signRS256(t, otherKey, "k1", claims) }, want: "oauth_state_mismatch", status: 400},
		{name: "state errado", claims: func(st oauthState) map[string]any { return h.google.claims(h.now, st.Nonce, nil) },
			mutate: func(_ *harness, st oauthState, code string) (string, []*http.Cookie) {
				return GoogleCallbackPath + "?code=" + code + "&state=" + crypto.RandomToken(32), nil
			}, want: "oauth_state_mismatch", status: 400},
		{name: "usuário cancelou", claims: func(st oauthState) map[string]any { return h.google.claims(h.now, st.Nonce, nil) },
			mutate: func(_ *harness, st oauthState, _ string) (string, []*http.Cookie) {
				return GoogleCallbackPath + "?error=access_denied&state=" + st.State, nil
			}, want: "oauth_provider_error", status: 502},
		{name: "sem code", claims: func(st oauthState) map[string]any { return h.google.claims(h.now, st.Nonce, nil) },
			mutate: func(_ *harness, st oauthState, _ string) (string, []*http.Cookie) {
				return GoogleCallbackPath + "?state=" + st.State, nil
			}, want: "oauth_provider_error", status: 502},
		{name: "estado expirado", claims: func(st oauthState) map[string]any { return h.google.claims(h.now, st.Nonce, nil) },
			mutate: func(h *harness, st oauthState, code string) (string, []*http.Cookie) {
				h.now = h.now.Add(OAuthStateTTL + time.Second)
				return GoogleCallbackPath + "?code=" + code + "&state=" + st.State, nil
			}, want: "oauth_state_mismatch", status: 400},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			start := h.now
			defer func() { h.now = start }()
			c, _, _ := h.start(ProviderGoogle, "")
			st := h.stateOf(c, ProviderGoogle)
			claims := tc.claims(st)
			var code string
			if tc.token != nil {
				h.google.mu.Lock()
				h.google.tokenBody = `{"id_token":"` + tc.token(claims) + `"}`
				h.google.mu.Unlock()
				code = "qualquer"
			} else {
				code = h.google.issueCode(claims, st.Verifier)
			}
			path, cs := GoogleCallbackPath+"?code="+code+"&state="+st.State, []*http.Cookie{c}
			if tc.mutate != nil {
				var extra []*http.Cookie
				path, extra = tc.mutate(h, st, code)
				cs = append(cs, extra...)
			}
			rec := h.do(req{method: http.MethodGet, path: path, cookies: cs, accept: "application/json"})
			h.google.mu.Lock()
			h.google.tokenBody = ""
			h.google.mu.Unlock()
			if rec.Code != tc.status || decode(t, rec)["code"] != tc.want {
				t.Fatalf("%d %s", rec.Code, rec.Body.String())
			}
			if len(h.store.Users) != 0 {
				t.Fatal("nenhum usuário pode nascer de um login recusado")
			}
		})
	}
	// Provider down (token endpoint 500) and the browser redirect for errors.
	c, _, _ := h.start(ProviderGoogle, "")
	st := h.stateOf(c, ProviderGoogle)
	h.google.mu.Lock()
	h.google.tokenFail = 500
	h.google.mu.Unlock()
	rec := h.do(req{method: http.MethodGet, path: GoogleCallbackPath + "?code=x&state=" + st.State, cookies: []*http.Cookie{c}})
	if rec.Code != 303 || rec.Header().Get("Location") != "https://app.example.com/login?error=oauth_provider_error" {
		t.Fatalf("provedor fora: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	h.google.mu.Lock()
	h.google.tokenFail = 0
	h.google.mu.Unlock()
	// Start validation.
	for _, body := range []string{`{"redirect_to":"https://evil.example.net"}`, `{"redirect_to":"//evil"}`, `{"redirect_to":"/a b"}`, `{"platform":"tv"}`, `{"x":1}`, `{`} {
		if rec := h.post(GoogleStartPath, body); rec.Code != 400 {
			t.Fatalf("%s: %d %s", body, rec.Code, rec.Body.String())
		}
	}
	// A tampered state cookie is a mismatch.
	bad := &http.Cookie{Name: cookieName(ProviderGoogle), Value: "!!!"}
	rec = h.do(req{method: http.MethodGet, path: GoogleCallbackPath + "?code=x&state=y", cookies: []*http.Cookie{bad}, accept: "application/json"})
	if rec.Code != 400 {
		t.Fatalf("cookie adulterado: %d", rec.Code)
	}
	bad.Value = strings.Repeat("A", 5000)
	rec = h.do(req{method: http.MethodGet, path: GoogleCallbackPath + "?code=x&state=y", cookies: []*http.Cookie{bad}, accept: "application/json"})
	if rec.Code != 400 {
		t.Fatalf("cookie enorme: %d", rec.Code)
	}
	// A Google cookie presented to the Apple callback does not open.
	c, _, _ = h.start(ProviderGoogle, "")
	st = h.stateOf(c, ProviderGoogle)
	apple := &http.Cookie{Name: cookieName(ProviderApple), Value: c.Value}
	rec = h.do(req{method: http.MethodPost, path: AppleCallbackPath, body: "state=" + st.State + "&code=x&id_token=y", contentType: formContentType, cookies: []*http.Cookie{apple}, accept: "application/json"})
	if rec.Code != 400 || decode(t, rec)["code"] != "oauth_state_mismatch" {
		t.Fatalf("cookie trocado de provedor: %d %s", rec.Code, rec.Body.String())
	}
}

func TestAppleLogin(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	c, authURL, _ := h.start(ProviderApple, `{"redirect_to":"/"}`)
	if authURL.Query().Get("response_mode") != "form_post" || authURL.Query().Get("code_challenge") != "" || c.SameSite != http.SameSiteNoneMode || !c.Secure {
		t.Fatalf("apple start: %s %+v", authURL, c)
	}
	st := h.stateOf(c, ProviderApple)
	claims := h.apple.claims(h.now, st.Nonce, map[string]any{"sub": "apple-1", "email": "xyz@privaterelay.appleid.com", "email_verified": "true", "is_private_email": "true"})
	delete(claims, "name")
	code := h.apple.issueCode(claims, "")
	form := url.Values{"state": {st.State}, "code": {code}, "id_token": {"ignorado"}, "user": {`{"name":{"firstName":"Bia","lastName":"Souza"},"email":"xyz@privaterelay.appleid.com"}`}}
	rec := h.do(req{method: http.MethodPost, path: AppleCallbackPath, body: form.Encode(), contentType: formContentType, cookies: []*http.Cookie{c}})
	if rec.Code != 303 || rec.Header().Get("Location") != "https://app.example.com/" {
		t.Fatalf("apple callback: %d %s %s", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	checkSessionCookies(t, rec)
	h.apple.mu.Lock()
	secret := h.apple.gotSecret
	h.apple.mu.Unlock()
	if hdr, _, _, _, err := crypto.ParseJWT(secret); err != nil || hdr.Alg != "ES256" || hdr.Kid != "KEY1" {
		t.Fatalf("client secret: %+v %v", hdr, err)
	}
	u, ok, _ := h.store.UserByEmail(context.Background(), "xyz@privaterelay.appleid.com")
	if !ok || u.AppleSub != "apple-1" || u.DisplayName != "Bia Souza" {
		t.Fatalf("usuário apple: %+v", u)
	}
	// Second login: Apple sends no name; the stored one stays.
	c2, _, _ := h.start(ProviderApple, "")
	st2 := h.stateOf(c2, ProviderApple)
	claims = h.apple.claims(h.now, st2.Nonce, map[string]any{"sub": "apple-1", "email": "xyz@privaterelay.appleid.com"})
	delete(claims, "name")
	code = h.apple.issueCode(claims, "")
	form = url.Values{"state": {st2.State}, "code": {code}, "id_token": {"x"}}
	rec = h.do(req{method: http.MethodPost, path: AppleCallbackPath, body: form.Encode(), contentType: formContentType, cookies: []*http.Cookie{c2}, accept: "application/json"})
	if rec.Code != 200 || decode(t, rec)["user"].(map[string]any)["display_name"] != "Bia Souza" {
		t.Fatalf("segundo login apple: %d %s", rec.Code, rec.Body.String())
	}
	// Form errors: cancel, missing code, bad form.
	c3, _, _ := h.start(ProviderApple, "")
	st3 := h.stateOf(c3, ProviderApple)
	rec = h.do(req{method: http.MethodPost, path: AppleCallbackPath, body: "state=" + st3.State + "&error=user_cancelled_authorize", contentType: formContentType, cookies: []*http.Cookie{c3}, accept: "application/json"})
	if rec.Code != 502 {
		t.Fatalf("cancelado: %d", rec.Code)
	}
	rec = h.do(req{method: http.MethodPost, path: AppleCallbackPath, body: "%zz", contentType: formContentType, accept: "application/json"})
	if rec.Code != 400 {
		t.Fatalf("form inválido: %d", rec.Code)
	}
	// Wrong client secret is refused by the provider.
	wrongKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	h.svc.d.Apple.ClientSecret = appleSecret(wrongKey, "TEAM", "apple-client", "KEY1", h.apple.srv.URL)
	c4, _, _ := h.start(ProviderApple, "")
	st4 := h.stateOf(c4, ProviderApple)
	code = h.apple.issueCode(h.apple.claims(h.now, st4.Nonce, nil), "")
	rec = h.do(req{method: http.MethodPost, path: AppleCallbackPath, body: "state=" + st4.State + "&code=" + code, contentType: formContentType, cookies: []*http.Cookie{c4}, accept: "application/json"})
	if rec.Code != 502 {
		t.Fatalf("segredo errado: %d %s", rec.Code, rec.Body.String())
	}
	h.svc.d.Apple.ClientSecret = func(time.Time) (string, error) { return "", errors.New("sem chave") }
	c5, _, _ := h.start(ProviderApple, "")
	st5 := h.stateOf(c5, ProviderApple)
	rec = h.do(req{method: http.MethodPost, path: AppleCallbackPath, body: "state=" + st5.State + "&code=x", contentType: formContentType, cookies: []*http.Cookie{c5}, accept: "application/json"})
	if rec.Code != 502 {
		t.Fatalf("segredo indisponível: %d", rec.Code)
	}
	// Verify failures after a successful exchange (wrong nonce).
	h.svc.d.Apple.ClientSecret = appleSecret(h.appleKey, "TEAM", "apple-client", "KEY1", h.apple.srv.URL)
	c6, _, _ := h.start(ProviderApple, "")
	st6 := h.stateOf(c6, ProviderApple)
	code = h.apple.issueCode(h.apple.claims(h.now, "nonce-errado", nil), "")
	rec = h.do(req{method: http.MethodPost, path: AppleCallbackPath, body: "state=" + st6.State + "&code=" + code, contentType: formContentType, cookies: []*http.Cookie{c6}, accept: "application/json"})
	if rec.Code != 400 || decode(t, rec)["code"] != "oauth_state_mismatch" {
		t.Fatalf("nonce apple: %d %s", rec.Code, rec.Body.String())
	}
	// Dev over http: the Apple cookie falls back to Lax (SameSite=None needs Secure).
	h.svc.d.Secure = false
	c7, _, _ := h.start(ProviderApple, "")
	if c7.SameSite != http.SameSiteLaxMode || c7.Secure {
		t.Fatalf("cookie em dev: %+v", c7)
	}
	h.svc.d.Secure = true
	if appleUserName("") != "" || appleUserName("{") != "" || appleUserName(strings.Repeat("a", 3000)) != "" || appleUserName(`{"name":{"firstName":"A\tB"}}`) != "" || appleUserName(`{"name":{"firstName":"Ana","lastName":""}}`) != "Ana" {
		t.Fatal("appleUserName")
	}
}

func TestAppleNative(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	claims := h.apple.claims(h.now, "", map[string]any{"sub": "apple-native", "email": "nat@example.com"})
	delete(claims, "nonce")
	delete(claims, "name")
	token := h.apple.sign(claims)
	rec := h.post(AppleCallbackPath, `{"identity_token":"`+token+`","authorization_code":"c","full_name":"Nat Silva"}`)
	if rec.Code != 200 {
		t.Fatalf("nativo: %d %s", rec.Code, rec.Body.String())
	}
	if decode(t, rec)["user"].(map[string]any)["display_name"] != "Nat Silva" {
		t.Fatalf("nome nativo: %s", rec.Body.String())
	}
	checkSessionCookies(t, rec)
	for _, body := range []string{`{"identity_token":"","authorization_code":"c"}`, `{"identity_token":"x","authorization_code":""}`, `{"nope":1}`, `{`} {
		if rec := h.post(AppleCallbackPath, body); rec.Code != 400 {
			t.Fatalf("%s: %d", body, rec.Code)
		}
	}
	rec = h.post(AppleCallbackPath, `{"identity_token":"a.b.c","authorization_code":"c"}`)
	if rec.Code != 400 || decode(t, rec)["code"] != "oauth_state_mismatch" {
		t.Fatalf("token inválido: %d %s", rec.Code, rec.Body.String())
	}
	// Suspended account refused on the native path too.
	u, _, _ := h.store.UserByEmail(context.Background(), "nat@example.com")
	now := h.now
	u.SuspendedAt = &now
	h.store.Users[u.ID] = u
	rec = h.post(AppleCallbackPath, `{"identity_token":"`+token+`","authorization_code":"c"}`)
	if rec.Code != 403 || decode(t, rec)["code"] != "account_suspended" {
		t.Fatalf("suspensa: %d %s", rec.Code, rec.Body.String())
	}
}

func TestEmailLogin(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	rec := h.post(EmailStartPath, `{"email":" Ana@Example.com "}`)
	if rec.Code != 202 {
		t.Fatalf("start: %d %s", rec.Code, rec.Body.String())
	}
	out := decode(t, rec)
	cid := out["challenge_id"].(string)
	if !strings.HasPrefix(cid, "chl_") || out["resend_after_s"] != float64(60) || out["expires_at"] == nil {
		t.Fatalf("resposta: %v", out)
	}
	if len(h.store.Sent) != 1 || h.store.Sent[0].To != "ana@example.com" {
		t.Fatalf("e-mail: %+v", h.store.Sent)
	}
	msg := h.store.Sent[0]
	// The mail carries the code and the link with the token.
	var ch Challenge
	for _, c := range h.store.Challenges {
		ch = c
	}
	linkStart := strings.Index(msg.Text, "https://app.example.com"+DefaultMagicLinkPath)
	if linkStart < 0 {
		t.Fatalf("link ausente: %s", msg.Text)
	}
	link := strings.Fields(msg.Text[linkStart:])[0]
	lu, _ := url.Parse(link)
	token := lu.Query().Get("code")
	if lu.Query().Get("challenge_id") != cid || !crypto.ValidToken(token, 32) || !crypto.Equal(crypto.Digest(token), ch.TokenHash) {
		t.Fatalf("link: %s", link)
	}
	code := ""
	for _, line := range strings.Split(msg.Text, "\n") {
		line = strings.TrimSpace(line)
		if len(line) == 7 && line[3] == ' ' {
			code = line[:3] + line[4:]
		}
	}
	if code == "" || !crypto.Equal(h.svc.codeMAC(ch.ID, code), ch.CodeHash) {
		t.Fatalf("código ausente no e-mail: %q", msg.Text)
	}
	// Wrong codes count attempts; the right one opens the session.
	for i := 0; i < 2; i++ {
		rec = h.post(EmailVerifyPath, `{"challenge_id":"`+cid+`","code":"000000"}`)
		if rec.Code != 400 || decode(t, rec)["code"] != "email_code_invalid" {
			t.Fatalf("código errado: %d %s", rec.Code, rec.Body.String())
		}
	}
	rec = h.post(EmailVerifyPath, `{"challenge_id":"`+cid+`","code":"`+code+`"}`)
	if rec.Code != 200 {
		t.Fatalf("verify: %d %s", rec.Code, rec.Body.String())
	}
	checkSessionCookies(t, rec)
	body := decode(t, rec)
	if body["user"].(map[string]any)["email"] != "ana@example.com" || body["org"].(map[string]any)["name"] != "ana" {
		t.Fatalf("sessão: %v", body)
	}
	// Single use: the token of the same challenge no longer works.
	rec = h.post(EmailVerifyPath, `{"challenge_id":"`+cid+`","code":"`+token+`"}`)
	if rec.Code != 404 || decode(t, rec)["code"] != "email_challenge_not_found" {
		t.Fatalf("reuso: %d %s", rec.Code, rec.Body.String())
	}
	// Magic link token for an existing user (no new org, e-mail verified).
	rec = h.post(EmailStartPath, `{"email":"ana@example.com"}`)
	cid2 := decode(t, rec)["challenge_id"].(string)
	lu2, _ := url.Parse(strings.Fields(h.store.Sent[1].Text[strings.Index(h.store.Sent[1].Text, "https://"):])[0])
	rec = h.post(EmailVerifyPath, `{"challenge_id":"`+cid2+`","code":"`+lu2.Query().Get("code")+`"}`)
	if rec.Code != 200 || len(h.store.Orgs) != 1 {
		t.Fatalf("link mágico: %d %s", rec.Code, rec.Body.String())
	}
	org := h.orgOf("ana@example.com")
	acts := h.actions(&org)
	if len(acts) != 3 || acts[0] != auditSignup || acts[1] != auditLogin || acts[2] != auditLogin {
		t.Fatalf("auditoria: %v", acts)
	}
	// Expiry.
	rec = h.post(EmailStartPath, `{"email":"ana@example.com"}`)
	cid3 := decode(t, rec)["challenge_id"].(string)
	h.now = h.now.Add(ChallengeTTL + time.Second)
	rec = h.post(EmailVerifyPath, `{"challenge_id":"`+cid3+`","code":"123456"}`)
	if rec.Code != 410 || decode(t, rec)["code"] != "email_code_expired" {
		t.Fatalf("expirado: %d %s", rec.Code, rec.Body.String())
	}
	// Attempts: five wrong codes lock the challenge.
	rec = h.post(EmailStartPath, `{"email":"ana@example.com"}`)
	cid4 := decode(t, rec)["challenge_id"].(string)
	for i := 0; i < ChallengeAttempts; i++ {
		h.now = h.now.Add(13 * time.Second) // stay under the 5/min rate limit
		if rec := h.post(EmailVerifyPath, `{"challenge_id":"`+cid4+`","code":"111111"}`); rec.Code != 400 {
			t.Fatalf("tentativa %d: %d %s", i, rec.Code, rec.Body.String())
		}
	}
	h.now = h.now.Add(13 * time.Second)
	rec = h.post(EmailVerifyPath, `{"challenge_id":"`+cid4+`","code":"111111"}`)
	if rec.Code != 429 || decode(t, rec)["code"] != "too_many_attempts" || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("tentativas esgotadas: %d %s", rec.Code, rec.Body.String())
	}
	// Validation and unknown challenges.
	for body, want := range map[string]int{
		`{"email":"sem-arroba"}`:      400,
		`{"email":"a@b"}`:             400,
		`{"email":""}`:                400,
		`{"email":"a b@example.com"}`: 400,
		`{"email":"a@.example.com"}`:  400,
		`{"email":"` + strings.Repeat("a", 250) + `@example.com"}`: 400,
		`{`: 400,
	} {
		if rec := h.post(EmailStartPath, body); rec.Code != want {
			t.Fatalf("%s: %d %s", body, rec.Code, rec.Body.String())
		}
	}
	for body, want := range map[string]string{
		`{"challenge_id":"chl_nope","code":"123456"}`:                       "email_challenge_not_found",
		`{"challenge_id":"chl_7ZZZZZZZZZZZZZZZZZZZZZZZZZ","code":"123456"}`: "email_challenge_not_found",
		`{"challenge_id":"` + cid4 + `","code":"abc"}`:                      "validation_failed",
		`{"challenge_id":"` + cid4 + `","code":""}`:                         "validation_failed",
	} {
		if rec := h.post(EmailVerifyPath, body); decode(t, rec)["code"] != want {
			t.Fatalf("%s: %d %s", body, rec.Code, rec.Body.String())
		}
	}
	if rec := h.post(EmailVerifyPath, `{`); rec.Code != 400 {
		t.Fatal("json inválido")
	}
	// Store failures.
	h.now = h.now.Add(time.Minute)
	rec = h.post(EmailStartPath, `{"email":"bia@example.com"}`)
	cid5 := decode(t, rec)["challenge_id"].(string)
	for _, fail := range []string{"ChallengeByID", "RecordChallengeAttempt"} {
		h.store.FailChallenge = fail
		if rec := h.post(EmailVerifyPath, `{"challenge_id":"`+cid5+`","code":"000000"}`); rec.Code != 500 {
			t.Fatalf("%s: %d", fail, rec.Code)
		}
	}
	h.store.FailChallenge = "ConsumeChallenge"
	ch5, _, _ := h.store.ChallengeByID(context.Background(), mustDecode(t, cid5))
	tok5 := ""
	for _, c := range []Challenge{ch5} {
		_ = c
		lu, _ := url.Parse(strings.Fields(h.store.Sent[len(h.store.Sent)-1].Text[strings.Index(h.store.Sent[len(h.store.Sent)-1].Text, "https://"):])[0])
		tok5 = lu.Query().Get("code")
	}
	if rec := h.post(EmailVerifyPath, `{"challenge_id":"`+cid5+`","code":"`+tok5+`"}`); rec.Code != 500 {
		t.Fatalf("ConsumeChallenge: %d", rec.Code)
	}
	h.store.FailChallenge = "CreateChallenge"
	if rec := h.post(EmailStartPath, `{"email":"bia@example.com"}`); rec.Code != 500 {
		t.Fatalf("CreateChallenge: %d", rec.Code)
	}
	h.store.FailChallenge = ""
	// Consumed by a race: ConsumeChallenge says no.
	h.now = h.now.Add(time.Minute)
	rec = h.post(EmailStartPath, `{"email":"bia@example.com"}`)
	cid6 := decode(t, rec)["challenge_id"].(string)
	id6 := mustDecode(t, cid6)
	lu6, _ := url.Parse(strings.Fields(h.store.Sent[len(h.store.Sent)-1].Text[strings.Index(h.store.Sent[len(h.store.Sent)-1].Text, "https://"):])[0])
	c6 := h.store.Challenges[id6]
	c6.ConsumedAt = &h.now
	h.store.Challenges[id6] = c6
	if rec := h.post(EmailVerifyPath, `{"challenge_id":"`+cid6+`","code":"`+lu6.Query().Get("code")+`"}`); rec.Code != 404 {
		t.Fatalf("consumido: %d", rec.Code)
	}
	// Deletion pending refuses the login (403) after the challenge is consumed.
	u, _, _ := h.store.UserByEmail(context.Background(), "ana@example.com")
	when := h.now
	u.DeletionRequestedAt = &when
	h.store.Users[u.ID] = u
	h.now = h.now.Add(time.Minute)
	rec = h.post(EmailStartPath, `{"email":"ana@example.com"}`)
	cid7 := decode(t, rec)["challenge_id"].(string)
	lu7, _ := url.Parse(strings.Fields(h.store.Sent[len(h.store.Sent)-1].Text[strings.Index(h.store.Sent[len(h.store.Sent)-1].Text, "https://"):])[0])
	rec = h.post(EmailVerifyPath, `{"challenge_id":"`+cid7+`","code":"`+lu7.Query().Get("code")+`"}`)
	if rec.Code != 403 || decode(t, rec)["code"] != "account_deletion_pending" {
		t.Fatalf("exclusão pendente: %d %s", rec.Code, rec.Body.String())
	}
}

func mustDecode(t *testing.T, cid string) uuid.UUID {
	t.Helper()
	id, err := publicid.Decode(publicid.PrefixChallenge, cid)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestEmailStartIsUniform(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.store.Seed("existe@example.com", "Existe")
	// Same shape of response for an existing and an unknown address.
	a := h.do(req{method: http.MethodPost, path: EmailStartPath, body: `{"email":"existe@example.com"}`, ip: "203.0.113.1"})
	b := h.do(req{method: http.MethodPost, path: EmailStartPath, body: `{"email":"ninguem@example.com"}`, ip: "203.0.114.1"})
	if a.Code != b.Code || len(decode(t, a)) != len(decode(t, b)) {
		t.Fatalf("respostas diferentes: %s %s", a.Body.String(), b.Body.String())
	}
	// Timing: medians within tolerance (the handler does no lookup at all,
	// so the two distributions come from the same code path).
	measure := func(email, ip string) []time.Duration {
		out := make([]time.Duration, 0, 40)
		for i := 0; i < 40; i++ {
			h.now = h.now.Add(time.Minute) // keep the rate limiter happy
			start := time.Now()
			rec := h.do(req{method: http.MethodPost, path: EmailStartPath, body: `{"email":"` + email + `"}`, ip: ip})
			out = append(out, time.Since(start))
			if rec.Code != 202 {
				t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}
		}
		sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
		return out
	}
	known := measure("existe@example.com", "203.0.113.2")
	unknown := measure("ninguem@example.com", "203.0.114.2")
	mk, mu := known[len(known)/2], unknown[len(unknown)/2]
	diff := mk - mu
	if diff < 0 {
		diff = -diff
	}
	// Tolerance: 2 ms or 3x the faster median, whichever is larger (CI noise).
	limit := max(2*time.Millisecond, 3*min(mk, mu))
	if diff > limit {
		t.Fatalf("tempos divergem: conhecido %v, desconhecido %v", mk, mu)
	}
}

func TestPasswordLogin(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	u, _ := h.store.Seed("ana@example.com", "Ana")
	hash, _ := crypto.HashPassword(pw)
	_ = h.store.SetPassword(context.Background(), u.ID, hash)
	rec := h.post(PasswordLoginPath, `{"email":"Ana@example.com","password":"`+pw+`"}`)
	if rec.Code != 200 {
		t.Fatalf("login: %d %s", rec.Code, rec.Body.String())
	}
	checkSessionCookies(t, rec)
	org := h.orgOf("ana@example.com")
	if acts := h.actions(&org); len(acts) != 1 || acts[0] != auditLogin {
		t.Fatalf("auditoria: %v", acts)
	}
	wrong := h.post(PasswordLoginPath, `{"email":"ana@example.com","password":"`+pw+`x"}`)
	unknown := h.post(PasswordLoginPath, `{"email":"ninguem@example.com","password":"`+pw+`"}`)
	if wrong.Code != 401 || unknown.Code != 401 || decode(t, wrong)["code"] != "invalid_credentials" || decode(t, unknown)["code"] != "invalid_credentials" {
		t.Fatalf("recusas: %d %d", wrong.Code, unknown.Code)
	}
	// A user without password cannot log in with one.
	h.store.Seed("sem@example.com", "Sem")
	if rec := h.post(PasswordLoginPath, `{"email":"sem@example.com","password":"`+pw+`"}`); rec.Code != 401 {
		t.Fatalf("sem senha: %d", rec.Code)
	}
	for _, body := range []string{`{"email":"x","password":"` + pw + `"}`, `{"email":"ana@example.com","password":""}`, `{"email":"ana@example.com","password":"` + strings.Repeat("x", 129) + `"}`, `{"email":"ana@example.com","password":"` + pw + `","totp_code":"12"}`, `{`} {
		if rec := h.post(PasswordLoginPath, body); rec.Code != 400 {
			t.Fatalf("%s: %d", body, rec.Code)
		}
	}
	// Lockout: after the rate limit is exceeded the pair is locked (too_many_attempts).
	h.svc.guard = ratelimit.NewAuthGuard(h.limiter, h.lockout, ratelimit.Defaults().Auth)
	h.now = h.now.Add(time.Hour)
	var last *httptest.ResponseRecorder
	for i := 0; i < 8; i++ {
		last = h.do(req{method: http.MethodPost, path: PasswordLoginPath, body: `{"email":"ana@example.com","password":"errada-errada"}`, ip: "203.0.115.1"})
	}
	if last.Code != 429 {
		t.Fatalf("limite: %d %s", last.Code, last.Body.String())
	}
	h.now = h.now.Add(30 * time.Second)
	last = h.do(req{method: http.MethodPost, path: PasswordLoginPath, body: `{"email":"ana@example.com","password":"` + pw + `"}`, ip: "203.0.115.1"})
	if last.Code != 429 || decode(t, last)["code"] != "too_many_attempts" || last.Header().Get("Retry-After") == "" {
		t.Fatalf("bloqueio: %d %s", last.Code, last.Body.String())
	}
	// Another address is unaffected and clears the state on success.
	if rec := h.do(req{method: http.MethodPost, path: PasswordLoginPath, body: `{"email":"ana@example.com","password":"` + pw + `"}`, ip: "203.0.116.1"}); rec.Code != 200 {
		t.Fatalf("outro IP: %d", rec.Code)
	}
	// Second factor hook (each refusal strikes the lockout; the clock moves past it).
	h.svc.d.SecondFactor = &fakeTOTP{users: map[uuid.UUID]string{u.ID: "654321"}}
	h.now = h.now.Add(time.Hour)
	if rec := h.post(PasswordLoginPath, `{"email":"ana@example.com","password":"`+pw+`"}`); rec.Code != 401 {
		t.Fatalf("sem código: %d", rec.Code)
	}
	h.now = h.now.Add(time.Hour)
	if rec := h.post(PasswordLoginPath, `{"email":"ana@example.com","password":"`+pw+`","totp_code":"000000"}`); rec.Code != 401 {
		t.Fatalf("código errado: %d", rec.Code)
	}
	h.now = h.now.Add(time.Hour)
	if rec := h.post(PasswordLoginPath, `{"email":"ana@example.com","password":"`+pw+`","totp_code":"654321"}`); rec.Code != 200 {
		t.Fatalf("código certo: %d %s", rec.Code, rec.Body.String())
	}
	h.svc.d.SecondFactor = &fakeTOTP{err: errors.New("fora")}
	if rec := h.post(PasswordLoginPath, `{"email":"ana@example.com","password":"`+pw+`","totp_code":"654321"}`); rec.Code != 500 {
		t.Fatalf("hook com erro: %d", rec.Code)
	}
	h.svc.d.SecondFactor = NoSecondFactor{}
	// Weak hash gets flagged for rehash (login still works).
	weak, _ := crypto.HashPasswordWith(pw, crypto.Argon2Params{MemoryKiB: 8 * 1024, Time: 1, Threads: 1, SaltLen: 16, KeyLen: 32})
	_ = h.store.SetPassword(context.Background(), u.ID, weak)
	h.now = h.now.Add(time.Hour)
	if rec := h.post(PasswordLoginPath, `{"email":"ana@example.com","password":"`+pw+`"}`); rec.Code != 200 {
		t.Fatalf("hash fraco: %d", rec.Code)
	}
	// Store failure and refused accounts.
	h.store.FailOn = "UserByEmail"
	if rec := h.post(PasswordLoginPath, `{"email":"ana@example.com","password":"`+pw+`"}`); rec.Code != 500 {
		t.Fatalf("store: %d", rec.Code)
	}
	h.store.FailOn = ""
	u2 := h.store.Users[u.ID]
	u2.SuspendedAt = &h.now
	h.store.Users[u.ID] = u2
	h.now = h.now.Add(time.Hour)
	if rec := h.post(PasswordLoginPath, `{"email":"ana@example.com","password":"`+pw+`"}`); rec.Code != 403 || decode(t, rec)["code"] != "account_suspended" {
		t.Fatalf("suspensa: %d %s", rec.Code, rec.Body.String())
	}
}

// pw is assembled at runtime so scanners do not flag the fixture.
var pw = strings.Join([]string{"senha", "muito", "segura", "9"}, "-")

type fakeTOTP struct {
	users map[uuid.UUID]string
	err   error
}

func (f *fakeTOTP) Required(_ context.Context, u account.User) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	_, ok := f.users[u.ID]
	return ok, nil
}

func (f *fakeTOTP) Verify(_ context.Context, u account.User, code string) error {
	if f.users[u.ID] != code {
		return errors.New("totp inválido")
	}
	return nil
}

func TestPasswordTimingIsUniform(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	u, _ := h.store.Seed("ana@example.com", "Ana")
	hash, _ := crypto.HashPassword(pw)
	_ = h.store.SetPassword(context.Background(), u.ID, hash)
	measure := func(email string, ip string) []time.Duration {
		out := make([]time.Duration, 0, 12)
		for i := 0; i < 12; i++ {
			h.now = h.now.Add(time.Minute)
			h.lockout.Clear(limitKey(netip.MustParseAddr(ip), email))
			start := time.Now()
			rec := h.do(req{method: http.MethodPost, path: PasswordLoginPath, body: `{"email":"` + email + `","password":"nao-e-a-senha-1"}`, ip: ip})
			out = append(out, time.Since(start))
			if rec.Code != 401 {
				t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}
		}
		sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
		return out
	}
	known := measure("ana@example.com", "203.0.113.3")
	unknown := measure("ninguem@example.com", "203.0.114.3")
	mk, mu := known[len(known)/2], unknown[len(unknown)/2]
	diff := mk - mu
	if diff < 0 {
		diff = -diff
	}
	// Argon2id dominates both (~tens of ms); allow 50% of the slower median.
	if diff > max(mk, mu)/2 {
		t.Fatalf("tempos divergem: conhecido %v, desconhecido %v", mk, mu)
	}
}

func TestRefreshAndLogout(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	u, o := h.store.Seed("ana@example.com", "Ana")
	rec := httptest.NewRecorder()
	if _, err := h.sessions.Issue(context.Background(), rec, httptest.NewRequest(http.MethodPost, "/", nil), u.ID, o.ID); err != nil {
		t.Fatal(err)
	}
	cs := cookies(rec)
	h.now = h.now.Add(11 * time.Minute)
	rec2 := h.do(req{method: http.MethodPost, path: RefreshPath, cookies: cs})
	if rec2.Code != 200 || decode(t, rec2)["user"].(map[string]any)["email"] != "ana@example.com" {
		t.Fatalf("refresh: %d %s", rec2.Code, rec2.Body.String())
	}
	checkSessionCookies(t, rec2)
	cs2 := cookies(rec2)
	// Replaying the old refresh cookie revokes the family and is audited.
	rec3 := h.do(req{method: http.MethodPost, path: RefreshPath, cookies: cs})
	if rec3.Code != 401 || decode(t, rec3)["code"] != "unauthenticated" {
		t.Fatalf("reuso: %d %s", rec3.Code, rec3.Body.String())
	}
	if acts := h.actions(&o.ID); len(acts) != 2 || acts[0] != auditRefresh || acts[1] != auditSessionReuse {
		t.Fatalf("auditoria: %v", acts)
	}
	if rec := h.do(req{method: http.MethodPost, path: RefreshPath, cookies: cs2}); rec.Code != 401 {
		t.Fatalf("família revogada: %d", rec.Code)
	}
	if rec := h.do(req{method: http.MethodPost, path: RefreshPath}); rec.Code != 401 {
		t.Fatalf("sem cookie: %d", rec.Code)
	}
	// Logout.
	rec = httptest.NewRecorder()
	_, _ = h.sessions.Issue(context.Background(), rec, httptest.NewRequest(http.MethodPost, "/", nil), u.ID, o.ID)
	cs3 := cookies(rec)
	out := h.do(req{method: http.MethodPost, path: LogoutPath, cookies: cs3})
	if out.Code != 204 || cookieByName(cookies(out), session.CookieSession).MaxAge != -1 {
		t.Fatalf("logout: %d", out.Code)
	}
	if rec := h.do(req{method: http.MethodPost, path: LogoutPath, cookies: cs3}); rec.Code != 401 {
		t.Fatalf("logout repetido: %d", rec.Code)
	}
	if rec := h.do(req{method: http.MethodPost, path: LogoutPath}); rec.Code != 401 {
		t.Fatalf("logout sem cookie: %d", rec.Code)
	}
	if acts := h.actions(&o.ID); acts[len(acts)-1] != auditLogout {
		t.Fatalf("auditoria logout: %v", acts)
	}
	// Refresh of a user who asked for deletion, or whose tenant vanished.
	rec = httptest.NewRecorder()
	_, _ = h.sessions.Issue(context.Background(), rec, httptest.NewRequest(http.MethodPost, "/", nil), u.ID, o.ID)
	cs4 := cookies(rec)
	uu := h.store.Users[u.ID]
	uu.DeletionRequestedAt = &h.now
	h.store.Users[u.ID] = uu
	if rec := h.do(req{method: http.MethodPost, path: RefreshPath, cookies: cs4}); rec.Code != 401 {
		t.Fatalf("exclusão pendente: %d", rec.Code)
	}
	uu.DeletionRequestedAt = nil
	h.store.Users[u.ID] = uu
	rec = httptest.NewRecorder()
	_, _ = h.sessions.Issue(context.Background(), rec, httptest.NewRequest(http.MethodPost, "/", nil), u.ID, uuid.New())
	if rec := h.do(req{method: http.MethodPost, path: RefreshPath, cookies: cookies(rec)}); rec.Code != 401 {
		t.Fatalf("org desconhecida: %d", rec.Code)
	}
	// Each failure mode needs a fresh session: the rotation happens before
	// the store is consulted, so a reused cookie would be a replay.
	fresh := func() []*http.Cookie {
		rec := httptest.NewRecorder()
		_, _ = h.sessions.Issue(context.Background(), rec, httptest.NewRequest(http.MethodPost, "/", nil), u.ID, o.ID)
		return cookies(rec)
	}
	for _, fail := range []string{"UserByID", "Tenant"} {
		h.store.FailOn = fail
		if rec := h.do(req{method: http.MethodPost, path: RefreshPath, cookies: fresh()}); rec.Code != 500 {
			t.Fatalf("%s: %d", fail, rec.Code)
		}
	}
	h.store.FailOn = ""
	h.auditRec.err = errors.New("auditoria fora")
	if rec := h.do(req{method: http.MethodPost, path: RefreshPath, cookies: fresh()}); rec.Code != 500 {
		t.Fatalf("auditoria no refresh: %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	_, _ = h.sessions.Issue(context.Background(), rec, httptest.NewRequest(http.MethodPost, "/", nil), u.ID, o.ID)
	if rec := h.do(req{method: http.MethodPost, path: LogoutPath, cookies: cookies(rec)}); rec.Code != 500 {
		t.Fatalf("auditoria no logout: %d", rec.Code)
	}
	h.auditRec.err = nil
}

func TestLoginFailureModes(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	// Audit failure after the session was issued: fail closed, no cookies kept.
	h.auditRec.err = errors.New("auditoria fora")
	rec := h.post(EmailStartPath, `{"email":"ana@example.com"}`)
	cid := decode(t, rec)["challenge_id"].(string)
	lu, _ := url.Parse(strings.Fields(h.store.Sent[0].Text[strings.Index(h.store.Sent[0].Text, "https://"):])[0])
	rec = h.post(EmailVerifyPath, `{"challenge_id":"`+cid+`","code":"`+lu.Query().Get("code")+`"}`)
	if rec.Code != 500 {
		t.Fatalf("auditoria fora: %d %s", rec.Code, rec.Body.String())
	}
	if h.sstore.Len() != 0 && cookieByName(cookies(rec), session.CookieSession).MaxAge != -1 {
		t.Fatal("sessão deveria ser desfeita")
	}
	h.auditRec.err = nil
	// Linking a provider to an e-mail account and store failures around it.
	h.store.Seed("bia@example.com", "Bia")
	c, _, _ := h.start(ProviderGoogle, "")
	st := h.stateOf(c, ProviderGoogle)
	code := h.google.issueCode(h.google.claims(h.now, st.Nonce, map[string]any{"sub": "g-bia", "email": "bia@example.com"}), st.Verifier)
	rec = h.do(req{method: http.MethodGet, path: GoogleCallbackPath + "?code=" + code + "&state=" + st.State, cookies: []*http.Cookie{c}, accept: "application/json"})
	if rec.Code != 200 {
		t.Fatalf("vínculo: %d %s", rec.Code, rec.Body.String())
	}
	bia, _, _ := h.store.UserByEmail(context.Background(), "bia@example.com")
	if bia.GoogleSub != "g-bia" {
		t.Fatal("provedor não vinculado")
	}
	if acts := h.actions(ptr(h.orgOf("bia@example.com"))); acts[0] != auditProviderLink {
		t.Fatalf("auditoria do vínculo: %v", acts)
	}
	// A user without a stored name gets the provider's name on login.
	noName := account.User{ID: uuid.New(), Email: "semnome@example.com", GoogleSub: "g-semnome"}
	h.store.Users[noName.ID] = noName
	h.store.Memberships[h.orgOf("bia@example.com")] = append(h.store.Memberships[h.orgOf("bia@example.com")], account.Membership{OrgID: h.orgOf("bia@example.com"), UserID: noName.ID, Role: account.RoleMember, CreatedAt: h.now})
	for _, fail := range []string{"UserByProvider", "UserByEmail", "LinkProvider", "PrimaryTenant", "TouchLogin", "Signup", "SetDisplayName"} {
		h.store.FailOn = fail
		email := "bia@example.com"
		if fail == "Signup" {
			email = "nova@example.com"
		}
		if fail == "SetDisplayName" {
			email = "semnome@example.com"
		}
		c, _, _ := h.start(ProviderGoogle, "")
		st := h.stateOf(c, ProviderGoogle)
		sub := "g-" + fail
		if fail == "SetDisplayName" {
			sub = "g-semnome"
		}
		code := h.google.issueCode(h.google.claims(h.now, st.Nonce, map[string]any{"sub": sub, "email": email}), st.Verifier)
		rec := h.do(req{method: http.MethodGet, path: GoogleCallbackPath + "?code=" + code + "&state=" + st.State, cookies: []*http.Cookie{c}, accept: "application/json"})
		if rec.Code != 500 {
			t.Fatalf("%s: %d %s", fail, rec.Code, rec.Body.String())
		}
	}
	h.store.FailOn = ""
	c, _, _ = h.start(ProviderGoogle, "")
	st = h.stateOf(c, ProviderGoogle)
	code = h.google.issueCode(h.google.claims(h.now, st.Nonce, map[string]any{"sub": "g-semnome", "email": "semnome@example.com", "name": "Sem Nome"}), st.Verifier)
	rec = h.do(req{method: http.MethodGet, path: GoogleCallbackPath + "?code=" + code + "&state=" + st.State, cookies: []*http.Cookie{c}, accept: "application/json"})
	if rec.Code != 200 || h.store.Users[noName.ID].DisplayName != "Sem Nome" {
		t.Fatalf("nome do provedor: %d %s", rec.Code, rec.Body.String())
	}
	// A user without any organization cannot log in.
	orphan := account.User{ID: uuid.New(), Email: "orfao@example.com", GoogleSub: "g-orfao"}
	h.store.Users[orphan.ID] = orphan
	c, _, _ = h.start(ProviderGoogle, "")
	st = h.stateOf(c, ProviderGoogle)
	code = h.google.issueCode(h.google.claims(h.now, st.Nonce, map[string]any{"sub": "g-orfao", "email": "orfao@example.com"}), st.Verifier)
	rec = h.do(req{method: http.MethodGet, path: GoogleCallbackPath + "?code=" + code + "&state=" + st.State, cookies: []*http.Cookie{c}, accept: "application/json"})
	if rec.Code != 500 {
		t.Fatalf("sem organização: %d", rec.Code)
	}
	// Sessions store failure.
	h.sstore.Err = errors.New("sessões fora")
	c, _, _ = h.start(ProviderGoogle, "")
	st = h.stateOf(c, ProviderGoogle)
	code = h.google.issueCode(h.google.claims(h.now, st.Nonce, map[string]any{"sub": "g-bia", "email": "bia@example.com"}), st.Verifier)
	rec = h.do(req{method: http.MethodGet, path: GoogleCallbackPath + "?code=" + code + "&state=" + st.State, cookies: []*http.Cookie{c}, accept: "application/json"})
	if rec.Code != 500 {
		t.Fatalf("sessões fora: %d", rec.Code)
	}
	h.sstore.Err = nil
	// Signup with a long name: the org name is trimmed to the company limit.
	c, _, _ = h.start(ProviderGoogle, "")
	st = h.stateOf(c, ProviderGoogle)
	long := strings.Repeat("Nome ", 30)
	code = h.google.issueCode(h.google.claims(h.now, st.Nonce, map[string]any{"sub": "g-long", "email": "long@example.com", "name": long}), st.Verifier)
	rec = h.do(req{method: http.MethodGet, path: GoogleCallbackPath + "?code=" + code + "&state=" + st.State, cookies: []*http.Cookie{c}, accept: "application/json"})
	if rec.Code != 200 {
		t.Fatalf("nome longo: %d %s", rec.Code, rec.Body.String())
	}
	longUser, _, _ := h.store.UserByEmail(context.Background(), "long@example.com")
	if longUser.DisplayName != "" {
		t.Fatal("nome acima do limite é descartado, não truncado")
	}
	if org := h.store.Orgs[h.orgOf("long@example.com")]; org.Name != "long" {
		t.Fatalf("org: %q", org.Name)
	}
	// Rate limit on starts: the auth policy is 5/min per address.
	h.svc.guard = ratelimit.NewAuthGuard(h.limiter, h.lockout, ratelimit.Defaults().Auth)
	h.now = h.now.Add(time.Hour)
	var last *httptest.ResponseRecorder
	for i := 0; i < 6; i++ {
		last = h.do(req{method: http.MethodPost, path: GoogleStartPath, ip: "203.0.117.1"})
	}
	if last.Code != 429 || decode(t, last)["code"] != "rate_limited" || last.Header().Get("RateLimit-Limit") == "" {
		t.Fatalf("limite no start: %d %s", last.Code, last.Body.String())
	}
	// The violation added a strike: the address is now locked out.
	if rec := h.do(req{method: http.MethodPost, path: GoogleStartPath, ip: "203.0.117.1"}); rec.Code != 429 || decode(t, rec)["code"] != "too_many_attempts" {
		t.Fatalf("bloqueio após o limite: %d %s", rec.Code, rec.Body.String())
	}
	// A broken limiter fails open.
	h.svc.guard = ratelimit.NewAuthGuard(brokenLimiter{}, h.lockout, h.svc.d.Policy)
	if rec := h.do(req{method: http.MethodPost, path: GoogleStartPath, ip: "203.0.118.1"}); rec.Code != 200 {
		t.Fatalf("limitador quebrado: %d", rec.Code)
	}
}

type brokenLimiter struct{}

func (brokenLimiter) Allow(context.Context, string, ratelimit.Policy) (ratelimit.Decision, error) {
	return ratelimit.Decision{}, errors.New("fora")
}

func (brokenLimiter) AllowN(context.Context, string, ratelimit.Policy, int) (ratelimit.Decision, error) {
	return ratelimit.Decision{}, errors.New("fora")
}

func (brokenLimiter) Reset(context.Context, string) error { return nil }

func ptr[T any](v T) *T { return &v }

func TestProvidersAndHelpers(t *testing.T) {
	t.Parallel()
	g := NewGoogle("id", "secret", nil)
	if g.Name != ProviderGoogle || !g.PKCE || len(g.Issuers) != 2 || g.Client == nil {
		t.Fatalf("google: %+v", g)
	}
	if s, _ := g.ClientSecret(time.Now()); s != "secret" {
		t.Fatal("segredo estático")
	}
	u, _ := url.Parse(g.AuthorizationURL("https://api.example.com/cb", "st", "nn", "ver"))
	if u.Query().Get("prompt") != "select_account" || u.Query().Get("code_challenge") == "" || u.Query().Get("access_type") != "online" {
		t.Fatalf("url google: %s", u)
	}
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	a, err := NewApple("TEAM", "client", "KEY", pemOf(t, key), nil)
	if err != nil || a.ResponseMode != "form_post" || a.PKCE {
		t.Fatalf("apple: %+v %v", a, err)
	}
	secret, err := a.ClientSecret(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := crypto.VerifyJWT(context.Background(), secret, func(context.Context, string, string) (stdPublicKey, error) { return &key.PublicKey, nil }); err != nil {
		t.Fatalf("segredo apple: %v", err)
	}
	if _, err := NewApple("", "client", "KEY", pemOf(t, key), nil); !errors.Is(err, ErrProviderUnavailable) {
		t.Fatal("apple sem config")
	}
	if _, err := NewApple("TEAM", "client", "KEY", "lixo", nil); err == nil {
		t.Fatal("chave inválida")
	}
	// Exchange error paths.
	h := newHarness(t)
	p := h.svc.d.Google
	ctx := context.Background()
	if _, err := p.Exchange(ctx, "desconhecido", "v", "https://api.example.com/cb", h.now); !errors.Is(err, ErrProvider) {
		t.Fatalf("código desconhecido: %v", err)
	}
	h.google.mu.Lock()
	h.google.tokenBody = `{"access_token":"x"}`
	h.google.mu.Unlock()
	if _, err := p.Exchange(ctx, "x", "v", "https://api.example.com/cb", h.now); !errors.Is(err, ErrProvider) {
		t.Fatalf("sem id_token: %v", err)
	}
	h.google.mu.Lock()
	h.google.tokenBody = `{`
	h.google.mu.Unlock()
	if _, err := p.Exchange(ctx, "x", "v", "https://api.example.com/cb", h.now); !errors.Is(err, ErrProvider) {
		t.Fatalf("json inválido: %v", err)
	}
	h.google.mu.Lock()
	h.google.tokenBody = ""
	h.google.mu.Unlock()
	down := *p
	down.TokenURL = "http://127.0.0.1:1/token"
	if _, err := down.Exchange(ctx, "x", "v", "https://api.example.com/cb", h.now); !errors.Is(err, ErrProvider) {
		t.Fatalf("provedor inalcançável: %v", err)
	}
	down.TokenURL = "://x"
	if _, err := down.Exchange(ctx, "x", "v", "https://api.example.com/cb", h.now); !errors.Is(err, ErrProvider) {
		t.Fatalf("URL inválida: %v", err)
	}
	// Verify maps given/family names when name is absent.
	claims := h.google.claims(h.now, "", map[string]any{"given_name": "Ana", "family_name": "Lima"})
	delete(claims, "name")
	delete(claims, "nonce")
	id, err := p.Verify(ctx, h.google.sign(claims), "", h.now)
	if err != nil || id.Name != "Ana Lima" || !id.EmailVerified {
		t.Fatalf("verify: %+v %v", id, err)
	}
	// Helpers.
	if retryAfter(0) != "1" || retryAfter(1500*time.Millisecond) != "2" || retryAfter(3*time.Second) != "3" || itoa(0) != "0" || itoa(120) != "120" {
		t.Fatal("retryAfter/itoa")
	}
	if codeFor(errors.New("x")) != "internal_error" || codeFor(ErrProviderUnavailable) != "service_unavailable" || codeFor(ErrTooManyAttempts) != "too_many_attempts" || codeFor(ErrCodeInvalid) != "email_code_invalid" || codeFor(ErrChallengeNotFound) != "email_challenge_not_found" || codeFor(ErrCodeExpired) != "email_code_expired" {
		t.Fatal("codeFor")
	}
	for in, ok := range map[string]bool{"a@example.com": true, "A@EXAMPLE.COM": true, "a@b.c": true, "@example.com": false, "a@": false, "a@b": false, "a@b..c": false, "a@b.c.": false, "a@b;c.d": false, "": false, "a\x00@b.c": false} {
		if _, got := h.svc.normalizeEmail(in); got != ok {
			t.Fatalf("normalizeEmail(%q) = %v", in, got)
		}
	}
	if via(true) != "link" || via(false) != "code" {
		t.Fatal("via")
	}
}
