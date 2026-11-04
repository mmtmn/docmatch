package session

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
)

// MemoryStore is the in-process Store used by unit tests of this package and
// of the packages that depend on sessions.
type MemoryStore struct {
	mu   sync.Mutex
	rows map[uuid.UUID]Session
	now  func() time.Time
	// Err, when set, is returned by every method (failure injection).
	Err error
}

// NewMemoryStore returns an empty store; now may be nil.
func NewMemoryStore(now func() time.Time) *MemoryStore {
	if now == nil {
		now = time.Now
	}
	return &MemoryStore{rows: map[uuid.UUID]Session{}, now: now}
}

// Insert implements Store.
func (s *MemoryStore) Insert(_ context.Context, row Session) error {
	if s.Err != nil {
		return s.Err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows[row.ID] = row
	return nil
}

// ByID implements Store.
func (s *MemoryStore) ByID(_ context.Context, id uuid.UUID) (Session, bool, error) {
	if s.Err != nil {
		return Session{}, false, s.Err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	row, ok := s.rows[id]
	return row, ok, nil
}

// Rotate implements Store.
func (s *MemoryStore) Rotate(_ context.Context, id uuid.UUID, r Rotation) (Session, bool, error) {
	if s.Err != nil {
		return Session{}, false, s.Err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	row, ok := s.rows[id]
	if !ok || row.RevokedAt != nil {
		return Session{}, false, nil
	}
	now := s.now()
	row.RefreshHash, row.AccessHash = r.RefreshHash, r.AccessHash
	row.ExpiresAt, row.AccessExpiresAt = r.ExpiresAt, r.AccessExpiresAt
	row.LastSeenAt, row.RotatedAt, row.Rotations = now, &now, row.Rotations+1
	s.rows[id] = row
	return row, true, nil
}

// Touch implements Store.
func (s *MemoryStore) Touch(_ context.Context, id uuid.UUID) error {
	if s.Err != nil {
		return s.Err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if row, ok := s.rows[id]; ok {
		row.LastSeenAt = s.now()
		s.rows[id] = row
	}
	return nil
}

func (s *MemoryStore) revokeWhere(match func(Session) bool) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	var n int64
	for id, row := range s.rows {
		if row.RevokedAt == nil && match(row) {
			row.RevokedAt = &now
			s.rows[id] = row
			n++
		}
	}
	return n
}

// Revoke implements Store.
func (s *MemoryStore) Revoke(_ context.Context, id uuid.UUID) (int64, error) {
	if s.Err != nil {
		return 0, s.Err
	}
	return s.revokeWhere(func(r Session) bool { return r.ID == id }), nil
}

// RevokeFamily implements Store.
func (s *MemoryStore) RevokeFamily(_ context.Context, familyID uuid.UUID) (int64, error) {
	if s.Err != nil {
		return 0, s.Err
	}
	return s.revokeWhere(func(r Session) bool { return r.FamilyID == familyID }), nil
}

// RevokeUser implements Store.
func (s *MemoryStore) RevokeUser(_ context.Context, userID, keep uuid.UUID) (int64, error) {
	if s.Err != nil {
		return 0, s.Err
	}
	return s.revokeWhere(func(r Session) bool { return r.UserID == userID && r.ID != keep }), nil
}

// ListActive implements Store.
func (s *MemoryStore) ListActive(_ context.Context, userID uuid.UUID) ([]Session, error) {
	if s.Err != nil {
		return nil, s.Err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	out := []Session{}
	for _, row := range s.rows {
		if row.UserID == userID && row.RevokedAt == nil && now.Before(row.ExpiresAt) {
			out = append(out, row)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastSeenAt.After(out[j].LastSeenAt) })
	return out, nil
}

// Len reports how many rows exist (tests).
func (s *MemoryStore) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.rows)
}
