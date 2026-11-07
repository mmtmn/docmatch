// Package extensions issues narrowly scoped, revocable browser-extension sessions.
// Browser cookies and extension bearer credentials never share their secret hashes.
package extensions

import (
	"context"
	"encoding/base64"
	"errors"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"net/http"

	contracts "github.com/mmtmn/docmatch/packages/contracts/gen"
	"github.com/mmtmn/docmatch/services/api/internal/config"
	"github.com/mmtmn/docmatch/services/api/internal/crypto"
	"github.com/mmtmn/docmatch/services/api/internal/db"
	"github.com/mmtmn/docmatch/services/api/internal/geo"
	"github.com/mmtmn/docmatch/services/api/internal/httpx"
	"github.com/mmtmn/docmatch/services/api/internal/ratelimit"
	"github.com/mmtmn/docmatch/services/api/internal/runs/apix"
	"github.com/mmtmn/docmatch/services/api/internal/session"
)

const accessPrefix = "dmxA1_"
const refreshPrefix = "dmxR1_"
const retryTTL = 2 * time.Minute

// Service keeps no credential state in memory: restart and concurrent refresh
// requests are handled by the database transaction and the encrypted retry cache.
type Service struct {
	pool     *pgxpool.Pool
	clients  map[string]config.ExtensionClient
	retryKey []byte
	limit    httpx.Middleware
	rollout  *config.AccountRollout
}

func New(cfg *config.Config, pool *pgxpool.Pool) (*Service, error) {
	keys, err := crypto.NewKeyring(cfg.Auth.SessionSecret.Reveal())
	if err != nil {
		return nil, err
	}
	clients := map[string]config.ExtensionClient{}
	for _, c := range cfg.Auth.ExtensionClients {
		clients[c.ID] = c
	}
	edge := geo.New(cfg.OriginSecret.Reveal(), geo.WithHeaderName(cfg.OriginSecretHeader))
	limit := ratelimit.Middleware(ratelimit.NewMemory(), func(r *http.Request) (string, ratelimit.Policy, bool) {
		return "extension:" + edge.ClientIP(r).String(), ratelimit.Policy{Limit: 60, Period: time.Minute}, true
	})
	return &Service{pool: pool, clients: clients, retryKey: keys.Key("docmatch/extension-refresh-retry/v1"), limit: limit, rollout: cfg.Rollouts.Extensions}, nil
}

func invalid() error { return apix.ErrUnauthenticated }
func validation() error {
	return apix.Validation(apix.FieldError{Path: "/authorization", Code: "invalid"})
}
func (s *Service) Client(id, redirect string) (config.ExtensionClient, error) {
	c, ok := s.clients[id]
	if !ok || !slices.Contains(c.RedirectURIs, redirect) {
		return config.ExtensionClient{}, validation()
	}
	return c, nil
}

// AuthorizeRequest uses S256 only. The caller must show an explicit consent action.
type AuthorizeRequest struct {
	ClientID            string `json:"client_id"`
	RedirectURI         string `json:"redirect_uri"`
	CodeChallenge       string `json:"code_challenge"`
	CodeChallengeMethod string `json:"code_challenge_method"`
	State               string `json:"state"`
}

func (s *Service) Authorize(ctx context.Context, p session.Principal, in AuthorizeRequest) (string, error) {
	if _, err := s.Client(in.ClientID, in.RedirectURI); err != nil {
		return "", err
	}
	if in.CodeChallengeMethod != "S256" || !crypto.ValidToken(in.CodeChallenge, 32) || !crypto.ValidToken(in.State, 32) {
		return "", validation()
	}
	code := crypto.RandomToken(32)
	err := db.WithSystemTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		// Serialize this user's issuance limit; inactive or unverified identities cannot authorize.
		var active bool
		err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM sessions WHERE id=$2 AND user_id=$1 AND revoked_at IS NULL AND expires_at>clock_timestamp() AND access_expires_at>clock_timestamp())
   FROM users WHERE id=$1 AND email_verified_at IS NOT NULL AND deleted_at IS NULL AND suspended_at IS NULL AND deletion_requested_at IS NULL FOR UPDATE`, p.UserID, p.SessionID).Scan(&active)
		if errors.Is(err, pgx.ErrNoRows) || err == nil && !active {
			return apix.ErrForbidden
		}
		if err != nil {
			return err
		}
		if s.rollout != nil && !s.rollout.All {
			rows, err := tx.Query(ctx, `SELECT m.org_id FROM memberships m JOIN orgs o ON o.id=m.org_id WHERE m.user_id=$1 AND o.deleted_at IS NULL`, p.UserID)
			if err != nil {
				return err
			}
			eligible := false
			for rows.Next() {
				var org uuid.UUID
				if err := rows.Scan(&org); err != nil {
					rows.Close()
					return err
				}
				eligible = eligible || s.rollout.Allows(org)
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return err
			}
			if !eligible {
				return apix.E(contracts.ErrFeaturePaused, nil)
			}
		}
		var count int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM extension_codes WHERE user_id=$1 AND created_at>now()-interval '1 minute'`, p.UserID).Scan(&count); err != nil {
			return err
		}
		if count >= 10 {
			return apix.E(contracts.ErrRateLimited, nil)
		}
		_, err = tx.Exec(ctx, `INSERT INTO extension_codes(code_hash,client_id,redirect_uri,challenge,user_id,org_id,authorizing_session) VALUES($1,$2,$3,$4,$5,$6,$7)`, crypto.Digest(code), in.ClientID, in.RedirectURI, in.CodeChallenge, p.UserID, nullableID(p.OrgID), p.SessionID)
		return err
	})
	if err != nil {
		return "", err
	}
	target, err := url.Parse(in.RedirectURI)
	if err != nil {
		return "", err
	}
	q := target.Query()
	q.Set("code", code)
	q.Set("state", in.State)
	target.RawQuery = q.Encode()
	return target.String(), nil
}

