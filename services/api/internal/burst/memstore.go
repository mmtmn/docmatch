package burst

import (
	"bytes"
	"context"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
)

// MemStore is an in-memory Store for tests. Its Now plays the database's
// clock: every lease, expiry and age is computed with it.
type MemStore struct {
	// Now is the store's clock (time.Now when nil).
	Now func() time.Time

	mu        sync.Mutex
	jobs      map[uuid.UUID]*Job
	events    map[uuid.UUID][]Event
	instances map[string]*memInstance
	failures  int
	unhealthy *time.Time
}

type memInstance struct {
	Instance
	first, last time.Time
	contact     *Contact
}

// NewMemStore returns an empty store.
func NewMemStore() *MemStore {
	return &MemStore{jobs: map[uuid.UUID]*Job{}, events: map[uuid.UUID][]Event{}, instances: map[string]*memInstance{}}
}

func (m *MemStore) now() time.Time {
	if m.Now == nil {
		return time.Now()
	}
	return m.Now()
}

func clone(j Job) Job {
	j.Request = slices.Clone(j.Request)
	j.TokenHash = slices.Clone(j.TokenHash)
	return j
}

func ptr(t time.Time) *time.Time { return &t }

// CreateJob implements Store.
func (m *MemStore) CreateJob(_ context.Context, j Job, maxLive int) (Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	live := 0
	for _, other := range m.jobs {
		if Terminal(other.State) {
			continue
		}
		if other.RunID == j.RunID {
			return Job{}, ErrJobActive
		}
		live++
	}
	if maxLive > 0 && live >= maxLive {
		return Job{}, ErrPoolFull
	}
	j.State, j.CreatedAt = StateQueued, m.now()
	c := clone(j)
	m.jobs[j.ID] = &c
	return clone(c), nil
}

// ActiveJob implements Store.
func (m *MemStore) ActiveJob(_ context.Context, runID uuid.UUID) (Job, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, j := range m.jobs {
		if j.RunID == runID && !Terminal(j.State) {
			return clone(*j), true, nil
		}
	}
	return Job{}, false, nil
}

// LatestJob implements Store.
func (m *MemStore) LatestJob(_ context.Context, runID uuid.UUID) (Job, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var latest *Job
	for _, j := range m.jobs {
		if j.RunID == runID && (latest == nil || !j.CreatedAt.Before(latest.CreatedAt)) {
			latest = j
		}
	}
	if latest == nil {
		return Job{}, false, nil
	}
	return clone(*latest), true, nil
}

// GetJob implements Store.
func (m *MemStore) GetJob(_ context.Context, id uuid.UUID) (Job, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if !ok {
		return Job{}, false, nil
	}
	return clone(*j), true, nil
}

// Claim implements Store.
func (m *MemStore) Claim(_ context.Context, instanceID string, tokenHash []byte, lease time.Duration) (Job, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var oldest *Job
	for _, j := range m.jobs {
		if j.State == StateQueued && (oldest == nil || j.CreatedAt.Before(oldest.CreatedAt)) {
			oldest = j
		}
	}
	if oldest == nil {
		return Job{}, false, nil
	}
	now := m.now()
	oldest.State, oldest.InstanceID, oldest.TokenHash = StateClaimed, instanceID, slices.Clone(tokenHash)
	oldest.ClaimedAt, oldest.LeaseExpiresAt = ptr(now), ptr(now.Add(lease))
	return clone(*oldest), true, nil
}

func (m *MemStore) claimed(id uuid.UUID, tokenHash []byte) *Job {
	j, ok := m.jobs[id]
	if !ok || j.State != StateClaimed || !bytes.Equal(j.TokenHash, tokenHash) {
		return nil
	}
	return j
}

