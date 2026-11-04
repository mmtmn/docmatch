//go:build integration

package db_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/mmtmn/docmatch/services/api/internal/db"
	"github.com/mmtmn/docmatch/services/api/internal/db/migrations"
	"github.com/mmtmn/docmatch/services/api/internal/logx"
	"github.com/mmtmn/docmatch/services/api/internal/testdb"
)

func TestMain(m *testing.M) { testdb.Main(m) }

func adminConn(t *testing.T, d *testdb.DB) *pgx.Conn {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), d.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return conn
}

func TestMigratorStatusAndChecksum(t *testing.T) {
	d := testdb.Get(t)
	ctx := context.Background()
	m, err := db.NewMigrator(adminConn(t, d), testdb.AppRole, logx.Discard())
	if err != nil {
		t.Fatal(err)
	}
	ms := migrations.MustLoad()
	st, err := m.Status(ctx, ms)
	if err != nil {
		t.Fatal(err)
	}
	if len(st) != len(ms) || st[0].Applied == nil || st[0].Drifted {
		t.Fatalf("status inesperado: %+v", st)
	}
	// Up is idempotent.
	done, err := m.Up(ctx, ms)
	if err != nil || len(done) != 0 {
		t.Fatalf("segundo Up deveria não fazer nada: %v %v", done, err)
	}
	// A changed script is refused.
	changed := make([]migrations.Migration, len(ms))
	copy(changed, ms)
	changed[0].Checksum = strings.Repeat("0", 64)
	if _, err := m.Up(ctx, changed); err == nil || !strings.Contains(err.Error(), "alterada") {
		t.Fatalf("checksum divergente deveria falhar: %v", err)
	}
	st, _ = m.Status(ctx, changed)
	if !st[0].Drifted {
		t.Fatal("Status deveria marcar a divergência")
	}
}

