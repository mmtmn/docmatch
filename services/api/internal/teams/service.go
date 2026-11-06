package teams

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	contracts "github.com/mmtmn/docmatch/packages/contracts/gen"
	"github.com/mmtmn/docmatch/services/api/internal/config"
	"github.com/mmtmn/docmatch/services/api/internal/crypto"
	"github.com/mmtmn/docmatch/services/api/internal/db"
	"github.com/mmtmn/docmatch/services/api/internal/email/emailpg"
	"github.com/mmtmn/docmatch/services/api/internal/entitlement"
	"github.com/mmtmn/docmatch/services/api/internal/httpx"
	"github.com/mmtmn/docmatch/services/api/internal/publicid"
	"github.com/mmtmn/docmatch/services/api/internal/runs/apix"
	"github.com/mmtmn/docmatch/services/api/internal/session"
	"github.com/mmtmn/docmatch/services/api/internal/tenancy"
)

type Service struct {
	pool  *pgxpool.Pool
	cfg   *config.Config
	keys  *crypto.Keyring
	queue *emailpg.Client
}

func New(cfg *config.Config, pool *pgxpool.Pool) (*Service, error) {
	keys, err := crypto.NewKeyring(cfg.Auth.SessionSecret.Reveal())
	if err != nil {
		return nil, err
	}
	queue, err := emailpg.NewInserter(pool)
	if err != nil {
		return nil, err
	}
	return &Service{pool: pool, cfg: cfg, keys: keys, queue: queue}, nil
}

func conflict(reason string) error {
	return apix.E(contracts.ErrConflict, nil).With(map[string]any{"reason": reason})
}
func missing() error { return apix.E(contracts.ErrNotFound, nil) }
func event(ctx context.Context, tx pgx.Tx, p session.Principal, action string, target *uuid.UUID) error {
	_, err := tx.Exec(ctx, `INSERT INTO team_events(org_id,actor_id,action,target_id) VALUES($1,$2,$3,$4)`, p.OrgID, p.UserID, action, target)
	return err
}

// Lock the organization before changing memberships, matching the last-owner
// trigger. Every operation uses live membership, not a role cached in a session.
func manager(ctx context.Context, tx pgx.Tx, p session.Principal) (role, kind string, err error) {
	err = tx.QueryRow(ctx, `SELECT kind FROM orgs WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, p.OrgID).Scan(&kind)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", apix.ErrForbidden
	}
	if err != nil {
		return
	}
	role, err = tenancy.Check(ctx, tx, p.OrgID, p.UserID)
	if err == nil && !tenancy.Permissions(role).ManageAccount {
		err = apix.ErrForbidden
	}
	return
}

func (s *Service) paid(ctx context.Context, tx pgx.Tx, org uuid.UUID) error {
	_, allowed, err := entitlement.Paid(ctx, tx, s.cfg, org)
	if err != nil {
		return err
	}
	if !allowed {
		return apix.E(contracts.ErrSubscriptionRequired, nil)
	}
	return nil
}

// Convert retains the paid organization's ID, subscription, balance and files.
// Only an empty personal organization is added when the owner lacks another.
func (s *Service) Convert(ctx context.Context, p session.Principal, name string) error {
	name, err := httpx.ValidateText(name, contracts.TextCompanyName, true)
	if err != nil || name == "" {
		return apix.Validation(apix.FieldError{Path: "/name", Code: "invalid", Max: contracts.TextCompanyName})
	}
	return db.WithSystemTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		role, kind, err := manager(ctx, tx, p)
		if err != nil {
			return err
		}
		if role != "owner" {
			return apix.ErrForbidden
		}
		if err := s.paid(ctx, tx, p.OrgID); err != nil {
			return err
		}
		var personalName string
		if err := tx.QueryRow(ctx, `SELECT COALESCE(NULLIF(display_name,''),split_part(email::text,'@',1)) FROM users WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, p.UserID).Scan(&personalName); err != nil {
			return err
		}
		if kind == "team" {
			return nil
		}
		var personalExists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM memberships m JOIN orgs o ON o.id=m.org_id WHERE m.user_id=$1 AND m.role='owner' AND o.kind='personal' AND o.id<>$2 AND o.deleted_at IS NULL)`, p.UserID, p.OrgID).Scan(&personalExists); err != nil {
			return err
		}
		if !personalExists {
			id := uuid.New()
			wrapped, err := crypto.WrapDataKey(s.keys.Key(crypto.PurposeKEK), crypto.NewDataKey(), id.String())
			if err != nil {
				return err
			}
			if httpx.CountChars(personalName) > contracts.TextCompanyName {
				personalName = string([]rune(personalName)[:contracts.TextCompanyName])
			}
			if _, err := tx.Exec(ctx, `INSERT INTO orgs(id,name,slug,kind) VALUES($1,$2,$3,'personal')`, id, personalName, strings.ToLower(publicid.Encode(publicid.PrefixOrg, id))); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO memberships(org_id,user_id,role) VALUES($1,$2,'owner')`, id, p.UserID); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO tenant_keys(org_id,wrapped_key,kek_version) VALUES($1,$2,1)`, id, wrapped); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE orgs SET kind='team',name=$2 WHERE id=$1`, p.OrgID, name); err != nil {
			return err
		}
		return event(ctx, tx, p, "team.created", nil)
	})
}

