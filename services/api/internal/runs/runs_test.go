package runs

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	contracts "github.com/mmtmn/docmatch/packages/contracts/gen"
	"github.com/mmtmn/docmatch/services/api/internal/credits"
	"github.com/mmtmn/docmatch/services/api/internal/estimate"
	"github.com/mmtmn/docmatch/services/api/internal/estimate/pricing"
	"github.com/mmtmn/docmatch/services/api/internal/harness"
	"github.com/mmtmn/docmatch/services/api/internal/harness/harnesstest"
	"github.com/mmtmn/docmatch/services/api/internal/httpx"
	"github.com/mmtmn/docmatch/services/api/internal/jobs/jobargs"
	"github.com/mmtmn/docmatch/services/api/internal/limits"
	"github.com/mmtmn/docmatch/services/api/internal/logx"
	"github.com/mmtmn/docmatch/services/api/internal/rules"
	"github.com/mmtmn/docmatch/services/api/internal/runs/apix"
	"github.com/mmtmn/docmatch/services/api/internal/sse"
	"github.com/mmtmn/docmatch/services/api/internal/storage"
	"github.com/mmtmn/docmatch/services/api/internal/uploads"
)

func repoPricing(t testing.TB) *pricing.Pricing {
	t.Helper()
	dir, _ := os.Getwd()
	for i := 0; i < 8; i++ {
		p := filepath.Join(dir, "config", "pricing.yaml")
		if _, err := os.Stat(p); err == nil {
			pr, err := pricing.Load(p, pricing.Overrides{})
			if err != nil {
				t.Fatal(err)
			}
			return pr
		}
		dir = filepath.Dir(dir)
	}
	t.Skip("config/pricing.yaml não encontrado")
	return nil
}

type fixture struct {
	svc      *Service
	store    *MemStore
	uploads  *uploads.MemStore
	est      *estimate.Service
	estStore *estimate.MemStore
	rules    *rules.Service
	credits  *credits.Service
	jobs     *jobargs.Recorder
	fake     *harnesstest.Server
	mail     *fakeMailer
	org      uuid.UUID
	user     uuid.UUID
	plan     string
	now      time.Time
	// clock is the real clock of the service; every read advances it a second.
	clock time.Time
}

func newFixture(t *testing.T, opts harnesstest.Options, client harness.Client) *fixture {
	t.Helper()
	p := repoPricing(t)
	signer, _ := estimate.NewSigner("chave-de-teste-com-16-chars")
	f := &fixture{store: NewMemStore(), uploads: uploads.NewMemStore(), estStore: estimate.NewMemStore(), jobs: &jobargs.Recorder{}, mail: &fakeMailer{}, org: uuid.New(), user: uuid.New(), plan: "pro", now: time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)}
	f.clock = f.now
	if client == nil {
		f.fake = harnesstest.NewServer(harnesstest.MustLoadGolden(), opts)
		sock, cleanup, err := harnesstest.SocketPath()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(cleanup)
		stop, err := harnesstest.Serve(sock, f.fake)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(stop)
		c, err := harness.Dial(sock)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = c.Close() })
		client = c
	}
	base, _ := url.Parse("http://localhost:8080")
	local, err := storage.NewLocal(t.TempDir(), "t", base)
	if err != nil {
		t.Fatal(err)
	}
	now := func() time.Time { return f.now }
	upSvc, err := uploads.New(uploads.Deps{Store: f.uploads, Stores: storage.Stores{Documents: local, Media: local}, Pricing: p, Jobs: f.jobs, Logger: logx.Discard(), Now: now})
	if err != nil {
		t.Fatal(err)
	}
	f.est, err = estimate.New(estimate.Deps{Store: f.estStore, Harness: client, Pricing: p, Throughput: repoThroughput(t), Signer: signer, Sources: upSvc, Logger: logx.Discard(), Now: now})
	if err != nil {
		t.Fatal(err)
	}
	f.rules, err = rules.New(rules.Deps{Store: rules.NewMemStore(), Logger: logx.Discard(), Now: now})
	if err != nil {
		t.Fatal(err)
	}
	f.credits = credits.New(credits.NewMemoryStore(), credits.WithClock(now))
	var clockMu sync.Mutex
	tick := func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		f.clock = f.clock.Add(time.Second)
		return f.clock
	}
	f.svc, err = New(Deps{
		Store: f.store, Uploads: upSvc, Estimates: f.est, Rules: f.rules, Credits: f.credits, Jobs: f.jobs, Harness: client,
		Mailer: f.mail, ResultsURL: func(id string) string { return "https://example.com/resultados?run=" + id }, Logger: logx.Discard(), Now: now, Clock: tick,
	})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *fixture) principal() apix.Principal {
	return apix.Principal{OrgID: f.org, UserID: &f.user, Plan: f.plan}
}

func (f *fixture) grant(t *testing.T, amount int64) {
	t.Helper()
	if _, err := f.credits.Grant(context.Background(), f.org, credits.GrantParams{Kind: credits.GrantPlan, Amount: amount, Source: "test"}); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) balance(t *testing.T) credits.Account {
	t.Helper()
	acct, err := f.credits.Balance(context.Background(), f.org)
	if err != nil {
		t.Fatal(err)
	}
	return acct
}

