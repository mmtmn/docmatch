package docker

import (
	"encoding/base64"
	"encoding/json"
	"regexp"
	"sort"
	"strings"
)

// PullProgress is the state of a pull, per service.
type PullProgress struct {
	Services []PullService
}

// PullService is one image being pulled.
type PullService struct {
	Name    string
	Current int64 // bytes downloaded
	Total   int64 // bytes known so far
	Done    bool
	Failed  bool
	Status  string
}

// Fraction is the downloaded share (0..1), 1 when done.
func (s PullService) Fraction() float64 {
	if s.Done {
		return 1
	}
	if s.Total <= 0 {
		return 0
	}
	f := float64(s.Current) / float64(s.Total)
	if f > 1 {
		f = 1
	}
	return f
}

// pullEvent is one line of `docker compose --progress json pull`.
type pullEvent struct {
	ID       string `json:"id"`
	ParentID string `json:"parent_id"`
	Text     string `json:"text"`
	Status   string `json:"status"`
	Current  int64  `json:"current"`
	Total    int64  `json:"total"`
	Error    bool   `json:"error"`
	Message  string `json:"message"`
}

type layer struct{ current, total int64 }

// PullTracker folds pull events into per-service progress.
type PullTracker struct {
	order    []string
	services map[string]*PullService
	layers   map[string]map[string]*layer
}

// NewPullTracker starts an empty tracker.
func NewPullTracker() *PullTracker {
	return &PullTracker{services: map[string]*PullService{}, layers: map[string]map[string]*layer{}}
}

func (t *PullTracker) service(name string) *PullService {
	s, ok := t.services[name]
	if !ok {
		s = &PullService{Name: name}
		t.services[name] = s
		t.order = append(t.order, name)
		t.layers[name] = map[string]*layer{}
	}
	return s
}

// Feed reads one output line; ok is false when it is not a pull event (plain log text).
func (t *PullTracker) Feed(line string) (changed, ok bool) {
	var ev pullEvent
	if !strings.HasPrefix(strings.TrimSpace(line), "{") || json.Unmarshal([]byte(line), &ev) != nil {
		return false, false
	}
	if ev.Error {
		return false, true
	}
	if ev.ParentID == "" {
		if ev.ID == "" {
			return false, true
		}
		s := t.service(ev.ID)
		switch ev.Text {
		case "Pulled", "Skipped", "Exists":
			s.Done = true
		case "Error":
			s.Failed = true
		}
		if ev.Status == "Interrupted" {
			s.Failed = true
		}
		s.Status = ev.Text
		return true, true
	}
	s := t.service(ev.ParentID)
	l, ok := t.layers[ev.ParentID][ev.ID]
	if !ok {
		l = &layer{}
		t.layers[ev.ParentID][ev.ID] = l
	}
	switch {
	case ev.Text == "Downloading" && ev.Total > 0:
		l.current, l.total = ev.Current, ev.Total
	case ev.Text == "Download complete" || ev.Text == "Pull complete" || ev.Text == "Already exists":
		if l.total > 0 {
			l.current = l.total
		}
	default:
		return false, true
	}
	s.Current, s.Total = 0, 0
	for _, ly := range t.layers[ev.ParentID] {
		s.Current += ly.current
		s.Total += ly.total
	}
	return true, true
}

// Snapshot copies the current state.
func (t *PullTracker) Snapshot() PullProgress {
	out := PullProgress{Services: make([]PullService, 0, len(t.order))}
	for _, name := range t.order {
		out.Services = append(out.Services, *t.services[name])
	}
	return out
}

// BuildProgress is the state of a build, per service.
type BuildProgress struct {
	Services []BuildService
}

// BuildService counts the build steps of one image.
type BuildService struct {
	Name    string
	Done    int
	Total   int
	Current string // the step running now, e.g. "RUN cargo build ..."
	// Bytes of the running step's downloads (base image layers), when it reports them.
	CurrentBytes, TotalBytes int64
	Failed                   bool
}

// Fraction is the share of steps done (0..1).
func (s BuildService) Fraction() float64 {
	if s.Total == 0 {
		return 0
	}
	return float64(s.Done) / float64(s.Total)
}

type vertex struct {
	Digest    string `json:"digest"`
	Name      string `json:"name"`
	Started   string `json:"started"`
	Completed string `json:"completed"`
	Cached    bool   `json:"cached"`
	Error     string `json:"error"`
}

