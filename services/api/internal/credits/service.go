package credits

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Errors returned by the service. Messages are internal (English); handlers
// map them to pt-BR envelopes.
var (
	ErrInvalidAmount = errors.New("credits: amount must be positive")
	ErrInvalidKind   = errors.New("credits: unknown grant kind")
	ErrHoldNotFound  = errors.New("credits: hold not found")
	ErrHoldClosed    = errors.New("credits: hold already settled or released")
	ErrInconsistent  = errors.New("credits: account state is inconsistent")
	ErrNilStore      = errors.New("credits: nil store")
)

// InsufficientError is returned by Reserve when the balance does not cover
// the request. Available is balance − held at the time of the attempt.
type InsufficientError struct {
	OrgID     uuid.UUID
	Requested int64
	Available int64
}

func (e *InsufficientError) Error() string {
	return fmt.Sprintf("credits: insufficient balance for org %s: requested %d, available %d", e.OrgID, e.Requested, e.Available)
}

// Is lets errors.Is(err, ErrInsufficient) match any InsufficientError.
func (e *InsufficientError) Is(target error) bool { return target == ErrInsufficient }

// ErrInsufficient is the sentinel every InsufficientError matches.
var ErrInsufficient = errors.New("credits: insufficient balance")

// Service implements the credit operations over a Store.
type Service struct {
	store Store
	now   func() time.Time
}

// Option configures New.
type Option func(*Service)

// WithClock substitutes the time source (tests).
func WithClock(now func() time.Time) Option {
	return func(s *Service) {
		if now != nil {
			s.now = now
		}
	}
}

