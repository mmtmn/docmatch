package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestConnectionPrivateAndValidated(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "profile")
	want := connection{API: "https://api.example.invalid", Key: "fixture-secret"}
	if err := saveConnection(dir, want); err != nil {
		t.Fatal(err)
	}
	got, err := loadConnection(dir)
	if err != nil || got != want {
		t.Fatalf("connection round trip: %v", err)
	}
	if runtime.GOOS != "windows" {
		st, err := os.Stat(filepath.Join(dir, "connection.json"))
		if err != nil || st.Mode().Perm() != 0o600 {
			t.Fatal("private file permissions", err)
		}
	}
	for _, bad := range []connection{{API: "http://remote.example.invalid"}, {API: "https://user:pass@example.invalid"}, {API: want.API, Key: "injected\nheader"}} {
		if saveConnection(dir, bad) == nil {
			t.Fatal("unsafe profile accepted")
		}
	}
	want.Key = "replacement-fixture"
	if err := saveConnection(dir, want); err != nil {
		t.Fatal(err)
	}
	got, err = loadConnection(dir)
	if err != nil || got != want {
		t.Fatal("replacement failed", err)
	}
}

func TestHomeMenuExitAndConfigRequiresTerminal(t *testing.T) {
	w, _, _ := testWorld(t, map[string]string{"NO_COLOR": "1"})
	w.stdin = strings.NewReader("7\n")
	if code, err := runHome(t.Context(), w, Build{}); code != 0 || err != nil {
		t.Fatal(code, err)
	}
	w.isTTY = false
	if code, _ := runConnection(t.Context(), w, nil); code != exitUsage {
		t.Fatal(code)
	}
}
