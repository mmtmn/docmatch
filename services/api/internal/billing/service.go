package billing

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	contracts "github.com/mmtmn/docmatch/packages/contracts/gen"
	"github.com/mmtmn/docmatch/services/api/internal/account"
	"github.com/mmtmn/docmatch/services/api/internal/config"
	"github.com/mmtmn/docmatch/services/api/internal/db"
	"github.com/mmtmn/docmatch/services/api/internal/estimate/pricing"
	"github.com/mmtmn/docmatch/services/api/internal/ratelimit"
	"github.com/mmtmn/docmatch/services/api/internal/session"
)

const minimumTopupCents int64 = 500 // R$5 minimum purchase; provider settlement minimum still applies.
const maximumTopupCredits int64 = 1_000_000

// Service keeps payment mode, tenant authorization and catalog validation on the server.
type Service struct {
	cfg          *config.Config
	pricing      *pricing.Pricing
	catalog      *account.Catalog
	pool         *pgxpool.Pool
	stripe       *Stripe
	limiter      ratelimit.Limiter
	now          func() time.Time
	availability paymentAvailability
}

func New(cfg *config.Config, prices *pricing.Pricing, catalog *account.Catalog, pool *pgxpool.Pool) *Service {
	return &Service{cfg: cfg, pricing: prices, catalog: catalog, pool: pool, stripe: &Stripe{Key: cfg.Billing.StripeSecretKey.Reveal()}, limiter: ratelimit.NewMemory(), now: time.Now}
}
func (s *Service) live() bool { return strings.Contains(s.stripe.Key, "_live_") }
func (s *Service) ready() bool {
	if s.stripe.Key == "" || !s.cfg.Billing.StripeWebhookSecret.IsSet() || s.cfg.Billing.StripePortalConfig == "" || s.cfg.Billing.StripePaymentConfig == "" {
		return false
	}
	for _, key := range []string{"starter", "pro", "business", "topup"} {
		if s.cfg.Billing.StripePrices[key] == "" {
			return false
		}
	}
	for _, flag := range s.cfg.KillFlags {
		if flag == "billing" {
			return false
		}
	}
	return true
}
func (s *Service) testAllowed(email string) bool {
	if s.live() || s.cfg.IsDev() {
		return true
	}
	for _, allowed := range s.cfg.Billing.StripeTestEmails {
		if strings.EqualFold(strings.TrimSpace(allowed), email) {
			return true
		}
	}
	return false
}

type fault struct{ code contracts.ErrorCode }

func (f fault) Error() string             { return string(f.code) }
func fail(code contracts.ErrorCode) error { return fault{code} }

