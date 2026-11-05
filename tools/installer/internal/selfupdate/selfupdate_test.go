package selfupdate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// memFS is a filesystem in memory. With windows set it behaves like Windows with the running
// binary: it can be renamed, never overwritten nor deleted (the lock follows the file).
type memFS struct {
	mu      sync.Mutex
	files   map[string][]byte
	modes   map[string]fs.FileMode
	locked  map[string]bool // running images
	windows bool
	failOn  map[string]error // "rename <old>" / "create" -> error
	n       int
}

func newMemFS(windows bool) *memFS {
	return &memFS{files: map[string][]byte{}, modes: map[string]fs.FileMode{}, locked: map[string]bool{}, windows: windows, failOn: map[string]error{}}
}

type memFile struct {
	fs   *memFS
	name string
	buf  bytes.Buffer
}

func (f *memFile) Write(p []byte) (int, error) { return f.buf.Write(p) }
func (f *memFile) Name() string                { return f.name }
func (f *memFile) Close() error {
	f.fs.mu.Lock()
	defer f.fs.mu.Unlock()
	f.fs.files[f.name] = f.buf.Bytes()
	return nil
}

func (m *memFS) CreateTemp(dir, pattern string) (File, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.failOn["create"]; err != nil {
		return nil, err
	}
	m.n++
	name := dir + "/" + strings.Replace(pattern, "*", fmt.Sprint(m.n), 1)
	m.files[name] = nil
	return &memFile{fs: m, name: name}, nil
}

func (m *memFS) Rename(oldpath, newpath string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.failOn["rename "+oldpath]; err != nil {
		return err
	}
	data, ok := m.files[oldpath]
	if !ok {
		return fs.ErrNotExist
	}
	if m.windows && m.locked[newpath] {
		return errors.New("Access is denied.")
	}
	delete(m.files, oldpath)
	m.files[newpath] = data
	m.modes[newpath] = m.modes[oldpath]
	if m.locked[oldpath] {
		delete(m.locked, oldpath)
		m.locked[newpath] = true
	}
	return nil
}

func (m *memFS) Remove(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.files[name]; !ok {
		return fs.ErrNotExist
	}
	if m.windows && m.locked[name] {
		return errors.New("Access is denied.")
	}
	delete(m.files, name)
	return nil
}

func (m *memFS) Chmod(name string, mode fs.FileMode) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.modes[name] = mode
	return nil
}

func (m *memFS) names() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.files))
	for k := range m.files {
		out = append(out, k)
	}
	return out
}

// release serves a fake release: VERSION.txt, SHA256SUMS.txt and the binaries of 0.2.0 at the
// root, 0.1.5 under v0.1.5/. tamper breaks the linux/amd64 binary after its checksum.
func release(t *testing.T, tamper bool) string {
	t.Helper()
	bins := map[string][]byte{}
	sums := map[string]string{}
	for _, v := range []string{"0.2.0", "0.1.5"} {
		lines := make([]string, 0, 2)
		for _, name := range []string{AssetName("linux", "amd64"), AssetName("windows", "amd64")} {
			body := []byte("binário " + name + " " + v)
			sum := sha256.Sum256(body)
			lines = append(lines, hex.EncodeToString(sum[:])+"  "+name)
			if tamper && strings.Contains(name, "linux") {
				body = append(body, '!')
			}
			bins[v+"/"+name] = body
		}
		sums[v] = strings.Join(lines, "\n") + "\n"
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/install/VERSION.txt", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("0.2.0\n")) })
	serve := func(prefix, v string) {
		mux.HandleFunc(prefix+"SHA256SUMS.txt", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(sums[v])) })
		for _, name := range []string{AssetName("linux", "amd64"), AssetName("windows", "amd64")} {
			body := bins[v+"/"+name]
			mux.HandleFunc(prefix+name, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(body) })
		}
	}
	serve("/install/", "0.2.0")
	serve("/install/v0.1.5/", "0.1.5")
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL + "/install"
}

// probe answers what the downloaded "binary" says it is.
func probe(m *memFS) func(context.Context, string) (string, error) {
	return func(_ context.Context, path string) (string, error) {
		m.mu.Lock()
		defer m.mu.Unlock()
		f := strings.Fields(string(m.files[path]))
		return "docmatch " + f[len(f)-1], nil
	}
}

