package audit

import (
	"context"
	"errors"
	"slices"
	"sort"
	"sync"

	"github.com/google/uuid"
)

// MemoryStore is an in-process Store for tests and tooling. Appends are
// serialized by a mutex (one lock for every chain, which is stricter than
// PostgreSQL's per-chain advisory lock but equivalent for correctness).
type MemoryStore struct {
	mu      sync.Mutex
	entries []Entry
}

// NewMemoryStore returns an empty store.
func NewMemoryStore() *MemoryStore { return &MemoryStore{} }

// InChainTx implements Store.
func (m *MemoryStore) InChainTx(ctx context.Context, orgID *uuid.UUID, fn func(ctx context.Context, tx Tx) error) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	snap := slices.Clone(m.entries)
	defer func() {
		if p := recover(); p != nil {
			m.entries = snap
			panic(p)
		}
		if err != nil {
			m.entries = snap
		}
	}()
	return fn(ctx, &memTx{store: m, chain: ChainID(orgID)})
}

// Range implements Store.
func (m *MemoryStore) Range(_ context.Context, orgID *uuid.UUID, from, to int64) ([]Entry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	chain := ChainID(orgID)
	var out []Entry
	for _, e := range m.entries {
		if e.ChainID == chain && e.Seq >= from && e.Seq <= to {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out, nil
}

// Tamper edits the entry with the given chain and seq in place (tests). It
// returns false when no such entry exists.
func (m *MemoryStore) Tamper(orgID *uuid.UUID, seq int64, fn func(e *Entry)) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	chain := ChainID(orgID)
	for i := range m.entries {
		if m.entries[i].ChainID == chain && m.entries[i].Seq == seq {
			fn(&m.entries[i])
			return true
		}
	}
	return false
}

// Remove deletes the entry with the given chain and seq (tests).
func (m *MemoryStore) Remove(orgID *uuid.UUID, seq int64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	chain := ChainID(orgID)
	for i := range m.entries {
		if m.entries[i].ChainID == chain && m.entries[i].Seq == seq {
			m.entries = slices.Delete(m.entries, i, i+1)
			return true
		}
	}
	return false
}

// Len returns the number of stored entries.
func (m *MemoryStore) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.entries)
}

type memTx struct {
	store *MemoryStore
	chain uuid.UUID
}

func (t *memTx) Tail(_ context.Context, chainID uuid.UUID) (int64, []byte, bool, error) {
	if chainID != t.chain {
		return 0, nil, false, errors.New("audit: transaction is scoped to another chain")
	}
	var best *Entry
	for i := range t.store.entries {
		e := &t.store.entries[i]
		if e.ChainID == chainID && (best == nil || e.Seq > best.Seq) {
			best = e
		}
	}
	if best == nil {
		return 0, nil, false, nil
	}
	return best.Seq, best.Hash, true, nil
}

func (t *memTx) Canonical(_ context.Context, meta []byte) (string, error) {
	return CanonicalJSON(meta)
}

func (t *memTx) Append(_ context.Context, e Entry) (Entry, error) {
	if e.ChainID != t.chain {
		return Entry{}, errors.New("audit: transaction is scoped to another chain")
	}
	if e.Seq <= 0 || len(e.Hash) != 32 {
		return Entry{}, errors.New("audit: invalid entry")
	}
	e.ID = uuid.New()
	t.store.entries = append(t.store.entries, e)
	return e, nil
}
