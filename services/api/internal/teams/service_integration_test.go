//go:build integration

package teams

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	contracts "github.com/mmtmn/docmatch/packages/contracts/gen"
	"github.com/mmtmn/docmatch/services/api/internal/config"
	"github.com/mmtmn/docmatch/services/api/internal/crypto"
	"github.com/mmtmn/docmatch/services/api/internal/db"
	"github.com/mmtmn/docmatch/services/api/internal/email"
	"github.com/mmtmn/docmatch/services/api/internal/email/emailpg"
	"github.com/mmtmn/docmatch/services/api/internal/publicid"
	"github.com/mmtmn/docmatch/services/api/internal/runs/apix"
	"github.com/mmtmn/docmatch/services/api/internal/session"
	"github.com/mmtmn/docmatch/services/api/internal/tenancy"
	"github.com/mmtmn/docmatch/services/api/internal/testdb"
)

func TestMain(m *testing.M) { testdb.Main(m) }
func fixture(t *testing.T) *Service {
	t.Helper()
	d := testdb.Get(t)
	keys, err := crypto.NewKeyring(bytes.Repeat([]byte{5}, 32))
	if err != nil {
		t.Fatal(err)
	}
	base, _ := url.Parse("https://docmatch.example.test")
	queue, err := emailpg.NewInserter(d.App)
	if err != nil {
		t.Fatal(err)
	}
	return &Service{pool: d.App, keys: keys, cfg: &config.Config{Env: "dev", PublicBaseURL: base}, queue: queue}
}
func person(t *testing.T) session.Principal {
	t.Helper()
	p := session.Principal{UserID: uuid.New(), OrgID: uuid.New()}
	d := testdb.Get(t)
	if _, err := d.Admin.Exec(t.Context(), `INSERT INTO users(id,email,email_verified_at,display_name) VALUES($1,$2,now(),'Ana')`, p.UserID, p.UserID.String()+"@example.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Admin.Exec(t.Context(), `INSERT INTO orgs(id,name,slug,plan_code) VALUES($1,'Conta pessoal',$2,'pro')`, p.OrgID, p.OrgID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Admin.Exec(t.Context(), `INSERT INTO memberships(org_id,user_id,role) VALUES($1,$2,'owner')`, p.OrgID, p.UserID); err != nil {
		t.Fatal(err)
	}
	return p
}
func inviteToken(t *testing.T, org uuid.UUID, to string) string {
	t.Helper()
	var raw []byte
	if err := testdb.Get(t).Admin.QueryRow(t.Context(), `SELECT payload FROM outbox WHERE org_id=$1 AND payload->>'to'=$2 AND payload->>'tag'='team_invitation' ORDER BY created_at DESC,id DESC LIMIT 1`, org, to).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var message email.Message
	if err := json.Unmarshal(raw, &message); err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(message.Text, "\n") {
		if strings.Contains(line, "/convite#token=") {
			u, err := url.Parse(line)
			if err != nil {
				t.Fatal(err)
			}
			return strings.TrimPrefix(u.Fragment, "token=")
		}
	}
	t.Fatal("invitation link absent")
	return ""
}
func reason(t *testing.T, err error, want string) {
	t.Helper()
	var e *apix.Error
	if !errors.As(err, &e) || e.Details["reason"] != want {
		t.Fatalf("want %s, got %v", want, err)
	}
}

