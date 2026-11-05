package uploads

import (
	"context"
	"sync"

	"github.com/google/uuid"
)

// MemStore is the in-memory Store (unit tests and the fake harness tooling).
type MemStore struct {
	mu   sync.Mutex
	rows map[uuid.UUID]Upload
}

// NewMemStore returns an empty store.
func NewMemStore() *MemStore { return &MemStore{rows: map[uuid.UUID]Upload{}} }

// CreateUpload implements Store.
func (m *MemStore) CreateUpload(_ context.Context, u Upload) (Upload, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rows[u.ID] = u
	return u, nil
}

// GetUpload implements Store.
func (m *MemStore) GetUpload(_ context.Context, orgID, id uuid.UUID) (Upload, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.rows[id]
	if !ok || u.OrgID != orgID {
		return Upload{}, false, nil
	}
	return u, true, nil
}

// ListUploads implements Store.
func (m *MemStore) ListUploads(_ context.Context, orgID uuid.UUID, ids []uuid.UUID) ([]Upload, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Upload, 0, len(ids))
	for _, id := range ids {
		if u, ok := m.rows[id]; ok && u.OrgID == orgID {
			out = append(out, u)
		}
	}
	return out, nil
}

// UpdateUpload implements Store.
func (m *MemStore) UpdateUpload(_ context.Context, u Upload) (Upload, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rows[u.ID] = u
	return u, nil
}

// GetUploadAny implements Store.
func (m *MemStore) GetUploadAny(_ context.Context, id uuid.UUID) (Upload, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.rows[id]
	return u, ok, nil
}
