package teams

import (
	"context"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/mmtmn/docmatch/services/api/internal/db"
	"github.com/mmtmn/docmatch/services/api/internal/httpx"
	"github.com/mmtmn/docmatch/services/api/internal/publicid"
	"github.com/mmtmn/docmatch/services/api/internal/runs/apix"
	"github.com/mmtmn/docmatch/services/api/internal/session"
	"github.com/mmtmn/docmatch/services/api/internal/tenancy"
)

func (s *Service) Mount(r *httpx.Router) {
	r.Handle(http.MethodGet, "/v1/me/invitations", httpx.BodyNone, session.RequireFunc(s.pending))
	r.Handle(http.MethodPost, "/v1/me/invitations/{invitation}/respond", httpx.BodyJSON, session.RequireFunc(s.respondPending))
	r.Handle(http.MethodGet, "/v1/team", httpx.BodyNone, session.RequireFunc(s.get))
	r.Handle(http.MethodPost, "/v1/team", httpx.BodyJSON, session.RequireFunc(s.convert))
	r.Handle(http.MethodPost, "/v1/team/invitations", httpx.BodyJSON, session.RequireFunc(s.invite))
	r.Handle(http.MethodPost, "/v1/team/invitations/{invitation}/resend", httpx.BodyNone, session.RequireFunc(s.resend))
	r.Handle(http.MethodDelete, "/v1/team/invitations/{invitation}", httpx.BodyNone, session.RequireFunc(s.revoke))
	r.Handle(http.MethodPatch, "/v1/team/members/{user}", httpx.BodyJSON, session.RequireFunc(s.changeRole))
	r.Handle(http.MethodDelete, "/v1/team/members/{user}", httpx.BodyNone, session.RequireFunc(s.remove))
	r.Handle(http.MethodPost, "/v1/team/owner", httpx.BodyJSON, session.RequireFunc(s.transfer))
	r.Handle(http.MethodPost, "/v1/team/leave", httpx.BodyNone, session.RequireFunc(s.leave))
	r.HandleFunc(http.MethodPost, "/v1/invitations/preview", httpx.BodyJSON, s.preview)
	r.Handle(http.MethodPost, "/v1/invitations/respond", httpx.BodyJSON, session.RequireFunc(s.respond))
}

func respond(w http.ResponseWriter, r *http.Request, v any, err error) {
	if err != nil {
		apix.Handle(w, r, err)
		return
	}
	if v == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	apix.WriteJSON(w, http.StatusOK, v)
}
func decodeID(w http.ResponseWriter, r *http.Request, prefix, key string) (uuid.UUID, bool) {
	id, err := publicid.Decode(prefix, r.PathValue(key))
	if err != nil {
		apix.Handle(w, r, missing())
		return uuid.Nil, false
	}
	return id, true
}

func (s *Service) get(w http.ResponseWriter, r *http.Request) {
	p, _ := session.FromContext(r.Context())
	v, err := s.Get(r.Context(), p)
	respond(w, r, v, err)
}
func (s *Service) convert(w http.ResponseWriter, r *http.Request) {
	p, _ := session.FromContext(r.Context())
	var body struct {
		Name string `json:"name"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		apix.Handle(w, r, err)
		return
	}
	if err := s.Convert(r.Context(), p, body.Name); err != nil {
		apix.Handle(w, r, err)
		return
	}
	v, err := s.Get(r.Context(), p)
	respond(w, r, v, err)
}
func (s *Service) invite(w http.ResponseWriter, r *http.Request) {
	p, _ := session.FromContext(r.Context())
	var body struct {
		Email string `json:"email"`
		Role  string `json:"role"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		apix.Handle(w, r, err)
		return
	}
	v, err := s.Invite(r.Context(), p, body.Email, body.Role)
	respond(w, r, v, err)
}
func (s *Service) resend(w http.ResponseWriter, r *http.Request) {
	p, _ := session.FromContext(r.Context())
	id, ok := decodeID(w, r, "inv", "invitation")
	if ok {
		respond(w, r, nil, s.Resend(r.Context(), p, id))
	}
}
func (s *Service) revoke(w http.ResponseWriter, r *http.Request) {
	p, _ := session.FromContext(r.Context())
	id, ok := decodeID(w, r, "inv", "invitation")
	if ok {
		respond(w, r, nil, s.Revoke(r.Context(), p, id))
	}
}
func (s *Service) changeRole(w http.ResponseWriter, r *http.Request) {
	p, _ := session.FromContext(r.Context())
	id, ok := decodeID(w, r, publicid.PrefixUser, "user")
	if !ok {
		return
	}
	var body struct {
		Role string `json:"role"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		apix.Handle(w, r, err)
		return
	}
	respond(w, r, nil, s.ChangeRole(r.Context(), p, id, body.Role))
}
func (s *Service) remove(w http.ResponseWriter, r *http.Request) {
	p, _ := session.FromContext(r.Context())
	id, ok := decodeID(w, r, publicid.PrefixUser, "user")
	if ok {
		respond(w, r, nil, s.Remove(r.Context(), p, id))
	}
}
func (s *Service) transfer(w http.ResponseWriter, r *http.Request) {
	p, _ := session.FromContext(r.Context())
	var body struct {
		UserID string `json:"user_id"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		apix.Handle(w, r, err)
		return
	}
	id, err := publicid.Decode(publicid.PrefixUser, body.UserID)
	if err != nil {
		apix.Handle(w, r, missing())
		return
	}
	respond(w, r, nil, s.Transfer(r.Context(), p, id))
}
func (s *Service) leave(w http.ResponseWriter, r *http.Request) {
	p, _ := session.FromContext(r.Context())
	err := db.WithOrgTx(r.Context(), s.pool, p.OrgID, func(ctx context.Context, tx pgx.Tx) error {
		var kind string
		if err := tx.QueryRow(ctx, `SELECT kind FROM orgs WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, p.OrgID).Scan(&kind); err != nil {
			return err
		}
		role, err := tenancy.Check(ctx, tx, p.OrgID, p.UserID)
		if err != nil {
			return err
		}
		if kind != "team" {
			return conflict("team_required")
		}
		if role == "owner" {
			return conflict("owner_transfer_required")
		}
		if _, err := tx.Exec(ctx, `DELETE FROM memberships WHERE org_id=$1 AND user_id=$2`, p.OrgID, p.UserID); err != nil {
			return err
		}
		return event(ctx, tx, p, "member.left", &p.UserID)
	})
	respond(w, r, nil, err)
}
func (s *Service) preview(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token string `json:"token"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		apix.Handle(w, r, err)
		return
	}
	var user *uuid.UUID
	if p, ok := session.FromContext(r.Context()); ok {
		user = &p.UserID
	}
	v, err := s.Preview(r.Context(), body.Token, user)
	respond(w, r, v, err)
}
func (s *Service) respond(w http.ResponseWriter, r *http.Request) {
	p, _ := session.FromContext(r.Context())
	var body struct {
		Token  string `json:"token"`
		Accept *bool  `json:"accept"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		apix.Handle(w, r, err)
		return
	}
	if body.Accept == nil {
		apix.Handle(w, r, apix.Validation(apix.FieldError{Path: "/accept", Code: "required"}))
		return
	}
	org, err := s.Respond(r.Context(), p, body.Token, *body.Accept)
	respond(w, r, map[string]any{"organization_id": org, "accepted": *body.Accept}, err)
}