// sources seeds the four twin sources as clean uploads.
func (f *fixture) sources(t *testing.T) []estimate.SourceRef {
	t.Helper()
	roles := []string{"protheus", "omie", "bank", "ap_inbox"}
	refs := make([]estimate.SourceRef, 0, len(roles))
	for _, role := range roles {
		id, _ := uuid.NewV7()
		u := uploads.Upload{ID: id, OrgID: f.org, Kind: uploads.KindDocument, Store: "local", Role: role, FileName: role + ".csv", DeclaredMime: "text/csv", DetectedMime: "text/csv", SizeBytes: 1000, SHA256: bytes.Repeat([]byte{1}, 32), StorageKey: "quarantine/" + role, Status: uploads.StatusClean, CreatedAt: f.now}
		if _, err := f.uploads.CreateUpload(context.Background(), u); err != nil {
			t.Fatal(err)
		}
		refs = append(refs, estimate.SourceRef{UploadID: u.PublicID(), Role: role})
	}
	return refs
}

func (f *fixture) quote(t *testing.T, req estimate.Request) estimate.Quote {
	t.Helper()
	q, err := f.est.Estimate(context.Background(), f.principal(), req, func(u uploads.Upload) string { return "local://t/" + u.StorageKey })
	if err != nil {
		t.Fatal(err)
	}
	return q
}

func (f *fixture) create(t *testing.T) (Run, estimate.Quote) {
	t.Helper()
	req := estimate.Request{Sources: f.sources(t)}
	q := f.quote(t, req)
	run, err := f.svc.Create(context.Background(), f.principal(), CreateRequest{Request: req, QuoteID: q.ID, QuoteSignature: q.Signature})
	if err != nil {
		t.Fatal(err)
	}
	return run, q
}

func assertCode(t *testing.T, err error, code contracts.ErrorCode) {
	t.Helper()
	var ae *apix.Error
	if !errors.As(err, &ae) || ae.Code != code {
		t.Fatalf("erro = %v, esperado %s", err, code)
	}
}

func TestCreateRunPaths(t *testing.T) {
	t.Parallel()
	f := newFixture(t, harnesstest.Options{}, nil)
	ctx := context.Background()
	p := f.principal()
	req := estimate.Request{Sources: f.sources(t)}
	q := f.quote(t, req)
	// 402: no credits.
	_, err := f.svc.Create(ctx, p, CreateRequest{Request: req, QuoteID: q.ID, QuoteSignature: q.Signature})
	assertCode(t, err, contracts.ErrInsufficientCredits)
	var ae *apix.Error
	_ = errors.As(err, &ae)
	if ae.Details["required"] != q.CapCredits || ae.Details["available"] != int64(0) {
		t.Fatalf("details = %v", ae.Details)
	}
	if len(f.jobs.Jobs) != 0 {
		t.Fatal("nada deveria ter sido enfileirado")
	}
	f.grant(t, q.CapCredits+5)
	// 400: tampered signature.
	_, err = f.svc.Create(ctx, p, CreateRequest{Request: req, QuoteID: q.ID, QuoteSignature: "v1=" + strings.Repeat("b", 64)})
	assertCode(t, err, contracts.ErrQuoteInvalidSignature)
	// 404: unknown / malformed quote id.
	_, err = f.svc.Create(ctx, p, CreateRequest{Request: req, QuoteID: "qte_x", QuoteSignature: q.Signature})
	assertCode(t, err, contracts.ErrQuoteNotFound)
	_, err = f.svc.Create(ctx, p, CreateRequest{Request: req, QuoteID: apix.Encode(apix.PrefixQuote, uuid.New()), QuoteSignature: q.Signature})
	assertCode(t, err, contracts.ErrQuoteNotFound)
	// 409: different sources than quoted.
	other := estimate.Request{Sources: req.Sources[:2]}
	_, err = f.svc.Create(ctx, p, CreateRequest{Request: other, QuoteID: q.ID, QuoteSignature: q.Signature})
	assertCode(t, err, contracts.ErrQuoteMismatch)
	// 410: expired.
	f.now = f.now.Add(16 * time.Minute)
	_, err = f.svc.Create(ctx, p, CreateRequest{Request: req, QuoteID: q.ID, QuoteSignature: q.Signature})
	assertCode(t, err, contracts.ErrQuoteExpired)
	f.now = f.now.Add(-16 * time.Minute)
	// 202: created, credits held, job enqueued, quote consumed.
	run, err := f.svc.Create(ctx, p, CreateRequest{Request: req, QuoteID: q.ID, QuoteSignature: q.Signature})
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != StatusQueued || run.CreditsHeld != q.CapCredits || run.HoldID == nil || run.ExpiresAt == nil || run.Inputs.Plan != "pro" {
		t.Fatalf("run = %+v", run)
	}
	if acct := f.balance(t); acct.Held != q.CapCredits {
		t.Fatalf("reserva = %+v", acct)
	}
	if kinds := f.jobs.Kinds(); len(kinds) != 1 || kinds[0] != jobargs.KindRun {
		t.Fatalf("jobs = %v", kinds)
	}
	_, err = f.svc.Create(ctx, p, CreateRequest{Request: req, QuoteID: q.ID, QuoteSignature: q.Signature})
	assertCode(t, err, contracts.ErrQuoteMismatch) // already used
	// 429: concurrent runs (pro allows 3; two more fit, the fourth does not).
	f.grant(t, 10000)
	for i := 0; i < 2; i++ {
		f.create(t)
	}
	req4 := estimate.Request{Sources: f.sources(t)}
	q4 := f.quote(t, req4)
	_, err = f.svc.Create(ctx, p, CreateRequest{Request: req4, QuoteID: q4.ID, QuoteSignature: q4.Signature})
	assertCode(t, err, contracts.ErrConcurrentRunsExceeded)
	// Enqueue failure releases the hold.
	f.jobs.Err = errors.New("fila fora")
	_, _ = f.svc.Cancel(ctx, p, run.ID) // frees a slot
	req5 := estimate.Request{Sources: f.sources(t)}
	q5 := f.quote(t, req5)
	before := f.balance(t)
	if _, err := f.svc.Create(ctx, p, CreateRequest{Request: req5, QuoteID: q5.ID, QuoteSignature: q5.Signature}); err == nil {
		t.Fatal("falha na fila deveria falhar a criação")
	}
	if after := f.balance(t); after.Held != before.Held {
		t.Fatalf("reserva não liberada: %+v → %+v", before, after)
	}
	f.jobs.Err = nil
	// List and Get.
	rows, err := f.svc.List(ctx, f.org, ListFilter{}, apix.Page{Limit: 10})
	if err != nil || len(rows) != 3 {
		t.Fatalf("list = %d %v", len(rows), err)
	}
	rows, _ = f.svc.List(ctx, f.org, ListFilter{Status: StatusCancelled}, apix.Page{Limit: 10})
	if len(rows) != 1 || rows[0].ID != run.ID {
		t.Fatalf("filtro = %v", rows)
	}
	if _, err := f.svc.Get(ctx, uuid.New(), run.ID); err == nil {
		t.Fatal("outra organização")
	}
	view := f.svc.View(ctx, rows[0])
	if view.Status != StatusCancelled || len(view.Sources) != 4 || view.Quote == nil || view.Error == nil || view.Error.Code != "run_cancelled" {
		t.Fatalf("view = %+v", view)
	}
	if _, err := New(Deps{}); err == nil {
		t.Fatal("deps vazias")
	}
}

