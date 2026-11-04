package session

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/mmtmn/docmatch/services/api/internal/crypto"
	"github.com/mmtmn/docmatch/services/api/internal/httpx"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newManager(t *testing.T, secure bool) (*Manager, *MemoryStore, *clock) {
	t.Helper()
	c := &clock{t: time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)}
	store := NewMemoryStore(c.now)
	m := NewManager(store, Config{Secure: secure, AllowedOrigins: []string{"https://app.example.com"}, Now: c.now})
	return m, store, c
}

func cookiesOf(rec *httptest.ResponseRecorder) map[string]*http.Cookie {
	out := map[string]*http.Cookie{}
	for _, c := range rec.Result().Cookies() { //nolint:bodyclose // recorder
		out[c.Name] = c
	}
	return out
}

func withCookies(req *http.Request, cs map[string]*http.Cookie, names ...string) *http.Request {
	for _, n := range names {
		if c, ok := cs[n]; ok && c.MaxAge >= 0 {
			req.AddCookie(&http.Cookie{Name: c.Name, Value: c.Value})
		}
	}
	return req
}

func login(t *testing.T, m *Manager) (Session, map[string]*http.Cookie) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/auth/email/verify", nil)
	req.RemoteAddr = "203.0.113.9:4444"
	req.Header.Set("User-Agent", strings.Repeat("ua", 200))
	s, err := m.Issue(context.Background(), rec, req, uuid.New(), uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	return s, cookiesOf(rec)
}

func TestIssueCookies(t *testing.T) {
	t.Parallel()
	m, store, c := newManager(t, true)
	s, cs := login(t, m)
	if s.IPPrefix != "203.0.113.0/24" || len(s.UserAgent) != maxUserAgent || s.FamilyID != s.ID || store.Len() != 1 {
		t.Fatalf("sessão: %+v", s)
	}
	if !s.ExpiresAt.Equal(c.t.Add(DefaultRefreshTTL)) || !s.AccessExpiresAt.Equal(c.t.Add(DefaultAccessTTL)) {
		t.Fatalf("validades: %v %v", s.ExpiresAt, s.AccessExpiresAt)
	}
	for _, name := range []string{CookieSession, CookieRefresh, CookieDevice} {
		ck := cs[name]
		if ck == nil {
			t.Fatalf("cookie %s ausente", name)
		}
		if !ck.HttpOnly || !ck.Secure || ck.SameSite != http.SameSiteLaxMode || ck.MaxAge <= 0 {
			t.Fatalf("flags de %s: %+v", name, ck)
		}
	}
	if cs[CookieSession].Path != "/" || cs[CookieRefresh].Path != DefaultRefreshPath || cs[CookieDevice].Path != "/" {
		t.Fatalf("paths: %s %s %s", cs[CookieSession].Path, cs[CookieRefresh].Path, cs[CookieDevice].Path)
	}
	if cs[CookieRefresh].MaxAge != int(DefaultRefreshTTL/time.Second) || cs[CookieDevice].MaxAge != int(DefaultDeviceTTL/time.Second) {
		t.Fatalf("max-age: %d %d", cs[CookieRefresh].MaxAge, cs[CookieDevice].MaxAge)
	}
	sid, access, _ := strings.Cut(cs[CookieSession].Value, ".")
	if id, ok := decodeID(sid); !ok || id != s.ID || !crypto.Equal(crypto.Digest(access), s.AccessHash) {
		t.Fatal("cookie de sessão não corresponde à linha")
	}
	if !crypto.Equal(crypto.Digest(cs[CookieDevice].Value), s.DeviceHash) {
		t.Fatal("device hash")
	}
	// A second login from the same browser keeps the device cookie.
	rec := httptest.NewRecorder()
	req := withCookies(httptest.NewRequest(http.MethodPost, "/", nil), cs, CookieDevice)
	s2, err := m.Issue(context.Background(), rec, req, s.UserID, s.OrgID)
	if err != nil || !crypto.Equal(s2.DeviceHash, s.DeviceHash) || cookiesOf(rec)[CookieDevice].Value != cs[CookieDevice].Value {
		t.Fatal("device cookie deveria ser reaproveitado")
	}
	// Dev over http: Secure off.
	devM, _, _ := newManager(t, false)
	_, devCs := login(t, devM)
	if devCs[CookieSession].Secure {
		t.Fatal("dev sem Secure")
	}
	if m.AccessTTL() != DefaultAccessTTL || m.Store() != store {
		t.Fatal("acessores")
	}
	store.Err = errors.New("banco fora")
	if _, err := m.Issue(context.Background(), httptest.NewRecorder(), req, s.UserID, s.OrgID); err == nil {
		t.Fatal("erro do store")
	}
}

