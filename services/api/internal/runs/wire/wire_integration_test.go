//go:build integration

package wire_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/mmtmn/docmatch/services/api/internal/config"
	"github.com/mmtmn/docmatch/services/api/internal/credits"
	"github.com/mmtmn/docmatch/services/api/internal/db"
	"github.com/mmtmn/docmatch/services/api/internal/estimate"
	"github.com/mmtmn/docmatch/services/api/internal/harness/harnesstest"
	"github.com/mmtmn/docmatch/services/api/internal/httpx"
	"github.com/mmtmn/docmatch/services/api/internal/jobs/jobargs"
	"github.com/mmtmn/docmatch/services/api/internal/limits"
	"github.com/mmtmn/docmatch/services/api/internal/logx"
	"github.com/mmtmn/docmatch/services/api/internal/rules"
	"github.com/mmtmn/docmatch/services/api/internal/runs"
	"github.com/mmtmn/docmatch/services/api/internal/runs/apix"
	"github.com/mmtmn/docmatch/services/api/internal/runs/wire"
	"github.com/mmtmn/docmatch/services/api/internal/testdb"
	"github.com/mmtmn/docmatch/services/api/internal/uploads"
	"github.com/mmtmn/docmatch/services/api/internal/workflows"
)

func seedOrg(t *testing.T, d *testdb.DB, name string) uuid.UUID {
	t.Helper()
	org := uuid.New()
	if err := db.WithOrgTx(context.Background(), d.App, org, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "INSERT INTO orgs (id, name, slug, plan_code) VALUES ($1, $2, $3, 'pro')", org, name, "org-"+org.String())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return org
}

func seedUser(t *testing.T, d *testdb.DB, org uuid.UUID) uuid.UUID {
	t.Helper()
	user := uuid.New()
	ctx := context.Background()
	if _, err := d.Admin.Exec(ctx, "INSERT INTO users (id, email) VALUES ($1, $2)", user, "u-"+user.String()[:8]+"@example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Admin.Exec(ctx, "INSERT INTO memberships (org_id, user_id, role) VALUES ($1, $2, 'owner')", org, user); err != nil {
		t.Fatal(err)
	}
	return user
}