func nullableID(id uuid.UUID) *uuid.UUID {
	if id == uuid.Nil {
		return nil
	}
	return &id
}
func verifierValid(v string) bool {
	if len(v) < 43 || len(v) > 128 {
		return false
	}
	for _, c := range v {
		valid := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("-._~", c)
		if !valid {
			return false
		}
	}
	return true
}
func challenge(verifier string) string {
	return base64.RawURLEncoding.EncodeToString(crypto.Digest(verifier))
}

type TokenRequest struct {
	GrantType    string `json:"grant_type"`
	ClientID     string `json:"client_id"`
	Code         string `json:"code"`
	RedirectURI  string `json:"redirect_uri"`
	CodeVerifier string `json:"code_verifier"`
	RefreshToken string `json:"refresh_token"`
	RequestID    string `json:"request_id"`
}
type Tokens struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	// Absolute times let an interrupted client retry without extending old access.
	ExpiresAt        time.Time `json:"expires_at"`
	RefreshExpiresAt time.Time `json:"refresh_expires_at"`
}

func tokens(id uuid.UUID, now time.Time) Tokens {
	return Tokens{AccessToken: accessPrefix + id.String() + "." + crypto.RandomToken(32), RefreshToken: refreshPrefix + id.String() + "." + crypto.RandomToken(32), TokenType: "Bearer", ExpiresIn: int(session.DefaultAccessTTL / time.Second), ExpiresAt: now.Add(session.DefaultAccessTTL), RefreshExpiresAt: now.Add(session.DefaultRefreshTTL)}
}
func tokenID(raw, prefix string) (uuid.UUID, error) {
	if !strings.HasPrefix(raw, prefix) {
		return uuid.Nil, invalid()
	}
	parts := strings.Split(strings.TrimPrefix(raw, prefix), ".")
	if len(parts) != 2 || !crypto.ValidToken(parts[1], 32) {
		return uuid.Nil, invalid()
	}
	id, err := uuid.Parse(parts[0])
	if err != nil || id.String() != parts[0] {
		return uuid.Nil, invalid()
	}
	return id, nil
}

func (s *Service) Exchange(ctx context.Context, in TokenRequest) (Tokens, error) {
	if _, err := s.Client(in.ClientID, in.RedirectURI); err != nil {
		return Tokens{}, invalid()
	}
	if !crypto.ValidToken(in.Code, 32) || !verifierValid(in.CodeVerifier) {
		return Tokens{}, invalid()
	}
	var out Tokens
	var rejected bool
	err := db.WithSystemTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		var user, source uuid.UUID
		var org, issued *uuid.UUID
		var expected, client, redirect string
		var expires time.Time
		var consumed *time.Time
		err := tx.QueryRow(ctx, `SELECT user_id,org_id,authorizing_session,issued_session,challenge,client_id,redirect_uri,expires_at,consumed_at FROM extension_codes WHERE code_hash=$1 FOR UPDATE`, crypto.Digest(in.Code)).Scan(&user, &org, &source, &issued, &expected, &client, &redirect, &expires, &consumed)
		if errors.Is(err, pgx.ErrNoRows) {
			return invalid()
		}
		if err != nil {
			return err
		}
		if client != in.ClientID || redirect != in.RedirectURI || !crypto.EqualString(expected, challenge(in.CodeVerifier)) {
			return invalid()
		}
		// Reusing a valid consumed code revokes the credentials issued from it (RFC 6749).
		if consumed != nil {
			rejected = true
			if issued != nil {
				_, err = tx.Exec(ctx, `UPDATE sessions SET revoked_at=COALESCE(revoked_at,now()) WHERE id=$1`, *issued)
			}
			return err
		}
		var now time.Time
		var active bool
		err = tx.QueryRow(ctx, `SELECT clock_timestamp(),EXISTS(SELECT 1 FROM sessions s JOIN users u ON u.id=s.user_id WHERE s.id=$1 AND s.user_id=$2 AND s.revoked_at IS NULL AND s.expires_at>clock_timestamp() AND u.email_verified_at IS NOT NULL AND u.deleted_at IS NULL AND u.suspended_at IS NULL AND u.deletion_requested_at IS NULL)`, source, user).Scan(&now, &active)
		if err != nil {
			return err
		}
		if !active || !now.Before(expires) {
			return invalid()
		}
		id := uuid.New()
		out = tokens(id, now)
		// Unexposed, independent cookie hashes prevent swapping a bearer token into a cookie.
		_, err = tx.Exec(ctx, `INSERT INTO sessions(id,user_id,org_id,family_id,device_hash,refresh_hash,access_hash,access_expires_at,expires_at,user_agent) VALUES($1,$2,$3,$1,$4,$5,$6,$7,$8,$9)`, id, user, org, crypto.Digest(crypto.RandomToken(32)), crypto.Digest(crypto.RandomToken(32)), crypto.Digest(crypto.RandomToken(32)), out.ExpiresAt, out.RefreshExpiresAt, "docmatch · "+s.clients[client].Name)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO extension_grants(session_id,client_id,access_hash,refresh_hash) VALUES($1,$2,$3,$4)`, id, client, crypto.Digest(out.AccessToken), crypto.Digest(out.RefreshToken)); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE extension_codes SET consumed_at=now(),issued_session=$2 WHERE code_hash=$1`, crypto.Digest(in.Code), id)
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
