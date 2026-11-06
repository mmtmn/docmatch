package runs

import (
	"context"
	"sort"
	"sync"

	"github.com/google/uuid"

	"github.com/mmtmn/docmatch/services/api/internal/sse"
)

// MemStore is the in-memory Store (unit tests and the fake tooling). It
// mirrors the ordering of the SQL queries so handler tests see the same
// pages the database would produce.
type MemStore struct {
	mu       sync.Mutex
	runs     map[uuid.UUID]Run
	events   map[uuid.UUID][]Event
	findings map[uuid.UUID][]Finding
	matches  map[uuid.UUID][]Match
	feedback []Feedback
}

// NewMemStore returns an empty store.
func NewMemStore() *MemStore {
	return &MemStore{runs: map[uuid.UUID]Run{}, events: map[uuid.UUID][]Event{}, findings: map[uuid.UUID][]Finding{}, matches: map[uuid.UUID][]Match{}}
}

// CreateRun implements Store.
func (m *MemStore) CreateRun(ctx context.Context, r Run, hook TxHook) (Run, error) {
	if hook != nil {
		if err := hook(ctx, nil); err != nil {
			return Run{}, err
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.runs[r.ID] = r
	return r, nil
}

// GetRun implements Store.
func (m *MemStore) GetRun(_ context.Context, orgID, id uuid.UUID) (Run, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.runs[id]
	if !ok || r.OrgID != orgID {
		return Run{}, false, nil
	}
	return r, true, nil
}

// ListRuns implements Store.
func (m *MemStore) ListRuns(_ context.Context, orgID uuid.UUID, f ListFilter, limit, offset int) ([]Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var all []Run
	for _, r := range m.runs {
		if r.OrgID != orgID {
			continue
		}
		if f.Status != "" && r.Status != f.Status {
			continue
		}
		if f.WorkflowID != nil && (r.WorkflowID == nil || *r.WorkflowID != *f.WorkflowID) {
			continue
		}
		if f.ProjectID != nil && (r.Inputs.Project == nil || r.Inputs.Project.ID != *f.ProjectID) {
			continue
		}
		if f.ProjectOnly && r.Inputs.Project == nil {
			continue
		}
		all = append(all, r)
	}
	sort.Slice(all, func(i, j int) bool {
		if !all[i].CreatedAt.Equal(all[j].CreatedAt) {
			return all[i].CreatedAt.After(all[j].CreatedAt)
		}
		return all[i].ID.String() > all[j].ID.String()
	})
	return pageRuns(all, limit, offset), nil
}

func pageRuns(all []Run, limit, offset int) []Run {
	if offset >= len(all) {
		return []Run{}
	}
	all = all[offset:]
	if limit > 0 && len(all) > limit {
		all = all[:limit]
	}
	return all
}

// SaveRun implements Store.
func (m *MemStore) SaveRun(ctx context.Context, r Run, hook TxHook) (Run, error) {
	if hook != nil {
		if err := hook(ctx, nil); err != nil {
			return Run{}, err
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.runs[r.ID] = r
	return r, nil
}

// CountActiveRuns implements Store.
func (m *MemStore) CountActiveRuns(_ context.Context, orgID uuid.UUID) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var n int64
	for _, r := range m.runs {
		if r.OrgID == orgID && (r.Status == StatusQueued || r.Status == StatusRunning) {
			n++
		}
	}
	return n, nil
}

// AppendEvent implements Store.
func (m *MemStore) AppendEvent(_ context.Context, e Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events[e.RunID] = append(m.events[e.RunID], e)
	return nil
}

// EventsAfter implements Store.
func (m *MemStore) EventsAfter(_ context.Context, orgID, runID uuid.UUID, afterSeq int64, limit int) ([]sse.Event, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []sse.Event
	for _, e := range m.events[runID] {
		if e.OrgID != orgID || e.Seq <= afterSeq {
			continue
		}
		out = append(out, sse.Event{Seq: e.Seq, Name: e.Name, Data: e.Data})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// InsertFinding implements Store.
func (m *MemStore) InsertFinding(_ context.Context, f Finding) (Finding, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.findings[f.RunID] = append(m.findings[f.RunID], f)
	return f, nil
}

// GetFinding implements Store.
func (m *MemStore) GetFinding(_ context.Context, orgID, runID, id uuid.UUID) (Finding, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, f := range m.findings[runID] {
		if f.ID == id && f.OrgID == orgID {
			return f, true, nil
		}
	}
	return Finding{}, false, nil
}

var severityRank = map[string]int{"critical": 0, "high": 1, "medium": 2, "low": 3, "info": 4}

func sortFindings(all []Finding) {
	sort.SliceStable(all, func(i, j int) bool {
		ri, rj := severityRank[all[i].Severity], severityRank[all[j].Severity]
		if ri != rj {
			return ri < rj
		}
		if all[i].AmountAtRisk != all[j].AmountAtRisk {
			return all[i].AmountAtRisk > all[j].AmountAtRisk
		}
		if !all[i].CreatedAt.Equal(all[j].CreatedAt) {
			return all[i].CreatedAt.Before(all[j].CreatedAt)
		}
		return all[i].ID.String() < all[j].ID.String()
	})
}

// ListFindings implements Store.
func (m *MemStore) ListFindings(_ context.Context, orgID, runID uuid.UUID, f FindingFilter, limit, offset int) ([]Finding, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var all []Finding
	for _, fd := range m.findings[runID] {
		if fd.OrgID != orgID {
			continue
		}
		if f.Status != "" && fd.Status != f.Status {
			continue
		}
		if len(f.Severities) > 0 {
			ok := false
			for _, s := range f.Severities {
				if s == fd.Severity {
					ok = true
				}
			}
			if !ok {
				continue
			}
		}
		all = append(all, fd)
	}
	sortFindings(all)
	if offset >= len(all) {
		return []Finding{}, nil
	}
	all = all[offset:]
	if limit > 0 && len(all) > limit {
		all = all[:limit]
	}
	return all, nil
}

// ListAllFindings implements Store.
func (m *MemStore) ListAllFindings(_ context.Context, orgID, runID uuid.UUID) ([]Finding, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var all []Finding
	for _, fd := range m.findings[runID] {
		if fd.OrgID == orgID {
			all = append(all, fd)
		}
	}
	sortFindings(all)
	return all, nil
}

// ListFindingsWithFeedback implements Store.
func (m *MemStore) ListFindingsWithFeedback(_ context.Context, orgID, runID uuid.UUID) ([]Finding, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var all []Finding
	for _, fd := range m.findings[runID] {
		if fd.OrgID == orgID && fd.Status != "open" {
			all = append(all, fd)
		}
	}
	return all, nil
}

// UpdateFindingFeedback implements Store.
func (m *MemStore) UpdateFindingFeedback(_ context.Context, orgID, runID, id uuid.UUID, status string, fb *FeedbackView) (Finding, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	list := m.findings[runID]
	for i, f := range list {
		if f.ID == id && f.OrgID == orgID {
			list[i].Status, list[i].Feedback = status, fb
			return list[i], nil
		}
	}
	return Finding{}, nil
}

// InsertMatch implements Store.
func (m *MemStore) InsertMatch(_ context.Context, mt Match) (Match, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.matches[mt.RunID] = append(m.matches[mt.RunID], mt)
	return mt, nil
}

// ListAllMatches implements Store.
func (m *MemStore) ListAllMatches(_ context.Context, orgID, runID uuid.UUID) ([]Match, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var all []Match
	for _, mt := range m.matches[runID] {
		if mt.OrgID == orgID {
			all = append(all, mt)
		}
	}
	return all, nil
}

// InsertFeedback implements Store.
func (m *MemStore) InsertFeedback(_ context.Context, f Feedback) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.feedback = append(m.feedback, f)
	return nil
}

// FeedbackRows returns the recorded feedback (tests).
func (m *MemStore) FeedbackRows() []Feedback {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Feedback(nil), m.feedback...)
}

// Events returns the events of a run (tests).
func (m *MemStore) Events(runID uuid.UUID) []Event {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Event(nil), m.events[runID]...)
}
