// Package apikeys manages revocable, scoped credentials for paid organizations.
// API credentials never become browser sessions or authorize billing/account changes.
package apikeys

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	contracts "github.com/mmtmn/docmatch/packages/contracts/gen"
	"github.com/mmtmn/docmatch/services/api/internal/account"
	"github.com/mmtmn/docmatch/services/api/internal/config"
	dmcrypto "github.com/mmtmn/docmatch/services/api/internal/crypto"
	"github.com/mmtmn/docmatch/services/api/internal/db"
	"github.com/mmtmn/docmatch/services/api/internal/entitlement"
	"github.com/mmtmn/docmatch/services/api/internal/httpx"
	"github.com/mmtmn/docmatch/services/api/internal/ratelimit"
	"github.com/mmtmn/docmatch/services/api/internal/runs/apix"
	"github.com/mmtmn/docmatch/services/api/internal/session"
)

var supportedScopes = []string{"runs:read", "runs:write", "uploads:write", "workflows:read", "workflows:write", "rules:read", "rules:write"}

type Service struct {
	cfg     *config.Config
	catalog *account.Catalog
	pool    *pgxpool.Pool
	limiter ratelimit.Limiter
}

func New(cfg *config.Config, catalog *account.Catalog, pool *pgxpool.Pool) *Service {
	return &Service{cfg: cfg, catalog: catalog, pool: pool, limiter: ratelimit.NewMemory()}
}

