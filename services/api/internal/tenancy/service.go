package tenancy

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	contracts "github.com/mmtmn/docmatch/packages/contracts/gen"
	"github.com/mmtmn/docmatch/services/api/internal/config"
	"github.com/mmtmn/docmatch/services/api/internal/db"
	"github.com/mmtmn/docmatch/services/api/internal/httpx"
	"github.com/mmtmn/docmatch/services/api/internal/publicid"
	"github.com/mmtmn/docmatch/services/api/internal/runs/apix"
	"github.com/mmtmn/docmatch/services/api/internal/session"
)

type Service struct {
	pool     *pgxpool.Pool
	rollouts config.Rollouts
}

func New(pool *pgxpool.Pool) *Service                 { return &Service{pool: pool} }
func (s *Service) SetRollouts(policy config.Rollouts) { s.rollouts = policy }

type Membership struct {
	ID           string                 `json:"id"`
	Name         string                 `json:"name"`
	Kind         string                 `json:"kind"`
	Role         string                 `json:"role"`
	Plan         string                 `json:"plan"`
	Capabilities Capabilities           `json:"capabilities"`
	Features     config.AccountFeatures `json:"features"`
}

// Check is also called inside project mutations. The shared lock makes removal
// wait for an already authorized transaction, without retaining a cached role.
func Check(ctx context.Context, tx pgx.Tx, org, user uuid.UUID) (string, error) {
	var role string
	err := tx.QueryRow(ctx, `SELECT m.role FROM memberships m
 JOIN users u ON u.id=m.user_id JOIN orgs o ON o.id=m.org_id
 WHERE m.org_id=$1 AND m.user_id=$2 AND o.deleted_at IS NULL AND u.deleted_at IS NULL
 AND u.suspended_at IS NULL AND u.deletion_requested_at IS NULL
 FOR SHARE OF m`, org, user).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", apix.ErrForbidden
	}
	return role, err
}

func (s *Service) List(ctx context.Context, user uuid.UUID) ([]Membership, error) {
	out := []Membership{}
	err := db.WithSystemTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT o.id,o.name,o.kind,m.role,o.plan_code
 FROM memberships m JOIN orgs o ON o.id=m.org_id JOIN users u ON u.id=m.user_id
 WHERE m.user_id=$1 AND o.deleted_at IS NULL AND u.deleted_at IS NULL
 AND u.suspended_at IS NULL AND u.deletion_requested_at IS NULL
 ORDER BY o.kind='personal' DESC,o.name,o.id`, user)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item Membership
			var id uuid.UUID
			if err := rows.Scan(&id, &item.Name, &item.Kind, &item.Role, &item.Plan); err != nil {
				return err
			}
			item.ID = publicid.Encode(publicid.PrefixOrg, id)
			item.Capabilities = Permissions(item.Role)
			item.Features = s.rollouts.For(id)
			item.Capabilities.CreateProject = item.Capabilities.CreateProject && item.Features.Projects
			out = append(out, item)
		}
		return rows.Err()
	})
	return out, err
}

// Identity operations remain usable when the cookie's former account was
// removed. In particular the account selector must allow recovery to a personal
// account; an explicit invalid account header is never silently substituted.
func identityRoute(path string) bool {
	return path == "/v1/integration-interests" || path == "/v1/platforms" || path == "/v1/platform-notices" || strings.HasPrefix(path, "/v1/platform-notices/") || strings.HasPrefix(path, "/v1/extensions/") || strings.HasPrefix(path, "/v1/auth/") || path == "/v1/me/organizations" ||
		path == "/v1/me/sessions" || strings.HasPrefix(path, "/v1/me/sessions/") ||
		path == "/v1/me/password" || path == "/v1/me/devices" || strings.HasPrefix(path, "/v1/me/devices/") ||
		path == "/v1/me/invitations" || strings.HasPrefix(path, "/v1/me/invitations/") || strings.HasPrefix(path, "/v1/invitations/")
}

func (s *Service) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := session.FromContext(r.Context())
		if !ok || identityRoute(r.URL.Path) || !strings.HasPrefix(r.URL.Path, "/v1/") {
			next.ServeHTTP(w, r)
			return
		}
		if value := r.Header.Get(Header); value != "" {
			var err error
			p.OrgID, err = publicid.Decode(publicid.PrefixOrg, value)
			if err != nil {
				apix.Handle(w, r, apix.ErrForbidden)
				return
			}
		}
		var role string
		err := db.WithOrgTx(r.Context(), s.pool, p.OrgID, func(ctx context.Context, tx pgx.Tx) error {
			var err error
			role, err = Check(ctx, tx, p.OrgID, p.UserID)
			return err
		})
		if err != nil {
			apix.Handle(w, r, err)
			return
		}
		if !routeAllowed(role, r) {
			apix.Handle(w, r, apix.ErrForbidden)
			return
		}
		next.ServeHTTP(w, r.WithContext(session.ContextWithPrincipal(r.Context(), p)))
	})
}

func routeAllowed(role string, r *http.Request) bool {
	p := r.URL.Path
	// These services perform action and assignment checks inside transactions.
	if p == "/v1/projects" || strings.HasPrefix(p, "/v1/projects/") ||
		p == "/v1/team" || strings.HasPrefix(p, "/v1/team/") {
		return true
	}
	if p == "/v1/plans" || p == "/v1/version" {
		return true
	}
	if strings.HasPrefix(p, "/v1/api-keys") || strings.HasPrefix(p, "/v1/billing/") ||
		strings.HasPrefix(p, "/v1/customizations") || p == "/v1/workspace/rollback" || p == "/v1/me/export" {
		return Permissions(role).ManageAccount
	}
	if role == "member" && (strings.HasPrefix(p, "/v1/uploads/") || strings.HasPrefix(p, "/v1/runs/") ||
		strings.HasPrefix(p, "/v1/rules/") || strings.HasPrefix(p, "/v1/workflows/")) {
		return true
	}
	return LegacyAllowed(role, r.Method, p)
}

func (s *Service) Mount(r *httpx.Router) {
	r.Handle(http.MethodGet, "/v1/me/organizations", httpx.BodyNone, session.RequireFunc(func(w http.ResponseWriter, r *http.Request) {
		p, _ := session.FromContext(r.Context())
		items, err := s.List(r.Context(), p.UserID)
		if err != nil {
			apix.Handle(w, r, err)
			return
		}
		if len(items) == 0 {
			apix.Write(w, r, contracts.ErrForbidden, nil)
			return
		}
		apix.WriteJSON(w, http.StatusOK, map[string]any{"user_id": publicid.Encode(publicid.PrefixUser, p.UserID), "items": items})
	}))
}
