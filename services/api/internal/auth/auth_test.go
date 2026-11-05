package auth

import (
	"bytes"
	"context"
	stdcrypto "crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/mmtmn/docmatch/services/api/internal/account"
	"github.com/mmtmn/docmatch/services/api/internal/audit"
	"github.com/mmtmn/docmatch/services/api/internal/crypto"
	"github.com/mmtmn/docmatch/services/api/internal/httpx"
	"github.com/mmtmn/docmatch/services/api/internal/limits"
	"github.com/mmtmn/docmatch/services/api/internal/logx"
	"github.com/mmtmn/docmatch/services/api/internal/ratelimit"
	"github.com/mmtmn/docmatch/services/api/internal/session"
)

// fakeOIDC is an identity provider: it signs ID tokens with an RSA key,
// serves its JWKS, exchanges codes and validates Apple-style client secrets.
type fakeOIDC struct {
	t        *testing.T
	srv      *httptest.Server
	key      *rsa.PrivateKey
	kid      string
	clientID string
	// appleKey, when set, must verify the client_secret JWT of the exchange.
	appleKey *ecdsa.PublicKey

	mu        sync.Mutex
	codes     map[string]map[string]any // code -> claims of the token to issue
	tokenFail int                       // status to answer on /token (0 = ok)
	tokenBody string                    // raw body override
	verifiers map[string]string         // code -> expected code_verifier
	gotSecret string
}