func TestAuthenticate(t *testing.T) {
	t.Parallel()
	m, store, c := newManager(t, true)
	s, cs := login(t, m)
	ctx := context.Background()
	req := withCookies(httptest.NewRequest(http.MethodGet, "/v1/me", nil), cs, CookieSession)
	p, err := m.Authenticate(ctx, req)
	if err != nil || p.UserID != s.UserID || p.OrgID != s.OrgID || p.SessionID != s.ID || !p.AccessExpiresAt.Equal(s.AccessExpiresAt) {
		t.Fatalf("principal: %+v %v", p, err)
	}
	// Touch moved last_seen_at.
	c.t = c.t.Add(5 * time.Minute)
	if _, err := m.Authenticate(ctx, req); err != nil {
		t.Fatal(err)
	}
	if row, _, _ := store.ByID(ctx, s.ID); !row.LastSeenAt.Equal(c.t) {
		t.Fatal("touch")
	}
	if _, err := m.Authenticate(ctx, httptest.NewRequest(http.MethodGet, "/", nil)); !errors.Is(err, ErrNoSession) {
		t.Fatalf("sem cookie: %v", err)
	}
	for _, bad := range []string{"", "semponto", "x." + crypto.RandomToken(32), encodeID(uuid.New()) + ".curto", encodeID(uuid.Nil) + "." + crypto.RandomToken(32)} {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.AddCookie(&http.Cookie{Name: CookieSession, Value: bad})
		_, err := m.Authenticate(ctx, r)
		if bad == "" && !errors.Is(err, ErrNoSession) {
			t.Fatalf("vazio: %v", err)
		}
		if bad != "" && !errors.Is(err, ErrInvalid) {
			t.Fatalf("%q: %v", bad, err)
		}
	}
	unknown := httptest.NewRequest(http.MethodGet, "/", nil)
	unknown.AddCookie(&http.Cookie{Name: CookieSession, Value: encodeID(uuid.New()) + "." + crypto.RandomToken(32)})
	if _, err := m.Authenticate(ctx, unknown); !errors.Is(err, ErrInvalid) {
		t.Fatalf("id desconhecido: %v", err)
	}
	wrongSecret := httptest.NewRequest(http.MethodGet, "/", nil)
	wrongSecret.AddCookie(&http.Cookie{Name: CookieSession, Value: encodeID(s.ID) + "." + crypto.RandomToken(32)})
	if _, err := m.Authenticate(ctx, wrongSecret); !errors.Is(err, ErrInvalid) {
		t.Fatalf("segredo errado: %v", err)
	}
	c.t = c.t.Add(DefaultAccessTTL)
	if _, err := m.Authenticate(ctx, req); !errors.Is(err, ErrAccessExpired) {
		t.Fatalf("janela de acesso: %v", err)
	}
	c.t = c.t.Add(DefaultRefreshTTL)
	if _, err := m.Authenticate(ctx, req); !errors.Is(err, ErrExpired) {
		t.Fatalf("sessão vencida: %v", err)
	}
	c.t = s.CreatedAt
	if _, err := store.Revoke(ctx, s.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Authenticate(ctx, req); !errors.Is(err, ErrRevoked) {
		t.Fatalf("revogada: %v", err)
	}
	store.Err = errors.New("banco fora")
	if _, err := m.Authenticate(ctx, req); err == nil || errors.Is(err, ErrInvalid) {
		t.Fatalf("erro do store: %v", err)
	}
}

func TestRefreshRotatesAndDetectsReuse(t *testing.T) {
	t.Parallel()
	m, store, c := newManager(t, true)
	s, cs := login(t, m)
	ctx := context.Background()
	c.t = c.t.Add(11 * time.Minute)
	rec := httptest.NewRecorder()
	req := withCookies(httptest.NewRequest(http.MethodPost, DefaultRefreshPath, nil), cs, CookieRefresh, CookieDevice)
	rotated, err := m.Refresh(ctx, rec, req)
	if err != nil || rotated.Rotations != 1 || rotated.RotatedAt == nil || !rotated.ExpiresAt.Equal(c.t.Add(DefaultRefreshTTL)) {
		t.Fatalf("rotação: %+v %v", rotated, err)
	}
	cs2 := cookiesOf(rec)
	if cs2[CookieSession].Value == cs[CookieSession].Value || cs2[CookieRefresh].Value == cs[CookieRefresh].Value || cs2[CookieDevice].Value != cs[CookieDevice].Value {
		t.Fatal("cookies deveriam rotacionar (menos o device)")
	}
	// The new access secret works and the old one does not.
	if _, err := m.Authenticate(ctx, withCookies(httptest.NewRequest(http.MethodGet, "/", nil), cs2, CookieSession)); err != nil {
		t.Fatalf("novo acesso: %v", err)
	}
	if _, err := m.Authenticate(ctx, withCookies(httptest.NewRequest(http.MethodGet, "/", nil), cs, CookieSession)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("acesso antigo: %v", err)
	}
	// Replaying the old refresh token revokes the family.
	_, err = m.Refresh(ctx, httptest.NewRecorder(), withCookies(httptest.NewRequest(http.MethodPost, DefaultRefreshPath, nil), cs, CookieRefresh, CookieDevice))
	if !errors.Is(err, ErrReuse) {
		t.Fatalf("reuso: %v", err)
	}
	if row, _, _ := store.ByID(ctx, s.ID); row.RevokedAt == nil {
		t.Fatal("família deveria estar revogada")
	}
	_, err = m.Refresh(ctx, httptest.NewRecorder(), withCookies(httptest.NewRequest(http.MethodPost, DefaultRefreshPath, nil), cs2, CookieRefresh, CookieDevice))
	if !errors.Is(err, ErrRevoked) {
		t.Fatalf("o token atual também morre com a família: %v", err)
	}
}

func TestRefreshDeviceBindingAndErrors(t *testing.T) {
	t.Parallel()
	m, store, c := newManager(t, true)
	s, cs := login(t, m)
	ctx := context.Background()
	// Refresh cookie without the device cookie: stolen cookie → family revoked.
	_, err := m.Refresh(ctx, httptest.NewRecorder(), withCookies(httptest.NewRequest(http.MethodPost, DefaultRefreshPath, nil), cs, CookieRefresh))
	if !errors.Is(err, ErrDeviceMismatch) {
		t.Fatalf("sem device: %v", err)
	}
	if row, _, _ := store.ByID(ctx, s.ID); row.RevokedAt == nil {
		t.Fatal("família revogada")
	}
	// Another login; wrong device value.
	s2, cs := login(t, m)
	req := withCookies(httptest.NewRequest(http.MethodPost, DefaultRefreshPath, nil), cs, CookieRefresh)
	req.AddCookie(&http.Cookie{Name: CookieDevice, Value: crypto.RandomToken(32)})
	if _, err := m.Refresh(ctx, httptest.NewRecorder(), req); !errors.Is(err, ErrDeviceMismatch) {
		t.Fatalf("device errado: %v", err)
	}
	if row, _, _ := store.ByID(ctx, s2.ID); row.RevokedAt == nil {
		t.Fatal("família revogada")
	}
	// Error cases: no cookie, unknown id, expired, store failures.
	if _, err := m.Refresh(ctx, httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, DefaultRefreshPath, nil)); !errors.Is(err, ErrNoSession) {
		t.Fatalf("sem cookie: %v", err)
	}
	unknown := httptest.NewRequest(http.MethodPost, DefaultRefreshPath, nil)
	unknown.AddCookie(&http.Cookie{Name: CookieRefresh, Value: encodeID(uuid.New()) + "." + crypto.RandomToken(32)})
	if _, err := m.Refresh(ctx, httptest.NewRecorder(), unknown); !errors.Is(err, ErrInvalid) {
		t.Fatalf("desconhecido: %v", err)
	}
	s3, cs := login(t, m)
	c.t = c.t.Add(DefaultRefreshTTL + time.Second)
	if _, err := m.Refresh(ctx, httptest.NewRecorder(), withCookies(httptest.NewRequest(http.MethodPost, DefaultRefreshPath, nil), cs, CookieRefresh, CookieDevice)); !errors.Is(err, ErrExpired) {
		t.Fatalf("vencida: %v", err)
	}
	c.t = s3.CreatedAt
	store.Err = errors.New("banco fora")
	if _, err := m.Refresh(ctx, httptest.NewRecorder(), withCookies(httptest.NewRequest(http.MethodPost, DefaultRefreshPath, nil), cs, CookieRefresh, CookieDevice)); err == nil || errors.Is(err, ErrInvalid) {
		t.Fatalf("erro do store: %v", err)
	}
	store.Err = nil
	// Rotate losing the race with a revocation.
	racy := &racyStore{MemoryStore: store}
	rm := NewManager(racy, Config{Now: c.now})
	if _, err := rm.Refresh(ctx, httptest.NewRecorder(), withCookies(httptest.NewRequest(http.MethodPost, DefaultRefreshPath, nil), cs, CookieRefresh, CookieDevice)); !errors.Is(err, ErrRevoked) {
		t.Fatalf("corrida com revogação: %v", err)
	}
	// Store failures while revoking a family surface as errors.
	failing := &failRevoke{MemoryStore: store}
	fm := NewManager(failing, Config{Now: c.now})
	_, cs4 := login(t, fm)
	if _, err := fm.Refresh(ctx, httptest.NewRecorder(), withCookies(httptest.NewRequest(http.MethodPost, DefaultRefreshPath, nil), cs4, CookieRefresh)); err == nil || errors.Is(err, ErrDeviceMismatch) {
		t.Fatalf("falha ao revogar (device): %v", err)
	}
	bad := withCookies(httptest.NewRequest(http.MethodPost, DefaultRefreshPath, nil), cs4, CookieDevice)
	sid, _, _ := strings.Cut(cs4[CookieRefresh].Value, ".")
	bad.AddCookie(&http.Cookie{Name: CookieRefresh, Value: sid + "." + crypto.RandomToken(32)})
	if _, err := fm.Refresh(ctx, httptest.NewRecorder(), bad); err == nil || errors.Is(err, ErrReuse) {
		t.Fatalf("falha ao revogar (reuso): %v", err)
	}
}

