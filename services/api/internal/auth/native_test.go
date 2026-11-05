package auth

import (
	"context"
	"net/http"
	"sync"
	"testing"

	"github.com/google/uuid"
)

func TestMethodsReflectTheConfiguration(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	rec := h.do(req{method: http.MethodGet, path: MethodsPath})
	if rec.Code != 200 {
		t.Fatalf("methods: %d %s", rec.Code, rec.Body.String())
	}
	m := decode(t, rec)
	if m["email"] != true || m["google"] != true || m["apple"] != true || m["password"] != true || m["dev"] != false {
		t.Fatalf("todos os provedores do harness, sem login local: %v", m)
	}
	h = newHarnessWith(t, func(d *Deps) { d.Google, d.Apple, d.EmailEnabled, d.DevLogin = nil, nil, false, true })
	m = decode(t, h.do(req{method: http.MethodGet, path: MethodsPath}))
	if m["email"] != false || m["google"] != false || m["apple"] != false || m["dev"] != true {
		t.Fatalf("sem chaves, só o login local: %v", m)
	}
}

func TestDevLoginIsMountedOnlyWhenEnabled(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	if rec := h.post(DevLoginPath, `{}`); rec.Code != http.StatusNotFound {
		t.Fatalf("sem DevLogin a rota não existe: %d", rec.Code)
	}
}

func TestDevLoginCreatesAndReusesTheAccount(t *testing.T) {
	t.Parallel()
	var (
		mu     sync.Mutex
		grants []uuid.UUID
	)
	h := newHarnessWith(t, func(d *Deps) {
		d.DevLogin = true
		d.DevGrant = func(_ context.Context, org uuid.UUID) error {
			mu.Lock()
			defer mu.Unlock()
			grants = append(grants, org)
			return nil
		}
	})
	rec := h.post(DevLoginPath, `{}`)
	if rec.Code != 200 {
		t.Fatalf("login local: %d %s", rec.Code, rec.Body.String())
	}
	checkSessionCookies(t, rec)
	body := decode(t, rec)
	if body["user"].(map[string]any)["email"] != DevLoginEmail {
		t.Fatalf("e-mail padrão: %v", body["user"])
	}
	org := h.orgOf(DevLoginEmail)
	// Same e-mail, another device: same account and organization, a second session.
	rec = h.do(req{method: http.MethodPost, path: DevLoginPath, body: `{}`, headers: map[string]string{"User-Agent": "docmatch-android/1.0"}})
	if rec.Code != 200 || h.orgOf(DevLoginEmail) != org {
		t.Fatalf("segundo dispositivo: %d", rec.Code)
	}
	if len(grants) != 2 || grants[0] != org || grants[1] != org {
		t.Fatalf("o crédito local é pedido a cada login (a concessão é idempotente por organização): %v", grants)
	}
	rec = h.post(DevLoginPath, `{"email":"Bia@Exemplo.com.br","name":"Bia"}`)
	if rec.Code != 200 || decode(t, rec)["user"].(map[string]any)["email"] != "bia@exemplo.com.br" {
		t.Fatalf("outro e-mail: %d %s", rec.Code, rec.Body.String())
	}
	if h.orgOf("bia@exemplo.com.br") == org {
		t.Fatal("outro e-mail é outra conta")
	}
	for _, bad := range []string{`{"email":"sem-arroba"}`, `{"email":"a@b"}`, `{`} {
		if rec := h.post(DevLoginPath, bad); rec.Code != 400 {
			t.Fatalf("%s: %d", bad, rec.Code)
		}
	}
}

