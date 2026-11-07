//go:build integration

package teams

import (
	"errors"
	"testing"

	"github.com/google/uuid"

	contracts "github.com/mmtmn/docmatch/packages/contracts/gen"
	"github.com/mmtmn/docmatch/services/api/internal/config"
	"github.com/mmtmn/docmatch/services/api/internal/publicid"
	"github.com/mmtmn/docmatch/services/api/internal/runs/apix"
)

func TestInvitationPauseKeepsDeclineAndRevocationAvailable(t *testing.T) {
	s, owner, guest := fixture(t), person(t), person(t)
	ctx := t.Context()
	if err := s.Convert(ctx, owner, "Equipe"); err != nil {
		t.Fatal(err)
	}
	email := guest.UserID.String() + "@example.test"
	_, err := s.Invite(ctx, owner, email, "uploader")
	if err != nil {
		t.Fatal(err)
	}
	token := inviteToken(t, owner.OrgID, email)
	second, err := s.Invite(ctx, owner, uuid.NewString()+"@example.test", "uploader")
	if err != nil {
		t.Fatal(err)
	}
	s.cfg.Rollouts.Invitations = &config.AccountRollout{}
	_, err = s.Respond(ctx, guest, token, true)
	var e *apix.Error
	if !errors.As(err, &e) || e.Code != contracts.ErrFeaturePaused {
		t.Fatalf("accept: %v", err)
	}
	view, err := s.Get(ctx, owner)
	if err != nil || view.CanInvite || view.Features.Invitations {
		t.Fatal(view, err)
	}
	if _, err = s.Respond(ctx, guest, token, false); err != nil {
		t.Fatal("decline", err)
	}
	_, err = s.Invite(ctx, owner, email, "uploader")
	if !errors.As(err, &e) || e.Code != contracts.ErrFeaturePaused {
		t.Fatalf("invite: %v", err)
	}
	id, err := publicid.Decode("inv", second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Revoke(ctx, owner, id); err != nil {
		t.Fatal("revoke", err)
	}
}