func TestConversionRetainsPaidAccountAndCreatesEmptyPersonal(t *testing.T) {
	s := fixture(t)
	p := person(t)
	ctx := t.Context()
	d := testdb.Get(t)
	if err := s.Convert(ctx, p, "Escritório"); err != nil {
		t.Fatal(err)
	}
	if err := s.Convert(ctx, p, "Outro nome"); err != nil {
		t.Fatal(err)
	}
	var name, kind, plan string
	if err := d.Admin.QueryRow(ctx, `SELECT name,kind,plan_code FROM orgs WHERE id=$1`, p.OrgID).Scan(&name, &kind, &plan); err != nil || name != "Escritório" || kind != "team" || plan != "pro" {
		t.Fatal("paid account changed", name, kind, plan, err)
	}
	var count int
	var personal uuid.UUID
	if err := d.Admin.QueryRow(ctx, `SELECT count(*) FROM orgs o JOIN memberships m ON m.org_id=o.id WHERE m.user_id=$1 AND o.kind='personal'`, p.UserID).Scan(&count); err != nil || count != 1 {
		t.Fatal("personal duplicate", count, err)
	}
	if err := d.Admin.QueryRow(ctx, `SELECT o.id FROM orgs o JOIN memberships m ON m.org_id=o.id WHERE m.user_id=$1 AND o.kind='personal'`, p.UserID).Scan(&personal); err != nil {
		t.Fatal(err)
	}
	var wrapped []byte
	if err := d.Admin.QueryRow(ctx, `SELECT wrapped_key FROM tenant_keys WHERE org_id=$1`, personal).Scan(&wrapped); err != nil {
		t.Fatal(err)
	}
	if _, err := crypto.UnwrapDataKey(s.keys.Key(crypto.PurposeKEK), wrapped, personal.String()); err != nil {
		t.Fatal("personal encryption missing", err)
	}
	s.cfg.Env = "prod"
	unpaid := person(t)
	var ae *apix.Error
	if err := s.Convert(ctx, unpaid, "Sem assinatura"); !errors.As(err, &ae) || ae.Code != contracts.ErrSubscriptionRequired {
		t.Fatal("unpaid conversion", err)
	}
}

func TestVerifiedAcceptancePreservesExistingAccountAndRevocation(t *testing.T) {
	s := fixture(t)
	owner, guest, wrong := person(t), person(t), person(t)
	ctx := t.Context()
	d := testdb.Get(t)
	if err := s.Convert(ctx, owner, "Empresa"); err != nil {
		t.Fatal(err)
	}
	address := guest.UserID.String() + "@example.test"
	invitation, err := s.Invite(ctx, owner, address, "uploader")
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := s.Invite(ctx, owner, address, "uploader")
	if err != nil || duplicate.ID != invitation.ID {
		t.Fatal("duplicate invite", err)
	}
	token := inviteToken(t, owner.OrgID, address)
	preview, err := s.Preview(ctx, token, &wrong.UserID)
	if err != nil || preview.EmailMatches || preview.Status != "pending" {
		t.Fatal("preview", err)
	}
	var members int
	if err := d.Admin.QueryRow(ctx, `SELECT count(*) FROM memberships WHERE org_id=$1`, owner.OrgID).Scan(&members); err != nil || members != 1 {
		t.Fatal("preview accepted invite", err)
	}
	_, err = s.Respond(ctx, wrong, token, true)
	reason(t, err, "invite_email_mismatch")
	if _, err := d.Admin.Exec(ctx, `UPDATE users SET email_verified_at=NULL WHERE id=$1`, guest.UserID); err != nil {
		t.Fatal(err)
	}
	_, err = s.Respond(ctx, guest, token, true)
	reason(t, err, "invite_email_mismatch")
	if _, err := d.Admin.Exec(ctx, `UPDATE users SET email_verified_at=now() WHERE id=$1`, guest.UserID); err != nil {
		t.Fatal(err)
	}
	outcomes := make(chan error, 5)
	var wg sync.WaitGroup
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			org, err := s.Respond(ctx, guest, token, true)
			if err == nil && org != publicid.Encode(publicid.PrefixOrg, owner.OrgID) {
				err = errors.New("wrong account")
			}
			outcomes <- err
		}()
	}
	wg.Wait()
	close(outcomes)
	for err := range outcomes {
		if err != nil {
			t.Fatal(err)
		}
	}
	var role, personalPlan string
	if err := d.Admin.QueryRow(ctx, `SELECT role FROM memberships WHERE org_id=$1 AND user_id=$2`, owner.OrgID, guest.UserID).Scan(&role); err != nil || role != "uploader" {
		t.Fatal("wrong invited role", role, err)
	}
	if err := d.Admin.QueryRow(ctx, `SELECT plan_code FROM orgs WHERE id=$1`, guest.OrgID).Scan(&personalPlan); err != nil || personalPlan != "pro" {
		t.Fatal("personal plan changed", err)
	}
	if err := d.Admin.QueryRow(ctx, `SELECT count(*) FROM memberships WHERE user_id=$1`, guest.UserID).Scan(&members); err != nil || members != 2 {
		t.Fatal("personal membership lost", members, err)
	}
	invited := guest
	invited.OrgID = owner.OrgID
	hidden, err := s.Get(ctx, invited)
	if err != nil || len(hidden.Members) != 0 || hidden.CanInvite {
		t.Fatal("uploader manages account", err)
	}
	if _, err := s.Invite(ctx, invited, wrong.UserID.String()+"@example.test", "reconciler"); !errors.Is(err, apix.ErrForbidden) {
		t.Fatal("uploader invited", err)
	}
	if err := s.GetRole(ctx, owner.OrgID, guest.UserID); err != nil {
		t.Fatal("accepted membership missing", err)
	}
	if err := s.Remove(ctx, owner, guest.UserID); err != nil {
		t.Fatal(err)
	}
	_, err = s.Respond(ctx, guest, token, true)
	reason(t, err, "invite_used")
	if err := s.GetRole(ctx, owner.OrgID, guest.UserID); !errors.Is(err, apix.ErrForbidden) {
		t.Fatal("revoked access", err)
	}
}

