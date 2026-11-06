//go:build integration

package billing

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/mmtmn/docmatch/services/api/internal/config"
)

// TestStripeSandbox is opt-in. It creates and cleans up a dedicated test customer,
// a hosted checkout, and a paid subscription using Stripe's token fixture (no card data).
func TestStripeSandbox(t *testing.T) {
	if os.Getenv("DM_STRIPE_SANDBOX_TEST") != "1" {
		t.Skip("set DM_STRIPE_SANDBOX_TEST=1 with test-only Stripe configuration")
	}
	key := os.Getenv("STRIPE_SECRET_KEY")
	if len(key) < 8 || key[:8] != "sk_test_" {
		t.Fatal("sandbox test requires a Stripe test key")
	}
	f := setup(t)
	f.s.stripe = &Stripe{Key: key}
	f.s.cfg.Billing.StripeSecretKey = config.Secret(key)
	for _, plan := range []string{"starter", "pro", "business", "topup"} {
		env := map[string]string{"starter": "DM_STRIPE_PRICE_STARTER", "pro": "DM_STRIPE_PRICE_PRO", "business": "DM_STRIPE_PRICE_BUSINESS", "topup": "DM_STRIPE_PRICE_TOPUP"}[plan]
		f.s.cfg.Billing.StripePrices[plan] = os.Getenv(env)
	}
	f.s.cfg.Billing.StripePortalConfig = os.Getenv("DM_STRIPE_PORTAL_CONFIGURATION_ID")
	f.s.cfg.Billing.StripePaymentConfig = os.Getenv("DM_STRIPE_PAYMENT_METHOD_CONFIGURATION_ID")
	ctx := t.Context()
	checkout, err := f.s.Checkout(ctx, f.p, CheckoutRequest{Plan: "starter"}, "sandbox-checkout", false)
	if err != nil {
		t.Fatal(err)
	}
	var remote checkoutSession
	if err = f.s.stripe.call(ctx, http.MethodGet, "/checkout/sessions/"+checkout.ID, nil, "", &remote); err != nil {
		t.Fatal(err)
	}
	customerID := string(remote.Customer)
	t.Cleanup(func() {
		var deleted any
		if err := f.s.stripe.call(context.Background(), http.MethodDelete, "/customers/"+customerID, nil, "", &deleted); err != nil {
			t.Error("sandbox customer cleanup", err)
		}
	})
	if _, err = f.s.Portal(ctx, f.p); err != nil {
		t.Fatal(err)
	}
	var ignored any
	if err = f.s.stripe.call(ctx, http.MethodPost, "/checkout/sessions/"+checkout.ID+"/expire", url.Values{}, "", &ignored); err != nil {
		t.Fatal(err)
	}
	var pm struct {
		ID string `json:"id"`
	}
	if err = f.s.stripe.call(ctx, http.MethodPost, "/payment_methods", url.Values{"type": {"card"}, "card[token]": {"tok_visa"}}, "", &pm); err != nil {
		t.Fatal(err)
	}
	if err = f.s.stripe.call(ctx, http.MethodPost, "/payment_methods/"+pm.ID+"/attach", url.Values{"customer": {customerID}}, "", &ignored); err != nil {
		t.Fatal(err)
	}

	var created struct {
		ID            string `json:"id"`
		LatestInvoice ID     `json:"latest_invoice"`
	}
	params := url.Values{"customer": {customerID}, "default_payment_method": {pm.ID}, "items[0][price]": {f.s.cfg.Billing.StripePrices["starter"]}, "metadata[app]": {"docmatch"}, "metadata[org_id]": {f.p.OrgID.String()}, "metadata[plan]": {"starter"}, "payment_behavior": {"error_if_incomplete"}}
	if err = f.s.stripe.call(ctx, http.MethodPost, "/subscriptions", params, "sandbox-sub-"+f.p.OrgID.String(), &created); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		var deleted any
		if err := f.s.stripe.call(context.Background(), http.MethodDelete, "/subscriptions/"+created.ID, nil, "", &deleted); err != nil {
			t.Error("sandbox subscription cleanup", err)
		}
	})
	var inv invoice
	if err = f.s.stripe.call(ctx, http.MethodGet, "/invoices/"+string(created.LatestInvoice), nil, "", &inv); err != nil {
		t.Fatal(err)
	}
	if inv.Status != "paid" {
		t.Fatal("sandbox invoice not paid", inv.Status)
	}
	for _, eventID := range []string{"evt_sandbox_paid_" + f.p.OrgID.String(), "evt_sandbox_paid_" + f.p.OrgID.String(), "evt_sandbox_retry_" + f.p.OrgID.String()} {
		if err = f.event("invoice.paid", eventID, inv); err != nil {
			t.Fatal(err)
		}
	}
	if f.balance() != 2000 {
		t.Fatal("incorrect real Stripe grant", f.balance())
	}
	var sub subscription
	if err = f.s.stripe.call(ctx, http.MethodPost, "/subscriptions/"+created.ID, url.Values{"cancel_at_period_end": {"true"}}, "", &sub); err != nil {
		t.Fatal(err)
	}
	if err = f.event("customer.subscription.updated", "evt_sandbox_cancel_"+f.p.OrgID.String(), sub); err != nil {
		t.Fatal(err)
	}
	status, err := f.s.Status(ctx, f.p)
	if err != nil || status.Subscription.CancelAt == nil || !status.Subscription.CancelAt.After(time.Now()) {
		t.Fatal("cancellation not synced", err)
	}
	t.Log("Stripe test Checkout, portal, paid invoice, replay protection, and scheduled cancellation verified")
}
