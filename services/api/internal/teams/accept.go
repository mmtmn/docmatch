package teams

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/mmtmn/docmatch/services/api/internal/crypto"
	"github.com/mmtmn/docmatch/services/api/internal/db"
	"github.com/mmtmn/docmatch/services/api/internal/publicid"
	"github.com/mmtmn/docmatch/services/api/internal/runs/apix"
	"github.com/mmtmn/docmatch/services/api/internal/session"
)

func emailHint(address string) string {
	parts := strings.SplitN(address, "@", 2)
	if len(parts) != 2 {
		return "***"
	}
	local := []rune(parts[0])
	if len(local) > 1 {
		return string(local[0]) + "***@" + parts[1]
	}
	return "***@" + parts[1]
}

// Preview is read-only. Tokens arrive in a JSON body, never in an API URL or
// access log; the e-mail link keeps the token in the browser's URL fragment.
func (s *Service) Preview(ctx context.Context, token string, user *uuid.UUID) (Preview, error) {
	if len(token) != 43 {
		return Preview{}, missing()
	}
	var out Preview
	err := db.WithSystemTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		var id uuid.UUID
		var address string
		err := tx.QueryRow(ctx, `SELECT i.id,o.name,i.email,i.role,CASE WHEN i.status='pending' AND i.expires_at<=now() THEN 'expired' ELSE i.status END,i.expires_at,
 EXISTS(SELECT 1 FROM users u WHERE u.id=$2 AND u.email=i.email AND u.email_verified_at IS NOT NULL AND u.deleted_at IS NULL AND u.suspended_at IS NULL AND u.deletion_requested_at IS NULL)
 FROM team_invitations i JOIN orgs o ON o.id=i.org_id WHERE i.token_hash=$1 AND o.deleted_at IS NULL`, crypto.Digest(token), user).Scan(&id, &out.Organization, &address, &out.Role, &out.Status, &out.ExpiresAt, &out.EmailMatches)
		if errors.Is(err, pgx.ErrNoRows) {
			return missing()
		}
		if err != nil {
			return err
		}
		out.ID = publicid.Encode("inv", id)
		out.EmailHint = emailHint(address)
		return nil
	})
	return out, err
}

// Respond requires a verified matching identity and an explicit authenticated
// POST. It never changes the user's login, personal balance or subscription.
func (s *Service) Respond(ctx context.Context, p session.Principal, token string, accept bool) (string, error) {
	if len(token) != 43 {
		return "", missing()
	}
	var org uuid.UUID
	err := db.WithSystemTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		// Discover the account, then take the same account-first lock order as
		// revocation and resending. Re-read the hash after taking the lock.
		if err := tx.QueryRow(ctx, `SELECT org_id FROM team_invitations WHERE token_hash=$1`, crypto.Digest(token)).Scan(&org); errors.Is(err, pgx.ErrNoRows) {
			return missing()
		} else if err != nil {
			return err
		}
		var kind string
		if err := tx.QueryRow(ctx, `SELECT kind FROM orgs WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, org).Scan(&kind); errors.Is(err, pgx.ErrNoRows) {
			return missing()
		} else if err != nil {
			return err
		}
		if kind != "team" {
			return missing()
		}
		var id uuid.UUID
		var role, status, address string
		var acceptedBy *uuid.UUID
		var expired bool
		err := tx.QueryRow(ctx, `SELECT id,email,role,status,expires_at<=clock_timestamp(),accepted_by FROM team_invitations WHERE token_hash=$1 AND org_id=$2 FOR UPDATE`, crypto.Digest(token), org).Scan(&id, &address, &role, &status, &expired, &acceptedBy)
		if errors.Is(err, pgx.ErrNoRows) {
			return missing()
		}
		if err != nil {
			return err
		}
		var matches bool
		err = tx.QueryRow(ctx, `SELECT email=$2 AND email_verified_at IS NOT NULL FROM users WHERE id=$1 AND deleted_at IS NULL AND suspended_at IS NULL AND deletion_requested_at IS NULL FOR SHARE`, p.UserID, address).Scan(&matches)
		if errors.Is(err, pgx.ErrNoRows) {
			return apix.ErrForbidden
		}
		if err != nil {
			return err
		}
		if !matches {
			return conflict("invite_email_mismatch")
		}
		if status == "accepted" && acceptedBy != nil && *acceptedBy == p.UserID && accept {
			var exists bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM memberships WHERE org_id=$1 AND user_id=$2)`, org, p.UserID).Scan(&exists); err != nil {
				return err
			}
			if exists {
				return nil
			}
			return conflict("invite_used")
		}
		if status != "pending" {
			return conflict("invite_used")
		}
		if expired {
			return conflict("invite_expired")
		}
		actor := p
		actor.OrgID = org
		if !accept {
			if _, err := tx.Exec(ctx, `UPDATE team_invitations SET status='declined',updated_at=now() WHERE id=$1`, id); err != nil {
				return err
			}
			return event(ctx, tx, actor, "invitation.declined", &id)
		}
		if err := s.paid(ctx, tx, org); err != nil {
			return err
		}
		var existing bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM memberships WHERE org_id=$1 AND user_id=$2)`, org, p.UserID).Scan(&existing); err != nil {
			return err
		}
		if !existing {
			if err := seatRoom(ctx, tx, org, false); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO memberships(org_id,user_id,role) VALUES($1,$2,$3)`, org, p.UserID, role); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE team_invitations SET status='accepted',accepted_by=$2,accepted_at=now(),updated_at=now() WHERE id=$1`, id, p.UserID); err != nil {
			return err
		}
		return event(ctx, tx, actor, "invitation.accepted", &id)
	})
	if err != nil {
		return "", err
	}
	return publicid.Encode(publicid.PrefixOrg, org), nil
}
