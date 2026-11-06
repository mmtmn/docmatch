package billing

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mmtmn/docmatch/services/api/internal/config"
)

func availabilityFixture(t *testing.T, handler http.HandlerFunc) *Service {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	cfg := &config.Config{}
	cfg.Billing.StripeSecretKey = config.Secret("sk_live_fixture")
	cfg.Billing.StripeWebhookSecret = config.Secret("whsec_fixture")
	cfg.Billing.StripePortalConfig = "bpc_fixture"
	cfg.Billing.StripePaymentConfig = "pmc_fixture"
	cfg.Billing.StripePrices = map[string]string{"starter": "price_starter", "pro": "price_pro", "business": "price_business", "topup": "price_topup"}
	s := New(cfg, nil, nil, nil)
	s.stripe.BaseURL = server.URL
	s.stripe.HTTP = server.Client()
	return s
}
func TestLivePaymentApprovalAndFailure(t *testing.T) {
	var active atomic.Bool
	var failed atomic.Bool
	var calls atomic.Int64
	s := availabilityFixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/account" {
			t.Error("unexpected endpoint")
		}
		if failed.Load() {
			http.Error(w, "provider unavailable", http.StatusServiceUnavailable)
			return
		}
		if active.Load() {
			_, _ = w.Write([]byte(`{"charges_enabled":true,"capabilities":{"card_payments":"active"}}`))
		} else {
			_, _ = w.Write([]byte(`{"charges_enabled":false,"capabilities":{"card_payments":"inactive"}}`))
		}
	})
	now := time.Now()
	s.now = func() time.Time { return now }
	if s.paymentsAvailable(t.Context()) {
		t.Fatal("unapproved account enabled")
	}
	active.Store(true)
	if s.paymentsAvailable(t.Context()) || calls.Load() != 1 {
		t.Fatal("readiness must be cached")
	}
	now = now.Add(time.Minute)
	if !s.paymentsAvailable(t.Context()) {
		t.Fatal("approval did not open purchases")
	}
	failed.Store(true)
	now = now.Add(time.Minute)
	if s.paymentsAvailable(t.Context()) {
		t.Fatal("provider failure must close new purchases")
	}
	failed.Store(false)
	now = now.Add(10 * time.Second)
	if !s.paymentsAvailable(t.Context()) {
		t.Fatal("provider recovery not retried")
	}
	s.cfg.KillFlags = []string{"billing"}
	if s.paymentsAvailable(t.Context()) {
		t.Fatal("kill switch bypassed by readiness cache")
	}
}
func TestPaymentReadinessRequiresBothSignals(t *testing.T) {
	for _, body := range []string{`{"charges_enabled":true,"capabilities":{"card_payments":"pending"}}`, `{"charges_enabled":false,"capabilities":{"card_payments":"active"}}`, `{}`, `invalid`} {
		t.Run(body, func(t *testing.T) {
			s := availabilityFixture(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) })
			if s.paymentsAvailable(t.Context()) {
				t.Fatal("unverified account enabled")
			}
		})
	}
}
func TestPaymentReadinessCoalescesConcurrentRequests(t *testing.T) {
	var calls atomic.Int64
	s := availabilityFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{"charges_enabled":true,"capabilities":{"card_payments":"active"}}`))
	})
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			if !s.paymentsAvailable(t.Context()) {
				t.Error("active account disabled")
			}
		})
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatal("duplicate provider readiness calls", calls.Load())
	}
}
func TestTestPaymentsAndMissingConfiguration(t *testing.T) {
	s := availabilityFixture(t, func(http.ResponseWriter, *http.Request) { t.Error("test mode should not query live activation") })
	s.stripe.Key = "sk_test_fixture"
	if !s.paymentsAvailable(t.Context()) {
		t.Fatal("test mode blocked")
	}
	s.cfg.Billing.StripePortalConfig = ""
	if s.paymentsAvailable(t.Context()) {
		t.Fatal("missing configuration accepted")
	}
}
func TestPaymentReadinessHonorsCancellation(t *testing.T) {
	s := availabilityFixture(t, func(http.ResponseWriter, *http.Request) { t.Error("canceled request reached provider") })
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if s.paymentsAvailable(ctx) {
		t.Fatal("canceled readiness request enabled payments")
	}
}
