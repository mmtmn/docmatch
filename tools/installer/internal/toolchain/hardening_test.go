package toolchain

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// fakeInfo is an fs.FileInfo with just a mode.
type fakeInfo struct{ mode fs.FileMode }

func (f fakeInfo) Name() string       { return "" }
func (f fakeInfo) Size() int64        { return 0 }
func (f fakeInfo) Mode() fs.FileMode  { return f.mode }
func (f fakeInfo) ModTime() time.Time { return time.Time{} }
func (f fakeInfo) IsDir() bool        { return true }
func (f fakeInfo) Sys() any           { return nil }

// S1: the probes never run under a folder anyone can write to (rustup reads a
// rust-toolchain.toml from the folder it runs in and every parent, and honors its `path`).
func TestProbeDirAvoidsSharedFolders(t *testing.T) {
	modes := map[string]fs.FileMode{
		"/": 0o755, "/tmp": 0o777 | fs.ModeSticky, "/tmp/cache": 0o700,
		"/home": 0o755, "/home/ana": 0o700, "/home/ana/.cache": 0o700,
		"/srv": 0o777, "/link": fs.ModeSymlink | 0o777,
	}
	lstat := func(p string) (fs.FileInfo, error) {
		if m, ok := modes[filepath.ToSlash(p)]; ok {
			return fakeInfo{m}, nil
		}
		return nil, fs.ErrNotExist
	}
	var prepared []string
	prepare := func(dir string) error { prepared = append(prepared, dir); return nil }
	got := pickProbeDir([]string{"/tmp/cache/docmatch/probe", "/home/ana/.cache/docmatch/probe"}, lstat, prepare, "linux")
	if filepath.ToSlash(got) != "/home/ana/.cache/docmatch/probe" || len(prepared) != 1 {
		t.Fatalf("debaixo do /tmp, nunca: %q %v", got, prepared)
	}
	if got := pickProbeDir([]string{"/srv/x", "/link/x"}, lstat, prepare, "linux"); got != "/" {
		t.Fatalf("sem pasta segura, a raiz (só o root escreve): %q", got)
	}
	failing := func(string) error { return errors.New("read-only") }
	if got := pickProbeDir([]string{"/home/ana/.cache/docmatch/probe"}, lstat, failing, "linux"); got != "/" {
		t.Fatalf("%q", got)
	}
	// Windows has no world-writable bit to read (the cache under %LOCALAPPDATA% is per user).
	if got := pickProbeDir([]string{`C:\Users\ana\AppData\Local\docmatch\probe`}, lstat, prepare, "windows"); got != `C:\Users\ana\AppData\Local\docmatch\probe` {
		t.Fatalf("%q", got)
	}
}

func TestSystemOutputRunsInTheProbeDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("pwd em sh")
	}
	dir := ProbeDir()
	if dir == "/" {
		t.Skip("sem pasta de cache segura nesta máquina")
	}
	info, err := os.Stat(dir)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("pasta das sondas: %v %v", info, err)
	}
	if !safePath(dir, os.Lstat) {
		t.Fatalf("%s tem uma pasta de todos acima", dir)
	}
	out, err := System{}.Output(context.Background(), "", "sh", "-c", "pwd -P")
	want, _ := filepath.EvalSymlinks(dir)
	if err != nil || out != want {
		t.Fatalf("as sondas rodam em %q, não em %q (%v)", out, want, err)
	}
}

// S3: only a plain version of pnpm reaches `npm install pnpm@<v>`.
func TestPNPMSpecMustBeAVersion(t *testing.T) {
	for spec, want := range map[string]string{
		"pnpm@11.5.3+sha512.abcdef":          "11.5.3",
		"pnpm@12.0.1":                        "12.0.1",
		"pnpm@npm:malicioso@1.0.0":           Defaults.PNPM,
		"pnpm@github:alguem/pnpm":            Defaults.PNPM,
		"pnpm@https://exemplo.invalid/p.tgz": Defaults.PNPM,
		"pnpm@latest":                        Defaults.PNPM,
		"yarn@4.0.0":                         Defaults.PNPM,
	} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"packageManager": "`+spec+`"}`), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := ReadRequirements(dir).PNPM; got != want {
			t.Errorf("%s: %q", spec, got)
		}
	}
}

// S3: what the screen shows is exactly the argument that runs, control characters included.
func TestQuoteSpellsControlCharacters(t *testing.T) {
	for in, want := range map[string]string{
		"pnpm@11.5.3":     "pnpm@11.5.3",
		"a b":             "'a b'",
		"ok\nrm -rf ~":    `$'ok\x0arm -rf ~'`,
		"esc\x1b[2Kfalso": `$'esc\x1b[2Kfalso'`,
		"it's\r":          `$'it\'s\x0d'`,
		"barra\\ e\ttab":  `$'barra\\ e\x09tab'`,
	} {
		if got := quote(in); got != want {
			t.Errorf("%q: %s (quero %s)", in, got, want)
		}
	}
}

// C1: on Ubuntu, snap's Node replaces an apt Node 18 in /usr/bin, but /snap/bin comes last in
// Ubuntu's PATH: the plan asks for it first, and the PATH puts it there even though it already
// has it.
func TestReplacementsComeFirstInPath(t *testing.T) {
	f := ubuntu()
	f.have["node"] = "v18.19.1" // apt's
	remove(f, "pnpm")
	f.have["apt-cache"] = ""
	f.out["apt-cache policy nodejs"] = aptPolicy // 18 again: snap it is
	p := plan(t, env(f, "linux"))
	if strings.Join(p.PathFirst, ",") != "/snap/bin,/home/ana/.local/bin" {
		t.Fatalf("%v", p.PathFirst)
	}
	e := env(f, "linux")
	e.Exists = func(string) bool { return true }
	got, _ := e.AugmentPath("/usr/local/bin:/usr/bin:/bin:/snap/bin", Defaults, p.PathFirst...)
	if !strings.HasPrefix(got, "/snap/bin:/home/ana/.local/bin:") || strings.Count(got, "/snap/bin") != 1 {
		t.Fatalf("o Node do snap vence o do apt: %q", got)
	}
	// An outdated pnpm too: the one just installed into ~/.local wins.
	f = ubuntu()
	f.have["pnpm"] = "9.1.0"
	p = plan(t, env(f, "linux"))
	if strings.Join(p.PathFirst, ",") != "/home/ana/.local/bin" {
		t.Fatalf("%v", p.PathFirst)
	}
}
