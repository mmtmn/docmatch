package migrations

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func TestEmbeddedMatchesCanonical(t *testing.T) {
	t.Parallel()
	canonical := filepath.Join("..", "..", "..", "..", "..", "db", "migrations")
	entries, err := os.ReadDir(canonical)
	if err != nil {
		t.Skipf("db/migrations indisponível (%v); módulo testado fora do monorepo", err)
	}
	seen := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		want, err := os.ReadFile(filepath.Join(canonical, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		got, err := fs.ReadFile(FS(), e.Name())
		if err != nil {
			t.Fatalf("%s existe em db/migrations mas não na cópia embarcada; rode `make generate` em services/api", e.Name())
		}
		if !bytes.Equal(want, got) {
			t.Fatalf("%s divergiu entre db/migrations e a cópia embarcada; rode `make generate` em services/api", e.Name())
		}
		seen[e.Name()] = true
	}
	embeddedEntries, err := fs.ReadDir(FS(), ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range embeddedEntries {
		if !seen[e.Name()] {
			t.Fatalf("%s está na cópia embarcada mas não em db/migrations", e.Name())
		}
	}
}

func TestLoadEmbedded(t *testing.T) {
	t.Parallel()
	ms := MustLoad()
	if len(ms) == 0 || ms[0].Version != 1 || ms[0].Name != "initial" {
		t.Fatalf("migrações embarcadas inesperadas: %+v", ms)
	}
	if len(ms[0].Checksum) != 64 || !strings.Contains(ms[0].Up, "CREATE TABLE orgs") || !strings.Contains(ms[0].Down, "DROP TABLE IF EXISTS orgs") {
		t.Fatal("conteúdo da migração inicial inesperado")
	}
}

func TestLoadParsing(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		files map[string]string
		want  string // error substring; empty means ok
		count int
	}{
		{"par válido", map[string]string{"0001_a.up.sql": "select 1;", "0001_a.down.sql": "select 2;"}, "", 1},
		{"ordem crescente", map[string]string{"0002_b.up.sql": "x", "0002_b.down.sql": "y", "0001_a.up.sql": "x", "0001_a.down.sql": "y"}, "", 2},
		{"ignora não-sql", map[string]string{"README.md": "oi", "0001_a.up.sql": "x", "0001_a.down.sql": "y"}, "", 1},
		{"nome inválido", map[string]string{"a.sql": "x"}, "nome inválido", 0},
		{"versão zero", map[string]string{"0000_a.up.sql": "x", "0000_a.down.sql": "y"}, "versão inválida", 0},
		{"sem down", map[string]string{"0001_a.up.sql": "x"}, "sem script down", 0},
		{"sem up", map[string]string{"0001_a.down.sql": "x"}, "sem script up", 0},
		{"up vazio", map[string]string{"0001_a.up.sql": "  \n", "0001_a.down.sql": "y"}, "sem script up", 0},
		{"nomes divergentes", map[string]string{"0001_a.up.sql": "x", "0001_b.down.sql": "y"}, "nomes diferentes", 0},
		{"lacuna", map[string]string{"0001_a.up.sql": "x", "0001_a.down.sql": "y", "0003_c.up.sql": "x", "0003_c.down.sql": "y"}, "lacuna", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fsys := fstest.MapFS{}
			for name, body := range tc.files {
				fsys[name] = &fstest.MapFile{Data: []byte(body)}
			}
			ms, err := Load(fsys)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("erro inesperado: %v", err)
				}
				if len(ms) != tc.count {
					t.Fatalf("len = %d, esperado %d", len(ms), tc.count)
				}
				for i := 1; i < len(ms); i++ {
					if ms[i].Version <= ms[i-1].Version {
						t.Fatal("ordem não crescente")
					}
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("erro = %v, esperado conter %q", err, tc.want)
			}
		})
	}
}

func TestLoadDuplicateScripts(t *testing.T) {
	t.Parallel()
	// Two different names for the same version and direction are caught as
	// "nomes diferentes"; the duplicate-direction branch needs same names,
	// which a filesystem cannot hold twice, so it stays defensive.
	fsys := fstest.MapFS{"0001_a.up.sql": &fstest.MapFile{Data: []byte("x")}, "0001_a.down.sql": &fstest.MapFile{Data: []byte("y")}, "0001_b.up.sql": &fstest.MapFile{Data: []byte("z")}}
	if _, err := Load(fsys); err == nil {
		t.Fatal("esperava erro")
	}
}
