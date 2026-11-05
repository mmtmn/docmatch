package wire

import (
	"context"
	"io"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/mmtmn/docmatch/services/api/internal/config"
	"github.com/mmtmn/docmatch/services/api/internal/estimate/throughput"
	"github.com/mmtmn/docmatch/services/api/internal/logx"
	"github.com/mmtmn/docmatch/services/api/internal/storage"
)

const poolSecret = "segredo-do-pool-com-mais-de-32-caracteres"

func TestPoolIsOffUnlessConfigured(t *testing.T) {
	t.Parallel()
	sys := &System{log: logx.Discard()}
	p, err := newPool(&config.Config{}, sys)
	if err != nil || p != nil {
		t.Fatalf("pool = %v %v", p, err)
	}
	sys.cfg = &config.Config{}
	if sys.BurstServer() != nil {
		t.Fatal("sem pool não há endpoint")
	}
	if err := sys.ServeBurst(context.Background()); err != nil {
		t.Fatal(err)
	}
	if sc, err := sys.BurstScaler(context.Background()); sc != nil || err != nil {
		t.Fatal("sem pool não há escalonador")
	}
	if _, err := newPool(&config.Config{Burst: config.Burst{Enabled: true, PoolSecret: "curto"}}, sys); err == nil {
		t.Fatal("segredo curto deveria falhar")
	}
}

func TestPoolAssembly(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Burst: config.Burst{
			Enabled: true, Listen: "127.0.0.1:0", PoolSecret: config.Secret(poolSecret), ASGName: "docmatch-test-worker",
			MaxInstances: 2, MonthlyHours: 60, ClaimTimeout: 7 * time.Minute,
		},
		Deadline: config.Deadline{Min: time.Hour, Max: 10 * time.Hour, Factor: 2},
		Storage:  config.Storage{AWSRegion: "us-east-1"},
	}
	sys := &System{cfg: cfg, log: logx.Discard(), Throughput: &throughput.Config{LargeAfterSeconds: 900, Burst: throughput.Burst{BootSeconds: 150}}}
	p, err := newPool(cfg, sys)
	if err != nil || p == nil {
		t.Fatalf("pool = %v %v", p, err)
	}
	if p.Settings.MinSeconds != 900 || p.Settings.BootSeconds != 150 || p.Router == nil || p.Dispatcher == nil {
		t.Fatalf("settings = %+v", p.Settings)
	}
	sys.Pool = p
	if srv := sys.BurstServer(); srv == nil || srv.Presign == nil || srv.Open == nil {
		t.Fatal("endpoint interno")
	}
	sc, err := sys.BurstScaler(context.Background())
	if err != nil || sc == nil || sc.ASG == nil {
		t.Fatalf("escalonador = %v %v", sc, err)
	}
	d := deadlines(cfg)
	if d.Min != time.Hour || d.Max != 10*time.Hour || d.Factor != 2 || d.Wait != 7*time.Minute {
		t.Fatalf("prazos = %+v", d)
	}
	cfg.Burst.Enabled = false
	if deadlines(cfg).Wait != 0 {
		t.Fatal("sem pool não há espera")
	}
}

func TestSourcesForTheWorkers(t *testing.T) {
	t.Parallel()
	base, _ := url.Parse("http://localhost:8080")
	local, err := storage.NewLocal(t.TempDir(), "midia", base)
	if err != nil {
		t.Fatal(err)
	}
	if err := local.Put(context.Background(), "quarantine/a", strings.NewReader("abc"), 3, "text/csv"); err != nil {
		t.Fatal(err)
	}
	endpoint, _ := url.Parse("http://127.0.0.1:1")
	s3, err := storage.NewS3(context.Background(), storage.S3Options{Bucket: "uploads", Region: "us-east-1", Endpoint: endpoint, PathStyle: true, AccessKeyID: "ak", SecretAccessKey: "sk"})
	if err != nil {
		t.Fatal(err)
	}
	sys := &System{Stores: storage.Stores{Documents: s3, Media: local}}
	ctx := context.Background()
	u, ok, err := sys.presignSource(ctx, "s3://uploads/quarantine/x", time.Minute)
	if err != nil || !ok || !strings.Contains(u, "X-Amz-Signature=") {
		t.Fatalf("pré-assinada = %q %v %v", u, ok, err)
	}
	if _, ok, _ := sys.presignSource(ctx, "local://midia/quarantine/a", time.Minute); ok {
		t.Fatal("o armazenamento local não assina URLs: o endpoint serve os bytes")
	}
	if _, ok, _ := sys.presignSource(ctx, "s3://outro/x", time.Minute); ok {
		t.Fatal("bucket desconhecido")
	}
	rc, ok, err := sys.openSource(ctx, "local://midia/quarantine/a")
	if err != nil || !ok {
		t.Fatalf("abrir local = %v %v", ok, err)
	}
	body, _ := io.ReadAll(rc)
	_ = rc.Close()
	if string(body) != "abc" {
		t.Fatalf("conteúdo = %q", body)
	}
	if _, ok, err := sys.openSource(ctx, "local://midia/quarantine/sumiu"); ok || err != nil {
		t.Fatalf("inexistente = %v %v", ok, err)
	}
	if _, ok, _ := sys.openSource(ctx, "sem-esquema"); ok {
		t.Fatal("URI inválida")
	}
}
