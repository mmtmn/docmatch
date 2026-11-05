// Package sse streams run events to clients as text/event-stream. Events
// are persisted in run_events by the worker; a trigger NOTIFYs the
// run_events channel, the Listener of every API instance forwards the wake
// up to the in-process Broker, and each stream reads what it has not sent
// yet from the database. Resume (Last-Event-ID) and multi-instance delivery
// therefore follow the same path: the table is the source of truth, the
// notification only says "there is something new".
package sse

import (
	"errors"
	"strconv"
	"strings"
	"sync"

	"github.com/google/uuid"
)

// Channel is the PostgreSQL NOTIFY channel (see migration 0020).
const Channel = "run_events"

// Broker fans wake-ups out to the streams of a run.
type Broker struct {
	mu   sync.Mutex
	subs map[uuid.UUID]map[chan struct{}]struct{}
}

// NewBroker returns an empty broker.
func NewBroker() *Broker {
	return &Broker{subs: map[uuid.UUID]map[chan struct{}]struct{}{}}
}

// Subscribe registers interest in runID. The channel carries coalesced
// wake-ups (a slow subscriber sees one signal for many events); cancel
// must be called when the stream ends.
func (b *Broker) Subscribe(runID uuid.UUID) (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	b.mu.Lock()
	if b.subs[runID] == nil {
		b.subs[runID] = map[chan struct{}]struct{}{}
	}
	b.subs[runID][ch] = struct{}{}
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if set := b.subs[runID]; set != nil {
			delete(set, ch)
			if len(set) == 0 {
				delete(b.subs, runID)
			}
		}
	}
}

// Notify wakes every subscriber of runID without blocking.
func (b *Broker) Notify(runID uuid.UUID) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs[runID] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// Subscribers reports how many streams watch runID (tests, metrics).
func (b *Broker) Subscribers(runID uuid.UUID) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subs[runID])
}

// ParseNotification decodes the "<run uuid>:<seq>" payload of the trigger.
func ParseNotification(payload string) (uuid.UUID, int64, error) {
	idPart, seqPart, ok := strings.Cut(payload, ":")
	if !ok {
		return uuid.Nil, 0, errors.New("sse: notificação sem separador")
	}
	id, err := uuid.Parse(idPart)
	if err != nil {
		return uuid.Nil, 0, err
	}
	seq, err := strconv.ParseInt(seqPart, 10, 64)
	if err != nil {
		return uuid.Nil, 0, err
	}
	return id, seq, nil
}
