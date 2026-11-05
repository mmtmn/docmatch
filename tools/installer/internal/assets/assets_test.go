package assets

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestTwin(t *testing.T) {
	files, err := Twin()
	if err != nil || len(files) != 4 {
		t.Fatalf("%d %v", len(files), err)
	}
	for _, f := range files {
		if len(f.Data) == 0 || f.MIME == "" {
			t.Errorf("%s vazio", f.Name)
		}
	}
	if n := ExpectedFindings(); n != 23 {
		t.Fatalf("o gabarito tem 23 achados, contei %d", n)
	}
}

func TestEmbeddedJSON(t *testing.T) {
	var v map[string]any
	if json.Unmarshal(LocalesPTBR, &v) != nil || v["installer"] == nil {
		t.Fatal("pt-BR.json sem installer")
	}
	if json.Unmarshal(Tokens, &v) != nil || v["color"] == nil {
		t.Fatal("tokens.json")
	}
	if !bytes.Contains(Compose, []byte("DM_DEV_LOGIN")) {
		t.Fatal("compose.yaml")
	}
}

// The copies match the canonical files (the same check as `make sync-check`).
func TestCopiesAreCurrent(t *testing.T) {
	root := filepath.Join("..", "..", "..", "..")
	pairs := map[string][]byte{
		"packages/locales/pt-BR.json":        LocalesPTBR,
		"deploy/local/compose.yaml":          Compose,
		"packages/design-tokens/tokens.json": Tokens,
	}
	for path, embedded := range pairs {
		canonical, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
		if err != nil {
			t.Skip("fora do repositório")
		}
		if !bytes.Equal(canonical, embedded) {
			t.Errorf("%s divergiu da cópia embarcada: rode make -C tools/installer sync", path)
		}
	}
	files, _ := Twin()
	for _, f := range files {
		canonical, err := os.ReadFile(filepath.Join(root, "testdata", "golden", "vantara-twin", f.Name))
		if err != nil || !bytes.Equal(canonical, f.Data) {
			t.Errorf("%s divergiu do gêmeo", f.Name)
		}
	}
}
