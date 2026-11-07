//go:build integration

package projects

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	contracts "github.com/mmtmn/docmatch/packages/contracts/gen"
	"github.com/mmtmn/docmatch/services/api/internal/config"
	"github.com/mmtmn/docmatch/services/api/internal/credits"
	"github.com/mmtmn/docmatch/services/api/internal/db"
	ledgerstore "github.com/mmtmn/docmatch/services/api/internal/db/pgstore"
	"github.com/mmtmn/docmatch/services/api/internal/email/emailpg"
	"github.com/mmtmn/docmatch/services/api/internal/estimate"
	"github.com/mmtmn/docmatch/services/api/internal/estimate/pricing"
	"github.com/mmtmn/docmatch/services/api/internal/harness"
	"github.com/mmtmn/docmatch/services/api/internal/harness/harnesstest"
	"github.com/mmtmn/docmatch/services/api/internal/jobs/jobargs"
	"github.com/mmtmn/docmatch/services/api/internal/logx"
	"github.com/mmtmn/docmatch/services/api/internal/publicid"
	"github.com/mmtmn/docmatch/services/api/internal/rules"
	"github.com/mmtmn/docmatch/services/api/internal/runs"
	"github.com/mmtmn/docmatch/services/api/internal/runs/apix"
	"github.com/mmtmn/docmatch/services/api/internal/runs/pgstore"
	"github.com/mmtmn/docmatch/services/api/internal/storage"
	"github.com/mmtmn/docmatch/services/api/internal/testdb"
	"github.com/mmtmn/docmatch/services/api/internal/uploads"
)

type pipelineFixture struct {
	s       *Service
	jobs    *jobargs.LazyEnqueuer
	queue   jobargs.RiverEnqueuer
	credits *credits.Service
}

