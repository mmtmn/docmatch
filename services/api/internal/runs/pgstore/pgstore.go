// Package pgstore implements the pipeline stores (uploads, quotes, runs,
// workflows, rules, idempotency keys, outbox) on PostgreSQL with pgx and
// the sqlc queries of db/queries/runs_*.sql. Every tenant operation runs
// inside db.WithOrgTx, so row-level security scopes it; the few
// cross-tenant reads (verdicts from the scanner queue, overrun samples,
// the outbox) use db.WithSystemTx.
package pgstore

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mmtmn/docmatch/services/api/internal/db"
	"github.com/mmtmn/docmatch/services/api/internal/db/sqlc"
	"github.com/mmtmn/docmatch/services/api/internal/estimate"
	"github.com/mmtmn/docmatch/services/api/internal/uploads"
)

// Store is the pgx implementation of every pipeline store interface.
type Store struct {
	pool *pgxpool.Pool
}

// New wraps the pool.
func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

func (s *Store) org(ctx context.Context, orgID uuid.UUID, fn func(ctx context.Context, q *sqlc.Queries, tx pgx.Tx) error) error {
	return db.WithOrgTx(ctx, s.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		return fn(ctx, sqlc.New(tx), tx)
	})
}

func (s *Store) system(ctx context.Context, fn func(ctx context.Context, q *sqlc.Queries) error) error {
	return db.WithSystemTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		return fn(ctx, sqlc.New(tx))
	})
}

func strPtr(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}

func strVal(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func jsonOrNil(v any) []byte {
	if v == nil {
		return nil
	}
	data, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return data
}

func isNoRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }

// ------------------------------------------------------------------ uploads

func toUpload(u sqlc.Upload) uploads.Upload {
	return uploads.Upload{
		ID: u.ID, OrgID: u.OrgID, UserID: u.UserID, Kind: uploads.Kind(u.Kind), Store: u.Store, Role: strVal(u.Role),
		FileName: u.FileName, DeclaredMime: strVal(u.ContentType), DetectedMime: strVal(u.DetectedMime), SizeBytes: u.SizeBytes,
		SHA256: u.Sha256, StorageKey: u.StorageKey, Status: uploads.Status(u.Status), RejectedRsn: strVal(u.RejectedReason),
		ScanResult: u.ScanResult, DurationMs: u.DurationMs, CreatedAt: u.CreatedAt, TicketExpiry: u.TicketExpiresAt,
		CompletedAt: u.CompletedAt, ScannedAt: u.ScannedAt, ExpiresAt: u.ExpiresAt,
	}
}

// CreateUpload implements uploads.Store.
func (s *Store) CreateUpload(ctx context.Context, u uploads.Upload) (uploads.Upload, error) {
	var out uploads.Upload
	err := s.org(ctx, u.OrgID, func(ctx context.Context, q *sqlc.Queries, _ pgx.Tx) error {
		row, err := q.InsertUploadTicket(ctx, sqlc.InsertUploadTicketParams{
			ID: u.ID, OrgID: u.OrgID, UserID: u.UserID, Kind: string(u.Kind), Store: u.Store, Role: strPtr(u.Role), FileName: u.FileName,
			ContentType: strPtr(u.DeclaredMime), SizeBytes: u.SizeBytes, Sha256: u.SHA256, StorageKey: u.StorageKey,
			TicketExpiresAt: u.TicketExpiry, ExpiresAt: u.ExpiresAt,
		})
		if err != nil {
			return err
		}
		out = toUpload(row)
		return nil
	})
	return out, err
}

// GetUpload implements uploads.Store.
func (s *Store) GetUpload(ctx context.Context, orgID, id uuid.UUID) (uploads.Upload, bool, error) {
	var out uploads.Upload
	found := false
	err := s.org(ctx, orgID, func(ctx context.Context, q *sqlc.Queries, _ pgx.Tx) error {
		row, err := q.GetUpload(ctx, sqlc.GetUploadParams{OrgID: orgID, ID: id})
		if isNoRows(err) {
			return nil
		}
		if err != nil {
			return err
		}
		out, found = toUpload(row), true
		return nil
	})
	return out, found, err
}

// ListUploads implements uploads.Store.
func (s *Store) ListUploads(ctx context.Context, orgID uuid.UUID, ids []uuid.UUID) ([]uploads.Upload, error) {
	out := []uploads.Upload{}
	if len(ids) == 0 {
		return out, nil
	}
	err := s.org(ctx, orgID, func(ctx context.Context, q *sqlc.Queries, _ pgx.Tx) error {
		rows, err := q.ListUploadsByIDs(ctx, sqlc.ListUploadsByIDsParams{OrgID: orgID, Ids: ids})
		if err != nil {
			return err
		}
		for _, r := range rows {
			out = append(out, toUpload(r))
		}
		return nil
	})
	return out, err
}

// UpdateUpload implements uploads.Store.
func (s *Store) UpdateUpload(ctx context.Context, u uploads.Upload) (uploads.Upload, error) {
	var out uploads.Upload
	err := s.org(ctx, u.OrgID, func(ctx context.Context, q *sqlc.Queries, _ pgx.Tx) error {
		row, err := q.UpdateUploadState(ctx, sqlc.UpdateUploadStateParams{
			OrgID: u.OrgID, ID: u.ID, Status: string(u.Status), Sha256: u.SHA256, DetectedMime: strPtr(u.DetectedMime),
			RejectedReason: strPtr(u.RejectedRsn), ScanResult: u.ScanResult, ScannedAt: u.ScannedAt, CompletedAt: u.CompletedAt, DurationMs: u.DurationMs,
		})
		if err != nil {
			return err
		}
		out = toUpload(row)
		return nil
	})
	return out, err
}

