package pgstore

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/mmtmn/docmatch/services/api/internal/credits"
	"github.com/mmtmn/docmatch/services/api/internal/db/sqlc"
)

// CreditStoreInTx lets payment processing commit its event and credit grant atomically.
// The caller owns a transaction already scoped to orgID through db.WithOrgTx.
func CreditStoreInTx(tx pgx.Tx, orgID uuid.UUID) credits.Store {
	return scopedCreditTx{tx: tx, orgID: orgID}
}

type scopedCreditTx struct {
	tx    pgx.Tx
	orgID uuid.UUID
}

func (s scopedCreditTx) InOrgTx(ctx context.Context, orgID uuid.UUID, fn func(context.Context, credits.Tx) error) error {
	if orgID != s.orgID {
		return errors.New("credits: transaction tenant mismatch")
	}
	return fn(ctx, &creditTx{q: sqlc.New(s.tx)})
}
