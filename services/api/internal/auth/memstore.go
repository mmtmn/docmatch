package auth

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/mmtmn/docmatch/services/api/internal/account"
	"github.com/mmtmn/docmatch/services/api/internal/email"
)

// MemoryStore is the in-process Store for unit tests: the account memory
// store for users and organizations plus the e-mail challenges.
type MemoryStore struct {
	*account.MemoryStore
	mu         sync.Mutex
	now        func() time.Time
	Challenges map[uuid.UUID]Challenge
	Sent       []email.Message
	// FailChallenge names a challenge method that must fail.
	FailChallenge string
}

// NewMemoryStore returns an empty store; now may be nil.
func NewMemoryStore(now func() time.Time) *MemoryStore {
	if now == nil {
		now = time.Now
	}
	return &MemoryStore{MemoryStore: account.NewMemoryStore(now), now: now, Challenges: map[uuid.UUID]Challenge{}}
}

func (s *MemoryStore) failing(method string) error {
	if s.Err != nil {
		return s.Err
	}
	if s.FailChallenge == method {
		return errInjected
	}
	return nil
}

type injectedError struct{}

func (injectedError) Error() string { return "auth: injected failure" }

var errInjected error = injectedError{}

// CreateChallenge implements Store.
func (s *MemoryStore) CreateChallenge(_ context.Context, ch Challenge, msg email.Message) error {
	if err := s.failing("CreateChallenge"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Challenges[ch.ID] = ch
	s.Sent = append(s.Sent, msg)
	return nil
}

// ChallengeByID implements Store.
func (s *MemoryStore) ChallengeByID(_ context.Context, id uuid.UUID) (Challenge, bool, error) {
	if err := s.failing("ChallengeByID"); err != nil {
		return Challenge{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ch, ok := s.Challenges[id]
	return ch, ok, nil
}

// RecordChallengeAttempt implements Store.
func (s *MemoryStore) RecordChallengeAttempt(_ context.Context, id uuid.UUID) (int, error) {
	if err := s.failing("RecordChallengeAttempt"); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ch := s.Challenges[id]
	ch.Attempts++
	s.Challenges[id] = ch
	return ch.Attempts, nil
}

// ConsumeChallenge implements Store.
func (s *MemoryStore) ConsumeChallenge(_ context.Context, id uuid.UUID) (Challenge, bool, error) {
	if err := s.failing("ConsumeChallenge"); err != nil {
		return Challenge{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ch, ok := s.Challenges[id]
	now := s.now()
	if !ok || ch.ConsumedAt != nil || !now.Before(ch.ExpiresAt) {
		return Challenge{}, false, nil
	}
	ch.ConsumedAt = &now
	s.Challenges[id] = ch
	return ch, true, nil
}
