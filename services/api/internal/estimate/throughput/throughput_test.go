package throughput

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// repoFile finds config/throughput.yaml from the package directory.
func repoFile(t *testing.T) string {
	t.Helper()
	dir, _ := os.Getwd()
	for i := 0; i < 8; i++ {
		p := filepath.Join(dir, "config", "throughput.yaml")
		if _, err := os.Stat(p); err == nil {
			return p
		}
		dir = filepath.Dir(dir)
	}
	t.Skip("config/throughput.yaml não encontrado")
	return ""
}

func TestLoadRepoFile(t *testing.T) {
	t.Parallel()
	c, err := Load(repoFile(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range Kinds() {
		if _, ok := c.Kinds[k]; !ok {
			t.Errorf("tipo %s ausente", k)
		}
	}
	if c.Default(KindTable) <= 0 || c.Default("desconhecido") != 0 {
		t.Fatalf("padrões: table=%v desconhecido=%v", c.Default(KindTable), c.Default("desconhecido"))
	}
	if c.Window() != time.Duration(c.History.WindowDays)*24*time.Hour {
		t.Fatalf("janela %v", c.Window())
	}
	if c.Large(c.LargeAfterSeconds) || !c.Large(c.LargeAfterSeconds+1) {
		t.Fatal("large é estritamente acima do limite")
	}
}

func TestLoadMissingFile(t *testing.T) {
	t.Parallel()
	_, err := Load(filepath.Join(t.TempDir(), "nao-existe.yaml"))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("arquivo ausente deveria ser fs.ErrNotExist: %v", err)
	}
}

const valid = `
overhead_seconds: 10
large_after_seconds: 600
notify_after_seconds: 600
history: { window_days: 30, max_runs: 100, min_runs: 3 }
queue: { parallel_runs: 2, default_run_seconds: 60 }
kinds:
  table: { unit: mb, seconds_per_unit: 3 }
  document: { unit: mb, seconds_per_unit: 20 }
  email: { unit: mb, seconds_per_unit: 6 }
  archive: { unit: mb, seconds_per_unit: 15 }
  audio: { unit: minutes, seconds_per_unit: 12, mb_per_minute: 1 }
  video: { unit: minutes, seconds_per_unit: 30, mb_per_minute: 20 }
  llm: { unit: ktokens, seconds_per_unit: 1.5 }
  new_tools: { unit: tools, seconds_per_unit: 240 }
  web_requests: { unit: requests, seconds_per_unit: 4 }
`

func TestParseValidation(t *testing.T) {
	t.Parallel()
	if _, err := Parse([]byte(valid)); err != nil {
		t.Fatalf("válido: %v", err)
	}
	cases := map[string]struct{ from, to, want string }{
		"yaml":          {"overhead_seconds: 10", "overhead_seconds: [", "yaml inválido"},
		"overhead":      {"overhead_seconds: 10", "overhead_seconds: -1", "overhead_seconds"},
		"large":         {"large_after_seconds: 600", "large_after_seconds: 0", "large_after_seconds"},
		"notify":        {"notify_after_seconds: 600", "notify_after_seconds: 0", "notify_after_seconds"},
		"history":       {"min_runs: 3", "min_runs: 0", "history"},
		"parallel":      {"parallel_runs: 2", "parallel_runs: 0", "parallel_runs"},
		"default run":   {"default_run_seconds: 60", "default_run_seconds: -5", "default_run_seconds"},
		"missing kind":  {"  web_requests: { unit: requests, seconds_per_unit: 4 }\n", "", "kinds.web_requests ausente"},
		"wrong unit":    {"table: { unit: mb,", "table: { unit: rows,", "kinds.table.unit"},
		"negative rate": {"seconds_per_unit: 20 }", "seconds_per_unit: -20 }", "kinds.document.seconds_per_unit"},
		"media size":    {"mb_per_minute: 20", "mb_per_minute: 0", "kinds.video.mb_per_minute"},
		"unknown kind":  {"kinds:\n", "kinds:\n  pages: { unit: mb, seconds_per_unit: 1 }\n", "kinds.pages desconhecido"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			doc := strings.Replace(valid, c.from, c.to, 1)
			if doc == valid {
				t.Fatalf("substituição %q não aconteceu", c.from)
			}
			_, err := Parse([]byte(doc))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("erro = %v, esperado conter %q", err, c.want)
			}
		})
	}
}
