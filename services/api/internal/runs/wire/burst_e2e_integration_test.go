//go:build integration

package wire_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/mmtmn/docmatch/services/api/internal/config"
	"github.com/mmtmn/docmatch/services/api/internal/credits"
	"github.com/mmtmn/docmatch/services/api/internal/estimate"
	"github.com/mmtmn/docmatch/services/api/internal/harness/harnesstest"
	"github.com/mmtmn/docmatch/services/api/internal/jobs/jobargs"
	"github.com/mmtmn/docmatch/services/api/internal/logx"
	"github.com/mmtmn/docmatch/services/api/internal/runs"
	"github.com/mmtmn/docmatch/services/api/internal/runs/apix"
	"github.com/mmtmn/docmatch/services/api/internal/runs/wire"
	"github.com/mmtmn/docmatch/services/api/internal/testdb"
	"github.com/mmtmn/docmatch/services/api/internal/uploads"
)

// The end-to-end run of the pool on one machine, without AWS: this test is
// the main box (the API's internal endpoint and the main worker's Execute,
// on Docker Postgres) and the burst worker is another process,
// `docmatch-worker burst` built from this module, with its own harness, its
// own media directory and no database setting at all. With
// DM_HARNESS_BIN=<dm-server> the burst worker runs the real Rust harness;
// otherwise the fake one that replays the Vantara twin.

const e2eSecret = "segredo-do-pool-do-teste-ponta-a-ponta-32+"

var (
	buildOnce sync.Once
	builtBins map[string]string
	buildErr  error
)

func moduleRoot() string {
	_, here, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(here), "..", "..", "..")
}

func repoRoot() string { return filepath.Join(moduleRoot(), "..", "..") }

// binaries builds docmatch-worker and the fake harness once per test binary.
func binaries(t *testing.T) map[string]string {
	t.Helper()
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "dm-burst-bin")
		if err != nil {
			buildErr = err
			return
		}
		builtBins = map[string]string{}
		for name, pkg := range map[string]string{"worker": "./cmd/worker", "fakeharness": "./internal/harness/harnesstest/cmd/fakeharness"} {
			out := filepath.Join(dir, name)
			cmd := exec.Command("go", "build", "-o", out, pkg)
			cmd.Dir = moduleRoot()
			cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
			if b, err := cmd.CombinedOutput(); err != nil {
				buildErr = fmt.Errorf("go build %s: %w\n%s", pkg, err, b)
				return
			}
			builtBins[name] = out
		}
	})
	if buildErr != nil {
		t.Fatal(buildErr)
	}
	return builtBins
}

// proc is a child process whose output is kept for the failure message.
type proc struct {
	cmd *exec.Cmd
	out *syncBuffer
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func start(t *testing.T, name string, env []string, args ...string) *proc {
	t.Helper()
	p := &proc{cmd: exec.Command(name, args...), out: &syncBuffer{}}
	p.cmd.Env, p.cmd.Stdout, p.cmd.Stderr, p.cmd.Dir = env, p.out, p.out, repoRoot()
	if err := p.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.stop)
	return p
}

func (p *proc) stop() {
	if p.cmd.ProcessState == nil {
		_ = p.cmd.Process.Signal(os.Interrupt)
		done := make(chan struct{})
		go func() { _ = p.cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			_ = p.cmd.Process.Kill()
		}
	}
}