// Extend implements Store.
func (m *MemStore) Extend(_ context.Context, id uuid.UUID, tokenHash []byte, lease time.Duration) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j := m.claimed(id, tokenHash)
	now := m.now()
	if j == nil || j.LeaseExpiresAt == nil || j.LeaseExpiresAt.Before(now) {
		return false, nil
	}
	j.LeaseExpiresAt = ptr(now.Add(lease))
	return true, nil
}

// Finish implements Store.
func (m *MemStore) Finish(_ context.Context, id uuid.UUID, tokenHash []byte, state, reason string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j := m.claimed(id, tokenHash)
	if j == nil {
		return false, nil
	}
	j.State, j.Reason, j.FinishedAt = state, reason, ptr(m.now())
	return true, nil
}

func (m *MemStore) move(j *Job, from []string, to, reason string) bool {
	if j == nil || !slices.Contains(from, j.State) {
		return false
	}
	j.State, j.Reason, j.FinishedAt = to, reason, ptr(m.now())
	return true
}

// Abandon implements Store.
func (m *MemStore) Abandon(_ context.Context, id uuid.UUID, wait time.Duration) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j := m.jobs[id]
	if j == nil || m.now().Sub(j.CreatedAt) < wait {
		return false, nil
	}
	return m.move(j, []string{StateQueued}, StateAbandoned, "nenhum trabalhador a tempo"), nil
}

// Cancel implements Store.
func (m *MemStore) Cancel(_ context.Context, id uuid.UUID, reason string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.move(m.jobs[id], []string{StateQueued, StateClaimed}, StateCancelled, reason), nil
}

// CancelRun implements Store.
func (m *MemStore) CancelRun(_ context.Context, runID uuid.UUID, reason string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	done := false
	for _, j := range m.jobs {
		if j.RunID == runID {
			done = m.move(j, []string{StateQueued, StateClaimed}, StateCancelled, reason) || done
		}
	}
	return done, nil
}

// ExpireLeases implements Store.
func (m *MemStore) ExpireLeases(context.Context) ([]uuid.UUID, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	var out []uuid.UUID
	for id, j := range m.jobs {
		if j.State == StateClaimed && j.LeaseExpiresAt != nil && j.LeaseExpiresAt.Before(now) {
			j.State, j.Reason, j.FinishedAt = StateLost, "concessão expirada", ptr(now)
			out = append(out, id)
		}
	}
	return out, nil
}

// AppendEvents implements Store.
func (m *MemStore) AppendEvents(_ context.Context, job Job, events []Event, quota EventQuota) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	j := m.jobs[job.ID]
	if j == nil {
		return nil
	}
	cur := m.events[job.ID]
	var addCount, addBytes int64
	var fresh []Event
	for _, e := range events {
		if slices.ContainsFunc(cur, func(x Event) bool { return x.Seq == e.Seq }) || slices.ContainsFunc(fresh, func(x Event) bool { return x.Seq == e.Seq }) {
			continue
		}
		fresh = append(fresh, Event{Seq: e.Seq, Data: slices.Clone(e.Data)})
		addCount++
		addBytes += int64(len(e.Data))
	}
	if (quota.MaxEvents > 0 && j.EventsCount+addCount > quota.MaxEvents) || (quota.MaxBytes > 0 && j.EventsBytes+addBytes > quota.MaxBytes) {
		return ErrEventQuota
	}
	cur = append(cur, fresh...)
	sort.Slice(cur, func(a, b int) bool { return cur[a].Seq < cur[b].Seq })
	m.events[job.ID] = cur
	j.EventsCount += addCount
	j.EventsBytes += addBytes
	return nil
}

// Events implements Store.
func (m *MemStore) Events(_ context.Context, jobID uuid.UUID, afterSeq int64, limit int, maxBytes int64) ([]Event, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Event
	var size int64
	for _, e := range m.events[jobID] {
		if e.Seq <= afterSeq {
			continue
		}
		if len(out) >= limit || (len(out) > 0 && size+int64(len(e.Data)) > maxBytes) {
			break
		}
		out = append(out, e)
		size += int64(len(e.Data))
	}
	return out, nil
}

