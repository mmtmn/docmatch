package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/mmtmn/docmatch/tools/installer/internal/install"
	"github.com/mmtmn/docmatch/tools/installer/internal/ports"
	"github.com/mmtmn/docmatch/tools/installer/internal/selfupdate"
	"github.com/mmtmn/docmatch/tools/installer/internal/state"
	"github.com/mmtmn/docmatch/tools/installer/internal/toolchain"
	"github.com/mmtmn/docmatch/tools/installer/internal/ui"
)

func TestVersionEverywhere(t *testing.T) {
	for _, args := range [][]string{{"version"}, {"--version"}, {"--plain", "--version"}, {"--plain", "version"}} {
		w, out, _ := testWorld(t, nil)
		if code := run(context.Background(), w, args, Build{Version: "0.3.1"}); code != 0 || out.String() != "docmatch 0.3.1\n" {
			t.Errorf("%v: %d %q", args, code, out)
		}
	}
}

// site serves a release of version for this machine's binary name.
func site(t *testing.T, version string) string {
	t.Helper()
	name := selfupdate.AssetName("linux", "amd64")
	body := []byte("docmatch " + version)
	sum := sha256.Sum256(body)
	mux := http.NewServeMux()
	mux.HandleFunc("/install/VERSION.txt", func(w http.ResponseWriter, _ *http.Request) { _, _ = fmt.Fprintln(w, version) })
	mux.HandleFunc("/install/SHA256SUMS.txt", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, "%s  %s\n", hex.EncodeToString(sum[:]), name)
	})
	mux.HandleFunc("/install/"+name, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(body) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL + "/install"
}

// updateWorld runs `update` on a copy of the binary in a temporary folder; the probe reads the
// downloaded file instead of running it.
func updateWorld(t *testing.T, env map[string]string) (world, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	w, out, errb := testWorld(t, env)
	w.exe = filepath.Join(t.TempDir(), "docmatch")
	if err := os.WriteFile(w.exe, []byte("docmatch 0.1.0"), 0o755); err != nil { //nolint:gosec // test binary
		t.Fatal(err)
	}
	w.probe = func(_ context.Context, path string) (string, error) {
		data, err := os.ReadFile(path) //nolint:gosec // test file
		return string(data), err
	}
	return w, out, errb
}

func TestUpdateCommand(t *testing.T) {
	base := site(t, "0.2.0")
	w, out, errb := updateWorld(t, map[string]string{"DOCMATCH_INSTALL_BASE": base})
	if code := run(context.Background(), w, []string{"update", "--check"}, Build{Version: "0.1.0"}); code != 0 || !strings.Contains(out.String(), "0.2.0 disponível") {
		t.Fatalf("%d %q %q", code, out, errb)
	}
	out.Reset()
	if code := run(context.Background(), w, []string{"update"}, Build{Version: "0.1.0"}); code != 0 {
		t.Fatalf("%d %q", code, errb)
	}
	if data, _ := os.ReadFile(w.exe); string(data) != "docmatch 0.2.0" {
		t.Fatalf("binário: %q", data)
	}
	for _, want := range []string{"procurando a versão mais recente em " + base, "baixando SHA256SUMS.txt", "checksum SHA-256 e versão conferidos", "0.1.0 -> 0.2.0"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("sem %q:\n%s", want, out)
		}
	}
	entries, _ := os.ReadDir(filepath.Dir(w.exe))
	if len(entries) != 1 {
		t.Fatalf("sobras do download: %v", entries)
	}
	out.Reset()
	if code := run(context.Background(), w, []string{"update"}, Build{Version: "0.2.0"}); code != 0 || !strings.Contains(out.String(), "já é a versão mais recente") {
		t.Fatalf("%d %q", code, out)
	}
}

