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

// MemStore is an in-memory Store for tests and the local end-to-end run.
type MemStore struct {
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
}

// NewMemStore returns an empty store.
func NewMemStore() *MemStore {
	return &MemStore{jobs: map[uuid.UUID]*Job{}, events: map[uuid.UUID][]Event{}, instances: map[string]*memInstance{}}
}

func clone(j Job) Job {
	j.Request = slices.Clone(j.Request)
	j.TokenHash = slices.Clone(j.TokenHash)
	return j
}

// CreateJob implements Store.
func (m *MemStore) CreateJob(_ context.Context, j Job) (Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, other := range m.jobs {
		if other.RunID == j.RunID && !Terminal(other.State) {
			return Job{}, ErrJobActive
		}
	}
	j.State = StateQueued
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
func (m *MemStore) Claim(_ context.Context, instanceID string, tokenHash []byte, now, leaseUntil time.Time) (Job, bool, error) {
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
	oldest.State, oldest.InstanceID, oldest.TokenHash = StateClaimed, instanceID, slices.Clone(tokenHash)
	oldest.ClaimedAt, oldest.LeaseExpiresAt = ptr(now), ptr(leaseUntil)
	return clone(*oldest), true, nil
}

func ptr(t time.Time) *time.Time { return &t }

func (m *MemStore) claimed(id uuid.UUID, tokenHash []byte) *Job {
	j, ok := m.jobs[id]
	if !ok || j.State != StateClaimed || !bytes.Equal(j.TokenHash, tokenHash) {
		return nil
	}
	return j
}

// Extend implements Store.
func (m *MemStore) Extend(_ context.Context, id uuid.UUID, tokenHash []byte, leaseUntil time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j := m.claimed(id, tokenHash)
	if j == nil {
		return false, nil
	}
	j.LeaseExpiresAt = ptr(leaseUntil)
	return true, nil
}

// Finish implements Store.
func (m *MemStore) Finish(_ context.Context, id uuid.UUID, tokenHash []byte, state, reason string, now time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j := m.claimed(id, tokenHash)
	if j == nil {
		return false, nil
	}
	j.State, j.Reason, j.FinishedAt = state, reason, ptr(now)
	return true, nil
}

func (m *MemStore) move(id uuid.UUID, from []string, to, reason string, now time.Time) bool {
	j, ok := m.jobs[id]
	if !ok || !slices.Contains(from, j.State) {
		return false
	}
	j.State, j.Reason, j.FinishedAt = to, reason, ptr(now)
	return true
}

// Abandon implements Store.
func (m *MemStore) Abandon(_ context.Context, id uuid.UUID, now time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.move(id, []string{StateQueued}, StateAbandoned, "nenhum trabalhador a tempo", now), nil
}

// Cancel implements Store.
func (m *MemStore) Cancel(_ context.Context, id uuid.UUID, reason string, now time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.move(id, []string{StateQueued, StateClaimed}, StateCancelled, reason, now), nil
}

// ExpireLeases implements Store.
func (m *MemStore) ExpireLeases(_ context.Context, now time.Time) ([]uuid.UUID, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
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
func (m *MemStore) AppendEvents(_ context.Context, job Job, events []Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur := m.events[job.ID]
	for _, e := range events {
		if slices.ContainsFunc(cur, func(x Event) bool { return x.Seq == e.Seq }) {
			continue
		}
		cur = append(cur, Event{Seq: e.Seq, Data: slices.Clone(e.Data)})
	}
	sort.Slice(cur, func(i, j int) bool { return cur[i].Seq < cur[j].Seq })
	m.events[job.ID] = cur
	return nil
}

// Events implements Store.
func (m *MemStore) Events(_ context.Context, jobID uuid.UUID, afterSeq int64, limit int) ([]Event, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Event
	for _, e := range m.events[jobID] {
		if e.Seq > afterSeq {
			out = append(out, e)
			if len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}

// TouchInstance implements Store.
func (m *MemStore) TouchInstance(_ context.Context, in Instance, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.instances[in.ID]
	if !ok {
		m.instances[in.ID] = &memInstance{Instance: in, first: now, last: now}
		return nil
	}
	cur.last = now
	if in.Type != "" {
		cur.Type = in.Type
	}
	return nil
}

// Status implements Store.
func (m *MemStore) Status(_ context.Context, q StatusQuery) (PoolStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
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
		if !in.last.Before(q.LiveSince) {
			st.Live++
			if !busy[id] {
				st.Idle++
			}
		}
		st.UsedSeconds += usage(in.first, in.last, q.MonthStart, q.Now, q.BootSeconds)
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
func (m *MemStore) NoteNoClaim(_ context.Context, threshold int, cooldownUntil time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failures++
	if m.failures >= threshold {
		m.unhealthy, m.failures = ptr(cooldownUntil), 0
	}
	return nil
}

// Prune implements Store.
func (m *MemStore) Prune(_ context.Context, before time.Time) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
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
