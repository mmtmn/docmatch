package jobs

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/riverqueue/river"

	"github.com/mmtmn/docmatch/services/api/internal/jobs/jobargs"
	"github.com/mmtmn/docmatch/services/api/internal/logx"
	"github.com/mmtmn/docmatch/services/api/internal/runs"
)

func TestRunTimeoutScalesWithTheExpectedTime(t *testing.T) {
	t.Parallel()
	w := &RunWorker{deps: Deps{Logger: logx.Discard()}}
	job := func(expected int64) *river.Job[jobargs.RunArgs] {
		return &river.Job[jobargs.RunArgs]{Args: jobargs.RunArgs{ExpectedSeconds: expected}}
	}
	if got := w.Timeout(job(0)); got != runs.DefaultRunDeadline+RunTimeoutPad {
		t.Fatalf("sem tempo esperado = %v", got)
	}
	if got := w.Timeout(job(5 * 3600)); got != 15*time.Hour+RunTimeoutPad {
		t.Fatalf("5 h esperadas = %v", got)
	}
	if got := w.Timeout(job(100 * 3600)); got != 24*time.Hour+RunTimeoutPad {
		t.Fatalf("teto = %v", got)
	}
}

func TestRequeueSnoozesWithoutSpendingAnAttempt(t *testing.T) {
	t.Parallel()
	w := &RunWorker{deps: Deps{Logger: logx.Discard()}}
	var snooze *river.JobSnoozeError
	if err := w.result("run_x", fmt.Errorf("%w: instância perdida", runs.ErrRequeue)); !errors.As(err, &snooze) || snooze.Duration != RequeueDelay {
		t.Fatalf("requeue = %v", err)
	}
	if err := w.result("run_x", errors.New("boom")); err == nil || errors.As(err, &snooze) {
		t.Fatalf("erro comum = %v", err)
	}
	if err := w.result("run_x", nil); err != nil {
		t.Fatal(err)
	}
}
