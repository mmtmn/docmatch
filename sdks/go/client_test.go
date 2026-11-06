package docmatch

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRetriesKeepIdempotencyAndDoNotDuplicateUnsafeWrites(t *testing.T) {
	attempts := 0
	id := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if r.URL.Path == "/v1/uploads" {
			w.WriteHeader(503)
			return
		}
		if id == "" {
			id = r.Header.Get("Idempotency-Key")
		} else if id != r.Header.Get("Idempotency-Key") {
			t.Error("idempotency changed")
		}
		if attempts == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(429)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"run_fixture","status":"queued"}`))
	}))
	defer srv.Close()
	c, _ := New(srv.URL, "fixture")
	_, err := c.Run(context.Background(), Quote{ID: "quote", Signature: "sig"}, c.Input("upl_fixture"))
	if err != nil || attempts != 2 || id == "" {
		t.Fatal(err, attempts)
	}
	attempts = 0
	if err := c.Do(context.Background(), "POST", "/v1/uploads", map[string]any{}, nil); err == nil || attempts != 1 {
		t.Fatal("unsafe write retried")
	}
}
func TestUploadNeverSendsCredentials(t *testing.T) {
	var apiURL string
	storage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Error("credentials leaked")
		}
		if r.Method != "POST" {
			t.Error("wrong method")
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Error(err)
		}
		w.WriteHeader(204)
	}))
	defer storage.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/uploads" {
			_, _ = w.Write([]byte(`{"id":"upl_fixture","method":"POST","url":"` + storage.URL + `","fields":{"policy":"fixture"}}`))
		} else {
			_, _ = w.Write([]byte(`{"id":"upl_fixture","status":"ready"}`))
		}
	}))
	defer srv.Close()
	apiURL = srv.URL
	c, _ := New(apiURL, "fixture-secret")
	if _, err := c.Upload(context.Background(), "a.csv", "text/csv", []byte("a,b\n1,2\n")); err != nil {
		t.Fatal(err)
	}
}
func TestWebhookAndCancellation(t *testing.T) {
	now := time.Unix(1234567890, 0)
	body := []byte(`{"id":"event"}`)
	h := hmac.New(sha256.New, []byte("secret"))
	h.Write([]byte("1234567890."))
	h.Write(body)
	header := "t=1234567890,v1=" + hex.EncodeToString(h.Sum(nil))
	if !VerifyWebhook(body, header, "secret", now) || VerifyWebhook([]byte("tampered"), header, "secret", now) || VerifyWebhook(body, header, "secret", now.Add(301*time.Second)) {
		t.Fatal("signature verification")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if sleep(ctx, time.Hour) == nil {
		t.Fatal("ignored cancellation")
	}
}
