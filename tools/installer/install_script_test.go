package main

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

var urlWithHost = regexp.MustCompile(`https?://[A-Za-z0-9]`)

// serveRelease serves a fake release: SHA256SUMS.txt and a "binary" (a shell script that prints
// its arguments). tamper changes the binary after its checksum was written.
func serveRelease(t *testing.T, tamper bool) string {
	t.Helper()
	name := "docmatch-" + runtime.GOOS + "-" + runtime.GOARCH + ".bin"
	bin := []byte("#!/bin/sh\necho \"docmatch falso: $*\"\n")
	sum := sha256.Sum256(bin)
	sums := hex.EncodeToString(sum[:]) + "  " + name + "\n" + strings.Repeat("0", 64) + "  docmatch-outro-amd64.bin\n"
	if tamper {
		bin = append(bin, []byte("# alterado\n")...)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/install/SHA256SUMS.txt", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(sums)) })
	mux.HandleFunc("/install/"+name, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(bin) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL + "/install"
}

func runScript(t *testing.T, env ...string) (string, error) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("install.sh é para Linux e macOS")
	}
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sem sh")
	}
	cmd := exec.Command("sh", "install.sh", "--plain")
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestInstallScriptVerifiesAndInstalls(t *testing.T) {
	base := serveRelease(t, false)
	binDir := filepath.Join(t.TempDir(), "bin")
	out, err := runScript(t, "DOCMATCH_INSTALL_BASE="+base, "DOCMATCH_BIN_DIR="+binDir)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "checksum SHA-256 conferido") || !strings.Contains(out, "docmatch falso: --plain --plain") {
		t.Fatalf("saída:\n%s", out)
	}
	info, err := os.Stat(filepath.Join(binDir, "docmatch"))
	if err != nil || info.Mode().Perm()&0o100 == 0 {
		t.Fatalf("binário não instalado: %v", err)
	}
}

func TestInstallScriptRefusesTamperedBinary(t *testing.T) {
	base := serveRelease(t, true)
	binDir := filepath.Join(t.TempDir(), "bin")
	out, err := runScript(t, "DOCMATCH_INSTALL_BASE="+base, "DOCMATCH_BIN_DIR="+binDir, "DOCMATCH_NO_RUN=1")
	if err == nil || !strings.Contains(out, "checksum não confere") {
		t.Fatalf("devia recusar:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(binDir, "docmatch")); err == nil {
		t.Fatal("nada pode ser instalado com checksum errado")
	}
}

func TestInstallScriptNeedsABase(t *testing.T) {
	out, err := runScript(t, "DOCMATCH_INSTALL_BASE=")
	if err == nil || !strings.Contains(out, "DOCMATCH_INSTALL_BASE") {
		t.Fatalf("sem base devia falhar:\n%s", out)
	}
}

func TestScriptsKeepThePlaceholder(t *testing.T) {
	for _, f := range []string{"install.sh", "install.ps1"} {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), "__DOCMATCH_INSTALL_BASE__") || urlWithHost.Match(data) {
			t.Errorf("%s: a base fica só no placeholder (o release.sh a grava)", f)
		}
	}
}