// Recheck through the same primitive used by every API request.
func (s *Service) GetRole(ctx context.Context, org, user uuid.UUID) error {
	return db.WithOrgTx(ctx, s.pool, org, func(ctx context.Context, tx pgx.Tx) error { _, err := tenancy.Check(ctx, tx, org, user); return err })
}

func TestResendExpiryDeclineAndOwnerTransfer(t *testing.T) {
	s := fixture(t)
	ctx := t.Context()
	owner, guest := person(t), person(t)
	d := testdb.Get(t)
	if err := s.Convert(ctx, owner, "Equipe"); err != nil {
		t.Fatal(err)
	}
	address := guest.UserID.String() + "@example.test"
	inv, err := s.Invite(ctx, owner, address, "reconciler")
	if err != nil {
		t.Fatal(err)
	}
	id, _ := publicid.Decode("inv", inv.ID)
	oldToken := inviteToken(t, owner.OrgID, address)
	reason(t, s.Resend(ctx, owner, id), "invite_rate_limited")
	if _, err := d.Admin.Exec(ctx, `UPDATE team_invitations SET sent_at=now()-interval '2 minutes' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if err := s.Resend(ctx, owner, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Preview(ctx, oldToken, nil); err == nil {
		t.Fatal("old token survived resend")
	}
	token := inviteToken(t, owner.OrgID, address)
	if _, err := d.Admin.Exec(ctx, `UPDATE team_invitations SET expires_at=now()-interval '1 second' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	_, err = s.Respond(ctx, guest, token, true)
	reason(t, err, "invite_expired")
	if _, err := d.Admin.Exec(ctx, `UPDATE team_invitations SET expires_at=now()+interval '1 day' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Respond(ctx, guest, token, true); err != nil {
		t.Fatal(err)
	}
	reason(t, s.Remove(ctx, owner, owner.UserID), "owner_transfer_required")
	if err := s.Transfer(ctx, owner, guest.UserID); err != nil {
		t.Fatal(err)
	}
	var oldRole, newRole string
	if err := d.Admin.QueryRow(ctx, `SELECT role FROM memberships WHERE org_id=$1 AND user_id=$2`, owner.OrgID, owner.UserID).Scan(&oldRole); err != nil {
		t.Fatal(err)
	}
	if err := d.Admin.QueryRow(ctx, `SELECT role FROM memberships WHERE org_id=$1 AND user_id=$2`, owner.OrgID, guest.UserID).Scan(&newRole); err != nil || newRole != "owner" || oldRole != "reconciler" {
		t.Fatal("owner transfer", oldRole, newRole, err)
	}
	if _, err := d.Admin.Exec(ctx, `DELETE FROM memberships WHERE org_id=$1 AND user_id=$2`, owner.OrgID, guest.UserID); err == nil {
		t.Fatal("last owner removed")
	}
	newOwner := guest
	newOwner.OrgID = owner.OrgID
	another := person(t)
	otherEmail := another.UserID.String() + "@example.test"
	declined, err := s.Invite(ctx, newOwner, otherEmail, "uploader")
	if err != nil {
		t.Fatal(err)
	}
	token = inviteToken(t, owner.OrgID, otherEmail)
	if _, err := s.Respond(ctx, another, token, false); err != nil {
		t.Fatal(err)
	}
	_, err = s.Respond(ctx, another, token, true)
	reason(t, err, "invite_used")
	declinedID, _ := publicid.Decode("inv", declined.ID)
	reason(t, s.Resend(ctx, newOwner, declinedID), "invite_used")
}

func TestInvitationCapacityRateAndDeliveryFailureAreAtomic(t *testing.T) {
	s := fixture(t)
	owner := person(t)
	ctx := t.Context()
	d := testdb.Get(t)
	if err := s.Convert(ctx, owner, "Empresa"); err != nil {
		t.Fatal(err)
	}
	// A missing delivery configuration must not leave a pending seat behind.
	base := s.cfg.PublicBaseURL
	s.cfg.PublicBaseURL = nil
	if _, err := s.Invite(ctx, owner, "retry@example.test", "uploader"); err == nil {
		t.Fatal("mail failure ignored")
	}
	var count int
	if err := d.Admin.QueryRow(ctx, `SELECT count(*) FROM team_invitations WHERE org_id=$1`, owner.OrgID).Scan(&count); err != nil || count != 0 {
		t.Fatal("orphan invitation", count, err)
	}
	s.cfg.PublicBaseURL = base
	first, err := s.Invite(ctx, owner, " ReTry@Example.Test ", "uploader")
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.Invite(ctx, owner, "retry@example.test", "uploader")
	if err != nil || again.ID != first.ID {
		t.Fatal("duplicate pending seat", again, err)
	}
	if err := d.Admin.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE org_id=$1`, owner.OrgID).Scan(&count); err != nil || count != 1 {
		t.Fatal("duplicate email", count, err)
	}
	_, err = s.Invite(ctx, owner, owner.UserID.String()+"@example.test", "uploader")
	reason(t, err, "already_member")
	// Fill all but one seat; concurrent invites cannot both consume the last one.
	for i := 0; i < contracts.IDsAccountMembersMax-3; i++ {
		user := uuid.New()
		if _, err := d.Admin.Exec(ctx, `INSERT INTO users(id,email,email_verified_at) VALUES($1,$2,now())`, user, user.String()+"@example.test"); err != nil {
			t.Fatal(err)
		}
		if _, err := d.Admin.Exec(ctx, `INSERT INTO memberships(org_id,user_id,role) VALUES($1,$2,'uploader')`, owner.OrgID, user); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, address := range []string{"last-a@example.test", "last-b@example.test"} {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := s.Invite(ctx, owner, address, "uploader"); results <- err }()
	}
	wg.Wait()
	close(results)
	accepted := 0
	for err := range results {
		if err == nil {
			accepted++
		} else {
			reason(t, err, "member_limit")
		}
	}
	if accepted != 1 {
		t.Fatal("last seat raced", accepted)
	}
	// Revoking frees the reservation, but the hourly delivery limit still applies.
	firstID, _ := publicid.Decode("inv", first.ID)
	if err := s.Revoke(ctx, owner, firstID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Admin.Exec(ctx, `INSERT INTO team_events(org_id,actor_id,action) SELECT $1,$2,'invitation.resent' FROM generate_series(1,$3)`, owner.OrgID, owner.UserID, InvitationsPerHour); err != nil {
		t.Fatal(err)
	}
	_, err = s.Invite(ctx, owner, "rate@example.test", "uploader")
	reason(t, err, "invite_rate_limited")
}
