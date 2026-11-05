package pgstore

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/mmtmn/docmatch/services/api/internal/db/sqlc"
	"github.com/mmtmn/docmatch/services/api/internal/estimate"
)

// maxTimingRows bounds one history read.
const maxTimingRows = 100_000

// RecentTimings implements estimate.Store: completed runs of every
// organization (system mode), newest first.
func (s *Store) RecentTimings(ctx context.Context, since time.Time, limit int) ([]estimate.TimingSample, error) {
	out := []estimate.TimingSample{}
	err := s.system(ctx, func(ctx context.Context, q *sqlc.Queries) error {
		rows, err := q.ListRecentRunTimings(ctx, sqlc.ListRecentRunTimingsParams{FinishedAt: since, Limit: int32(min(max(limit, 1), maxTimingRows))}) //nolint:gosec // bounded
		if err != nil {
			return err
		}
		for _, r := range rows {
			units := estimate.Units{}
			if len(r.Units) > 0 {
				if err := json.Unmarshal(r.Units, &units); err != nil {
					continue // a malformed row is skipped, never fatal to an estimate
				}
			}
			out = append(out, estimate.TimingSample{Units: units, ActiveSeconds: r.ActiveSeconds})
		}
		return nil
	})
	return out, err
}

// InsertTiming implements estimate.Store (ON CONFLICT DO NOTHING: once per run).
func (s *Store) InsertTiming(ctx context.Context, t estimate.RunTiming) error {
	units, err := json.Marshal(t.Units)
	if err != nil {
		return err
	}
	return s.org(ctx, t.OrgID, func(ctx context.Context, q *sqlc.Queries, _ pgx.Tx) error {
		return q.InsertRunTiming(ctx, sqlc.InsertRunTimingParams{
			RunID: t.RunID, OrgID: t.OrgID, Status: t.Status, Units: units, ActiveSeconds: max(t.ActiveSeconds, 0),
			ExpectedSeconds: max(t.ExpectedSeconds, 0), FinishedAt: t.FinishedAt,
		})
	})
}

// RunsInFlight implements estimate.Store: queued or running runs of every
// organization touched after since (system mode).
func (s *Store) RunsInFlight(ctx context.Context, since time.Time) (int64, error) {
	var n int64
	err := s.system(ctx, func(ctx context.Context, q *sqlc.Queries) error {
		var err error
		n, err = q.CountRunsInFlight(ctx, since)
		return err
	})
	return n, err
}
