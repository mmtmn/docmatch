package teams

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	contracts "github.com/mmtmn/docmatch/packages/contracts/gen"
	"github.com/mmtmn/docmatch/services/api/internal/crypto"
	"github.com/mmtmn/docmatch/services/api/internal/db"
	"github.com/mmtmn/docmatch/services/api/internal/email"
	"github.com/mmtmn/docmatch/services/api/internal/email/emailpg"
	"github.com/mmtmn/docmatch/services/api/internal/httpx"
	"github.com/mmtmn/docmatch/services/api/internal/publicid"
	"github.com/mmtmn/docmatch/services/api/internal/runs/apix"
	"github.com/mmtmn/docmatch/services/api/internal/session"
)

func normalizeEmail(raw string) (string, error) {
	v, err := httpx.ValidateText(raw, contracts.TextEmail, true)
	if err != nil {
		return "", err
	}
	v = strings.ToLower(v)
	parsed, err := mail.ParseAddress(v)
	at := strings.LastIndex(v, "@")
	if err != nil || parsed.Address != v || parsed.Name != "" || at < 1 || !strings.Contains(v[at+1:], ".") || strings.ContainsAny(v, " <>[](),;:\"\\") {
		return "", errors.New("invalid email")
	}

	return v, nil
}

func seatRoom(ctx context.Context, tx pgx.Tx, org uuid.UUID, includePending bool) error {
	var count int
	err := tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM memberships m JOIN users u ON u.id=m.user_id WHERE m.org_id=$1 AND u.deleted_at IS NULL)+CASE WHEN $2 THEN (SELECT count(*) FROM team_invitations WHERE org_id=$1 AND status='pending' AND expires_at>now()) ELSE 0 END`, org, includePending).Scan(&count)
	if err != nil {
		return err
	}
	if count >= contracts.IDsAccountMembersMax {
		return conflict("member_limit")
	}
	return nil
}

func sendRoom(ctx context.Context, tx pgx.Tx, org uuid.UUID) error {
	var count int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM team_events WHERE org_id=$1 AND action IN ('invitation.created','invitation.resent') AND created_at>now()-interval '1 hour'`, org).Scan(&count); err != nil {
		return err
	}
	if count >= InvitationsPerHour {
		return conflict("invite_rate_limited")
	}
	return nil
}

func (s *Service) sendInvitation(ctx context.Context, tx pgx.Tx, p session.Principal, id uuid.UUID, to, role, token string) error {
	if s.cfg.PublicBaseURL == nil {
		return errors.New("teams: public URL missing")
	}
	link := *s.cfg.PublicBaseURL
	link.Path = "/convite"
	link.RawQuery = ""
	link.Fragment = "token=" + token
	var name string
	if err := tx.QueryRow(ctx, `SELECT name FROM orgs WHERE id=$1`, p.OrgID).Scan(&name); err != nil {
		return err
	}
	permission := "adicionar documentos"
	if role == "reconciler" {
		permission = "adicionar documentos, conciliar projetos e ver resultados"
	}
	message := email.Message{To: to, Subject: "Convite para uma equipe na docmatch", Tag: "team_invitation", Text: fmt.Sprintf("Olá,\n\nVocê recebeu um convite para participar de %s na docmatch, com permissão para %s.\n\nAbra o link, entre com este endereço de e-mail e confirme se deseja participar:\n\n%s\n\nO convite vale por 7 dias. Abrir o link não aceita o convite. Sua conta pessoal e sua assinatura continuam separadas. Não há cobrança adicional por assento nesta etapa.\n\nSe não esperava este convite, ignore esta mensagem.\n\n— docmatch\n", name, permission, link.String())}
	_, _, err := emailpg.EnqueueOnce(ctx, tx, s.queue, &p.OrgID, "team-invite:"+id.String()+":"+fmt.Sprintf("%x", crypto.Digest(token)), message)
	return err
}

