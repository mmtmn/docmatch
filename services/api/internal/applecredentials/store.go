// Package applecredentials stores encrypted Apple grants and retries revocation
// without keeping the deleted user's identity in the queue or its job arguments.
package applecredentials

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mmtmn/docmatch/services/api/internal/crypto"
	"github.com/mmtmn/docmatch/services/api/internal/db"
)

const (
	maxTokenBytes = 16384
	batchSize     = 20
	retryLifetime = 7 * 24 * time.Hour
)

// Revoker sends a grant to the provider. An error leaves it pending.
type Revoker func(context.Context, string, string) error

// Store contains no plaintext credentials outside an individual operation.
type Store struct {
	pool   *pgxpool.Pool
	key    []byte
	revoke Revoker
	log    *slog.Logger
}

// New uses the existing server keyring; no additional service or secret is needed.
func New(pool *pgxpool.Pool, keys *crypto.Keyring, revoke Revoker, log *slog.Logger) *Store {
	if log == nil {
		log = slog.Default()
	}
	return &Store{pool: pool, key: keys.Key(crypto.PurposeAppleToken), revoke: revoke, log: log}
}

func aad(id uuid.UUID, clientID string) []byte {
	return []byte("apple/v1/" + id.String() + "/" + clientID)
}

// QueueRevocation keeps an unused grant without retaining an account identity.
func (s *Store) QueueRevocation(ctx context.Context, clientID, token string) error {
	if token == "" || len(token) > maxTokenBytes || clientID == "" || len(clientID) > 255 {
		return errors.New("applecredentials: invalid grant")
	}
	id := uuid.New()
	sealed, err := crypto.Seal(s.key, []byte(token), aad(id, clientID))
	if err != nil {
		return err
	}
	return db.WithSystemTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO apple_credentials(id,client_id,sealed_token,next_attempt_at,expires_at)
 VALUES($1,$2,$3,now(),now()+$4::interval)`, id, clientID, sealed, fmt.Sprintf("%d seconds", int(retryLifetime.Seconds())))
		return err
	})
}

// Save serializes with account deletion, so a late login cannot attach another
// grant after deletion was requested. A new login replaces the same client's grant.
func (s *Store) Save(ctx context.Context, userID uuid.UUID, clientID, token string) error {
	if token == "" || len(token) > maxTokenBytes || clientID == "" || len(clientID) > 255 {
		return errors.New("applecredentials: invalid grant")
	}
	id := uuid.New()
	sealed, err := crypto.Seal(s.key, []byte(token), aad(id, clientID))
	if err != nil {
		return err
	}
	return db.WithSystemTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		var active bool
		err := tx.QueryRow(ctx, `SELECT deletion_requested_at IS NULL AND deleted_at IS NULL AND suspended_at IS NULL FROM users WHERE id=$1 FOR UPDATE`, userID).Scan(&active)
		if err != nil {
			return err
		}
		if !active {
			return errors.New("applecredentials: account unavailable")
		}
		_, err = tx.Exec(ctx, `INSERT INTO apple_credentials(id,user_id,client_id,sealed_token) VALUES($1,$2,$3,$4)
 ON CONFLICT(user_id,client_id) DO UPDATE SET id=EXCLUDED.id,sealed_token=EXCLUDED.sealed_token`, id, userID, clientID, sealed)
		if err != nil {
			return err
		}
		return nil
	})
}

// Detach runs in the deletion transaction. Revocation starts at the request,
// independently of the account's erasure grace period and Apple availability.
func Detach(ctx context.Context, tx pgx.Tx, userID uuid.UUID) error {
	_, err := tx.Exec(ctx, `UPDATE apple_credentials SET user_id=NULL,next_attempt_at=now(),expires_at=now()+$2::interval WHERE user_id=$1`, userID, fmt.Sprintf("%d seconds", int(retryLifetime.Seconds())))
	return err
}

// Sweep processes a bounded batch. SKIP LOCKED permits more than one worker;
// holding only the detached grant lock prevents concurrent duplicate revocations.
func (s *Store) Sweep(ctx context.Context) error {
	for range batchSize {
		processed, err := s.one(ctx)
		if err != nil {
			return err
		}
		if !processed {
			return nil
		}
	}
	return nil
}

func (s *Store) one(ctx context.Context) (bool, error) {
	processed := false
	err := db.WithSystemTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		var id uuid.UUID
		var clientID string
		var sealed []byte
		var attempts int
		var expired bool
		err := tx.QueryRow(ctx, `SELECT id,client_id,sealed_token,attempts,expires_at<=now() FROM apple_credentials
 WHERE user_id IS NULL AND next_attempt_at<=now() ORDER BY next_attempt_at LIMIT 1 FOR UPDATE SKIP LOCKED`).Scan(&id, &clientID, &sealed, &attempts, &expired)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if !expired {
			plain, openErr := crypto.Open(s.key, sealed, aad(id, clientID))
			err = openErr
			if err == nil {
				if s.revoke == nil {
					err = errors.New("applecredentials: provider unavailable")
				} else {
					err = s.revoke(ctx, clientID, string(plain))
				}
			}
			clear(plain)
		}
		if expired || err == nil {
			if expired {
				s.log.Warn("Apple revocation retry expired", "credential_id", id.String())
			}
			_, err = tx.Exec(ctx, `DELETE FROM apple_credentials WHERE id=$1`, id)
		} else {
			// Never log provider errors, token bodies or identifying account data.
			s.log.Warn("Apple revocation pending", "credential_id", id.String())
			delay := min(5*time.Minute*time.Duration(1<<min(attempts, 7)), 6*time.Hour)
			_, err = tx.Exec(ctx, `UPDATE apple_credentials SET attempts=attempts+1,next_attempt_at=least(expires_at,now()+$2::interval) WHERE id=$1`, id, fmt.Sprintf("%d seconds", int(delay.Seconds())))
		}
		if err != nil {
			return err
		}
		processed = true
		return nil

	})
	return processed, err
}