func TestExecuteCompletesAndSettles(t *testing.T) {
	t.Parallel()
	f := newFixture(t, harnesstest.Options{}, nil)
	ctx := context.Background()
	f.grant(t, 1000)
	run, q := f.create(t)
	if err := f.svc.Execute(ctx, f.org, run.ID, false); err != nil {
		t.Fatal(err)
	}
	done, _ := f.svc.Get(ctx, f.org, run.ID)
	if done.Status != StatusCompleted || done.Summary == nil || done.Summary.FindingsTotal != 23 || done.FinishedAt == nil || done.StartedAt == nil {
		t.Fatalf("run = %+v", done)
	}
	if done.CreditsSpent == 0 || done.CreditsSettled != done.CreditsSpent || done.CreditsRefunded != q.CapCredits-done.CreditsSpent {
		t.Fatalf("créditos = spent %d settled %d refunded %d cap %d", done.CreditsSpent, done.CreditsSettled, done.CreditsRefunded, q.CapCredits)
	}
	if acct := f.balance(t); acct.Held != 0 || acct.Balance != 1000-done.CreditsSpent {
		t.Fatalf("conta = %+v", acct)
	}
	events := f.store.Events(run.ID)
	if len(events) == 0 || events[len(events)-1].Name != EventCompleted || events[len(events)-1].Seq != done.LastSeq {
		t.Fatalf("eventos = %d, último %+v", len(events), events[len(events)-1])
	}
	findings, err := f.svc.Findings(ctx, f.org, run.ID, FindingFilter{}, apix.Page{Limit: 100})
	if err != nil || len(findings) != 23 || findings[0].Severity != "critical" {
		t.Fatalf("achados = %d %v (%+v)", len(findings), err, findings[0])
	}
	high, _ := f.svc.Findings(ctx, f.org, run.ID, FindingFilter{Severities: []string{"high"}}, apix.Page{Limit: 100})
	if len(high) != 6 {
		t.Fatalf("high = %d", len(high))
	}
	page2, _ := f.svc.Findings(ctx, f.org, run.ID, FindingFilter{}, apix.Page{Limit: 10, Offset: 20})
	if len(page2) != 3 {
		t.Fatalf("página = %d", len(page2))
	}
	matches, _ := f.store.ListAllMatches(ctx, f.org, run.ID)
	if len(matches) != 15 {
		t.Fatalf("casamentos = %d", len(matches))
	}
	fv := findings[0].View()
	if fv.ConfidenceLabel != "Confirmado" || !strings.HasPrefix(fv.ID, "fnd_") || !bytes.Contains(fv.Evidence, []byte(`"type":"row"`)) || fv.Status != "open" {
		t.Fatalf("view do achado = %+v", fv)
	}
	// Export.
	ex, err := f.svc.Export(ctx, f.org, run.ID, "csv")
	if err != nil || ex.ContentType != "text/csv; charset=utf-8" || !strings.HasSuffix(ex.FileName, ".csv") {
		t.Fatalf("export = %+v %v", ex, err)
	}
	var buf bytes.Buffer
	if err := ex.Write(&buf); err != nil || !strings.Contains(buf.String(), "twin.f04") {
		t.Fatalf("csv = %v %s", err, buf.String()[:80])
	}
	xl, err := f.svc.Export(ctx, f.org, run.ID, "xlsx")
	if err != nil {
		t.Fatal(err)
	}
	buf.Reset()
	if err := xl.Write(&buf); err != nil || buf.Len() == 0 {
		t.Fatal("xlsx vazio")
	}
	pdf, err := f.svc.Export(ctx, f.org, run.ID, "pdf")
	if err != nil {
		t.Fatal(err)
	}
	buf.Reset()
	if err := pdf.Write(&buf); err != nil || !bytes.HasPrefix(buf.Bytes(), []byte("%PDF-")) {
		t.Fatal("PDF inválido", err)
	}
	_, err = f.svc.Export(ctx, f.org, run.ID, "txt")
	assertCode(t, err, contracts.ErrExportFormatUnsupported)
	// Re-executing a finished run is a no-op; cancelling it is refused.
	if err := f.svc.Execute(ctx, f.org, run.ID, false); err != nil {
		t.Fatal(err)
	}
	_, err = f.svc.Cancel(ctx, f.principal(), run.ID)
	assertCode(t, err, contracts.ErrRunAlreadyFinished)
	if Money(123456) != "1234,56" || Money(-5) != "-0,05" {
		t.Fatal("Money")
	}
}

