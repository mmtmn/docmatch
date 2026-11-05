package flow

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestRunOrderAndResults(t *testing.T) {
	var events []string
	var done []string
	steps := []Step{
		{ID: "a", Title: "A", Run: func(_ context.Context, r *Reporter) (Result, error) {
			r.Progress([]Bar{{Label: "x", Fraction: 0.5}}, "meio")
			r.Log("linha")
			r.Retitle("A2")
			return Done("feito"), nil
		}},
		{ID: "b", Title: "B", Run: func(context.Context, *Reporter) (Result, error) { return Skip("já"), nil }},
		{ID: "c", Title: "C", Run: func(context.Context, *Reporter) (Result, error) { return Warn("quase"), nil }},
		{ID: "d", Title: "D", Run: func(context.Context, *Reporter) (Result, error) { return Result{}, errors.New("quebrou") }},
		{ID: "e", Title: "E", Run: func(context.Context, *Reporter) (Result, error) {
			t.Fatal("não roda depois do erro")
			return Result{}, nil
		}},
	}
	err := Run(context.Background(), steps, func(e Event) {
		events = append(events, e.Step+":"+map[Kind]string{Started: "start", Progressed: "prog", Logged: "log", Finished: "end"}[e.Kind])
	}, func(id string) { done = append(done, id) })
	if err == nil || err.Error() != "quebrou" {
		t.Fatal(err)
	}
	want := "a:start,a:prog,a:log,a:start,a:end,b:start,b:end,c:start,c:end,d:start,d:end"
	if got := strings.Join(events, ","); got != want {
		t.Fatalf("eventos:\n%s\n%s", got, want)
	}
	// Warned steps are not recorded as done: the next run tries them again.
	if strings.Join(done, ",") != "a,b" {
		t.Fatalf("feitos: %v", done)
	}
}

func TestRunCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var last Event
	err := Run(ctx, []Step{{ID: "a", Run: func(ctx context.Context, _ *Reporter) (Result, error) {
		cancel()
		return Result{}, ctx.Err()
	}}}, func(e Event) { last = e }, nil)
	if !errors.Is(err, ErrCancelled) || last.Outcome != Failed || !errors.Is(last.Err, ErrCancelled) {
		t.Fatalf("%v %+v", err, last)
	}
	if err := Run(ctx, []Step{{ID: "b"}}, func(Event) {}, nil); !errors.Is(err, ErrCancelled) {
		t.Fatal(err)
	}
}