// harnessFor starts the harness of one burst worker: the real dm-server
// when DM_HARNESS_BIN is set, else the fake (delay between events).
func harnessFor(t *testing.T, media string, delay time.Duration) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "dmb")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "h.sock")
	env := []string{"PATH=" + os.Getenv("PATH"), "DM_MEDIA_LOCAL_DIR=" + media, "DM_LLM_MODE=off", "HOME=" + dir}
	if bin := os.Getenv("DM_HARNESS_BIN"); bin != "" && delay == 0 {
		start(t, bin, env, "--socket", sock)
	} else {
		golden := filepath.Join(repoRoot(), "testdata", "golden", "vantara-twin", "expected.yaml")
		start(t, binaries(t)["fakeharness"], env, "-socket", sock, "-golden", golden, "-delay", delay.String())
	}
	for i := 0; i < 200; i++ {
		if _, err := os.Stat(sock); err == nil {
			return sock
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("o harness do trabalhador não subiu")
	return ""
}

// burstWorker starts `docmatch-worker burst` against the internal endpoint.
// Its environment is all it gets: no DATABASE_URL, no store credentials.
func burstWorker(t *testing.T, apiURL string, delay time.Duration, args ...string) *proc {
	t.Helper()
	media := t.TempDir()
	sock := harnessFor(t, media, delay)
	env := []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + media, "DM_ENV=dev",
		config.VarBurstAPIURL + "=" + apiURL, config.VarBurstPoolSecret + "=" + e2eSecret,
		config.VarHarnessSocket + "=" + sock, config.VarMediaLocalDir + "=" + media,
	}
	return start(t, binaries(t)["worker"], env, append([]string{"burst"}, args...)...)
}

func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

type mainBox struct {
	sys    *wire.System
	me     apix.Principal
	apiURL string
	refs   []estimate.SourceRef
}

// newMainBox assembles the pipeline with the pool on: text documents always go to
// the pool (DM_BURST_KINDS=document), the internal endpoint on loopback, the
// twin stored as clean uploads in the local store.
func newMainBox(t *testing.T, ctx context.Context) *mainBox {
	t.Helper()
	d := testdb.Get(t)
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
	listen := freePort(t)
	env := map[string]string{
		config.VarEnv: "dev", config.VarPublicBaseURL: "http://localhost:4321", config.VarAPIBaseURL: "http://localhost:8080",
		config.VarAPIListen: "127.0.0.1:18080", config.VarAdminListen: "127.0.0.1:18443", config.VarDatabaseURL: d.URL,
		config.VarSessionSecret: strings.Repeat("s", 48), config.VarQuoteHMACKey: strings.Repeat("q", 32),
		config.VarAPIKeyPepper: strings.Repeat("p", 32), config.VarWebhookSigningKey: strings.Repeat("w", 32),
		config.VarPricingFile: "config/pricing.yaml", config.VarHarnessSocket: sock, config.VarMediaLocalDir: t.TempDir(), config.VarLLMMode: "off",
		config.VarBurstEnabled: "1", config.VarBurstListen: listen, config.VarBurstPoolSecret: e2eSecret, config.VarBurstKinds: "document",
	}
	cfg, err := config.LoadFrom(func(k string) (string, bool) { v, ok := env[k]; return v, ok })
	if err != nil {
		t.Fatal(err)
	}
	org := seedOrg(t, d, "Burst")
	user := seedUser(t, d, org)
	me := apix.Principal{OrgID: org, UserID: &user, Plan: "pro"}
	sys, err := wire.New(ctx, wire.Options{Config: cfg, Logger: logx.Discard(), Pool: d.App, Auth: func(*http.Request) (apix.Principal, error) { return me, nil }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sys.Close)
	sys.Jobs.Set(&jobargs.Recorder{})
	serveCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	go func() { _ = sys.ServeBurst(serveCtx) }()
	if _, err := sys.Credits.Grant(ctx, org, credits.GrantParams{Kind: credits.GrantPlan, Amount: 5000, Source: "test"}); err != nil {
		t.Fatal(err)
	}
	twin := filepath.Join(repoRoot(), "testdata", "golden", "vantara-twin")
	entries, err := os.ReadDir(twin)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		if ext := strings.ToLower(filepath.Ext(e.Name())); ext == ".csv" || ext == ".txt" {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	mb := &mainBox{sys: sys, me: me, apiURL: "https://" + listen}
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(twin, name))
		if err != nil {
			t.Fatal(err)
		}
		id, _ := uuid.NewV7()
		key := "quarantine/" + id.String()
		if err := sys.Local.Put(ctx, key, bytes.NewReader(data), int64(len(data)), "text/plain"); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		u, err := sys.Store.CreateUpload(ctx, uploads.Upload{
			ID: id, OrgID: org, UserID: &user, Kind: uploads.KindDocument, Store: "local", FileName: name, DeclaredMime: "text/plain",
			SizeBytes: int64(len(data)), SHA256: sum[:], StorageKey: key, CreatedAt: time.Now().UTC(),
		})
		if err != nil {
			t.Fatal(err)
		}
		u.Status, u.DetectedMime = uploads.StatusClean, "text/plain"
		if _, err := sys.Store.UpdateUpload(ctx, u); err != nil {
			t.Fatal(err)
		}
		mb.refs = append(mb.refs, estimate.SourceRef{UploadID: u.PublicID()})
	}
	return mb
}