type racyStore struct{ *MemoryStore }

func (r *racyStore) Rotate(context.Context, uuid.UUID, Rotation) (Session, bool, error) {
	return Session{}, false, nil
}

type failRevoke struct{ *MemoryStore }

func (f *failRevoke) RevokeFamily(context.Context, uuid.UUID) (int64, error) {
	return 0, errors.New("falha ao revogar")
}

func TestLogoutAndClear(t *testing.T) {
	t.Parallel()
	m, store, _ := newManager(t, true)
	s, cs := login(t, m)
	ctx := context.Background()
	rec := httptest.NewRecorder()
	got, err := m.Logout(ctx, rec, withCookies(httptest.NewRequest(http.MethodPost, "/v1/auth/logout", nil), cs, CookieSession))
	if err != nil || got.ID != s.ID {
		t.Fatalf("logout: %v", err)
	}
	cleared := cookiesOf(rec)
	if cleared[CookieSession].MaxAge != -1 || cleared[CookieRefresh].MaxAge != -1 || cleared[CookieRefresh].Path != DefaultRefreshPath || cleared[CookieDevice] != nil {
		t.Fatalf("limpeza: %+v", cleared)
	}
	if row, _, _ := store.ByID(ctx, s.ID); row.RevokedAt == nil {
		t.Fatal("revogada")
	}
	if _, err := m.Authenticate(ctx, withCookies(httptest.NewRequest(http.MethodGet, "/", nil), cs, CookieSession)); !errors.Is(err, ErrRevoked) {
		t.Fatalf("após logout: %v", err)
	}
	if _, err := m.Logout(ctx, httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/", nil)); !errors.Is(err, ErrNoSession) {
		t.Fatalf("sem cookie: %v", err)
	}
	unknown := httptest.NewRequest(http.MethodPost, "/", nil)
	unknown.AddCookie(&http.Cookie{Name: CookieSession, Value: encodeID(uuid.New()) + "." + crypto.RandomToken(32)})
	if _, err := m.Logout(ctx, httptest.NewRecorder(), unknown); !errors.Is(err, ErrInvalid) {
		t.Fatalf("desconhecida: %v", err)
	}
	store.Err = errors.New("banco fora")
	if _, err := m.Logout(ctx, httptest.NewRecorder(), withCookies(httptest.NewRequest(http.MethodPost, "/", nil), cs, CookieSession)); err == nil {
		t.Fatal("erro do store")
	}
}

