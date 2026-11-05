package text

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// Keys built at run time ("steps." + id): every expansion is listed here.
var dynamic = map[string][]string{
	"cli.demo_phase_": {"login", "upload", "estimate", "run", "findings"},
	"mode.":           {"try_name", "try_text", "dev_name", "dev_text"},
	"ports.":          {"web", "api", "postgres", "mailpit"},
	"steps.":          {"prepare", "base_images", "images", "database", "migrate", "services", "dev_clone", "dev_setup", "dev_local", "verify", "demo"},
	"steps.clone_":    {"objects", "deltas", "files"},
	"nav.":            {"mode", "checks", "tools", "choices", "install", "done"},
	"tools.names.":    {"git", "make", "cc", "go", "rust", "node", "pnpm", "docker", "openssl", "curl", "wsl"},
	"tools.fix.":      {"make", "cc", "go", "rust", "node", "pnpm", "openssl", "curl"},
}

var call = regexp.MustCompile(`text\.(?:T|L|Has)\("([a-z0-9_.]+)"`)

// usedKeys scans the module's Go sources for text.T/L calls.
func usedKeys(t *testing.T) map[string]bool {
	t.Helper()
	used := map[string]bool{}
	root := filepath.Join("..", "..")
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == "testdata" || d.Name() == "dist") {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for _, m := range call.FindAllStringSubmatch(string(data), -1) {
			key := m[1]
			if exp, ok := dynamic[key]; ok {
				for _, e := range exp {
					used[key+e] = true
				}
				continue
			}
			used[key] = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// nav.* is reached through a table of screens; mode.* names through modeKey.
	for prefix, exp := range dynamic {
		for _, e := range exp {
			used[prefix+e] = true
		}
	}
	return used
}

func TestSectionLoads(t *testing.T) {
	if err := Err(); err != nil {
		t.Fatal(err)
	}
	if T("tagline") != "concilie qualquer documento · instalação local, sem chave" {
		t.Fatal(T("tagline"))
	}
}

func TestEveryUsedKeyExists(t *testing.T) {
	var missing []string
	for key := range usedKeys(t) {
		if !Has(key) {
			missing = append(missing, key)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("chaves usadas que não existem em installer.*: %v", missing)
	}
}

func TestNoUnusedKeys(t *testing.T) {
	used := usedKeys(t)
	var unused []string
	for _, key := range Keys() {
		if !used[key] {
			unused = append(unused, key)
		}
	}
	if len(unused) > 0 {
		t.Fatalf("chaves de installer.* sem uso (apague ou use): %v", unused)
	}
}

func TestFill(t *testing.T) {
	if got := T("preflight.port", "port", 4321, "name", "site"); got != "Porta 4321 (site)" {
		t.Fatal(got)
	}
	if got := T("nao.existe"); got != "[nao.existe]" {
		t.Fatal(got)
	}
	help := L("cli.help")
	if len(help) < 10 || !strings.HasPrefix(help[0], "uso: docmatch") {
		t.Fatalf("%v", help)
	}
}