func pipeline(t *testing.T, options ...harnesstest.Options) pipelineFixture {
	t.Helper()
	d := testdb.Get(t)
	pr, err := pricing.Load("../../../../config/pricing.yaml", pricing.Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	opts := harnesstest.Options{}
	if len(options) > 0 {
		opts = options[0]
	}
	fake := harnesstest.NewServer(harnesstest.MustLoadGolden(), opts)
	sock, cleanup, err := harnesstest.SocketPath()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	stop, err := harnesstest.Serve(sock, fake)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	client, err := harness.Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	base, _ := url.Parse("http://localhost:8080")
	local, err := storage.NewLocal(t.TempDir(), "projects", base)
	if err != nil {
		t.Fatal(err)
	}
	store := pgstore.New(d.App)
	jobs := &jobargs.LazyEnqueuer{}
	up, err := uploads.New(uploads.Deps{Store: store, Stores: storage.Stores{Documents: local, Media: local}, Pricing: pr, Jobs: jobs, Logger: logx.Discard()})
	if err != nil {
		t.Fatal(err)
	}
	signer, err := estimate.NewSigner("project-integration-test-signing-key")
	if err != nil {
		t.Fatal(err)
	}
	est, err := estimate.New(estimate.Deps{Store: store, Harness: client, Pricing: pr, Signer: signer, Sources: up, Logger: logx.Discard()})
	if err != nil {
		t.Fatal(err)
	}
	rulesSvc, err := rules.New(rules.Deps{Store: store, Logger: logx.Discard()})
	if err != nil {
		t.Fatal(err)
	}
	creditSvc := credits.New(ledgerstore.NewCreditStore(d.App))
	runSvc, err := runs.New(runs.Deps{Store: store, Uploads: up, Estimates: est, Rules: rulesSvc, Credits: creditSvc, Jobs: jobs, Harness: client, Logger: logx.Discard()})
	if err != nil {
		t.Fatal(err)
	}
	inserter, err := emailpg.NewInserter(d.App)
	if err != nil {
		t.Fatal(err)
	}
	queue := jobargs.RiverEnqueuer{Client: inserter}
	jobs.Set(queue)
	s := New(d.App, nil, up)
	s.SetEngine(Engine{Config: &config.Config{Env: "dev"}, Estimates: est, Runs: runSvc, Rules: rulesSvc, Uploads: up})
	return pipelineFixture{s, jobs, queue, creditSvc}
}

func TestAnalysisConcurrencyRollbackAndContentReuse(t *testing.T) {
	f := pipeline(t)
	s := f.s
	d := testdb.Get(t)
	p := seed(t, uuid.Nil, "owner")
	ctx := t.Context()
	if _, err := f.credits.Grant(ctx, p.OrgID, credits.GrantParams{Kind: credits.GrantBonus, Amount: 1000000, Source: "test"}); err != nil {
		t.Fatal(err)
	}
	project, err := s.Create(ctx, p, "Conferência")
	if err != nil {
		t.Fatal(err)
	}
	id := projectID(t, project)
	for _, role := range []string{"protheus", "bank"} {
		up := seededUpload(t, p)
		uid, _ := publicid.Decode("upl", up)
		if _, err := d.Admin.Exec(ctx, `UPDATE uploads SET sha256=$2,store='local',content_type='text/csv',detected_mime='text/csv' WHERE id=$1`, uid, bytes.Repeat([]byte{1}, 32)); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Attach(ctx, p, id, AttachRequest{UploadID: up, Role: role}); err != nil {
			t.Fatal(err)
		}
	}
	detail, err := s.Get(ctx, p, id)
	if err != nil {
		t.Fatal(err)
	}
	quoted, err := s.Analyze(ctx, p, id, AnalysisRequest{Revision: detail.Revision}, false)
	if err != nil || quoted.Quote == nil || quoted.State != "ready" {
		t.Fatal("quote", quoted, err)
	}
	request := AnalysisRequest{Revision: detail.Revision, QuoteID: quoted.Quote.ID, QuoteSignature: quoted.Quote.Signature}
	// A queue failure after the reservation must roll back the hold, ledger and run.
	f.jobs.Set(&jobargs.Recorder{Err: errors.New("queue unavailable")})
	if _, err := s.Analyze(ctx, p, id, request, true); err == nil {
		t.Fatal("queue failure ignored")
	}
	for _, table := range []string{"credit_holds", "runs", "project_runs"} {
		var n int
		if err := d.Admin.QueryRow(ctx, `SELECT count(*) FROM `+table+` WHERE org_id=$1`, p.OrgID).Scan(&n); err != nil || n != 0 {
			t.Fatal("partial commit", table, n, err)
		}
	}
	balance, err := f.credits.Balance(ctx, p.OrgID)
	if err != nil || balance.Held != 0 {
		t.Fatal("orphan hold", balance, err)
	}
	f.jobs.Set(f.queue)
	var wg sync.WaitGroup
	ids := make(chan string, 6)
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := s.Analyze(ctx, p, id, request, true)
			if err != nil {
				t.Error(err)
				return
			}
			if result.Run == nil {
				t.Error("missing run")
				return
			}
			ids <- result.Run.ID
		}()
	}
	wg.Wait()
	close(ids)
	first := ""
	count := 0
	for result := range ids {
		count++
		if first != "" && first != result {
			t.Fatal("duplicate run")
		}
		first = result
	}
	if count != 6 {
		t.Fatal("requests failed", count)
	}
	for _, table := range []string{"credit_holds", "runs", "project_runs"} {
		var n int
		if err := d.Admin.QueryRow(ctx, `SELECT count(*) FROM `+table+` WHERE org_id=$1`, p.OrgID).Scan(&n); err != nil || n != 1 {
			t.Fatal("duplicate", table, n, err)
		}
	}
	var jobs int
	if err := d.Admin.QueryRow(ctx, `SELECT count(*) FROM river_job WHERE kind='run_execute' AND args->>'org_id'=$1`, p.OrgID.String()).Scan(&jobs); err != nil || jobs != 1 {
		t.Fatal("duplicate queue jobs", jobs, err)
	}
	runID, _ := publicid.Decode("run", first)
	if _, err := d.Admin.Exec(ctx, `UPDATE runs SET status='completed' WHERE id=$1`, runID); err != nil {
		t.Fatal(err)
	}
	name := "Nome novo"
	detail, err = s.Update(ctx, p, id, Patch{Version: detail.Version, Name: &name})
	if err != nil {
		t.Fatal(err)
	}
	unchanged, err := s.Analyze(ctx, p, id, AnalysisRequest{Revision: detail.Revision}, false)
	if err != nil || unchanged.State != "unchanged" || unchanged.Run.ID != first || unchanged.Quote != nil {
		t.Fatal("repeat after rename", err)
	}
	// Replacing an upload with identical verified content does not spend again.
	previous := detail.Documents[0]
	newUpload := seededUpload(t, p)
	newID, _ := publicid.Decode("upl", newUpload)
	if _, err := d.Admin.Exec(ctx, `UPDATE uploads SET sha256=$2 WHERE id=$1`, newID, bytes.Repeat([]byte{1}, 32)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Attach(ctx, p, id, AttachRequest{UploadID: newUpload, Role: previous.Role, ReplacesID: &previous.ID}); err != nil {
		t.Fatal(err)
	}
	detail, err = s.Get(ctx, p, id)
	if err != nil {
		t.Fatal(err)
	}
	unchanged, err = s.Analyze(ctx, p, id, AnalysisRequest{Revision: detail.Revision}, true)
	if err != nil || unchanged.State != "unchanged" || unchanged.Run.ID != first {
		t.Fatal("same content new upload charged", err)
	}
	// Old endpoints cannot bypass project checks or manufacture the trusted context.
	legacy := estimate.Request{Sources: []estimate.SourceRef{{UploadID: newUpload}}}
	if err := s.CheckAnalysisAccess(ctx, p, legacy); err == nil {
		t.Fatal("legacy bypass")
	}
	legacy.ProjectID = project.ID
	if err := s.CheckAnalysisAccess(ctx, p, legacy); !errors.Is(err, apix.ErrForbidden) {
		t.Fatal("forged context", err)
	}
	// A changed analysis must reject the earlier quote and take a fresh snapshot.
	settings := Settings{UseRules: true, RulesText: "Comparar somente novembro"}
	detail, err = s.Update(ctx, p, id, Patch{Version: detail.Version, Settings: &settings})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Analyze(ctx, p, id, request, true)
	requireCode(t, err, contracts.ErrConflict)
	request.Revision = detail.Revision
	if _, err := s.Analyze(ctx, p, id, request, true); err == nil {
		t.Fatal("old quote accepted for different rules")
	}
	// The old result keeps its approved options even after project edits.
	stored, err := s.engine.Runs.Get(ctx, p.OrgID, runID)
	if err != nil || stored.Inputs.Project == nil || stored.Inputs.Request.RulesText != "" {
		t.Fatal("mutable run snapshot", err)
	}
}

