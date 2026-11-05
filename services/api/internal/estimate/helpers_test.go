package estimate

import (
	"context"
	"net/url"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/mmtmn/docmatch/services/api/internal/storage"
)

// noStores returns local stores the estimate tests never write to.
func noStores(t *testing.T) storage.Stores {
	t.Helper()
	base, _ := url.Parse("http://localhost:8080")
	local, err := storage.NewLocal(t.TempDir(), "test", base)
	if err != nil {
		t.Fatal(err)
	}
	return storage.Stores{Documents: local, Media: local}
}

// noJobs is an Enqueuer that drops everything.
type noJobs struct{}

func (noJobs) Enqueue(context.Context, river.JobArgs) error           { return nil }
func (noJobs) EnqueueTx(context.Context, pgx.Tx, river.JobArgs) error { return nil }
