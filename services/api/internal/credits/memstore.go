package credits

import (
	"context"
	"errors"
	"maps"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
)

// ErrWrongOrg is returned by the memory store when a transaction touches a
// row of another organization, mimicking row-level security.
var ErrWrongOrg = errors.New("credits: row belongs to another organization")

// MemoryStore is an in-process Store with transactional semantics: a
// transaction holds the store lock, works on the live maps and restores a
// snapshot when the callback fails or panics. It backs the unit tests and
// is useful for local tooling; production uses the pgx store.
type MemoryStore struct {
	mu       sync.Mutex
	accounts map[uuid.UUID]Account
	grants   map[uuid.UUID]Grant
	holds    map[uuid.UUID]Hold
	ledger   []LedgerEntry
	seq      map[uuid.UUID]int64
	now      func() time.Time
}

// NewMemoryStore returns an empty store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		accounts: map[uuid.UUID]Account{},
		grants:   map[uuid.UUID]Grant{},
		holds:    map[uuid.UUID]Hold{},
		seq:      map[uuid.UUID]int64{},
		now:      time.Now,
	}
}

type snapshot struct {
	accounts map[uuid.UUID]Account
	grants   map[uuid.UUID]Grant
	holds    map[uuid.UUID]Hold
	ledger   []LedgerEntry
	seq      map[uuid.UUID]int64
}

func (m *MemoryStore) snapshot() snapshot {
	return snapshot{
		accounts: maps.Clone(m.accounts),
		grants:   maps.Clone(m.grants),
		holds:    maps.Clone(m.holds),
		ledger:   slices.Clone(m.ledger),
		seq:      maps.Clone(m.seq),
	}
}

func (m *MemoryStore) restore(s snapshot) {
	m.accounts, m.grants, m.holds, m.ledger, m.seq = s.accounts, s.grants, s.holds, s.ledger, s.seq
}

// InOrgTx implements Store.
func (m *MemoryStore) InOrgTx(ctx context.Context, orgID uuid.UUID, fn func(ctx context.Context, tx Tx) error) (err error) {
	if orgID == uuid.Nil {
		return errors.New("credits: org_id vazio")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	snap := m.snapshot()
	defer func() {
		if p := recover(); p != nil {
			m.restore(snap)
			panic(p)
		}
		if err != nil {
			m.restore(snap)
		}
	}()
	return fn(ctx, &memTx{store: m, org: orgID})
}

// Snapshot returns copies of the current state (tests and invariants).
func (m *MemoryStore) Snapshot() (map[uuid.UUID]Account, map[uuid.UUID]Grant, map[uuid.UUID]Hold, []LedgerEntry) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.snapshot()
	return s.accounts, s.grants, s.holds, s.ledger
}

type memTx struct {
	store *MemoryStore
	org   uuid.UUID
}

func (t *memTx) check(org uuid.UUID) error {
	if org != t.org {
		return ErrWrongOrg
	}
	return nil
}

func (t *memTx) Account(_ context.Context, orgID uuid.UUID) (Account, bool, error) {
	if err := t.check(orgID); err != nil {
		return Account{}, false, err
	}
	a, ok := t.store.accounts[orgID]
	return a, ok, nil
}

func (t *memTx) AccountForUpdate(_ context.Context, orgID uuid.UUID) (Account, error) {
	if err := t.check(orgID); err != nil {
		return Account{}, err
	}
	a, ok := t.store.accounts[orgID]
	if !ok {
		a = Account{OrgID: orgID, UpdatedAt: t.store.now()}
		t.store.accounts[orgID] = a
	}
	return a, nil
}

func (t *memTx) UpdateAccount(_ context.Context, orgID uuid.UUID, balance, held int64) (Account, error) {
	if err := t.check(orgID); err != nil {
		return Account{}, err
	}
	if held < 0 || balance < held {
		return Account{}, errors.New("credits: check constraint balance >= held >= 0 violated")
	}
	a := Account{OrgID: orgID, Balance: balance, Held: held, UpdatedAt: t.store.now()}
	t.store.accounts[orgID] = a
	return a, nil
}

func (t *memTx) GrantByIdempotencyKey(_ context.Context, orgID uuid.UUID, key string) (Grant, bool, error) {
	if err := t.check(orgID); err != nil {
		return Grant{}, false, err
	}
	for _, g := range t.store.grants {
		if g.OrgID == orgID && g.IdempotencyKey == key {
			return g, true, nil
		}
	}
	return Grant{}, false, nil
}

func (t *memTx) InsertGrant(_ context.Context, g Grant) (Grant, error) {
	if err := t.check(g.OrgID); err != nil {
		return Grant{}, err
	}
	if g.Amount <= 0 || g.Remaining < 0 || g.Remaining > g.Amount {
		return Grant{}, errors.New("credits: grant check constraint violated")
	}
	if g.IdempotencyKey != "" {
		for _, other := range t.store.grants {
			if other.OrgID == g.OrgID && other.IdempotencyKey == g.IdempotencyKey {
				return Grant{}, errors.New("credits: duplicate idempotency key")
			}
		}
	}
	g.ID = uuid.New()
	if g.GrantedAt.IsZero() {
		g.GrantedAt = t.store.now()
	}
	t.store.grants[g.ID] = g
	return g, nil
}

