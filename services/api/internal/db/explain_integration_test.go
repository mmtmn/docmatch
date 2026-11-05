//go:build integration

package db_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/mmtmn/docmatch/services/api/internal/db"
	"github.com/mmtmn/docmatch/services/api/internal/testdb"
)

// The EXPLAIN budget: every hot query below must be served by indexes. The
// test seeds ~100k ledger rows (plus runs, findings, sessions, keys, rules,
// outbox and audit rows) across several monthly partitions, ANALYZEs, then
// runs EXPLAIN as the application role (so row-level security is part of the
// plan) and fails on any Seq Scan over a watched table or partition.

const (
	seedOrgs       = 20
	seedLedgerRows = 100_000
	seedRuns       = 20_000
	seedFindings   = 60_000
	seedSessions   = 5_000
	seedAPIKeys    = 2_000
	seedRules      = 20_000
	seedOutbox     = 5_000
	seedAudit      = 20_000
	seedHolds      = 10_000
	seedStripe     = 2_000
)

// watched lists the tables whose scans must use an index. Partitions are
// matched by prefix.
var watched = []string{
	"credit_ledger", "findings", "api_usage", "audit_log", "runs", "sessions", "api_keys", "rules",
	"outbox", "credit_accounts", "credit_holds", "credit_grants", "stripe_events", "orgs", "users",
}

type hotQuery struct {
	name string
	// org scopes the EXPLAIN to a tenant transaction (RLS applied); nil runs
	// in system mode (non-tenant tables and worker queries).
	org  *uuid.UUID
	args []any
}

// minPages is the size below which a sequential scan is the right plan: the
// planner reads one or two pages faster than it walks an index. Anything
// larger must come through an index.
const minPages = 4

// errRollbackExplain aborts the explain transaction so nothing is committed.
var errRollbackExplain = errors.New("rollback após EXPLAIN")

func TestExplainBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("orçamento de EXPLAIN pulado com -short")
	}
	d := testdb.Get(t)
	ctx := context.Background()
	queries := loadQueries(t)

	s := seed(t, d)
	org := s.orgs[0]
	var runID, sessionID, keyID, holdID uuid.UUID
	var refresh []byte
	var ledgerSeq int64
	if err := d.Admin.QueryRow(ctx, "SELECT id FROM runs WHERE org_id = $1 LIMIT 1", org).Scan(&runID); err != nil {
		t.Fatal(err)
	}
	if err := d.Admin.QueryRow(ctx, "SELECT id, refresh_hash FROM sessions WHERE user_id = $1 LIMIT 1", s.userID).Scan(&sessionID, &refresh); err != nil {
		t.Fatal(err)
	}
	if err := d.Admin.QueryRow(ctx, "SELECT id FROM api_keys WHERE org_id = $1 LIMIT 1", org).Scan(&keyID); err != nil {
		t.Fatal(err)
	}
	if err := d.Admin.QueryRow(ctx, "SELECT id FROM credit_holds WHERE org_id = $1 LIMIT 1", org).Scan(&holdID); err != nil {
		t.Fatal(err)
	}
	if err := d.Admin.QueryRow(ctx, "SELECT max(seq) FROM credit_ledger WHERE org_id = $1", org).Scan(&ledgerSeq); err != nil {
		t.Fatal(err)
	}

	tenant := &org
	hot := []hotQuery{
		{name: "ListCreditLedger", org: tenant, args: []any{org, int32(50)}},
		{name: "ListCreditLedgerBefore", org: tenant, args: []any{org, ledgerSeq / 2, int32(50)}},
		{name: "GetCreditAccountForUpdate", org: tenant, args: []any{org}},
		{name: "GetCreditAccount", org: tenant, args: []any{org}},
		{name: "GetCreditHoldForUpdate", org: tenant, args: []any{org, holdID}},
		{name: "ListOpenCreditGrantsForUpdate", org: tenant, args: []any{org}},
		{name: "GetSession", args: []any{sessionID}},
		{name: "GetSessionByRefreshHash", args: []any{refresh}},
		{name: "GetAPIKey", args: []any{keyID}},
		{name: "GetStripeEvent", args: []any{s.stripeEventID}},
		{name: "ListRecentRuns", org: tenant, args: []any{org, int32(20)}},
		{name: "ListRecentRunsBefore", org: tenant, args: []any{org, time.Now(), int32(20)}},
		{name: "CountActiveRuns", org: tenant, args: []any{org}},
		{name: "NextQueuedRun"},
		{name: "GetRun", org: tenant, args: []any{org, runID}},
		{name: "ListFindingsByRun", org: tenant, args: []any{org, runID, int32(50), int32(0)}},
		{name: "CountFindingsByRun", org: tenant, args: []any{org, runID}},
		{name: "SearchRules", org: tenant, args: []any{org, "iof retido", int32(10)}},
		{name: "ListRecentlyUsedRules", org: tenant, args: []any{org, int32(20)}},
		{name: "GetRule", org: tenant, args: []any{org, uuid.New()}},
		{name: "ClaimOutbox", args: []any{[]string{"email"}, int32(10)}},
		{name: "CountUnsentOutbox"},
		{name: "GetAuditChainTail", org: tenant, args: []any{org}},
		{name: "ListAuditRange", org: tenant, args: []any{org, int64(10), int64(60)}},
		{name: "ListAuditByOrg", org: tenant, args: []any{org, int32(50)}},
		{name: "GetOrg", org: tenant, args: []any{org}},
		{name: "GetUserByEmail", args: []any{s.userEmail}},
		{name: "ListAPIKeys", org: tenant, args: []any{org}},
		{name: "CountRecentUploads", org: tenant, args: []any{org, time.Now().Add(-time.Hour)}},
	}
	for _, q := range hot {
		t.Run(q.name, func(t *testing.T) {
			sql, ok := queries[q.name]
			if !ok {
				t.Fatalf("consulta %s não encontrada em db/queries", q.name)
			}
			plan := explain(t, d, q, sql)
			var offenders []string
			for _, rel := range seqScans(plan) {
				if pages := relPages(t, d, rel); pages > minPages {
					offenders = append(offenders, fmt.Sprintf("%s (%d páginas)", rel, pages))
				}
			}
			if len(offenders) > 0 {
				pretty, _ := json.MarshalIndent(plan, "", "  ")
				t.Fatalf("%s faz Seq Scan em %v; plano:\n%s", q.name, offenders, pretty)
			}
		})
	}
}

