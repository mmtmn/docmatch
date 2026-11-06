// Package pgstore implements the domain Store interfaces (credits, audit)
// on PostgreSQL with pgx and the sqlc-generated queries. Every transaction
// goes through db.WithOrgTx, so row-level security scopes it to one
// organization.
package pgstore

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mmtmn/docmatch/services/api/internal/credits"
	"github.com/mmtmn/docmatch/services/api/internal/db"
	"github.com/mmtmn/docmatch/services/api/internal/db/sqlc"
)

// CreditStore is the pgx implementation of credits.Store.
type CreditStore struct {
	pool *pgxpool.Pool
}

// NewCreditStore wraps the pool.
func NewCreditStore(pool *pgxpool.Pool) *CreditStore { return &CreditStore{pool: pool} }

// InOrgTx implements credits.Store.
func (s *CreditStore) InOrgTx(ctx context.Context, orgID uuid.UUID, fn func(ctx context.Context, tx credits.Tx) error) error {
	return db.WithOrgTx(ctx, s.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		return fn(ctx, &creditTx{q: sqlc.New(tx)})
	})
}

type creditTx struct {
	q *sqlc.Queries
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func strVal(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func toAccount(a sqlc.CreditAccount) credits.Account {
	return credits.Account{OrgID: a.OrgID, Balance: a.Balance, Held: a.Held, UpdatedAt: a.UpdatedAt, BillingBlocked: a.BillingBlocked, TokenExempt: a.TokenExempt}
}

func toGrant(g sqlc.CreditGrant) credits.Grant {
	return credits.Grant{
		ID: g.ID, OrgID: g.OrgID, Kind: credits.GrantKind(g.Kind), Amount: g.Amount, Remaining: g.Remaining,
		Source: strVal(g.Source), IdempotencyKey: strVal(g.IdempotencyKey), GrantedAt: g.GrantedAt,
		ExpiresAt: g.ExpiresAt, ExpiredAt: g.ExpiredAt,
	}
}

func toHold(h sqlc.CreditHold) credits.Hold {
	return credits.Hold{
		ID: h.ID, OrgID: h.OrgID, Amount: h.Amount, Sponsored: h.Sponsored, Status: credits.HoldStatus(h.Status), SettledAmount: h.SettledAmount,
		RunID: h.RunID, QuoteID: h.QuoteID, Reference: strVal(h.Reference), CreatedAt: h.CreatedAt, ClosedAt: h.ClosedAt,
	}
}

func toLedger(l sqlc.CreditLedger) credits.LedgerEntry {
	return credits.LedgerEntry{
		ID: l.ID, OrgID: l.OrgID, Seq: l.Seq, Kind: credits.LedgerKind(l.Kind), Amount: l.Amount, HeldDelta: l.HeldDelta,
		BalanceAfter: l.BalanceAfter, HeldAfter: l.HeldAfter, GrantID: l.GrantID, HoldID: l.HoldID, RunID: l.RunID,
		Reference: strVal(l.Reference), CreatedAt: l.CreatedAt,
	}
}

func (t *creditTx) Account(ctx context.Context, orgID uuid.UUID) (credits.Account, bool, error) {
	a, err := t.q.GetCreditAccount(ctx, orgID)
	if errors.Is(err, pgx.ErrNoRows) {
		return credits.Account{}, false, nil
	}
	if err != nil {
		return credits.Account{}, false, err
	}
	return toAccount(a), true, nil
}

// AccountForUpdate upserts the row; ON CONFLICT DO UPDATE takes the row lock
// that serializes concurrent reservations.
func (t *creditTx) AccountForUpdate(ctx context.Context, orgID uuid.UUID) (credits.Account, error) {
	a, err := t.q.EnsureCreditAccount(ctx, orgID)
	if err != nil {
		return credits.Account{}, err
	}
	return toAccount(a), nil
}

func (t *creditTx) UpdateAccount(ctx context.Context, orgID uuid.UUID, balance, held int64) (credits.Account, error) {
	a, err := t.q.UpdateCreditAccount(ctx, sqlc.UpdateCreditAccountParams{OrgID: orgID, Balance: balance, Held: held})
	if err != nil {
		return credits.Account{}, err
	}
	return toAccount(a), nil
}

func (t *creditTx) GrantByIdempotencyKey(ctx context.Context, orgID uuid.UUID, key string) (credits.Grant, bool, error) {
	g, err := t.q.GetCreditGrantByIdempotencyKey(ctx, sqlc.GetCreditGrantByIdempotencyKeyParams{OrgID: orgID, IdempotencyKey: strPtr(key)})
	if errors.Is(err, pgx.ErrNoRows) {
		return credits.Grant{}, false, nil
	}
	if err != nil {
		return credits.Grant{}, false, err
	}
	return toGrant(g), true, nil
}

func (t *creditTx) InsertGrant(ctx context.Context, g credits.Grant) (credits.Grant, error) {
	row, err := t.q.InsertCreditGrant(ctx, sqlc.InsertCreditGrantParams{
		OrgID: g.OrgID, Kind: string(g.Kind), Amount: g.Amount, Source: strPtr(g.Source),
		IdempotencyKey: strPtr(g.IdempotencyKey), ExpiresAt: g.ExpiresAt,
	})
	if err != nil {
		return credits.Grant{}, err
	}
	return toGrant(row), nil
}

func (t *creditTx) OpenGrantsForUpdate(ctx context.Context, orgID uuid.UUID) ([]credits.Grant, error) {
	rows, err := t.q.ListOpenCreditGrantsForUpdate(ctx, orgID)
	if err != nil {
		return nil, err
	}
	out := make([]credits.Grant, 0, len(rows))
	for _, r := range rows {
		out = append(out, toGrant(r))
	}
	return out, nil
}

func (t *creditTx) ExpiredGrantsForUpdate(ctx context.Context, orgID uuid.UUID, now time.Time) ([]credits.Grant, error) {
	rows, err := t.q.ListExpiredOpenCreditGrantsForUpdate(ctx, sqlc.ListExpiredOpenCreditGrantsForUpdateParams{OrgID: orgID, ExpiresAt: &now})
	if err != nil {
		return nil, err
	}
	out := make([]credits.Grant, 0, len(rows))
	for _, r := range rows {
		out = append(out, toGrant(r))
	}
	return out, nil
}

func (t *creditTx) SetGrantRemaining(ctx context.Context, grantID uuid.UUID, remaining int64) error {
	return t.q.UpdateCreditGrantRemaining(ctx, sqlc.UpdateCreditGrantRemainingParams{ID: grantID, Remaining: remaining})
}

func (t *creditTx) MarkGrantExpired(ctx context.Context, grantID uuid.UUID, remaining int64, at time.Time) error {
	return t.q.MarkCreditGrantExpired(ctx, sqlc.MarkCreditGrantExpiredParams{ID: grantID, Remaining: remaining, ExpiredAt: &at})
}

func (t *creditTx) InsertHold(ctx context.Context, h credits.Hold) (credits.Hold, error) {
	row, err := t.q.InsertCreditHold(ctx, sqlc.InsertCreditHoldParams{
		OrgID: h.OrgID, Amount: h.Amount, Sponsored: h.Sponsored, RunID: h.RunID, QuoteID: h.QuoteID, Reference: strPtr(h.Reference),
	})
	if err != nil {
		return credits.Hold{}, err
	}
	return toHold(row), nil
}

func (t *creditTx) HoldForUpdate(ctx context.Context, orgID, holdID uuid.UUID) (credits.Hold, bool, error) {
	h, err := t.q.GetCreditHoldForUpdate(ctx, sqlc.GetCreditHoldForUpdateParams{OrgID: orgID, ID: holdID})
	if errors.Is(err, pgx.ErrNoRows) {
		return credits.Hold{}, false, nil
	}
	if err != nil {
		return credits.Hold{}, false, err
	}
	return toHold(h), true, nil
}

func (t *creditTx) CloseHold(ctx context.Context, orgID, holdID uuid.UUID, status credits.HoldStatus, settled *int64) (credits.Hold, error) {
	h, err := t.q.CloseCreditHold(ctx, sqlc.CloseCreditHoldParams{OrgID: orgID, ID: holdID, Status: string(status), SettledAmount: settled})
	if errors.Is(err, pgx.ErrNoRows) {
		return credits.Hold{}, credits.ErrHoldClosed
	}
	if err != nil {
		return credits.Hold{}, err
	}
	return toHold(h), nil
}

func (t *creditTx) AppendLedger(ctx context.Context, e credits.LedgerEntry) (credits.LedgerEntry, error) {
	row, err := t.q.AppendCreditLedger(ctx, sqlc.AppendCreditLedgerParams{
		OrgID: e.OrgID, Kind: string(e.Kind), Amount: e.Amount, HeldDelta: e.HeldDelta,
		BalanceAfter: e.BalanceAfter, HeldAfter: e.HeldAfter, GrantID: e.GrantID, HoldID: e.HoldID, RunID: e.RunID,
		Reference: strPtr(e.Reference), Meta: []byte("{}"),
	})
	if err != nil {
		return credits.LedgerEntry{}, err
	}
	return toLedger(row), nil
}

func (t *creditTx) RecentLedger(ctx context.Context, orgID uuid.UUID, limit int) ([]credits.LedgerEntry, error) {
	rows, err := t.q.ListCreditLedger(ctx, sqlc.ListCreditLedgerParams{OrgID: orgID, Limit: int32(limit)}) //nolint:gosec // limit is small
	if err != nil {
		return nil, err
	}
	out := make([]credits.LedgerEntry, 0, len(rows))
	for _, r := range rows {
		out = append(out, toLedger(r))
	}
	return out, nil
}
