//go:build integration

package extensions

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/riverqueue/river"

	"github.com/mmtmn/docmatch/services/api/internal/api"
	"github.com/mmtmn/docmatch/services/api/internal/config"
	"github.com/mmtmn/docmatch/services/api/internal/crypto"
	"github.com/mmtmn/docmatch/services/api/internal/httpx"
	"github.com/mmtmn/docmatch/services/api/internal/projects"
	"github.com/mmtmn/docmatch/services/api/internal/publicid"
	"github.com/mmtmn/docmatch/services/api/internal/runs/apix"
	"github.com/mmtmn/docmatch/services/api/internal/session"
	"github.com/mmtmn/docmatch/services/api/internal/session/sessionpg"
	"github.com/mmtmn/docmatch/services/api/internal/tenancy"
	"github.com/mmtmn/docmatch/services/api/internal/testdb"
)

func TestMain(m *testing.M) { testdb.Main(m) }

type fixture struct {
	s       *Service
	cfg     *config.Config
	manager *session.Manager
	p       session.Principal
	cookies []*http.Cookie
	handler http.Handler
}

func setup(t *testing.T) *fixture {
	t.Helper()
	d := testdb.Get(t)
	user, org := uuid.New(), uuid.New()
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO orgs(id,name,slug) VALUES($1,'Projeto da extensão',$2)`, []any{org, org.String()}},
		{`INSERT INTO users(id,email,email_verified_at) VALUES($1,$2,now())`, []any{user, user.String() + "@example.test"}},
		{`INSERT INTO memberships(org_id,user_id,role) VALUES($1,$2,'owner')`, []any{org, user}},
	} {
		if _, err := d.Admin.Exec(t.Context(), q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}
	base, _ := url.Parse("https://docmatch.example.invalid")
	cfg := &config.Config{Env: config.EnvDev, PublicBaseURL: base, APIBaseURL: base, Auth: config.Auth{SessionSecret: crypto.RandomBytes(32), ExtensionClients: []config.ExtensionClient{{ID: "chrome-local", Name: "Chrome local", RedirectURIs: []string{"https://extension.example.invalid/"}}}}}
	s, err := New(cfg, d.App)
	if err != nil {
		t.Fatal(err)
	}
	manager := session.NewManager(sessionpg.New(d.App), session.Config{AllowedOrigins: []string{base.String()}})
	w := httptest.NewRecorder()
	row, err := manager.Issue(t.Context(), w, httptest.NewRequest(http.MethodPost, base.String(), nil), user, org)
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{s: s, cfg: cfg, manager: manager, p: session.Principal{SessionID: row.ID, UserID: user, OrgID: org}, cookies: w.Result().Cookies()}
	tenants := tenancy.New(d.App)
	cookieResolver := func(r *http.Request) (apix.Principal, error) {
		p, ok := session.FromContext(r.Context())
		if !ok {
			return apix.Principal{}, apix.ErrUnauthenticated
		}
		return apix.Principal{OrgID: p.OrgID, UserID: &p.UserID, Plan: "starter"}, nil
	}
	other := func(r *http.Request) (apix.Principal, error) {
		if r.Header.Get("Authorization") != "" {
			return apix.Principal{}, apix.ErrUnauthenticated
		}
		return cookieResolver(r)
	}
	resolver := s.Resolve(cookieResolver, other)
	projectSvc := projects.New(d.App, resolver, nil)
	f.handler = api.NewHandler(api.Deps{Config: cfg, Mount: []func(*httpx.Router){s.Mount, tenants.Mount, projectSvc.Mount, func(r *httpx.Router) {
		r.Handle(http.MethodGet, "/v1/me", httpx.BodyNone, session.RequireFunc(func(w http.ResponseWriter, r *http.Request) {
			p, _ := session.FromContext(r.Context())
			apix.WriteJSON(w, 200, map[string]any{"user": p.UserID})
		}))
	}}, Middleware: []httpx.Middleware{manager.Middleware, s.Middleware, tenants.Middleware}, OriginExempt: OriginExempt})
	return f
}
func (f *fixture) call(t *testing.T, method, path string, body any, cookie bool, token string, want int) *httptest.ResponseRecorder {
	t.Helper()
	var data []byte
	var err error
	if raw, ok := body.(string); ok {
		data = []byte(raw)
	} else if body != nil {
		data, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	r := httptest.NewRequest(method, f.cfg.PublicBaseURL.String()+path, bytes.NewReader(data))
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	if cookie {
		for _, c := range f.cookies {
			r.AddCookie(c)
		}
		r.Header.Set("Origin", f.cfg.PublicBaseURL.String())
	} else {
		r.Header.Set("Origin", "chrome-extension://test")
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	if w.Code != want {
		t.Fatalf("%s %s: status %d, wanted %d; %s", method, path, w.Code, want, w.Body.String())
	}
	return w
}
func (f *fixture) code(t *testing.T) (TokenRequest, string) {
	t.Helper()
	verifier := crypto.RandomToken(32)
	state := crypto.RandomToken(32)
	in := AuthorizeRequest{ClientID: "chrome-local", RedirectURI: "https://extension.example.invalid/", CodeChallenge: challenge(verifier), CodeChallengeMethod: "S256", State: state}
	w := f.call(t, "POST", "/v1/extensions/authorize", in, true, "", 200)
	var out map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	target, err := url.Parse(out["redirect_uri"])
	if err != nil {
		t.Fatal(err)
	}
	if target.Scheme+"://"+target.Host+target.Path != in.RedirectURI || target.Query().Get("state") != state {
		t.Fatal("incorrect authorization destination")
	}
	return TokenRequest{GrantType: "authorization_code", ClientID: in.ClientID, Code: target.Query().Get("code"), RedirectURI: in.RedirectURI, CodeVerifier: verifier}, state
}
func (f *fixture) issue(t *testing.T) Tokens {
	t.Helper()
	in, _ := f.code(t)
	w := f.call(t, "POST", "/v1/extensions/token", in, false, "", 200)
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("tokens cacheable")
	}
	var out Tokens
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestConsentPKCEAndOneUseCodes(t *testing.T) {
	f := setup(t)
	f.call(t, "GET", "/v1/extensions/clients/chrome-local?redirect_uri="+url.QueryEscape("https://extension.example.invalid/"), nil, false, "", 200)
	f.call(t, "GET", "/v1/extensions/clients/chrome-local?redirect_uri="+url.QueryEscape("https://extension.example.invalid/evil"), nil, false, "", 400)
	f.call(t, "POST", "/v1/extensions/authorize", map[string]string{}, false, "", 403)
	f.call(t, "POST", "/v1/extensions/authorize", "{broken", true, "", 400)
	in, _ := f.code(t)
	for _, bad := range []TokenRequest{
		{GrantType: in.GrantType, ClientID: "unknown", Code: in.Code, RedirectURI: in.RedirectURI, CodeVerifier: in.CodeVerifier},
		{GrantType: in.GrantType, ClientID: in.ClientID, Code: in.Code, RedirectURI: in.RedirectURI + "evil", CodeVerifier: in.CodeVerifier},
		{GrantType: in.GrantType, ClientID: in.ClientID, Code: in.Code, RedirectURI: in.RedirectURI, CodeVerifier: crypto.RandomToken(32)},
		{GrantType: in.GrantType, ClientID: in.ClientID, Code: "invalid", RedirectURI: in.RedirectURI, CodeVerifier: in.CodeVerifier},
		{GrantType: in.GrantType, ClientID: in.ClientID, Code: crypto.RandomToken(32), RedirectURI: in.RedirectURI, CodeVerifier: in.CodeVerifier},
	} {
		f.call(t, "POST", "/v1/extensions/token", bad, false, "", 401)
	}
	f.call(t, "POST", "/v1/extensions/token", in, true, "", 403)
	w := f.call(t, "POST", "/v1/extensions/token", in, false, "", 200)
	var pair Tokens
	if err := json.Unmarshal(w.Body.Bytes(), &pair); err != nil {
		t.Fatal(err)
	}
	if pair.TokenType != "Bearer" || pair.ExpiresIn != 600 {
		t.Fatal("wrong lifetime")
	}
	f.call(t, "GET", "/v1/me/organizations", nil, false, pair.AccessToken, 200)
	// A consumed authorization code is rejected and its grant is revoked.
	f.call(t, "POST", "/v1/extensions/token", in, false, "", 401)
	f.call(t, "GET", "/v1/me/organizations", nil, false, pair.AccessToken, 401)
}

func TestExtensionCredentialsCannotBecomeCookiesOrAdminKeys(t *testing.T) {
	f := setup(t)
	pair := f.issue(t)
	for _, path := range []string{"/v1/team", "/v1/api-keys", "/v1/me/password", "/v1/auth/refresh", "/v1/extensions/authorize", "/v1/runs"} {
		f.call(t, "POST", path, map[string]any{}, false, pair.AccessToken, 403)
	}
	f.call(t, "GET", "/v1/me", nil, true, pair.AccessToken, 403)
	id, _ := tokenID(pair.AccessToken, accessPrefix)
	secret := strings.Split(pair.AccessToken, ".")[1]
	req := httptest.NewRequest(http.MethodGet, "https://docmatch.example.invalid/v1/me", nil)
	req.AddCookie(&http.Cookie{Name: session.CookieSession, Value: id.String() + "." + secret})
	if _, err := f.manager.Authenticate(t.Context(), req); err == nil {
		t.Fatal("extension became a cookie session")
	}
	// The regular project engine sees an app caller, never a paid API-key lane.
	f.call(t, "POST", "/v1/projects", map[string]string{"name": "Fechamento"}, false, pair.AccessToken, 201)
	f.call(t, "GET", "/v1/projects", nil, false, pair.AccessToken, 200)
	f.call(t, "GET", "/v1/me", nil, false, pair.AccessToken, 200)
	// Logout works even if membership in the formerly selected account is removed.
	other := setup(t)
	r := httptest.NewRequest(http.MethodGet, "https://docmatch.example.invalid/v1/projects", nil)
	r.Header.Set("Authorization", "Bearer "+pair.AccessToken)
	r.Header.Set(tenancy.Header, publicid.Encode("org", other.p.OrgID))
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("foreign account reachable")
	}
	f.call(t, "POST", "/v1/extensions/revoke", nil, false, pair.AccessToken, 204)
	f.call(t, "GET", "/v1/me/organizations", nil, false, pair.AccessToken, 401)
}

func TestRefreshSurvivesRestartAndConcurrentLostResponse(t *testing.T) {
	f := setup(t)
	first := f.issue(t)
	in := TokenRequest{GrantType: "refresh_token", ClientID: "chrome-local", RefreshToken: first.RefreshToken, RequestID: uuid.NewString()}
	var got [2]Tokens
	var errs [2]error
	var wg sync.WaitGroup
	for i := range got {
		wg.Go(func() { got[i], errs[i] = f.s.Refresh(t.Context(), in) })
	}
	wg.Wait()
	if errs[0] != nil || errs[1] != nil {
		t.Fatal(errs)
	}
	if got[0].AccessToken != got[1].AccessToken || got[0].RefreshToken != got[1].RefreshToken || got[0].AccessToken == first.AccessToken {
		t.Fatal("concurrent refresh returned differing credentials")
	}
	restarted, err := New(f.cfg, testdb.Get(t).App)
	if err != nil {
		t.Fatal(err)
	}
	again, err := restarted.Refresh(t.Context(), in)
	if err != nil || again.RefreshToken != got[0].RefreshToken {
		t.Fatal("lost-response retry after restart", err)
	}
	f.call(t, "GET", "/v1/me/organizations", nil, false, first.AccessToken, 401)
	f.call(t, "GET", "/v1/me/organizations", nil, false, got[0].AccessToken, 200)
	// A random secret cannot revoke a known UUID.
	id, _ := tokenID(got[0].RefreshToken, refreshPrefix)
	bad := in
	bad.RefreshToken = refreshPrefix + id.String() + "." + crypto.RandomToken(32)
	if _, err := restarted.Refresh(t.Context(), bad); err == nil {
		t.Fatal("random refresh accepted")
	}
	f.call(t, "GET", "/v1/me/organizations", nil, false, got[0].AccessToken, 200)
	second := in
	second.RefreshToken = got[0].RefreshToken
	second.RequestID = uuid.NewString()
	newest, err := restarted.Refresh(t.Context(), second)
	if err != nil {
		t.Fatal(err)
	}
	// Replaying an older, genuinely issued secret also revokes the family.
	in.RequestID = uuid.NewString()
	if _, err := restarted.Refresh(t.Context(), in); err == nil {
		t.Fatal("replayed refresh accepted")
	}
	f.call(t, "GET", "/v1/me/organizations", nil, false, newest.AccessToken, 401)
}

func TestExtensionExpiryRevocationAndSuspension(t *testing.T) {
	for _, scenario := range []string{"code_expired", "source_revoked", "access_expired", "session_expired", "device_revoked", "suspended", "client_disabled", "retry_expired"} {
		t.Run(scenario, func(t *testing.T) {
			f := setup(t)
			d := testdb.Get(t)
			exec := func(query string, args ...any) {
				t.Helper()
				if _, err := d.Admin.Exec(t.Context(), query, args...); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "code_expired" || scenario == "source_revoked" {
				in, _ := f.code(t)
				if scenario == "code_expired" {
					exec(`UPDATE extension_codes SET expires_at=now()-interval '1 second' WHERE code_hash=$1`, crypto.Digest(in.Code))
				} else {
					exec(`UPDATE sessions SET revoked_at=now() WHERE id=$1`, f.p.SessionID)
				}
				f.call(t, "POST", "/v1/extensions/token", in, false, "", 401)
				return
			}
			pair := f.issue(t)
			id, _ := tokenID(pair.AccessToken, accessPrefix)
			switch scenario {
			case "access_expired":
				exec(`UPDATE sessions SET access_expires_at=now()-interval '1 second' WHERE id=$1`, id)
				if _, err := f.s.Authenticate(t.Context(), pair.AccessToken); !errors.Is(err, apix.ErrSessionExpired) {
					t.Fatal(err)
				}
				f.call(t, "POST", "/v1/extensions/token", TokenRequest{GrantType: "refresh_token", ClientID: "chrome-local", RefreshToken: pair.RefreshToken, RequestID: uuid.NewString()}, false, "", 200)
				return
			case "session_expired":
				exec(`UPDATE sessions SET expires_at=now()-interval '1 second' WHERE id=$1`, id)
			case "device_revoked":
				if _, err := f.manager.Store().RevokeUser(t.Context(), f.p.UserID, f.p.SessionID); err != nil {
					t.Fatal(err)
				}
			case "suspended":
				exec(`UPDATE users SET suspended_at=now() WHERE id=$1`, f.p.UserID)
			case "client_disabled":
				delete(f.s.clients, "chrome-local")
			case "retry_expired":
				in := TokenRequest{ClientID: "chrome-local", RefreshToken: pair.RefreshToken, RequestID: uuid.NewString()}
				var err error
				pair, err = f.s.Refresh(t.Context(), in)
				if err != nil {
					t.Fatal(err)
				}
				exec(`UPDATE extension_grants SET retry_until=now()-interval '1 second' WHERE session_id=$1`, id)
				if _, err = f.s.Refresh(t.Context(), in); err == nil {
					t.Fatal("expired retry accepted")
				}
			}
			f.call(t, "GET", "/v1/me/organizations", nil, false, pair.AccessToken, 401)
			f.call(t, "POST", "/v1/extensions/token", TokenRequest{GrantType: "refresh_token", ClientID: "chrome-local", RefreshToken: pair.RefreshToken, RequestID: uuid.NewString()}, false, "", 401)
		})
	}
}

func TestConsentLimitsAndInvalidRequests(t *testing.T) {
	f := setup(t)
	for _, in := range []AuthorizeRequest{{}, {ClientID: "chrome-local", RedirectURI: "https://extension.example.invalid/", CodeChallenge: crypto.RandomToken(32), State: crypto.RandomToken(32), CodeChallengeMethod: "plain"}} {
		if _, err := f.s.Authorize(t.Context(), f.p, in); err == nil {
			t.Fatal("bad consent accepted")
		}
	}
	for range 10 {
		f.code(t)
	}
	_, err := f.s.Authorize(t.Context(), f.p, AuthorizeRequest{ClientID: "chrome-local", RedirectURI: "https://extension.example.invalid/", CodeChallenge: crypto.RandomToken(32), State: crypto.RandomToken(32), CodeChallengeMethod: "S256"})
	if err == nil {
		t.Fatal("issuance limit ignored")
	}
	f.call(t, "POST", "/v1/extensions/token", "{broken", false, "", 400)
	f.call(t, "POST", "/v1/extensions/token", map[string]string{"grant_type": "password"}, false, "", 400)
	f.call(t, "POST", "/v1/extensions/revoke", nil, true, "", 403)
	if _, err := f.s.Refresh(t.Context(), TokenRequest{RefreshToken: "invalid"}); err == nil {
		t.Fatal("invalid refresh accepted")
	}
	if _, err := f.s.Refresh(t.Context(), TokenRequest{RefreshToken: refreshPrefix + uuid.NewString() + "." + crypto.RandomToken(32)}); err == nil {
		t.Fatal("invalid retry UUID accepted")
	}
	f2 := setup(t)
	if _, err := testdb.Get(t).Admin.Exec(t.Context(), `UPDATE users SET email_verified_at=NULL WHERE id=$1`, f2.p.UserID); err != nil {
		t.Fatal(err)
	}
	_, err = f2.s.Authorize(t.Context(), f2.p, AuthorizeRequest{ClientID: "chrome-local", RedirectURI: "https://extension.example.invalid/", CodeChallenge: crypto.RandomToken(32), State: crypto.RandomToken(32), CodeChallengeMethod: "S256"})
	if err == nil {
		t.Fatal("unverified identity authorized")
	}
}

func TestExpiryUsesDatabaseClockAfterLock(t *testing.T) {
	f := setup(t)
	in, _ := f.code(t)
	d := testdb.Get(t)
	tx, err := d.Admin.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(t.Context()) }()
	if _, err = tx.Exec(t.Context(), `UPDATE extension_codes SET expires_at=clock_timestamp()+interval '200 milliseconds' WHERE code_hash=$1`, crypto.Digest(in.Code)); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := f.s.Exchange(t.Context(), in); done <- err }()
	time.Sleep(300 * time.Millisecond)
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = <-done; err == nil {
		t.Fatal("code expired while waiting for lock was exchanged")
	}
}

func TestCredentialCleanupPreservesLiveSessions(t *testing.T) {
	f := setup(t)
	pair := f.issue(t)
	id, _ := tokenID(pair.AccessToken, accessPrefix)
	in := TokenRequest{ClientID: "chrome-local", RefreshToken: pair.RefreshToken, RequestID: uuid.NewString()}
	current, err := f.s.Refresh(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	d := testdb.Get(t)
	var cipher []byte
	if err = d.Admin.QueryRow(t.Context(), `SELECT retry_response FROM extension_grants WHERE session_id=$1`, id).Scan(&cipher); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(cipher, []byte(current.AccessToken)) || bytes.Contains(cipher, []byte(current.RefreshToken)) {
		t.Fatal("retry cache contains plaintext credentials")
	}
	for _, query := range []string{
		`UPDATE extension_codes SET expires_at=now()-interval '2 days' WHERE issued_session=$1`,
		`UPDATE extension_grants SET retry_until=now()-interval '1 minute' WHERE session_id=$1`,
		`UPDATE extension_spent_refresh SET expires_at=now()-interval '1 minute' WHERE session_id=$1`,
	} {
		if _, err = d.Admin.Exec(t.Context(), query, id); err != nil {
			t.Fatal(err)
		}
	}
	worker := &cleanupWorker{pool: d.App}
	if err = worker.Work(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`SELECT count(*) FROM extension_codes WHERE issued_session=$1`,
		`SELECT count(*) FROM extension_grants WHERE session_id=$1 AND retry_response IS NOT NULL`,
		`SELECT count(*) FROM extension_spent_refresh WHERE session_id=$1`,
	} {
		var n int
		if err = d.Admin.QueryRow(t.Context(), query, id).Scan(&n); err != nil || n != 0 {
			t.Fatal("expired credential state retained", n, err)
		}
	}
	f.call(t, "GET", "/v1/me/organizations", nil, false, current.AccessToken, 200)
	if _, err = d.Admin.Exec(t.Context(), `UPDATE sessions SET revoked_at=now()-interval '31 days' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if err = Cleanup(t.Context(), d.App); err != nil {
		t.Fatal(err)
	}
	var exists bool
	if err = d.Admin.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM extension_grants WHERE session_id=$1)`, id).Scan(&exists); err != nil || exists {
		t.Fatal("stale grant retained", err)
	}
	// Registration adds one lightweight periodic job on the existing River queue.
	workers := river.NewWorkers()
	AddWorkers(d.App)(workers)
	if len(PeriodicJobs()) != 1 || worker.Timeout(nil) > time.Minute || (cleanupArgs{}).Kind() == "" {
		t.Fatal("invalid cleanup registration")
	}
}

func TestExtensionLogoutAfterMembershipRemoval(t *testing.T) {
	f := setup(t)
	pair := f.issue(t)
	d := testdb.Get(t)
	replacement := uuid.New()
	if _, err := d.Admin.Exec(t.Context(), `INSERT INTO users(id,email,email_verified_at) VALUES($1,$2,now())`, replacement, replacement.String()+"@example.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Admin.Exec(t.Context(), `INSERT INTO memberships(org_id,user_id,role) VALUES($1,$2,'owner')`, f.p.OrgID, replacement); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Admin.Exec(t.Context(), `DELETE FROM memberships WHERE org_id=$1 AND user_id=$2`, f.p.OrgID, f.p.UserID); err != nil {
		t.Fatal(err)
	}
	f.call(t, "GET", "/v1/projects", nil, false, pair.AccessToken, 403)
	f.call(t, "POST", "/v1/extensions/revoke", nil, false, pair.AccessToken, 204)
}
