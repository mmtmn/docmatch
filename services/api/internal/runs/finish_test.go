package runs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/mmtmn/docmatch/services/api/internal/email"
	"github.com/mmtmn/docmatch/services/api/internal/estimate"
	"github.com/mmtmn/docmatch/services/api/internal/estimate/throughput"
	"github.com/mmtmn/docmatch/services/api/internal/harness/harnesstest"
	"github.com/mmtmn/docmatch/services/api/internal/logx"
)

func repoThroughput(t testing.TB) *throughput.Config {
	t.Helper()
	dir, _ := os.Getwd()
	for i := 0; i < 8; i++ {
		p := filepath.Join(dir, "config", "throughput.yaml")
		if _, err := os.Stat(p); err == nil {
			c, err := throughput.Load(p)
			if err != nil {
				t.Fatal(err)
			}
			return c
		}
		dir = filepath.Dir(dir)
	}
	t.Skip("config/throughput.yaml não encontrado")
	return nil
}

type sentMail struct {
	org uuid.UUID
	key string
	msg email.Message
}

// fakeMailer records the e-mails, dedupes by key like the outbox, and can
// pretend the user is gone or the database is down.
type fakeMailer struct {
	mu      sync.Mutex
	sent    []sentMail
	address string
	gone    bool
	fail    error
}

func (m *fakeMailer) UserEmail(_ context.Context, _ pgx.Tx, userID uuid.UUID) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail != nil {
		return "", m.fail
	}
	if m.gone {
		return "", nil
	}
	if m.address != "" {
		return m.address, nil
	}
	return "u-" + userID.String()[:8] + "@example.com", nil
}

func (m *fakeMailer) EnqueueOnce(_ context.Context, _ pgx.Tx, orgID uuid.UUID, key string, msg email.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range m.sent {
		if s.key == key {
			return nil
		}
	}
	m.sent = append(m.sent, sentMail{org: orgID, key: key, msg: msg})
	return nil
}

func (m *fakeMailer) Sent() []sentMail {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]sentMail(nil), m.sent...)
}

func (m *fakeMailer) set(fn func(m *fakeMailer)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	fn(m)
}

func TestRunTimingIsKeptAndRecorded(t *testing.T) {
	t.Parallel()
	f := newFixture(t, harnesstest.Options{}, nil)
	ctx := context.Background()
	f.grant(t, 1000)
	run, q := f.create(t)
	timing := run.Timing()
	if q.ExpectedSeconds <= 0 || timing.ExpectedSeconds != q.ExpectedSeconds || timing.Large || timing.Units[throughput.KindTable] <= 0 {
		t.Fatalf("timing na criação = %+v (orçamento %d s)", timing, q.ExpectedSeconds)
	}
	if err := f.svc.Execute(ctx, f.org, run.ID, false); err != nil {
		t.Fatal(err)
	}
	done, _ := f.svc.Get(ctx, f.org, run.ID)
	// The first event starts the clock and the save of every event reads it
	// again; the fake clock moves a second per read: one second per event.
	events := len(f.store.Events(run.ID))
	if got := done.Timing().ActiveMs; got != int64(events)*1000 {
		t.Fatalf("tempo ativo = %d ms com %d eventos", got, events)
	}
	if done.Timing().ExpectedSeconds != q.ExpectedSeconds {
		t.Fatal("o tempo esperado precisa sobreviver à execução")
	}
	timings := f.estStore.Timings()
	if len(timings) != 1 || timings[0].RunID != run.ID || timings[0].Status != estimate.TimingCompleted || timings[0].ActiveSeconds != float64(events) || timings[0].ExpectedSeconds != q.ExpectedSeconds {
		t.Fatalf("durações = %+v", timings)
	}
	if len(f.mail.Sent()) != 0 {
		t.Fatalf("pedido pequeno e rápido não manda e-mail: %+v", f.mail.Sent())
	}
	// The view keeps the timing of the quote for the clients.
	if v := f.svc.View(ctx, done); v.Quote == nil || v.Quote.ExpectedSeconds != q.ExpectedSeconds {
		t.Fatalf("view = %+v", v.Quote)
	}
}

func TestStatsKeepOtherKeys(t *testing.T) {
	t.Parallel()
	r := Run{Stats: []byte(`{"other":{"a":1}}`)}
	r.SetTiming(Timing{ExpectedSeconds: 5, Units: estimate.Units{throughput.KindTable: 1}})
	if !strings.Contains(string(r.Stats), `"other":{"a":1}`) || r.Timing().ExpectedSeconds != 5 {
		t.Fatalf("stats = %s", r.Stats)
	}
	if bad := (Run{Stats: []byte("não é json")}).Timing(); bad.ExpectedSeconds != 0 || bad.Large || bad.Units != nil || bad.ActiveMs != 0 {
		t.Fatal("stats ilegíveis = timing zero")
	}
	broken := Run{Stats: []byte("null")}
	broken.SetTiming(Timing{Large: true})
	if !broken.Timing().Large {
		t.Fatalf("stats nulos: %s", broken.Stats)
	}
}