// TestExplainBudgetDetectsMissingIndex drops a hot index for real and checks
// the budget flags the resulting sequential scan, so a green run means the
// check is alive and not merely lenient.
func TestExplainBudgetDetectsMissingIndex(t *testing.T) {
	if testing.Short() {
		t.Skip("orçamento de EXPLAIN pulado com -short")
	}
	d := testdb.Get(t)
	ctx := context.Background()
	queries := loadQueries(t)
	org := seed(t, d).orgs[0]
	if _, err := d.Admin.Exec(ctx, "DROP INDEX runs_org_recent_idx"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := d.Admin.Exec(context.Background(), "CREATE INDEX IF NOT EXISTS runs_org_recent_idx ON runs (org_id, created_at DESC)"); err != nil {
			t.Errorf("recriar índice: %v", err)
		}
	})
	plan := explain(t, d, hotQuery{name: "ListRecentRuns", org: &org, args: []any{org, int32(20)}}, queries["ListRecentRuns"])
	flagged := false
	for _, rel := range seqScans(plan) {
		if relPages(t, d, rel) > minPages {
			flagged = true
		}
	}
	if !flagged {
		pretty, _ := json.MarshalIndent(plan, "", "  ")
		t.Fatalf("sem o índice, ListRecentRuns deveria ser pego pelo orçamento; plano:\n%s", pretty)
	}
}

// relPages returns the on-disk size of a relation in pages (after ANALYZE).
func relPages(t *testing.T, d *testdb.DB, rel string) int {
	t.Helper()
	var pages int
	if err := d.Admin.QueryRow(context.Background(), "SELECT relpages FROM pg_class WHERE relname = $1", rel).Scan(&pages); err != nil {
		t.Fatalf("relpages de %s: %v", rel, err)
	}
	return pages
}

// loadQueries parses db/queries/*.sql into name → SQL, rewriting sqlc macros
// into positional parameters so the real query text is what gets explained.
func loadQueries(t *testing.T) map[string]string {
	t.Helper()
	dir := filepath.Join("..", "..", "..", "..", "db", "queries")
	files, err := filepath.Glob(filepath.Join(dir, "*.sql"))
	if err != nil || len(files) == 0 {
		t.Fatalf("db/queries indisponível: %v", err)
	}
	nameRe := regexp.MustCompile(`(?m)^-- name: (\w+) :\w+\s*$`)
	out := map[string]string{}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		text := string(data)
		locs := nameRe.FindAllStringSubmatchIndex(text, -1)
		for i, loc := range locs {
			end := len(text)
			if i+1 < len(locs) {
				end = locs[i+1][0]
			}
			name := text[loc[2]:loc[3]]
			body := text[loc[1]:end]
			out[name] = rewriteSQLC(body)
		}
	}
	return out
}

