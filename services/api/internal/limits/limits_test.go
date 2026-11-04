package limits

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEmbeddedMatchesContract(t *testing.T) {
	t.Parallel()
	canonical := filepath.Join("..", "..", "..", "..", "packages", "contracts", "limits.yaml")
	want, err := os.ReadFile(canonical)
	if err != nil {
		t.Skipf("arquivo canônico indisponível (%v); o módulo foi testado fora do monorepo", err)
	}
	if !bytes.Equal(want, Embedded()) {
		t.Fatalf("internal/limits/limits.yaml divergiu de packages/contracts/limits.yaml; rode `make generate` em services/api")
	}
}

func TestLoadEmbedded(t *testing.T) {
	t.Parallel()
	l, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if l.Version != 1 {
		t.Fatalf("version = %d", l.Version)
	}
	if got, want := l.HTTP.ReadHeaderTimeout(), 5*time.Second; got != want {
		t.Fatalf("ReadHeaderTimeout = %v, esperado %v", got, want)
	}
	if got, want := l.HTTP.RequestTimeout(), 30*time.Second; got != want {
		t.Fatalf("RequestTimeout = %v, esperado %v", got, want)
	}
	if got, want := l.HTTP.SSEIdleTimeout(), 300*time.Second; got != want {
		t.Fatalf("SSEIdleTimeout = %v, esperado %v", got, want)
	}
	if got, want := l.HTTP.JSONBodyMax(), int64(256*1024); got != want {
		t.Fatalf("JSONBodyMax = %d, esperado %d", got, want)
	}
	if got, want := l.HTTP.HeaderMax(), 16*1024; got != want {
		t.Fatalf("HeaderMax = %d, esperado %d", got, want)
	}
	if got, want := l.Files.DocumentFileMax(), int64(50)<<20; got != want {
		t.Fatalf("DocumentFileMax = %d, esperado %d", got, want)
	}
	if got, want := l.Files.DocumentRunMax(), int64(500)<<20; got != want {
		t.Fatalf("DocumentRunMax = %d, esperado %d", got, want)
	}
	if got, want := l.Files.MediaFileMax(), int64(4)<<30; got != want {
		t.Fatalf("MediaFileMax = %d, esperado %d", got, want)
	}
	if l.Text.BlogComment != 0 {
		t.Fatalf("blog_comment = %d, esperado 0 (comentários desativados)", l.Text.BlogComment)
	}
	if l.Text.Email != 254 || l.IDs.IdempotencyKeyMax != 128 {
		t.Fatalf("limites de texto/ids inesperados: %+v %+v", l.Text, l.IDs)
	}
	if Must() != l {
		t.Fatal("Must deveria devolver a mesma instância cacheada")
	}
}

func TestParseRejectsBadDocuments(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		yaml string
		want string
	}{
		{"yaml inválido", "version: [", "limits.yaml"},
		{"sem versão", "text:\n  email: 1\n", "version"},
		{"limite ausente", "version: 1\ntext:\n  email: 254\n", "ausentes"},
		{"valor negativo", strings.Replace(string(Embedded()), "email: 254", "email: -1", 1), "text.email"},
		{"senha mínima maior", strings.Replace(string(Embedded()), "password_min: 12", "password_min: 200", 1), "password_min"},
		{"blog negativo", strings.Replace(string(Embedded()), "blog_comment: 0", "blog_comment: -3", 1), "blog_comment"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := Parse([]byte(tc.yaml))
			if err == nil {
				t.Fatal("esperava erro")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("erro %q não menciona %q", err, tc.want)
			}
		})
	}
}

func TestParseToleratesUnknownKeys(t *testing.T) {
	t.Parallel()
	doc := string(Embedded()) + "\nfuture:\n  something: 1\n"
	if _, err := Parse([]byte(doc)); err != nil {
		t.Fatalf("chaves desconhecidas deveriam ser toleradas: %v", err)
	}
}

func TestMustPanicsOnError(t *testing.T) {
	t.Parallel()
	defer func() {
		if recover() == nil {
			t.Fatal("must deveria entrar em pânico com erro")
		}
	}()
	must(nil, errors.New("quebrado"))
}
