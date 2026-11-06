package billing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	contracts "github.com/mmtmn/docmatch/packages/contracts/gen"
	"github.com/mmtmn/docmatch/services/api/internal/credits"
	"github.com/mmtmn/docmatch/services/api/internal/db"
	"github.com/mmtmn/docmatch/services/api/internal/db/pgstore"
)

// Webhook verifies raw bytes before reading any tenant identifiers or calling Stripe.
// Stripe retries failed transactions; successful replays acknowledge without granting again.
func (s *Service) Webhook(ctx context.Context, raw []byte, signature string) error {
	if !s.cfg.Billing.StripeWebhookSecret.IsSet() {
		return fail(contracts.ErrBillingUnavailable)
	}
	if !verifySignature(raw, signature, s.cfg.Billing.StripeWebhookSecret.Reveal(), s.now()) {
		return fail(contracts.ErrWebhookSignatureInvalid)
	}
	var e event
	if err := json.Unmarshal(raw, &e); err != nil || e.ID == "" {
		return fail(contracts.ErrValidationFailed)
	}
	if e.Livemode != s.live() {
		return nil
	}
	switch e.Type {
	case "checkout.session.completed", "checkout.session.async_payment_succeeded", "checkout.session.async_payment_failed",
		"invoice.paid", "invoice.payment_failed", "customer.subscription.created", "customer.subscription.updated", "customer.subscription.deleted",
		"charge.refunded", "charge.dispute.created":
	default:
		return nil
	}
	var obj struct {
		ID       string `json:"id"`
		Customer ID     `json:"customer"`
		Charge   ID     `json:"charge"`
	}
	if err := json.Unmarshal(e.Data.Object, &obj); err != nil || obj.ID == "" {
		return fail(contracts.ErrValidationFailed)
	}
	customerID := string(obj.Customer)
	if e.Type == "charge.dispute.created" {
		var charge struct {
			Customer ID `json:"customer"`
		}
		if obj.Charge == "" {
			return errors.New("billing: dispute without charge")
		}
		if err := s.stripe.call(ctx, http.MethodGet, "/charges/"+url.PathEscape(string(obj.Charge)), nil, "", &charge); err != nil {
			return err
		}
		customerID = string(charge.Customer)
	}
	if customerID == "" {
		return nil
	}
	var c customer
	if err := s.stripe.call(ctx, http.MethodGet, "/customers/"+url.PathEscape(customerID), nil, "", &c); err != nil {
		return err
	}
	if c.Deleted || c.Metadata["app"] != "docmatch" {
		return nil
	} // Shared Stripe account: leave other apps alone.
	org, err := uuid.Parse(c.Metadata["org_id"])
	if err != nil {
		return errors.New("billing: invalid customer tenant")
	}
	return db.WithOrgTx(ctx, s.pool, org, func(ctx context.Context, tx pgx.Tx) error {
		if err := billingLock(ctx, tx, org); err != nil {
			return err
		}
		var blocked bool
		if err := tx.QueryRow(ctx, "SELECT blocked FROM billing_accounts WHERE org_id=$1 AND livemode=$2 AND stripe_customer_id=$3", org, s.live(), customerID).Scan(&blocked); err != nil {
			return fmt.Errorf("billing: customer mapping: %w", err)
		}
		tag, err := tx.Exec(ctx, "INSERT INTO stripe_events(id,type,api_version,payload) VALUES($1,$2,$3,$4) ON CONFLICT(id) DO NOTHING", e.ID, e.Type, e.APIVersion, raw)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return nil
		}
		switch {
		case strings.HasPrefix(e.Type, "checkout.session."):
			err = s.checkoutPaid(ctx, tx, org, customerID, obj.ID)
		case strings.HasPrefix(e.Type, "invoice."):
			err = s.invoicePaid(ctx, tx, org, customerID, obj.ID, e.Type == "invoice.paid")
		case strings.HasPrefix(e.Type, "customer.subscription."):
			_, err = s.syncSubscription(ctx, tx, org, customerID, obj.ID)
		case e.Type == "charge.refunded" || e.Type == "charge.dispute.created":
			// Freeze new spending for manual reconciliation. Never destroy grants backing open holds.
			_, err = tx.Exec(ctx, "UPDATE billing_accounts SET blocked=true WHERE org_id=$1 AND livemode=$2", org, s.live())
			if err == nil {
				_, err = tx.Exec(ctx, `INSERT INTO credit_accounts(org_id,billing_blocked) VALUES($1,true)
    ON CONFLICT(org_id) DO UPDATE SET billing_blocked=true`, org)
			}
		}
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, "UPDATE stripe_events SET processed_at=now(),error=NULL WHERE id=$1", e.ID)
		return err
	})
}

