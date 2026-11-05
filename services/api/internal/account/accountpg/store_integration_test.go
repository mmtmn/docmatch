//go:build integration

package accountpg

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/mmtmn/docmatch/services/api/internal/account"
	"github.com/mmtmn/docmatch/services/api/internal/audit"
	"github.com/mmtmn/docmatch/services/api/internal/auth"
	"github.com/mmtmn/docmatch/services/api/internal/crypto"
	"github.com/mmtmn/docmatch/services/api/internal/db"
	"github.com/mmtmn/docmatch/services/api/internal/db/pgstore"
	"github.com/mmtmn/docmatch/services/api/internal/db/sqlc"
	"github.com/mmtmn/docmatch/services/api/internal/email"
	"github.com/mmtmn/docmatch/services/api/internal/email/emailpg"
	"github.com/mmtmn/docmatch/services/api/internal/publicid"
	"github.com/mmtmn/docmatch/services/api/internal/session"
	"github.com/mmtmn/docmatch/services/api/internal/session/sessionpg"
	"github.com/mmtmn/docmatch/services/api/internal/testdb"
)

func getDB(t *testing.T) *testdb.DB {
	t.Helper()
	if os.Getenv(testdb.EnvDatabaseURL) == "" && os.Getenv("DOCKER_HOST") == "" {
		if _, err := os.Stat("/var/run/docker.sock"); err != nil {
			t.Skip("Docker indisponível (sem /var/run/docker.sock e sem DOCKER_HOST): defina DM_TEST_DATABASE_URL ou suba o Docker")
		}
	}
	return testdb.Get(t)
}

func signup(t *testing.T, s *Store, keys *crypto.Keyring, email, provider, sub string) (account.User, account.Org) {
	t.Helper()
	orgID, _ := uuid.NewV7()
	wrapped, err := crypto.WrapDataKey(keys.Key(crypto.PurposeKEK), crypto.NewDataKey(), orgID.String())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	u := account.User{Email: email, DisplayName: "Pessoa", EmailVerifiedAt: &now}
	switch provider {
	case auth.ProviderGoogle:
		u.GoogleSub = sub
	case auth.ProviderApple:
		u.AppleSub = sub
	}
	o := account.Org{ID: orgID, Name: "Org " + email, Slug: publicid.Encode(publicid.PrefixOrg, orgID), PlanCode: "starter", Country: "BR"}
	u, o, err = s.Signup(context.Background(), u, o, wrapped)
	if err != nil {
		t.Fatal(err)
	}
	return u, o
}