func TestPrivateResultsAndTransactionAccountBoundary(t *testing.T) {
	f := pipeline(t)
	s := f.s
	d := testdb.Get(t)
	ctx := t.Context()
	p := seed(t, uuid.Nil, "owner")
	reader := seed(t, p.OrgID, "member")
	foreign := seed(t, uuid.Nil, "owner")
	project, err := s.Create(ctx, p, "Privado")
	if err != nil {
		t.Fatal(err)
	}
	id := projectID(t, project)
	run := uuid.New()
	if _, err := d.Admin.Exec(ctx, `INSERT INTO runs(id,org_id,lane) VALUES($1,$2,'app')`, run, p.OrgID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Admin.Exec(ctx, `INSERT INTO project_runs(org_id,project_id,run_id,revision,signature) VALUES($1,$2,$3,1,$4)`, p.OrgID, id, run, string(bytes.Repeat([]byte{'a'}, 64))); err != nil {
		t.Fatal(err)
	}
	resolve := s.Resolve(apix.Static(reader))
	r := httptest.NewRequest(http.MethodGet, "/v1/runs/"+publicid.Encode("run", run), nil)
	r.SetPathValue("id", publicid.Encode("run", run))
	if _, err := resolve(r); err == nil {
		t.Fatal("private result through legacy URL")
	}
	items, err := s.engine.Runs.List(ctx, p.OrgID, runs.ListFilter{VisibleTo: reader.UserID}, apix.Page{Limit: 25})
	if err != nil || len(items) != 0 {
		t.Fatal("private history", err)
	}
	if err := s.Share(ctx, p, id, project.Version, "private", []uuid.UUID{*reader.UserID}); err != nil {
		t.Fatal(err)
	}
	if _, err := resolve(r); err != nil {
		t.Fatal("assigned result", err)
	}
	items, err = s.engine.Runs.List(ctx, p.OrgID, runs.ListFilter{VisibleTo: reader.UserID}, apix.Page{Limit: 25})
	if err != nil || len(items) != 1 {
		t.Fatal("assigned history", err)
	}
	// A composed operation cannot accidentally open a second tenant transaction.
	err = db.WithOrgUnit(ctx, d.App, p.OrgID, func(ctx context.Context, tx pgx.Tx) error {
		return db.WithOrgTx(ctx, d.App, foreign.OrgID, func(context.Context, pgx.Tx) error { return nil })
	})
	if err == nil {
		t.Fatal("transaction switched accounts")
	}
}

func TestProjectRulesStayPrivateAcrossLegacyAndSharedWork(t *testing.T) {
	f := pipeline(t)
	ctx := t.Context()
	owner := seed(t, uuid.Nil, "owner")
	first, err := f.s.Create(ctx, owner, "Privado")
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.s.Create(ctx, owner, "Compartilhado")
	if err != nil {
		t.Fatal(err)
	}
	firstID, secondID := projectID(t, first), projectID(t, second)
	store := pgstore.New(testdb.Get(t).App)
	privateCtx := rules.WithProject(ctx, owner.OrgID, firstID)
	private, err := f.s.engine.Rules.Create(privateCtx, owner, rules.CreateRequest{Title: "Regra privada", Body: "O contrato privado concede desconto."})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PromoteRule(privateCtx, owner.OrgID, private.ID, rules.StatusTrusted); err != nil {
		t.Fatal(err)
	}
	legacy, err := f.s.engine.Rules.Create(ctx, owner, rules.CreateRequest{Title: "Histórico pessoal", Body: "Condição do trabalho anterior."})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PromoteRule(ctx, owner.OrgID, legacy.ID, rules.StatusTrusted); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name     string
		ctx      context.Context
		expected []uuid.UUID
	}{
		{"original project", privateCtx, []uuid.UUID{private.ID}},
		{"another project", rules.WithProject(ctx, owner.OrgID, secondID), nil},
		{"legacy", ctx, []uuid.UUID{legacy.ID}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := f.s.engine.Rules.Resolve(tc.ctx, owner.OrgID, true, nil)
			if err != nil || len(got) != len(tc.expected) {
				t.Fatalf("resolved %v: %v", got, err)
			}
			if len(got) > 0 && got[0].ID != tc.expected[0] {
				t.Fatal("wrong rule scope")
			}
			if tc.name != "original project" {
				if _, err := f.s.engine.Rules.Resolve(tc.ctx, owner.OrgID, false, []uuid.UUID{private.ID}); err == nil {
					t.Fatal("private explicit rule exposed")
				}
				if _, err := f.s.engine.Rules.Get(tc.ctx, owner.OrgID, private.ID); err == nil {
					t.Fatal("private rule detail exposed")
				}
				found, err := store.SearchRules(tc.ctx, owner.OrgID, rules.Query{Term: "privada", Limit: 25})
				if err != nil || len(found) != 0 {
					t.Fatal("private rule search exposed", err)
				}
			}
		})
	}
	// The same context cannot select a project when used with another account.
	foreign := seed(t, uuid.Nil, "owner")
	if rules.ProjectID(privateCtx, foreign.OrgID) != nil {
		t.Fatal("rule context crossed accounts")
	}
}