func TestNeedsApprovalThenApprove(t *testing.T) {
	t.Parallel()
	f := newFixture(t, harnesstest.Options{PauseAfterFindings: 5, AdditionalCredits: 20}, nil)
	ctx := context.Background()
	p := f.principal()
	f.grant(t, 1000)
	run, _ := f.create(t)
	if err := f.svc.Execute(ctx, f.org, run.ID, false); err != nil {
		t.Fatal(err)
	}
	paused, _ := f.svc.Get(ctx, f.org, run.ID)
	if paused.Status != StatusNeedsApproval || paused.PendingQuoteID == nil {
		t.Fatalf("run = %+v", paused)
	}
	view := f.svc.View(ctx, paused)
	if view.PendingQuote == nil || !view.PendingQuote.RequiresApproval || view.PendingQuote.ExpectedCredits < 20 {
		t.Fatalf("pending = %+v", view.PendingQuote)
	}
	pending := view.PendingQuote
	_, err := f.svc.Export(ctx, f.org, run.ID, "csv")
	assertCode(t, err, contracts.ErrRunNotCompleted)
	// Wrong quote, tampered signature, not awaiting.
	_, err = f.svc.Approve(ctx, p, run.ID, ApproveRequest{QuoteID: apix.Encode(apix.PrefixQuote, uuid.New()), QuoteSignature: pending.Signature})
	assertCode(t, err, contracts.ErrQuoteMismatch)
	_, err = f.svc.Approve(ctx, p, run.ID, ApproveRequest{QuoteID: pending.ID, QuoteSignature: "v1=" + strings.Repeat("c", 64)})
	assertCode(t, err, contracts.ErrQuoteInvalidSignature)
	_, err = f.svc.Approve(ctx, p, run.ID, ApproveRequest{QuoteID: "qte_x", QuoteSignature: pending.Signature})
	assertCode(t, err, contracts.ErrQuoteNotFound)
	// Approve: old hold settled for what was spent, new hold for the new cap, job re-queued with resume.
	spentBefore := paused.CreditsSpent
	resumed, err := f.svc.Approve(ctx, p, run.ID, ApproveRequest{QuoteID: pending.ID, QuoteSignature: pending.Signature})
	if err != nil || resumed.Status != StatusQueued || resumed.PendingQuoteID != nil || resumed.CreditsHeld != pending.CapCredits || resumed.CreditsSettled != spentBefore {
		t.Fatalf("approve = %+v %v", resumed, err)
	}
	jobs := f.jobs.Jobs
	if last, ok := jobs[len(jobs)-1].(jobargs.RunArgs); !ok || !last.Resume {
		t.Fatalf("job de retomada esperado: %v", jobs)
	}
	_, err = f.svc.Approve(ctx, p, run.ID, ApproveRequest{QuoteID: pending.ID, QuoteSignature: pending.Signature})
	assertCode(t, err, contracts.ErrRunNotAwaitingApproval)
	if err := f.svc.Execute(ctx, f.org, run.ID, false); err != nil {
		t.Fatal(err)
	}
	done, _ := f.svc.Get(ctx, f.org, run.ID)
	if done.Status != StatusCompleted {
		t.Fatalf("run = %+v", done)
	}
	findings, _ := f.store.ListAllFindings(ctx, f.org, run.ID)
	if len(findings) != 23 {
		t.Fatalf("achados após retomada = %d", len(findings))
	}
	if acct := f.balance(t); acct.Held != 0 || acct.Balance != 1000-done.CreditsSpent {
		t.Fatalf("conta = %+v (spent %d)", acct, done.CreditsSpent)
	}
	if len(f.fake.Runs()) != 2 || !f.fake.Runs()[1].GetResume() {
		t.Fatal("o harness deveria ter sido chamado com resume")
	}
}

