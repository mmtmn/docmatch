//go:build integration

package db_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/mmtmn/docmatch/services/api/internal/testdb"
)

func TestReleaseGuardProtectsAcceptedProjectWork(t *testing.T) {
	d := testdb.Get(t)
	sql, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "infra", "scripts", "release-compatibility.sql"))
	if err != nil {
		t.Fatal(err)
	}
	check := func(t *testing.T, support string, setup string, wantBlocked bool) {
		t.Helper()
		tx, err := d.Admin.Begin(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(context.Background()) }()
		if setup != "" {
			if _, err = tx.Exec(t.Context(), setup); err != nil {
				t.Fatal(err)
			}
		}
		if _, err = tx.Exec(t.Context(), "SELECT set_config('docmatch.release_account_context',$1,true)", support); err != nil {
			t.Fatal(err)
		}
		_, err = tx.Exec(t.Context(), string(sql))
		if wantBlocked {
			if err == nil || !strings.Contains(err.Error(), "release incompatible") {
				t.Fatalf("downgrade guard: %v", err)
			}
		} else if err != nil {
			t.Fatal(err)
		}
	}
	// Every mutation stays in a rolled-back transaction; the live database is never used.
	check(t, "0", "", false)
	project := `WITH org AS (INSERT INTO orgs(name,slug) VALUES('release guard','release-'||uuidv7()) RETURNING id)
 INSERT INTO projects(org_id,name) SELECT id,'Saved evidence' FROM org`
	check(t, "0", project, true)
	check(t, "1", project, false)
	seat := `WITH org AS (INSERT INTO orgs(name,slug) VALUES('release seat','release-'||uuidv7()) RETURNING id),
 person AS (INSERT INTO users(email) VALUES('release-'||uuidv7()||'@example.test') RETURNING id)
 INSERT INTO memberships(org_id,user_id,role) SELECT org.id,person.id,'uploader' FROM org,person`
	check(t, "0", seat, true)
	check(t, "1", seat, false)
	// Missing legacy tables are allowed before the feature migration is installed.
	tx, err := d.Admin.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	for _, table := range []string{"projects", "team_invitations", "extension_grants"} {
		if _, err = tx.Exec(t.Context(), "DROP TABLE "+pgx.Identifier{table}.Sanitize()+" CASCADE"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = tx.Exec(t.Context(), string(sql)); err != nil {
		t.Fatal(err)
	}
}