func (mb *mainBox) createRun(t *testing.T, ctx context.Context) (runs.Run, estimate.Quote) {
	t.Helper()
	req := estimate.Request{Sources: mb.refs}
	q, err := mb.sys.Estimates.Estimate(ctx, mb.me, req, func(u uploads.Upload) string { return mb.sys.Local.URI(u.StorageKey) })
	if err != nil {
		t.Fatal(err)
	}
	run, err := mb.sys.Runs.Create(ctx, mb.me, runs.CreateRequest{Request: req, QuoteID: q.ID, QuoteSignature: q.Signature})
	if err != nil {
		t.Fatal(err)
	}
	return run, q
}

func TestBurstWorkerEndToEnd(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	mb := newMainBox(t, ctx)
	run, q := mb.createRun(t, ctx)
	if q.ExpectedSeconds <= 0 {
		t.Fatalf("orçamento sem tempo: %+v", q)
	}
	worker := burstWorker(t, mb.apiURL, 0, "-imds", "", "-instance-id", "local-e2e", "-once")

	if err := mb.sys.Runs.Execute(ctx, mb.me.OrgID, run.ID, false); err != nil {
		t.Fatalf("execute: %v\n--- trabalhador:\n%s", err, worker.out)
	}
	done, err := mb.sys.Runs.Get(ctx, mb.me.OrgID, run.ID)
	if err != nil || done.Status != runs.StatusCompleted || done.Summary == nil || done.Summary.FindingsTotal != 23 {
		t.Fatalf("run = %s %+v %v\n--- trabalhador:\n%s", done.Status, done.Summary, err, worker.out)
	}
	findings, _ := mb.sys.Store.ListAllFindings(ctx, mb.me.OrgID, run.ID)
	if len(findings) != 23 {
		t.Fatalf("achados gravados = %d", len(findings))
	}
	if tm := done.Timing(); tm.Placement != estimate.PlacementBurst || !strings.HasPrefix(tm.Reason, estimate.ReasonKind) {
		t.Fatalf("a execução deveria ter rodado no pool: %+v", tm)
	}
	acct, _ := mb.sys.Credits.Balance(ctx, mb.me.OrgID)
	if acct.Held != 0 || done.CreditsSpent <= 0 || acct.Balance != 5000-done.CreditsSpent {
		t.Fatalf("liquidação: %+v, gastos %d", acct, done.CreditsSpent)
	}
	// The worker exits after its job (-once), having reported it done.
	waitExit(t, worker)
	status, err := mb.sys.Pool.Store.Status(ctx, mb.sys.Pool.Settings.Query(time.Now()))
	if err != nil || status.Queued != 0 || status.Busy != 0 || status.UsedSeconds <= 0 {
		t.Fatalf("pool depois: %+v %v", status, err)
	}
	if strings.Contains(worker.out.String(), "DATABASE_URL") {
		t.Fatal("o trabalhador não deveria nem mencionar o banco")
	}
}