func TestApproveInsufficientAndExpired(t *testing.T) {
	t.Parallel()
	// The harness pauses asking for far more than the balance can cover.
	f := newFixture(t, harnesstest.Options{PauseAfterFindings: 2, AdditionalCredits: 1_000_000}, nil)
	ctx := context.Background()
	p := f.principal()
	// Enough for the initial hold only.
	cap := f.quote(t, estimate.Request{Sources: f.sources(t)}).CapCredits
	f.grant(t, cap)
	run, _ := f.create(t)
	_ = f.svc.Execute(ctx, f.org, run.ID, false)
	paused, _ := f.svc.Get(ctx, f.org, run.ID)
	pending := f.svc.View(ctx, paused).PendingQuote
	_, err := f.svc.Approve(ctx, p, run.ID, ApproveRequest{QuoteID: pending.ID, QuoteSignature: pending.Signature})
	assertCode(t, err, contracts.ErrInsufficientCredits)
	still, _ := f.svc.Get(ctx, f.org, run.ID)
	if still.Status != StatusNeedsApproval {
		t.Fatal("deveria continuar aguardando")
	}
	// Expired pending quote cancels the run and refunds.
	f.now = f.now.Add(20 * time.Minute)
	_, err = f.svc.Approve(ctx, p, run.ID, ApproveRequest{QuoteID: pending.ID, QuoteSignature: pending.Signature})
	assertCode(t, err, contracts.ErrQuoteExpired)
	cancelled, _ := f.svc.Get(ctx, f.org, run.ID)
	if cancelled.Status != StatusCancelled {
		t.Fatalf("run = %+v", cancelled)
	}
	if acct := f.balance(t); acct.Held != 0 {
		t.Fatalf("reserva não devolvida: %+v", acct)
	}
}

func TestCancelAndFailure(t *testing.T) {
	t.Parallel()
	f := newFixture(t, harnesstest.Options{FailAfterFindings: 3, FailCode: "model_unavailable"}, nil)
	ctx := context.Background()
	p := f.principal()
	f.grant(t, 1000)
	run, _ := f.create(t)
	cancelled, err := f.svc.Cancel(ctx, p, run.ID)
	if err != nil || cancelled.Status != StatusCancelled || cancelled.Error.Code != "run_cancelled" || !cancelled.CancelRequested {
		t.Fatalf("cancel = %+v %v", cancelled, err)
	}
	if acct := f.balance(t); acct.Held != 0 || acct.Balance != 1000 {
		t.Fatalf("conta = %+v", acct)
	}
	events := f.store.Events(run.ID)
	if len(events) != 1 || events[0].Name != EventFailed {
		t.Fatalf("eventos = %+v", events)
	}
	if err := f.svc.Execute(ctx, f.org, run.ID, false); err != nil {
		t.Fatal("executar cancelada deveria ser no-op")
	}
	_, err = f.svc.Cancel(ctx, p, uuid.New())
	assertCode(t, err, contracts.ErrRunNotFound)
	// Failure from the harness releases the hold.
	run2, _ := f.create(t)
	if err := f.svc.Execute(ctx, f.org, run2.ID, false); err != nil {
		t.Fatal(err)
	}
	failed, _ := f.svc.Get(ctx, f.org, run2.ID)
	if failed.Status != StatusFailed || failed.Error == nil || failed.Error.Code != "model_unavailable" || failed.ErrorCode != "model_unavailable" {
		t.Fatalf("falha = %+v", failed)
	}
	if acct := f.balance(t); acct.Held != 0 || acct.Balance != 1000 {
		t.Fatalf("conta após falha = %+v", acct)
	}
	findings, _ := f.store.ListAllFindings(ctx, f.org, run2.ID)
	if len(findings) != 3 {
		t.Fatalf("achados parciais = %d", len(findings))
	}
	// Cancellation while running: the worker stops at the next event.
	run3, _ := f.create(t)
	cur, _ := f.svc.Get(ctx, f.org, run3.ID)
	cur.Status, cur.CancelRequested = StatusCancelled, true
	_, _ = f.store.SaveRun(ctx, cur, nil)
	if err := f.svc.Execute(ctx, f.org, run3.ID, false); err != nil {
		t.Fatal(err)
	}
}