func TestUpdateRefusals(t *testing.T) {
	w, _, errb := updateWorld(t, nil)
	if code := run(context.Background(), w, []string{"update"}, Build{Version: "dev"}); code != exitFailed || !strings.Contains(errb.String(), "DOCMATCH_INSTALL_BASE") {
		t.Fatalf("binário local sem base: %d %q", code, errb)
	}
	// The stamped base is enough.
	w, out, _ := updateWorld(t, nil)
	if code := run(context.Background(), w, []string{"update", "--check"}, Build{Version: "0.2.0", InstallBase: site(t, "0.2.0")}); code != 0 || !strings.Contains(out.String(), "já é a versão mais recente") {
		t.Fatalf("%d %q", code, out)
	}
	w, _, errb = testWorld(t, map[string]string{"DOCMATCH_INSTALL_BASE": "http://127.0.0.1:1/install"})
	w.exe = "/opt/homebrew/Cellar/docmatch/0.1.0/bin/docmatch"
	if code := run(context.Background(), w, []string{"update"}, Build{Version: "0.1.0"}); code != exitFailed || !strings.Contains(errb.String(), "brew upgrade docmatch") {
		t.Fatalf("Homebrew: %d %q", code, errb)
	}
}

// recorder is uninstallOps that remembers what it was asked to do.
type recorder struct {
	calls []string
	fail  map[string]error
}

func (r *recorder) do(call string) error {
	r.calls = append(r.calls, call)
	return r.fail[strings.Fields(call)[0]]
}

func (r *recorder) Down(_ context.Context, o install.Options, _ *state.State, volumes bool) error {
	return r.do(fmt.Sprintf("down %s volumes=%v", o.Project, volumes))
}
func (r *recorder) StopDev(_ context.Context, source string) error { return r.do("stopdev " + source) }
func (r *recorder) WipeDev(_ context.Context, source string) error { return r.do("wipedev " + source) }
func (r *recorder) RemoveImages(_ context.Context, images []string) error {
	return r.do("rmi " + strings.Join(images, " "))
}
func (r *recorder) RemoveAll(dir string) error    { return r.do("rmdir " + dir) }
func (r *recorder) RemoveBinary(exe string) error { return r.do("rmbin " + exe) }

func installed(t *testing.T, st state.State) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "local")
	if st.Ports == nil {
		st.Ports = ports.Defaults()
	}
	if err := st.Save(dir); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestUninstallTry(t *testing.T) {
	dir := installed(t, state.State{Mode: state.ModeTry, Project: "docmatch-local", Registry: "ghcr.io/owner", Tag: "0.1.0"})
	w, out, errb := testWorld(t, nil)
	rec := &recorder{}
	w.ops = rec
	w.stdin = strings.NewReader("s\nn\ns\n") // volumes yes, images no, folder yes
	// The binary was updated to 0.2.0 since: the images on disk are still the installed 0.1.0.
	if code := run(context.Background(), w, []string{"uninstall", "--dir", dir}, Build{Version: "0.2.0"}); code != 0 {
		t.Fatalf("%d %q", code, errb)
	}
	want := []string{"down docmatch-local volumes=true", "rmdir " + dir, "rmbin " + w.exe}
	if strings.Join(rec.calls, "|") != strings.Join(want, "|") {
		t.Fatalf("%q", rec.calls)
	}
	for _, s := range []string{"Apagar também o banco", "ghcr.io/owner/docmatch-api:0.1.0", "serviços parados e dados apagados", "removido", "desinstalada"} {
		if !strings.Contains(out.String(), s) {
			t.Errorf("sem %q:\n%s", s, out)
		}
	}

	// Saying no to everything keeps data, images and folder; the binary still goes, last.
	w, _, _ = testWorld(t, nil)
	rec = &recorder{fail: map[string]error{"down": errors.New("Cannot connect to the Docker daemon\nmais")}}
	w.ops = rec
	w.stdin = strings.NewReader("\n\n\n")
	if code := run(context.Background(), w, []string{"uninstall", "--dir", dir}, Build{}); code != 0 {
		t.Fatal(code)
	}
	if strings.Join(rec.calls, "|") != "down docmatch-local volumes=false|rmbin "+w.exe {
		t.Fatalf("%q", rec.calls)
	}

	// --yes answers yes to every question.
	w, _, _ = testWorld(t, nil)
	rec = &recorder{}
	w.ops, w.isTTY = rec, false
	if code := run(context.Background(), w, []string{"uninstall", "--dir", dir, "--yes"}, Build{}); code != 0 || len(rec.calls) != 4 {
		t.Fatalf("%d %q", code, rec.calls)
	}
}