// rewriteSQLC replaces sqlc.arg/narg/embed macros. Named args are numbered
// after the highest positional parameter already present, in order of first
// appearance, which matches how sqlc assigns them.
func rewriteSQLC(sql string) string {
	// Drop comment lines.
	var lines []string
	for _, l := range strings.Split(sql, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "--") {
			continue
		}
		lines = append(lines, l)
	}
	sql = strings.Join(lines, "\n")
	sql = regexp.MustCompile(`sqlc\.embed\((\w+)\)`).ReplaceAllString(sql, "$1.*")
	posRe := regexp.MustCompile(`\$(\d+)`)
	maxPos := 0
	for _, m := range posRe.FindAllStringSubmatch(sql, -1) {
		var n int
		_, _ = fmt.Sscanf(m[1], "%d", &n)
		maxPos = max(maxPos, n)
	}
	argRe := regexp.MustCompile(`sqlc\.n?arg\(['"]?(\w+)['"]?\)`)
	names := map[string]int{}
	sql = argRe.ReplaceAllStringFunc(sql, func(m string) string {
		name := argRe.FindStringSubmatch(m)[1]
		if _, ok := names[name]; !ok {
			maxPos++
			names[name] = maxPos
		}
		return fmt.Sprintf("$%d", names[name])
	})
	return strings.TrimSpace(sql)
}

func explain(t *testing.T, d *testdb.DB, q hotQuery, sql string) map[string]any {
	t.Helper()
	ctx := context.Background()
	var raw []byte
	run := func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, "EXPLAIN (FORMAT JSON) "+sql, q.args...)
		if err != nil {
			return fmt.Errorf("explain %s: %w", q.name, err)
		}
		defer rows.Close()
		if !rows.Next() {
			return fmt.Errorf("explain %s: sem plano", q.name)
		}
		if err := rows.Scan(&raw); err != nil {
			return err
		}
		rows.Close()
		// Never commit side effects of UPDATE ... RETURNING explains.
		return errRollbackExplain
	}
	var err error
	if q.org == nil {
		err = db.WithSystemTx(ctx, d.App, run)
	} else {
		err = db.WithOrgTx(ctx, d.App, *q.org, run)
	}
	if err != nil && !errors.Is(err, errRollbackExplain) {
		t.Fatal(err)
	}
	var plans []map[string]any
	if err := json.Unmarshal(raw, &plans); err != nil || len(plans) == 0 {
		t.Fatalf("plano inválido: %s (%v)", raw, err)
	}
	return plans[0]
}

// seqScans walks the plan tree and returns the watched relations hit by a
// sequential scan.
func seqScans(plan map[string]any) []string {
	var out []string
	var walk func(node map[string]any)
	walk = func(node map[string]any) {
		if node["Node Type"] == "Seq Scan" {
			rel, _ := node["Relation Name"].(string)
			for _, w := range watched {
				if rel == w || strings.HasPrefix(rel, w+"_") {
					out = append(out, rel)
				}
			}
		}
		if children, ok := node["Plans"].([]any); ok {
			for _, c := range children {
				if m, ok := c.(map[string]any); ok {
					walk(m)
				}
			}
		}
	}
	if root, ok := plan["Plan"].(map[string]any); ok {
		walk(root)
	}
	return out
}

// seeded carries the handles a test needs after seeding. Every unique value
// is suffixed with a random tag so several tests can seed the same database.
type seeded struct {
	orgs          []uuid.UUID
	userID        uuid.UUID
	userEmail     string
	stripeEventID string
}

