package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"

	"github.com/mmtmn/docmatch/services/api/internal/burst"
	"github.com/mmtmn/docmatch/services/api/internal/harness"
	"github.com/mmtmn/docmatch/services/api/internal/harness/harnesstest"
	pb "github.com/mmtmn/docmatch/services/api/internal/harness/pb"
	"github.com/mmtmn/docmatch/services/api/internal/logx"
	"github.com/mmtmn/docmatch/services/api/internal/runs/apix"
)

const testSecret = "segredo-do-pool-de-teste-com-32-caracteres!"

const sourceBody = "data;valor\n2026-11-01;100,00\n"

type env struct {
	t     *testing.T
	store *burst.MemStore
	keys  burst.Keys
	api   *url.URL
	fake  *harnesstest.Server
	h     harness.Client
}

// newEnv starts the internal endpoint over TLS on a memory store and a fake
// harness on a Unix socket, the two ends a burst worker talks to.
func newEnv(t *testing.T, opts harnesstest.Options) *env {
	t.Helper()
	keys, err := burst.DeriveKeys(testSecret)
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, store: burst.NewMemStore(), keys: keys}
	settings := burst.Settings{LongPoll: 50 * time.Millisecond, Heartbeat: time.Second}.WithDefaults()
	srv := &burst.Server{
		Store: e.store, Keys: keys, Settings: settings, Logger: logx.Discard(),
		Open: func(_ context.Context, uri string) (io.ReadCloser, bool, error) {
			if strings.HasPrefix(uri, "local://t/") {
				return io.NopCloser(strings.NewReader(sourceBody)), true, nil
			}
			return nil, false, nil
		},
	}
	tlsCfg, err := keys.ServerTLS(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewUnstartedServer(srv.Handler())
	ts.TLS = tlsCfg
	ts.StartTLS()
	t.Cleanup(ts.Close)
	e.api, _ = url.Parse(ts.URL)

	e.fake = harnesstest.NewServer(harnesstest.MustLoadGolden(), opts)
	sock, cleanup, err := harnesstest.SocketPath()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	stop, err := harnesstest.Serve(sock, e.fake)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	c, err := harness.Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	e.h = c
	return e
}