func decodeEnvelope(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var env map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("corpo: %s", rec.Body.String())
	}
	return env
}

func TestMiddlewareAndRequire(t *testing.T) {
	t.Parallel()
	m, _, c := newManager(t, true)
	_, cs := login(t, m)
	var seen Principal
	var seenOK bool
	h := httpx.Chain(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen, seenOK = FromContext(r.Context())
		w.WriteHeader(http.StatusNoContent)
	}), m.Middleware, Require)

	do := func(method string, headers map[string]string, cookies ...string) *httptest.ResponseRecorder {
		req := withCookies(httptest.NewRequest(method, "/v1/me", nil), cs, cookies...)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	if rec := do(http.MethodGet, nil); rec.Code != 401 || decodeEnvelope(t, rec)["code"] != "unauthenticated" {
		t.Fatalf("sem cookie: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(http.MethodGet, nil, CookieSession); rec.Code != 204 || !seenOK || seen.UserID == uuid.Nil {
		t.Fatalf("com cookie: %d %s", rec.Code, rec.Body.String())
	}
	// CSRF: state-changing with a foreign Origin or cross-site fetch metadata.
	if rec := do(http.MethodPost, map[string]string{"Origin": "https://evil.example.net"}, CookieSession); rec.Code != 403 || decodeEnvelope(t, rec)["code"] != "forbidden" {
		t.Fatalf("origem estranha: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(http.MethodDelete, map[string]string{"Sec-Fetch-Site": "cross-site"}, CookieSession); rec.Code != 403 {
		t.Fatalf("cross-site: %d", rec.Code)
	}
	if rec := do(http.MethodPost, map[string]string{"Origin": "https://app.example.com"}, CookieSession); rec.Code != 204 {
		t.Fatalf("origem permitida: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(http.MethodPatch, map[string]string{"Sec-Fetch-Site": "same-origin"}, CookieSession); rec.Code != 204 {
		t.Fatalf("same-origin: %d", rec.Code)
	}
	if rec := do(http.MethodPut, nil, CookieSession); rec.Code != 204 {
		t.Fatalf("cliente não navegador: %d", rec.Code)
	}
	if rec := do(http.MethodGet, map[string]string{"Origin": "https://evil.example.net"}, CookieSession); rec.Code != 204 {
		t.Fatalf("GET ignora Origin: %d", rec.Code)
	}
	// Without a cookie the CSRF check does not apply (nothing to forge).
	if rec := do(http.MethodPost, map[string]string{"Origin": "https://evil.example.net"}); rec.Code != 401 {
		t.Fatalf("sem cookie e origem estranha: %d", rec.Code)
	}
	// Expired access window: session_expired tells the client to refresh.
	c.t = c.t.Add(DefaultAccessTTL + time.Second)
	if rec := do(http.MethodGet, nil, CookieSession); rec.Code != 401 || decodeEnvelope(t, rec)["code"] != "session_expired" {
		t.Fatalf("acesso vencido: %d %s", rec.Code, rec.Body.String())
	}
	// A garbage cookie is unauthenticated.
	req := httptest.NewRequest(http.MethodGet, "/v1/me", nil)
	req.AddCookie(&http.Cookie{Name: CookieSession, Value: "lixo"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 401 || decodeEnvelope(t, rec)["code"] != "unauthenticated" || AuthError(req.Context()) != nil {
		t.Fatalf("cookie inválido: %d", rec.Code)
	}
	if RequireFunc(func(http.ResponseWriter, *http.Request) {}) == nil {
		t.Fatal("RequireFunc")
	}
}

type fakeTx struct {
	pgx.Tx
	execs []string
}

func (f *fakeTx) Exec(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	f.execs = append(f.execs, sql)
	return pgconn.CommandTag{}, nil
}
func (f *fakeTx) Commit(context.Context) error   { return nil }
func (f *fakeTx) Rollback(context.Context) error { return nil }

type fakeBeginner struct{ tx *fakeTx }

func (b *fakeBeginner) Begin(context.Context) (pgx.Tx, error) { return b.tx, nil }

func TestWithOrgTx(t *testing.T) {
	t.Parallel()
	b := &fakeBeginner{tx: &fakeTx{}}
	if err := WithOrgTx(context.Background(), b, func(context.Context, pgx.Tx) error { return nil }); !errors.Is(err, ErrNoTenant) {
		t.Fatalf("sem principal: %v", err)
	}
	ctx := ContextWithPrincipal(context.Background(), Principal{UserID: uuid.New()})
	if err := WithOrgTx(ctx, b, func(context.Context, pgx.Tx) error { return nil }); !errors.Is(err, ErrNoTenant) {
		t.Fatalf("sem organização: %v", err)
	}
	org := uuid.New()
	ctx = ContextWithPrincipal(context.Background(), Principal{UserID: uuid.New(), OrgID: org})
	called := false
	if err := WithOrgTx(ctx, b, func(_ context.Context, tx pgx.Tx) error { called = tx == b.tx; return nil }); err != nil || !called {
		t.Fatalf("org tx: %v %v", err, called)
	}
	if len(b.tx.execs) != 1 || !strings.Contains(b.tx.execs[0], "app.org_id") {
		t.Fatalf("SET LOCAL ausente: %v", b.tx.execs)
	}
}

func TestHelpers(t *testing.T) {
	t.Parallel()
	if ipPrefix(netip.MustParseAddr("2001:db8::1")) != "2001:db8::/64" || ipPrefix(netip.MustParseAddr("::ffff:203.0.113.9")) != "203.0.113.0/24" || ipPrefix(netip.Addr{}) != "" {
		t.Fatal("ipPrefix")
	}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "[2001:db8::1]:80"
	if !remoteAddr(r).Is6() {
		t.Fatal("remoteAddr v6")
	}
	r.RemoteAddr = "203.0.113.9"
	if !remoteAddr(r).Is4() {
		t.Fatal("remoteAddr sem porta")
	}
	r.RemoteAddr = "pipe"
	if remoteAddr(r).IsValid() {
		t.Fatal("remoteAddr inválido")
	}
	if truncate("abc", 2) != "ab" || truncate("abc", 5) != "abc" {
		t.Fatal("truncate")
	}
	if _, ok := decodeID("!!"); ok {
		t.Fatal("decodeID")
	}
	m, store, c := newManager(t, true)
	s, _ := login(t, m)
	ctx := context.Background()
	if list, err := store.ListActive(ctx, s.UserID); err != nil || len(list) != 1 {
		t.Fatalf("ListActive: %v %d", err, len(list))
	}
	s2, _ := login(t, m)
	if n, err := store.RevokeUser(ctx, s.UserID, s.ID); err != nil || n != 0 {
		t.Fatalf("RevokeUser de outro usuário: %d %v", n, err)
	}
	if _, err := m.Issue(ctx, httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/", nil), s2.UserID, s2.OrgID); err != nil {
		t.Fatal(err)
	}
	if n, _ := store.RevokeUser(ctx, s2.UserID, s2.ID); n != 1 {
		t.Fatalf("RevokeUser mantém a atual: %d", n)
	}
	if list, _ := store.ListActive(ctx, s2.UserID); len(list) != 1 || list[0].ID != s2.ID {
		t.Fatal("ListActive após revogação")
	}
	c.t = c.t.Add(DefaultRefreshTTL + time.Hour)
	if list, _ := store.ListActive(ctx, s2.UserID); len(list) != 0 {
		t.Fatal("ListActive ignora vencidas")
	}
	store.Err = errors.New("fora")
	if _, err := store.ListActive(ctx, s.UserID); err == nil {
		t.Fatal("ListActive erro")
	}
	if _, err := store.RevokeUser(ctx, s.UserID, uuid.Nil); err == nil {
		t.Fatal("RevokeUser erro")
	}
	if _, err := store.Revoke(ctx, s.ID); err == nil {
		t.Fatal("Revoke erro")
	}
	if _, err := store.RevokeFamily(ctx, s.ID); err == nil {
		t.Fatal("RevokeFamily erro")
	}
	if _, _, err := store.Rotate(ctx, s.ID, Rotation{}); err == nil {
		t.Fatal("Rotate erro")
	}
	if err := store.Touch(ctx, s.ID); err == nil {
		t.Fatal("Touch erro")
	}
	if err := store.Insert(ctx, Session{}); err == nil {
		t.Fatal("Insert erro")
	}
	if _, _, err := store.ByID(ctx, s.ID); err == nil {
		t.Fatal("ByID erro")
	}
	if NewMemoryStore(nil).now == nil {
		t.Fatal("relógio padrão")
	}
}