type Key struct {
	ID         string     `json:"id"`
	Label      string     `json:"label"`
	Prefix     string     `json:"prefix"`
	Scopes     []string   `json:"scopes"`
	Lane       string     `json:"lane"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
}
type Access struct {
	CanCreate bool     `json:"can_create"`
	Eligible  bool     `json:"eligible"`
	Limit     int      `json:"limit"`
	Lanes     []string `json:"lanes"`
	Scopes    []string `json:"scopes"`
}
type CreateRequest struct {
	Label  string   `json:"label"`
	Scopes []string `json:"scopes"`
	Lane   string   `json:"lane"`
}
type Created struct {
	Key
	Secret string `json:"key"`
}

// entitlement is checked on every use, so cancellation, suspension and downgrade
// take effect without waiting for a token/cache expiry. Test exemption is server-only.
func (s *Service) entitlement(ctx context.Context, tx pgx.Tx, org uuid.UUID) (string, bool, error) {
	plan, eligible, err := entitlement.Paid(ctx, tx, s.cfg, org)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, apix.ErrForbidden
	}
	return plan, eligible, err
}
func manager(ctx context.Context, tx pgx.Tx, p session.Principal) error {
	var allowed bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM memberships m JOIN users u ON u.id=m.user_id
 WHERE m.org_id=$1 AND m.user_id=$2 AND m.role IN ('owner','admin') AND u.email_verified_at IS NOT NULL
 AND u.suspended_at IS NULL AND u.deletion_requested_at IS NULL)`, p.OrgID, p.UserID).Scan(&allowed)
	if err != nil {
		return err
	}
	if !allowed {
		return apix.ErrForbidden
	}
	return nil
}
func (s *Service) access(ctx context.Context, tx pgx.Tx, p session.Principal) (Access, error) {
	if err := manager(ctx, tx, p); err != nil {
		return Access{}, err
	}
	plan, eligible, err := s.entitlement(ctx, tx, p.OrgID)
	if err != nil {
		return Access{}, err
	}
	ent, ok := s.catalog.For(plan)
	if !ok {
		return Access{}, apix.ErrForbidden
	}
	var count int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM api_keys WHERE org_id=$1 AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at>now())`, p.OrgID).Scan(&count); err != nil {
		return Access{}, err
	}
	return Access{CanCreate: eligible && count < ent.APIKeys && s.cfg.Signing.APIKeyPepper.IsSet(), Eligible: eligible, Limit: ent.APIKeys, Lanes: ent.APILanes, Scopes: supportedScopes}, nil
}
func (s *Service) digest(secret string) []byte {
	h := hmac.New(sha256.New, []byte(s.cfg.Signing.APIKeyPepper.Reveal()))
	_, _ = h.Write([]byte(secret))
	return h.Sum(nil)
}
func validate(v CreateRequest) error {
	if n := utf8.RuneCountInString(v.Label); n < 1 || n > 60 || len(v.Scopes) < 1 || len(v.Scopes) > 10 {
		return apix.E(contracts.ErrValidationFailed, nil)
	}
	seen := map[string]bool{}
	for _, scope := range v.Scopes {
		if !slices.Contains(supportedScopes, scope) || seen[scope] {
			return apix.E(contracts.ErrValidationFailed, nil)
		}
		seen[scope] = true
	}
	return nil
}
func (s *Service) Create(ctx context.Context, p session.Principal, v CreateRequest) (Created, error) {
	v.Label = strings.TrimSpace(v.Label)
	if v.Lane == "" {
		v.Lane = "api_batch"
	}
	if err := validate(v); err != nil {
		return Created{}, err
	}
	var out Created
	err := db.WithOrgTx(ctx, s.pool, p.OrgID, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "api-keys:"+p.OrgID.String()); err != nil {
			return err
		}
		a, err := s.access(ctx, tx, p)
		if err != nil {
			return err
		}
		if !a.Eligible {
			return apix.E(contracts.ErrSubscriptionRequired, nil)
		}
		if !s.cfg.Signing.APIKeyPepper.IsSet() {
			return apix.E(contracts.ErrServiceUnavailable, nil)
		}
		if !a.CanCreate {
			return apix.E(contracts.ErrAPIKeysLimitReached, nil)
		}
		if !slices.Contains(a.Lanes, v.Lane) {
			return apix.E(contracts.ErrLaneNotAllowed, nil)
		}
		id, err := uuid.NewV7()
		if err != nil {
			return err
		}
		publicID := apix.Encode(apix.PrefixAPIKey, id)
		prefix := "dm_live_" + strings.TrimPrefix(publicID, "key_")
		secret := prefix + "_" + dmcrypto.RandomToken(32)
		out = Created{Key: Key{ID: publicID, Label: v.Label, Prefix: prefix, Scopes: v.Scopes, Lane: v.Lane}, Secret: secret}
		return tx.QueryRow(ctx, `INSERT INTO api_keys(id,org_id,label,prefix,key_hash,scopes,lanes,created_by)
  VALUES($1,$2,$3,$4,$5,$6,$7,$8) RETURNING created_at`, id, p.OrgID, v.Label, prefix, s.digest(secret), v.Scopes, []string{v.Lane}, p.UserID).Scan(&out.CreatedAt)
	})
	if err != nil {
		return Created{}, err
	}
	return out, nil
}
func (s *Service) Revoke(ctx context.Context, p session.Principal, id uuid.UUID) error {
	return db.WithOrgTx(ctx, s.pool, p.OrgID, func(ctx context.Context, tx pgx.Tx) error {
		if err := manager(ctx, tx, p); err != nil {
			return err
		}
		result, err := tx.Exec(ctx, `UPDATE api_keys SET revoked_at=COALESCE(revoked_at,now()) WHERE id=$1 AND org_id=$2`, id, p.OrgID)
		if err == nil && result.RowsAffected() == 0 {
			return apix.E(contracts.ErrNotFound, nil)
		}
		return err
	})
}
func requiredScope(r *http.Request) string {
	path := strings.TrimPrefix(r.URL.Path, "/v1/")
	group, _, _ := strings.Cut(path, "/")
	switch group {
	case "uploads":
		return "uploads:write"
	case "runs", "workflows", "rules":
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			return group + ":read"
		}
		return group + ":write"
	}
	return ""
}
func parse(secret string) (uuid.UUID, error) {
	if len(secret) != 78 || !strings.HasPrefix(secret, "dm_live_") || secret[34] != '_' {
		return uuid.Nil, apix.ErrInvalidAPIKey
	}
	id, err := apix.Decode(apix.PrefixAPIKey, "key_"+secret[8:34])
	if err != nil {
		return uuid.Nil, apix.ErrInvalidAPIKey
	}
	return id, nil
}
func (s *Service) Resolve(next apix.Resolver) apix.Resolver {
	return func(r *http.Request) (apix.Principal, error) {
		auth := r.Header.Get("Authorization")
		if auth == "" {
			return next(r)
		}
		parts := strings.Fields(auth)
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || !s.cfg.Signing.APIKeyPepper.IsSet() {
			return apix.Principal{}, apix.ErrInvalidAPIKey
		}
		id, err := parse(parts[1])
		if err != nil {
			return apix.Principal{}, err
		}
		var p apix.Principal
		err = db.WithSystemTx(r.Context(), s.pool, func(ctx context.Context, tx pgx.Tx) error {
			var hash []byte
			var lanes []string
			err := tx.QueryRow(ctx, `SELECT k.org_id,k.key_hash,k.scopes,k.lanes FROM api_keys k
 JOIN users u ON u.id=k.created_by JOIN memberships m ON m.user_id=u.id AND m.org_id=k.org_id
 WHERE k.id=$1 AND k.revoked_at IS NULL AND (k.expires_at IS NULL OR k.expires_at>now())
 AND u.suspended_at IS NULL AND u.deletion_requested_at IS NULL AND m.role IN ('owner','admin')`, id).Scan(&p.OrgID, &hash, &p.Scopes, &lanes)
			if errors.Is(err, pgx.ErrNoRows) {
				return apix.ErrInvalidAPIKey
			}
			if err != nil {
				return err
			}
			if !hmac.Equal(hash, s.digest(parts[1])) {
				return apix.ErrInvalidAPIKey
			}
			plan, eligible, err := s.entitlement(ctx, tx, p.OrgID)
			if err != nil {
				return err
			}
			if !eligible {
				return apix.E(contracts.ErrSubscriptionRequired, nil)
			}
			ent, ok := s.catalog.For(plan)
			if !ok {
				return apix.ErrForbidden
			}
			for _, lane := range lanes {
				if slices.Contains(ent.APILanes, lane) {
					p.Lanes = append(p.Lanes, lane)
				}
			}
			if len(p.Lanes) == 0 {
				return apix.E(contracts.ErrLaneNotAllowed, nil)
			}
			scope := requiredScope(r)
			if scope == "" || !slices.Contains(p.Scopes, scope) {
				return apix.ErrScopeDenied
			}
			d, err := s.limiter.Allow(ctx, id.String(), ratelimit.Policy{Limit: 60, Period: time.Minute})
			if err != nil {
				return err
			}
			if !d.Allowed {
				return apix.E(contracts.ErrRateLimited, nil)
			}
			p.Plan = plan
			p.APIKeyID = &id
			_, err = tx.Exec(ctx, `UPDATE api_keys SET last_used_at=now() WHERE id=$1`, id)
			return err
		})
		if err != nil {
			return apix.Principal{}, err
		}
		return p, nil
	}
}
func (s *Service) Mount(r *httpx.Router) {
	r.Handle(http.MethodGet, "/v1/api-keys", httpx.BodyNone, session.RequireFunc(s.list))
	r.Handle(http.MethodPost, "/v1/api-keys", httpx.BodyJSON, session.RequireFunc(s.create))
	r.Handle(http.MethodDelete, "/v1/api-keys/{id}", httpx.BodyNone, session.RequireFunc(s.revoke))
}
func (s *Service) list(w http.ResponseWriter, r *http.Request) {
	p, _ := session.FromContext(r.Context())
	items := []Key{}
	var access Access
	err := db.WithOrgTx(r.Context(), s.pool, p.OrgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		access, err = s.access(ctx, tx, p)
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT id,label,prefix,scopes,lanes[1],created_at,last_used_at FROM api_keys WHERE org_id=$1 AND revoked_at IS NULL ORDER BY created_at DESC`, p.OrgID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var k Key
			var id uuid.UUID
			if err = rows.Scan(&id, &k.Label, &k.Prefix, &k.Scopes, &k.Lane, &k.CreatedAt, &k.LastUsedAt); err != nil {
				return err
			}
			k.ID = apix.Encode(apix.PrefixAPIKey, id)
			items = append(items, k)
		}
		return rows.Err()
	})
	if err != nil {
		apix.Handle(w, r, err)
		return
	}
	apix.WriteJSON(w, 200, map[string]any{"items": items, "access": access})
}
func (s *Service) create(w http.ResponseWriter, r *http.Request) {
	var v CreateRequest
	if err := httpx.DecodeJSON(r, &v); err != nil {
		apix.Handle(w, r, err)
		return
	}
	p, _ := session.FromContext(r.Context())
	out, err := s.Create(r.Context(), p, v)
	if err != nil {
		apix.Handle(w, r, err)
		return
	}
	apix.WriteJSON(w, 201, out)
}
func (s *Service) revoke(w http.ResponseWriter, r *http.Request) {
	id, err := apix.Decode(apix.PrefixAPIKey, r.PathValue("id"))
	if err != nil {
		apix.Handle(w, r, apix.E(contracts.ErrNotFound, nil))
		return
	}
	p, _ := session.FromContext(r.Context())
	if err = s.Revoke(r.Context(), p, id); err != nil {
		apix.Handle(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