func sum(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func (e *env) queue(sha string) burst.Job {
	e.t.Helper()
	runID := uuid.New()
	req := &pb.RunRequest{RunId: apix.Encode(apix.PrefixRun, runID), Sources: []*pb.Source{
		{Id: "upl_bank", Uri: "local://t/quarantine/bank", Sha256: sha, SizeBytes: uint64(len(sourceBody)), FileName: "bank.csv", Mime: "text/csv"},
	}}
	raw, _ := proto.Marshal(req)
	j, err := e.store.CreateJob(context.Background(), burst.Job{ID: uuid.New(), OrgID: uuid.New(), RunID: runID, Request: raw, DeadlineSeconds: 600, CreatedAt: time.Now()})
	if err != nil {
		e.t.Fatal(err)
	}
	return j
}

func (e *env) agent(o Options) *Agent {
	e.t.Helper()
	o.APIURL, o.Keys, o.Harness, o.MediaRoot = e.api, e.keys, e.h, e.t.TempDir()
	if o.Identity.InstanceID == "" {
		o.Identity = Identity{InstanceID: "i-test", InstanceType: "c7g.large", Lifecycle: "spot"}
	}
	o.Logger = logx.Discard()
	o.FlushEvery = 20 * time.Millisecond
	a, err := New(o)
	if err != nil {
		e.t.Fatal(err)
	}
	return a
}

func (e *env) events(job burst.Job) []*pb.RunEvent {
	e.t.Helper()
	raw, err := e.store.Events(context.Background(), job.ID, 0, 10_000)
	if err != nil {
		e.t.Fatal(err)
	}
	out := make([]*pb.RunEvent, 0, len(raw))
	for _, r := range raw {
		ev := &pb.RunEvent{}
		if err := proto.Unmarshal(r.Data, ev); err != nil {
			e.t.Fatal(err)
		}
		out = append(out, ev)
	}
	return out
}

func (e *env) job(id uuid.UUID) burst.Job {
	e.t.Helper()
	j, _, err := e.store.GetJob(context.Background(), id)
	if err != nil {
		e.t.Fatal(err)
	}
	return j
}

func TestAgentRunsAJobToTheEnd(t *testing.T) {
	t.Parallel()
	e := newEnv(t, harnesstest.Options{})
	job := e.queue(sum(sourceBody))
	a := e.agent(Options{Once: true})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := a.Run(ctx); err != nil {
		t.Fatal(err)
	}
	got := e.job(job.ID)
	if got.State != burst.StateDone || got.InstanceID != "i-test" {
		t.Fatalf("job = %s (%s)", got.State, got.Reason)
	}
	evs := e.events(job)
	findings := 0
	for i, ev := range evs {
		if ev.GetSeq() != uint64(i+1) { //nolint:gosec // small
			t.Fatalf("seq %d na posição %d", ev.GetSeq(), i)
		}
		if ev.GetFinding() != nil {
			findings++
		}
	}
	if evs[len(evs)-1].GetCompleted() == nil || findings != len(harnesstest.MustLoadGolden().Findings) {
		t.Fatalf("eventos = %d, achados = %d", len(evs), findings)
	}
	// The harness read a staged copy of the source, never the store URI.
	runs := e.fake.Runs()
	if len(runs) != 1 || !strings.HasPrefix(runs[0].GetSources()[0].GetUri(), "local://staging/.staging/") {
		t.Fatalf("pedido ao harness = %v", runs)
	}
	if runs[0].GetOptions().GetDeadline() == nil {
		t.Fatal("o prazo carimbado na entrega deveria chegar ao harness")
	}
}

func TestAgentRefusesASourceThatDoesNotMatch(t *testing.T) {
	t.Parallel()
	e := newEnv(t, harnesstest.Options{})
	job := e.queue(sum("outro conteúdo"))
	a := e.agent(Options{Once: true})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := a.Run(ctx); err != nil {
		t.Fatal(err)
	}
	got := e.job(job.ID)
	if got.State != burst.StateFailed || !strings.Contains(got.Reason, "SHA-256") {
		t.Fatalf("job = %s (%s)", got.State, got.Reason)
	}
	if len(e.fake.Runs()) != 0 {
		t.Fatal("o harness não deveria rodar com uma fonte adulterada")
	}
}

// fakeIMDS answers the metadata paths; the Spot notice appears after armAt
// polls of spot/instance-action.
type fakeIMDS struct {
	polls atomic.Int32
	armAt int32
}

func (f *fakeIMDS) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodPut && r.URL.Path == "/latest/api/token":
		_, _ = io.WriteString(w, "tok")
		return
	case r.Header.Get("X-aws-ec2-metadata-token") != "tok":
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	switch strings.TrimPrefix(r.URL.Path, "/latest/meta-data/") {
	case "instance-id":
		_, _ = io.WriteString(w, "i-0spot")
	case "instance-type":
		_, _ = io.WriteString(w, "c7g.large")
	case "instance-life-cycle":
		_, _ = io.WriteString(w, "spot")
	case "spot/instance-action":
		if f.polls.Add(1) >= f.armAt {
			_, _ = io.WriteString(w, `{"action":"terminate","time":"2026-11-05T12:02:00Z"}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	case "autoscaling/target-lifecycle-state":
		_, _ = io.WriteString(w, "InService")
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func TestAgentHandsBackAJobOnSpotInterruption(t *testing.T) {
	t.Parallel()
	e := newEnv(t, harnesstest.Options{Delay: 20 * time.Millisecond})
	job := e.queue(sum(sourceBody))
	md := httptest.NewServer(&fakeIMDS{armAt: 6})
	t.Cleanup(md.Close)
	imds := NewIMDS(md.URL)
	id, err := imds.Identity(context.Background())
	if err != nil || id.InstanceID != "i-0spot" || id.Lifecycle != "spot" {
		t.Fatalf("identidade = %+v %v", id, err)
	}
	a := e.agent(Options{Identity: id, IMDS: imds, NoticeInterval: 50 * time.Millisecond})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	deadline := time.Now().Add(20 * time.Second)
	for e.job(job.ID).State == burst.StateQueued || e.job(job.ID).State == burst.StateClaimed {
		if time.Now().After(deadline) {
			t.Fatal("o trabalho não terminou")
		}
		time.Sleep(20 * time.Millisecond)
	}
	got := e.job(job.ID)
	if got.State != burst.StateInterrupted || !strings.Contains(got.Reason, "Spot") {
		t.Fatalf("job = %s (%s)", got.State, got.Reason)
	}
	evs := e.events(job)
	if len(evs) == 0 || evs[len(evs)-1].GetCompleted() != nil {
		t.Fatalf("deveria ter parado no meio: %d eventos", len(evs))
	}
	// A draining instance takes no new job; it waits to be terminated.
	next := e.queue(sum(sourceBody))
	time.Sleep(200 * time.Millisecond)
	if !a.Draining() || e.job(next.ID).State != burst.StateQueued {
		t.Fatalf("instância em encerramento pegou trabalho novo: %s", e.job(next.ID).State)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestAgentStopsAJobTheAPIDropped(t *testing.T) {
	t.Parallel()
	e := newEnv(t, harnesstest.Options{Delay: 20 * time.Millisecond})
	job := e.queue(sum(sourceBody))
	a := e.agent(Options{Once: true})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	go func() {
		for e.job(job.ID).State != burst.StateClaimed {
			time.Sleep(10 * time.Millisecond)
		}
		time.Sleep(100 * time.Millisecond)
		_, _ = e.store.Cancel(context.Background(), job.ID, "cancelada pelo cliente", time.Now())
	}()
	start := time.Now()
	if err := a.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if got := e.job(job.ID); got.State != burst.StateCancelled {
		t.Fatalf("job = %s", got.State)
	}
	if time.Since(start) > 10*time.Second {
		t.Fatal("o trabalho cancelado deveria parar logo")
	}
}

func TestAgentGivesUpWhenTheAPIIsGone(t *testing.T) {
	t.Parallel()
	keys, _ := burst.DeriveKeys(testSecret)
	down, _ := url.Parse("https://127.0.0.1:1")
	a, err := New(Options{APIURL: down, Keys: keys, Harness: harness.Unavailable{}, MediaRoot: t.TempDir(), Identity: Identity{InstanceID: "i-x"}, APIDownAfter: time.Millisecond, Logger: logx.Discard()})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := a.Run(ctx); err == nil || !strings.Contains(err.Error(), "sem resposta") {
		t.Fatalf("err = %v", err)
	}
	if _, err := New(Options{}); err == nil {
		t.Fatal("opções vazias deveriam falhar")
	}
	// MaxIdle ends a worker without jobs (local runs).
	e := newEnv(t, harnesstest.Options{})
	idle := e.agent(Options{MaxIdle: time.Millisecond})
	if err := idle.Run(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestIMDS(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	md := httptest.NewServer(&fakeIMDS{armAt: 2})
	t.Cleanup(md.Close)
	m := NewIMDS(md.URL + "/")
	if n, err := m.Notice(ctx); err != nil || n != "" {
		t.Fatalf("sem aviso: %q %v", n, err)
	}
	if n, err := m.Notice(ctx); err != nil || !strings.Contains(n, "Spot") {
		t.Fatalf("aviso Spot: %q %v", n, err)
	}
	if _, err := m.Get(ctx, "nada"); !errors.Is(err, errNotFound) {
		t.Fatalf("inexistente: %v", err)
	}
	// The Auto Scaling group taking the instance out is a notice too.
	asg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest/api/token":
			_, _ = io.WriteString(w, "tok")
		case "/latest/meta-data/autoscaling/target-lifecycle-state":
			_, _ = io.WriteString(w, "Terminated")
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(asg.Close)
	if n, err := NewIMDS(asg.URL).Notice(ctx); err != nil || !strings.Contains(n, "Terminated") {
		t.Fatalf("scale-in: %q %v", n, err)
	}
	// A rejected token is dropped and the error surfaces.
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			_, _ = io.WriteString(w, "tok")
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(broken.Close)
	bm := NewIMDS(broken.URL)
	if _, err := bm.Notice(ctx); err == nil {
		t.Fatal("401 deveria falhar")
	}
	if bm.token != "" {
		t.Fatal("o token recusado deveria ser descartado")
	}
	if _, err := NewIMDS("http://127.0.0.1:1").Identity(ctx); err == nil {
		t.Fatal("sem serviço de metadados deveria falhar")
	}
}