func (s *Service) Invite(ctx context.Context, p session.Principal, address, role string) (Invitation, error) {
	if !s.cfg.Rollouts.Invitations.Allows(p.OrgID) {
		return Invitation{}, apix.E(contracts.ErrFeaturePaused, nil)
	}
	address, err := normalizeEmail(address)
	if err != nil {
		return Invitation{}, conflict("invalid_email")
	}
	if role != "uploader" && role != "reconciler" {
		return Invitation{}, conflict("invalid_role")
	}
	out := Invitation{}
	err = db.WithOrgTx(ctx, s.pool, p.OrgID, func(ctx context.Context, tx pgx.Tx) error {
		_, kind, err := manager(ctx, tx, p)
		if err != nil {
			return err
		}
		if kind != "team" {
			return conflict("team_required")
		}
		if err := s.paid(ctx, tx, p.OrgID); err != nil {
			return err
		}
		var member bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM memberships m JOIN users u ON u.id=m.user_id WHERE m.org_id=$1 AND u.email=$2 AND u.deleted_at IS NULL)`, p.OrgID, address).Scan(&member); err != nil {
			return err
		}
		if member {
			return conflict("already_member")
		}
		var existing uuid.UUID
		err = tx.QueryRow(ctx, `SELECT id,email,role,status,expires_at,sent_at FROM team_invitations WHERE org_id=$1 AND email=$2 AND status='pending' AND expires_at>now()`, p.OrgID, address).Scan(&existing, &out.Email, &out.Role, &out.Status, &out.ExpiresAt, &out.SentAt)
		if err == nil {
			out.ID = publicid.Encode("inv", existing)
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err := seatRoom(ctx, tx, p.OrgID, true); err != nil {
			return err
		}
		if err := sendRoom(ctx, tx, p.OrgID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE team_invitations SET status='expired',updated_at=now() WHERE org_id=$1 AND email=$2 AND status='pending' AND expires_at<=now()`, p.OrgID, address); err != nil {
			return err
		}
		id := uuid.New()
		token := crypto.RandomToken(32)
		err = tx.QueryRow(ctx, `INSERT INTO team_invitations(id,org_id,email,role,token_hash,invited_by,expires_at) VALUES($1,$2,$3,$4,$5,$6,now()+($7::bigint * interval '1 second')) RETURNING email,role,status,expires_at,sent_at`, id, p.OrgID, address, role, crypto.Digest(token), p.UserID, int64(InvitationTTL/time.Second)).Scan(&out.Email, &out.Role, &out.Status, &out.ExpiresAt, &out.SentAt)
		if err != nil {
			return err
		}
		out.ID = publicid.Encode("inv", id)
		if err := s.sendInvitation(ctx, tx, p, id, address, role, token); err != nil {
			return err
		}
		return event(ctx, tx, p, "invitation.created", &id)
	})
	return out, err
}

func (s *Service) Resend(ctx context.Context, p session.Principal, id uuid.UUID) error {
	if !s.cfg.Rollouts.Invitations.Allows(p.OrgID) {
		return apix.E(contracts.ErrFeaturePaused, nil)
	}
	return db.WithOrgTx(ctx, s.pool, p.OrgID, func(ctx context.Context, tx pgx.Tx) error {
		if _, _, err := manager(ctx, tx, p); err != nil {
			return err
		}
		if err := s.paid(ctx, tx, p.OrgID); err != nil {
			return err
		}
		var address, role, status string
		var sent time.Time
		err := tx.QueryRow(ctx, `SELECT email,role,status,sent_at FROM team_invitations WHERE org_id=$1 AND id=$2 FOR UPDATE`, p.OrgID, id).Scan(&address, &role, &status, &sent)
		if errors.Is(err, pgx.ErrNoRows) {
			return missing()
		}
		if err != nil {
			return err
		}
		if status != "pending" && status != "expired" {
			return conflict("invite_used")
		}
		var superseded bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM team_invitations WHERE org_id=$1 AND email=$2 AND id<>$3 AND status='pending') OR EXISTS(SELECT 1 FROM memberships m JOIN users u ON u.id=m.user_id WHERE m.org_id=$1 AND u.email=$2 AND u.deleted_at IS NULL)`, p.OrgID, address, id).Scan(&superseded); err != nil {
			return err
		}
		if superseded {
			return conflict("invite_used")
		}
		if time.Since(sent) < ResendCooldown {
			return conflict("invite_rate_limited")
		}
		if err := sendRoom(ctx, tx, p.OrgID); err != nil {
			return err
		}
		// An expired invitation no longer reserved a seat.
		var expired bool
		if err := tx.QueryRow(ctx, `SELECT expires_at<=now() FROM team_invitations WHERE org_id=$1 AND id=$2`, p.OrgID, id).Scan(&expired); err != nil {
			return err
		}
		if expired {
			if err := seatRoom(ctx, tx, p.OrgID, true); err != nil {
				return err
			}
		}
		token := crypto.RandomToken(32)
		if _, err := tx.Exec(ctx, `UPDATE team_invitations SET token_hash=$3,status='pending',expires_at=now()+($4::bigint * interval '1 second'),sent_at=now(),updated_at=now() WHERE org_id=$1 AND id=$2`, p.OrgID, id, crypto.Digest(token), int64(InvitationTTL/time.Second)); err != nil {
			return err
		}
		if err := s.sendInvitation(ctx, tx, p, id, address, role, token); err != nil {
			return err
		}
		return event(ctx, tx, p, "invitation.resent", &id)
	})
}

func (s *Service) Revoke(ctx context.Context, p session.Principal, id uuid.UUID) error {
	return db.WithOrgTx(ctx, s.pool, p.OrgID, func(ctx context.Context, tx pgx.Tx) error {
		if _, _, err := manager(ctx, tx, p); err != nil {
			return err
		}
		result, err := tx.Exec(ctx, `UPDATE team_invitations SET status='revoked',updated_at=now() WHERE org_id=$1 AND id=$2 AND status IN ('pending','expired')`, p.OrgID, id)
		if err != nil {
			return err
		}
		if result.RowsAffected() == 0 {
			return conflict("invite_used")
		}
		return event(ctx, tx, p, "invitation.revoked", &id)
	})
}