func updater(t *testing.T, base, goos string, m *memFS) *Updater {
	t.Helper()
	exe := "/home/ana/.local/bin/docmatch"
	if goos == "windows" {
		exe = "C:/Users/ana/AppData/Local/docmatch/bin/docmatch.exe"
	}
	m.files[exe] = []byte("binário antigo 0.1.0")
	m.locked[exe] = true
	return &Updater{Base: base, Current: "0.1.0", Exe: exe, GOOS: goos, GOARCH: "amd64", FS: m, Probe: probe(m)}
}

func TestAssetName(t *testing.T) {
	if AssetName("darwin", "arm64") != "docmatch-darwin-arm64.bin" || AssetName("windows", "arm64") != "docmatch-windows-arm64.exe" {
		t.Fatal(AssetName("darwin", "arm64"))
	}
}

func TestUpdateUnix(t *testing.T) {
	m := newMemFS(false)
	u := updater(t, release(t, false), "linux", m)
	var said []string
	out, err := u.Update(context.Background(), "", false, func(s string) { said = append(said, s) })
	if err != nil || !out.Updated || out.From != "0.1.0" || out.To != "0.2.0" {
		t.Fatalf("%+v %v", out, err)
	}
	if got := string(m.files[u.Exe]); got != "binário docmatch-linux-amd64.bin 0.2.0" || m.modes[u.Exe] != 0o755 {
		t.Fatalf("%q %v", got, m.modes[u.Exe])
	}
	if len(m.names()) != 1 || strings.Join(said, ",") != "SHA256SUMS.txt,docmatch-linux-amd64.bin" {
		t.Fatalf("sobras: %v; fases: %v", m.names(), said)
	}
	// Already the latest: nothing is downloaded unless forced.
	u.Current = "0.2.0"
	if out, err = u.Update(context.Background(), "", false, nil); err != nil || out.Updated {
		t.Fatalf("%+v %v", out, err)
	}
	if out, err = u.Update(context.Background(), "", true, nil); err != nil || !out.Updated {
		t.Fatalf("--force: %+v %v", out, err)
	}
}

func TestUpdatePinnedVersion(t *testing.T) {
	m := newMemFS(false)
	u := updater(t, release(t, false), "linux", m)
	out, err := u.Update(context.Background(), "v0.1.5", false, nil)
	if err != nil || out.To != "0.1.5" || string(m.files[u.Exe]) != "binário docmatch-linux-amd64.bin 0.1.5" {
		t.Fatalf("%+v %v %q", out, err, m.files[u.Exe])
	}
	if _, err := u.Update(context.Background(), "9.9.9", false, nil); err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("versão que não existe: %v", err)
	}
}

func TestUpdateRefusesTamperedBinary(t *testing.T) {
	m := newMemFS(false)
	u := updater(t, release(t, true), "linux", m)
	_, err := u.Update(context.Background(), "", false, nil)
	if !errors.Is(err, ErrChecksum) || !strings.Contains(err.Error(), "nada foi trocado") {
		t.Fatalf("%v", err)
	}
	if string(m.files[u.Exe]) != "binário antigo 0.1.0" || len(m.names()) != 1 {
		t.Fatalf("o binário antigo fica e o download some: %v", m.names())
	}
}

func TestUpdateRefusesWrongVersion(t *testing.T) {
	// VERSION.txt says 0.2.0 but the binary says otherwise (a half-published release): no swap.
	m := newMemFS(false)
	u := updater(t, release(t, false), "linux", m)
	u.Probe = func(context.Context, string) (string, error) { return "docmatch 0.1.9", nil }
	if _, err := u.Update(context.Background(), "", false, nil); !errors.Is(err, ErrVersion) {
		t.Fatalf("%v", err)
	}
	u.Probe = func(context.Context, string) (string, error) { return "", errors.New("exec format error") }
	if _, err := u.Update(context.Background(), "", false, nil); err == nil || !strings.Contains(err.Error(), "não rodou") {
		t.Fatalf("%v", err)
	}
	if string(m.files[u.Exe]) != "binário antigo 0.1.0" || len(m.names()) != 1 {
		t.Fatalf("%v", m.names())
	}
}

