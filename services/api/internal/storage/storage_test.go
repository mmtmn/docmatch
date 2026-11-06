package storage

import (
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/mmtmn/docmatch/services/api/internal/httpx"
)

func TestLocalSignedBrowserUploadOrigin(t *testing.T) {
	t.Parallel()
	local, _ := newLocal(t)
	ticket, err := local.PresignPut(context.Background(), "media/browser", 5, "text/plain", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	handler := httpx.OriginCheckMiddleware([]string{"https://site.invalid"}, local.OriginExempt)(local)
	for _, tc := range []struct {
		name, target, header, value string
		status                      int
	}{
		{"signed write", ticket.URL, "", "", http.StatusOK},
		{"ambient cookie", ticket.URL, "Cookie", "dm_session=ambient", http.StatusForbidden},
		{"ambient authorization", ticket.URL, "Authorization", "Bearer unrelated", http.StatusForbidden},
		{"invalid signature", ticket.URL + "x", "", "", http.StatusForbidden},
		{"duplicate signature", ticket.URL + "&sig=other", "", "", http.StatusForbidden},
		{"other route", "http://api.invalid/v1/projects", "", "", http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPut, tc.target, strings.NewReader("hello"))
			r.Header.Set("Origin", "chrome-extension://local-test")
			if tc.header != "" {
				r.Header.Set(tc.header, tc.value)
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("got %d, want %d", w.Code, tc.status)
			}
		})
	}
	var absent *LocalStore
	if absent.OriginExempt(httptest.NewRequest(http.MethodPut, ticket.URL, nil)) {
		t.Fatal("absent store allowed write")
	}
	local.now = func() time.Time { return time.Now().Add(2 * time.Minute) }
	if local.OriginExempt(httptest.NewRequest(http.MethodPut, ticket.URL, nil)) {
		t.Fatal("expired signature allowed write")
	}
}

func TestValidateKeyAndKeys(t *testing.T) {
	t.Parallel()
	ok := []string{"a", "quarantine/org/upl/abc", "media/x.y-z_1"}
	bad := []string{"", "/abs", "a//b", "../x", "a/../b", "a b", "a\x00", strings.Repeat("a", 513), ".hidden", "a/.b"}
	for _, k := range ok {
		if err := ValidateKey(k); err != nil {
			t.Errorf("%q deveria ser válida: %v", k, err)
		}
	}
	for _, k := range bad {
		if err := ValidateKey(k); err == nil {
			t.Errorf("%q deveria ser inválida", k)
		}
	}
	org, up := uuid.New(), uuid.New()
	q := QuarantineKey(org, up)
	if !strings.HasPrefix(q, PrefixQuarantine+"/"+org.String()+"/"+up.String()+"/") || ValidateKey(q) != nil || q == QuarantineKey(org, up) {
		t.Fatalf("quarantine key = %s", q)
	}
	if m := MediaKey(org, up); !strings.HasPrefix(m, PrefixMedia+"/") || ValidateKey(m) != nil {
		t.Fatalf("media key = %s", m)
	}
}

func newLocal(t *testing.T) (*LocalStore, *httptest.Server) {
	t.Helper()
	var local *LocalStore
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { local.ServeHTTP(w, r) }))
	t.Cleanup(srv.Close)
	base, _ := url.Parse(srv.URL)
	var err error
	local, err = NewLocal(t.TempDir(), "bucket", base)
	if err != nil {
		t.Fatal(err)
	}
	return local, srv
}

func TestLocalPresignedPut(t *testing.T) {
	t.Parallel()
	local, _ := newLocal(t)
	ctx := context.Background()
	if local.Name() != NameLocal || local.Bucket() != "bucket" || local.URI("k") != "local://bucket/k" {
		t.Fatal("metadados")
	}
	body := []byte("hello media")
	put, err := local.PresignPut(ctx, "media/a/b", int64(len(body)), "audio/mpeg", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if put.Headers["Content-Type"] != "audio/mpeg" || put.Headers["Content-Length"] != "11" {
		t.Fatalf("headers = %v", put.Headers)
	}
	do := func(method, u string, data []byte, headers map[string]string) *http.Response {
		req, _ := http.NewRequestWithContext(ctx, method, u, bytes.NewReader(data))
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		if method == http.MethodPut {
			req.ContentLength = int64(len(data))
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp
	}
	if resp := do(http.MethodPut, put.URL, body, put.Headers); resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT = %d", resp.StatusCode)
	}
	obj, ok, err := local.Head(ctx, "media/a/b")
	if err != nil || !ok || obj.Size != 11 {
		t.Fatalf("head = %+v %v %v", obj, ok, err)
	}
	rc, err := local.Open(ctx, "media/a/b")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(rc)
	_ = rc.Close()
	if string(got) != "hello media" {
		t.Fatalf("conteúdo = %q", got)
	}
	// Too big for the signed size.
	if resp := do(http.MethodPut, put.URL, append(body, '!'), put.Headers); resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("PUT grande = %d", resp.StatusCode)
	}
	// Tampered signature / wrong method / expired.
	if resp := do(http.MethodPut, strings.Replace(put.URL, "sig=", "sig=0", 1), body, nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("assinatura adulterada = %d", resp.StatusCode)
	}
	if resp := do(http.MethodDelete, put.URL, nil, nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("método não assinado = %d", resp.StatusCode)
	}
	local.now = func() time.Time { return time.Now().Add(2 * time.Minute) }
	if resp := do(http.MethodPut, put.URL, body, nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("URL expirada = %d", resp.StatusCode)
	}
	local.now = time.Now
	// GET through a signed URL and Delete.
	getURL, _ := local.signedURL(http.MethodGet, "media/a/b", 0, time.Minute)
	resp, err := http.Get(getURL) //nolint:noctx // test
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("GET = %v %v", resp, err)
	}
	_ = resp.Body.Close()
	if err := local.Delete(ctx, "media/a/b"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := local.Head(ctx, "media/a/b"); ok {
		t.Fatal("objeto deveria ter sumido")
	}
	if _, err := local.Open(ctx, "media/a/b"); err != ErrNotFound { //nolint:errorlint // sentinel
		t.Fatalf("open ausente = %v", err)
	}
	if err := local.Delete(ctx, "media/a/b"); err != nil {
		t.Fatal("delete ausente deveria ser silencioso")
	}
	if _, err := local.PresignPut(ctx, "../x", 1, "", time.Minute); err == nil {
		t.Fatal("chave inválida")
	}
}