func TestLargeRunMailsOnceWhenItCompletes(t *testing.T) {
	t.Parallel()
	f := newFixture(t, harnesstest.Options{}, nil)
	ctx := context.Background()
	f.grant(t, 1000)
	// A full queue makes the quote large.
	f.estStore.SetRunsInFlight(500)
	run, q := f.create(t)
	f.estStore.SetRunsInFlight(0)
	if !q.Large || !run.Timing().Large {
		t.Fatalf("orçamento deveria ser grande: %+v", q)
	}
	if err := f.svc.Execute(ctx, f.org, run.ID, false); err != nil {
		t.Fatal(err)
	}
	sent := f.mail.Sent()
	if len(sent) != 1 {
		t.Fatalf("e-mails = %d", len(sent))
	}
	m := sent[0]
	if m.org != f.org || m.key != MailKeyPrefix+run.PublicID() || m.msg.Tag != email.TagRunFinished || !strings.HasPrefix(m.msg.To, "u-") {
		t.Fatalf("e-mail = %+v", m)
	}
	if !strings.Contains(m.msg.Text, "https://example.com/resultados?run="+run.PublicID()) || m.msg.Subject != "Sua conciliação terminou" {
		t.Fatalf("texto = %s", m.msg.Text)
	}
	// Nothing of the documents goes in the e-mail.
	for _, leak := range []string{"protheus", "omie", ".csv", "twin."} {
		if strings.Contains(m.msg.Text, leak) || strings.Contains(m.msg.HTML, leak) {
			t.Fatalf("o e-mail não pode citar %q: %s", leak, m.msg.Text)
		}
	}
	// A repeated job on a finished run sends nothing new.
	if err := f.svc.Execute(ctx, f.org, run.ID, false); err != nil {
		t.Fatal(err)
	}
	if len(f.mail.Sent()) != 1 {
		t.Fatal("segunda execução do job não pode mandar outro e-mail")
	}
}

func TestSlowRunAndFailureMail(t *testing.T) {
	t.Parallel()
	f := newFixture(t, harnesstest.Options{FailAfterFindings: 2, FailCode: "model_unavailable"}, nil)
	ctx := context.Background()
	f.grant(t, 1000)
	cfg := repoThroughput(t)
	// A small order that fails after waiting longer than notify_after_seconds.
	run, q := f.create(t)
	if q.Large {
		t.Fatal("orçamento pequeno esperado")
	}
	f.now = f.now.Add(time.Duration(cfg.NotifyAfterSeconds+1) * time.Second)
	if err := f.svc.Execute(ctx, f.org, run.ID, false); err != nil {
		t.Fatal(err)
	}
	sent := f.mail.Sent()
	if len(sent) != 1 || sent[0].msg.Subject != "Sua conciliação não terminou" || !strings.Contains(sent[0].msg.Text, "voltaram para o seu saldo") {
		t.Fatalf("e-mail de falha = %+v", sent)
	}
	timings := f.estStore.Timings()
	if len(timings) != 1 || timings[0].Status != estimate.TimingFailed {
		t.Fatalf("duração da falha = %+v", timings)
	}

	// The same slow failure for a user who is gone, or with the database
	// refusing the e-mail, still fails the run and sends nothing.
	f.mail.set(func(m *fakeMailer) { m.gone = true })
	gone, _ := f.create(t)
	f.now = f.now.Add(time.Duration(cfg.NotifyAfterSeconds+1) * time.Second)
	if err := f.svc.Execute(ctx, f.org, gone.ID, false); err != nil {
		t.Fatal(err)
	}
	f.mail.set(func(m *fakeMailer) { m.gone, m.fail = false, errors.New("banco fora") })
	down, _ := f.create(t)
	f.now = f.now.Add(time.Duration(cfg.NotifyAfterSeconds+1) * time.Second)
	if err := f.svc.Execute(ctx, f.org, down.ID, false); err != nil {
		t.Fatal(err)
	}
	for _, id := range []uuid.UUID{gone.ID, down.ID} {
		if r, _ := f.svc.Get(ctx, f.org, id); r.Status != StatusFailed {
			t.Fatalf("execução %s = %s", r.PublicID(), r.Status)
		}
	}
	f.mail.set(func(m *fakeMailer) { m.fail = nil })
	// An invalid stored address is logged and skipped.
	f.mail.set(func(m *fakeMailer) { m.address = "sem arroba" })
	bad, _ := f.create(t)
	f.now = f.now.Add(time.Duration(cfg.NotifyAfterSeconds+1) * time.Second)
	if err := f.svc.Execute(ctx, f.org, bad.ID, false); err != nil {
		t.Fatal(err)
	}
	if len(f.mail.Sent()) != 1 {
		t.Fatalf("e-mails = %+v", f.mail.Sent())
	}
}

