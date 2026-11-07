//go:build integration

package teams

import (
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/mmtmn/docmatch/services/api/internal/crypto"
	"github.com/mmtmn/docmatch/services/api/internal/publicid"
	"github.com/mmtmn/docmatch/services/api/internal/runs/apix"
	"github.com/mmtmn/docmatch/services/api/internal/testdb"
)

func TestInboxVerifiedIdentityAndExplicitConsent(t *testing.T) {
	s := fixture(t)
	owner, guest, stranger := person(t), person(t), person(t)
	ctx := t.Context()
	d := testdb.Get(t)
	if err := s.Convert(ctx, owner, "Equipe convidante"); err != nil {
		t.Fatal(err)
	}
	inv, err := s.Invite(ctx, owner, guest.UserID.String()+"@example.test", "uploader")
	if err != nil {
		t.Fatal(err)
	}
	id, _ := publicid.Decode("inv", inv.ID)
	// A stale selected account does not hide invitations addressed to this person.
	guest.OrgID = uuid.New()
	inbox, err := s.Pending(ctx, guest, "")
	if err != nil || len(inbox.Items) != 1 || inbox.Items[0].ID != inv.ID || !inbox.Items[0].EmailMatches {
		t.Fatal(inbox, err)
	}
	if err := s.GetRole(ctx, owner.OrgID, guest.UserID); !errors.Is(err, apix.ErrForbidden) {
		t.Fatal("listing joined team", err)
	}
	other, err := s.Pending(ctx, stranger, "")
	if err != nil || len(other.Items) != 0 {
		t.Fatal("stranger saw invitation", other, err)
	}
	if _, err := s.RespondByID(ctx, stranger, id, true); err == nil {
		t.Fatal("stranger accepted invitation")
	}
	if _, err := d.Admin.Exec(ctx, `UPDATE users SET email_verified_at=NULL WHERE id=$1`, guest.UserID); err != nil {
		t.Fatal(err)
	}
	unverified, err := s.Pending(ctx, guest, "")
	if err != nil || len(unverified.Items) != 0 {
		t.Fatal("unverified inbox", unverified, err)
	}
	if _, err := s.RespondByID(ctx, guest, id, true); err == nil {
		t.Fatal("unverified acceptance")
	}
	if _, err := d.Admin.Exec(ctx, `UPDATE users SET email_verified_at=now() WHERE id=$1`, guest.UserID); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		org, err := s.RespondByID(ctx, guest, id, true)
		if err != nil || org != publicid.Encode(publicid.PrefixOrg, owner.OrgID) {
			t.Fatal(org, err)
		}
	}
	accepted, err := s.Pending(ctx, guest, "")
	if err != nil || len(accepted.Items) != 0 {
		t.Fatal("accepted still pending", accepted, err)
	}
	if err := s.Remove(ctx, owner, guest.UserID); err != nil {
		t.Fatal(err)
	}
	_, err = s.RespondByID(ctx, guest, id, true)
	reason(t, err, "invite_used")
}

func TestInboxPaginationExpiryRevocationAndDecline(t *testing.T) {
	s := fixture(t)
	owner, guest := person(t), person(t)
	ctx := t.Context()
	d := testdb.Get(t)
	if err := s.Convert(ctx, owner, "Equipe"); err != nil {
		t.Fatal(err)
	}
	// Separate companies may invite one recipient; bulk synthetic rows exercise a
	// page boundary without sending e-mails or weakening the send-rate limit.
	for range inboxPageSize + 1 {
		org := uuid.New()
		if _, err := d.Admin.Exec(ctx, `INSERT INTO orgs(id,name,slug,kind) VALUES($1,'Outra equipe',$2,'team')`, org, org.String()); err != nil {
			t.Fatal(err)
		}
		if _, err := d.Admin.Exec(ctx, `INSERT INTO team_invitations(id,org_id,email,role,token_hash,invited_by,expires_at) VALUES($1,$2,$3,'reconciler',$4,$5,now()+interval '1 day')`, uuid.New(), org, guest.UserID.String()+"@example.test", crypto.Digest(uuid.NewString()), owner.UserID); err != nil {
			t.Fatal(err)
		}
	}
	first, err := s.Pending(ctx, guest, "")
	if err != nil || len(first.Items) != inboxPageSize || first.NextCursor == "" {
		t.Fatal(first, err)
	}
	next, err := s.Pending(ctx, guest, first.NextCursor)
	if err != nil || len(next.Items) != 1 || next.NextCursor != "" {
		t.Fatal(next, err)
	}
	for _, v := range first.Items {
		if v.ID == next.Items[0].ID {
			t.Fatal("repeated page item")
		}
	}
	if _, err := s.Pending(ctx, guest, "bad"); err == nil {
		t.Fatal("bad cursor accepted")
	}
	id, _ := publicid.Decode("inv", next.Items[0].ID)
	if _, err := s.RespondByID(ctx, guest, id, false); err != nil {
		t.Fatal(err)
	}
	_, err = s.RespondByID(ctx, guest, id, true)
	reason(t, err, "invite_used")
	expired, _ := publicid.Decode("inv", first.Items[0].ID)
	if _, err := d.Admin.Exec(ctx, `UPDATE team_invitations SET expires_at=now()-interval '1 second' WHERE id=$1`, expired); err != nil {
		t.Fatal(err)
	}
	_, err = s.RespondByID(ctx, guest, expired, true)
	reason(t, err, "invite_expired")
	revoked, _ := publicid.Decode("inv", first.Items[1].ID)
	if _, err := d.Admin.Exec(ctx, `UPDATE team_invitations SET status='revoked' WHERE id=$1`, revoked); err != nil {
		t.Fatal(err)
	}
	_, err = s.RespondByID(ctx, guest, revoked, true)
	reason(t, err, "invite_used")
	remaining, err := s.Pending(ctx, guest, "")
	if err != nil || len(remaining.Items) != inboxPageSize-2 {
		t.Fatal(len(remaining.Items), err)
	}
}
