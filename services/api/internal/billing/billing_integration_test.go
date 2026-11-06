//go:build integration

package billing

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	contracts "github.com/mmtmn/docmatch/packages/contracts/gen"
	"github.com/mmtmn/docmatch/services/api/internal/account"
	"github.com/mmtmn/docmatch/services/api/internal/config"
	"github.com/mmtmn/docmatch/services/api/internal/credits"
	"github.com/mmtmn/docmatch/services/api/internal/db/pgstore"
	"github.com/mmtmn/docmatch/services/api/internal/estimate/pricing"
	"github.com/mmtmn/docmatch/services/api/internal/session"
	"github.com/mmtmn/docmatch/services/api/internal/testdb"
)

func TestMain(m *testing.M) { testdb.Main(m) }

type fixture struct {
	t           *testing.T
	db          *testdb.DB
	s           *Service
	p           session.Principal
	customer    string
	sub         map[string]any
	invoice     map[string]any
	sessions    map[string]map[string]any
	creates     int
	badPrice    bool
	failInvoice bool
}

func setup(t *testing.T) *fixture {
	t.Helper()
	d := testdb.Get(t)
	org, user := uuid.New(), uuid.New()
	ctx := t.Context()
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{"INSERT INTO orgs(id,name,slug) VALUES($1,'Billing fixture',$2)", []any{org, org.String()}},
		{"INSERT INTO users(id,email,email_verified_at) VALUES($1,$2,now())", []any{user, user.String() + "@example.test"}},
		{"INSERT INTO memberships(org_id,user_id,role) VALUES($1,$2,'owner')", []any{org, user}},
	} {
		if _, err := d.Admin.Exec(ctx, q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}
	prices, err := pricing.Load("../../../../config/pricing.yaml", pricing.Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := account.LoadEntitlements("config/pricing.yaml")
	if err != nil {
		t.Fatal(err)
	}
	base, _ := url.Parse("https://docmatch.test")
	cfg := &config.Config{PublicBaseURL: base}
	cfg.Billing.StripeSecretKey = config.Secret("sk_test_fixture")
	cfg.Billing.StripeWebhookSecret = config.Secret("whsec_fixture")
	cfg.Billing.StripePortalConfig = "bpc_fixture"
	cfg.Billing.StripePaymentConfig = "pmc_fixture"
	cfg.Billing.StripePrices = map[string]string{"starter": "price_starter", "pro": "price_pro", "business": "price_business", "topup": "price_topup"}
	cfg.Billing.StripeTestEmails = []string{user.String() + "@example.test"}
	f := &fixture{t: t, db: d, s: New(cfg, prices, catalog, d.App), p: session.Principal{OrgID: org, UserID: user}, customer: "cus_" + org.String(), sessions: map[string]map[string]any{}}
	server := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(server.Close)
	f.s.stripe.BaseURL = server.URL
	f.s.stripe.HTTP = server.Client()
	start, end := time.Now().Add(-time.Hour).Unix(), time.Now().AddDate(0, 1, 0).Unix()
	f.sub = map[string]any{"id": "sub_" + org.String(), "customer": f.customer, "status": "active", "livemode": false, "metadata": map[string]string{"app": "docmatch", "org_id": org.String(), "plan": "starter"}, "items": map[string]any{"data": []any{map[string]any{"price": map[string]any{"id": "price_starter", "currency": "brl", "unit_amount": 10000}, "quantity": 1, "current_period_start": start, "current_period_end": end}}}}
	f.invoice = map[string]any{"id": "in_" + org.String(), "customer": f.customer, "livemode": false, "status": "paid", "currency": "brl", "amount_paid": 10000, "billing_reason": "subscription_create", "parent": map[string]any{"subscription_details": map[string]any{"subscription": f.sub["id"]}}, "lines": map[string]any{"data": []any{map[string]any{"quantity": 1, "amount": 10000, "pricing": map[string]any{"price_details": map[string]any{"price": "price_starter"}}, "period": map[string]any{"start": start, "end": end}}}}}
	return f
}
func (f *fixture) serve(w http.ResponseWriter, r *http.Request) {
	f.t.Helper()
	if err := r.ParseForm(); err != nil {
		f.t.Error(err)
	}
	var out any
	switch {
	case strings.HasPrefix(r.URL.Path, "/prices/"):
		plan := strings.TrimPrefix(r.URL.Path, "/prices/price_")
		amount := cents(f.s.pricing.TokenBRL)
		if plan != "topup" {
			amount = cents(f.s.pricing.Plans[plan].PriceBRL)
		}
		if f.badPrice {
			amount++
		}
		p := map[string]any{"id": "price_" + plan, "currency": "brl", "unit_amount": amount, "active": true, "livemode": false, "metadata": map[string]string{"app": "docmatch", "plan": plan}}
		if plan != "topup" {
			p["recurring"] = map[string]any{"interval": "month", "interval_count": 1}
		}
		out = p
	case strings.HasPrefix(r.URL.Path, "/customers"):
		out = map[string]any{"id": f.customer, "metadata": map[string]string{"app": "docmatch", "org_id": f.p.OrgID.String()}}
	case r.URL.Path == "/checkout/sessions" && r.Method == http.MethodPost:
		f.creates++
		id := "cs_" + uuid.NewString()
		meta := map[string]string{}
		for _, k := range []string{"app", "org_id", "order_id", "plan"} {
			meta[k] = r.Form.Get("metadata[" + k + "]")
		}
		amount := int64(10000)
		if meta["plan"] == "topup" {
			amount = 500
		}
		v := map[string]any{"id": id, "url": "https://checkout.stripe.com/c/pay/" + id, "expires_at": time.Now().Add(time.Hour).Unix(), "livemode": false, "customer": f.customer, "metadata": meta, "mode": r.Form.Get("mode"), "status": "complete", "payment_status": "paid", "amount_total": amount, "currency": "brl", "payment_intent": "pi_fixture", "subscription": f.sub["id"]}
		f.sessions[id] = v
		out = v
	case strings.HasSuffix(r.URL.Path, "/line_items"):
		out = map[string]any{"data": []any{map[string]any{"price": map[string]any{"id": "price_topup"}, "quantity": 100, "amount_total": 500}}}
	case strings.HasPrefix(r.URL.Path, "/checkout/sessions/"):
		out = f.sessions[strings.TrimPrefix(r.URL.Path, "/checkout/sessions/")]
	case strings.HasPrefix(r.URL.Path, "/subscriptions/"):
		out = f.sub
	case strings.HasPrefix(r.URL.Path, "/invoices/"):
		if f.failInvoice {
			http.Error(w, "temporary", http.StatusServiceUnavailable)
			return
		}
		out = f.invoice
	case r.URL.Path == "/billing_portal/sessions":
		if r.Form.Get("configuration") != "bpc_fixture" {
			f.t.Error("wrong portal")
		}
		out = map[string]string{"url": "https://billing.stripe.com/p/session/fixture"}
	default:
		f.t.Errorf("unexpected Stripe call %s %s", r.Method, r.URL.Path)
		http.NotFound(w, r)
		return
	}
	if err := json.NewEncoder(w).Encode(out); err != nil {
		f.t.Error(err)
	}
}
func (f *fixture) event(kind, id string, obj any) error {
	f.t.Helper()
	raw, err := json.Marshal(map[string]any{"id": id, "type": kind, "api_version": stripeVersion, "livemode": false, "data": map[string]any{"object": obj}})
	if err != nil {
		f.t.Fatal(err)
	}
	return f.s.Webhook(f.t.Context(), raw, signature(raw, "whsec_fixture", time.Now()))
}
func (f *fixture) balance() int64 {
	f.t.Helper()
	v, err := credits.New(pgstore.NewCreditStore(f.db.App)).Balance(f.t.Context(), f.p.OrgID)
	if err != nil {
		f.t.Fatal(err)
	}
	return v.Balance
}
func requireCode(t *testing.T, err error, code contracts.ErrorCode) {
	t.Helper()
	var f fault
	if !errors.As(err, &f) || f.code != code {
		t.Fatalf("got %v, want %s", err, code)
	}
}
func TestCheckoutAuthorizationAndIdempotency(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	req := CheckoutRequest{Plan: "starter"}
	_, err := f.s.Checkout(ctx, f.p, req, "", false)
	requireCode(t, err, contracts.ErrMissingIdempotencyKey)
	f.s.cfg.Billing.StripeTestEmails = nil
	_, err = f.s.Checkout(ctx, f.p, req, "key", false)
	requireCode(t, err, contracts.ErrBillingUnavailable)
	f.s.cfg.Billing.StripeTestEmails = []string{f.p.UserID.String() + "@example.test"}
	wrong := f.p
	wrong.UserID = uuid.New()
	_, err = f.s.Checkout(ctx, wrong, req, "key", false)
	requireCode(t, err, contracts.ErrForbidden)
	f.badPrice = true
	_, err = f.s.Checkout(ctx, f.p, req, "key", false)
	requireCode(t, err, contracts.ErrBillingUnavailable)
	f.badPrice = false
	first, err := f.s.Checkout(ctx, f.p, req, "key", false)
	if err != nil {
		t.Fatal(err)
	}
	again, err := f.s.Checkout(ctx, f.p, req, "key", false)
	if err != nil || again.ID != first.ID || f.creates != 1 {
		t.Fatal(again, err, f.creates)
	}
	again, err = f.s.Checkout(ctx, f.p, req, "another", false)
	if err != nil || again.ID != first.ID || f.creates != 1 {
		t.Fatal(again, err)
	}
	_, err = f.s.Checkout(ctx, f.p, CheckoutRequest{Plan: "pro"}, "key", false)
	requireCode(t, err, contracts.ErrIdempotencyKeyReused)
	_, err = f.s.Checkout(ctx, f.p, CheckoutRequest{Credits: 100}, "topup", true)
	requireCode(t, err, contracts.ErrSubscriptionRequired)
	if _, err = f.s.Portal(ctx, f.p); err != nil {
		t.Fatal(err)
	}
	st, err := f.s.Status(ctx, f.p)
	if err != nil || !st.Enabled || !st.TestMode || !st.HasCustomer {
		t.Fatal(st, err)
	}
}
func TestPaidInvoicesTopupsRetriesCancellationAndRefund(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	checkout, err := f.s.Checkout(ctx, f.p, CheckoutRequest{Plan: "starter"}, "first", false)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.event("checkout.session.completed", "evt_checkout", f.sessions[checkout.ID]); err != nil {
		t.Fatal(err)
	}
	if f.balance() != 0 {
		t.Fatal("checkout must not grant subscription credits")
	}
	f.failInvoice = true
	if err = f.event("invoice.paid", "evt_invoice", f.invoice); err == nil {
		t.Fatal("expected retryable error")
	}
	f.failInvoice = false
	for _, id := range []string{"evt_invoice", "evt_invoice", "evt_invoice_duplicate_object"} {
		if err = f.event("invoice.paid", id, f.invoice); err != nil {
			t.Fatal(err)
		}
	}
	if f.balance() != 2000 {
		t.Fatal("duplicate invoice grant", f.balance())
	}
	f.invoice["id"] = "in_renewal_" + f.p.OrgID.String()
	f.invoice["billing_reason"] = "subscription_cycle"
	if err = f.event("invoice.paid", "evt_renewal", f.invoice); err != nil {
		t.Fatal(err)
	}
	if f.balance() != 4000 {
		t.Fatal(f.balance())
	}
	topup, err := f.s.Checkout(ctx, f.p, CheckoutRequest{Credits: 100}, "topup", true)
	if err != nil {
		t.Fatal(err)
	}
	f.sessions[topup.ID]["payment_status"] = "unpaid"
	if err = f.event("checkout.session.completed", "evt_unpaid", f.sessions[topup.ID]); err != nil {
		t.Fatal(err)
	}
	if f.balance() != 4000 {
		t.Fatal("unpaid grant")
	}
	f.sessions[topup.ID]["payment_status"] = "paid"
	f.sessions[topup.ID]["amount_total"] = 101
	if err = f.event("checkout.session.async_payment_succeeded", "evt_paid", f.sessions[topup.ID]); err == nil {
		t.Fatal("amount mismatch accepted")
	}
	f.sessions[topup.ID]["amount_total"] = 500
	for _, id := range []string{"evt_paid", "evt_paid", "evt_paid_other"} {
		if err = f.event("checkout.session.async_payment_succeeded", id, f.sessions[topup.ID]); err != nil {
			t.Fatal(err)
		}
	}
	if f.balance() != 4100 {
		t.Fatal(f.balance())
	}
	f.sub["cancel_at_period_end"] = true
	if err = f.event("customer.subscription.updated", "evt_cancel_scheduled", f.sub); err != nil {
		t.Fatal(err)
	}
	st, err := f.s.Status(ctx, f.p)
	if err != nil || st.Subscription.CancelAt == nil {
		t.Fatal(st, err)
	}
	f.sub["status"] = "canceled"
	if err = f.event("customer.subscription.deleted", "evt_canceled", f.sub); err != nil {
		t.Fatal(err)
	}
	// Delivery order is deliberately stale; current state fetched from Stripe wins.
	old := map[string]any{"id": f.sub["id"], "customer": f.customer, "status": "active"}
	if err = f.event("customer.subscription.updated", "evt_old", old); err != nil {
		t.Fatal(err)
	}
	st, err = f.s.Status(ctx, f.p)
	if err != nil || st.Subscription.Status != "canceled" {
		t.Fatal(st, err)
	}
	ledger := credits.New(pgstore.NewCreditStore(f.db.App))
	hold, err := ledger.Reserve(ctx, f.p.OrgID, credits.ReserveParams{Amount: 50})
	if err != nil {
		t.Fatal(err)
	}
	if err = f.event("charge.refunded", "evt_refund", map[string]any{"id": "ch_fixture", "customer": f.customer}); err != nil {
		t.Fatal(err)
	}
	_, err = ledger.Reserve(ctx, f.p.OrgID, credits.ReserveParams{Amount: 1})
	if !errors.Is(err, credits.ErrInsufficient) {
		t.Fatal("refund failed to freeze spending", err)
	}
	if _, err = ledger.Settle(ctx, f.p.OrgID, hold.ID, 10); err != nil {
		t.Fatal("existing hold must settle", err)
	}
	st, err = f.s.Status(ctx, f.p)
	if err != nil || !st.Blocked {
		t.Fatal(st, err)
	}
	if _, err = f.s.Portal(ctx, f.p); err != nil {
		t.Fatal("frozen account can still cancel", err)
	}
}
func TestWebhookRejectsInvalidSignaturesAndWrongMode(t *testing.T) {
	f := setup(t)
	requireCode(t, f.s.Webhook(t.Context(), []byte(`{}`), "invalid"), contracts.ErrWebhookSignatureInvalid)
	raw := []byte(`{"id":"evt_wrong_mode","type":"invoice.paid","livemode":true}`)
	if err := f.s.Webhook(t.Context(), raw, signature(raw, "whsec_fixture", time.Now())); err != nil {
		t.Fatal(err)
	}
	if f.balance() != 0 {
		t.Fatal("wrong mode credited")
	}
}

func TestUnapprovedLiveAccountBlocksCheckoutButKeepsPortal(t *testing.T) {
	f := setup(t)
	f.s.stripe.Key = "sk_live_fixture"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/account" {
			_, _ = w.Write([]byte(`{"charges_enabled":false,"capabilities":{"card_payments":"inactive"}}`))
			return
		}
		f.serve(w, r)
	}))
	t.Cleanup(server.Close)
	f.s.stripe.BaseURL = server.URL
	f.s.stripe.HTTP = server.Client()
	status, err := f.s.Status(t.Context(), f.p)
	if err != nil || status.Enabled || status.TestMode || !status.CanManage {
		t.Fatal(status, err)
	}
	_, err = f.s.Checkout(t.Context(), f.p, CheckoutRequest{Plan: "starter"}, "unapproved", false)
	requireCode(t, err, contracts.ErrBillingUnavailable)
	if f.creates != 0 {
		t.Fatal("unapproved account created a checkout")
	}
	_, err = f.db.Admin.Exec(t.Context(), "INSERT INTO billing_accounts(org_id,livemode,stripe_customer_id) VALUES($1,true,$2)", f.p.OrgID, f.customer)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Portal(t.Context(), f.p); err != nil {
		t.Fatal("account review must not prevent cancellation", err)
	}
	recorder := httptest.NewRecorder()
	f.s.getPlans(recorder, httptest.NewRequest(http.MethodGet, "/v1/plans", nil))
	var catalog struct {
		Plans []struct {
			Available bool `json:"available"`
		} `json:"plans"`
	}
	if err = json.Unmarshal(recorder.Body.Bytes(), &catalog); err != nil {
		t.Fatal(err)
	}
	if len(catalog.Plans) != 3 {
		t.Fatal("missing plans")
	}
	for _, plan := range catalog.Plans {
		if plan.Available {
			t.Fatal("unapproved plan advertised as purchasable")
		}
	}
}
