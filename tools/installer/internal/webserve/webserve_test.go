package webserve

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The cases of rewriteUri() in apps/web/tools/serve-dist.mjs.
func TestRewriteURI(t *testing.T) {
	for in, want := range map[string]string{
		"/": "/index.html", "/app": "/app.html", "/blog/": "/blog.html", "/blog/post": "/blog/post.html",
		"/_astro/x.js": "/_astro/x.js", "/favicon.svg": "/favicon.svg", "/v1.2/page": "/v1.2/page.html",
	} {
		if got := RewriteURI(in); got != want {
			t.Errorf("RewriteURI(%q) = %q; quero %q", in, got, want)
		}
	}
}

func site(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"index.html":       `<meta http-equiv="content-security-policy" content="connect-src 'self' http://dm-api.invalid"><link rel="canonical" href="http://dm-site.invalid/">`,
		"app.html":         "app",
		"404.html":         "não achei",
		"_astro/app.js":    `const api="http://dm-api.invalid";`,
		"_astro/plain.js":  "console.log(1)",
		"favicon.svg":      "<svg/>",
		"blog/post-1.html": "post",
	}
	for name, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func get(t *testing.T, h http.Handler, method, path string) *http.Response {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), method, path, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Result()
}

func body(t *testing.T, res *http.Response) string {
	t.Helper()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestServe(t *testing.T) {
	h, err := New(site(t), map[string]string{SitePlaceholder: "http://localhost:14321", APIPlaceholder: "http://localhost:18080"})
	if err != nil {
		t.Fatal(err)
	}
	if h.Rendered() != 2 {
		t.Fatalf("arquivos com placeholder: %d", h.Rendered())
	}

	res := get(t, h, http.MethodGet, "/")
	b := body(t, res)
	if res.StatusCode != http.StatusOK || !strings.Contains(b, "connect-src 'self' http://localhost:18080") || !strings.Contains(b, `href="http://localhost:14321/"`) || strings.Contains(b, ".invalid") {
		t.Fatalf("index: %d %s", res.StatusCode, b)
	}
	if res.Header.Get("Cache-Control") != "no-cache" || res.Header.Get("Content-Type") != "text/html; charset=utf-8" || res.Header.Get("X-Frame-Options") != "DENY" {
		t.Fatalf("cabeçalhos: %v", res.Header)
	}

	res = get(t, h, http.MethodGet, "/_astro/app.js")
	if b := body(t, res); b != `const api="http://localhost:18080";` || res.Header.Get("Cache-Control") != "no-cache" {
		t.Fatalf("bundle com origem: %q %v", b, res.Header.Get("Cache-Control"))
	}
	res = get(t, h, http.MethodGet, "/_astro/plain.js")
	if res.Header.Get("Cache-Control") != "public, max-age=31536000, immutable" {
		t.Fatalf("bundle sem origem fica imutável: %v", res.Header.Get("Cache-Control"))
	}

	if res = get(t, h, http.MethodGet, "/app"); res.StatusCode != http.StatusOK || body(t, res) != "app" {
		t.Fatal("URL limpa")
	}
	if res = get(t, h, http.MethodGet, "/blog/post-1?x=1"); res.StatusCode != http.StatusOK {
		t.Fatal("subpasta")
	}
	res = get(t, h, http.MethodGet, "/nada")
	if res.StatusCode != http.StatusNotFound || body(t, res) != "não achei" {
		t.Fatal("404.html")
	}
	if res = get(t, h, http.MethodHead, "/app"); res.StatusCode != http.StatusOK || body(t, res) != "" || res.Header.Get("Content-Length") != "3" {
		t.Fatal("HEAD")
	}
	if res = get(t, h, http.MethodPost, "/app"); res.StatusCode != http.StatusMethodNotAllowed || res.Header.Get("Allow") != "GET, HEAD" {
		t.Fatal("POST")
	}
	for _, p := range []string{"/../../etc/passwd", "/%2e%2e/%2e%2e/etc/passwd", "/blog/../../x"} {
		if res = get(t, h, http.MethodGet, p); res.StatusCode == http.StatusOK {
			t.Fatalf("%s escapou da raiz", p)
		}
	}
	if res = get(t, h, http.MethodGet, "/a%00b"); res.StatusCode != http.StatusBadRequest {
		t.Fatalf("NUL: %d", res.StatusCode)
	}
}

func TestNewAndOrigin(t *testing.T) {
	if _, err := New(t.TempDir(), nil); err == nil {
		t.Fatal("sem index.html")
	}
	if v, err := Origin("X", " http://localhost:4321/ "); err != nil || v != "http://localhost:4321" {
		t.Fatal(v, err)
	}
	for _, bad := range []string{"localhost:4321", "ftp://x", "http://x/app", "http://"} {
		if _, err := Origin("X", bad); err == nil {
			t.Errorf("Origin(%q) devia falhar", bad)
		}
	}
	h, err := New(site(t), nil)
	if err != nil || h.Rendered() != 0 {
		t.Fatal(err)
	}
}