func TestUpdateWindowsRenameDance(t *testing.T) {
	m := newMemFS(true)
	u := updater(t, release(t, false), "windows", m)
	// An earlier update's leftover that is no longer running.
	m.files[OldPath(u.Exe)] = []byte("bem antigo")
	out, err := u.Update(context.Background(), "", false, nil)
	if err != nil || !out.Updated {
		t.Fatalf("%+v %v", out, err)
	}
	if got := string(m.files[u.Exe]); got != "binário docmatch-windows-amd64.exe 0.2.0" {
		t.Fatalf("%q", got)
	}
	// The running one moved aside and stays (locked) until the next start removes it.
	if string(m.files[OldPath(u.Exe)]) != "binário antigo 0.1.0" || !m.locked[OldPath(u.Exe)] {
		t.Fatalf("%v", m.names())
	}
	m.locked = map[string]bool{} // the old process ended
	Cleanup(m, u.Exe)
	if _, ok := m.files[OldPath(u.Exe)]; ok || len(m.names()) != 1 {
		t.Fatalf("%v", m.names())
	}
}

func TestUpdateWindowsRollsBack(t *testing.T) {
	m := newMemFS(true)
	u := updater(t, release(t, false), "windows", m)
	// The new file cannot take the place (an antivirus holding it, say): the old one returns.
	u.FS = &failingSecondRename{memFS: m, exe: u.Exe}
	if _, err := u.Update(context.Background(), "", false, nil); err == nil {
		t.Fatal("devia falhar")
	}
	if string(m.files[u.Exe]) != "binário antigo 0.1.0" {
		t.Fatalf("o antigo volta ao lugar: %v", m.names())
	}
}

// failingSecondRename refuses to move anything onto exe except the old binary coming back.
type failingSecondRename struct {
	*memFS
	exe string
}

func (f *failingSecondRename) Rename(oldpath, newpath string) error {
	if newpath == f.exe && oldpath != OldPath(f.exe) {
		return errors.New("The process cannot access the file because it is being used by another process.")
	}
	return f.memFS.Rename(oldpath, newpath)
}

func TestUpdateNeedsBaseAndWritableFolder(t *testing.T) {
	m := newMemFS(false)
	u := updater(t, "", "linux", m)
	if _, err := u.Update(context.Background(), "", false, nil); !errors.Is(err, ErrNoBase) {
		t.Fatal(err)
	}
	u = updater(t, release(t, false), "linux", m)
	m.failOn["create"] = fs.ErrPermission
	if _, err := u.Update(context.Background(), "", false, nil); err == nil || !strings.Contains(err.Error(), "sem permissão") {
		t.Fatal(err)
	}
}

func TestLatestAndExpected(t *testing.T) {
	u := updater(t, release(t, false)+"/", "linux", newMemFS(false))
	if v, err := u.Latest(context.Background()); err != nil || v != "0.2.0" {
		t.Fatalf("%q %v", v, err)
	}
	sums := strings.Repeat("a", 64) + "  docmatch-linux-amd64.bin\n" + strings.Repeat("B", 64) + " *docmatch-darwin-arm64.bin\n"
	if Expected(sums, "docmatch-darwin-arm64.bin") != strings.Repeat("b", 64) || Expected(sums, "x") != "" {
		t.Fatal("SHA256SUMS.txt")
	}
}

func TestManaged(t *testing.T) {
	for path, want := range map[string]string{
		"/opt/homebrew/Cellar/docmatch/0.1.0/bin/docmatch":                                   "brew",
		`C:\Users\ana\AppData\Local\Microsoft\WinGet\Packages\mmtmn.docmatch_x\docmatch.exe`: "winget",
		"/tmp/go-build123/b001/exe/docmatch":                                                 "go",
		"/home/ana/.local/bin/docmatch":                                                      "",
	} {
		if got := Managed(path); got != want {
			t.Errorf("%s: %q", path, got)
		}
	}
}

func TestRemove(t *testing.T) {
	m := newMemFS(false)
	m.files["/bin/docmatch"] = []byte("x")
	if err := (Remover{FS: m, GOOS: "linux"}).Remove("/bin/docmatch"); err != nil || len(m.names()) != 0 {
		t.Fatalf("%v %v", err, m.names())
	}
	w := newMemFS(true)
	exe := "C:/docmatch/bin/docmatch.exe"
	w.files[exe] = []byte("x")
	w.locked[exe] = true
	var later string
	if err := (Remover{FS: w, GOOS: "windows", Later: func(p string) error { later = p; return nil }}).Remove(exe); err != nil {
		t.Fatal(err)
	}
	if later != OldPath(exe) || len(w.names()) != 1 || w.names()[0] != OldPath(exe) {
		t.Fatalf("o Windows tira o binário do caminho e apaga depois: %q %v", later, w.names())
	}
}