func (t *memTx) openGrants(orgID uuid.UUID, filter func(Grant) bool) []Grant {
	var out []Grant
	for _, g := range t.store.grants {
		if g.OrgID == orgID && g.ExpiredAt == nil && g.Remaining > 0 && filter(g) {
			out = append(out, g)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		switch {
		case a.ExpiresAt == nil && b.ExpiresAt != nil:
			return false
		case a.ExpiresAt != nil && b.ExpiresAt == nil:
			return true
		case a.ExpiresAt != nil && b.ExpiresAt != nil && !a.ExpiresAt.Equal(*b.ExpiresAt):
			return a.ExpiresAt.Before(*b.ExpiresAt)
		}
		if !a.GrantedAt.Equal(b.GrantedAt) {
			return a.GrantedAt.Before(b.GrantedAt)
		}
		return a.ID.String() < b.ID.String()
	})
	return out
}

func (t *memTx) OpenGrantsForUpdate(_ context.Context, orgID uuid.UUID) ([]Grant, error) {
	if err := t.check(orgID); err != nil {
		return nil, err
	}
	return t.openGrants(orgID, func(Grant) bool { return true }), nil
}

func (t *memTx) ExpiredGrantsForUpdate(_ context.Context, orgID uuid.UUID, now time.Time) ([]Grant, error) {
	if err := t.check(orgID); err != nil {
		return nil, err
	}
	return t.openGrants(orgID, func(g Grant) bool { return g.ExpiresAt != nil && !g.ExpiresAt.After(now) }), nil
}

func (t *memTx) SetGrantRemaining(_ context.Context, grantID uuid.UUID, remaining int64) error {
	g, ok := t.store.grants[grantID]
	if !ok || g.OrgID != t.org {
		return ErrWrongOrg
	}
	if remaining < 0 || remaining > g.Amount {
		return errors.New("credits: grant check constraint violated")
	}
	g.Remaining = remaining
	t.store.grants[grantID] = g
	return nil
}

func (t *memTx) MarkGrantExpired(_ context.Context, grantID uuid.UUID, remaining int64, at time.Time) error {
	if err := t.SetGrantRemaining(context.Background(), grantID, remaining); err != nil {
		return err
	}
	g := t.store.grants[grantID]
	at = at.UTC()
	g.ExpiredAt = &at
	t.store.grants[grantID] = g
	return nil
}

func (t *memTx) InsertHold(_ context.Context, h Hold) (Hold, error) {
	if err := t.check(h.OrgID); err != nil {
		return Hold{}, err
	}
	if h.Amount <= 0 {
		return Hold{}, errors.New("credits: hold check constraint violated")
	}
	h.ID = uuid.New()
	h.Status = HoldHeld
	if h.CreatedAt.IsZero() {
		h.CreatedAt = t.store.now()
	}
	t.store.holds[h.ID] = h
	return h, nil
}

func (t *memTx) HoldForUpdate(_ context.Context, orgID, holdID uuid.UUID) (Hold, bool, error) {
	if err := t.check(orgID); err != nil {
		return Hold{}, false, err
	}
	h, ok := t.store.holds[holdID]
	if !ok || h.OrgID != orgID {
		return Hold{}, false, nil
	}
	return h, true, nil
}

func (t *memTx) CloseHold(_ context.Context, orgID, holdID uuid.UUID, status HoldStatus, settled *int64) (Hold, error) {
	if err := t.check(orgID); err != nil {
		return Hold{}, err
	}
	h, ok := t.store.holds[holdID]
	if !ok || h.OrgID != orgID || h.Status != HoldHeld {
		return Hold{}, ErrHoldClosed
	}
	if settled != nil && (*settled < 0 || *settled > h.Amount) {
		return Hold{}, errors.New("credits: hold check constraint violated")
	}
	h.Status = status
	h.SettledAmount = settled
	now := t.store.now()
	h.ClosedAt = &now
	t.store.holds[holdID] = h
	return h, nil
}

func (t *memTx) AppendLedger(_ context.Context, e LedgerEntry) (LedgerEntry, error) {
	if err := t.check(e.OrgID); err != nil {
		return LedgerEntry{}, err
	}
	e.ID = uuid.New()
	t.store.seq[e.OrgID]++
	e.Seq = t.store.seq[e.OrgID]
	if e.CreatedAt.IsZero() {
		e.CreatedAt = t.store.now()
	}
	t.store.ledger = append(t.store.ledger, e)
	return e, nil
}

func (t *memTx) RecentLedger(_ context.Context, orgID uuid.UUID, limit int) ([]LedgerEntry, error) {
	if err := t.check(orgID); err != nil {
		return nil, err
	}
	var out []LedgerEntry
	for _, e := range t.store.ledger {
		if e.OrgID == orgID {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seq > out[j].Seq })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
