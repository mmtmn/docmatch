package cli

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/mmtmn/docmatch/tools/installer/internal/ports"
	"github.com/mmtmn/docmatch/tools/installer/internal/state"
)

// `docmatch up` in Desenvolver runs make local with what the installation used: the go.mod's
// toolchain for an older local Go, and the toolchain folders on PATH (a ~/.cargo/bin the
// installation added, say), not the bare environment of the shell.
func TestUpDevUsesTheDevEnvironment(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("make e go falsos em sh")
	}
	src := checkout(t)
	if err := os.WriteFile(filepath.Join(src, "services", "api", "go.mod"), []byte("module x\n\ngo 1.27.0\n\ntoolchain go1.27.3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	for name, body := range map[string]string{
		"make": "#!/bin/sh\necho \"make $1 GOTOOLCHAIN=$GOTOOLCHAIN\"\necho \"PATH=$PATH\"\n",
		"go":   "#!/bin/sh\necho go1.22.2\n", // `go env GOVERSION` of Ubuntu's Go
	} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil { //nolint:gosec // test helper
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+"/usr/bin:/bin")
	t.Setenv("GOTOOLCHAIN", "")
	w, out, errb := testWorld(t, nil)
	cargo := filepath.Join(w.home, ".cargo", "bin")
	if err := os.MkdirAll(cargo, 0o755); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "inst")
	if err := (&state.State{Mode: state.ModeDev, Project: "docmatch-local", Ports: ports.Defaults(), SourceDir: src}).Save(dir); err != nil {
		t.Fatal(err)
	}
	if code := run(context.Background(), w, []string{"up", "--dir", dir}, Build{}); code != 0 {
		t.Fatalf("%d %s %s", code, out, errb)
	}
	got := out.String()
	if !strings.Contains(got, "make local GOTOOLCHAIN=go1.27.3") || !strings.Contains(got, "PATH="+cargo) {
		t.Fatalf("o make local roda com o ambiente da instalação:\n%s", got)
	}
}
