package sse

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/mmtmn/docmatch/services/api/internal/httpx"
)

// Event is one persisted run event.
type Event struct {
	Seq  int64
	Name string
	Data json.RawMessage
}

// Terminal reports whether the stream closes after this event.
func (e Event) Terminal() bool {
	switch e.Name {
	case "completed", "failed", "needs_approval":
		return true
	}
	return false
}

// Source reads persisted events (the runs store).
type Source interface {
	EventsAfter(ctx context.Context, orgID, runID uuid.UUID, afterSeq int64, limit int) ([]Event, error)
}

// Options tune a stream.
type Options struct {
	// Heartbeat is the ": ping" interval (openapi: 15s).
	Heartbeat time.Duration
	// IdleTimeout closes a stream with no event for this long
	// (limits.yaml http.sse_idle_timeout_s).
	IdleTimeout time.Duration
	// PageSize bounds one read from the source.
	PageSize int
}

// HeaderLastEventID is the resume header.
const HeaderLastEventID = "Last-Event-ID"

// ErrLastEventID is returned for a malformed resume header.
var ErrLastEventID = errors.New("sse: Last-Event-ID inválido")

// LastEventID parses the resume header (0 when absent).
func LastEventID(r *http.Request) (int64, error) {
	v := strings.TrimSpace(r.Header.Get(HeaderLastEventID))
	if v == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		return 0, ErrLastEventID
	}
	return n, nil
}

// Stream writes the events of runID after lastSeq until a terminal event,
// the idle timeout or the client going away. Every write extends the
// server's write deadline so the request timeout does not cut the stream.
func Stream(w http.ResponseWriter, r *http.Request, b *Broker, src Source, orgID, runID uuid.UUID, lastSeq int64, opts Options) error {
	if opts.Heartbeat <= 0 {
		opts.Heartbeat = 15 * time.Second
	}
	if opts.IdleTimeout <= 0 {
		opts.IdleTimeout = 5 * time.Minute
	}
	if opts.PageSize <= 0 {
		opts.PageSize = 100
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		return errors.New("sse: resposta não suporta flush")
	}
	wake, cancel := b.Subscribe(runID)
	defer cancel()

	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache, no-store")
	h.Set("X-Accel-Buffering", "no")
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_ = httpx.ExtendWriteDeadline(w, opts.IdleTimeout)
	flusher.Flush()

	ctx := r.Context()
	heartbeat := time.NewTicker(opts.Heartbeat)
	defer heartbeat.Stop()
	idle := time.NewTimer(opts.IdleTimeout)
	defer idle.Stop()

	for {
		for {
			events, err := src.EventsAfter(ctx, orgID, runID, lastSeq, opts.PageSize)
			if err != nil {
				return fmt.Errorf("sse: ler eventos: %w", err)
			}
			for _, ev := range events {
				if err := writeEvent(w, ev); err != nil {
					return err
				}
				lastSeq = ev.Seq
				_ = httpx.ExtendWriteDeadline(w, opts.IdleTimeout)
				flusher.Flush()
				if ev.Terminal() {
					return nil
				}
			}
			if len(events) > 0 {
				if !idle.Stop() {
					select {
					case <-idle.C:
					default:
					}
				}
				idle.Reset(opts.IdleTimeout)
			}
			if len(events) < opts.PageSize {
				break
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-wake:
		case <-heartbeat.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return nil
			}
			_ = httpx.ExtendWriteDeadline(w, opts.IdleTimeout)
			flusher.Flush()
		case <-idle.C:
			return nil
		}
	}
}

// writeEvent renders one message: id, event and a single-line data field.
func writeEvent(w http.ResponseWriter, ev Event) error {
	data := bytes(ev.Data)
	if len(data) == 0 {
		data = "{}"
	}
	// JSON never contains raw newlines, so one data line is enough.
	_, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", ev.Seq, ev.Name, data)
	return err
}

func bytes(raw json.RawMessage) string {
	return strings.ReplaceAll(strings.ReplaceAll(string(raw), "\r", ""), "\n", "")
}
