package pgstore

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/mmtmn/docmatch/services/api/internal/db/sqlc"
	"github.com/mmtmn/docmatch/services/api/internal/runs/apix"
)

// --------------------------------------------------------- idempotency keys

func toIdem(r sqlc.IdempotencyKey) apix.IdemRecord {
	rec := apix.IdemRecord{Key: r.Key, RequestHash: r.RequestHash, Done: r.Status == "done", ResponseBody: r.ResponseBody}
	if r.ResponseStatus != nil {
		rec.ResponseStatus = int(*r.ResponseStatus)
	}
	return rec
}

// BeginIdem implements apix.IdemStore.
func (s *Store) BeginIdem(ctx context.Context, orgID uuid.UUID, key string, hash []byte, expiresAt time.Time) (apix.IdemRecord, bool, error) {
	var rec apix.IdemRecord
	existing := false
	err := s.org(ctx, orgID, func(ctx context.Context, q *sqlc.Queries, _ pgx.Tx) error {
		row, err := q.InsertIdempotencyKey(ctx, sqlc.InsertIdempotencyKeyParams{OrgID: orgID, Key: key, RequestHash: hash, ExpiresAt: expiresAt})
		if err == nil {
			rec = toIdem(row)
			return nil
		}
		if !isNoRows(err) {
			return err
		}
		// ON CONFLICT DO NOTHING returned no row: the key exists.
		row, err = q.GetIdempotencyKey(ctx, sqlc.GetIdempotencyKeyParams{OrgID: orgID, Key: key})
		if err != nil {
			return err
		}
		rec, existing = toIdem(row), true
		return nil
	})
	return rec, existing, err
}

// FinishIdem implements apix.IdemStore.
func (s *Store) FinishIdem(ctx context.Context, orgID uuid.UUID, key string, status int, body []byte) error {
	return s.org(ctx, orgID, func(ctx context.Context, q *sqlc.Queries, _ pgx.Tx) error {
		st := int32(status) //nolint:gosec // HTTP status
		return q.FinishIdempotencyKey(ctx, sqlc.FinishIdempotencyKeyParams{OrgID: orgID, Key: key, ResponseStatus: &st, ResponseBody: nonEmpty(body)})
	})
}

// AbortIdem implements apix.IdemStore.
func (s *Store) AbortIdem(ctx context.Context, orgID uuid.UUID, key string) error {
	return s.org(ctx, orgID, func(ctx context.Context, q *sqlc.Queries, _ pgx.Tx) error {
		return q.DeleteIdempotencyKey(ctx, sqlc.DeleteIdempotencyKeyParams{OrgID: orgID, Key: key})
	})
}

// DeleteExpiredIdem removes keys past their 24 h (worker upkeep).
func (s *Store) DeleteExpiredIdem(ctx context.Context, now time.Time) (int64, error) {
	var n int64
	err := s.system(ctx, func(ctx context.Context, q *sqlc.Queries) error {
		var err error
		n, err = q.DeleteExpiredIdempotencyKeys(ctx, now)
		return err
	})
	return n, err
}

// ------------------------------------------------------------------- outbox

// OutboxMessage is one claimed outbox row.
type OutboxMessage struct {
	ID       uuid.UUID
	OrgID    *uuid.UUID
	Kind     string
	Payload  json.RawMessage
	Attempts int
}

// Enqueue implements runs.Outbox: the row is written in its own
// transaction scoped to the organization (or system mode without one).
func (s *Store) Enqueue(ctx context.Context, orgID *uuid.UUID, kind string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	insert := func(ctx context.Context, q *sqlc.Queries) error {
		_, err := q.EnqueueOutbox(ctx, sqlc.EnqueueOutboxParams{OrgID: orgID, Kind: kind, Payload: data})
		return err
	}
	if orgID == nil {
		return s.system(ctx, insert)
	}
	return s.org(ctx, *orgID, func(ctx context.Context, q *sqlc.Queries, _ pgx.Tx) error { return insert(ctx, q) })
}

// ClaimOutbox takes up to limit unsent rows (SKIP LOCKED, system mode),
// leaving the kinds in skip to their dedicated workers.
func (s *Store) ClaimOutbox(ctx context.Context, limit int, skip []string) ([]OutboxMessage, error) {
	var out []OutboxMessage
	err := s.system(ctx, func(ctx context.Context, q *sqlc.Queries) error {
		if skip == nil {
			skip = []string{}
		}
		rows, err := q.ClaimOutbox(ctx, sqlc.ClaimOutboxParams{SkipKinds: skip, MaxRows: int32(min(max(limit, 1), 1000))}) //nolint:gosec // bounded
		if err != nil {
			return err
		}
		for _, r := range rows {
			out = append(out, OutboxMessage{ID: r.ID, OrgID: r.OrgID, Kind: r.Kind, Payload: r.Payload, Attempts: int(r.Attempts)})
		}
		return nil
	})
	return out, err
}

// MarkOutboxSent records a delivery.
func (s *Store) MarkOutboxSent(ctx context.Context, id uuid.UUID) error {
	return s.system(ctx, func(ctx context.Context, q *sqlc.Queries) error { return q.MarkOutboxSent(ctx, id) })
}

// RetryOutboxLater schedules another attempt.
func (s *Store) RetryOutboxLater(ctx context.Context, id uuid.UUID, at time.Time, lastErr string) error {
	return s.system(ctx, func(ctx context.Context, q *sqlc.Queries) error {
		return q.RetryOutboxLater(ctx, sqlc.RetryOutboxLaterParams{ID: id, AvailableAt: at, LastError: strPtr(lastErr)})
	})
}