func billingLock(ctx context.Context, tx pgx.Tx, org uuid.UUID) error {
	_, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", "billing:"+org.String())
	return err
}
func (s *Service) actor(ctx context.Context, tx pgx.Tx, p session.Principal) (string, error) {
	var email, role string
	err := tx.QueryRow(ctx, `SELECT u.email, m.role FROM users u JOIN memberships m ON m.user_id=u.id JOIN orgs o ON o.id=m.org_id
 WHERE u.id=$1 AND m.org_id=$2 AND o.deleted_at IS NULL AND u.email_verified_at IS NOT NULL
 AND u.suspended_at IS NULL AND u.deletion_requested_at IS NULL`, p.UserID, p.OrgID).Scan(&email, &role)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", fail(contracts.ErrForbidden)
	}
	if err != nil {
		return "", err
	}
	if role != "owner" && role != "admin" {
		return "", fail(contracts.ErrForbidden)
	}
	return email, nil
}
func (s *Service) authorized(ctx context.Context, tx pgx.Tx, p session.Principal) (string, error) {
	email, err := s.actor(ctx, tx, p)
	if err != nil {
		return "", err
	}
	if !s.ready() || !s.testAllowed(email) {
		return "", fail(contracts.ErrBillingUnavailable)
	}
	d, err := s.limiter.Allow(ctx, p.OrgID.String(), ratelimit.Policy{Limit: 10, Period: time.Minute})
	if err != nil {
		return "", err
	}
	if !d.Allowed {
		return "", fail(contracts.ErrRateLimited)
	}
	return email, nil
}
func (s *Service) customer(ctx context.Context, tx pgx.Tx, org uuid.UUID, email string) (string, error) {
	var id string
	var blocked bool
	err := tx.QueryRow(ctx, "SELECT stripe_customer_id, blocked FROM billing_accounts WHERE org_id=$1 AND livemode=$2", org, s.live()).Scan(&id, &blocked)
	if err == nil {
		if blocked {
			return "", fail(contracts.ErrSubscriptionInactive)
		}
		return id, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	var c customer
	form := url.Values{"email": {email}, "metadata[app]": {"docmatch"}, "metadata[org_id]": {org.String()}}
	if err = s.stripe.call(ctx, http.MethodPost, "/customers", form, "docmatch-customer-"+org.String()+"-"+strconv.FormatBool(s.live()), &c); err != nil {
		return "", err
	}
	if !strings.HasPrefix(c.ID, "cus_") {
		return "", errors.New("billing: missing Stripe customer")
	}
	_, err = tx.Exec(ctx, "INSERT INTO billing_accounts(org_id,livemode,stripe_customer_id) VALUES($1,$2,$3)", org, s.live(), c.ID)
	return c.ID, err
}

type CheckoutRequest struct {
	Plan        string `json:"plan,omitempty"`
	Credits     int64  `json:"credits,omitempty"`
	CouponCode  string `json:"coupon_code,omitempty"`
	SuccessPath string `json:"success_path,omitempty"`
	CancelPath  string `json:"cancel_path,omitempty"`
}
type CheckoutResponse struct {
	URL       string    `json:"checkout_url"`
	ID        string    `json:"session_id"`
	ExpiresAt time.Time `json:"expires_at"`
}
type order struct {
	ID                                        uuid.UUID
	Kind, Plan, Price, Session, URL, Currency string
	Credits, Amount                           int64
	Expires                                   *time.Time
	Paid                                      *time.Time
}

func returnURL(base, path, fallback string) (string, error) {
	if path == "" {
		path = fallback
	}
	if len(path) > 1024 || !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") || strings.ContainsAny(path, "\\\r\n\t") {
		return "", fail(contracts.ErrValidationFailed)
	}
	parsed, err := url.Parse(path)
	if err != nil || parsed.Host != "" || parsed.IsAbs() {
		return "", fail(contracts.ErrValidationFailed)
	}
	return strings.TrimRight(base, "/") + path, nil
}
func cents(value float64) int64 { return int64(math.Round(value * 100)) }
func (s *Service) minimumTopup() int64 {
	return (minimumTopupCents + cents(s.pricing.TokenBRL) - 1) / cents(s.pricing.TokenBRL)
}
func (s *Service) validatePrice(ctx context.Context, code string, amount int64) (price, error) {
	var p price
	id := s.cfg.Billing.StripePrices[code]
	if id == "" {
		return p, fail(contracts.ErrBillingUnavailable)
	}
	if err := s.stripe.call(ctx, http.MethodGet, "/prices/"+url.PathEscape(id), nil, "", &p); err != nil {
		return p, err
	}
	if p.ID != id || !p.Active || p.Livemode != s.live() || p.Currency != "brl" || p.UnitAmount != amount || p.Metadata["app"] != "docmatch" || p.Metadata["plan"] != code {
		return p, fail(contracts.ErrBillingUnavailable)
	}
	if code == "topup" {
		if p.Recurring != nil {
			return p, fail(contracts.ErrBillingUnavailable)
		}
	} else if p.Recurring == nil || p.Recurring.Interval != "month" || p.Recurring.IntervalCount != 1 {
		return p, fail(contracts.ErrBillingUnavailable)
	}
	return p, nil
}