func TestHarnessDownRetryThenFail(t *testing.T) {
	t.Parallel()
	f := newFixture(t, harnesstest.Options{}, nil)
	ctx := context.Background()
	f.grant(t, 1000)
	run, _ := f.create(t)
	// Swap the harness for an unavailable one after the quote was issued.
	down := newFixture(t, harnesstest.Options{}, harness.Unavailable{})
	down.store, down.uploads, down.estStore, down.org, down.now = f.store, f.uploads, f.estStore, f.org, f.now
	svc, err := New(Deps{Store: f.store, Uploads: f.svc.d.Uploads, Estimates: f.est, Rules: f.rules, Credits: f.credits, Jobs: f.jobs, Harness: harness.Unavailable{}, Logger: logx.Discard(), Now: func() time.Time { return f.now }})
	if err != nil {
		t.Fatal(err)
	}
	err = svc.Execute(ctx, f.org, run.ID, false)
	if !errors.Is(err, ErrRetry) {
		t.Fatalf("primeira tentativa = %v", err)
	}
	running, _ := f.svc.Get(ctx, f.org, run.ID)
	if running.Status != StatusRunning {
		t.Fatalf("deveria continuar em execução: %+v", running)
	}
	if err := svc.Execute(ctx, f.org, run.ID, true); err != nil {
		t.Fatal(err)
	}
	failed, _ := f.svc.Get(ctx, f.org, run.ID)
	if failed.Status != StatusFailed || failed.Error.Code != "harness_unavailable" {
		t.Fatalf("última tentativa = %+v", failed)
	}
	if acct := f.balance(t); acct.Held != 0 {
		t.Fatalf("reserva não liberada: %+v", acct)
	}
	// Setup failures (sources gone) fail the run too.
	run2, _ := f.create(t)
	for _, id := range run2.SourceIDs() {
		u, _, _ := f.uploads.GetUpload(ctx, f.org, id)
		u.Status = uploads.StatusRejected
		_, _ = f.uploads.UpdateUpload(ctx, u)
	}
	if err := f.svc.Execute(ctx, f.org, run2.ID, false); err != nil {
		t.Fatal(err)
	}
	if r2, _ := f.svc.Get(ctx, f.org, run2.ID); r2.Status != StatusFailed || r2.Error.Code != "upload_not_ready" {
		t.Fatalf("fontes indisponíveis = %+v", r2)
	}
}

func TestFeedbackAndLearn(t *testing.T) {
	t.Parallel()
	f := newFixture(t, harnesstest.Options{}, nil)
	ctx := context.Background()
	p := f.principal()
	f.grant(t, 1000)
	run, _ := f.create(t)
	_ = f.svc.Execute(ctx, f.org, run.ID, false)
	findings, _ := f.store.ListAllFindings(ctx, f.org, run.ID)
	target := findings[0]
	jobsBefore := len(f.jobs.Jobs)
	updated, err := f.svc.GiveFeedback(ctx, p, run.ID, target.ID, FeedbackRequest{Status: "confirmed", Note: "é isso"})
	if err != nil || updated.Status != "confirmed" || updated.Feedback == nil || updated.Feedback.By == "" {
		t.Fatalf("feedback = %+v %v", updated, err)
	}
	if len(f.store.FeedbackRows()) != 1 || len(f.jobs.Jobs) != jobsBefore+1 || f.jobs.Jobs[len(f.jobs.Jobs)-1].Kind() != jobargs.KindLearn {
		t.Fatalf("linhas=%d jobs=%v", len(f.store.FeedbackRows()), f.jobs.Kinds())
	}
	// Same verdict again: idempotent.
	if _, err := f.svc.GiveFeedback(ctx, p, run.ID, target.ID, FeedbackRequest{Status: "confirmed", Note: "é isso"}); err != nil || len(f.store.FeedbackRows()) != 1 {
		t.Fatal("feedback repetido deveria ser inofensivo")
	}
	// A changed verdict appends.
	if _, err := f.svc.GiveFeedback(ctx, p, run.ID, findings[1].ID, FeedbackRequest{Status: "dismissed", Reason: "false_positive"}); err != nil || len(f.store.FeedbackRows()) != 2 {
		t.Fatal("segundo feedback")
	}
	_, err = f.svc.GiveFeedback(ctx, p, run.ID, target.ID, FeedbackRequest{Status: "maybe"})
	assertCode(t, err, contracts.ErrValidationFailed)
	_, err = f.svc.GiveFeedback(ctx, p, run.ID, target.ID, FeedbackRequest{Status: "confirmed", Reason: "because"})
	assertCode(t, err, contracts.ErrValidationFailed)
	_, err = f.svc.GiveFeedback(ctx, p, run.ID, target.ID, FeedbackRequest{Status: "confirmed", Note: strings.Repeat("x", 501)})
	assertCode(t, err, contracts.ErrTextTooLong)
	_, err = f.svc.GiveFeedback(ctx, p, run.ID, uuid.New(), FeedbackRequest{Status: "confirmed"})
	assertCode(t, err, contracts.ErrFindingNotFound)
	_, err = f.svc.GiveFeedback(ctx, p, uuid.New(), target.ID, FeedbackRequest{Status: "confirmed"})
	assertCode(t, err, contracts.ErrRunNotFound)
	// Starter plans do not learn.
	starter := apix.Principal{OrgID: f.org, Plan: "starter"}
	n := len(f.jobs.Jobs)
	if _, err := f.svc.GiveFeedback(ctx, starter, run.ID, findings[2].ID, FeedbackRequest{Status: "resolved"}); err != nil || len(f.jobs.Jobs) != n {
		t.Fatal("starter não deveria enfileirar aprendizado")
	}
	// Learn: the fake proposes one rule per confirmed finding.
	if err := f.svc.Learn(ctx, f.org, run.ID); err != nil {
		t.Fatal(err)
	}
	proposed, _ := f.rules.Search(ctx, f.org, rules.Query{Limit: 10})
	if len(proposed) != 2 || proposed[0].Status != rules.StatusCandidate {
		t.Fatalf("regras propostas = %+v", proposed)
	}
	after, _ := f.svc.Get(ctx, f.org, run.ID)
	if after.Summary == nil || len(after.Summary.ProposedRuleIDs) != 2 {
		t.Fatalf("resumo = %+v", after.Summary)
	}
	if len(f.fake.Proposals()) != 1 || len(f.fake.Proposals()[0].GetConfirmedFindings()) != 2 || len(f.fake.Proposals()[0].GetDismissedFindings()) != 1 {
		t.Fatalf("pedido ao harness = %v", f.fake.Proposals())
	}
	if err := f.svc.Learn(ctx, f.org, uuid.New()); err != nil {
		t.Fatal("execução inexistente deveria ser ignorada")
	}
	// Round trip of a finding through the proto.
	pbF := FindingToProto(target)
	if pbF.GetCode() != target.Code || len(pbF.GetEvidence()) == 0 || pbF.GetSeverity().String() != "SEVERITY_CRITICAL" {
		t.Fatalf("FindingToProto = %v", pbF)
	}
}