func (s *Service) Get(ctx context.Context, p session.Principal) (View, error) {
	out := View{ID: publicid.Encode(publicid.PrefixOrg, p.OrgID), Members: []Member{}, Invitations: []Invitation{}, MemberLimit: contracts.IDsAccountMembersMax}
	err := db.WithOrgTx(ctx, s.pool, p.OrgID, func(ctx context.Context, tx pgx.Tx) error {
		role, err := tenancy.Check(ctx, tx, p.OrgID, p.UserID)
		if err != nil {
			return err
		}
		out.Role = role
		out.Capabilities = tenancy.Permissions(role)
		if err := tx.QueryRow(ctx, `SELECT name,kind FROM orgs WHERE id=$1`, p.OrgID).Scan(&out.Name, &out.Kind); err != nil {
			return err
		}
		if !out.Capabilities.ManageAccount {
			return nil
		}
		_, out.CanInvite, err = entitlement.Paid(ctx, tx, s.cfg, p.OrgID)
		if err != nil {
			return err
		}
		out.CanInvite = out.CanInvite && out.Kind == "team"
		rows, err := tx.Query(ctx, `SELECT u.id,COALESCE(u.display_name,''),u.email,m.role,m.created_at FROM memberships m JOIN users u ON u.id=m.user_id WHERE m.org_id=$1 AND u.deleted_at IS NULL ORDER BY m.role='owner' DESC,u.email`, p.OrgID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var v Member
			var id uuid.UUID
			if err := rows.Scan(&id, &v.Name, &v.Email, &v.Role, &v.JoinedAt); err != nil {
				rows.Close()
				return err
			}
			v.ID = publicid.Encode(publicid.PrefixUser, id)
			out.Members = append(out.Members, v)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		rows, err = tx.Query(ctx, `SELECT id,email,role,CASE WHEN status='pending' AND expires_at<=now() THEN 'expired' ELSE status END,expires_at,sent_at FROM team_invitations WHERE org_id=$1 ORDER BY created_at DESC LIMIT $2`, p.OrgID, contracts.IDsAccountMembersMax)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var v Invitation
			var id uuid.UUID
			if err := rows.Scan(&id, &v.Email, &v.Role, &v.Status, &v.ExpiresAt, &v.SentAt); err != nil {
				return err
			}
			v.ID = publicid.Encode("inv", id)
			out.Invitations = append(out.Invitations, v)
		}
		return rows.Err()
	})
	return out, err
}

func (s *Service) ChangeRole(ctx context.Context, p session.Principal, user uuid.UUID, role string) error {
	if role != "uploader" && role != "reconciler" {
		return apix.Validation(apix.FieldError{Path: "/role", Code: "invalid"})
	}
	return db.WithOrgTx(ctx, s.pool, p.OrgID, func(ctx context.Context, tx pgx.Tx) error {
		_, kind, err := manager(ctx, tx, p)
		if err != nil {
			return err
		}
		if kind != "team" {
			return conflict("team_required")
		}
		var old string
		if err := tx.QueryRow(ctx, `SELECT role FROM memberships WHERE org_id=$1 AND user_id=$2`, p.OrgID, user).Scan(&old); errors.Is(err, pgx.ErrNoRows) {
			return missing()
		} else if err != nil {
			return err
		}
		if old == "owner" || old == "admin" || user == p.UserID {
			return conflict("owner_transfer_required")
		}
		if _, err := tx.Exec(ctx, `UPDATE memberships SET role=$3 WHERE org_id=$1 AND user_id=$2`, p.OrgID, user, role); err != nil {
			return err
		}
		return event(ctx, tx, p, "member.role_changed", &user)
	})
}

func (s *Service) Remove(ctx context.Context, p session.Principal, user uuid.UUID) error {
	return db.WithOrgTx(ctx, s.pool, p.OrgID, func(ctx context.Context, tx pgx.Tx) error {
		actor, kind, err := manager(ctx, tx, p)
		if err != nil {
			return err
		}
		if kind != "team" {
			return conflict("team_required")
		}
		var target string
		if err := tx.QueryRow(ctx, `SELECT role FROM memberships WHERE org_id=$1 AND user_id=$2`, p.OrgID, user).Scan(&target); errors.Is(err, pgx.ErrNoRows) {
			return nil
		} else if err != nil {
			return err
		}
		if target == "owner" || target == "admin" && actor != "owner" || user == p.UserID {
			return conflict("owner_transfer_required")
		}
		if _, err := tx.Exec(ctx, `DELETE FROM memberships WHERE org_id=$1 AND user_id=$2`, p.OrgID, user); err != nil {
			return err
		}
		// Revocation is immediate through live membership checks. The person's
		// own organizations, sessions and subscription are deliberately retained.
		return event(ctx, tx, p, "member.removed", &user)
	})
}

func (s *Service) Transfer(ctx context.Context, p session.Principal, user uuid.UUID) error {
	if user == p.UserID {
		return conflict("same_owner")
	}
	return db.WithOrgTx(ctx, s.pool, p.OrgID, func(ctx context.Context, tx pgx.Tx) error {
		actor, kind, err := manager(ctx, tx, p)
		if err != nil {
			return err
		}
		if actor != "owner" || kind != "team" {
			return apix.ErrForbidden
		}
		role, err := tenancy.Check(ctx, tx, p.OrgID, user)
		if err != nil {
			return err
		}
		if role != "reconciler" && role != "admin" && role != "owner" {
			return conflict("full_access_required")
		}
		if _, err := tx.Exec(ctx, `UPDATE memberships SET role='owner' WHERE org_id=$1 AND user_id=$2`, p.OrgID, user); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE memberships SET role='reconciler' WHERE org_id=$1 AND user_id=$2`, p.OrgID, p.UserID); err != nil {
			return err
		}
		return event(ctx, tx, p, "owner.transferred", &user)
	})
}