type buildEvent struct {
	Vertexes []vertex `json:"vertexes"`
	Logs     []struct {
		Vertex string `json:"vertex"`
		Data   string `json:"data"`
	} `json:"logs"`
	Statuses []struct {
		ID      string `json:"id"`
		Vertex  string `json:"vertex"`
		Current int64  `json:"current"`
		Total   int64  `json:"total"`
	} `json:"statuses"`
	Level string `json:"level"`
	Msg   string `json:"msg"`
}

// stepName matches "[api build 3/6] RUN ..." and "[web 2/4] COPY ...", not "[api internal] ...".
var stepName = regexp.MustCompile(`^\[([a-z0-9_.-]+)(?: [a-z0-9_.-]+)? (\d+)/(\d+)\] (.*)$`)

type step struct {
	service string
	label   string
	done    bool
	running bool
	failed  bool
	bytes   map[string][2]int64 // status id -> current, total
}

// BuildTracker folds BuildKit solve status (compose --progress json build) into per-service
// step counts.
type BuildTracker struct {
	steps   map[string]*step // by digest
	order   []string         // services in first-seen order
	seen    map[string]bool
	partial map[string]string // vertex -> unfinished log line
}

// NewBuildTracker starts an empty tracker.
func NewBuildTracker() *BuildTracker {
	return &BuildTracker{steps: map[string]*step{}, seen: map[string]bool{}, partial: map[string]string{}}
}

// Feed reads one output line. It returns the build log lines it carried, whether the progress
// changed, and ok=false when the line is not a build event.
func (t *BuildTracker) Feed(line string) (logs []string, changed, ok bool) {
	var ev buildEvent
	if !strings.HasPrefix(strings.TrimSpace(line), "{") || json.Unmarshal([]byte(line), &ev) != nil {
		return nil, false, false
	}
	if ev.Msg != "" && len(ev.Vertexes) == 0 && len(ev.Logs) == 0 {
		return []string{ev.Msg}, false, true
	}
	for _, v := range ev.Vertexes {
		m := stepName.FindStringSubmatch(v.Name)
		if m == nil {
			continue
		}
		s, exists := t.steps[v.Digest]
		if !exists {
			s = &step{service: m[1], label: m[4]}
			t.steps[v.Digest] = s
			if !t.seen[m[1]] {
				t.seen[m[1]] = true
				t.order = append(t.order, m[1])
			}
			changed = true
		}
		done := v.Completed != "" || v.Cached
		if done != s.done || (v.Started != "" && !done) != s.running || (v.Error != "") != s.failed {
			changed = true
		}
		s.done = done
		s.running = v.Started != "" && !done
		s.failed = v.Error != ""
	}
	for _, st := range ev.Statuses {
		s, known := t.steps[st.Vertex]
		if !known || st.Total <= 0 {
			continue
		}
		if s.bytes == nil {
			s.bytes = map[string][2]int64{}
		}
		s.bytes[st.ID] = [2]int64{st.Current, st.Total}
		changed = true
	}
	for _, l := range ev.Logs {
		data, err := base64.StdEncoding.DecodeString(l.Data)
		if err != nil {
			continue
		}
		text := t.partial[l.Vertex] + string(data)
		parts := strings.Split(text, "\n")
		t.partial[l.Vertex] = parts[len(parts)-1]
		for _, p := range parts[:len(parts)-1] {
			p = strings.TrimRight(p, "\r")
			if p != "" {
				logs = append(logs, p)
			}
		}
	}
	return logs, changed, true
}

// Snapshot copies the current state.
func (t *BuildTracker) Snapshot() BuildProgress {
	by := map[string]*BuildService{}
	for _, name := range t.order {
		by[name] = &BuildService{Name: name}
	}
	digests := make([]string, 0, len(t.steps))
	for d := range t.steps {
		digests = append(digests, d)
	}
	sort.Strings(digests)
	for _, d := range digests {
		s := t.steps[d]
		b := by[s.service]
		b.Total++
		if s.done {
			b.Done++
		}
		if s.running {
			b.Current = s.label
			b.CurrentBytes, b.TotalBytes = 0, 0
			for _, v := range s.bytes {
				b.CurrentBytes += v[0]
				b.TotalBytes += v[1]
			}
		}
		if s.failed {
			b.Failed = true
		}
	}
	out := BuildProgress{Services: make([]BuildService, 0, len(t.order))}
	for _, name := range t.order {
		out.Services = append(out.Services, *by[name])
	}
	return out
}