func (s *Service) subscriptionPlan(sub subscription, org uuid.UUID, customerID string) (string, int64, int64, error) {
	if string(sub.Customer) != customerID || sub.Livemode != s.live() || sub.Metadata["app"] != "docmatch" || sub.Metadata["org_id"] != org.String() || len(sub.Items.Data) != 1 {
		return "", 0, 0, errors.New("billing: subscription ownership mismatch")
	}
	item := sub.Items.Data[0]
	plan := sub.Metadata["plan"]
	p, ok := s.pricing.Plans[plan]
	if !ok || item.Quantity != 1 || item.Price.ID != s.cfg.Billing.StripePrices[plan] || item.Price.Currency != "brl" || item.Price.UnitAmount != cents(p.PriceBRL) {
		return "", 0, 0, errors.New("billing: subscription price mismatch")
	}
	start, end := item.CurrentPeriodStart, item.CurrentPeriodEnd
	if end == 0 {
		start, end = sub.CurrentPeriodStart, sub.CurrentPeriodEnd
	}
	if start <= 0 || end <= start {
		return "", 0, 0, errors.New("billing: invalid subscription period")
	}
	switch sub.Status {
	case "trialing", "active", "past_due", "canceled", "unpaid", "incomplete", "incomplete_expired", "paused":
	default:
		return "", 0, 0, errors.New("billing: unknown subscription status")
	}
	return plan, start, end, nil
}
func unixTime(n int64) *time.Time {
	if n <= 0 {
		return nil
	}
	v := time.Unix(n, 0).UTC()
	return &v
}

func (s *Service) syncSubscription(ctx context.Context, tx pgx.Tx, org uuid.UUID, customerID, id string) (subscription, error) {
	var sub subscription
	if err := s.stripe.call(ctx, http.MethodGet, "/subscriptions/"+url.PathEscape(id), nil, "", &sub); err != nil {
		return sub, err
	}
	if sub.ID != id {
		return sub, errors.New("billing: subscription ID mismatch")
	}
	plan, start, end, err := s.subscriptionPlan(sub, org, customerID)
	if err != nil {
		return sub, err
	}
	cancel := sub.CancelAt
	if sub.CancelAtPeriodEnd && cancel == 0 {
		cancel = end
	}
	_, err = tx.Exec(ctx, `INSERT INTO subscriptions(org_id,livemode,plan_code,stripe_customer_id,stripe_subscription_id,status,current_period_start,current_period_end,cancel_at,canceled_at)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT(stripe_subscription_id) DO UPDATE SET
 plan_code=EXCLUDED.plan_code,status=EXCLUDED.status,current_period_start=EXCLUDED.current_period_start,current_period_end=EXCLUDED.current_period_end,
 cancel_at=EXCLUDED.cancel_at,canceled_at=EXCLUDED.canceled_at`, org, s.live(), plan, customerID, id, sub.Status, unixTime(start), unixTime(end), unixTime(cancel), unixTime(sub.CanceledAt))
	if err != nil {
		return sub, err
	}
	// Recompute from all current subscriptions, so a late cancellation of an old subscription cannot downgrade a newer one.
	_, err = tx.Exec(ctx, `UPDATE orgs SET plan_code=COALESCE((SELECT plan_code FROM subscriptions WHERE org_id=$1 AND livemode=$2
 AND status='active' AND current_period_end>now() ORDER BY current_period_end DESC LIMIT 1),'starter') WHERE id=$1`, org, s.live())
	return sub, err
}

