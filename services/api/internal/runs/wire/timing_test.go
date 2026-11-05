package wire

import (
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/mmtmn/docmatch/services/api/internal/config"
	"github.com/mmtmn/docmatch/services/api/internal/logx"
)

func TestResultsURL(t *testing.T) {
	t.Parallel()
	if ResultsURL(nil) != nil {
		t.Fatal("sem DM_PUBLIC_BASE_URL não há link")
	}
	for base, want := range map[string]string{
		"http://localhost:4321":         "http://localhost:4321/resultados?run=run_01JBXKAH4M0N5P6Q7R8S9T0V1W",
		"https://example.com/":          "https://example.com/resultados?run=run_01JBXKAH4M0N5P6Q7R8S9T0V1W",
		"https://example.com/app#topo":  "https://example.com/app/resultados?run=run_01JBXKAH4M0N5P6Q7R8S9T0V1W",
		"https://example.com/?utm=hero": "https://example.com/resultados?run=run_01JBXKAH4M0N5P6Q7R8S9T0V1W",
	} {
		u, err := url.Parse(base)
		if err != nil {
			t.Fatal(err)
		}
		if got := ResultsURL(u)("run_01JBXKAH4M0N5P6Q7R8S9T0V1W"); got != want {
			t.Errorf("%s → %s, esperado %s", base, got, want)
		}
	}
}

// writeFile writes a file under dir and returns its path.
func writeFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func repoThroughputBytes(t *testing.T) []byte {
	t.Helper()
	dir, _ := os.Getwd()
	for i := 0; i < 8; i++ {
		if data, err := os.ReadFile(filepath.Join(dir, "config", "throughput.yaml")); err == nil {
			return data
		}
		dir = filepath.Dir(dir)
	}
	t.Skip("config/throughput.yaml não encontrado")
	return nil
}

func TestLoadThroughput(t *testing.T) {
	t.Parallel()
	good := string(repoThroughputBytes(t))
	dir := t.TempDir()
	pricing := writeFile(t, dir, "pricing.yaml", "token_usd: 0.01\n")
	log := logx.Discard()

	// Unset: the file next to the pricing table, when it is there.
	cfg := &config.Config{Billing: config.Billing{PricingFile: pricing}}
	if tp, err := loadThroughput(cfg, log); err != nil || tp != nil {
		t.Fatalf("sem arquivo ao lado: %v %v (deveria só desligar o tempo)", tp, err)
	}
	writeFile(t, dir, config.ThroughputFileName, good)
	if tp, err := loadThroughput(cfg, log); err != nil || tp == nil {
		t.Fatalf("ao lado do pricing: %v %v", tp, err)
	}
	// A broken file next to the pricing table is an error, not silence.
	broken := t.TempDir()
	writeFile(t, broken, config.ThroughputFileName, "kinds: [")
	if _, err := loadThroughput(&config.Config{Billing: config.Billing{PricingFile: filepath.Join(broken, "pricing.yaml")}}, log); err == nil {
		t.Fatal("arquivo inválido deveria falhar")
	}

	// Set: it must load.
	explicit := writeFile(t, t.TempDir(), "outro.yaml", good)
	if tp, err := loadThroughput(&config.Config{Billing: config.Billing{PricingFile: pricing, ThroughputFile: explicit}}, log); err != nil || tp == nil {
		t.Fatalf("DM_THROUGHPUT_FILE: %v %v", tp, err)
	}
	if _, err := loadThroughput(&config.Config{Billing: config.Billing{PricingFile: pricing, ThroughputFile: filepath.Join(dir, "nao-existe.yaml")}}, log); err == nil {
		t.Fatal("DM_THROUGHPUT_FILE apontando para nada deveria falhar")
	}
}
