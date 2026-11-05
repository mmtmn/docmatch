//go:build integration

package wire_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/mmtmn/docmatch/services/api/internal/config"
	"github.com/mmtmn/docmatch/services/api/internal/credits"
	"github.com/mmtmn/docmatch/services/api/internal/estimate"
	"github.com/mmtmn/docmatch/services/api/internal/jobs/jobargs"
	"github.com/mmtmn/docmatch/services/api/internal/logx"
	"github.com/mmtmn/docmatch/services/api/internal/runs"
	"github.com/mmtmn/docmatch/services/api/internal/runs/apix"
	"github.com/mmtmn/docmatch/services/api/internal/runs/wire"
	"github.com/mmtmn/docmatch/services/api/internal/testdb"
	"github.com/mmtmn/docmatch/services/api/internal/uploads"
)

// TestPipelineWithRealHarness runs the API pipeline against the real Rust
// harness (dm-server) instead of the fake: the twin goes in through local
// storage and the run must finish with the 23 findings of the golden case.
// It needs the binary: DM_HARNESS_BIN=<path to dm-server>.
func TestPipelineWithRealHarness(t *testing.T) {
	bin := os.Getenv("DM_HARNESS_BIN")
	if bin == "" {
		t.Skip("DM_HARNESS_BIN não definido (caminho do binário dm-server)")
	}
	d := testdb.Get(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	_, here, _, _ := runtime.Caller(0)
	repo := filepath.Join(filepath.Dir(here), "..", "..", "..", "..", "..")
	twin := filepath.Join(repo, "testdata", "golden", "vantara-twin")

	media := t.TempDir()
	sockDir, err := os.MkdirTemp("/tmp", "dmh")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })
	sock := filepath.Join(sockDir, "h.sock")
	cmd := exec.CommandContext(ctx, bin, "--socket", sock)
	cmd.Env = append(os.Environ(), "DM_MEDIA_LOCAL_DIR="+media)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Signal(os.Interrupt)
		_ = cmd.Wait()
	})
	for i := 0; i < 100; i++ {
		if _, err := os.Stat(sock); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	env := map[string]string{
		config.VarEnv:               "dev",
		config.VarPublicBaseURL:     "http://localhost:4321",
		config.VarAPIBaseURL:        "http://localhost:8080",
		config.VarAPIListen:         "127.0.0.1:18080",
		config.VarAdminListen:       "127.0.0.1:18443",
		config.VarDatabaseURL:       d.URL,
		config.VarSessionSecret:     "c2Vzc2lvbi1zZWNyZXQtZm9yLWludGVncmF0aW9uLXRlc3RzLW9ubHktMDEyMzQ1",
		config.VarQuoteHMACKey:      strings.Repeat("q", 32),
		config.VarAPIKeyPepper:      strings.Repeat("p", 32),
		config.VarWebhookSigningKey: strings.Repeat("w", 32),
		config.VarPricingFile:       "config/pricing.yaml",
		config.VarHarnessSocket:     sock,
		config.VarMediaLocalDir:     media,
		config.VarLLMMode:           "off",
	}
	cfg, err := config.LoadFrom(func(k string) (string, bool) { v, ok := env[k]; return v, ok })
	if err != nil {
		t.Fatal(err)
	}
	org := seedOrg(t, d, "Real")
	user := seedUser(t, d, org)
	me := apix.Principal{OrgID: org, UserID: &user, Plan: "pro"}
	sys, err := wire.New(ctx, wire.Options{Config: cfg, Logger: logx.Discard(), Pool: d.App, Auth: func(*http.Request) (apix.Principal, error) { return me, nil }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sys.Close)
	sys.Jobs.Set(&jobargs.Recorder{})
	if _, err := sys.Credits.Grant(ctx, org, credits.GrantParams{Kind: credits.GrantPlan, Amount: 5000, Source: "test"}); err != nil {
		t.Fatal(err)
	}

	// The twin, stored as clean uploads in local storage (what a finished scan leaves).
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
	refs := make([]estimate.SourceRef, 0, len(names))
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
			ID: id, OrgID: org, UserID: &user, Kind: uploads.KindDocument, Store: "local", Role: "",
			FileName: name, DeclaredMime: "text/plain", SizeBytes: int64(len(data)), SHA256: sum[:],
			StorageKey: key, CreatedAt: time.Now().UTC(),
		})
		if err != nil {
			t.Fatal(err)
		}
		u.Status, u.DetectedMime = uploads.StatusClean, "text/plain"
		if _, err := sys.Store.UpdateUpload(ctx, u); err != nil {
			t.Fatal(err)
		}
		refs = append(refs, estimate.SourceRef{UploadID: u.PublicID()})
	}

	req := estimate.Request{Sources: refs}
	q, err := sys.Estimates.Estimate(ctx, me, req, func(u uploads.Upload) string { return sys.Local.URI(u.StorageKey) })
	if err != nil {
		t.Fatalf("estimate: %v (%s)", err, stderr.String())
	}
	run, err := sys.Runs.Create(ctx, me, runs.CreateRequest{Request: req, QuoteID: q.ID, QuoteSignature: q.Signature})
	if err != nil {
		t.Fatal(err)
	}
	if err := sys.Runs.Execute(ctx, org, run.ID, false); err != nil {
		t.Fatalf("execute: %v (%s)", err, stderr.String())
	}
	done, err := sys.Runs.Get(ctx, org, run.ID)
	if err != nil || done.Status != runs.StatusCompleted || done.Summary == nil {
		t.Fatalf("run = %+v %v (%s)", done, err, stderr.String())
	}
	if done.Summary.FindingsTotal != 23 {
		t.Fatalf("achados = %d, quer 23", done.Summary.FindingsTotal)
	}
	findings, err := sys.Store.ListAllFindings(ctx, org, run.ID)
	if err != nil || len(findings) != 23 {
		t.Fatalf("achados gravados: %d %v", len(findings), err)
	}
	var partial *runs.Finding
	for i := range findings {
		if findings[i].Code == "recon.partial_payment" {
			partial = &findings[i]
		}
	}
	if partial == nil || partial.AmountAtRisk != 4_900_000 {
		t.Fatalf("pagamento parcial: %+v", partial)
	}
	acct, err := sys.Credits.Balance(ctx, org)
	if err != nil || acct.Held != 0 || acct.Balance != 5000-done.CreditsSpent || done.CreditsSpent <= 0 {
		t.Fatalf("liquidação: conta %+v, gastos %d, %v", acct, done.CreditsSpent, err)
	}
}
