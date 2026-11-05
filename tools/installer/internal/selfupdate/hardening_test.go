package selfupdate

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// S2: the binary replaces itself with what it downloads, so only https counts. http is refused
// as a base (DOCMATCH_INSTALL_BASE included) and as the target of a redirect; the tests' own
// loopback server is the one exception, and only when asked for.
func TestUpdateIsHTTPSOnly(t *testing.T) {
	m := newMemFS(false)
	u := updater(t, release(t, false), "linux", m)
	u.AllowLoopbackHTTP = false
	if _, err := u.Update(context.Background(), "", false, nil); !errors.Is(err, ErrInsecure) {
		t.Fatalf("http no 127.0.0.1 sem a exceção dos testes: %v", err)
	}
	for _, base := range []string{"http://example.invalid/install", "ftp://example.invalid/install", "file:///tmp/install", "example.invalid/install"} {
		u.Base = base
		if _, err := u.Latest(context.Background()); !errors.Is(err, ErrInsecure) {
			t.Errorf("%s: %v", base, err)
		}
	}
	u.AllowLoopbackHTTP = true
	u.Base = "http://example.invalid/install"
	if _, err := u.Latest(context.Background()); !errors.Is(err, ErrInsecure) {
		t.Fatalf("a exceção vale só para o loopback: %v", err)
	}
	if string(m.files[u.Exe]) != "binário antigo 0.1.0" {
		t.Fatal("nada foi trocado")
	}
}

func TestUpdateRefusesRedirectToHTTP(t *testing.T) {
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("9.9.9\n")) }))
	t.Cleanup(plain.Close)
	secure := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL+r.URL.Path, http.StatusFound)
	}))
	t.Cleanup(secure.Close)
	m := newMemFS(false)
	u := updater(t, secure.URL+"/install", "linux", m)
	u.AllowLoopbackHTTP = false
	u.HTTP = secure.Client() // trusts the test certificate
	_, err := u.Latest(context.Background())
	if !errors.Is(err, ErrInsecure) {
		t.Fatalf("https -> http: %v", err)
	}
	// The caller's client is not changed by the update.
	if secure.Client().CheckRedirect != nil {
		t.Fatal("o cliente de quem chamou ficou com o CheckRedirect")
	}
}

// S4: the delayed delete of Windows runs System32's own programs from System32 and refuses a
// path cmd.exe would expand.
func TestLaterCommand(t *testing.T) {
	exe, line, dir, err := laterCommand(`C:\Windows`, `C:\Users\ana\AppData\Local\docmatch\bin\docmatch.exe.old`)
	if err != nil {
		t.Fatal(err)
	}
	if exe != `C:\Windows\System32\cmd.exe` || dir != `C:\Windows\System32` {
		t.Fatalf("%s %s", exe, dir)
	}
	want := `"C:\Windows\System32\cmd.exe" /d /s /c ""C:\Windows\System32\PING.EXE" -n 3 127.0.0.1 >nul & ` +
		`del /f /q "C:\Users\ana\AppData\Local\docmatch\bin\docmatch.exe.old" & ` +
		`rmdir "C:\Users\ana\AppData\Local\docmatch\bin" 2>nul & rmdir "C:\Users\ana\AppData\Local\docmatch" 2>nul"`
	if line != want {
		t.Fatalf("%s\n%s", line, want)
	}
	if _, line, _, _ = laterCommand("", `D:\tools\docmatch.exe.old`); strings.Contains(line, "rmdir") || !strings.Contains(line, `C:\Windows\System32\PING.EXE`) {
		t.Fatalf("fora de docmatch\\bin só o arquivo sai: %s", line)
	}
	for _, bad := range []string{`C:\Users\%USERNAME%\docmatch.exe.old`, `C:\a"b\docmatch.exe.old`} {
		if _, _, _, err := laterCommand(`C:\Windows`, bad); err == nil {
			t.Errorf("%s devia ser recusado", bad)
		}
	}
}
