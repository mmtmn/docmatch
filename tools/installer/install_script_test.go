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
	return serveFile(t, "docmatch-"+runtime.GOOS+"-"+runtime.GOARCH+".bin", []byte("#!/bin/sh\necho \"docmatch falso: $*\"\n"), tamper)
}

// serveFile serves SHA256SUMS.txt (with name and another binary) and name with bin under /install.
func serveFile(t *testing.T, name string, bin []byte, tamper bool) string {
	t.Helper()
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

// install.ps1 under PowerShell 7 (pwsh): Windows runs it in CI; anywhere else it runs when pwsh is
// installed, pretending to be an x64 Windows.
func runPS1(t *testing.T, env ...string) (string, error) {
	t.Helper()
	pwsh, err := exec.LookPath("pwsh")
	if err != nil {
		t.Skip("sem pwsh")
	}
	cmd := exec.Command(pwsh, "-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", "install.ps1")
	cmd.Env = append(os.Environ(), env...)
	if runtime.GOOS != "windows" {
		cmd.Env = append(cmd.Env, "PROCESSOR_ARCHITECTURE=AMD64")
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func windowsAsset() string {
	if runtime.GOOS == "windows" && runtime.GOARCH == "arm64" {
		return "docmatch-windows-arm64.exe"
	}
	return "docmatch-windows-amd64.exe"
}

var ansi = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]`)

var fakeExe = []byte("MZ docmatch falso para o install.ps1\n")

func TestInstallPS1VerifiesAndInstalls(t *testing.T) {
	base := serveFile(t, windowsAsset(), fakeExe, false)
	binDir := filepath.Join(t.TempDir(), "bin")
	out, err := runPS1(t, "DOCMATCH_INSTALL_BASE="+base, "DOCMATCH_BIN_DIR="+binDir, "DOCMATCH_NO_RUN=1")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "checksum SHA-256 conferido") {
		t.Fatalf("saída:\n%s", out)
	}
	got, err := os.ReadFile(filepath.Join(binDir, "docmatch.exe"))
	if err != nil || string(got) != string(fakeExe) {
		t.Fatalf("docmatch.exe não instalado como baixado: %v", err)
	}
}

func TestInstallPS1RefusesTamperedBinary(t *testing.T) {
	base := serveFile(t, windowsAsset(), fakeExe, true)
	binDir := filepath.Join(t.TempDir(), "bin")
	out, err := runPS1(t, "DOCMATCH_INSTALL_BASE="+base, "DOCMATCH_BIN_DIR="+binDir, "DOCMATCH_NO_RUN=1")
	// PowerShell wraps and colors the error and the console may re-encode the accents: compare
	// the ASCII words with the escapes, the gutter and the spacing taken out.
	flat := strings.NewReplacer(" ", "", "\n", "", "\r", "", "|", "").Replace(ansi.ReplaceAllString(out, ""))
	if err == nil || !strings.Contains(flat, "nadafoiinstalado") {
		t.Fatalf("devia recusar:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(binDir, "docmatch.exe")); err == nil {
		t.Fatal("nada pode ser instalado com checksum errado")
	}
}

func TestInstallPS1NeedsABase(t *testing.T) {
	out, err := runPS1(t, "DOCMATCH_INSTALL_BASE=", "DOCMATCH_NO_RUN=1")
	if err == nil || !strings.Contains(out, "DOCMATCH_INSTALL_BASE") {
		t.Fatalf("sem base devia falhar:\n%s", out)
	}
}