func preparedAnalysis(t *testing.T, f pipelineFixture) (apix.Principal, uuid.UUID, Detail, AnalysisRequest) {
	t.Helper()
	ctx := t.Context()
	p := seed(t, uuid.Nil, "owner")
	if _, err := f.credits.Grant(ctx, p.OrgID, credits.GrantParams{Kind: credits.GrantBonus, Amount: 1000000, Source: "test"}); err != nil {
		t.Fatal(err)
	}
	project, err := f.s.Create(ctx, p, "Fechamento")
	if err != nil {
		t.Fatal(err)
	}
	id := projectID(t, project)
	for _, role := range []string{"protheus", "bank"} {
		upload := seededUpload(t, p)
		uid, _ := publicid.Decode("upl", upload)
		if _, err := testdb.Get(t).Admin.Exec(ctx, `UPDATE uploads SET sha256=$2,store='local',content_type='text/csv',detected_mime='text/csv' WHERE id=$1`, uid, bytes.Repeat([]byte{2}, 32)); err != nil {
			t.Fatal(err)
		}
		if _, err := f.s.Attach(ctx, p, id, AttachRequest{UploadID: upload, Role: role}); err != nil {
			t.Fatal(err)
		}
	}
	detail, err := f.s.Get(ctx, p, id)
	if err != nil {
		t.Fatal(err)
	}
	quoted, err := f.s.Analyze(ctx, p, id, AnalysisRequest{Revision: detail.Revision}, false)
	if err != nil || quoted.Quote == nil {
		t.Fatal("prepare quote", err)
	}
	return p, id, detail, AnalysisRequest{Revision: detail.Revision, QuoteID: quoted.Quote.ID, QuoteSignature: quoted.Quote.Signature}
}