// checkout is a folder with the files that make it a docmatch repository.
func checkout(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, f := range []string{"deploy/local/compose.yaml", "services/api/go.mod", "services/harness/Cargo.toml"} {
		p := filepath.Join(dir, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestUninstallDevKeepsTheCheckout(t *testing.T) {
	src := checkout(t)
	dir := installed(t, state.State{Mode: state.ModeDev, Project: "docmatch-local", SourceDir: src})
	w, out, _ := testWorld(t, nil)
	rec := &recorder{}
	w.ops = rec
	w.stdin = strings.NewReader("s\ns\n")
	if code := run(context.Background(), w, []string{"uninstall", "--dir", dir}, Build{}); code != 0 {
		t.Fatal(code)
	}
	if strings.Join(rec.calls, "|") != strings.Join([]string{"stopdev " + src, "wipedev " + src, "rmdir " + dir, "rmbin " + w.exe}, "|") {
		t.Fatalf("%q", rec.calls)
	}
	if !strings.Contains(out.String(), "o código em "+src+" fica") {
		t.Fatal(out.String())
	}
	// The checkout is gone: nothing runs make in some other folder.
	dir = installed(t, state.State{Mode: state.ModeDev, Project: "docmatch-local", SourceDir: filepath.Join(t.TempDir(), "sumiu")})
	w, out, _ = testWorld(t, nil)
	rec = &recorder{}
	w.ops, w.stdin = rec, strings.NewReader("n\n")
	if code := run(context.Background(), w, []string{"uninstall", "--dir", dir}, Build{}); code != 0 || strings.Join(rec.calls, "|") != "rmbin "+w.exe || !strings.Contains(out.String(), "nada a parar") {
		t.Fatalf("%d %q\n%s", code, rec.calls, out)
	}
}

func TestUninstallGuards(t *testing.T) {
	w, _, errb := testWorld(t, nil)
	w.isTTY = false
	if code := run(context.Background(), w, []string{"uninstall"}, Build{}); code != exitUsage || !strings.Contains(errb.String(), "--yes") {
		t.Fatalf("sem terminal e sem --yes: %d %q", code, errb)
	}
	// No installation: only the binary goes.
	w, out, _ := testWorld(t, nil)
	rec := &recorder{}
	w.ops = rec
	if code := run(context.Background(), w, []string{"uninstall", "--dir", filepath.Join(t.TempDir(), "nada")}, Build{}); code != 0 || len(rec.calls) != 1 || !strings.Contains(out.String(), "nenhuma instalação") {
		t.Fatalf("%d %q %s", code, rec.calls, out)
	}
	// A binary from Homebrew stays for brew to remove.
	w, out, _ = testWorld(t, nil)
	rec = &recorder{}
	w.ops, w.exe = rec, "/opt/homebrew/Cellar/docmatch/0.1.0/bin/docmatch"
	if code := run(context.Background(), w, []string{"uninstall", "--yes", "--dir", filepath.Join(t.TempDir(), "nada")}, Build{}); code != 0 || len(rec.calls) != 0 || !strings.Contains(out.String(), "brew uninstall docmatch") {
		t.Fatalf("%d %q %s", code, rec.calls, out)
	}
}

func TestRemovableDir(t *testing.T) {
	home := t.TempDir()
	ok := installed(t, state.State{Mode: state.ModeTry})
	if !removableDir(ok, home, install.Options{Mode: state.ModeTry}) {
		t.Fatal("a pasta da instalação sai")
	}
	src := filepath.Join(ok, "repo")
	if removableDir(ok, home, install.Options{Mode: state.ModeDev, SourceDir: src}) {
		t.Fatal("o checkout de quem desenvolve não sai junto")
	}
	if removableDir(home, home, install.Options{}) || removableDir(filepath.Dir(home), home, install.Options{}) || removableDir(string(filepath.Separator), home, install.Options{}) {
		t.Fatal("a pasta pessoal e as de cima, nunca")
	}
	if removableDir(t.TempDir(), home, install.Options{}) {
		t.Fatal("sem state.json não é nossa")
	}
	withGit := installed(t, state.State{Mode: state.ModeTry})
	if err := os.Mkdir(filepath.Join(withGit, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if removableDir(withGit, home, install.Options{}) {
		t.Fatal("um repositório git, nunca")
	}
}

// toolsWorld is a Linux machine without Go, with apt and sudo.
func toolsWorld(t *testing.T, isTTY bool, stdin string) (*devTools, *fakeRunner, *bytes.Buffer) {
	t.Helper()
	w, out, _ := testWorld(t, nil)
	f := &fakeRunner{have: map[string]bool{"git": true, "make": true, "cc": true, "rustup": true, "cargo": true, "node": true, "pnpm": true,
		"docker": true, "openssl": true, "curl": true, "apt-get": true, "apt-cache": true, "sudo": true}}
	w.runner, w.exists, w.isTTY, w.stdin = f, func(string) bool { return false }, isTTY, strings.NewReader(stdin)
	d := &devTools{w: w}
	d.exec = func(_ context.Context, args []string, term toolchain.Terminal) error {
		f.ran = append(f.ran, strings.Join(args, " "))
		if strings.Contains(strings.Join(args, " "), "install -y golang-go") {
			f.have["go"] = true
		}
		return nil
	}
	return d, f, out
}

type fakeRunner struct {
	have map[string]bool
	ran  []string
}

func (f *fakeRunner) LookPath(name string) (string, error) {
	if f.have[name] {
		return "/usr/bin/" + name, nil
	}
	return "", errors.New("not found")
}

func (f *fakeRunner) Output(_ context.Context, _, name string, args ...string) (string, error) {
	if !f.have[name] {
		return "", errors.New("not found")
	}
	return map[string]string{"go": "go version go1.27.3 linux/amd64", "node": "v22.23.4", "pnpm": "11.5.3", "cargo": "cargo 1.93.0",
		"docker": "2.40.3"}[name], nil
}

func sudo() string {
	if runtime.GOOS != "windows" && os.Geteuid() == 0 {
		return ""
	}
	return "sudo "
}

func TestToolsPlainInstalls(t *testing.T) {
	d, f, out := toolsWorld(t, false, "")
	p := ui.NewPlain(out)
	goOn, code := d.toolsPlain(context.Background(), p, install.Options{Mode: state.ModeDev}, installFlags{installTools: true})
	if !goOn || code != 0 {
		t.Fatalf("%v %d\n%s", goOn, code, out)
	}
	if strings.Join(f.ran, "|") != sudo()+"apt-get update|"+sudo()+"apt-get install -y golang-go" {
		t.Fatalf("%q", f.ran)
	}
	for _, s := range []string{"[xx] Go", "Para instalar com o apt, nesta ordem:", "! $ " + sudo() + "apt-get install -y golang-go", "[ok] " + sudo() + "apt-get update", "Tudo pronto para o make local."} {
		if !strings.Contains(out.String(), s) {
			t.Errorf("sem %q:\n%s", s, out)
		}
	}
}

func TestToolsPlainAsks(t *testing.T) {
	// No terminal, no --install-tools: nothing runs.
	d, f, out := toolsWorld(t, false, "")
	if goOn, code := d.toolsPlain(context.Background(), ui.NewPlain(out), install.Options{Mode: state.ModeDev}, installFlags{}); goOn || code != exitFailed || len(f.ran) != 0 || !strings.Contains(out.String(), "--install-tools") {
		t.Fatalf("%v %d %q\n%s", goOn, code, f.ran, out)
	}
	// A terminal: the answer decides.
	d, f, out = toolsWorld(t, true, "n\n")
	if goOn, _ := d.toolsPlain(context.Background(), ui.NewPlain(out), install.Options{Mode: state.ModeDev}, installFlags{}); goOn || len(f.ran) != 0 || !strings.Contains(out.String(), "Nada foi instalado") {
		t.Fatalf("%q\n%s", f.ran, out)
	}
	d, f, out = toolsWorld(t, true, "s\n")
	if goOn, _ := d.toolsPlain(context.Background(), ui.NewPlain(out), install.Options{Mode: state.ModeDev}, installFlags{}); !goOn || len(f.ran) != 2 {
		t.Fatalf("%q\n%s", f.ran, out)
	}
}

func TestToolsWindowsHandoff(t *testing.T) {
	d, f, out := toolsWorld(t, false, "")
	d.w.info.OS = "windows"
	d.b.InstallBase = "https://site.invalid/install/"
	f.have = map[string]bool{"wsl": true, "docker": true}
	goOn, code := d.toolsPlain(context.Background(), ui.NewPlain(out), install.Options{Mode: state.ModeDev}, installFlags{})
	if goOn || code != 0 || !strings.Contains(out.String(), "curl -fsSL https://site.invalid/install/install.sh | sh -s -- --mode desenvolver") {
		t.Fatalf("%v %d\n%s", goOn, code, out)
	}
}
