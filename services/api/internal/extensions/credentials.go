package extensions

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/mmtmn/docmatch/services/api/internal/crypto"
	"github.com/mmtmn/docmatch/services/api/internal/db"
	"github.com/mmtmn/docmatch/services/api/internal/runs/apix"
	"github.com/mmtmn/docmatch/services/api/internal/session"
)

// Refresh serializes rotation. A persisted request UUID makes the one permitted
// retry return identical credentials when the browser lost the first response.
// A different request with the previous secret revokes the whole session.
func (s *Service) Refresh(ctx context.Context, in TokenRequest) (Tokens, error) {
	id, err := tokenID(in.RefreshToken, refreshPrefix)
	if err != nil {
		return Tokens{}, err
	}
	requestID, err := uuid.Parse(in.RequestID)
	if err != nil || requestID == uuid.Nil {
		return Tokens{}, validation()
	}
	if _, ok := s.clients[in.ClientID]; !ok {
		return Tokens{}, invalid()
	}
	var out Tokens
	var rejected bool
	err = db.WithSystemTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		var current, previous, cached []byte
		var client string
		var request *uuid.UUID
		var retryUntil, revoked *time.Time
		var expires time.Time
		err := tx.QueryRow(ctx, `SELECT g.refresh_hash,g.previous_refresh_hash,g.retry_response,g.client_id,g.refresh_request_id,g.retry_until,s.revoked_at,s.expires_at
   FROM extension_grants g JOIN sessions s ON s.id=g.session_id JOIN users u ON u.id=s.user_id
   WHERE g.session_id=$1 AND u.email_verified_at IS NOT NULL AND u.deleted_at IS NULL AND u.suspended_at IS NULL AND u.deletion_requested_at IS NULL FOR UPDATE OF s,g`, id).Scan(&current, &previous, &cached, &client, &request, &retryUntil, &revoked, &expires)
		if errors.Is(err, pgx.ErrNoRows) {
			return invalid()
		}
		if err != nil {
			return err
		}
		var now time.Time
		if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
			return err
		}
		if client != in.ClientID || revoked != nil || !now.Before(expires) {
			return invalid()
		}
		digest := crypto.Digest(in.RefreshToken)
		if !crypto.Equal(current, digest) {
			if crypto.Equal(previous, digest) && request != nil && *request == requestID && retryUntil != nil && now.Before(*retryUntil) {
				plain, err := crypto.Open(s.retryKey, cached, []byte(id.String()+":"+requestID.String()))
				if err != nil {
					return err
				}
				return json.Unmarshal(plain, &out)
			}
			// Only possession of the previous issued token proves reuse. A random
			// incorrect secret must not let someone revoke a guessed session UUID.
			var used bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM extension_spent_refresh WHERE session_id=$1 AND token_hash=$2)`, id, digest).Scan(&used); err != nil {
				return err
			}
			if used {
				rejected = true
				_, err = tx.Exec(ctx, `UPDATE sessions SET revoked_at=now() WHERE id=$1`, id)
				return err
			}
			return invalid()
		}
		if _, err := tx.Exec(ctx, `INSERT INTO extension_spent_refresh(session_id,token_hash,expires_at) VALUES($1,$2,$3)`, id, current, expires); err != nil {
			return err
		}
		out = tokens(id, now)
		raw, err := json.Marshal(out) //nolint:gosec // G117: encrypted immediately below; only ciphertext enters storage, never logs.
		if err != nil {
			return err
		}
		sealed, err := crypto.Seal(s.retryKey, raw, []byte(id.String()+":"+requestID.String()))
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE extension_grants SET previous_refresh_hash=refresh_hash,refresh_hash=$2,access_hash=$3,refresh_request_id=$4,retry_until=$5,retry_response=$6 WHERE session_id=$1`, id, crypto.Digest(out.RefreshToken), crypto.Digest(out.AccessToken), requestID, now.Add(retryTTL), sealed)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE sessions SET access_expires_at=$2,expires_at=$3,last_seen_at=now(),rotated_at=now(),rotations=rotations+1 WHERE id=$1`, id, out.ExpiresAt, out.RefreshExpiresAt)
		return err
	})
	if err != nil {
		return Tokens{}, err
	}
	if rejected {
		return Tokens{}, invalid()
	}
	return out, nil
}

func (s *Service) Authenticate(ctx context.Context, raw string) (session.Principal, error) {
	id, err := tokenID(raw, accessPrefix)
	if err != nil {
		return session.Principal{}, err
	}
	var p session.Principal
	var org *uuid.UUID
	var hash []byte
	var client string
	var revoked *time.Time
	var expires, now time.Time
	err = s.pool.QueryRow(ctx, `SELECT s.id,s.user_id,s.org_id,s.created_at,s.access_expires_at,s.expires_at,s.revoked_at,g.access_hash,g.client_id,clock_timestamp()
  FROM extension_grants g JOIN sessions s ON s.id=g.session_id JOIN users u ON u.id=s.user_id
  WHERE g.session_id=$1 AND u.email_verified_at IS NOT NULL AND u.deleted_at IS NULL AND u.suspended_at IS NULL AND u.deletion_requested_at IS NULL`, id).Scan(&p.SessionID, &p.UserID, &org, &p.SessionCreatedAt, &p.AccessExpiresAt, &expires, &revoked, &hash, &client, &now)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, invalid()
	}
	if err != nil {
		return p, err
	}
	if _, ok := s.clients[client]; !ok || revoked != nil || !now.Before(expires) || !crypto.Equal(hash, crypto.Digest(raw)) {
		return p, invalid()
	}
	if !now.Before(p.AccessExpiresAt) {
		return p, apix.ErrSessionExpired
	}
	if org != nil {
		p.OrgID = *org
	}
	// Keep the device list useful without a write on every poll.
	_, err = s.pool.Exec(ctx, `UPDATE sessions SET last_seen_at=now() WHERE id=$1 AND last_seen_at<now()-interval '1 minute'`, id)
	return p, err
}