func TestExpiredResultNeedsReasonAndPaidAccess(t *testing.T) {
	f := pipeline(t)
	ctx := t.Context()
	p, id, detail, request := preparedAnalysis(t, f)
	created, err := f.s.Analyze(ctx, p, id, request, true)
	if err != nil {
		t.Fatal(err)
	}
	runID, _ := publicid.Decode("run", created.Run.ID)
	if _, err := testdb.Get(t).Admin.Exec(ctx, `UPDATE runs SET status='completed' WHERE id=$1`, runID); err != nil {
		t.Fatal(err)
	}
	f.s.engine.Config = &config.Config{Env: "prod"}
	unchanged, err := f.s.Analyze(ctx, p, id, AnalysisRequest{Revision: detail.Revision}, false)
	if err != nil || unchanged.State != "unchanged" {
		t.Fatal("inactive account cannot reuse result", err)
	}
	if _, err := testdb.Get(t).Admin.Exec(ctx, `UPDATE runs SET expires_at=now()-interval '1 day' WHERE id=$1`, runID); err != nil {
		t.Fatal(err)
	}
	expired, err := f.s.Analyze(ctx, p, id, AnalysisRequest{Revision: detail.Revision}, true)
	if err != nil || expired.State != "expired" || expired.Quote != nil || expired.Run != nil {
		t.Fatal("expired result spent automatically", expired, err)
	}
	reason := "Revisão solicitada pelo responsável"
	_, err = f.s.Analyze(ctx, p, id, AnalysisRequest{Revision: detail.Revision, ReprocessReason: reason}, false)
	requireCode(t, err, contracts.ErrSubscriptionRequired)
	f.s.engine.Config = &config.Config{Env: "dev"}
	fresh, err := f.s.Analyze(ctx, p, id, AnalysisRequest{Revision: detail.Revision, ReprocessReason: reason}, false)
	if err != nil || fresh.Quote == nil {
		t.Fatal(err)
	}
	rerun, err := f.s.Analyze(ctx, p, id, AnalysisRequest{Revision: detail.Revision, ReprocessReason: reason, QuoteID: fresh.Quote.ID, QuoteSignature: fresh.Quote.Signature}, true)
	if err != nil || rerun.Run == nil || rerun.Run.ID == created.Run.ID {
		t.Fatal("reprocessing", err)
	}
	newID, _ := publicid.Decode("run", rerun.Run.ID)
	stored, err := f.s.engine.Runs.Get(ctx, p.OrgID, newID)
	if err != nil || stored.Inputs.Project.ReprocessReason != reason {
		t.Fatal("missing reprocess audit", err)
	}
}

