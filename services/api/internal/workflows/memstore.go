package workflows

import (
	"context"
	"sort"
	"sync"

	"github.com/google/uuid"
)

// MemStore is the in-memory Store (unit tests).
type MemStore struct {
	mu   sync.Mutex
	rows map[uuid.UUID]Workflow
	runs map[uuid.UUID]int64
}

// NewMemStore returns an empty store.
func NewMemStore() *MemStore {
	return &MemStore{rows: map[uuid.UUID]Workflow{}, runs: map[uuid.UUID]int64{}}
}

// CreateWorkflow implements Store.
func (m *MemStore) CreateWorkflow(_ context.Context, w Workflow) (Workflow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if w.ID == uuid.Nil {
		id, err := uuid.NewV7()
		if err != nil {
			return Workflow{}, err
		}
		w.ID = id
	}
	m.rows[w.ID] = w
	return w, nil
}

// GetWorkflow implements Store.
func (m *MemStore) GetWorkflow(_ context.Context, orgID, id uuid.UUID) (Workflow, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	w, ok := m.rows[id]
	if !ok || w.OrgID != orgID {
		return Workflow{}, false, nil
	}
	return w, true, nil
}

// ListWorkflows implements Store.
func (m *MemStore) ListWorkflows(_ context.Context, orgID uuid.UUID, limit, offset int) ([]Workflow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var all []Workflow
	for _, w := range m.rows {
		if w.OrgID == orgID {
			all = append(all, w)
		}
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].Name != all[j].Name {
			return all[i].Name < all[j].Name
		}
		return all[i].ID.String() < all[j].ID.String()
	})
	if offset >= len(all) {
		return []Workflow{}, nil
	}
	all = all[offset:]
	if len(all) > limit {
		all = all[:limit]
	}
	return all, nil
}

// UpdateWorkflow implements Store.
func (m *MemStore) UpdateWorkflow(_ context.Context, w Workflow) (Workflow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rows[w.ID] = w
	return w, nil
}

// CountRuns implements Store.
func (m *MemStore) CountRuns(_ context.Context, _ uuid.UUID, id uuid.UUID) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.runs[id], nil
}

// SetRuns records a run count (tests).
func (m *MemStore) SetRuns(id uuid.UUID, n int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.runs[id] = n
}