func TestUsersTenantsAndRLS(t *testing.T) {
	d := getDB(t)
	ctx := context.Background()
	keys, _ := crypto.NewKeyring(make([]byte, 32))
	store := New(d.App, nil)
	a, orgA := signup(t, store, keys, "a-"+uuid.NewString()+"@example.com", auth.ProviderGoogle, "g-"+uuid.NewString())
	b, orgB := signup(t, store, keys, "b-"+uuid.NewString()+"@example.com", auth.ProviderApple, "ap-"+uuid.NewString())

	if _, _, err := store.Signup(ctx, account.User{}, account.Org{}, nil); err == nil {
		t.Fatal("signup sem id de org")
	}
	if got, ok, err := store.UserByEmail(ctx, a.Email); err != nil || !ok || got.ID != a.ID || got.GoogleSub != a.GoogleSub {
		t.Fatalf("UserByEmail: %+v %v %v", got, ok, err)
	}
	if got, ok, _ := store.UserByProvider(ctx, auth.ProviderApple, b.AppleSub); !ok || got.ID != b.ID {
		t.Fatal("UserByProvider apple")
	}
	if _, ok, _ := store.UserByProvider(ctx, auth.ProviderGoogle, "ninguem"); ok {
		t.Fatal("sub desconhecido")
	}
	if _, ok, _ := store.UserByProvider(ctx, "outro", "x"); ok {
		t.Fatal("provedor desconhecido")
	}
	if _, ok, _ := store.UserByID(ctx, uuid.New()); ok {
		t.Fatal("UserByID desconhecido")
	}
	tnt, ok, err := store.PrimaryTenant(ctx, a.ID)
	if err != nil || !ok || tnt.Org.ID != orgA.ID || tnt.Membership.Role != account.RoleOwner || tnt.Subscription != nil {
		t.Fatalf("PrimaryTenant: %+v %v %v", tnt, ok, err)
	}
	if _, ok, _ := store.PrimaryTenant(ctx, uuid.New()); ok {
		t.Fatal("sem organização")
	}
	if _, ok, _ := store.Tenant(ctx, uuid.Nil, a.ID); ok {
		t.Fatal("org nula")
	}
	// Tenant isolation: inside A's transaction, B's rows do not exist.
	if _, ok, err := store.Tenant(ctx, orgB.ID, a.ID); err != nil || ok {
		t.Fatalf("A não é membro de B: %v %v", ok, err)
	}
	err = db.WithOrgTx(ctx, d.App, orgA.ID, func(ctx context.Context, tx pgx.Tx) error {
		q := sqlc.New(tx)
		if _, err := q.GetOrg(ctx, orgB.ID); err == nil {
			t.Fatal("RLS: A enxergou a org de B")
		}
		if _, err := q.GetMembership(ctx, sqlc.GetMembershipParams{OrgID: orgB.ID, UserID: b.ID}); err == nil {
			t.Fatal("RLS: A enxergou o vínculo de B")
		}
		if _, err := q.AuthTenantKey(ctx, orgB.ID); err == nil {
			t.Fatal("RLS: A enxergou a chave de B")
		}
		if _, err := q.AuthTenantKey(ctx, orgA.ID); err != nil {
			t.Fatalf("A deveria ver a própria chave: %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// Writing B's rows from A's transaction violates the policy (and aborts
	// the transaction, so it runs on its own).
	err = db.WithOrgTx(ctx, d.App, orgA.ID, func(ctx context.Context, tx pgx.Tx) error {
		_, err := sqlc.New(tx).AuthInsertTenantKey(ctx, sqlc.AuthInsertTenantKeyParams{OrgID: orgB.ID, WrappedKey: []byte("x"), KekVersion: 1})
		return err
	})
	if !testdb.IsPermissionDenied(err) {
		t.Fatalf("RLS: A escreveu na chave de B: %v", err)
	}
	// Linking, verification, name, login timestamp, profile and org name.
	if err := store.LinkProvider(ctx, b.ID, auth.ProviderGoogle, "g-b"); err != nil {
		t.Fatal(err)
	}
	if err := store.LinkProvider(ctx, b.ID, "outro", "x"); err == nil {
		t.Fatal("provedor desconhecido no vínculo")
	}
	if err := store.MarkEmailVerified(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.SetDisplayName(ctx, b.ID, "Bia"); err != nil {
		t.Fatal(err)
	}
	if err := store.TouchLogin(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	yes := true
	if err := store.UpdateProfile(ctx, b.ID, account.ProfilePatch{DisplayName: ptr("Bia Souza"), Notifications: &account.NotificationPrefs{ProductNews: &yes}}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateOrgName(ctx, orgB.ID, "Souza Ltda"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetPassword(ctx, b.ID, "$argon2id$x"); err != nil {
		t.Fatal(err)
	}
	got, _, _ := store.UserByID(ctx, b.ID)
	if got.GoogleSub != "g-b" || got.DisplayName != "Bia Souza" || got.LastLoginAt == nil || got.Notifications.ProductNews == nil || !*got.Notifications.ProductNews || got.PasswordHash != "$argon2id$x" {
		t.Fatalf("usuário atualizado: %+v", got)
	}
	if tnt, _, _ := store.Tenant(ctx, orgB.ID, b.ID); tnt.Org.Name != "Souza Ltda" {
		t.Fatalf("org: %+v", tnt.Org)
	}
	// Credits: empty account.
	if c, err := store.Credits(ctx, orgA.ID); err != nil || c.Balance != 0 || c.NextExpiryAt != nil {
		t.Fatalf("credits: %+v %v", c, err)
	}
	// Subscription shows in the tenant.
	if _, err := d.Admin.Exec(ctx, "INSERT INTO subscriptions (org_id, plan_code, status) VALUES ($1, 'pro', 'active')", orgA.ID); err != nil {
		t.Fatal(err)
	}
	if tnt, _, _ := store.Tenant(ctx, orgA.ID, a.ID); tnt.Subscription == nil || tnt.Subscription.PlanCode != "pro" {
		t.Fatalf("assinatura: %+v", tnt)
	}
}

func ptr[T any](v T) *T { return &v }

func TestChallengesExportsAndErasure(t *testing.T) {
	d := getDB(t)
	ctx := context.Background()
	keys, _ := crypto.NewKeyring(make([]byte, 32))
	inserter, err := emailpg.NewInserter(d.App)
	if err != nil {
		t.Fatal(err)
	}
	store := New(d.App, inserter)
	u, o := signup(t, store, keys, "c-"+uuid.NewString()+"@example.com", "", "")

	// Challenges: the row and the e-mail land together.
	id, _ := uuid.NewV7()
	ch := auth.Challenge{ID: id, Email: u.Email, TokenHash: crypto.Digest("tok"), CodeHash: crypto.Digest("code"), Purpose: "login", IPPrefix: "203.0.113.0/24", ExpiresAt: time.Now().Add(15 * time.Minute)}
	if err := store.CreateChallenge(ctx, ch, email.Message{To: u.Email, Subject: "Código", Text: "123 456"}); err != nil {
		t.Fatal(err)
	}
	var outbox int
	if err := d.Admin.QueryRow(ctx, "SELECT count(*) FROM outbox WHERE kind = 'email' AND payload->>'to' = $1", u.Email).Scan(&outbox); err != nil || outbox != 1 {
		t.Fatalf("outbox: %d %v", outbox, err)
	}
	if err := store.CreateChallenge(ctx, ch, email.Message{To: "x"}); err == nil {
		t.Fatal("e-mail inválido reverte o desafio")
	}
	got, ok, err := store.ChallengeByID(ctx, id)
	if err != nil || !ok || got.Email != u.Email || got.Attempts != 0 || got.IPPrefix != "203.0.113.0/24" {
		t.Fatalf("ChallengeByID: %+v %v %v", got, ok, err)
	}
	if n, ok, err := store.ClaimChallengeAttempt(ctx, id, 2); err != nil || !ok || n != 1 {
		t.Fatalf("attempt: %d %v %v", n, ok, err)
	}
	if n, ok, err := store.ClaimChallengeAttempt(ctx, id, 2); err != nil || !ok || n != 2 {
		t.Fatalf("attempt 2: %d %v %v", n, ok, err)
	}
	if _, ok, err := store.ClaimChallengeAttempt(ctx, id, 2); err != nil || ok {
		t.Fatalf("teto de tentativas: %v %v", ok, err)
	}
	if c, ok, err := store.ConsumeChallenge(ctx, id); err != nil || !ok || c.ConsumedAt == nil {
		t.Fatalf("consume: %+v %v %v", c, ok, err)
	}
	if _, ok, _ := store.ConsumeChallenge(ctx, id); ok {
		t.Fatal("uso único")
	}
	if _, ok, _ := store.ChallengeByID(ctx, uuid.New()); ok {
		t.Fatal("desafio desconhecido")
	}
	if n, err := store.DeleteStaleChallenges(ctx, time.Now().Add(time.Hour)); err != nil || n < 1 {
		t.Fatalf("limpeza: %d %v", n, err)
	}

	// Exports.
	sessions := sessionpg.New(d.App)
	now := time.Now()
	sid, _ := uuid.NewV7()
	if err := sessions.Insert(ctx, session.Session{ID: sid, UserID: u.ID, OrgID: o.ID, FamilyID: sid, DeviceHash: []byte{1}, RefreshHash: crypto.Digest("r"), AccessHash: crypto.Digest("a"), CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(time.Hour), AccessExpiresAt: now.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	auditSvc := audit.New(pgstore.NewAuditStore(d.App))
	if _, err := auditSvc.Record(ctx, &o.ID, audit.Actor{Type: audit.ActorUser, ID: u.ID.String()}, "auth.login", audit.Target{Type: "session", ID: sid.String()}, nil); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := store.LatestExport(ctx, u.ID); ok {
		t.Fatal("sem exportações")
	}
	exp, err := store.CreateExport(ctx, u.ID, o.ID)
	if err != nil || exp.Status != account.ExportPending {
		t.Fatalf("CreateExport: %+v %v", exp, err)
	}
	if latest, ok, _ := store.LatestExport(ctx, u.ID); !ok || latest.ID != exp.ID {
		t.Fatal("LatestExport")
	}
	if _, ok, _ := store.ExportArchive(ctx, exp.ID); ok {
		t.Fatal("arquivo antes de pronta")
	}
	bundle, err := store.ExportBundle(ctx, u.ID, o.ID)
	if err != nil || bundle.User.ID != u.ID || len(bundle.Sessions) != 1 || len(bundle.Audit) != 1 || bundle.Audit[0].Action != "auth.login" {
		t.Fatalf("bundle: %+v %v", bundle, err)
	}
	if _, err := store.ExportBundle(ctx, uuid.New(), o.ID); err == nil {
		t.Fatal("bundle sem usuário")
	}
	if _, err := store.ExportBundle(ctx, u.ID, uuid.New()); err == nil {
		t.Fatal("bundle sem org")
	}
	archive, err := account.BuildArchive(bundle, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteExport(ctx, exp.ID, archive, now.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got, ok, _ := store.ExportByID(ctx, exp.ID); !ok || got.Status != account.ExportReady || got.SizeBytes != int64(len(archive)) || got.ExpiresAt == nil {
		t.Fatalf("ExportByID: %+v", got)
	}
	if data, ok, err := store.ExportArchive(ctx, exp.ID); err != nil || !ok || len(data) != len(archive) {
		t.Fatalf("ExportArchive: %d %v %v", len(data), ok, err)
	}
	if n, err := store.ExpireExports(ctx, now.Add(48*time.Hour)); err != nil || n != 1 {
		t.Fatalf("ExpireExports: %d %v", n, err)
	}
	if _, ok, _ := store.ExportArchive(ctx, exp.ID); ok {
		t.Fatal("arquivo apagado ao expirar")
	}
	exp2, _ := store.CreateExport(ctx, u.ID, o.ID)
	if err := store.FailExport(ctx, exp2.ID, "boom"); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := store.ExportByID(ctx, exp2.ID); got.Status != account.ExportFailed || got.Error != "boom" {
		t.Fatalf("falha: %+v", got)
	}
	if _, ok, _ := store.ExportByID(ctx, uuid.New()); ok {
		t.Fatal("exportação desconhecida")
	}
	if err := store.EnqueueEmail(ctx, &o.ID, email.Message{To: u.Email, Subject: "Pronta", Text: "link"}); err != nil {
		t.Fatal(err)
	}

	// Deletion: request once, then erase after the grace period.
	when := time.Now().Add(-time.Minute)
	if ok, err := store.RequestDeletion(ctx, u.ID, when); err != nil || !ok {
		t.Fatalf("RequestDeletion: %v %v", ok, err)
	}
	if ok, _ := store.RequestDeletion(ctx, u.ID, when); ok {
		t.Fatal("segundo pedido")
	}
	due, err := store.DueErasures(ctx, time.Now(), 10)
	if err != nil || !contains(due, u.ID) {
		t.Fatalf("DueErasures: %v %v", due, err)
	}
	if rep, _ := store.Erase(ctx, u.ID, when.Add(-time.Hour)); rep.Erased {
		t.Fatal("antes do prazo não apaga")
	}
	rep, err := store.Erase(ctx, u.ID, time.Now())
	if err != nil || !rep.Erased || len(rep.OrgsDeleted) != 1 || rep.OrgsDeleted[0] != o.ID {
		t.Fatalf("Erase: %+v %v", rep, err)
	}
	if _, ok, _ := store.UserByID(ctx, u.ID); ok {
		t.Fatal("usuário apagado")
	}
	var keysLeft, orgsLeft, sessionsLeft int
	_ = d.Admin.QueryRow(ctx, "SELECT count(*) FROM tenant_keys WHERE org_id = $1", o.ID).Scan(&keysLeft)
	_ = d.Admin.QueryRow(ctx, "SELECT count(*) FROM orgs WHERE id = $1", o.ID).Scan(&orgsLeft)
	_ = d.Admin.QueryRow(ctx, "SELECT count(*) FROM sessions WHERE user_id = $1", u.ID).Scan(&sessionsLeft)
	if keysLeft != 0 || orgsLeft != 0 || sessionsLeft != 0 {
		t.Fatalf("restos: chaves %d orgs %d sessões %d", keysLeft, orgsLeft, sessionsLeft)
	}
	if rep, err := store.Erase(ctx, u.ID, time.Now()); err != nil || rep.Erased {
		t.Fatal("apagar de novo é inofensivo")
	}
	// Shared organization: the leaving member is removed, the org survives.
	m1, shared := signup(t, store, keys, "m1-"+uuid.NewString()+"@example.com", "", "")
	m2, own2 := signup(t, store, keys, "m2-"+uuid.NewString()+"@example.com", "", "")
	if err := db.WithOrgTx(ctx, d.App, shared.ID, func(ctx context.Context, tx pgx.Tx) error {
		return sqlc.New(tx).AddMembership(ctx, sqlc.AddMembershipParams{OrgID: shared.ID, UserID: m2.ID, Role: account.RoleMember})
	}); err != nil {
		t.Fatal(err)
	}
	_, _ = store.RequestDeletion(ctx, m2.ID, when)
	rep, err = store.Erase(ctx, m2.ID, time.Now())
	if err != nil || !rep.Erased || rep.MembershipsRemoved != 1 || len(rep.OrgsDeleted) != 1 || rep.OrgsDeleted[0] != own2.ID {
		t.Fatalf("erase compartilhado: %+v %v", rep, err)
	}
	if tnt, ok, _ := store.Tenant(ctx, shared.ID, m1.ID); !ok || tnt.Org.ID != shared.ID {
		t.Fatal("org compartilhada sobrevive")
	}
}

func contains(ids []uuid.UUID, id uuid.UUID) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}