func TestLocalPresignedPost(t *testing.T) {
	t.Parallel()
	local, _ := newLocal(t)
	ctx := context.Background()
	post, err := local.PresignPost(ctx, "quarantine/o/u/x", 100, "text/csv", time.Minute)
	if err != nil || post.Fields["key"] != "quarantine/o/u/x" {
		t.Fatalf("post = %+v %v", post, err)
	}
	send := func(field string, data []byte) int {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		for k, v := range post.Fields {
			_ = mw.WriteField(k, v)
		}
		fw, _ := mw.CreateFormFile(field, "a.csv")
		_, _ = fw.Write(data)
		_ = mw.Close()
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, post.URL, &buf)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	if code := send("file", []byte("a;b\n1;2\n")); code != http.StatusNoContent {
		t.Fatalf("POST = %d", code)
	}
	obj, ok, _ := local.Head(ctx, "quarantine/o/u/x")
	if !ok || obj.Size != 8 {
		t.Fatalf("head = %+v", obj)
	}
	if code := send("other", []byte("x")); code != http.StatusBadRequest {
		t.Fatalf("sem campo file = %d", code)
	}
	// Direct Put with a size mismatch is refused.
	if err := local.Put(ctx, "quarantine/o/u/y", strings.NewReader("abc"), 2, ""); err == nil {
		t.Fatal("tamanho diferente deveria falhar")
	}
	if _, err := NewLocal("", "", nil); err == nil {
		t.Fatal("NewLocal sem pasta")
	}
}

func TestS3PresignOffline(t *testing.T) {
	t.Parallel()
	endpoint, _ := url.Parse("http://127.0.0.1:1")
	st, err := NewS3(context.Background(), S3Options{Name: NameR2, Bucket: "media", Region: "auto", Endpoint: endpoint, PathStyle: true, AccessKeyID: "ak", SecretAccessKey: "sk"})
	if err != nil {
		t.Fatal(err)
	}
	if st.Name() != NameR2 || st.Bucket() != "media" || st.URI("k") != "r2://media/k" {
		t.Fatal("metadados")
	}
	put, err := st.PresignPut(context.Background(), "media/x", 10, "audio/mpeg", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(put.URL, "/media/media/x") || !strings.Contains(put.URL, "X-Amz-Signature=") || put.Headers["Content-Length"] != "10" {
		t.Fatalf("put = %+v", put)
	}
	post, err := st.PresignPost(context.Background(), "quarantine/x", 50, "application/pdf", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if post.Fields["key"] != "quarantine/x" || post.Fields["policy"] == "" || post.Fields["Content-Type"] != "application/pdf" {
		t.Fatalf("post = %+v", post)
	}
	if _, err := st.PresignPut(context.Background(), "../x", 1, "", time.Minute); err == nil {
		t.Fatal("chave inválida")
	}
	get, err := st.PresignGet(context.Background(), "media/x", time.Minute)
	if err != nil || !strings.Contains(get, "/media/media/x") || !strings.Contains(get, "X-Amz-Expires=60") {
		t.Fatalf("get = %q %v", get, err)
	}
	if _, err := st.PresignGet(context.Background(), "../x", time.Minute); err == nil {
		t.Fatal("chave inválida no GET")
	}
	if _, err := NewS3(context.Background(), S3Options{}); err == nil {
		t.Fatal("bucket vazio")
	}
	// Network calls against a closed port fail fast and are wrapped.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, _, err := st.Head(ctx, "x"); err == nil {
		t.Fatal("head sem servidor deveria falhar")
	}
}
