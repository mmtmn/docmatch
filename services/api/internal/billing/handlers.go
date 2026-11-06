package billing

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	contracts "github.com/mmtmn/docmatch/packages/contracts/gen"
	"github.com/mmtmn/docmatch/services/api/internal/apierr"
	"github.com/mmtmn/docmatch/services/api/internal/db"
	"github.com/mmtmn/docmatch/services/api/internal/httpx"
	"github.com/mmtmn/docmatch/services/api/internal/session"
)

// Mount adds authenticated purchase routes and a signature-authenticated webhook.
func (s *Service) Mount(r *httpx.Router) {
	r.HandleFunc(http.MethodGet, "/v1/plans", httpx.BodyNone, s.getPlans)
	r.Handle(http.MethodGet, "/v1/billing/status", httpx.BodyNone, session.RequireFunc(s.getStatus))
	r.Handle(http.MethodPost, "/v1/billing/checkout", httpx.BodyJSON, session.RequireFunc(s.postCheckout))
	r.Handle(http.MethodPost, "/v1/billing/topup", httpx.BodyJSON, session.RequireFunc(s.postTopup))
	r.Handle(http.MethodPost, "/v1/billing/portal", httpx.BodyNone, session.RequireFunc(s.postPortal))
	r.HandleFunc(http.MethodPost, "/v1/webhooks/stripe", httpx.BodyJSON, s.postWebhook)
}
func principal(r *http.Request) session.Principal { p, _ := session.FromContext(r.Context()); return p }
func writeErr(w http.ResponseWriter, r *http.Request, err error) {
	var f fault
	if errors.As(err, &f) {
		apierr.Write(w, r, f.code)
		return
	}
	var provider *stripeError
	if errors.As(err, &provider) {
		apierr.Write(w, r, contracts.ErrCheckoutFailed)
		return
	}
	apierr.Internal(w, r, err)
}
func (s *Service) postCheckout(w http.ResponseWriter, r *http.Request) {
	var v struct {
		Plan        string `json:"plan"`
		CouponCode  string `json:"coupon_code,omitempty"`
		SuccessPath string `json:"success_path,omitempty"`
		CancelPath  string `json:"cancel_path,omitempty"`
	}
	if err := httpx.DecodeJSON(r, &v); err != nil {
		apierr.FromRequestError(w, r, err)
		return
	}
	out, err := s.Checkout(r.Context(), principal(r), CheckoutRequest{Plan: v.Plan, CouponCode: v.CouponCode, SuccessPath: v.SuccessPath, CancelPath: v.CancelPath}, r.Header.Get("Idempotency-Key"), false)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}
func (s *Service) postTopup(w http.ResponseWriter, r *http.Request) {
	var v struct {
		Credits     int64  `json:"credits"`
		SuccessPath string `json:"success_path,omitempty"`
		CancelPath  string `json:"cancel_path,omitempty"`
	}
	if err := httpx.DecodeJSON(r, &v); err != nil {
		apierr.FromRequestError(w, r, err)
		return
	}
	out, err := s.Checkout(r.Context(), principal(r), CheckoutRequest{Credits: v.Credits, SuccessPath: v.SuccessPath, CancelPath: v.CancelPath}, r.Header.Get("Idempotency-Key"), true)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}
func (s *Service) postPortal(w http.ResponseWriter, r *http.Request) {
	out, err := s.Portal(r.Context(), principal(r))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"portal_url": out})
}
func (s *Service) postWebhook(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		apierr.Write(w, r, contracts.ErrValidationFailed)
		return
	}
	if err = s.Webhook(r.Context(), raw, r.Header.Get("Stripe-Signature")); err != nil {
		writeErr(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]bool{"received": true})
}

type subscriptionStatus struct {
	Plan      string     `json:"plan"`
	Status    string     `json:"status"`
	PeriodEnd *time.Time `json:"current_period_end"`
	CancelAt  *time.Time `json:"cancel_at"`
}
type Status struct {
	Enabled      bool                `json:"enabled"`
	TestMode     bool                `json:"test_mode"`
	CanManage    bool                `json:"can_manage"`
	HasCustomer  bool                `json:"has_customer"`
	Blocked      bool                `json:"blocked"`
	MinimumTopup int64               `json:"minimum_topup_credits"`
	MaximumTopup int64               `json:"maximum_topup_credits"`
	TokenBRL     float64             `json:"token_brl"`
	Subscription *subscriptionStatus `json:"subscription"`
}

// Status exposes availability without leaking the test allowlist or provider credentials.
func (s *Service) Status(ctx context.Context, p session.Principal) (Status, error) {
	out := Status{TestMode: !s.live(), MinimumTopup: s.minimumTopup(), MaximumTopup: maximumTopupCredits, TokenBRL: s.pricing.TokenBRL}
	err := db.WithOrgTx(ctx, s.pool, p.OrgID, func(ctx context.Context, tx pgx.Tx) error {
		email, err := s.actor(ctx, tx, p)
		if err != nil {
			var f fault
			if errors.As(err, &f) && f.code == contracts.ErrForbidden {
				return nil
			}
			return err
		}
		out.CanManage = s.ready() && s.testAllowed(email)
		out.Enabled = out.CanManage && s.paymentsAvailable(ctx)
		err = tx.QueryRow(ctx, "SELECT true,blocked FROM billing_accounts WHERE org_id=$1 AND livemode=$2", p.OrgID, s.live()).Scan(&out.HasCustomer, &out.Blocked)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		var sub subscriptionStatus
		err = tx.QueryRow(ctx, `SELECT plan_code,status,current_period_end,cancel_at FROM subscriptions WHERE org_id=$1 AND livemode=$2
   ORDER BY CASE WHEN status IN ('active','trialing','past_due','incomplete','paused','unpaid') THEN 0 ELSE 1 END,created_at DESC LIMIT 1`, p.OrgID, s.live()).Scan(&sub.Plan, &sub.Status, &sub.PeriodEnd, &sub.CancelAt)
		if err == nil {
			out.Subscription = &sub
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		return nil
	})
	return out, err
}
func (s *Service) getStatus(w http.ResponseWriter, r *http.Request) {
	v, err := s.Status(r.Context(), principal(r))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, v)
}
func (s *Service) getPlans(w http.ResponseWriter, r *http.Request) {
	country := strings.ToUpper(r.URL.Query().Get("country"))
	if country == "" {
		country = "BR"
	}
	if len(country) != 2 || country[0] < 'A' || country[0] > 'Z' || country[1] < 'A' || country[1] > 'Z' {
		apierr.Write(w, r, contracts.ErrValidationFailed)
		return
	}
	available := s.paymentsAvailable(r.Context())
	plans := make([]map[string]any, 0, 3)
	for _, code := range []string{"starter", "pro", "business"} {
		p := s.pricing.Plans[code]
		ent, _ := s.catalog.For(code)
		plans = append(plans, map[string]any{"code": code, "name": map[string]string{"starter": "Essencial", "pro": "Plus", "business": "Max"}[code], "price": map[string]any{"amount": cents(p.PriceBRL), "currency": "BRL"}, "topup_price_per_credit": map[string]any{"amount": cents(s.pricing.TokenBRL), "currency": "BRL"}, "credits": p.Tokens, "interval": "month", "entitlements": ent, "available": available})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"country": country, "plans": plans})
}
