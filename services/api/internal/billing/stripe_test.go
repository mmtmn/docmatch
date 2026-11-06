package billing

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mmtmn/docmatch/services/api/internal/config"
)

func signature(raw []byte, secret string, at time.Time) string {
	stamp := strconv.FormatInt(at.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(stamp + "."))
	_, _ = mac.Write(raw)
	return "t=" + stamp + ",v1=" + hex.EncodeToString(mac.Sum(nil))
}
func TestSignature(t *testing.T) {
	now := time.Now()
	raw := []byte(`{"type":"invoice.paid"}`)
	secret := "whsec_fixture"
	for _, tc := range []struct {
		name, header, secrets string
		raw                   []byte
		ok                    bool
	}{
		{"valid", signature(raw, secret, now), secret, raw, true},
		{"rotation", signature(raw, secret, now), "old," + secret, raw, true},
		{"multiple", signature(raw, secret, now) + ",v1=" + strings.Repeat("0", 64), secret, raw, true},
		{"tampered", signature(raw, secret, now), secret, []byte(`{}`), false},
		{"old", signature(raw, secret, now.Add(-301*time.Second)), secret, raw, false},
		{"future", signature(raw, secret, now.Add(301*time.Second)), secret, raw, false},
		{"wrong", signature(raw, secret, now), "wrong", raw, false},
		{"empty", "", "", raw, false},
		{"duplicate timestamp", signature(raw, secret, now) + ",t=1", secret, raw, false},
		{"malformed", "t=no,v1=xyz", secret, raw, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := verifySignature(tc.raw, tc.header, tc.secrets, now); got != tc.ok {
				t.Fatalf("got %v want %v", got, tc.ok)
			}
		})
	}
}
func TestURLsAndIDs(t *testing.T) {
	for _, path := range []string{"https://bad.test", "//bad.test", "/\\bad.test", "/\nfoo", "no-slash", strings.Repeat("/", 1025)} {
		if _, err := returnURL("https://docmatch.test", path, "/"); err == nil {
			t.Errorf("accepted %q", path)
		}
	}
	if got, err := returnURL("https://docmatch.test/", "/conta?a=b", "/"); err != nil || got != "https://docmatch.test/conta?a=b" {
		t.Fatal(got, err)
	}
	if got, err := returnURL("https://docmatch.test", "", "/conta"); err != nil || got != "https://docmatch.test/conta" {
		t.Fatal(got, err)
	}
	for _, raw := range []string{"http://checkout.stripe.com/a", "https://checkout.stripe.com.evil.test/a", "https://user@checkout.stripe.com/a"} {
		if safeStripeURL(raw, "checkout.stripe.com") {
			t.Fatal(raw)
		}
	}
	for _, raw := range []string{`"cus_1"`, `{"id":"cus_1"}`, `null`} {
		var id ID
		if err := json.Unmarshal([]byte(raw), &id); err != nil {
			t.Fatal(err)
		}
	}
}
func TestStripeTransport(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Bearer fixture" || r.Header.Get("Stripe-Version") != stripeVersion {
			t.Error("missing headers")
		}
		if r.URL.Path == "/error" {
			http.Error(w, "sensitive body", http.StatusBadRequest)
			return
		}
		if r.URL.Path == "/invalid" {
			_, _ = w.Write([]byte("bad json"))
			return
		}
		if r.Method == http.MethodPost {
			if err := r.ParseForm(); err != nil {
				t.Error(err)
			}
			if r.Form.Get("name") != "docmatch" || r.Header.Get("Idempotency-Key") != "request" {
				t.Error("missing form/key")
			}
		}
		_, _ = w.Write([]byte(`{"id":"ok"}`))
	}))
	defer server.Close()
	stripe := Stripe{Key: "fixture", BaseURL: server.URL, HTTP: server.Client()}
	var out customer
	if err := stripe.call(t.Context(), http.MethodPost, "/customers", url.Values{"name": {"docmatch"}}, "request", &out); err != nil || out.ID != "ok" {
		t.Fatal(out, err)
	}
	for _, path := range []string{"/error", "/invalid"} {
		if err := stripe.call(t.Context(), http.MethodGet, path, nil, "", &out); err == nil || strings.Contains(err.Error(), "sensitive") {
			t.Fatal(err)
		}
	}
	if calls != 3 {
		t.Fatal(calls)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := stripe.call(ctx, http.MethodGet, "/customers", nil, "", &out); err == nil {
		t.Fatal("cancellation ignored")
	}
}
func TestTestAllowlist(t *testing.T) {
	cfg := &config.Config{}
	cfg.Billing.StripeSecretKey = config.Secret("sk_test_fixture")
	cfg.Billing.StripeTestEmails = []string{"Owner@example.test"}
	s := New(cfg, nil, nil, nil)
	if s.testAllowed("visitor@example.test") || !s.testAllowed("owner@example.test") {
		t.Fatal("allowlist")
	}
	s.stripe.Key = "sk_live_fixture"
	if !s.testAllowed("visitor@example.test") {
		t.Fatal("live gate")
	}
}