func newFakeOIDC(t *testing.T, clientID string) *fakeOIDC {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeOIDC{t: t, key: key, kid: "k1", clientID: clientID, codes: map[string]map[string]any{}, verifiers: map[string]string{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		set := map[string]any{"keys": []map[string]string{{
			"kty": "RSA", "kid": f.kid, "use": "sig", "alg": "RS256",
			"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
		}}}
		_ = json.NewEncoder(w).Encode(set)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if err := r.ParseForm(); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if f.tokenFail != 0 {
			w.WriteHeader(f.tokenFail)
			_, _ = w.Write([]byte(`{"error":"server_error"}`))
			return
		}
		if f.tokenBody != "" {
			_, _ = w.Write([]byte(f.tokenBody))
			return
		}
		f.gotSecret = r.PostFormValue("client_secret")
		if f.appleKey != nil {
			if _, _, err := crypto.VerifyJWT(r.Context(), f.gotSecret, func(context.Context, string, string) (stdPublicKey, error) { return f.appleKey, nil }); err != nil {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":"invalid_client"}`))
				return
			}
		}
		code := r.PostFormValue("code")
		claims, ok := f.codes[code]
		if !ok || r.PostFormValue("grant_type") != "authorization_code" || r.PostFormValue("client_id") != f.clientID {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
			return
		}
		if want, ok := f.verifiers[code]; ok && r.PostFormValue("code_verifier") != want {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
			return
		}
		delete(f.codes, code)
		_ = json.NewEncoder(w).Encode(map[string]any{"id_token": f.sign(claims), "access_token": "at"})
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

type stdPublicKey = stdcrypto.PublicKey

// sign produces an RS256 ID token with the fake's key.
func (f *fakeOIDC) sign(claims map[string]any) string {
	return signRS256(f.t, f.key, f.kid, claims)
}

func signRS256(t *testing.T, key *rsa.PrivateKey, kid string, claims map[string]any) string {
	t.Helper()
	hb, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": kid})
	pb, _ := json.Marshal(claims)
	input := base64.RawURLEncoding.EncodeToString(hb) + "." + base64.RawURLEncoding.EncodeToString(pb)
	sum := sha256.Sum256([]byte(input))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, 5, sum[:]) // 5 = crypto.SHA256
	if err != nil {
		t.Fatal(err)
	}
	return input + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// claims returns a standard token for the provider at the given clock.
func (f *fakeOIDC) claims(now time.Time, nonce string, extra map[string]any) map[string]any {
	c := map[string]any{"iss": f.srv.URL, "sub": "sub-1", "aud": f.clientID, "exp": now.Add(time.Hour).Unix(), "iat": now.Unix(),
		"nonce": nonce, "email": "ana@example.com", "email_verified": true, "name": "Ana Lima"}
	for k, v := range extra {
		c[k] = v
	}
	return c
}

func (f *fakeOIDC) issueCode(claims map[string]any, verifier string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	code := crypto.RandomToken(8)
	f.codes[code] = claims
	if verifier != "" {
		f.verifiers[code] = verifier
	}
	return code
}

// provider builds a Provider pointed at the fake.
func (f *fakeOIDC) provider(name string, pkce bool, secret func(time.Time) (string, error)) *Provider {
	p := &Provider{
		Name: name, ClientID: f.clientID, ClientSecret: secret,
		AuthURL: f.srv.URL + "/auth", TokenURL: f.srv.URL + "/token", Issuers: []string{f.srv.URL},
		Keys: crypto.NewJWKS(f.srv.URL+"/jwks", f.srv.Client()).Key, Scopes: []string{"openid", "email"}, PKCE: pkce, Client: f.srv.Client(),
	}
	if name == ProviderApple {
		p.ResponseMode = "form_post"
	}
	return p
}

type harness struct {
	t        *testing.T
	store    *MemoryStore
	sessions *session.Manager
	sstore   *session.MemoryStore
	audit    *audit.MemoryStore
	auditRec *flakyAudit
	svc      *Service
	handler  http.Handler
	google   *fakeOIDC
	apple    *fakeOIDC
	appleKey *ecdsa.PrivateKey
	now      time.Time
	clock    func() time.Time
	limiter  *ratelimit.Memory
	lockout  *ratelimit.Lockout
}

type flakyAudit struct {
	inner account.Recorder
	err   error
}

func (f *flakyAudit) Record(ctx context.Context, orgID *uuid.UUID, actor audit.Actor, action string, target audit.Target, meta any) (audit.Entry, error) {
	if f.err != nil {
		return audit.Entry{}, f.err
	}
	return f.inner.Record(ctx, orgID, actor, action, target, meta)
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	return newHarnessWith(t, nil)
}

// newHarnessWith lets a test adjust the dependencies before the service is built.
func newHarnessWith(t *testing.T, mutate func(*Deps)) *harness {
	t.Helper()
	h := &harness{t: t, now: time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)}
	h.clock = func() time.Time { return h.now }
	h.store = NewMemoryStore(h.clock)
	h.sstore = session.NewMemoryStore(h.clock)
	h.sessions = session.NewManager(h.sstore, session.Config{Secure: true, AllowedOrigins: []string{"https://app.example.com"}, Now: h.clock})
	h.audit = audit.NewMemoryStore()
	h.auditRec = &flakyAudit{inner: audit.New(h.audit)}
	keys, _ := crypto.NewKeyring(bytes.Repeat([]byte{5}, 32))
	h.google = newFakeOIDC(t, "google-client")
	h.apple = newFakeOIDC(t, "apple-client")
	h.appleKey, _ = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	h.apple.appleKey = &h.appleKey.PublicKey
	pub, _ := url.Parse("https://app.example.com")
	api, _ := url.Parse("https://api.example.com")
	h.limiter = ratelimit.NewMemory(ratelimit.WithClock(h.clock))
	h.lockout = ratelimit.NewLockout(ratelimit.DefaultLockoutPolicy(), h.clock)
	d := Deps{
		Store: h.store, Sessions: h.sessions, Keys: keys, Audit: h.auditRec,
		Google:       h.google.provider(ProviderGoogle, true, func(time.Time) (string, error) { return "google-secret", nil }),
		Apple:        h.apple.provider(ProviderApple, false, appleSecret(h.appleKey, "TEAM", "apple-client", "KEY1", h.apple.srv.URL)),
		EmailEnabled: true, PublicBaseURL: pub, APIBaseURL: api, Secure: true,
		// Generous by default: the tests that exercise the limit install the real policy.
		Limiter: h.limiter, Lockout: h.lockout, Policy: ratelimit.Policy{Limit: 1000, Period: time.Minute}, Logger: logx.Discard(), Now: h.clock,
		Country: func(*http.Request) string { return "BR" },
	}
	if mutate != nil {
		mutate(&d)
	}
	svc, err := New(d)
	if err != nil {
		t.Fatal(err)
	}
	h.svc = svc
	r := httpx.NewRouter(limits.Must())
	svc.Mount(r)
	h.handler = httpx.Chain(r, httpx.RequestIDMiddleware, h.sessions.Middleware)
	return h
}

type req struct {
	method, path, body, contentType, accept, ip string
	cookies                                     []*http.Cookie
	headers                                     map[string]string
}

func (h *harness) do(q req) *httptest.ResponseRecorder {
	h.t.Helper()
	var rd io.Reader
	if q.body != "" {
		rd = strings.NewReader(q.body)
	}
	r := httptest.NewRequest(q.method, q.path, rd)
	if q.body != "" {
		ct := q.contentType
		if ct == "" {
			ct = "application/json"
		}
		r.Header.Set("Content-Type", ct)
	}
	if q.accept != "" {
		r.Header.Set("Accept", q.accept)
	}
	if q.ip != "" {
		r.RemoteAddr = q.ip + ":1234"
	} else {
		r.RemoteAddr = "203.0.113.10:1234"
	}
	r.Header.Set("Origin", "https://app.example.com")
	for k, v := range q.headers {
		r.Header.Set(k, v)
	}
	for _, c := range q.cookies {
		if c.MaxAge >= 0 {
			r.AddCookie(&http.Cookie{Name: c.Name, Value: c.Value})
		}
	}
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, r)
	return rec
}

func (h *harness) post(path, body string) *httptest.ResponseRecorder {
	return h.do(req{method: http.MethodPost, path: path, body: body})
}

func cookies(rec *httptest.ResponseRecorder) []*http.Cookie {
	return rec.Result().Cookies() //nolint:bodyclose // recorder
}

func cookieByName(cs []*http.Cookie, name string) *http.Cookie {
	for _, c := range cs {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("corpo não é JSON (%d): %s", rec.Code, rec.Body.String())
	}
	return m
}

func (h *harness) actions(org *uuid.UUID) []string {
	rows, _ := h.audit.Range(context.Background(), org, 1, 1<<62)
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Action)
	}
	return out
}

