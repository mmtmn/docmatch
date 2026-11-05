package envfile

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/mmtmn/docmatch/tools/installer/internal/ports"
)

func settings() Settings {
	return Settings{Ports: ports.Set{ports.Web: 14321, ports.API: 18080, ports.Postgres: 15432, ports.Mailpit: 18025},
		Registry: "ghcr.io/owner", Tag: "0.1.0", SourceDir: "/src", Revision: "abc123"}
}

func TestValuesLLMModes(t *testing.T) {
	s := settings()
	v := s.Values()
	if v["DM_LLM_MODE"] != LLMOff || v["DM_LOCAL_LLM_BASE_URL"] != "" || v["OPENROUTER_API_KEY"] != "" {
		t.Fatalf("off: %v", v)
	}
	if v["DM_WEB_PORT"] != "14321" || v["DM_API_PORT"] != "18080" || v["DM_PG_PORT"] != "15432" || v["DM_MAILPIT_PORT"] != "18025" {
		t.Fatalf("portas: %v", v)
	}
	s.LLMMode, s.LocalLLM, s.OpenRouter = LLMLocal, "http://localhost:11434/v1", "sk-ignorada"
	v = s.Values()
	if v["DM_LOCAL_LLM_BASE_URL"] != "http://host.docker.internal:11434/v1" || v["OPENROUTER_API_KEY"] != "" {
		t.Fatalf("local: %v", v)
	}
	s.LLMMode = LLMOpenRouter
	v = s.Values()
	if v["OPENROUTER_API_KEY"] != "sk-ignorada" || v["DM_LOCAL_LLM_BASE_URL"] != "" {
		t.Fatalf("openrouter: %v", v)
	}
}

func TestContainerURL(t *testing.T) {
	cases := map[string]string{
		"http://localhost:11434/v1":    "http://host.docker.internal:11434/v1",
		"http://127.0.0.1:8000/v1":     "http://host.docker.internal:8000/v1",
		"http://[::1]:8000/v1":         "http://host.docker.internal:8000/v1",
		"http://localhost/v1":          "http://host.docker.internal/v1",
		"http://gpu.example.com:80/v1": "http://gpu.example.com:80/v1",
		"  http://localhost:1/x  ":     "http://host.docker.internal:1/x",
	}
	for in, want := range cases {
		if got := ContainerURL(in); got != want {
			t.Errorf("ContainerURL(%q) = %q; quero %q", in, got, want)
		}
	}
	if ValidateLLMURL("http://localhost:11434/v1") != nil || ValidateLLMURL("localhost:11434") == nil || ValidateLLMURL("ftp://x") == nil {
		t.Fatal("ValidateLLMURL")
	}
}

func TestGenerateShapes(t *testing.T) {
	rnd := bytes.NewReader(bytes.Repeat([]byte{7}, 1024))
	session, err := Generate("DM_SESSION_SECRET", rnd)
	if err != nil {
		t.Fatal(err)
	}
	if raw, err := base64.StdEncoding.DecodeString(session); err != nil || len(raw) != 48 {
		t.Fatalf("sessão: %q %v", session, err)
	}
	key, _ := Generate("DM_QUOTE_HMAC_KEY", rnd)
	if raw, err := hex.DecodeString(key); err != nil || len(raw) != 32 {
		t.Fatalf("chave: %q", key)
	}
	pw, _ := Generate("DM_PG_PASSWORD", rnd)
	if len(pw) != 48 || strings.ContainsAny(pw, "@:/?#%") {
		t.Fatalf("senha do Postgres precisa caber na URL: %q", pw)
	}
}

func TestMergeKeepsSecrets(t *testing.T) {
	existing := map[string]string{"DM_SESSION_SECRET": "mantida", "DM_PG_PASSWORD": "senha", "DM_WEB_PORT": "1"}
	rnd := bytes.NewReader(bytes.Repeat([]byte{1}, 1024))
	out, generated, err := Merge(existing, settings(), rnd)
	if err != nil {
		t.Fatal(err)
	}
	if out["DM_SESSION_SECRET"] != "mantida" || out["DM_PG_PASSWORD"] != "senha" {
		t.Fatalf("segredos trocados: %v", out)
	}
	if out["DM_WEB_PORT"] != "14321" {
		t.Fatalf("o resto vem das escolhas: %v", out["DM_WEB_PORT"])
	}
	if strings.Join(generated, ",") != "DM_QUOTE_HMAC_KEY,DM_APIKEY_PEPPER,DM_WEBHOOK_SIGNING_KEY" {
		t.Fatalf("gerados: %v", generated)
	}
	for _, name := range Secrets {
		if out[name] == "" {
			t.Errorf("%s vazio", name)
		}
	}
}

func TestParse(t *testing.T) {
	got := Parse([]byte("# comentário\n\nA=1\nB = \"dois\" \nC='três'\nsem-igual\nD=\n"))
	if got["A"] != "1" || got["B"] != "dois" || got["C"] != "três" || got["D"] != "" || len(got) != 4 {
		t.Fatalf("%v", got)
	}
}

func TestWriteIsStableAndPrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "inst", ".env")
	generated, err := Write(path, settings())
	if err != nil {
		t.Fatal(err)
	}
	if len(generated) != len(Secrets) {
		t.Fatalf("primeira vez gera todos: %v", generated)
	}
	first, _ := Read(path)
	s := settings()
	s.Ports[ports.Web] = 24321
	generated, err = Write(path, s)
	if err != nil || len(generated) != 0 {
		t.Fatalf("segunda vez não gera nada: %v %v", generated, err)
	}
	second, _ := Read(path)
	for _, name := range Secrets {
		if first[name] != second[name] {
			t.Errorf("%s mudou entre execuções", name)
		}
	}
	if second["DM_WEB_PORT"] != "24321" {
		t.Fatalf("porta não atualizada: %v", second["DM_WEB_PORT"])
	}
	if runtime.GOOS != "windows" {
		info, _ := os.Stat(path)
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("permissão %v", info.Mode().Perm())
		}
	}
	data, _ := os.ReadFile(path)
	if !strings.HasPrefix(string(data), "# docmatch local") || strings.Index(string(data), "DM_SESSION_SECRET") < strings.Index(string(data), "DM_WEB_PORT") {
		t.Fatalf("formato inesperado:\n%s", data)
	}
}
