package logx

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestNewWritesJSONWithUTCTime(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	l := New(&buf, "prod")
	l.Info("olá", "chave", "valor")
	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatalf("saída não é JSON: %s (%v)", buf.String(), err)
	}
	ts, _ := rec["time"].(string)
	if !strings.HasSuffix(ts, "Z") {
		t.Fatalf("time %q não está em UTC", ts)
	}
	if _, err := time.Parse(time.RFC3339Nano, ts); err != nil {
		t.Fatalf("time %q não é RFC3339: %v", ts, err)
	}
	if rec["msg"] != "olá" || rec["chave"] != "valor" {
		t.Fatalf("registro inesperado: %v", rec)
	}
}

func TestLevelByEnv(t *testing.T) {
	t.Parallel()
	var dev, prod bytes.Buffer
	New(&dev, "dev").Debug("debug")
	New(&prod, "prod").Debug("debug")
	if dev.Len() == 0 {
		t.Fatal("dev deveria registrar debug")
	}
	if prod.Len() != 0 {
		t.Fatalf("prod não deveria registrar debug: %s", prod.String())
	}
}

func TestDiscardIsSilent(t *testing.T) {
	t.Parallel()
	l := Discard()
	l.Error("nada")
	if l.Enabled(context.Background(), slog.LevelError) {
		t.Fatal("Discard não deveria habilitar nenhum nível")
	}
}

func TestContextLogger(t *testing.T) {
	t.Parallel()
	fallback := Discard()
	ctx := context.Background()
	if From(ctx, fallback) != fallback {
		t.Fatal("sem logger no contexto, From deveria devolver o fallback")
	}
	if From(ctx, nil) != slog.Default() {
		t.Fatal("sem fallback, From deveria devolver slog.Default")
	}
	l := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	ctx = WithLogger(ctx, l)
	if From(ctx, fallback) != l {
		t.Fatal("From deveria devolver o logger do contexto")
	}
}