func (h *harness) orgOf(email string) uuid.UUID {
	h.t.Helper()
	u, ok, _ := h.store.UserByEmail(context.Background(), email)
	if !ok {
		h.t.Fatalf("usuário %s não existe", email)
	}
	tnt, ok, _ := h.store.PrimaryTenant(context.Background(), u.ID)
	if !ok {
		h.t.Fatalf("usuário %s sem organização", email)
	}
	return tnt.Org.ID
}

// checkSessionCookies asserts the flags of a login response.
func checkSessionCookies(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	cs := cookies(rec)
	for _, name := range []string{session.CookieSession, session.CookieRefresh, session.CookieDevice} {
		c := cookieByName(cs, name)
		if c == nil || c.MaxAge <= 0 {
			t.Fatalf("cookie %s ausente: %v", name, cs)
		}
		if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode {
			t.Fatalf("flags de %s: %+v", name, c)
		}
	}
}

// pemOf renders an EC key as PKCS#8 PEM.
func pemOf(t *testing.T, key *ecdsa.PrivateKey) string {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

// startGoogle performs POST /auth/google/start and returns the state cookie
// and the parsed authorization URL.
func (h *harness) start(provider, body string) (*http.Cookie, *url.URL, map[string]any) {
	h.t.Helper()
	path := GoogleStartPath
	if provider == ProviderApple {
		path = AppleStartPath
	}
	rec := h.post(path, body)
	if rec.Code != 200 {
		h.t.Fatalf("start %s: %d %s", provider, rec.Code, rec.Body.String())
	}
	out := decode(h.t, rec)
	u, err := url.Parse(out["authorization_url"].(string))
	if err != nil {
		h.t.Fatal(err)
	}
	c := cookieByName(cookies(rec), cookieName(provider))
	if c == nil {
		h.t.Fatal("cookie de estado ausente")
	}
	return c, u, out
}

// stateOf opens the sealed state cookie (tests need the nonce and verifier).
func (h *harness) stateOf(c *http.Cookie, provider string) oauthState {
	h.t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(&http.Cookie{Name: c.Name, Value: c.Value})
	raw, _ := base64.RawURLEncoding.DecodeString(c.Value)
	pt, err := crypto.Open(h.svc.d.Keys.Key(crypto.PurposeCookie), raw, []byte(provider))
	if err != nil {
		h.t.Fatal(err)
	}
	var st oauthState
	_ = json.Unmarshal(pt, &st)
	return st
}
