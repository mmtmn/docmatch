package sse

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestBroker(t *testing.T) {
	t.Parallel()
	b := NewBroker()
	run := uuid.New()
	ch, cancel := b.Subscribe(run)
	other, cancelOther := b.Subscribe(uuid.New())
	defer cancelOther()
	b.Notify(run)
	b.Notify(run) // coalesced
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("sem wake-up")
	}
	select {
	case <-other:
		t.Fatal("outra execução não deveria acordar")
	default:
	}
	if b.Subscribers(run) != 1 {
		t.Fatal("contagem")
	}
	cancel()
	if b.Subscribers(run) != 0 {
		t.Fatal("cancel não removeu")
	}
	b.Notify(run) // no subscribers: must not block
	id, seq, err := ParseNotification(run.String() + ":42")
	if err != nil || id != run || seq != 42 {
		t.Fatalf("parse = %s %d %v", id, seq, err)
	}
	for _, bad := range []string{"", "x", run.String(), run.String() + ":a", "nope:1"} {
		if _, _, err := ParseNotification(bad); err == nil {
			t.Errorf("%q deveria falhar", bad)
		}
	}
}

// memSource is an in-memory Source scoped by organization.
type memSource struct {
	mu     sync.Mutex
	events map[uuid.UUID][]Event
	org    uuid.UUID
	err    error
}

func (m *memSource) add(run uuid.UUID, ev Event) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events[run] = append(m.events[run], ev)
}

func (m *memSource) EventsAfter(_ context.Context, orgID, runID uuid.UUID, after int64, limit int) ([]Event, error) {
	if m.err != nil {
		return nil, m.err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if orgID != m.org {
		return nil, nil
	}
	var out []Event
	for _, e := range m.events[runID] {
		if e.Seq > after {
			out = append(out, e)
		}
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

type frame struct {
	id, event, data string
}

func readFrames(t *testing.T, body string) ([]frame, int) {
	t.Helper()
	var frames []frame
	pings := 0
	cur := frame{}
	sc := bufio.NewScanner(strings.NewReader(body))
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			if cur.event != "" {
				frames = append(frames, cur)
			}
			cur = frame{}
		case strings.HasPrefix(line, ": ping"):
			pings++
		case strings.HasPrefix(line, "id: "):
			cur.id = line[4:]
		case strings.HasPrefix(line, "event: "):
			cur.event = line[7:]
		case strings.HasPrefix(line, "data: "):
			cur.data = line[6:]
		}
	}
	return frames, pings
}

func serve(t *testing.T, b *Broker, src Source, org, run uuid.UUID, opts Options) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		last, err := LastEventID(r)
		if err != nil {
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		_ = Stream(w, r, b, src, org, run, last, opts)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func get(t *testing.T, url string, lastID string) *http.Response {
	t.Helper()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if lastID != "" {
		req.Header.Set(HeaderLastEventID, lastID)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestStreamReplayResumeAndTerminal(t *testing.T) {
	t.Parallel()
	org, run := uuid.New(), uuid.New()
	src := &memSource{events: map[uuid.UUID][]Event{}, org: org}
	for i := 1; i <= 3; i++ {
		src.add(run, Event{Seq: int64(i), Name: "progress", Data: json.RawMessage(`{"done":` + string(rune('0'+i)) + `}`)})
	}
	src.add(run, Event{Seq: 4, Name: "completed", Data: json.RawMessage(`{"summary":{}}`)})
	b := NewBroker()
	srv := serve(t, b, src, org, run, Options{PageSize: 2})

	resp := get(t, srv.URL, "")
	body := readAll(t, resp)
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content-type = %s", ct)
	}
	frames, _ := readFrames(t, body)
	if len(frames) != 4 || frames[0].id != "1" || frames[3].event != "completed" || frames[3].id != "4" {
		t.Fatalf("frames = %+v", frames)
	}
	// Resume from 2: only 3 and 4.
	frames, _ = readFrames(t, readAll(t, get(t, srv.URL, "2")))
	if len(frames) != 2 || frames[0].id != "3" {
		t.Fatalf("resume = %+v", frames)
	}
	// Malformed Last-Event-ID.
	if resp := get(t, srv.URL, "x"); resp.StatusCode != http.StatusBadRequest {
		t.Fatal("Last-Event-ID inválido aceito")
	}
	_ = resp.Body.Close()
}

func readAll(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer func() { _ = resp.Body.Close() }()
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return sb.String()
}

func TestStreamIsolationAndLive(t *testing.T) {
	t.Parallel()
	org, run := uuid.New(), uuid.New()
	src := &memSource{events: map[uuid.UUID][]Event{}, org: org}
	src.add(run, Event{Seq: 1, Name: "stage_started", Data: json.RawMessage(`{"stage":"scan"}`)})
	b := NewBroker()
	// A stream opened for another organization sees nothing and closes on idle.
	otherSrv := serve(t, b, src, uuid.New(), run, Options{IdleTimeout: 150 * time.Millisecond, Heartbeat: 50 * time.Millisecond})
	frames, pings := readFrames(t, readAll(t, get(t, otherSrv.URL, "")))
	if len(frames) != 0 || pings == 0 {
		t.Fatalf("isolamento: frames=%d pings=%d", len(frames), pings)
	}
	// The right organization gets the first event, then live ones after Notify.
	srv := serve(t, b, src, org, run, Options{IdleTimeout: 2 * time.Second, Heartbeat: time.Hour})
	done := make(chan string, 1)
	go func() { done <- readAll(t, get(t, srv.URL, "")) }()
	deadline := time.Now().Add(2 * time.Second)
	for b.Subscribers(run) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	src.add(run, Event{Seq: 2, Name: "finding", Data: json.RawMessage(`{"finding":{}}`)})
	src.add(run, Event{Seq: 3, Name: "failed", Data: json.RawMessage(`{"error":{}}`)})
	b.Notify(run)
	select {
	case body := <-done:
		frames, _ := readFrames(t, body)
		if len(frames) != 3 || frames[2].event != "failed" {
			t.Fatalf("frames = %+v", frames)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stream não terminou")
	}
	if !(Event{Name: "needs_approval"}).Terminal() || (Event{Name: "progress"}).Terminal() {
		t.Fatal("Terminal")
	}
}

func TestStreamSourceError(t *testing.T) {
	t.Parallel()
	org, run := uuid.New(), uuid.New()
	src := &memSource{events: map[uuid.UUID][]Event{}, org: org, err: errors.New("banco fora")}
	rec := httptest.NewRecorder()
	err := Stream(rec, httptest.NewRequest(http.MethodGet, "/", nil), NewBroker(), src, org, run, 0, Options{})
	if err == nil || !strings.Contains(err.Error(), "banco fora") {
		t.Fatalf("erro = %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatal("cabeçalhos já enviados")
	}
	noFlush := struct{ http.ResponseWriter }{httptest.NewRecorder()}
	if err := Stream(noFlush, httptest.NewRequest(http.MethodGet, "/", nil), NewBroker(), src, org, run, 0, Options{}); err == nil {
		t.Fatal("sem flusher deveria falhar")
	}
}
