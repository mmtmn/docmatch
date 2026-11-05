// Package flow runs the installation as an ordered list of steps and reports what happens as
// events. The full-screen interface and the plain line output consume the same events, so both
// show the same steps, progress and errors.
package flow

import (
	"context"
	"errors"
	"time"
)

// Kind is the type of an event.
type Kind int

// Event kinds.
const (
	Started Kind = iota
	Progressed
	Logged
	Finished
)

// Bar is one progress bar of a step (an image being pulled or built, the uploads...).
type Bar struct {
	Label    string
	Fraction float64 // 0..1; negative means unknown
	Text     string  // "12/16", "120 MB/310 MB"
}

// Outcome of a finished step.
type Outcome int

// Outcomes.
const (
	OK Outcome = iota
	Skipped
	Warned // finished with a problem that does not stop the installation
	Failed
)

// Event is something that happened to a step.
type Event struct {
	Kind    Kind
	Step    string
	Title   string
	Bars    []Bar
	Note    string
	Line    string
	Detail  string
	Outcome Outcome
	Err     error
	Elapsed time.Duration
}

// Reporter is what a running step uses to report progress.
type Reporter struct {
	step string
	emit func(Event)
}

// NewReporter makes a reporter for step (tests and one-off commands).
func NewReporter(step string, emit func(Event)) *Reporter {
	return &Reporter{step: step, emit: emit}
}

// Progress updates the step's bars and note.
func (r *Reporter) Progress(bars []Bar, note string) {
	r.emit(Event{Kind: Progressed, Step: r.step, Bars: bars, Note: note})
}

// Note updates only the step's note.
func (r *Reporter) Note(note string) { r.Progress(nil, note) }

// Log adds a line to the log pane.
func (r *Reporter) Log(line string) {
	r.emit(Event{Kind: Logged, Step: r.step, Line: line})
}

// Retitle changes the step's title (e.g. pulling turned into building).
func (r *Reporter) Retitle(title string) {
	r.emit(Event{Kind: Started, Step: r.step, Title: title})
}

// Result is what a step returns: a detail for the finished line, and how it ended.
type Result struct {
	Detail  string
	Outcome Outcome
}

// Done is a successful result with detail.
func Done(detail string) Result { return Result{Detail: detail} }

// Skip is a step that had nothing to do (already done in an earlier run).
func Skip(detail string) Result { return Result{Detail: detail, Outcome: Skipped} }

// Warn is a step that finished with a non-fatal problem.
func Warn(detail string) Result { return Result{Detail: detail, Outcome: Warned} }

// Step is one unit of the installation.
type Step struct {
	ID    string
	Title string
	Run   func(ctx context.Context, r *Reporter) (Result, error)
}

// ErrCancelled is returned when the context ends the run.
var ErrCancelled = errors.New("cancelado")

// Run executes steps in order and stops at the first error. After each successful step, done
// (when not nil) is called, so progress is saved for the next run.
func Run(ctx context.Context, steps []Step, emit func(Event), done func(id string)) error {
	for _, s := range steps {
		if ctx.Err() != nil {
			return ErrCancelled
		}
		start := time.Now()
		emit(Event{Kind: Started, Step: s.ID, Title: s.Title})
		res, err := s.Run(ctx, &Reporter{step: s.ID, emit: emit})
		elapsed := time.Since(start)
		if err != nil {
			if ctx.Err() != nil {
				err = ErrCancelled
			}
			emit(Event{Kind: Finished, Step: s.ID, Outcome: Failed, Err: err, Elapsed: elapsed})
			return err
		}
		emit(Event{Kind: Finished, Step: s.ID, Outcome: res.Outcome, Detail: res.Detail, Elapsed: elapsed})
		if done != nil && res.Outcome != Warned {
			done(s.ID)
		}
	}
	return nil
}
