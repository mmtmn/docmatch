package teams

import (
	"context"
	"errors"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/mmtmn/docmatch/services/api/internal/db"
	"github.com/mmtmn/docmatch/services/api/internal/httpx"
	"github.com/mmtmn/docmatch/services/api/internal/publicid"
	"github.com/mmtmn/docmatch/services/api/internal/runs/apix"
	"github.com/mmtmn/docmatch/services/api/internal/session"
)

const inboxPageSize = 50

type Inbox struct {
	Items      []Preview `json:"items"`
	NextCursor string    `json:"next_cursor,omitempty"`
}

// Pending only reveals invitations to the current verified identity, independent
// of its selected account. Listing never changes membership or exposes a token.
func (s *Service) Pending(ctx context.Context, p session.Principal, cursor string) (Inbox, error) {
	out := Inbox{Items: []Preview{}}
	after := uuid.Nil
	if cursor != "" {
		var err error
		after, err = publicid.Decode("inv", cursor)
		if err != nil {
			return out, apix.Validation(apix.FieldError{Path: "/cursor", Code: "invalid"})
		}
	}
	err := db.WithSystemTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT i.id,o.name,i.email,i.role,i.expires_at
   FROM users u JOIN team_invitations i ON i.email=u.email JOIN orgs o ON o.id=i.org_id
   WHERE u.id=$1 AND u.email_verified_at IS NOT NULL AND u.deleted_at IS NULL
   AND u.suspended_at IS NULL AND u.deletion_requested_at IS NULL
   AND o.deleted_at IS NULL AND o.kind='team' AND i.status='pending' AND i.expires_at>now()
   AND i.id>$2 ORDER BY i.id LIMIT $3`, p.UserID, after, inboxPageSize+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id uuid.UUID
			var address string
			item := Preview{Status: "pending", EmailMatches: true}
			if err := rows.Scan(&id, &item.Organization, &address, &item.Role, &item.ExpiresAt); err != nil {
				return err
			}
			if len(out.Items) == inboxPageSize {
				out.NextCursor = out.Items[len(out.Items)-1].ID
				break
			}
			item.ID, item.EmailHint = publicid.Encode("inv", id), emailHint(address)
			out.Items = append(out.Items, item)
		}
		return rows.Err()
	})
	return out, err
}

// RespondByID needs no e-mail secret because it first proves the same verified
// identity. The shared responder rechecks identity, expiry and membership under
// account/invitation locks. A concurrent resend invalidates the discovered hash.
func (s *Service) RespondByID(ctx context.Context, p session.Principal, id uuid.UUID, accept bool) (string, error) {
	var digest []byte
	err := db.WithSystemTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `SELECT i.token_hash FROM team_invitations i JOIN users u ON u.email=i.email
   WHERE i.id=$1 AND u.id=$2 AND u.email_verified_at IS NOT NULL AND u.deleted_at IS NULL
   AND u.suspended_at IS NULL AND u.deletion_requested_at IS NULL`, id, p.UserID).Scan(&digest)
		if errors.Is(err, pgx.ErrNoRows) {
			return missing()
		}
		return err
	})
	if err != nil {
		return "", err
	}
	return s.respondDigest(ctx, p, digest, accept)
}

func (s *Service) pending(w http.ResponseWriter, r *http.Request) {
	p, _ := session.FromContext(r.Context())
	v, err := s.Pending(r.Context(), p, r.URL.Query().Get("cursor"))
	respond(w, r, v, err)
}
func (s *Service) respondPending(w http.ResponseWriter, r *http.Request) {
	p, _ := session.FromContext(r.Context())
	id, ok := decodeID(w, r, "inv", "invitation")
	if !ok {
		return
	}
	var body struct {
		Accept *bool `json:"accept"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		apix.Handle(w, r, err)
		return
	}
	if body.Accept == nil {
		apix.Handle(w, r, apix.Validation(apix.FieldError{Path: "/accept", Code: "required"}))
		return
	}
	org, err := s.RespondByID(r.Context(), p, id, *body.Accept)
	respond(w, r, map[string]any{"organization_id": org, "accepted": *body.Accept}, err)
}
