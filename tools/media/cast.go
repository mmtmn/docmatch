package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"
)

// castHeader is the first line of an asciicast v2 file.
type castHeader struct {
	Version   int               `json:"version"`
	Width     int               `json:"width"`
	Height    int               `json:"height"`
	Timestamp int64             `json:"timestamp,omitempty"`
	Title     string            `json:"title,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
}

// castEvent is one event line: [seconds, "o"|"i"|"m"|"r", data].
type castEvent struct {
	Time float64
	Kind string
	Data string
}

// castWriter writes an asciicast v2 stream. Output arrives as raw bytes that may split a UTF-8
// sequence; the incomplete tail waits for the next chunk so every event is valid text.
type castWriter struct {
	mu   sync.Mutex
	w    *bufio.Writer
	tail []byte
	err  error
}

func newCastWriter(w io.Writer, h castHeader) (*castWriter, error) {
	h.Version = 2
	b, err := json.Marshal(h)
	if err != nil {
		return nil, err
	}
	cw := &castWriter{w: bufio.NewWriter(w)}
	if _, err := cw.w.Write(append(b, '\n')); err != nil {
		return nil, err
	}
	return cw, nil
}

// Output records a chunk of terminal output at t seconds.
func (c *castWriter) Output(t float64, p []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	buf := append(c.tail, p...)
	cut := completeUTF8(buf)
	c.tail = append([]byte(nil), buf[cut:]...)
	if cut > 0 {
		c.event(t, "o", string(buf[:cut]))
	}
}

// Marker records a marker event (asciicast "m"), e.g. "speed=20".
func (c *castWriter) Marker(t float64, label string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.event(t, "m", label)
}

func (c *castWriter) event(t float64, kind, data string) {
	if c.err != nil {
		return
	}
	d, err := json.Marshal(data)
	if err != nil {
		c.err = err
		return
	}
	_, c.err = fmt.Fprintf(c.w, "[%s, %q, %s]\n", strconv.FormatFloat(t, 'f', 6, 64), kind, d)
}

// Close flushes what is left (an incomplete sequence becomes U+FFFD).
func (c *castWriter) Close(t float64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.tail) > 0 {
		c.event(t, "o", strings.ToValidUTF8(string(c.tail), "�"))
		c.tail = nil
	}
	if c.err != nil {
		return c.err
	}
	return c.w.Flush()
}

// completeUTF8 returns the length of the longest prefix of b that does not end inside a UTF-8
// sequence that more bytes could still complete.
func completeUTF8(b []byte) int {
	n := len(b)
	// A sequence is at most 4 bytes: look back at most 3 for its start.
	for i := n - 1; i >= 0 && i >= n-3; i-- {
		c := b[i]
		if c < 0x80 {
			return n // ASCII: everything before it is complete
		}
		if c >= 0xC0 { // a leading byte
			need := 0
			switch {
			case c >= 0xF0:
				need = 4
			case c >= 0xE0:
				need = 3
			default:
				need = 2
			}
			if n-i < need && utf8.RuneStart(c) {
				return i
			}
			return n
		}
	}
	return n
}

// readCast parses an asciicast v2 file.
func readCast(r io.Reader) (castHeader, []castEvent, error) {
	var h castHeader
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	if !sc.Scan() {
		if err := sc.Err(); err != nil {
			return h, nil, err
		}
		return h, nil, fmt.Errorf("asciicast vazio")
	}
	if err := json.Unmarshal(sc.Bytes(), &h); err != nil {
		return h, nil, fmt.Errorf("cabeçalho do asciicast: %w", err)
	}
	if h.Version != 2 {
		return h, nil, fmt.Errorf("asciicast versão %d: só a 2 é suportada", h.Version)
	}
	if h.Width <= 0 || h.Height <= 0 {
		return h, nil, fmt.Errorf("asciicast sem tamanho (%dx%d)", h.Width, h.Height)
	}
	var events []castEvent
	line := 1
	for sc.Scan() {
		line++
		b := sc.Bytes()
		if len(strings.TrimSpace(string(b))) == 0 {
			continue
		}
		var raw []json.RawMessage
		if err := json.Unmarshal(b, &raw); err != nil || len(raw) != 3 {
			return h, nil, fmt.Errorf("linha %d do asciicast: evento inválido", line)
		}
		var e castEvent
		if err := json.Unmarshal(raw[0], &e.Time); err != nil {
			return h, nil, fmt.Errorf("linha %d do asciicast: tempo: %w", line, err)
		}
		if err := json.Unmarshal(raw[1], &e.Kind); err != nil {
			return h, nil, fmt.Errorf("linha %d do asciicast: tipo: %w", line, err)
		}
		if err := json.Unmarshal(raw[2], &e.Data); err != nil {
			return h, nil, fmt.Errorf("linha %d do asciicast: dados: %w", line, err)
		}
		events = append(events, e)
	}
	return h, events, sc.Err()
}
