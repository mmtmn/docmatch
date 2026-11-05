package ui

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/mmtmn/docmatch/tools/installer/internal/flow"
	"github.com/mmtmn/docmatch/tools/installer/internal/preflight"
	"github.com/mmtmn/docmatch/tools/installer/internal/text"
)

// Plain prints the installation as lines: the same steps as the full screen, for pipes, CI,
// NO_COLOR, --plain and screen readers. ASCII only, no colors, no cursor movement.
type Plain struct {
	W   io.Writer
	Now func() time.Time
	// Verbose also prints every log line (otherwise they only go to the log file).
	Verbose bool

	mu         sync.Mutex
	titles     map[string]string
	lastNote   map[string]string
	lastNoteAt map[string]time.Time
	lastBars   map[string]time.Time
	tail       []string
	tailLimit  int
}

// NewPlain writes to w.
func NewPlain(w io.Writer) *Plain {
	return &Plain{
		W: w, Now: time.Now, titles: map[string]string{}, lastNote: map[string]string{},
		lastNoteAt: map[string]time.Time{}, lastBars: map[string]time.Time{}, tailLimit: 30,
	}
}

// PlainMark is the fixed-width status marker of a line.
func PlainMark(kind string) string {
	switch kind {
	case "ok":
		return "[ok]"
	case "warn":
		return "[!!]"
	case "fail":
		return "[xx]"
	case "skip":
		return "[--]"
	default:
		return "[..]"
	}
}

func (p *Plain) printf(format string, args ...any) {
	_, _ = fmt.Fprintf(p.W, format, args...)
}

// Header is the first line.
func (p *Plain) Header(version string) {
	name := "docmatch"
	if version != "" {
		name += " " + version
	}
	p.printf("%s - %s\n", name, asciiOnly(text.T("tagline")))
}

// Section starts a block ("==> Verificações").
func (p *Plain) Section(title string) { p.printf("\n==> %s\n", asciiOnly(title)) }

// Line prints an indented line.
func (p *Plain) Line(s string) { p.printf("  %s\n", asciiOnly(s)) }

// Checks prints the preflight results.
func (p *Plain) Checks(checks []preflight.Check, results []preflight.Result) {
	for i, c := range checks {
		r := results[i]
		kind := map[preflight.Status]string{preflight.OK: "ok", preflight.Warn: "warn", preflight.Fail: "fail", preflight.Skip: "skip"}[r.Status]
		p.printf("  %s %-26s %s\n", PlainMark(kind), asciiOnly(c.Name), asciiOnly(r.Detail))
		if r.Fix != "" && (r.Status == preflight.Fail || r.Status == preflight.Warn) {
			p.printf("       %s\n", asciiOnly(r.Fix))
		}
	}
}

// Event prints one flow event. Progress is throttled to one line per step every few seconds.
func (p *Plain) Event(e flow.Event) {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch e.Kind {
	case flow.Started:
		if p.titles[e.Step] != "" && p.titles[e.Step] == e.Title {
			return
		}
		p.titles[e.Step] = e.Title
		p.printf("  %s %s\n", PlainMark("run"), asciiOnly(e.Title))
	case flow.Progressed:
		now := p.Now()
		if e.Note != "" && e.Note != p.lastNote[e.Step] && now.Sub(p.lastNoteAt[e.Step]) >= 2*time.Second {
			p.lastNote[e.Step], p.lastNoteAt[e.Step] = e.Note, now
			p.printf("       %s\n", asciiOnly(e.Note))
		}
		if line := barsLine(e.Bars); line != "" && now.Sub(p.lastBars[e.Step]) >= 5*time.Second {
			p.lastBars[e.Step] = now
			p.printf("       %s\n", asciiOnly(line))
		}
	case flow.Logged:
		p.tail = append(p.tail, e.Line)
		if len(p.tail) > p.tailLimit {
			p.tail = p.tail[1:]
		}
		if p.Verbose {
			p.printf("       | %s\n", e.Line)
		}
	case flow.Finished:
		title := p.titles[e.Step]
		kind := map[flow.Outcome]string{flow.OK: "ok", flow.Skipped: "skip", flow.Warned: "warn", flow.Failed: "fail"}[e.Outcome]
		detail := e.Detail
		if e.Err != nil {
			detail = e.Err.Error()
		}
		if detail != "" {
			detail = ": " + detail
		}
		p.printf("  %s %s%s (%s)\n", PlainMark(kind), asciiOnly(title), asciiOnly(detail), asciiOnly(formatDuration(e.Elapsed)))
	}
}

// barsLine renders the bars that carry information: "api: 12/16 | web 40%".
func barsLine(bars []flow.Bar) string {
	parts := make([]string, 0, len(bars))
	for _, b := range bars {
		switch {
		case b.Text != "":
			parts = append(parts, b.Label+": "+b.Text)
		case b.Fraction >= 0:
			parts = append(parts, fmt.Sprintf("%s %d%%", b.Label, int(b.Fraction*100)))
		}
	}
	return strings.Join(parts, " | ")
}

// Tail prints the last log lines (after a failure).
func (p *Plain) Tail() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.tail) == 0 {
		return
	}
	p.printf("\n  %s\n", asciiOnly(text.T("progress.log_tail")))
	for _, l := range p.tail {
		p.printf("       | %s\n", l)
	}
}