// seed fills the tables through the owner connection (RLS bypassed) using
// generate_series, spreading rows over the current and two previous months.
func seed(t *testing.T, d *testdb.DB) seeded {
	t.Helper()
	ctx := context.Background()
	start := time.Now()
	if _, err := db.EnsureMonthPartitions(ctx, d.Admin, 1, 2); err != nil {
		t.Fatal(err)
	}
	tag := uuid.NewString()[:8]
	orgs := make([]uuid.UUID, seedOrgs)
	for i := range orgs {
		orgs[i] = uuid.New()
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := d.Admin.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("seed: %v\n%s", err, sql)
		}
	}
	exec(`INSERT INTO orgs (id, name, slug) SELECT o, 'Org ' || o::text, 'org-' || o::text FROM unnest($1::uuid[]) AS o`, orgs)
	exec(`INSERT INTO users (id, email) SELECT uuidv7(), 'user-' || g || '-' || $1 || '@example.test' FROM generate_series(1, 2000) g`, tag)
	var userID uuid.UUID
	userEmail := "user-1-" + tag + "@example.test"
	if err := d.Admin.QueryRow(ctx, "SELECT id FROM users WHERE email = $1", userEmail).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO credit_accounts (org_id, balance, held, ledger_seq) SELECT o, 1000000, 1000, $2 FROM unnest($1::uuid[]) AS o`, orgs, seedLedgerRows/seedOrgs+1)
	exec(`INSERT INTO credit_grants (org_id, kind, amount, remaining, expires_at)
	      SELECT o, 'plan', 100000, 50000, now() + interval '30 days' FROM unnest($1::uuid[]) AS o, generate_series(1, 20)`, orgs)
	exec(`INSERT INTO credit_holds (org_id, amount, status)
	      SELECT o[1 + (g % $2)], 10, CASE WHEN g % 10 = 0 THEN 'held' ELSE 'settled' END
	      FROM (SELECT $1::uuid[] AS o) s, generate_series(1, $3) g`, orgs, seedOrgs, seedHolds)
	exec(`INSERT INTO credit_ledger (org_id, seq, kind, amount, held_delta, balance_after, held_after, created_at)
	      SELECT o[1 + (g % $2)], g / $2 + 1, 'settle', -10, -10, 1000000 - g, 1000,
	             now() - (g % 80) * interval '1 day'
	      FROM (SELECT $1::uuid[] AS o) s, generate_series(1, $3) g`, orgs, seedOrgs, seedLedgerRows)
	exec(`INSERT INTO sessions (user_id, device_hash, refresh_hash, family_id, expires_at)
	      SELECT $2, sha256(g::text::bytea), sha256(('r' || g || $3)::text::bytea), uuidv7(), now() + interval '30 days'
	      FROM generate_series(1, $1) g`, seedSessions, userID, tag)
	exec(`INSERT INTO api_keys (org_id, label, prefix, key_hash)
	      SELECT o[1 + (g % $2)], 'chave ' || g, 'dm_' || g, sha256(('k' || g || $4)::text::bytea)
	      FROM (SELECT $1::uuid[] AS o) s, generate_series(1, $3) g`, orgs, seedOrgs, seedAPIKeys, tag)
	exec(`INSERT INTO stripe_events (id, type, payload) SELECT 'evt_' || $2 || '_' || g, 'invoice.paid', '{}'::jsonb FROM generate_series(1, $1) g`, seedStripe, tag)
	exec(`INSERT INTO runs (id, org_id, lane, status, created_at)
	      SELECT uuidv7(), o[1 + (g % $2)], 'app', CASE WHEN g % 50 = 0 THEN 'queued' WHEN g % 51 = 0 THEN 'running' ELSE 'completed' END,
	             now() - (g % 80) * interval '1 day'
	      FROM (SELECT $1::uuid[] AS o) s, generate_series(1, $3) g`, orgs, seedOrgs, seedRuns)
	exec(`INSERT INTO findings (org_id, run_id, kind, amount_at_risk, title, created_at)
	      SELECT r.org_id, r.id, 'divergencia', (g * 37) % 100000, 'achado ' || g, r.created_at
	      FROM (SELECT id, org_id, created_at, row_number() OVER () AS n FROM runs) r
	      JOIN generate_series(1, $1) g ON r.n = 1 + (g % (SELECT count(*) FROM runs))`, seedFindings)
	exec(`INSERT INTO rules (org_id, title, body, last_used_at)
	      SELECT o[1 + (g % $2)], 'regra ' || g || ' ' || (ARRAY['iof retido', 'tarifa bancária', 'juros', 'desconto', 'estorno'])[1 + g % 5],
	             'corpo', now() - (g % 365) * interval '1 day'
	      FROM (SELECT $1::uuid[] AS o) s, generate_series(1, $3) g`, orgs, seedOrgs, seedRules)
	exec(`INSERT INTO outbox (org_id, kind, payload, sent_at)
	      SELECT o[1 + (g % $2)], 'webhook', '{}'::jsonb, CASE WHEN g % 2 = 0 THEN now() ELSE NULL END
	      FROM (SELECT $1::uuid[] AS o) s, generate_series(1, $3) g`, orgs, seedOrgs, seedOutbox)
	exec(`INSERT INTO audit_log (org_id, seq, actor_type, actor_id, action, target_type, hash, created_at)
	      SELECT o[1 + (g % $2)], g / $2 + 1, 'system', 'seed', 'seed', 'seed', sha256(g::text::bytea), now() - (g % 80) * interval '1 day'
	      FROM (SELECT $1::uuid[] AS o) s, generate_series(1, $3) g`, orgs, seedOrgs, seedAudit)
	exec(`ANALYZE`)
	t.Logf("seed de %d linhas de razão (e demais tabelas) em %v", seedLedgerRows, time.Since(start).Round(time.Millisecond))
	return seeded{orgs: orgs, userID: userID, userEmail: userEmail, stripeEventID: "evt_" + tag + "_1"}
}
