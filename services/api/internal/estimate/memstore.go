package estimate

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
)

// MemStore is the in-memory Store (unit tests and the fake harness tooling).
type MemStore struct {
	mu       sync.Mutex
	quotes   map[uuid.UUID]Record
	prices   []ModelPrice
	overruns []Overrun
	timings  []RunTiming
	inFlight int64
}

// NewMemStore returns an empty store.
func NewMemStore() *MemStore { return &MemStore{quotes: map[uuid.UUID]Record{}} }

// InsertQuote implements Store.
func (m *MemStore) InsertQuote(_ context.Context, rec Record) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.quotes[rec.ID] = rec
	return nil
}

// GetQuote implements Store.
func (m *MemStore) GetQuote(_ context.Context, orgID, id uuid.UUID) (Record, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.quotes[id]
	if !ok || rec.OrgID != orgID {
		return Record{}, false, nil
	}
	return rec, true, nil
}

// SetQuoteStatus implements Store.
func (m *MemStore) SetQuoteStatus(_ context.Context, orgID, id uuid.UUID, status string, runID *uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.quotes[id]
	if !ok || rec.OrgID != orgID {
		return nil
	}
	rec.Status = status
	if runID != nil {
		rec.RunID = runID
	}
	m.quotes[id] = rec
	return nil
}

// LatestModelPrices implements Store.
func (m *MemStore) LatestModelPrices(context.Context) ([]ModelPrice, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]ModelPrice(nil), m.prices...), nil
}

// SeedModelPrices implements Store.
func (m *MemStore) SeedModelPrices(_ context.Context, rows []ModelPrice) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.prices) == 0 {
		m.prices = append([]ModelPrice(nil), rows...)
	}
	return nil
}

// RecentOverruns implements Store.
func (m *MemStore) RecentOverruns(_ context.Context, _ time.Time, limit int) ([]Overrun, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if limit > 0 && len(m.overruns) > limit {
		return append([]Overrun(nil), m.overruns[:limit]...), nil
	}
	return append([]Overrun(nil), m.overruns...), nil
}

// AddOverruns records finished-run samples (tests).
func (m *MemStore) AddOverruns(rows ...Overrun) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.overruns = append(m.overruns, rows...)
}

// RecentTimings implements Store: completed runs since a time, newest first.
func (m *MemStore) RecentTimings(_ context.Context, since time.Time, limit int) ([]TimingSample, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rows := append([]RunTiming(nil), m.timings...)
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].FinishedAt.After(rows[j].FinishedAt) })
	out := []TimingSample{}
	for _, t := range rows {
		if t.Status != TimingCompleted || !t.FinishedAt.After(since) {
			continue
		}
		if limit > 0 && len(out) >= limit {
			break
		}
		out = append(out, TimingSample{Units: t.Units, ActiveSeconds: t.ActiveSeconds})
	}
	return out, nil
}

// InsertTiming implements Store (once per run).
func (m *MemStore) InsertTiming(_ context.Context, t RunTiming) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, row := range m.timings {
		if row.RunID == t.RunID {
			return nil
		}
	}
	m.timings = append(m.timings, t)
	return nil
}

// RunsInFlight implements Store.
func (m *MemStore) RunsInFlight(context.Context, time.Time) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.inFlight, nil
}

// SetRunsInFlight sets what RunsInFlight answers (tests).
func (m *MemStore) SetRunsInFlight(n int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.inFlight = n
}

// Timings returns a snapshot of the stored run timings (tests).
func (m *MemStore) Timings() []RunTiming {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]RunTiming(nil), m.timings...)
}

// Quotes returns a snapshot of the stored quotes (tests).
func (m *MemStore) Quotes() []Record {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Record, 0, len(m.quotes))
	for _, r := range m.quotes {
		out = append(out, r)
	}
	return out
}
