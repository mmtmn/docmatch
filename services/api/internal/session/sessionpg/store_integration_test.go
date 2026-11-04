//go:build integration

package sessionpg

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/mmtmn/docmatch/services/api/internal/crypto"
	"github.com/mmtmn/docmatch/services/api/internal/db/sqlc"
	"github.com/mmtmn/docmatch/services/api/internal/session"
	"github.com/mmtmn/docmatch/services/api/internal/testdb"
)

// getDB skips with a clear reason when neither a database URL nor Docker is
// available, instead of failing the run.
func getDB(t *testing.T) *testdb.DB {
	t.Helper()
	if os.Getenv(testdb.EnvDatabaseURL) == "" && os.Getenv("DOCKER_HOST") == "" {
		if _, err := os.Stat("/var/run/docker.sock"); err != nil {
			t.Skip("Docker indisponível (sem /var/run/docker.sock e sem DOCKER_HOST): defina DM_TEST_DATABASE_URL ou suba o Docker")
		}
	}
	return testdb.Get(t)
}

func newUser(t *testing.T, d *testdb.DB) uuid.UUID {
	t.Helper()
	u, err := sqlc.New(d.App).CreateUser(context.Background(), sqlc.CreateUserParams{Email: "s-" + uuid.NewString() + "@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	return u.ID
}

func TestStoreRoundTrip(t *testing.T) {
	d := getDB(t)
	ctx := context.Background()
	store := New(d.App)
	userID := newUser(t, d)
	now := time.Now().UTC().Truncate(time.Microsecond)
	id, _ := uuid.NewV7()
	row := session.Session{
		ID: id, UserID: userID, FamilyID: id, DeviceHash: crypto.Digest("d"), RefreshHash: crypto.Digest("r"), AccessHash: crypto.Digest("a"),
		IPPrefix: "203.0.113.0/24", UserAgent: "teste", CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(time.Hour), AccessExpiresAt: now.Add(10 * time.Minute),
	}
	if err := store.Insert(ctx, row); err != nil {
		t.Fatal(err)
	}
	got, ok, err := store.ByID(ctx, id)
	if err != nil || !ok || got.UserID != userID || got.OrgID != uuid.Nil || got.IPPrefix != "203.0.113.0/24" || !got.AccessExpiresAt.Equal(row.AccessExpiresAt) || got.Rotations != 0 {
		t.Fatalf("ByID: %+v %v %v", got, ok, err)
	}
	if _, ok, err := store.ByID(ctx, uuid.New()); err != nil || ok {
		t.Fatal("id desconhecido")
	}
	rotated, ok, err := store.Rotate(ctx, id, session.Rotation{RefreshHash: crypto.Digest("r2"), AccessHash: crypto.Digest("a2"), ExpiresAt: now.Add(2 * time.Hour), AccessExpiresAt: now.Add(20 * time.Minute)})
	if err != nil || !ok || rotated.Rotations != 1 || rotated.RotatedAt == nil || !crypto.Equal(rotated.RefreshHash, crypto.Digest("r2")) {
		t.Fatalf("Rotate: %+v %v %v", rotated, ok, err)
	}
	if err := store.Touch(ctx, id); err != nil {
		t.Fatal(err)
	}
	list, err := store.ListActive(ctx, userID)
	if err != nil || len(list) != 1 || list[0].ID != id {
		t.Fatalf("ListActive: %v %v", list, err)
	}
	// Second session of the same user; RevokeUser keeps the one named.
	id2, _ := uuid.NewV7()
	row2 := row
	row2.ID, row2.FamilyID, row2.RefreshHash, row2.AccessHash = id2, id2, crypto.Digest("r3"), crypto.Digest("a3")
	if err := store.Insert(ctx, row2); err != nil {
		t.Fatal(err)
	}
	if n, err := store.RevokeUser(ctx, userID, id2); err != nil || n != 1 {
		t.Fatalf("RevokeUser: %d %v", n, err)
	}
	if got, _, _ := store.ByID(ctx, id); got.RevokedAt == nil {
		t.Fatal("primeira sessão deveria estar revogada")
	}
	if _, ok, _ := store.Rotate(ctx, id, session.Rotation{RefreshHash: crypto.Digest("x"), AccessHash: crypto.Digest("y"), ExpiresAt: now, AccessExpiresAt: now}); ok {
		t.Fatal("rotação de sessão revogada")
	}
	if n, _ := store.RevokeFamily(ctx, id2); n != 1 {
		t.Fatalf("RevokeFamily: %d", n)
	}
	if n, _ := store.Revoke(ctx, id2); n != 0 {
		t.Fatal("revogar duas vezes não conta")
	}
	if list, _ := store.ListActive(ctx, userID); len(list) != 0 {
		t.Fatal("nada ativo")
	}
	if n, err := store.DeleteStale(ctx, now.Add(48*time.Hour)); err != nil || n < 2 {
		t.Fatalf("DeleteStale: %d %v", n, err)
	}
	if _, ok, _ := store.ByID(ctx, id); ok {
		t.Fatal("sessão antiga apagada")
	}
}
