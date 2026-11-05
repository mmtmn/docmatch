// Package sessionpg is the PostgreSQL implementation of session.Store over
// the sqlc queries of db/queries/auth_sessions.sql. The sessions table has
// no row-level security (it belongs to the user, who may not have selected
// an organization yet), so every call runs straight on the pool.
package sessionpg

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mmtmn/docmatch/services/api/internal/db/sqlc"
	"github.com/mmtmn/docmatch/services/api/internal/session"
)

// Store implements session.Store.
type Store struct {
	q *sqlc.Queries
}

// New wraps the pool.
func New(pool *pgxpool.Pool) *Store { return &Store{q: sqlc.New(pool)} }

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func strVal(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func uuidPtr(id uuid.UUID) *uuid.UUID {
	if id == uuid.Nil {
		return nil
	}
	return &id
}

func uuidVal(p *uuid.UUID) uuid.UUID {
	if p == nil {
		return uuid.Nil
	}
	return *p
}

func timeVal(p *time.Time) time.Time {
	if p == nil {
		return time.Time{}
	}
	return *p
}

// FromRow converts a sqlc row.
func FromRow(r sqlc.Session) session.Session {
	return session.Session{
		ID: r.ID, UserID: r.UserID, OrgID: uuidVal(r.OrgID), FamilyID: r.FamilyID,
		DeviceHash: r.DeviceHash, RefreshHash: r.RefreshHash, AccessHash: r.AccessHash,
		IPPrefix: strVal(r.IpPrefix), UserAgent: strVal(r.UserAgent),
		CreatedAt: r.CreatedAt, LastSeenAt: r.LastSeenAt, ExpiresAt: r.ExpiresAt, AccessExpiresAt: timeVal(r.AccessExpiresAt),
		RevokedAt: r.RevokedAt, RotatedAt: r.RotatedAt, Rotations: int(r.Rotations),
	}
}

// Insert implements session.Store.
func (s *Store) Insert(ctx context.Context, row session.Session) error {
	access := row.AccessExpiresAt
	_, err := s.q.AuthInsertSession(ctx, sqlc.AuthInsertSessionParams{
		ID: row.ID, UserID: row.UserID, OrgID: uuidPtr(row.OrgID), DeviceHash: row.DeviceHash, RefreshHash: row.RefreshHash,
		AccessHash: row.AccessHash, FamilyID: row.FamilyID, IpPrefix: strPtr(row.IPPrefix), UserAgent: strPtr(row.UserAgent),
		ExpiresAt: row.ExpiresAt, AccessExpiresAt: &access,
	})
	return err
}

// ByID implements session.Store.
func (s *Store) ByID(ctx context.Context, id uuid.UUID) (session.Session, bool, error) {
	row, err := s.q.AuthSessionByID(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return session.Session{}, false, nil
	}
	if err != nil {
		return session.Session{}, false, err
	}
	return FromRow(row), true, nil
}

// Rotate implements session.Store.
func (s *Store) Rotate(ctx context.Context, id uuid.UUID, r session.Rotation) (session.Session, bool, error) {
	access := r.AccessExpiresAt
	row, err := s.q.AuthRotateSession(ctx, sqlc.AuthRotateSessionParams{
		ID: id, RefreshHash: r.RefreshHash, AccessHash: r.AccessHash, ExpiresAt: r.ExpiresAt, AccessExpiresAt: &access,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return session.Session{}, false, nil
	}
	if err != nil {
		return session.Session{}, false, err
	}
	return FromRow(row), true, nil
}

// Touch implements session.Store.
func (s *Store) Touch(ctx context.Context, id uuid.UUID) error { return s.q.AuthTouchSession(ctx, id) }

// Revoke implements session.Store.
func (s *Store) Revoke(ctx context.Context, id uuid.UUID) (int64, error) {
	return s.q.AuthRevokeSession(ctx, id)
}

// RevokeFamily implements session.Store.
func (s *Store) RevokeFamily(ctx context.Context, familyID uuid.UUID) (int64, error) {
	return s.q.AuthRevokeSessionFamily(ctx, familyID)
}

// RevokeUser implements session.Store.
func (s *Store) RevokeUser(ctx context.Context, userID, keep uuid.UUID) (int64, error) {
	return s.q.AuthRevokeUserSessions(ctx, sqlc.AuthRevokeUserSessionsParams{UserID: userID, Keep: uuidPtr(keep)})
}

// ListActive implements session.Store.
func (s *Store) ListActive(ctx context.Context, userID uuid.UUID) ([]session.Session, error) {
	rows, err := s.q.AuthListUserSessions(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]session.Session, 0, len(rows))
	for _, r := range rows {
		out = append(out, FromRow(r))
	}
	return out, nil
}

// DeleteStale removes sessions expired or revoked before the cut-off
// (periodic maintenance).
func (s *Store) DeleteStale(ctx context.Context, before time.Time) (int64, error) {
	return s.q.AuthDeleteStaleSessions(ctx, before)
}
