//go:build integration

package pgstore_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/mmtmn/docmatch/services/api/internal/credits"
	"github.com/mmtmn/docmatch/services/api/internal/credits/credtest"
	"github.com/mmtmn/docmatch/services/api/internal/db"
	"github.com/mmtmn/docmatch/services/api/internal/db/pgstore"
	"github.com/mmtmn/docmatch/services/api/internal/testdb"
)

func TestMain(m *testing.M) { testdb.Main(m) }

// newOrg creates an organization row through the application role.
func newOrg(t *testing.T, d *testdb.DB) uuid.UUID {
	t.Helper()
	id := uuid.New()
	err := db.WithOrgTx(context.Background(), d.App, id, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "INSERT INTO orgs (id, name, slug) VALUES ($1, $2, $3)", id, "Org "+id.String()[:8], "org-"+id.String())
		return err
	})
	if err != nil {
		t.Fatalf("criar org: %v", err)
	}
	return id
}

func TestCreditStoreConformance(t *testing.T) {
	d := testdb.Get(t)
	credtest.Run(t, func(t *testing.T) (credits.Store, func(t *testing.T) uuid.UUID) {
		return pgstore.NewCreditStore(d.App), func(t *testing.T) uuid.UUID { return newOrg(t, d) }
	})
}

// TestCreditStoreDatabaseConstraints checks the guards that live only in
// PostgreSQL: the CHECK on balance/held and the append-only ledger.
func TestCreditStoreDatabaseConstraints(t *testing.T) {
	d := testdb.Get(t)
	ctx := context.Background()
	org := newOrg(t, d)
	store := pgstore.NewCreditStore(d.App)
	err := store.InOrgTx(ctx, org, func(ctx context.Context, tx credits.Tx) error {
		if _, err := tx.AccountForUpdate(ctx, org); err != nil {
			return err
		}
		_, err := tx.UpdateAccount(ctx, org, 10, 20)
		return err
	})
	if !db.IsCheckViolation(err) {
		t.Fatalf("held > balance deveria violar o CHECK: %v", err)
	}
	svc := credits.New(store)
	if _, err := svc.Grant(ctx, org, credits.GrantParams{Kind: credits.GrantPlan, Amount: 5}); err != nil {
		t.Fatal(err)
	}
	err = db.WithOrgTx(ctx, d.App, org, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "UPDATE credit_ledger SET amount = 999 WHERE org_id = $1", org)
		return err
	})
	if err == nil {
		t.Fatal("o papel da aplicação não pode alterar o razão")
	}
	// The ledger sums agree with the account.
	var balance, held int64
	err = db.WithOrgTx(ctx, d.App, org, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, "SELECT coalesce(sum(amount),0), coalesce(sum(held_delta),0) FROM credit_ledger WHERE org_id = $1", org).Scan(&balance, &held)
	})
	if err != nil || balance != 5 || held != 0 {
		t.Fatalf("somas do razão: %d %d %v", balance, held, err)
	}
}
