// Package entitlement centralizes live paid/QA workspace access.
package entitlement

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/mmtmn/docmatch/services/api/internal/config"
)

func Paid(ctx context.Context, tx pgx.Tx, cfg *config.Config, org uuid.UUID) (plan string, eligible bool, err error) {
	live := strings.Contains(cfg.Billing.StripeSecretKey.Reveal(), "_live_")
	err = tx.QueryRow(ctx, `SELECT o.plan_code, NOT COALESCE(c.billing_blocked,false) AND
 (COALESCE(c.token_exempt,false) OR $3 OR EXISTS(SELECT 1 FROM subscriptions sub
 WHERE sub.org_id=o.id AND sub.livemode=$2 AND sub.status='active' AND sub.current_period_end>now()
 AND sub.plan_code=o.plan_code)) FROM orgs o LEFT JOIN credit_accounts c ON c.org_id=o.id
 WHERE o.id=$1 AND o.deleted_at IS NULL`, org, live, cfg.IsDev()).Scan(&plan, &eligible)
	return
}