// New returns a service over store.
func New(store Store, opts ...Option) *Service {
	s := &Service{store: store, now: time.Now}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Balance returns the account (zero values when none exists yet).
func (s *Service) Balance(ctx context.Context, orgID uuid.UUID) (Account, error) {
	if s.store == nil {
		return Account{}, ErrNilStore
	}
	var acct Account
	err := s.store.InOrgTx(ctx, orgID, func(ctx context.Context, tx Tx) error {
		a, ok, err := tx.Account(ctx, orgID)
		if err != nil {
			return err
		}
		if !ok {
			a = Account{OrgID: orgID}
		}
		acct = a
		return nil
	})
	return acct, err
}

// Grant adds credits. With an idempotency key, a repeated call returns the
// original grant and changes nothing.
func (s *Service) Grant(ctx context.Context, orgID uuid.UUID, p GrantParams) (Grant, error) {
	if s.store == nil {
		return Grant{}, ErrNilStore
	}
	if p.Amount <= 0 {
		return Grant{}, ErrInvalidAmount
	}
	if !p.Kind.Valid() {
		return Grant{}, ErrInvalidKind
	}
	var out Grant
	err := s.store.InOrgTx(ctx, orgID, func(ctx context.Context, tx Tx) error {
		if p.IdempotencyKey != "" {
			existing, ok, err := tx.GrantByIdempotencyKey(ctx, orgID, p.IdempotencyKey)
			if err != nil {
				return err
			}
			if ok {
				out = existing
				return nil
			}
		}
		acct, err := tx.AccountForUpdate(ctx, orgID)
		if err != nil {
			return err
		}
		g, err := tx.InsertGrant(ctx, Grant{
			OrgID: orgID, Kind: p.Kind, Amount: p.Amount, Remaining: p.Amount,
			Source: p.Source, IdempotencyKey: p.IdempotencyKey, GrantedAt: s.now(), ExpiresAt: p.ExpiresAt,
		})
		if err != nil {
			return err
		}
		acct, err = tx.UpdateAccount(ctx, orgID, acct.Balance+p.Amount, acct.Held)
		if err != nil {
			return err
		}
		_, err = tx.AppendLedger(ctx, LedgerEntry{
			OrgID: orgID, Kind: LedgerGrant, Amount: p.Amount, HeldDelta: 0,
			BalanceAfter: acct.Balance, HeldAfter: acct.Held, GrantID: &g.ID, Reference: p.Source, CreatedAt: s.now(),
		})
		if err != nil {
			return err
		}
		out = g
		return nil
	})
	return out, err
}

// Reserve places a hold for p.Amount if, and only if, balance − held covers
// it. The account row is locked for the duration, so concurrent calls
// serialize and can never overdraw.
func (s *Service) Reserve(ctx context.Context, orgID uuid.UUID, p ReserveParams) (Hold, error) {
	if s.store == nil {
		return Hold{}, ErrNilStore
	}
	if p.Amount <= 0 {
		return Hold{}, ErrInvalidAmount
	}
	var out Hold
	err := s.store.InOrgTx(ctx, orgID, func(ctx context.Context, tx Tx) error {
		acct, err := tx.AccountForUpdate(ctx, orgID)
		if err != nil {
			return err
		}
		amount := p.Amount
		if acct.TokenExempt {
			amount = 0
		}
		if acct.BillingBlocked || acct.Available() < amount {
			return &InsufficientError{OrgID: orgID, Requested: p.Amount, Available: acct.Available()}
		}
		h, err := tx.InsertHold(ctx, Hold{
			OrgID: orgID, Amount: amount, Sponsored: acct.TokenExempt, Status: HoldHeld, RunID: p.RunID, QuoteID: p.QuoteID,
			Reference: p.Reference, CreatedAt: s.now(),
		})
		if err != nil {
			return err
		}
		acct, err = tx.UpdateAccount(ctx, orgID, acct.Balance, acct.Held+amount)
		if err != nil {
			return err
		}
		_, err = tx.AppendLedger(ctx, LedgerEntry{
			OrgID: orgID, Kind: LedgerHold, Amount: 0, HeldDelta: amount,
			BalanceAfter: acct.Balance, HeldAfter: acct.Held, HoldID: &h.ID, RunID: p.RunID, Reference: p.Reference, CreatedAt: s.now(),
		})
		if err != nil {
			return err
		}
		out = h
		return nil
	})
	return out, err
}

// Settle closes a hold: debits min(actual, hold) from the balance, consuming
// the oldest-expiring grants first, and releases the remainder of the hold.
func (s *Service) Settle(ctx context.Context, orgID, holdID uuid.UUID, actual int64) (Settlement, error) {
	if s.store == nil {
		return Settlement{}, ErrNilStore
	}
	if actual < 0 {
		return Settlement{}, ErrInvalidAmount
	}
	var out Settlement
	err := s.store.InOrgTx(ctx, orgID, func(ctx context.Context, tx Tx) error {
		acct, err := tx.AccountForUpdate(ctx, orgID)
		if err != nil {
			return err
		}
		hold, ok, err := tx.HoldForUpdate(ctx, orgID, holdID)
		if err != nil {
			return err
		}
		if !ok {
			return ErrHoldNotFound
		}
		if hold.Status != HoldHeld {
			return ErrHoldClosed
		}
		debit := min(actual, hold.Amount)
		released := hold.Amount - debit
		if acct.Held < hold.Amount || acct.Balance < debit {
			return fmt.Errorf("%w: held %d < hold %d or balance %d < debit %d", ErrInconsistent, acct.Held, hold.Amount, acct.Balance, debit)
		}
		if err := consumeGrants(ctx, tx, orgID, debit); err != nil {
			return err
		}
		closed, err := tx.CloseHold(ctx, orgID, holdID, HoldSettled, &debit)
		if err != nil {
			return err
		}
		acct, err = tx.UpdateAccount(ctx, orgID, acct.Balance-debit, acct.Held-hold.Amount)
		if err != nil {
			return err
		}
		_, err = tx.AppendLedger(ctx, LedgerEntry{
			OrgID: orgID, Kind: LedgerSettle, Amount: -debit, HeldDelta: -hold.Amount,
			BalanceAfter: acct.Balance, HeldAfter: acct.Held, HoldID: &closed.ID, RunID: closed.RunID, Reference: closed.Reference, CreatedAt: s.now(),
		})
		if err != nil {
			return err
		}
		out = Settlement{Hold: closed, Debited: debit, Released: released}
		return nil
	})
	return out, err
}

// consumeGrants decrements Remaining across open grants, oldest expiry first,
// until amount is covered. Running out means the balance and the grants
// disagree, which is a bug worth failing loudly on.
func consumeGrants(ctx context.Context, tx Tx, orgID uuid.UUID, amount int64) error {
	if amount == 0 {
		return nil
	}
	grants, err := tx.OpenGrantsForUpdate(ctx, orgID)
	if err != nil {
		return err
	}
	left := amount
	for _, g := range grants {
		if left == 0 {
			break
		}
		take := min(g.Remaining, left)
		if err := tx.SetGrantRemaining(ctx, g.ID, g.Remaining-take); err != nil {
			return err
		}
		left -= take
	}
	if left != 0 {
		return fmt.Errorf("%w: grants cover %d of %d", ErrInconsistent, amount-left, amount)
	}
	return nil
}

// Release cancels a hold and gives the reserved amount back to the available
// balance. Nothing is debited.
func (s *Service) Release(ctx context.Context, orgID, holdID uuid.UUID) (Hold, error) {
	if s.store == nil {
		return Hold{}, ErrNilStore
	}
	var out Hold
	err := s.store.InOrgTx(ctx, orgID, func(ctx context.Context, tx Tx) error {
		acct, err := tx.AccountForUpdate(ctx, orgID)
		if err != nil {
			return err
		}
		hold, ok, err := tx.HoldForUpdate(ctx, orgID, holdID)
		if err != nil {
			return err
		}
		if !ok {
			return ErrHoldNotFound
		}
		if hold.Status != HoldHeld {
			return ErrHoldClosed
		}
		if acct.Held < hold.Amount {
			return fmt.Errorf("%w: held %d < hold %d", ErrInconsistent, acct.Held, hold.Amount)
		}
		closed, err := tx.CloseHold(ctx, orgID, holdID, HoldReleased, nil)
		if err != nil {
			return err
		}
		acct, err = tx.UpdateAccount(ctx, orgID, acct.Balance, acct.Held-hold.Amount)
		if err != nil {
			return err
		}
		_, err = tx.AppendLedger(ctx, LedgerEntry{
			OrgID: orgID, Kind: LedgerRelease, Amount: 0, HeldDelta: -hold.Amount,
			BalanceAfter: acct.Balance, HeldAfter: acct.Held, HoldID: &closed.ID, RunID: closed.RunID, Reference: closed.Reference, CreatedAt: s.now(),
		})
		if err != nil {
			return err
		}
		out = closed
		return nil
	})
	return out, err
}

// ExpireGrants retires grants whose expiry passed. Credits still covered by
// open holds cannot be retired without breaking held ≤ balance, so that part
// stays open (reported as Deferred) until the holds close; the next pass
// retires it. The service never retires more than balance − held.
func (s *Service) ExpireGrants(ctx context.Context, orgID uuid.UUID, now time.Time) (Expiry, error) {
	if s.store == nil {
		return Expiry{}, ErrNilStore
	}
	var out Expiry
	err := s.store.InOrgTx(ctx, orgID, func(ctx context.Context, tx Tx) error {
		acct, err := tx.AccountForUpdate(ctx, orgID)
		if err != nil {
			return err
		}
		grants, err := tx.ExpiredGrantsForUpdate(ctx, orgID, now)
		if err != nil {
			return err
		}
		available := acct.Available()
		var total int64
		for _, g := range grants {
			take := min(g.Remaining, available)
			if take == g.Remaining {
				if err := tx.MarkGrantExpired(ctx, g.ID, 0, now); err != nil {
					return err
				}
				out.Grants++
			} else {
				if err := tx.SetGrantRemaining(ctx, g.ID, g.Remaining-take); err != nil {
					return err
				}
				out.Deferred += g.Remaining - take
			}
			available -= take
			total += take
			if take > 0 {
				acct, err = tx.UpdateAccount(ctx, orgID, acct.Balance-take, acct.Held)
				if err != nil {
					return err
				}
				gid := g.ID
				if _, err := tx.AppendLedger(ctx, LedgerEntry{
					OrgID: orgID, Kind: LedgerExpire, Amount: -take, HeldDelta: 0,
					BalanceAfter: acct.Balance, HeldAfter: acct.Held, GrantID: &gid, Reference: g.Source, CreatedAt: s.now(),
				}); err != nil {
					return err
				}
			}
		}
		out.Expired = total
		return nil
	})
	return out, err
}

// RecentLedger lists the newest ledger rows of the organization.
func (s *Service) RecentLedger(ctx context.Context, orgID uuid.UUID, limit int) ([]LedgerEntry, error) {
	if s.store == nil {
		return nil, ErrNilStore
	}
	if limit <= 0 {
		limit = 50
	}
	var out []LedgerEntry
	err := s.store.InOrgTx(ctx, orgID, func(ctx context.Context, tx Tx) error {
		rows, err := tx.RecentLedger(ctx, orgID, limit)
		out = rows
		return err
	})
	return out, err
}
