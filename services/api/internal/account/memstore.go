package account

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/mmtmn/docmatch/services/api/internal/email"
)

// MemoryStore is the in-process Store for unit tests (this package and the
// auth package, whose memory store embeds it). It also offers the user and
// organization primitives the auth store needs (lookups by e-mail and
// provider, signup, linking) so both packages share one fake.
type MemoryStore struct {
	mu            sync.Mutex
	now           func() time.Time
	Users         map[uuid.UUID]User
	Orgs          map[uuid.UUID]Org
	Memberships   map[uuid.UUID][]Membership // by org
	Subscriptions map[uuid.UUID]Subscription
	CreditsOf     map[uuid.UUID]CreditSummary
	TenantKeys    map[uuid.UUID][]byte
	Exports       map[uuid.UUID]Export
	Archives      map[uuid.UUID][]byte
	Sessions      map[uuid.UUID][]SessionInfo
	AuditOf       map[uuid.UUID][]AuditEntry
	Emails        []email.Message
	// Err, when set, is returned by every method; FailOn fails only the
	// named method.
	Err    error
	FailOn string
}

// NewMemoryStore returns an empty store; now may be nil.
func NewMemoryStore(now func() time.Time) *MemoryStore {
	if now == nil {
		now = time.Now
	}
	return &MemoryStore{
		now: now, Users: map[uuid.UUID]User{}, Orgs: map[uuid.UUID]Org{}, Memberships: map[uuid.UUID][]Membership{},
		Subscriptions: map[uuid.UUID]Subscription{}, CreditsOf: map[uuid.UUID]CreditSummary{}, TenantKeys: map[uuid.UUID][]byte{},
		Exports: map[uuid.UUID]Export{}, Archives: map[uuid.UUID][]byte{}, Sessions: map[uuid.UUID][]SessionInfo{}, AuditOf: map[uuid.UUID][]AuditEntry{},
	}
}

func (s *MemoryStore) fail(method string) error {
	if s.Err != nil {
		return s.Err
	}
	if s.FailOn == method {
		return errInjected
	}
	return nil
}

type injectedError struct{}

func (injectedError) Error() string { return "account: injected failure" }

var errInjected error = injectedError{}

// Seed creates a user with a personal organization and returns both.
func (s *MemoryStore) Seed(emailAddr, name string) (User, Org) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	u := User{ID: uuid.New(), Email: strings.ToLower(emailAddr), DisplayName: name, CreatedAt: now}
	o := Org{ID: uuid.New(), Name: name, Slug: strings.ToLower(uuid.NewString()), PlanCode: "starter", CreatedAt: now}
	s.Users[u.ID] = u
	s.Orgs[o.ID] = o
	s.Memberships[o.ID] = []Membership{{OrgID: o.ID, UserID: u.ID, Role: RoleOwner, CreatedAt: now}}
	s.TenantKeys[o.ID] = []byte("wrapped")
	return u, o
}