func TestSetupFailureOfLargeRunMails(t *testing.T) {
	t.Parallel()
	f := newFixture(t, harnesstest.Options{}, nil)
	ctx := context.Background()
	f.grant(t, 1000)
	f.estStore.SetRunsInFlight(500)
	run, _ := f.create(t)
	// The quote disappears: the worker fails the run before the harness.
	cur, _ := f.svc.Get(ctx, f.org, run.ID)
	cur.QuoteID = nil
	_, _ = f.store.SaveRun(ctx, cur, nil)
	if err := f.svc.Execute(ctx, f.org, run.ID, false); err != nil {
		t.Fatal(err)
	}
	if r, _ := f.svc.Get(ctx, f.org, run.ID); r.Status != StatusFailed || r.Timing().ActiveMs != 0 {
		t.Fatalf("run = %+v", r)
	}
	if sent := f.mail.Sent(); len(sent) != 1 || sent[0].msg.Subject != "Sua conciliação não terminou" {
		t.Fatalf("e-mails = %+v", sent)
	}
}

func TestNoMailWithoutPersonOrForCancel(t *testing.T) {
	t.Parallel()
	f := newFixture(t, harnesstest.Options{}, nil)
	ctx := context.Background()
	f.grant(t, 1000)
	f.estStore.SetRunsInFlight(500)
	// Cancelled by the person: no e-mail, no timing.
	run, _ := f.create(t)
	if _, err := f.svc.Cancel(ctx, f.principal(), run.ID); err != nil {
		t.Fatal(err)
	}
	// An API-key run has no user to write to.
	keyRun, _ := f.create(t)
	cur, _ := f.svc.Get(ctx, f.org, keyRun.ID)
	cur.UserID = nil
	_, _ = f.store.SaveRun(ctx, cur, nil)
	if err := f.svc.Execute(ctx, f.org, keyRun.ID, false); err != nil {
		t.Fatal(err)
	}
	if len(f.mail.Sent()) != 0 {
		t.Fatalf("e-mails = %+v", f.mail.Sent())
	}
	if n := len(f.estStore.Timings()); n != 1 {
		t.Fatalf("só a execução concluída entra no histórico: %d", n)
	}
	// Without a mailer or a results address, or without timing configured,
	// nothing qualifies.
	done, _ := f.svc.Get(ctx, f.org, keyRun.ID)
	uid := uuid.New()
	done.UserID = &uid
	plain, err := New(Deps{Store: f.store, Uploads: f.svc.d.Uploads, Estimates: f.est, Rules: f.rules, Credits: f.credits, Jobs: f.jobs, Harness: f.svc.d.Harness, Logger: logx.Discard()})
	if err != nil {
		t.Fatal(err)
	}
	if plain.shouldMail(done) || !f.svc.shouldMail(done) {
		t.Fatal("shouldMail depende do mailer e do endereço dos resultados")
	}
	small := done
	small.SetTiming(Timing{})
	if f.svc.shouldMail(small) {
		t.Fatal("pequena e rápida não qualifica")
	}
	noTiming, _ := estimate.New(estimate.Deps{Store: f.estStore, Harness: f.svc.d.Harness, Pricing: f.est.Pricing(), Signer: mustSigner(t), Sources: f.svc.d.Uploads})
	slow := small
	later := slow.CreatedAt.Add(24 * time.Hour)
	slow.FinishedAt = &later
	withoutCfg, _ := New(Deps{Store: f.store, Uploads: f.svc.d.Uploads, Estimates: noTiming, Rules: f.rules, Credits: f.credits, Jobs: f.jobs, Harness: f.svc.d.Harness, Mailer: f.mail, ResultsURL: func(string) string { return "" }, Logger: logx.Discard()})
	if withoutCfg.shouldMail(slow) || !f.svc.shouldMail(slow) {
		t.Fatal("sem throughput.yaml só o orçamento grande qualifica")
	}
	// recordTiming ignores runs that did not finish.
	f.svc.recordTiming(ctx, Run{ID: uuid.New(), OrgID: f.org, Status: StatusRunning})
	if n := len(f.estStore.Timings()); n != 1 {
		t.Fatalf("execução em andamento entrou no histórico: %d", n)
	}
}

func mustSigner(t *testing.T) *estimate.Signer {
	t.Helper()
	s, err := estimate.NewSigner("chave-de-teste-com-16-chars")
	if err != nil {
		t.Fatal(err)
	}
	return s
}