func TestMigratorDownUpRoundTrip(t *testing.T) {
	if testing.Short() {
		t.Skip("round trip completo pulado com -short")
	}
	d := testdb.Get(t)
	ctx := context.Background()
	m, err := db.NewMigrator(adminConn(t, d), testdb.AppRole, logx.Discard())
	if err != nil {
		t.Fatal(err)
	}
	ms := migrations.MustLoad()
	reverted, err := m.Down(ctx, ms, 1)
	if err != nil || len(reverted) != 1 {
		t.Fatalf("Down: %v %v", reverted, err)
	}
	var n int
	// River's tables belong to its own migrator and stay in place.
	if err := d.Admin.QueryRow(ctx, "SELECT count(*) FROM pg_tables WHERE schemaname = 'public' AND tablename <> 'schema_migrations' AND tablename NOT LIKE 'river\\_%'").Scan(&n); err != nil || n != 0 {
		t.Fatalf("tabelas restantes após down: %d (%v)", n, err)
	}
	applied, err := m.Up(ctx, ms)
	if err != nil || len(applied) != 1 {
		t.Fatalf("Up após Down: %v %v", applied, err)
	}
	if err := d.Admin.QueryRow(ctx, "SELECT count(*) FROM pg_tables WHERE schemaname = 'public'").Scan(&n); err != nil || n < 40 {
		t.Fatalf("tabelas após re-up: %d (%v)", n, err)
	}
	// Down of everything (steps <= 0) then back up keeps the harness usable.
	if _, err := m.Down(ctx, ms, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Up(ctx, ms); err != nil {
		t.Fatal(err)
	}
}

func TestMigratorRejectsBrokenScript(t *testing.T) {
	d := testdb.Get(t)
	ctx := context.Background()
	m, err := db.NewMigrator(adminConn(t, d), testdb.AppRole, logx.Discard())
	if err != nil {
		t.Fatal(err)
	}
	ms := append([]migrations.Migration{}, migrations.MustLoad()...)
	ms = append(ms, migrations.Migration{Version: len(ms) + 1, Name: "quebrada", Up: "CREATE TABLE will_rollback (id int); SELECT * FROM tabela_inexistente;", Down: "DROP TABLE will_rollback;", Checksum: strings.Repeat("a", 64)})
	if _, err := m.Up(ctx, ms); err == nil || !strings.Contains(err.Error(), "quebrada") {
		t.Fatalf("script quebrado deveria falhar com o nome: %v", err)
	}
	var exists bool
	if err := d.Admin.QueryRow(ctx, "SELECT to_regclass('will_rollback') IS NOT NULL").Scan(&exists); err != nil || exists {
		t.Fatalf("transação da migração quebrada deveria ter sido revertida (exists=%v, %v)", exists, err)
	}
	var recorded int
	if err := d.Admin.QueryRow(ctx, "SELECT count(*) FROM schema_migrations WHERE version = $1", len(ms)).Scan(&recorded); err != nil || recorded != 0 {
		t.Fatalf("migração quebrada não pode ficar registrada: %d (%v)", recorded, err)
	}
}

func TestNewMigratorValidation(t *testing.T) {
	if _, err := db.NewMigrator(nil, "", nil); err == nil {
		t.Fatal("conexão nula deveria falhar")
	}
	d := testdb.Get(t)
	if _, err := db.NewMigrator(adminConn(t, d), "bad role", nil); err == nil {
		t.Fatal("papel inválido deveria falhar")
	}
}

func TestAppRoleAndRLS(t *testing.T) {
	d := testdb.Get(t)
	ctx := context.Background()
	var bypass, login bool
	if err := d.Admin.QueryRow(ctx, "SELECT rolbypassrls, rolcanlogin FROM pg_roles WHERE rolname = $1", testdb.AppRole).Scan(&bypass, &login); err != nil {
		t.Fatal(err)
	}
	if bypass || login {
		t.Fatalf("papel da aplicação precisa ser NOBYPASSRLS NOLOGIN (bypass=%v login=%v)", bypass, login)
	}
	var cur string
	if err := d.App.QueryRow(ctx, "SELECT current_user").Scan(&cur); err != nil || cur != testdb.AppRole {
		t.Fatalf("pool da aplicação deveria assumir o papel: %q (%v)", cur, err)
	}

	orgA, orgB := uuid.New(), uuid.New()
	for _, id := range []uuid.UUID{orgA, orgB} {
		err := db.WithOrgTx(ctx, d.App, id, func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, "INSERT INTO orgs (id, name, slug) VALUES ($1, $2, $3)", id, "Org "+id.String()[:8], "org-"+id.String())
			return err
		})
		if err != nil {
			t.Fatalf("criar org: %v", err)
		}
	}
	// Cross-tenant insert is refused by the policy.
	err := db.WithOrgTx(ctx, d.App, orgA, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "INSERT INTO credit_accounts (org_id, balance, held) VALUES ($1, 10, 0)", orgB)
		return err
	})
	if !testdb.IsPermissionDenied(err) {
		t.Fatalf("inserção em outro locatário deveria violar RLS: %v", err)
	}
	// Reads are scoped.
	var visible int
	_ = db.WithOrgTx(ctx, d.App, orgA, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, "SELECT count(*) FROM orgs").Scan(&visible)
	})
	if visible != 1 {
		t.Fatalf("org A deveria ver só a si mesma, viu %d", visible)
	}
	_ = db.WithSystemTx(ctx, d.App, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, "SELECT count(*) FROM orgs WHERE id = ANY($1)", []uuid.UUID{orgA, orgB}).Scan(&visible)
	})
	if visible != 2 {
		t.Fatalf("modo sistema deveria ver as duas, viu %d", visible)
	}
	// Without any context nothing is visible, and the app role cannot update the ledger.
	if err := d.App.QueryRow(ctx, "SELECT count(*) FROM orgs").Scan(&visible); err != nil || visible != 0 {
		t.Fatalf("sem contexto nada deveria aparecer: %d %v", visible, err)
	}
	err = db.WithOrgTx(ctx, d.App, orgA, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "UPDATE credit_ledger SET amount = 0")
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("UPDATE em credit_ledger deveria ser negado: %v", err)
	}
	for _, table := range []string{"audit_log", "feedback"} {
		err = db.WithOrgTx(ctx, d.App, orgA, func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, "DELETE FROM "+table)
			return err
		})
		if err == nil || !strings.Contains(err.Error(), "permission denied") {
			t.Fatalf("DELETE em %s deveria ser negado: %v", table, err)
		}
	}
}