func TestFailedOrCancelledAnalysisCanRetry(t *testing.T) {
	f := pipeline(t)
	ctx := t.Context()
	for _, status := range []string{"failed", "cancelled"} {
		t.Run(status, func(t *testing.T) {
			p, id, detail, request := preparedAnalysis(t, f)
			created, err := f.s.Analyze(ctx, p, id, request, true)
			if err != nil {
				t.Fatal(err)
			}
			runID, _ := publicid.Decode("run", created.Run.ID)
			if _, err := testdb.Get(t).Admin.Exec(ctx, `UPDATE runs SET status=$2 WHERE id=$1`, runID, status); err != nil {
				t.Fatal(err)
			}
			retry, err := f.s.Analyze(ctx, p, id, AnalysisRequest{Revision: detail.Revision}, false)
			if err != nil || retry.Quote == nil || retry.Run != nil {
				t.Fatal("failure cannot retry", err)
			}
			next, err := f.s.Analyze(ctx, p, id, AnalysisRequest{Revision: detail.Revision, QuoteID: retry.Quote.ID, QuoteSignature: retry.Quote.Signature}, true)
			if err != nil || next.Run == nil || next.Run.ID == created.Run.ID {
				t.Fatal("retry reused failure", err)
			}
		})
	}
}

func TestAdditionalApprovalIsAtomicAndCannotReserveTwice(t *testing.T) {
	f := pipeline(t, harnesstest.Options{PauseAfterFindings: 5, AdditionalCredits: 20})
	ctx := t.Context()
	p, id, _, request := preparedAnalysis(t, f)
	created, err := f.s.Analyze(ctx, p, id, request, true)
	if err != nil {
		t.Fatal(err)
	}
	runID, _ := publicid.Decode("run", created.Run.ID)
	if err := f.s.engine.Runs.Execute(ctx, p.OrgID, runID, false); err != nil {
		t.Fatal(err)
	}
	paused, err := f.s.engine.Runs.Get(ctx, p.OrgID, runID)
	if err != nil {
		t.Fatal(err)
	}
	view := f.s.engine.Runs.View(ctx, paused)
	if view.PendingQuote == nil {
		t.Fatalf("expected approval: %#v", view)
	}
	approval := runs.ApproveRequest{QuoteID: view.PendingQuote.ID, QuoteSignature: view.PendingQuote.Signature}
	before, err := f.credits.Balance(ctx, p.OrgID)
	if err != nil {
		t.Fatal(err)
	}
	f.jobs.Set(&jobargs.Recorder{Err: errors.New("queue unavailable")})
	if _, err := f.s.engine.Runs.Approve(ctx, p, runID, approval); err == nil {
		t.Fatal("queue failure ignored")
	}
	after, err := f.credits.Balance(ctx, p.OrgID)
	if err != nil || before != after {
		t.Fatal("partial credit state", before, after, err)
	}
	still, err := f.s.engine.Runs.Get(ctx, p.OrgID, runID)
	if err != nil || still.Status != runs.StatusNeedsApproval {
		t.Fatal("approval partially committed", err)
	}
	f.jobs.Set(f.queue)
	outcomes := make(chan error, 4)
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := f.s.engine.Runs.Approve(ctx, p, runID, approval); outcomes <- err }()
	}
	wg.Wait()
	close(outcomes)
	successful := 0
	for err := range outcomes {
		if err == nil {
			successful++
		} else {
			requireCode(t, err, contracts.ErrRunNotAwaitingApproval)
		}
	}
	if successful != 1 {
		t.Fatal("duplicate approvals", successful)
	}
	var holds int
	if err := testdb.Get(t).Admin.QueryRow(ctx, `SELECT count(*) FROM credit_holds WHERE org_id=$1`, p.OrgID).Scan(&holds); err != nil || holds != 2 {
		t.Fatal("duplicate additional reservation", holds, err)
	}
}