type frame struct {
	id, event string
}

func readFrames(body string) []frame {
	var frames []frame
	cur := frame{}
	sc := bufio.NewScanner(strings.NewReader(body))
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			if cur.event != "" {
				frames = append(frames, cur)
			}
			cur = frame{}
		case strings.HasPrefix(line, "id: "):
			cur.id = line[4:]
		case strings.HasPrefix(line, "event: "):
			cur.event = line[7:]
		}
	}
	return frames
}

func TestHandlerHTTP(t *testing.T) {
	t.Parallel()
	f := newFixture(t, harnesstest.Options{}, nil)
	ctx := context.Background()
	f.grant(t, 1000)
	p := f.principal()
	r := httpx.NewRouter(limits.Must())
	NewHandler(f.svc, apix.Static(p)).Mount(r)
	estimate.NewHandler(f.est, apix.Static(p), func(u uploads.Upload) string { return u.StorageKey }).Mount(r)
	srv := httptest.NewServer(httpx.Chain(r, httpx.Standard(httpx.StandardOptions{Logger: logx.Discard()})...))
	t.Cleanup(srv.Close)
	do := func(method, path, body string, headers map[string]string) (*http.Response, map[string]any) {
		req, _ := http.NewRequestWithContext(ctx, method, srv.URL+path, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp, out
	}
	sources := f.sources(t)
	estReq, _ := json.Marshal(estimate.Request{Sources: sources})
	resp, quote := do(http.MethodPost, "/v1/runs/estimate", string(estReq), nil)
	if resp.StatusCode != http.StatusOK || quote["signature"] == nil {
		t.Fatalf("estimate = %d %v", resp.StatusCode, quote)
	}
	createBody := `{"sources":` + mustJSONString(sources) + `,"quote_id":"` + quote["id"].(string) + `","quote_signature":"` + quote["signature"].(string) + `"}`
	if resp, out := do(http.MethodPost, "/v1/runs", createBody, nil); resp.StatusCode != http.StatusBadRequest || out["code"] != "missing_idempotency_key" {
		t.Fatalf("sem chave = %d %v", resp.StatusCode, out)
	}
	resp, run := do(http.MethodPost, "/v1/runs", createBody, map[string]string{"Idempotency-Key": "k1"})
	if resp.StatusCode != http.StatusAccepted || run["status"] != "queued" || !strings.HasPrefix(resp.Header.Get("Location"), "/v1/runs/run_") {
		t.Fatalf("create = %d %v %s", resp.StatusCode, run, resp.Header.Get("Location"))
	}
	id := run["id"].(string)
	if resp, replay := do(http.MethodPost, "/v1/runs", createBody, map[string]string{"Idempotency-Key": "k1"}); resp.StatusCode != http.StatusAccepted || replay["id"] != id {
		t.Fatalf("replay = %d %v", resp.StatusCode, replay)
	}
	if resp, out := do(http.MethodPost, "/v1/runs", createBody, map[string]string{"Idempotency-Key": "k2"}); resp.StatusCode != http.StatusConflict || out["code"] != "quote_mismatch" {
		t.Fatalf("orçamento usado = %d %v", resp.StatusCode, out)
	}
	if resp, out := do(http.MethodGet, "/v1/runs/"+id, "", nil); resp.StatusCode != http.StatusOK || out["credits"].(map[string]any)["reserved"] == float64(0) {
		t.Fatalf("get = %d %v", resp.StatusCode, out)
	}
	if resp, out := do(http.MethodGet, "/v1/runs?status=queued&limit=5", "", nil); resp.StatusCode != http.StatusOK || len(out["items"].([]any)) != 1 {
		t.Fatalf("list = %d %v", resp.StatusCode, out)
	}
	for _, bad := range []string{"/v1/runs?status=x", "/v1/runs?workflow_id=x", "/v1/runs?limit=0", "/v1/runs/" + id + "/findings?severity=huge", "/v1/runs/" + id + "/findings?status=x"} {
		if resp, _ := do(http.MethodGet, bad, "", nil); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s deveria ser 400", bad)
		}
	}
	if resp, out := do(http.MethodGet, "/v1/runs/run_nope", "", nil); resp.StatusCode != http.StatusNotFound || out["code"] != "run_not_found" {
		t.Fatalf("id inválido = %d %v", resp.StatusCode, out)
	}
	if resp, out := do(http.MethodPost, "/v1/runs/"+id+"/approve-quote", `{"quote_id":"qte_x","quote_signature":"v1=aa"}`, map[string]string{"Idempotency-Key": "a1"}); resp.StatusCode != http.StatusConflict || out["code"] != "run_not_awaiting_approval" {
		t.Fatalf("approve = %d %v", resp.StatusCode, out)
	}
	if resp, out := do(http.MethodGet, "/v1/runs/"+id+"/export?format=csv", "", nil); resp.StatusCode != http.StatusConflict || out["code"] != "run_not_completed" {
		t.Fatalf("export cedo = %d %v", resp.StatusCode, out)
	}
	// Execute the run, then read findings, feedback, export and the SSE stream.
	runID, _ := apix.Decode(apix.PrefixRun, id)
	if err := f.svc.Execute(ctx, f.org, runID, false); err != nil {
		t.Fatal(err)
	}
	resp, findings := do(http.MethodGet, "/v1/runs/"+id+"/findings?severity=high,critical&limit=5", "", nil)
	items := findings["items"].([]any)
	if resp.StatusCode != http.StatusOK || len(items) != 5 || findings["next_cursor"] == nil {
		t.Fatalf("findings = %d %v", resp.StatusCode, findings)
	}
	fid := items[0].(map[string]any)["id"].(string)
	if resp, out := do(http.MethodPost, "/v1/runs/"+id+"/findings/"+fid+"/feedback", `{"status":"confirmed"}`, nil); resp.StatusCode != http.StatusOK || out["status"] != "confirmed" {
		t.Fatalf("feedback = %d %v", resp.StatusCode, out)
	}
	if resp, _ := do(http.MethodPost, "/v1/runs/"+id+"/findings/fnd_x/feedback", `{"status":"confirmed"}`, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatal("finding id inválido")
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/v1/runs/"+id+"/export?format=xlsx", nil)
	xresp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := readAll(xresp)
	if xresp.StatusCode != http.StatusOK || !strings.Contains(xresp.Header.Get("Content-Disposition"), "docmatch-"+id+".xlsx") || !bytes.HasPrefix(data, []byte("PK")) {
		t.Fatalf("export = %d %s", xresp.StatusCode, xresp.Header.Get("Content-Disposition"))
	}
	// SSE: full replay, then resume from the middle.
	req, _ = http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/v1/runs/"+id+"/events", nil)
	sresp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := readAll(sresp)
	frames := readFrames(string(body))
	if sresp.StatusCode != http.StatusOK || len(frames) < 30 || frames[len(frames)-1].event != "completed" {
		t.Fatalf("sse = %d, %d frames", sresp.StatusCode, len(frames))
	}
	req, _ = http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/v1/runs/"+id+"/events", nil)
	req.Header.Set(sse.HeaderLastEventID, frames[len(frames)-3].id)
	sresp, _ = http.DefaultClient.Do(req)
	body, _ = readAll(sresp)
	if tail := readFrames(string(body)); len(tail) != 2 || tail[1].event != "completed" {
		t.Fatalf("resume = %+v", tail)
	}
	req, _ = http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/v1/runs/"+id+"/events", nil)
	req.Header.Set(sse.HeaderLastEventID, "abc")
	sresp, _ = http.DefaultClient.Do(req)
	_, _ = readAll(sresp)
	if sresp.StatusCode != http.StatusBadRequest {
		t.Fatal("Last-Event-ID inválido")
	}
	// Tenant isolation: another organization sees neither the run nor its events.
	other := httpx.NewRouter(limits.Must())
	NewHandler(f.svc, apix.Static(apix.Principal{OrgID: uuid.New(), Plan: "pro"})).Mount(other)
	rec := httptest.NewRecorder()
	other.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/runs/"+id+"/events", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("isolamento = %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	other.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/runs/"+id+"/findings", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("isolamento dos achados = %d", rec.Code)
	}
	if resp, out := do(http.MethodPost, "/v1/runs/"+id+"/cancel", "", nil); resp.StatusCode != http.StatusConflict || out["code"] != "run_already_finished" {
		t.Fatalf("cancel = %d %v", resp.StatusCode, out)
	}
	anon := httpx.NewRouter(limits.Must())
	NewHandler(f.svc, nil).Mount(anon)
	rec = httptest.NewRecorder()
	anon.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/runs", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("sem auth = %d", rec.Code)
	}
}

func mustJSONString(v any) string {
	data, _ := json.Marshal(v)
	return string(data)
}

func readAll(resp *http.Response) ([]byte, error) {
	defer func() { _ = resp.Body.Close() }()
	var buf bytes.Buffer
	_, err := buf.ReadFrom(resp.Body)
	return buf.Bytes(), err
}
