package main

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

const sample = `mode: atomic
github.com/mmtmn/docmatch/services/api/internal/a/a.go:1.1,2.2 2 1
github.com/mmtmn/docmatch/services/api/internal/a/a.go:3.1,4.2 2 0
github.com/mmtmn/docmatch/services/api/internal/a/a.go:3.1,4.2 2 1
github.com/mmtmn/docmatch/services/api/internal/b/b.go:1.1,2.2 10 0
github.com/mmtmn/docmatch/services/api/internal/gen/g.go:1.1,2.2 100 0
github.com/mmtmn/docmatch/services/api/cmd/x/main.go:1.1,2.2 5 0
`

func writeProfile(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "cover.out")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestParseMergesDuplicateBlocks(t *testing.T) {
	t.Parallel()
	per, err := parse(writeProfile(t, sample))
	if err != nil {
		t.Fatal(err)
	}
	// Block 3.1,4.2 appears twice (once uncovered, once covered): counts once, as covered.
	if a := per["internal/a"]; a.statements != 4 || a.covered != 4 {
		t.Fatalf("internal/a = %+v", a)
	}
	if b := per["internal/b"]; b.statements != 10 || b.covered != 0 {
		t.Fatalf("internal/b = %+v", b)
	}
	if _, ok := per["cmd/x"]; !ok {
		t.Fatal("cmd/x deveria estar no mapa (ignorado só no relatório)")
	}
}

func TestParseErrors(t *testing.T) {
	t.Parallel()
	if _, err := parse(filepath.Join(t.TempDir(), "nao-existe")); err == nil {
		t.Fatal("arquivo ausente")
	}
	for _, bad := range []string{"mode: set\nsem-espaco\n", "mode: set\nx:1.1,2.2 a b\n", "mode: set\nx:1.1,2.2 1\n", "mode: set\nnoColon 1 1\n"} {
		if _, err := parse(writeProfile(t, bad)); err == nil {
			t.Fatalf("perfil inválido deveria falhar: %q", bad)
		}
	}
}

func TestReportFloors(t *testing.T) {
	t.Parallel()
	per, err := parse(writeProfile(t, sample))
	if err != nil {
		t.Fatal(err)
	}
	devnull := io.Discard
	// a=100%, b=0%, gen excluded: overall over a+b = 4/14 = 28.6%.
	if code := report(devnull, per, 25, []string{"internal/gen"}, nil); code != 0 {
		t.Fatal("28.6% deveria passar o piso de 25%")
	}
	if code := report(devnull, per, 30, []string{"internal/gen"}, nil); code != 1 {
		t.Fatal("28.6% deveria falhar o piso de 30%")
	}
	if code := report(devnull, per, 1, []string{"internal/gen/"}, map[string]float64{"internal/a": 95}); code != 0 {
		t.Fatal("piso por pacote satisfeito")
	}
	if code := report(devnull, per, 1, []string{"internal/gen"}, map[string]float64{"internal/b": 50}); code != 1 {
		t.Fatal("piso por pacote violado")
	}
	if code := report(devnull, per, 1, []string{"internal/gen"}, map[string]float64{"internal/missing": 50}); code != 1 {
		t.Fatal("pacote ausente do perfil viola o piso")
	}
	// Without the exclusion the generated package drags the total under any floor.
	if code := report(devnull, per, 10, nil, nil); code != 1 {
		t.Fatal("sem exclusão o total cai")
	}
	var s stats
	if s.percent() != 100 {
		t.Fatal("pacote sem instruções conta como 100%")
	}
	var m multiFlag
	_ = m.Set("x")
	_ = m.Set("y")
	if m.String() != "x,y" {
		t.Fatal("multiFlag")
	}
}
