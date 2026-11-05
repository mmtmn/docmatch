package state

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mmtmn/docmatch/tools/installer/internal/ports"
)

func TestSaveLoadAndSteps(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nova")
	if _, found, err := Load(dir); found || err != nil {
		t.Fatalf("pasta vazia: %v %v", found, err)
	}
	s := &State{Mode: ModeTry, Project: "p", Ports: ports.Defaults(), Tag: "dev"}
	if s.LastDone() != "" || s.Done("prepare") {
		t.Fatal("estado novo não tem etapas")
	}
	s.Mark("prepare")
	s.Mark("images")
	s.Mark("prepare")
	if err := s.Save(dir); err != nil {
		t.Fatal(err)
	}
	got, found, err := Load(dir)
	if err != nil || !found {
		t.Fatalf("Load: %v %v", found, err)
	}
	if got.Version != 1 || got.Project != "p" || got.Ports[ports.API] != 8080 || len(got.Completed) != 2 || got.LastDone() != "images" {
		t.Fatalf("%+v", got)
	}
	if got.UpdatedAt.IsZero() {
		t.Fatal("sem data")
	}
}

func TestLoadBrokenAndMissingPorts(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(Path(dir), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Load(dir); err == nil {
		t.Fatal("JSON quebrado devia falhar")
	}
	if err := os.WriteFile(Path(dir), []byte(`{"mode":"desenvolver"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s, found, err := Load(dir)
	if err != nil || !found || s.Ports[ports.Web] != 4321 {
		t.Fatalf("%+v %v %v", s, found, err)
	}
}