// GetUploadAny implements uploads.Store (system mode).
func (s *Store) GetUploadAny(ctx context.Context, id uuid.UUID) (uploads.Upload, bool, error) {
	var out uploads.Upload
	found := false
	err := s.system(ctx, func(ctx context.Context, q *sqlc.Queries) error {
		row, err := q.GetUploadSystem(ctx, id)
		if isNoRows(err) {
			return nil
		}
		if err != nil {
			return err
		}
		out, found = toUpload(row), true
		return nil
	})
	return out, found, err
}

// ------------------------------------------------------------------- quotes

func toQuote(r sqlc.Quote) (estimate.Record, error) {
	rec := estimate.Record{
		ID: r.ID, OrgID: r.OrgID, UserID: r.UserID, Lane: r.Lane, Expected: r.EstimatedCredits, Cap: r.CapCredits, Request: r.Inputs,
		Signature: string(r.Signature), Fingerprint: strVal(r.Fingerprint), RunID: r.RunID, Kind: r.Kind, Status: r.Status,
		ExpiresAt: r.ExpiresAt, CreatedAt: r.CreatedAt,
	}
	if len(r.Quote) > 0 {
		if err := json.Unmarshal(r.Quote, &rec.Quote); err != nil {
			return estimate.Record{}, err
		}
	}
	return rec, nil
}

// InsertQuote implements estimate.Store.
func (s *Store) InsertQuote(ctx context.Context, rec estimate.Record) error {
	quote, err := json.Marshal(rec.Quote)
	if err != nil {
		return err
	}
	return s.org(ctx, rec.OrgID, func(ctx context.Context, q *sqlc.Queries, _ pgx.Tx) error {
		_, err := q.InsertQuote(ctx, sqlc.InsertQuoteParams{
			ID: rec.ID, OrgID: rec.OrgID, UserID: rec.UserID, Lane: rec.Lane, EstimatedCredits: rec.Expected, CapCredits: rec.Cap,
			Inputs: nonEmpty(rec.Request), Signature: []byte(rec.Signature), Fingerprint: strPtr(rec.Fingerprint), Quote: quote,
			RunID: rec.RunID, Kind: rec.Kind, ExpiresAt: rec.ExpiresAt,
		})
		return err
	})
}

func nonEmpty(raw json.RawMessage) []byte {
	if len(raw) == 0 {
		return []byte("{}")
	}
	return raw
}

// GetQuote implements estimate.Store.
func (s *Store) GetQuote(ctx context.Context, orgID, id uuid.UUID) (estimate.Record, bool, error) {
	var out estimate.Record
	found := false
	err := s.org(ctx, orgID, func(ctx context.Context, q *sqlc.Queries, _ pgx.Tx) error {
		row, err := q.GetQuoteByID(ctx, sqlc.GetQuoteByIDParams{OrgID: orgID, ID: id})
		if isNoRows(err) {
			return nil
		}
		if err != nil {
			return err
		}
		out, err = toQuote(row)
		found = err == nil
		return err
	})
	return out, found, err
}

// SetQuoteStatus implements estimate.Store.
func (s *Store) SetQuoteStatus(ctx context.Context, orgID, id uuid.UUID, status string, runID *uuid.UUID) error {
	return s.org(ctx, orgID, func(ctx context.Context, q *sqlc.Queries, _ pgx.Tx) error {
		_, err := q.SetQuoteStatus(ctx, sqlc.SetQuoteStatusParams{OrgID: orgID, ID: id, Status: status, RunID: runID})
		if isNoRows(err) {
			return nil
		}
		return err
	})
}

// LatestModelPrices implements estimate.Store.
func (s *Store) LatestModelPrices(ctx context.Context) ([]estimate.ModelPrice, error) {
	var out []estimate.ModelPrice
	err := s.system(ctx, func(ctx context.Context, q *sqlc.Queries) error {
		rows, err := q.ListLatestModelPrices(ctx)
		if err != nil {
			return err
		}
		for _, r := range rows {
			out = append(out, estimate.ModelPrice{Model: r.Model, InputCentsPerMTok: r.InputCentsPerMtok, OutputCentsPerMTok: r.OutputCentsPerMtok, Source: r.Source})
		}
		return nil
	})
	return out, err
}

// SeedModelPrices implements estimate.Store.
func (s *Store) SeedModelPrices(ctx context.Context, rows []estimate.ModelPrice) error {
	return s.system(ctx, func(ctx context.Context, q *sqlc.Queries) error {
		n, err := q.CountModelPrices(ctx)
		if err != nil || n > 0 {
			return err
		}
		for _, r := range rows {
			if err := q.InsertModelPrice(ctx, sqlc.InsertModelPriceParams{Model: r.Model, InputCentsPerMtok: r.InputCentsPerMTok, OutputCentsPerMtok: r.OutputCentsPerMTok, Source: r.Source}); err != nil {
				return err
			}
		}
		return nil
	})
}

// RecentOverruns implements estimate.Store.
func (s *Store) RecentOverruns(ctx context.Context, since time.Time, limit int) ([]estimate.Overrun, error) {
	var out []estimate.Overrun
	err := s.system(ctx, func(ctx context.Context, q *sqlc.Queries) error {
		rows, err := q.ListRecentRunOverruns(ctx, sqlc.ListRecentRunOverrunsParams{FinishedAt: &since, Limit: int32(min(max(limit, 1), 100000))}) //nolint:gosec // bounded
		if err != nil {
			return err
		}
		for _, r := range rows {
			out = append(out, estimate.Overrun{Estimated: r.EstimatedCredits, Spent: r.CreditsSpent})
		}
		return nil
	})
	return out, err
}
