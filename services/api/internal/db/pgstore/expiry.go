package pgstore

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mmtmn/docmatch/services/api/internal/db"
	"github.com/mmtmn/docmatch/services/api/internal/db/sqlc"
)

// ListOrgsWithExpiredGrants finds organizations holding grants past their
// expiry. It crosses tenants, so it runs in system mode; the caller then
// expires each organization inside its own scoped transaction.
func ListOrgsWithExpiredGrants(ctx context.Context, pool *pgxpool.Pool, now time.Time) ([]uuid.UUID, error) {
	var out []uuid.UUID
	err := db.WithSystemTx(ctx, pool, func(ctx context.Context, tx pgx.Tx) error {
		ids, err := sqlc.New(tx).ListOrgsWithExpiredGrants(ctx, &now)
		out = ids
		return err
	})
	return out, err
}