func waitExit(t *testing.T, p *proc) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- p.cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("trabalhador saiu com erro: %v\n%s", err, p.out)
		}
	case <-time.After(30 * time.Second):
		t.Fatalf("trabalhador não terminou\n%s", p.out)
	}
}

// spotIMDS is the metadata service of a Spot instance that gets its
// two-minute notice after armAt polls.
type spotIMDS struct {
	polls atomic.Int32
	armAt int32
}

func (s *spotIMDS) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPut {
		_, _ = io.WriteString(w, "tok")
		return
	}
	switch strings.TrimPrefix(r.URL.Path, "/latest/meta-data/") {
	case "instance-id":
		_, _ = io.WriteString(w, "i-0e2espot")
	case "instance-type":
		_, _ = io.WriteString(w, "m7g.medium")
	case "instance-life-cycle":
		_, _ = io.WriteString(w, "spot")
	case "spot/instance-action":
		if s.polls.Add(1) >= s.armAt {
			_, _ = io.WriteString(w, `{"action":"terminate","time":"2026-11-05T12:02:00Z"}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func TestBurstSpotInterruptionRequeuesEndToEnd(t *testing.T) {
	// Both workers must replay the same event sequence. This fixture deliberately slows the
	// fake harness to inject a notice mid-run; a real replacement cannot resume its fake cursor.
	// TestBurstEndToEnd and TestPipelineWithRealHarness separately exercise the real processor.
	t.Setenv("DM_HARNESS_BIN", "")
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	mb := newMainBox(t, ctx)
	run, _ := mb.createRun(t, ctx)

	// Worker A is a Spot instance (fake metadata) whose notice arrives mid-run;
	// its harness emits an event every 60 ms, the notice is polled every 200 ms.
	md := httptest.NewServer(&spotIMDS{armAt: 8})
	t.Cleanup(md.Close)
	a := burstWorker(t, mb.apiURL, 60*time.Millisecond, "-imds", md.URL, "-notice-every", "200ms")
	err := mb.sys.Runs.Execute(ctx, mb.me.OrgID, run.ID, false)
	if !errors.Is(err, runs.ErrRequeue) {
		t.Fatalf("a interrupção deveria devolver a execução à fila: %v\n--- A:\n%s", err, a.out)
	}
	mid, _ := mb.sys.Runs.Get(ctx, mb.me.OrgID, run.ID)
	if mid.Status != runs.StatusRunning || mid.LastSeq == 0 || mid.Timing().Interruptions != 1 {
		t.Fatalf("depois da interrupção: %s seq=%d %+v\n--- A:\n%s", mid.Status, mid.LastSeq, mid.Timing(), a.out)
	}
	if !strings.Contains(a.out.String(), "interrupção Spot") {
		t.Fatalf("A deveria ter ouvido o aviso:\n%s", a.out)
	}

	// Worker B (another instance, a harness that never saw the run) takes the
	// requeued run from its checkpoint; A, draining, takes nothing.
	b := burstWorker(t, mb.apiURL, 0, "-imds", "", "-instance-id", "local-b", "-once")
	if err := mb.sys.Runs.Execute(ctx, mb.me.OrgID, run.ID, false); err != nil {
		t.Fatalf("retomada: %v\n--- B:\n%s", err, b.out)
	}
	done, _ := mb.sys.Runs.Get(ctx, mb.me.OrgID, run.ID)
	findings, _ := mb.sys.Store.ListAllFindings(ctx, mb.me.OrgID, run.ID)
	if done.Status != runs.StatusCompleted || len(findings) != 23 || done.Summary.FindingsTotal != 23 {
		t.Fatalf("retomada: %s, %d achados\n--- B:\n%s", done.Status, len(findings), b.out)
	}
	waitExit(t, b)
	if strings.Contains(a.out.String(), "trabalho iniciado no harness local") && strings.Count(a.out.String(), "trabalho iniciado no harness local") != 1 {
		t.Fatalf("A, em encerramento, pegou outro trabalho:\n%s", a.out)
	}
}
