// Package credits manages docmatch tokens ("credits"): grants add balance,
// Reserve places an atomic hold that never overdraws, Settle debits the
// actual cost capped at the hold and releases the rest, Release cancels a
// hold and ExpireGrants retires credits past their expiry.
//
// Invariants, enforced in transactions and checked by the conformance suite
// in credtest:
//
//	balance = Σ grants − Σ settled − Σ expired
//	0 ≤ held ≤ balance            (also a CHECK constraint in PostgreSQL)
//	held    = Σ amount of open holds
//	balance = Σ remaining of open (unexpired) grants
//	each ledger row carries balance_after/held_after equal to the account
//
// Every operation runs inside Store.InOrgTx, which scopes the transaction to
// the organization (SET LOCAL app.org_id for row-level security) and rolls
// back on any error, so a crash mid-operation leaves nothing half-applied.
package credits

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// Account is the per-organization balance.
type Account struct {
	BillingBlocked bool
	TokenExempt    bool
	OrgID          uuid.UUID
	Balance        int64
	Held           int64
	UpdatedAt      time.Time
}

// Available is what a new hold may reserve.
func (a Account) Available() int64 { return a.Balance - a.Held }

// GrantKind classifies where credits came from.
type GrantKind string

// Grant kinds (mirror the CHECK constraint on credit_grants.kind).
const (
	GrantPlan       GrantKind = "plan"
	GrantTopup      GrantKind = "topup"
	GrantBonus      GrantKind = "bonus"
	GrantRefund     GrantKind = "refund"
	GrantAdjustment GrantKind = "adjustment"
)

// Valid reports whether the kind is one of the known values.
func (k GrantKind) Valid() bool {
	switch k {
	case GrantPlan, GrantTopup, GrantBonus, GrantRefund, GrantAdjustment:
		return true
	}
	return false
}

// Grant is a batch of credits with an optional expiry. Remaining tracks how
// much of it is still unconsumed (settlements consume oldest-expiring first).
type Grant struct {
	ID             uuid.UUID
	OrgID          uuid.UUID
	Kind           GrantKind
	Amount         int64
	Remaining      int64
	Source         string
	IdempotencyKey string
	GrantedAt      time.Time
	ExpiresAt      *time.Time
	ExpiredAt      *time.Time
}

// HoldStatus is the lifecycle of a hold.
type HoldStatus string

// Hold statuses (mirror credit_holds.status).
const (
	HoldHeld     HoldStatus = "held"
	HoldSettled  HoldStatus = "settled"
	HoldReleased HoldStatus = "released"
)

// Hold is a reservation against the balance for a run.
type Hold struct {
	Sponsored     bool
	ID            uuid.UUID
	OrgID         uuid.UUID
	Amount        int64
	Status        HoldStatus
	SettledAmount *int64
	RunID         *uuid.UUID
	QuoteID       *uuid.UUID
	Reference     string
	CreatedAt     time.Time
	ClosedAt      *time.Time
}

// LedgerKind classifies ledger rows.
type LedgerKind string

// Ledger kinds (mirror credit_ledger.kind).
const (
	LedgerGrant   LedgerKind = "grant"
	LedgerHold    LedgerKind = "hold"
	LedgerSettle  LedgerKind = "settle"
	LedgerRelease LedgerKind = "release"
	LedgerExpire  LedgerKind = "expire"
	LedgerAdjust  LedgerKind = "adjust"
)

// LedgerEntry is one append-only ledger row. Amount is the balance delta and
// HeldDelta the held delta; the *After fields snapshot the account. Seq is
// the per-organization sequence number assigned under the account lock, so
// ordering by it is the exact order of balance changes.
type LedgerEntry struct {
	ID           uuid.UUID
	OrgID        uuid.UUID
	Seq          int64
	Kind         LedgerKind
	Amount       int64
	HeldDelta    int64
	BalanceAfter int64
	HeldAfter    int64
	GrantID      *uuid.UUID
	HoldID       *uuid.UUID
	RunID        *uuid.UUID
	Reference    string
	CreatedAt    time.Time
}

// GrantParams describe a Grant call.
type GrantParams struct {
	Kind   GrantKind
	Amount int64
	Source string
	// IdempotencyKey makes repeated calls (webhook retries) return the
	// original grant instead of granting twice.
	IdempotencyKey string
	ExpiresAt      *time.Time
}

// ReserveParams describe a Reserve call.
type ReserveParams struct {
	Amount    int64
	RunID     *uuid.UUID
	QuoteID   *uuid.UUID
	Reference string
}

// Settlement is the outcome of Settle.
type Settlement struct {
	Hold     Hold
	Debited  int64 // min(actual, hold.Amount)
	Released int64 // hold.Amount − Debited
}

// Expiry is the outcome of ExpireGrants.
type Expiry struct {
	// Expired is the amount retired from the balance.
	Expired int64
	// Grants is how many grants were fully retired.
	Grants int
	// Deferred is the amount that stayed open because holds still cover it;
	// the next pass retires it once the holds settle or release.
	Deferred int64
}

// Store is the persistence boundary. InOrgTx runs fn in a transaction
// scoped to orgID and commits only when fn returns nil.
type Store interface {
	InOrgTx(ctx context.Context, orgID uuid.UUID, fn func(ctx context.Context, tx Tx) error) error
}

// Tx is the set of primitives the service composes inside one transaction.
// Row-returning methods report ok=false (not an error) when nothing matches.
type Tx interface {
	// Account reads the account without locking; ok=false when it does not exist yet.
	Account(ctx context.Context, orgID uuid.UUID) (Account, bool, error)
	// AccountForUpdate creates the account row if missing and locks it.
	AccountForUpdate(ctx context.Context, orgID uuid.UUID) (Account, error)
	UpdateAccount(ctx context.Context, orgID uuid.UUID, balance, held int64) (Account, error)

	GrantByIdempotencyKey(ctx context.Context, orgID uuid.UUID, key string) (Grant, bool, error)
	InsertGrant(ctx context.Context, g Grant) (Grant, error)
	// OpenGrantsForUpdate returns unexpired grants with remaining > 0 in
	// consumption order (earliest expiry first, then oldest), locked.
	OpenGrantsForUpdate(ctx context.Context, orgID uuid.UUID) ([]Grant, error)
	// ExpiredGrantsForUpdate returns grants past expiry that still hold credits, locked.
	ExpiredGrantsForUpdate(ctx context.Context, orgID uuid.UUID, now time.Time) ([]Grant, error)
	SetGrantRemaining(ctx context.Context, grantID uuid.UUID, remaining int64) error
	MarkGrantExpired(ctx context.Context, grantID uuid.UUID, remaining int64, at time.Time) error

	InsertHold(ctx context.Context, h Hold) (Hold, error)
	HoldForUpdate(ctx context.Context, orgID, holdID uuid.UUID) (Hold, bool, error)
	CloseHold(ctx context.Context, orgID, holdID uuid.UUID, status HoldStatus, settled *int64) (Hold, error)

	// AppendLedger assigns the next per-organization Seq and stores the row.
	AppendLedger(ctx context.Context, e LedgerEntry) (LedgerEntry, error)
	// RecentLedger lists the newest rows of the organization by Seq descending.
	RecentLedger(ctx context.Context, orgID uuid.UUID, limit int) ([]LedgerEntry, error)
}