func TestWithOrgTxRollbackAndPanic(t *testing.T) {
	d := testdb.Get(t)
	ctx := context.Background()
	org := uuid.New()
	err := db.WithOrgTx(ctx, d.App, org, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "INSERT INTO orgs (id, name, slug) VALUES ($1, 'x', $2)", org, "slug-"+org.String()); err != nil {
			return err
		}
		return context.DeadlineExceeded
	})
	if err == nil {
		t.Fatal("erro deveria propagar")
	}
	var n int
	_ = db.WithSystemTx(ctx, d.App, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, "SELECT count(*) FROM orgs WHERE id = $1", org).Scan(&n)
	})
	if n != 0 {
		t.Fatal("transação com erro deveria ter sido revertida")
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("pânico deveria propagar")
			}
		}()
		_ = db.WithOrgTx(ctx, d.App, org, func(ctx context.Context, tx pgx.Tx) error {
			_, _ = tx.Exec(ctx, "INSERT INTO orgs (id, name, slug) VALUES ($1, 'x', $2)", org, "slug-"+org.String())
			panic("explodiu")
		})
	}()
	_ = db.WithSystemTx(ctx, d.App, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, "SELECT count(*) FROM orgs WHERE id = $1", org).Scan(&n)
	})
	if n != 0 {
		t.Fatal("transação com pânico deveria ter sido revertida")
	}
}

func TestPartitions(t *testing.T) {
	d := testdb.Get(t)
	ctx := context.Background()
	parts, err := db.EnsureMonthPartitions(ctx, d.App, 3, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 4*5 {
		t.Fatalf("esperava 20 partições (4 tabelas × 5 meses), veio %d", len(parts))
	}
	again, err := db.EnsureMonthPartitions(ctx, d.App, 3, 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range again {
		if p.Created {
			t.Fatalf("segunda chamada não deveria criar nada: %+v", p)
		}
	}
	all, err := db.ListPartitions(ctx, d.Admin)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	want := map[string]bool{}
	for _, parent := range []string{"credit_ledger", "findings", "api_usage", "audit_log"} {
		want[parent+"_default"] = false
		for m := -1; m <= 3; m++ {
			d := time.Date(now.Year(), now.Month()+time.Month(m), 1, 0, 0, 0, 0, time.UTC)
			want[parent+"_"+d.Format("y2006m01")] = false
		}
	}
	for _, p := range all {
		if _, ok := want[p.Name]; ok {
			want[p.Name] = true
		}
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("partição %s ausente", name)
		}
	}
	if _, err := db.EnsureMonthPartitions(ctx, d.App, -1, 0); err == nil {
		t.Fatal("intervalo negativo deveria falhar")
	}
	var bound string
	if err := d.Admin.QueryRow(ctx, "SELECT pg_get_expr(relpartbound, oid) FROM pg_class WHERE relname = $1", "credit_ledger_"+now.Format("y2006m01")).Scan(&bound); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(bound, now.Format("2006-01")+"-01 00:00:00+00") {
		t.Fatalf("limite da partição não está em UTC: %s", bound)
	}
}

func TestGrantAppRole(t *testing.T) {
	d := testdb.Get(t)
	ctx := context.Background()
	conn := adminConn(t, d)
	if _, err := conn.Exec(ctx, "CREATE TABLE IF NOT EXISTS river_fake (id bigserial PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = conn.Exec(context.Background(), "DROP TABLE IF EXISTS river_fake") })
	if err := db.GrantAppRole(ctx, conn, testdb.AppRole); err != nil {
		t.Fatal(err)
	}
	if _, err := d.App.Exec(ctx, "INSERT INTO river_fake DEFAULT VALUES"); err != nil {
		t.Fatalf("papel da aplicação deveria acessar tabelas river_*: %v", err)
	}
	if err := db.GrantAppRole(ctx, conn, ""); err != nil {
		t.Fatal("papel vazio é no-op")
	}
	if err := db.GrantAppRole(ctx, conn, "bad role"); err == nil {
		t.Fatal("papel inválido deveria falhar")
	}
}

func TestConnectOptions(t *testing.T) {
	d := testdb.Get(t)
	ctx := context.Background()
	pool, err := db.Connect(ctx, db.Options{URL: d.URL, ApplicationName: "docmatch-test-opts", Logger: logx.Discard()})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var name string
	if err := pool.QueryRow(ctx, "SELECT current_setting('application_name')").Scan(&name); err != nil || name != "docmatch-test-opts" {
		t.Fatalf("application_name = %q (%v)", name, err)
	}
	if _, err := db.Connect(ctx, db.Options{URL: d.URL, AppRole: "papel_inexistente_xyz"}); err == nil {
		t.Fatal("SET ROLE para papel inexistente deveria falhar no ping")
	}
}