func (s *Service) checkoutPaid(ctx context.Context, tx pgx.Tx, org uuid.UUID, customerID, id string) error {
	var v checkoutSession
	if err := s.stripe.call(ctx, http.MethodGet, "/checkout/sessions/"+url.PathEscape(id), nil, "", &v); err != nil {
		return err
	}
	if v.ID != id || string(v.Customer) != customerID || v.Livemode != s.live() || v.Metadata["app"] != "docmatch" || v.Metadata["org_id"] != org.String() {
		return errors.New("billing: checkout ownership mismatch")
	}
	var o order
	err := tx.QueryRow(ctx, `SELECT id,kind,plan_code,credits,amount_cents,price_id,paid_at,currency FROM billing_orders
 WHERE stripe_session_id=$1 AND org_id=$2 AND livemode=$3`, id, org, s.live()).Scan(&o.ID, &o.Kind, &o.Plan, &o.Credits, &o.Amount, &o.Price, &o.Paid, &o.Currency)
	if err != nil {
		return err
	}
	if v.Metadata["order_id"] != o.ID.String() || v.Metadata["plan"] != o.Plan {
		return errors.New("billing: checkout order mismatch")
	}
	if v.Status != "complete" || v.PaymentStatus != "paid" {
		return nil
	}
	if v.Currency != o.Currency || v.AmountTotal != o.Amount {
		return errors.New("billing: checkout amount mismatch")
	}
	if o.Kind == "subscription" {
		if v.Mode != "subscription" || v.Subscription == "" {
			return errors.New("billing: checkout subscription missing")
		}
		if _, err = s.syncSubscription(ctx, tx, org, customerID, string(v.Subscription)); err != nil {
			return err
		}
	} else {
		if v.Mode != "payment" || v.PaymentIntent == "" {
			return errors.New("billing: checkout payment missing")
		}
		var lines struct {
			HasMore bool `json:"has_more"`
			Data    []struct {
				Price       price `json:"price"`
				Quantity    int64 `json:"quantity"`
				AmountTotal int64 `json:"amount_total"`
			} `json:"data"`
		}
		if err = s.stripe.call(ctx, http.MethodGet, "/checkout/sessions/"+url.PathEscape(id)+"/line_items", nil, "", &lines); err != nil {
			return err
		}
		if lines.HasMore || len(lines.Data) != 1 || lines.Data[0].Price.ID != o.Price || lines.Data[0].Quantity != o.Credits || lines.Data[0].AmountTotal != o.Amount {
			return errors.New("billing: checkout line mismatch")
		}
		// Keep confirmed payments in the ledger even while frozen; they cannot be spent until reconciliation.
		expiry := s.now().UTC().AddDate(0, s.pricing.Rollover.TopupTokensMonths, 0)
		_, err = credits.New(pgstore.CreditStoreInTx(tx, org)).Grant(ctx, org, credits.GrantParams{Kind: credits.GrantTopup, Amount: o.Credits, Source: "stripe:" + id, IdempotencyKey: "stripe:checkout:" + id, ExpiresAt: &expiry})
		if err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, "UPDATE billing_orders SET paid_at=COALESCE(paid_at,now()) WHERE id=$1", o.ID)
	return err
}

func (s *Service) invoicePaid(ctx context.Context, tx pgx.Tx, org uuid.UUID, customerID, id string, paidEvent bool) error {
	var v invoice
	if err := s.stripe.call(ctx, http.MethodGet, "/invoices/"+url.PathEscape(id), nil, "", &v); err != nil {
		return err
	}
	if v.ID != id || string(v.Customer) != customerID || v.Livemode != s.live() {
		return errors.New("billing: invoice ownership mismatch")
	}
	subID := v.subscriptionID()
	if subID == "" {
		return nil
	}
	sub, err := s.syncSubscription(ctx, tx, org, customerID, subID)
	if err != nil {
		return err
	}
	if !paidEvent || v.Status != "paid" || (v.BillingReason != "subscription_create" && v.BillingReason != "subscription_cycle") {
		return nil
	}
	plan, _, _, err := s.subscriptionPlan(sub, org, customerID)
	if err != nil {
		return err
	}
	p := s.pricing.Plans[plan]
	if v.Currency != "brl" || v.AmountPaid != cents(p.PriceBRL) || v.Lines.HasMore || len(v.Lines.Data) != 1 {
		return errors.New("billing: invoice amount mismatch")
	}
	line := v.Lines.Data[0]
	priceID := line.Price.ID
	if line.Pricing != nil && line.Pricing.PriceDetails != nil {
		priceID = string(line.Pricing.PriceDetails.Price)
	}
	if priceID != s.cfg.Billing.StripePrices[plan] || line.Quantity != 1 || line.Amount != cents(p.PriceBRL) || line.Proration || line.Period.Start <= 0 || line.Period.End <= line.Period.Start {
		return errors.New("billing: invoice line mismatch")
	}
	expiry := time.Unix(line.Period.End, 0).UTC().AddDate(0, s.pricing.Rollover.PlanTokensCycles, 0)
	_, err = credits.New(pgstore.CreditStoreInTx(tx, org)).Grant(ctx, org, credits.GrantParams{Kind: credits.GrantPlan, Amount: p.Tokens, Source: "stripe:" + id, IdempotencyKey: "stripe:invoice:" + id, ExpiresAt: &expiry})
	return err
}