func (s *Service) Checkout(ctx context.Context, p session.Principal, req CheckoutRequest, key string, topup bool) (CheckoutResponse, error) {
	var out CheckoutResponse
	if p.OrgID == uuid.Nil {
		return out, fail(contracts.ErrForbidden)
	}
	if len(key) > 128 || strings.ContainsAny(key, "\r\n\x00") {
		return out, fail(contracts.ErrValidationFailed)
	}
	if key == "" {
		return out, fail(contracts.ErrMissingIdempotencyKey)
	}
	if req.CouponCode != "" {
		return out, fail(contracts.ErrCouponInvalid)
	}
	kind, plan, count, amount, quantity := "subscription", req.Plan, int64(0), int64(0), int64(1)
	if topup {
		kind, plan = "topup", "topup"
		if req.Credits < s.minimumTopup() {
			return out, fail(contracts.ErrTopupBelowMinimum)
		}
		if req.Credits > maximumTopupCredits {
			return out, fail(contracts.ErrValidationFailed)
		}
		count, quantity = req.Credits, req.Credits
		amount = count * cents(s.pricing.TokenBRL)
	} else {
		v, ok := s.pricing.Plans[plan]
		if !ok {
			return out, fail(contracts.ErrPlanUnknown)
		}
		count, amount = v.Tokens, cents(v.PriceBRL)
	}
	success, err := returnURL(s.cfg.PublicBaseURL.String(), req.SuccessPath, "/conta?pagamento=sucesso")
	if err != nil {
		return out, err
	}
	cancel, err := returnURL(s.cfg.PublicBaseURL.String(), req.CancelPath, "/conta?pagamento=cancelado")
	if err != nil {
		return out, err
	}
	err = db.WithOrgTx(ctx, s.pool, p.OrgID, func(ctx context.Context, tx pgx.Tx) error {
		if err := billingLock(ctx, tx, p.OrgID); err != nil {
			return err
		}
		email, err := s.authorized(ctx, tx, p)
		if err != nil {
			return err
		}
		if !s.paymentsAvailable(ctx) {
			return fail(contracts.ErrBillingUnavailable)
		}

		var blocked bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM billing_accounts WHERE org_id=$1 AND livemode=$2 AND blocked)", p.OrgID, s.live()).Scan(&blocked); err != nil {
			return err
		}
		if blocked {
			return fail(contracts.ErrSubscriptionInactive)
		}
		var existing order
		err = tx.QueryRow(ctx, `SELECT stripe_session_id,checkout_url,expires_at,kind,plan_code,credits FROM billing_orders
   WHERE org_id=$1 AND livemode=$2 AND idempotency_key=$3`, p.OrgID, s.live(), key).Scan(&existing.Session, &existing.URL, &existing.Expires, &existing.Kind, &existing.Plan, &existing.Credits)
		if err == nil {
			if existing.Kind != kind || existing.Plan != plan || existing.Credits != count {
				return fail(contracts.ErrIdempotencyKeyReused)
			}
			if existing.Expires == nil || !existing.Expires.After(s.now()) {
				return fail(contracts.ErrConflict)
			}
			out = CheckoutResponse{existing.URL, existing.Session, *existing.Expires}
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		var active bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM subscriptions WHERE org_id=$1 AND livemode=$2 AND status IN ('active','trialing','past_due','incomplete','paused','unpaid'))`, p.OrgID, s.live()).Scan(&active)
		if err != nil {
			return err
		}
		if !topup && active {
			return fail(contracts.ErrConflict)
		}
		if topup {
			err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM subscriptions WHERE org_id=$1 AND livemode=$2 AND status='active' AND current_period_end>now())`, p.OrgID, s.live()).Scan(&active)
			if err != nil {
				return err
			}
			if !active {
				return fail(contracts.ErrSubscriptionRequired)
			}
		} else {
			err = tx.QueryRow(ctx, `SELECT stripe_session_id,checkout_url,expires_at,plan_code FROM billing_orders
    WHERE org_id=$1 AND livemode=$2 AND kind='subscription' AND paid_at IS NULL AND expires_at>now() ORDER BY created_at DESC LIMIT 1`, p.OrgID, s.live()).Scan(&existing.Session, &existing.URL, &existing.Expires, &existing.Plan)
			if err == nil {
				if existing.Plan != plan {
					return fail(contracts.ErrConflict)
				}
				out = CheckoutResponse{existing.URL, existing.Session, *existing.Expires}
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		unit := amount
		if topup {
			unit = cents(s.pricing.TokenBRL)
		}
		stripePrice, err := s.validatePrice(ctx, plan, unit)
		if err != nil {
			return err
		}
		customerID, err := s.customer(ctx, tx, p.OrgID, email)
		if err != nil {
			return err
		}
		id := uuid.NewSHA1(uuid.NameSpaceURL, []byte("docmatch-checkout/"+p.OrgID.String()+"/"+strconv.FormatBool(s.live())+"/"+key))
		_, err = tx.Exec(ctx, `INSERT INTO billing_orders(id,org_id,livemode,idempotency_key,kind,plan_code,credits,amount_cents,price_id,currency) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,'brl')`, id, p.OrgID, s.live(), key, kind, plan, count, amount, stripePrice.ID)
		if err != nil {
			return err
		}
		mode := "subscription"
		if topup {
			mode = "payment"
		}
		form := url.Values{"mode": {mode}, "customer": {customerID}, "line_items[0][price]": {stripePrice.ID}, "line_items[0][quantity]": {strconv.FormatInt(quantity, 10)},
			"success_url": {success}, "cancel_url": {cancel}, "client_reference_id": {p.OrgID.String()}, "locale": {"pt-BR"}, "payment_method_configuration": {s.cfg.Billing.StripePaymentConfig},
			"metadata[app]": {"docmatch"}, "metadata[org_id]": {p.OrgID.String()}, "metadata[order_id]": {id.String()}, "metadata[plan]": {plan}}
		prefix := "subscription_data"
		if topup {
			prefix = "payment_intent_data"
		}
		form.Set(prefix+"[metadata][app]", "docmatch")
		form.Set(prefix+"[metadata][org_id]", p.OrgID.String())
		form.Set(prefix+"[metadata][plan]", plan)
		form.Set(prefix+"[metadata][order_id]", id.String())
		sum := sha256.Sum256([]byte(id.String()))
		var checkout checkoutSession
		if err = s.stripe.call(ctx, http.MethodPost, "/checkout/sessions", form, "docmatch-checkout-"+hex.EncodeToString(sum[:]), &checkout); err != nil {
			return err
		}
		if checkout.ID == "" || !safeStripeURL(checkout.URL, "checkout.stripe.com") || checkout.Livemode != s.live() {
			return errors.New("billing: invalid checkout response")
		}
		expires := time.Unix(checkout.ExpiresAt, 0).UTC()
		_, err = tx.Exec(ctx, "UPDATE billing_orders SET stripe_session_id=$2,checkout_url=$3,expires_at=$4 WHERE id=$1", id, checkout.ID, checkout.URL, expires)
		out = CheckoutResponse{checkout.URL, checkout.ID, expires}
		return err
	})
	return out, err
}
func safeStripeURL(raw, host string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Host == host && u.User == nil
}

func (s *Service) Portal(ctx context.Context, p session.Principal) (string, error) {
	var result string
	err := db.WithOrgTx(ctx, s.pool, p.OrgID, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := s.authorized(ctx, tx, p); err != nil {
			return err
		}
		var id string
		err := tx.QueryRow(ctx, "SELECT stripe_customer_id FROM billing_accounts WHERE org_id=$1 AND livemode=$2", p.OrgID, s.live()).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return fail(contracts.ErrSubscriptionRequired)
		}
		if err != nil {
			return err
		}
		var out struct {
			URL string `json:"url"`
		}
		form := url.Values{"customer": {id}, "configuration": {s.cfg.Billing.StripePortalConfig}, "return_url": {strings.TrimRight(s.cfg.PublicBaseURL.String(), "/") + "/conta"}, "locale": {"pt-BR"}}
		if err = s.stripe.call(ctx, http.MethodPost, "/billing_portal/sessions", form, "", &out); err != nil {
			return err
		}
		if !safeStripeURL(out.URL, "billing.stripe.com") {
			return errors.New("billing: invalid portal response")
		}
		result = out.URL
		return nil
	})
	return result, err
}