// UserByID implements Store.
func (s *MemoryStore) UserByID(_ context.Context, id uuid.UUID) (User, bool, error) {
	if err := s.fail("UserByID"); err != nil {
		return User{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.Users[id]
	return u, ok, nil
}

// UserByEmail finds an active user by e-mail (case-insensitive).
func (s *MemoryStore) UserByEmail(_ context.Context, emailAddr string) (User, bool, error) {
	if err := s.fail("UserByEmail"); err != nil {
		return User{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, u := range s.Users {
		if strings.EqualFold(u.Email, emailAddr) {
			return u, true, nil
		}
	}
	return User{}, false, nil
}

// UserByProvider finds a user by google or apple subject.
func (s *MemoryStore) UserByProvider(_ context.Context, provider, sub string) (User, bool, error) {
	if err := s.fail("UserByProvider"); err != nil {
		return User{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, u := range s.Users {
		if (provider == "google" && u.GoogleSub == sub) || (provider == "apple" && u.AppleSub == sub) {
			return u, true, nil
		}
	}
	return User{}, false, nil
}

// Signup creates the user, the personal organization, the owner membership
// and the tenant key atomically.
func (s *MemoryStore) Signup(_ context.Context, u User, o Org, wrappedKey []byte) (User, Org, error) {
	if err := s.fail("Signup"); err != nil {
		return User{}, Org{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if u.ID == uuid.Nil {
		u.ID = uuid.New()
	}
	if o.ID == uuid.Nil {
		o.ID = uuid.New()
	}
	u.CreatedAt, o.CreatedAt = now, now
	s.Users[u.ID] = u
	s.Orgs[o.ID] = o
	s.Memberships[o.ID] = []Membership{{OrgID: o.ID, UserID: u.ID, Role: RoleOwner, CreatedAt: now}}
	s.TenantKeys[o.ID] = wrappedKey
	return u, o, nil
}

// LinkProvider stores the provider subject (and marks the e-mail verified).
func (s *MemoryStore) LinkProvider(_ context.Context, userID uuid.UUID, provider, sub string) error {
	if err := s.fail("LinkProvider"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	u := s.Users[userID]
	switch provider {
	case "google":
		u.GoogleSub = sub
	case "apple":
		u.AppleSub = sub
	}
	if u.EmailVerifiedAt == nil {
		now := s.now()
		u.EmailVerifiedAt = &now
	}
	s.Users[userID] = u
	return nil
}

// MarkEmailVerified sets email_verified_at when unset.
func (s *MemoryStore) MarkEmailVerified(_ context.Context, userID uuid.UUID) error {
	if err := s.fail("MarkEmailVerified"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	u := s.Users[userID]
	if u.EmailVerifiedAt == nil {
		now := s.now()
		u.EmailVerifiedAt = &now
		s.Users[userID] = u
	}
	return nil
}

// SetDisplayName sets the name.
func (s *MemoryStore) SetDisplayName(_ context.Context, userID uuid.UUID, name string) error {
	if err := s.fail("SetDisplayName"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	u := s.Users[userID]
	u.DisplayName = name
	s.Users[userID] = u
	return nil
}

// TouchLogin sets last_login_at.
func (s *MemoryStore) TouchLogin(_ context.Context, userID uuid.UUID) error {
	if err := s.fail("TouchLogin"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	u := s.Users[userID]
	now := s.now()
	u.LastLoginAt = &now
	s.Users[userID] = u
	return nil
}

// PrimaryTenant returns the oldest organization the user belongs to.
func (s *MemoryStore) PrimaryTenant(ctx context.Context, userID uuid.UUID) (Tenant, bool, error) {
	if err := s.fail("PrimaryTenant"); err != nil {
		return Tenant{}, false, err
	}
	s.mu.Lock()
	var candidates []Membership
	for _, ms := range s.Memberships {
		for _, m := range ms {
			if m.UserID == userID {
				candidates = append(candidates, m)
			}
		}
	}
	s.mu.Unlock()
	if len(candidates) == 0 {
		return Tenant{}, false, nil
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].CreatedAt.Before(candidates[j].CreatedAt) })
	return s.Tenant(ctx, candidates[0].OrgID, userID)
}

// Tenant implements Store.
func (s *MemoryStore) Tenant(_ context.Context, orgID, userID uuid.UUID) (Tenant, bool, error) {
	if err := s.fail("Tenant"); err != nil {
		return Tenant{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.Orgs[orgID]
	if !ok {
		return Tenant{}, false, nil
	}
	for _, m := range s.Memberships[orgID] {
		if m.UserID == userID {
			t := Tenant{Org: o, Membership: m}
			if sub, ok := s.Subscriptions[orgID]; ok {
				t.Subscription = &sub
			}
			return t, true, nil
		}
	}
	return Tenant{}, false, nil
}

// Credits implements Store.
func (s *MemoryStore) Credits(_ context.Context, orgID uuid.UUID) (CreditSummary, error) {
	if err := s.fail("Credits"); err != nil {
		return CreditSummary{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.CreditsOf[orgID], nil
}

// UpdateProfile implements Store.
func (s *MemoryStore) UpdateProfile(_ context.Context, userID uuid.UUID, p ProfilePatch) error {
	if err := s.fail("UpdateProfile"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	u := s.Users[userID]
	if p.DisplayName != nil {
		u.DisplayName = *p.DisplayName
	}
	if p.Notifications != nil {
		u.Notifications = *p.Notifications
	}
	s.Users[userID] = u
	return nil
}

// UpdateOrgName implements Store.
func (s *MemoryStore) UpdateOrgName(_ context.Context, orgID uuid.UUID, name string) error {
	if err := s.fail("UpdateOrgName"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	o := s.Orgs[orgID]
	o.Name = name
	s.Orgs[orgID] = o
	return nil
}

// SetPassword implements Store.
func (s *MemoryStore) SetPassword(_ context.Context, userID uuid.UUID, hash string) error {
	if err := s.fail("SetPassword"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	u := s.Users[userID]
	u.PasswordHash = hash
	s.Users[userID] = u
	return nil
}

// RequestDeletion implements Store.
func (s *MemoryStore) RequestDeletion(_ context.Context, userID uuid.UUID, scheduledFor time.Time) (bool, error) {
	if err := s.fail("RequestDeletion"); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.Users[userID]
	if !ok || u.DeletionRequestedAt != nil {
		return false, nil
	}
	now := s.now()
	u.DeletionRequestedAt, u.DeletionScheduledFor = &now, &scheduledFor
	s.Users[userID] = u
	return true, nil
}

// DueErasures implements Store.
func (s *MemoryStore) DueErasures(_ context.Context, now time.Time, limit int) ([]uuid.UUID, error) {
	if err := s.fail("DueErasures"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []uuid.UUID
	for id, u := range s.Users {
		if u.DeletionScheduledFor != nil && !u.DeletionScheduledFor.After(now) && len(out) < limit {
			out = append(out, id)
		}
	}
	return out, nil
}

// Erase implements Store.
func (s *MemoryStore) Erase(_ context.Context, userID uuid.UUID, now time.Time) (EraseReport, error) {
	if err := s.fail("Erase"); err != nil {
		return EraseReport{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.Users[userID]
	if !ok || u.DeletionScheduledFor == nil || u.DeletionScheduledFor.After(now) {
		return EraseReport{}, nil
	}
	rep := EraseReport{Erased: true}
	for orgID, ms := range s.Memberships {
		var keep []Membership
		member := false
		for _, m := range ms {
			if m.UserID == userID {
				member = true
			} else {
				keep = append(keep, m)
			}
		}
		if !member {
			continue
		}
		if len(keep) == 0 {
			delete(s.TenantKeys, orgID)
			delete(s.Orgs, orgID)
			delete(s.Memberships, orgID)
			rep.OrgsDeleted = append(rep.OrgsDeleted, orgID)
			continue
		}
		s.Memberships[orgID] = keep
		rep.MembershipsRemoved++
	}
	delete(s.Users, userID)
	return rep, nil
}

// CreateExport implements Store.
func (s *MemoryStore) CreateExport(_ context.Context, userID, orgID uuid.UUID) (Export, error) {
	if err := s.fail("CreateExport"); err != nil {
		return Export{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e := Export{ID: uuid.New(), UserID: userID, OrgID: orgID, Status: ExportPending, RequestedAt: s.now()}
	s.Exports[e.ID] = e
	return e, nil
}

// LatestExport implements Store.
func (s *MemoryStore) LatestExport(_ context.Context, userID uuid.UUID) (Export, bool, error) {
	if err := s.fail("LatestExport"); err != nil {
		return Export{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var latest Export
	found := false
	for _, e := range s.Exports {
		if e.UserID == userID && (!found || e.RequestedAt.After(latest.RequestedAt)) {
			latest, found = e, true
		}
	}
	return latest, found, nil
}

// ExportByID implements Store.
func (s *MemoryStore) ExportByID(_ context.Context, id uuid.UUID) (Export, bool, error) {
	if err := s.fail("ExportByID"); err != nil {
		return Export{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.Exports[id]
	return e, ok, nil
}

// ExportArchive implements Store.
func (s *MemoryStore) ExportArchive(_ context.Context, id uuid.UUID) ([]byte, bool, error) {
	if err := s.fail("ExportArchive"); err != nil {
		return nil, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.Archives[id]
	return a, ok, nil
}

// CompleteExport implements Store.
func (s *MemoryStore) CompleteExport(_ context.Context, id uuid.UUID, archive []byte, expiresAt time.Time) error {
	if err := s.fail("CompleteExport"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.Exports[id]
	now := s.now()
	e.Status, e.ReadyAt, e.ExpiresAt, e.SizeBytes = ExportReady, &now, &expiresAt, int64(len(archive))
	s.Exports[id] = e
	s.Archives[id] = archive
	return nil
}

// FailExport implements Store.
func (s *MemoryStore) FailExport(_ context.Context, id uuid.UUID, reason string) error {
	if err := s.fail("FailExport"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.Exports[id]
	e.Status, e.Error = ExportFailed, reason
	s.Exports[id] = e
	return nil
}

// ExpireExports implements Store.
func (s *MemoryStore) ExpireExports(_ context.Context, now time.Time) (int64, error) {
	if err := s.fail("ExpireExports"); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int64
	for id, e := range s.Exports {
		if e.Status == ExportReady && e.ExpiresAt != nil && !e.ExpiresAt.After(now) {
			e.Status = ExportExpired
			s.Exports[id] = e
			delete(s.Archives, id)
			n++
		}
	}
	return n, nil
}

// ExportBundle implements Store.
func (s *MemoryStore) ExportBundle(ctx context.Context, userID, orgID uuid.UUID) (Bundle, error) {
	if err := s.fail("ExportBundle"); err != nil {
		return Bundle{}, err
	}
	u, ok, err := s.UserByID(ctx, userID)
	if err != nil || !ok {
		return Bundle{}, ErrNotFound
	}
	t, ok, err := s.Tenant(ctx, orgID, userID)
	if err != nil || !ok {
		return Bundle{}, ErrNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return Bundle{User: u, Org: t.Org, Membership: t.Membership, Subscription: t.Subscription, Credits: s.CreditsOf[orgID], Sessions: s.Sessions[userID], Audit: s.AuditOf[orgID]}, nil
}

// EnqueueEmail implements Store.
func (s *MemoryStore) EnqueueEmail(_ context.Context, _ *uuid.UUID, msg email.Message) error {
	if err := s.fail("EnqueueEmail"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Emails = append(s.Emails, msg)
	return nil
}