// TestPipelineOnPostgres drives the real wiring (Postgres store, local storage,
// pricing, the harness client over a Unix socket) from clean uploads to a
// settled run with findings, feedback, rules, workflows, the outbox and tenant
// isolation through the HTTP routes.
func TestPipelineOnPostgres(t *testing.T) {
	d := testdb.Get(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	sock, cleanup, err := harnesstest.SocketPath()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	stop, err := harnesstest.Serve(sock, harnesstest.NewServer(harnesstest.MustLoadGolden(), harnesstest.Options{}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)

	env := map[string]string{
		config.VarEnv:               "dev",
		config.VarPublicBaseURL:     "http://localhost:4321",
		config.VarAPIBaseURL:        "http://localhost:8080",
		config.VarAPIListen:         "127.0.0.1:18080",
		config.VarAdminListen:       "127.0.0.1:18443",
		config.VarDatabaseURL:       d.URL,
		config.VarSessionSecret:     strings.Repeat("s", 48),
		config.VarQuoteHMACKey:      strings.Repeat("q", 32),
		config.VarAPIKeyPepper:      strings.Repeat("p", 32),
		config.VarWebhookSigningKey: strings.Repeat("w", 32),
		config.VarPricingFile:       "config/pricing.yaml",
		config.VarHarnessSocket:     sock,
		config.VarMediaLocalDir:     t.TempDir(),
		config.VarLLMMode:           "off",
	}
	cfg, err := config.LoadFrom(func(k string) (string, bool) { v, ok := env[k]; return v, ok })
	if err != nil {
		t.Fatal(err)
	}

	org := seedOrg(t, d, "Principal")
	other := seedOrg(t, d, "Outra")
	user := seedUser(t, d, org)
	me := apix.Principal{OrgID: org, UserID: &user, Plan: "pro"}
	resolver := func(r *http.Request) (apix.Principal, error) {
		if r.Header.Get("X-Test-Org") == other.String() {
			return apix.Principal{OrgID: other, Plan: "pro"}, nil
		}
		return me, nil
	}
	sys, err := wire.New(ctx, wire.Options{Config: cfg, Logger: logx.Discard(), Pool: d.App, Auth: resolver})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sys.Close)
	queue := &jobargs.Recorder{}
	sys.Jobs.Set(queue)

	if _, err := sys.Credits.Grant(ctx, org, credits.GrantParams{Kind: credits.GrantPlan, Amount: 5000, Source: "test"}); err != nil {
		t.Fatal(err)
	}

	// Clean uploads straight in the store: the presigned upload dance has its
	// own tests; here the point is every query of the pipeline on Postgres.
	roles := []string{"protheus", "omie", "bank", "ap_inbox"}
	refs := make([]estimate.SourceRef, 0, len(roles))
	for _, role := range roles {
		id, _ := uuid.NewV7()
		u, err := sys.Store.CreateUpload(ctx, uploads.Upload{
			ID: id, OrgID: org, UserID: &user, Kind: uploads.KindDocument, Store: "local", Role: role,
			FileName: role + ".csv", DeclaredMime: "text/csv", DetectedMime: "text/csv", SizeBytes: 1000,
			SHA256: bytes.Repeat([]byte{1}, 32), StorageKey: "quarantine/" + role, Status: uploads.StatusClean, CreatedAt: time.Now().UTC(),
		})
		if err != nil {
			t.Fatal(err)
		}
		// New uploads always start pending; the scan verdict cleans them.
		u.Status, u.DetectedMime = uploads.StatusClean, "text/csv"
		if u, err = sys.Store.UpdateUpload(ctx, u); err != nil {
			t.Fatal(err)
		}
		if got, ok, err := sys.Store.GetUpload(ctx, org, u.ID); err != nil || !ok || got.Status != uploads.StatusClean {
			t.Fatalf("GetUpload: %+v %v %v", got, ok, err)
		}
		if _, ok, _ := sys.Store.GetUpload(ctx, other, u.ID); ok {
			t.Fatal("upload visível para outra organização")
		}
		refs = append(refs, estimate.SourceRef{UploadID: u.PublicID(), Role: role})
	}

	req := estimate.Request{Sources: refs}
	q, err := sys.Estimates.Estimate(ctx, me, req, func(u uploads.Upload) string { return "local://t/" + u.StorageKey })
	if err != nil || q.CapCredits <= 0 || q.Signature == "" {
		t.Fatalf("Estimate: %+v %v", q, err)
	}
	run, err := sys.Runs.Create(ctx, me, runs.CreateRequest{Request: req, QuoteID: q.ID, QuoteSignature: q.Signature})
	if err != nil || run.Status != runs.StatusQueued || run.CreditsHeld <= 0 {
		t.Fatalf("Create: %+v %v", run, err)
	}
	if kinds := queue.Kinds(); len(kinds) == 0 || kinds[len(kinds)-1] != jobargs.KindRun {
		t.Fatalf("job de execução não enfileirado: %v", kinds)
	}

	// What the worker does with the job.
	if err := sys.Runs.Execute(ctx, org, run.ID, false); err != nil {
		t.Fatal(err)
	}
	done, err := sys.Runs.Get(ctx, org, run.ID)
	if err != nil || done.Status != runs.StatusCompleted || done.Summary == nil || done.Summary.FindingsTotal != 23 {
		t.Fatalf("run = %+v %v", done, err)
	}
	findings, err := sys.Store.ListAllFindings(ctx, org, run.ID)
	if err != nil || len(findings) != 23 {
		t.Fatalf("achados: %d %v", len(findings), err)
	}
	if page, err := sys.Runs.Findings(ctx, org, run.ID, runs.FindingFilter{}, apix.Page{Limit: 5}); err != nil || len(page) != 5 {
		t.Fatalf("página de achados: %d %v", len(page), err)
	}
	if m, err := sys.Store.ListAllMatches(ctx, org, run.ID); err != nil || len(m) == 0 {
		t.Fatalf("casamentos: %d %v", len(m), err)
	}
	events, err := sys.Runs.EventsAfter(ctx, org, run.ID, 0, 1000)
	if err != nil || len(events) == 0 {
		t.Fatalf("eventos: %d %v", len(events), err)
	}
	acct, err := sys.Credits.Balance(ctx, org)
	if err != nil || acct.Held != 0 || acct.Balance >= 5000 {
		t.Fatalf("tokens não liquidados: %+v %v", acct, err)
	}

	// Feedback (idempotent), then learning proposes rules through the harness.
	if _, err := sys.Runs.GiveFeedback(ctx, me, run.ID, findings[0].ID, runs.FeedbackRequest{Status: "confirmed", Note: "confere"}); err != nil {
		t.Fatal(err)
	}
	if _, err := sys.Runs.GiveFeedback(ctx, me, run.ID, findings[0].ID, runs.FeedbackRequest{Status: "confirmed", Note: "confere"}); err != nil {
		t.Fatal(err)
	}
	if err := sys.Runs.Learn(ctx, org, run.ID); err != nil {
		t.Fatal(err)
	}
	exp, err := sys.Runs.Export(ctx, org, run.ID, "csv")
	if err != nil {
		t.Fatalf("exportação: %v", err)
	}
	var csv bytes.Buffer
	if err := exp.Write(&csv); err != nil || csv.Len() == 0 {
		t.Fatalf("exportação vazia: %v", err)
	}

	// Rules and workflows on Postgres (search uses pg_trgm).
	rule, err := sys.Rules.Create(ctx, me, rules.CreateRequest{Title: "Tarifa de cobrança", Body: "Tarifas até R$ 30 fazem parte do pagamento"})
	if err != nil {
		t.Fatal(err)
	}
	if found, err := sys.Rules.Search(ctx, org, rules.Query{Term: "tarifa", Limit: 10}); err != nil || len(found) == 0 {
		t.Fatalf("busca de regras: %d %v", len(found), err)
	}
	if _, err := sys.Rules.NewVersion(ctx, me, rule.ID, rules.VersionRequest{Body: "Tarifas até R$ 35 fazem parte do pagamento"}); err != nil {
		t.Fatal(err)
	}
	wf, err := sys.Workflows.Create(ctx, me, workflows.Config{
		Name:     "Conciliação bancária",
		Sources:  []workflows.SourceRole{{Role: "bank_statement", Kind: "table", Label: "Extrato"}, {Role: "ledger", Kind: "any", Accept: []string{"text/csv"}}},
		Matching: &workflows.Matching{Keys: []string{"amount", "date"}, Strategies: []string{"exact", "sum"}},
		Budget:   &workflows.Budget{Lane: "app"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if list, err := sys.Workflows.List(ctx, org, apix.Page{Limit: 10}); err != nil || len(list) != 1 || list[0].ID != wf.ID {
		t.Fatalf("fluxos: %v %v", list, err)
	}
	if list, _ := sys.Workflows.List(ctx, other, apix.Page{Limit: 10}); len(list) != 0 {
		t.Fatal("fluxo visível para outra organização")
	}

	// Tenant isolation through the real routes, RLS underneath.
	router := httpx.NewRouter(limits.Must())
	for _, mount := range sys.Mount() {
		mount(router)
	}
	get := func(org string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/v1/runs/"+run.PublicID(), nil)
		r.Header.Set("X-Test-Org", org)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	if w := get(org.String()); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"completed"`) {
		t.Fatalf("GET próprio: %d %s", w.Code, w.Body.String())
	}
	if w := get(other.String()); w.Code != http.StatusNotFound {
		t.Fatalf("GET de outra organização: %d %s", w.Code, w.Body.String())
	}
	if _, ok, _ := sys.Store.GetRun(ctx, other, run.ID); ok {
		t.Fatal("execução visível para outra organização")
	}

	// Outbox: the run.completed webhook is queued; the generic drain leaves
	// e-mail to its own worker.
	if err := sys.Store.Enqueue(ctx, &org, "email", map[string]string{"to": "x@example.com"}); err != nil {
		t.Fatal(err)
	}
	msgs, err := sys.Store.ClaimOutbox(ctx, 100, jobargs.DedicatedOutboxKinds)
	if err != nil {
		t.Fatal(err)
	}
	sawCompleted := false
	for _, m := range msgs {
		if m.Kind == "email" {
			t.Fatal("o dreno genérico reivindicou um e-mail")
		}
		sawCompleted = sawCompleted || m.Kind == "run.completed"
		if err := sys.Store.MarkOutboxSent(ctx, m.ID); err != nil {
			t.Fatal(err)
		}
	}
	if !sawCompleted {
		t.Fatalf("run.completed não está no outbox: %v", msgs)
	}

	// Timing: the quote carries the expected duration (throughput.yaml next
	// to pricing.yaml), the finished run is in run_timings, and a small,
	// quick run sends no e-mail.
	if q.ExpectedSeconds <= 0 || q.Large {
		t.Fatalf("orçamento sem tempo esperado: %+v", q)
	}
	var status string
	var active float64
	if err := d.Admin.QueryRow(ctx, "SELECT status, active_seconds FROM run_timings WHERE run_id = $1", run.ID).Scan(&status, &active); err != nil || status != "completed" || active < 0 {
		t.Fatalf("run_timings: %q %v %v", status, active, err)
	}
	samples, err := sys.Store.RecentTimings(ctx, time.Now().Add(-time.Hour), 10)
	if err != nil || len(samples) == 0 || samples[0].Units["table"] <= 0 {
		t.Fatalf("histórico: %+v %v", samples, err)
	}
	if _, err := sys.Store.RunsInFlight(ctx, time.Now().Add(-time.Hour)); err != nil {
		t.Fatalf("execuções em voo: %v", err)
	}
	var mails int
	if err := d.Admin.QueryRow(ctx, "SELECT count(*) FROM outbox WHERE dedupe_key = $1", runs.MailKeyPrefix+run.PublicID()).Scan(&mails); err != nil || mails != 0 {
		t.Fatalf("pedido pequeno não manda e-mail: %d %v", mails, err)
	}
}

// TestLargeRunMailOnPostgres: with a throughput.yaml where every order is
// large, a finished run leaves exactly one e-mail in the outbox for the person
// who started it, written in the transaction that finished the run.
func TestLargeRunMailOnPostgres(t *testing.T) {
	d := testdb.Get(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	sock, cleanup, err := harnesstest.SocketPath()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	stop, err := harnesstest.Serve(sock, harnesstest.NewServer(harnesstest.MustLoadGolden(), harnesstest.Options{}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	repo, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "..", "config", "throughput.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	tpFile := filepath.Join(t.TempDir(), "throughput.yaml")
	if err := os.WriteFile(tpFile, bytes.Replace(repo, []byte("large_after_seconds: 600"), []byte("large_after_seconds: 1"), 1), 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{
		config.VarEnv: "dev", config.VarPublicBaseURL: "http://localhost:4321", config.VarAPIBaseURL: "http://localhost:8080",
		config.VarAPIListen: "127.0.0.1:18081", config.VarAdminListen: "127.0.0.1:18444", config.VarDatabaseURL: d.URL,
		config.VarSessionSecret: strings.Repeat("s", 48), config.VarQuoteHMACKey: strings.Repeat("q", 32), config.VarAPIKeyPepper: strings.Repeat("p", 32),
		config.VarWebhookSigningKey: strings.Repeat("w", 32), config.VarPricingFile: "config/pricing.yaml", config.VarThroughputFile: tpFile,
		config.VarHarnessSocket: sock, config.VarMediaLocalDir: t.TempDir(), config.VarLLMMode: "off",
	}
	cfg, err := config.LoadFrom(func(k string) (string, bool) { v, ok := env[k]; return v, ok })
	if err != nil {
		t.Fatal(err)
	}
	org := seedOrg(t, d, "Grande")
	user := seedUser(t, d, org)
	me := apix.Principal{OrgID: org, UserID: &user, Plan: "pro"}
	sys, err := wire.New(ctx, wire.Options{Config: cfg, Logger: logx.Discard(), Pool: d.App, Auth: func(*http.Request) (apix.Principal, error) { return me, nil }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sys.Close)
	sys.Jobs.Set(&jobargs.Recorder{})
	if sys.Throughput == nil || sys.Throughput.LargeAfterSeconds != 1 {
		t.Fatalf("DM_THROUGHPUT_FILE não foi lido: %+v", sys.Throughput)
	}
	if _, err := sys.Credits.Grant(ctx, org, credits.GrantParams{Kind: credits.GrantPlan, Amount: 5000, Source: "test"}); err != nil {
		t.Fatal(err)
	}
	id, _ := uuid.NewV7()
	u, err := sys.Store.CreateUpload(ctx, uploads.Upload{
		ID: id, OrgID: org, UserID: &user, Kind: uploads.KindDocument, Store: "local", Role: "bank", FileName: "extrato.csv",
		DeclaredMime: "text/csv", SizeBytes: 1000, SHA256: bytes.Repeat([]byte{2}, 32), StorageKey: "quarantine/bank", Status: uploads.StatusClean, CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	u.Status, u.DetectedMime = uploads.StatusClean, "text/csv"
	if _, err := sys.Store.UpdateUpload(ctx, u); err != nil {
		t.Fatal(err)
	}
	req := estimate.Request{Sources: []estimate.SourceRef{{UploadID: u.PublicID(), Role: "bank"}}}
	q, err := sys.Estimates.Estimate(ctx, me, req, func(u uploads.Upload) string { return "local://t/" + u.StorageKey })
	if err != nil || !q.Large || q.ExpectedSeconds <= 1 {
		t.Fatalf("orçamento grande esperado: %+v %v", q, err)
	}
	before, err := sys.Store.RunsInFlight(ctx, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	run, err := sys.Runs.Create(ctx, me, runs.CreateRequest{Request: req, QuoteID: q.ID, QuoteSignature: q.Signature})
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := sys.Store.RunsInFlight(ctx, time.Now().Add(-time.Hour)); n != before+1 {
		t.Fatalf("a execução criada deveria contar na fila: %d (antes %d)", n, before)
	}
	if n, _ := sys.Store.RunsInFlight(ctx, time.Now().Add(time.Minute)); n != 0 {
		t.Fatalf("execuções sem toque desde o corte não contam na fila: %d", n)
	}
	stored, _ := sys.Runs.Get(ctx, org, run.ID)
	if tm := stored.Timing(); !tm.Large || tm.ExpectedSeconds != q.ExpectedSeconds || tm.Units["table"] <= 0 {
		t.Fatalf("timing gravado com a execução: %+v", tm)
	}
	for i := 0; i < 2; i++ {
		if err := sys.Runs.Execute(ctx, org, run.ID, false); err != nil {
			t.Fatal(err)
		}
	}
	var to, text, subject string
	var mails int
	key := runs.MailKeyPrefix + run.PublicID()
	if err := d.Admin.QueryRow(ctx, "SELECT count(*), max(payload->>'to'), max(payload->>'text'), max(payload->>'subject') FROM outbox WHERE dedupe_key = $1 AND kind = 'email' AND org_id = $2", key, org).Scan(&mails, &to, &text, &subject); err != nil {
		t.Fatal(err)
	}
	if mails != 1 || !strings.HasPrefix(to, "u-") || subject != "Sua conciliação terminou" || !strings.Contains(text, "http://localhost:4321/resultados?run="+run.PublicID()) {
		t.Fatalf("e-mail: %d para %q: %q\n%s", mails, to, subject, text)
	}
	if strings.Contains(text, "extrato") || strings.Contains(text, "bank") {
		t.Fatalf("o e-mail cita o documento: %s", text)
	}
}
