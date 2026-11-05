package account

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// loginAs issues a session the way a given client would (its User-Agent).
func (h *harness) loginAs(userID, orgID uuid.UUID, ua string) []*http.Cookie {
	h.t.Helper()
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.Header.Set("User-Agent", ua)
	if _, err := h.sessions.Issue(context.Background(), rec, r, userID, orgID); err != nil {
		h.t.Fatal(err)
	}
	return rec.Result().Cookies() //nolint:bodyclose // recorder
}

func TestDevicesAcrossClients(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	web := h.loginAs(h.user.ID, h.org.ID, "Mozilla/5.0 (X11; Linux x86_64; rv:140.0) Gecko/20100101 Firefox/140.0")
	h.now = h.now.Add(time.Minute)
	android := h.loginAs(h.user.ID, h.org.ID, "docmatch-android/1.0.0 (Pixel 8; Android 15)")
	h.now = h.now.Add(time.Minute)
	ios := h.loginAs(h.user.ID, h.org.ID, "docmatch-ios/1.0.0 (iPhone; iOS 18.1)")
	// Someone else's session never shows up.
	other, otherOrg := h.store.Seed("bia@example.com", "Bia")
	foreign := h.loginAs(other.ID, otherOrg.ID, "docmatch-ios/1.0.0 (iPhone; iOS 18.1)")

	rec := h.do(http.MethodGet, DevicesPath, "", android)
	if rec.Code != 200 {
		t.Fatalf("lista: %d %s", rec.Code, rec.Body.String())
	}
	items := body(t, rec)["items"].([]any)
	if len(items) != 3 {
		t.Fatalf("três dispositivos da mesma conta: %v", items)
	}
	first := items[0].(map[string]any)
	if first["current"] != true || first["platform"] != "android" || first["name"] != "App Android · Pixel 8" {
		t.Fatalf("a sessão atual vem primeiro: %v", first)
	}
	names := map[string]string{}
	var iosID string
	for _, it := range items {
		m := it.(map[string]any)
		names[m["platform"].(string)] = m["name"].(string)
		if !strings.HasPrefix(m["id"].(string), "ses_") {
			t.Fatalf("id público: %v", m["id"])
		}
		if m["platform"] == "ios" {
			iosID = m["id"].(string)
		}
	}
	if names["web"] != "Firefox · Linux" || names["ios"] != "App iOS · iPhone" {
		t.Fatalf("nomes: %v", names)
	}

	// The phone signs the iPhone out; the iPhone's next request is refused, the others go on.
	if rec := h.do(http.MethodDelete, DevicesPath+"/"+iosID, "", android); rec.Code != 204 {
		t.Fatalf("encerrar outra sessão: %d %s", rec.Code, rec.Body.String())
	}
	if rec := h.do(http.MethodGet, "/v1/me", "", ios); rec.Code != 401 {
		t.Fatalf("o iPhone saiu: %d", rec.Code)
	}
	if rec := h.do(http.MethodGet, "/v1/me", "", web); rec.Code != 200 {
		t.Fatalf("o navegador continua: %d", rec.Code)
	}
	if got := h.actions(&h.org.ID); !strings.Contains(strings.Join(got, ","), ActionSessionRevoked) {
		t.Fatalf("auditoria: %v", got)
	}
	// Another account's session id is a 404, not a revocation.
	foreignList := body(t, h.do(http.MethodGet, DevicesPath, "", foreign))["items"].([]any)
	foreignID := foreignList[0].(map[string]any)["id"].(string)
	if rec := h.do(http.MethodDelete, DevicesPath+"/"+foreignID, "", web); rec.Code != 404 {
		t.Fatalf("sessão alheia: %d", rec.Code)
	}
	if rec := h.do(http.MethodGet, "/v1/me", "", foreign); rec.Code != 200 {
		t.Fatalf("a sessão alheia continua: %d", rec.Code)
	}
	for _, bad := range []string{"ses_nao-existe", "usr_" + strings.Repeat("0", 26), iosID} {
		if rec := h.do(http.MethodDelete, DevicesPath+"/"+bad, "", web); rec.Code != 404 {
			t.Fatalf("%s: %d", bad, rec.Code)
		}
	}
	// Signing out the current device clears its cookies.
	webID := ""
	for _, it := range body(t, h.do(http.MethodGet, DevicesPath, "", web))["items"].([]any) {
		if m := it.(map[string]any); m["current"] == true {
			webID = m["id"].(string)
		}
	}
	rec = h.do(http.MethodDelete, DevicesPath+"/"+webID, "", web)
	if rec.Code != 204 {
		t.Fatalf("encerrar a própria: %d", rec.Code)
	}
	cleared := false
	for _, c := range rec.Result().Cookies() { //nolint:bodyclose // recorder
		if c.Name == "dm_session" && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Fatal("a sessão atual deveria limpar os cookies")
	}
	if rec := h.do(http.MethodGet, DevicesPath, "", nil); rec.Code != 401 {
		t.Fatalf("sem sessão: %d", rec.Code)
	}
}

func TestDescribeUserAgent(t *testing.T) {
	t.Parallel()
	for ua, want := range map[string][2]string{
		"docmatch-android/1.0.0 (Galaxy S24; Android 15)": {"android", "App Android · Galaxy S24"},
		"docmatch-android/1.0.0":                          {"android", "App Android"},
		"docmatch-ios/1.0.0 (iPad; iPadOS 18.1)":          {"ios", "App iOS · iPad"},
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0 Safari/537.36":                             {"web", "Chrome · Windows"},
		"Mozilla/5.0 (Windows NT 10.0) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0 Safari/537.36 Edg/131.0":                               {"web", "Edge · Windows"},
		"Mozilla/5.0 (iPhone; CPU iPhone OS 18_1 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.1 Mobile/15E148 Safari/604.1": {"web", "Safari · iPhone"},
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 14_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.1 Safari/605.1.15":                      {"web", "Safari · macOS"},
		"curl/8.5": {"web", "Navegador"},
		"":         {"web", "Navegador"},
	} {
		p, n := DescribeUserAgent(ua)
		if p != want[0] || n != want[1] {
			t.Fatalf("%q: %s/%s, quer %s/%s", ua, p, n, want[0], want[1])
		}
	}
}
