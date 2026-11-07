package rules

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// MemStore is the in-memory Store (unit tests).
type MemStore struct {
	mu       sync.Mutex
	rows     map[uuid.UUID]Rule
	versions []Version
}

// NewMemStore returns an empty store.
func NewMemStore() *MemStore { return &MemStore{rows: map[uuid.UUID]Rule{}} }

// CreateRule implements Store.
func (m *MemStore) CreateRule(_ context.Context, r Rule) (Rule, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r.ID == uuid.Nil {
		id, err := uuid.NewV7()
		if err != nil {
			return Rule{}, err
		}
		r.ID = id
	}
	m.rows[r.ID] = r
	return r, nil
}

// GetRule implements Store.
func (m *MemStore) GetRule(ctx context.Context, orgID, id uuid.UUID) (Rule, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rows[id]
	if !ok || r.OrgID != orgID || !visible(ctx, orgID, r) {
		return Rule{}, false, nil
	}
	return r, true, nil
}

// ListRulesByIDs implements Store.
func (m *MemStore) ListRulesByIDs(ctx context.Context, orgID uuid.UUID, ids []uuid.UUID) ([]Rule, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []Rule{}
	for _, id := range ids {
		if r, ok := m.rows[id]; ok && r.OrgID == orgID && visible(ctx, orgID, r) {
			out = append(out, r)
		}
	}
	return out, nil
}

// SearchRules implements Store with a substring match standing in for the trigram index.
func (m *MemStore) SearchRules(ctx context.Context, orgID uuid.UUID, q Query) ([]Rule, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var all []Rule
	term := strings.ToLower(q.Term)
	for _, r := range m.rows {
		if r.OrgID != orgID || !visible(ctx, orgID, r) {
			continue
		}
		if q.Status != "" && r.Status != q.Status {
			continue
		}
		if q.Scope != "" && r.Scope != q.Scope {
			continue
		}
		if term != "" && !strings.Contains(strings.ToLower(r.Title), term) && !strings.Contains(strings.ToLower(r.Body), term) {
			continue
		}
		all = append(all, r)
	}
	sort.Slice(all, func(i, j int) bool {
		li, lj := all[i].LastUsedAt, all[j].LastUsedAt
		switch {
		case li != nil && lj != nil && !li.Equal(*lj):
			return li.After(*lj)
		case li != nil && lj == nil:
			return true
		case li == nil && lj != nil:
			return false
		}
		if !all[i].CreatedAt.Equal(all[j].CreatedAt) {
			return all[i].CreatedAt.After(all[j].CreatedAt)
		}
		return all[i].ID.String() < all[j].ID.String()
	})
	if q.Offset >= len(all) {
		return []Rule{}, nil
	}
	all = all[q.Offset:]
	if q.Limit > 0 && len(all) > q.Limit {
		all = all[:q.Limit]
	}
	if all == nil {
		all = []Rule{}
	}
	return all, nil
}

// ListTrustedRules implements Store.
func (m *MemStore) ListTrustedRules(ctx context.Context, orgID uuid.UUID) ([]Rule, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Rule
	for _, r := range m.rows {
		if r.OrgID == orgID && visible(ctx, orgID, r) && r.Status == StatusTrusted && r.Enabled {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

// UpdateRuleVersion implements Store.
func (m *MemStore) UpdateRuleVersion(_ context.Context, r Rule) (Rule, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rows[r.ID] = r
	return r, nil
}

// PromoteRule implements Store.
func (m *MemStore) PromoteRule(_ context.Context, orgID, id uuid.UUID, status string) (Rule, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.rows[id]
	if r.OrgID != orgID {
		return Rule{}, nil
	}
	now := time.Now().UTC()
	r.Status, r.UpdatedAt = status, now
	m.rows[id] = r
	return r, nil
}

// InsertVersion implements Store.
func (m *MemStore) InsertVersion(_ context.Context, v Version) (Version, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if v.ID == uuid.Nil {
		v.ID = uuid.New()
	}
	m.versions = append(m.versions, v)
	return v, nil
}

// TouchRules implements Store.
func (m *MemStore) TouchRules(_ context.Context, orgID uuid.UUID, ids []uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now().UTC()
	for _, id := range ids {
		if r, ok := m.rows[id]; ok && r.OrgID == orgID {
			r.UseCount++
			r.LastUsedAt = &now
			m.rows[id] = r
		}
	}
	return nil
}

// Versions returns the recorded versions (tests).
func (m *MemStore) Versions() []Version {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Version(nil), m.versions...)
}