func TestGoogleNativeAcceptsTheServerAndAppAudiences(t *testing.T) {
	t.Parallel()
	h := newHarnessWith(t, func(d *Deps) { d.GoogleAudiences = []string{"android-client"} })
	// Android: the token is issued to the server (web) client id.
	token := h.google.sign(h.google.claims(h.now, "", map[string]any{"sub": "g-native", "email": "gi@example.com"}))
	rec := h.do(req{method: http.MethodPost, path: GoogleNativePath, body: `{"id_token":"` + token + `"}`, headers: map[string]string{"User-Agent": "docmatch-android/1.0"}})
	if rec.Code != 200 {
		t.Fatalf("audiência do servidor: %d %s", rec.Code, rec.Body.String())
	}
	checkSessionCookies(t, rec)
	org := h.orgOf("gi@example.com")
	// iOS: the token is issued to the app's own client id.
	token = h.google.sign(h.google.claims(h.now, "", map[string]any{"sub": "g-native", "email": "gi@example.com", "aud": "android-client"}))
	rec = h.do(req{method: http.MethodPost, path: GoogleNativePath, body: `{"id_token":"` + token + `"}`})
	if rec.Code != 200 || h.orgOf("gi@example.com") != org {
		t.Fatalf("audiência do app: %d %s", rec.Code, rec.Body.String())
	}
	// Someone else's app: refused.
	token = h.google.sign(h.google.claims(h.now, "", map[string]any{"aud": "foreign-client"}))
	if rec := h.post(GoogleNativePath, `{"id_token":"`+token+`"}`); rec.Code != 400 || decode(t, rec)["code"] != "oauth_state_mismatch" {
		t.Fatalf("audiência estranha: %d %s", rec.Code, rec.Body.String())
	}
	for _, bad := range []string{`{"id_token":""}`, `{}`, `{`} {
		if rec := h.post(GoogleNativePath, bad); rec.Code != 400 {
			t.Fatalf("%s: %d", bad, rec.Code)
		}
	}
}

func TestGoogleNativeLinksTheEmailAccount(t *testing.T) {
	t.Parallel()
	h := newHarnessWith(t, func(d *Deps) { d.DevLogin = true })
	// An account made on the web (here by the local sign-in, same path as the e-mail code)...
	if rec := h.post(DevLoginPath, `{"email":"ana@example.com"}`); rec.Code != 200 {
		t.Fatalf("conta por e-mail: %d", rec.Code)
	}
	org := h.orgOf("ana@example.com")
	// ...is the account the phone's Google sign-in lands on.
	token := h.google.sign(h.google.claims(h.now, "", map[string]any{"sub": "g-ana"}))
	rec := h.do(req{method: http.MethodPost, path: GoogleNativePath, body: `{"id_token":"` + token + `"}`, headers: map[string]string{"User-Agent": "docmatch-ios/1.0"}})
	if rec.Code != 200 || h.orgOf("ana@example.com") != org {
		t.Fatalf("Google no celular cai na mesma conta: %d %s", rec.Code, rec.Body.String())
	}
	u, _, _ := h.store.UserByEmail(context.Background(), "ana@example.com")
	if u.GoogleSub != "g-ana" {
		t.Fatalf("o Google fica ligado à conta: %+v", u)
	}
}

func TestGoogleNativeUnavailableWithoutKeys(t *testing.T) {
	t.Parallel()
	h := newHarnessWith(t, func(d *Deps) { d.Google = nil })
	if rec := h.post(GoogleNativePath, `{"id_token":"x"}`); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("sem Google: %d", rec.Code)
	}
}

func TestPlatformOf(t *testing.T) {
	t.Parallel()
	for ua, want := range map[string]string{
		"docmatch-android/1.2.0 (Pixel 8; Android 15)": PlatformAndroid,
		"docmatch-ios/1.2.0 (iPhone; iOS 18.1)":        PlatformIOS,
		"Mozilla/5.0":                                  PlatformWeb,
		"":                                             PlatformWeb,
	} {
		r, _ := http.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("User-Agent", ua)
		if got := PlatformOf(r); got != want {
			t.Fatalf("%q: %s, quer %s", ua, got, want)
		}
	}
}