// TouchInstance implements Store.
func (m *MemStore) TouchInstance(_ context.Context, in Instance) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	cur, ok := m.instances[in.ID]
	if !ok {
		m.instances[in.ID] = &memInstance{Instance: in, first: now, last: now}
		return nil
	}
	if now.After(cur.last) {
		cur.last = now
	}
	if in.Type != "" {
		cur.Type = in.Type
	}
	if in.Lifecycle != "" {
		cur.Lifecycle = in.Lifecycle
	}
	return nil
}

// InstanceSeen implements Store.
func (m *MemStore) InstanceSeen(_ context.Context, id string, within time.Duration) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	in, ok := m.instances[id]
	return ok && !in.last.Before(m.now().Add(-within)), nil
}

// NoteContact implements Store.
func (m *MemStore) NoteContact(ctx context.Context, c Contact, register bool) error {
	m.mu.Lock()
	_, ok := m.instances[c.InstanceID]
	m.mu.Unlock()
	if !ok && register {
		if err := m.TouchInstance(ctx, Instance{ID: c.InstanceID}); err != nil {
			return err
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if in, ok := m.instances[c.InstanceID]; ok {
		cc := c
		in.contact = &cc
	}
	return nil
}

// LastContact returns what an instance last reported (tests).
func (m *MemStore) LastContact(id string) (Contact, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	in, ok := m.instances[id]
	if !ok || in.contact == nil {
		return Contact{}, false
	}
	return *in.contact, true
}

// Status implements Store.
func (m *MemStore) Status(_ context.Context, q StatusQuery) (PoolStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	st := PoolStatus{UnhealthyUntil: m.unhealthy}
	busy := map[string]bool{}
	for _, j := range m.jobs {
		switch j.State {
		case StateQueued:
			st.Queued++
		case StateClaimed:
			st.Busy++
			busy[j.InstanceID] = true
		}
	}
	for id, in := range m.instances {
		if !in.last.Before(now.Add(-q.LiveWindow)) {
			st.Live++
			if !busy[id] {
				st.Idle++
			}
		}
		st.UsedSeconds += usage(in.first, in.last, q.MonthStart, now, q.BootSeconds)
	}
	return st, nil
}

// usage is the instance-seconds of one instance inside [monthStart, now],
// plus the boot charge when it started in that window.
func usage(first, last, monthStart, now time.Time, boot float64) float64 {
	from := first
	if from.Before(monthStart) {
		from = monthStart
	}
	to := last
	if to.After(now) {
		to = now
	}
	var s float64
	if to.After(from) {
		s = to.Sub(from).Seconds()
	}
	if !first.Before(monthStart) && !first.After(now) {
		s += boot
	}
	return s
}

// BusyInstances implements Store.
func (m *MemStore) BusyInstances(context.Context) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for _, j := range m.jobs {
		if j.State == StateClaimed && !slices.Contains(out, j.InstanceID) {
			out = append(out, j.InstanceID)
		}
	}
	sort.Strings(out)
	return out, nil
}

// NoteClaim implements Store.
func (m *MemStore) NoteClaim(context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failures, m.unhealthy = 0, nil
	return nil
}

// NoteNoClaim implements Store.
func (m *MemStore) NoteNoClaim(_ context.Context, threshold int, cooldown time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failures++
	if m.failures >= threshold {
		m.unhealthy, m.failures = ptr(m.now().Add(cooldown)), 0
	}
	return nil
}

// Prune implements Store.
func (m *MemStore) Prune(_ context.Context, olderThan time.Duration) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	before := m.now().Add(-olderThan)
	var n int64
	for id, j := range m.jobs {
		if Terminal(j.State) && j.FinishedAt != nil && j.FinishedAt.Before(before) {
			delete(m.jobs, id)
			delete(m.events, id)
			n++
		}
	}
	return n, nil
}
