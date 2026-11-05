package platform

import (
	"path/filepath"
	"testing"
)

func env(kv map[string]string) Lookup {
	return func(k string) (string, bool) { v, ok := kv[k]; return v, ok }
}

func TestSupported(t *testing.T) {
	for _, c := range []struct {
		os, arch string
		ok       bool
	}{{"linux", "amd64", true}, {"darwin", "arm64", true}, {"windows", "arm64", true}, {"linux", "386", false}, {"freebsd", "amd64", false}} {
		if (Info{OS: c.os, Arch: c.arch}).Supported() != c.ok {
			t.Errorf("%s/%s", c.os, c.arch)
		}
	}
	if (Info{OS: "linux", Arch: "amd64", WSL: true}).Name() != "linux/amd64 (WSL)" {
		t.Fatal("WSL")
	}
}

func TestEnvFlags(t *testing.T) {
	if !IsCI(env(map[string]string{"CI": "true"})) || IsCI(env(map[string]string{"CI": "0"})) || IsCI(env(nil)) {
		t.Fatal("CI")
	}
	if !NoColor(env(map[string]string{"NO_COLOR": "1"})) || !NoColor(env(map[string]string{"TERM": "dumb"})) || NoColor(env(nil)) {
		t.Fatal("NO_COLOR")
	}
	if !env(map[string]string{"X": "yes"}).Truthy("X") || env(map[string]string{"X": "false"}).Truthy("X") {
		t.Fatal("Truthy")
	}
}

func TestUnicode(t *testing.T) {
	cases := []struct {
		os  string
		env map[string]string
		ok  bool
	}{
		{"linux", map[string]string{"LANG": "pt_BR.UTF-8"}, true},
		{"linux", map[string]string{"LC_ALL": "C", "LANG": "pt_BR.UTF-8"}, false},
		{"linux", map[string]string{"LANG": "pt_BR.utf8", "TERM": "linux"}, false},
		{"linux", nil, false},
		{"darwin", nil, true},
		{"windows", nil, false},
		{"windows", map[string]string{"WT_SESSION": "x"}, true},
		{"windows", map[string]string{"TERM_PROGRAM": "vscode"}, true},
	}
	for i, c := range cases {
		if SupportsUnicode(c.os, env(c.env)) != c.ok {
			t.Errorf("caso %d: %s %v", i, c.os, c.env)
		}
	}
}

func TestDarkBackground(t *testing.T) {
	if !DarkBackground(env(nil)) || DarkBackground(env(map[string]string{"COLORFGBG": "0;15"})) || !DarkBackground(env(map[string]string{"COLORFGBG": "15;default;0"})) {
		t.Fatal("COLORFGBG")
	}
	if DarkBackground(env(map[string]string{"DOCMATCH_THEME": "light", "COLORFGBG": "15;0"})) {
		t.Fatal("DOCMATCH_THEME vence")
	}
}

func TestDefaultHome(t *testing.T) {
	home := filepath.FromSlash("/home/u")
	if got := DefaultHome("linux", env(nil), home); got != filepath.Join(home, ".local", "share", "docmatch", "local") {
		t.Fatal(got)
	}
	if got := DefaultHome("linux", env(map[string]string{"XDG_DATA_HOME": "/d"}), home); got != filepath.Join("/d", "docmatch", "local") {
		t.Fatal(got)
	}
	if got := DefaultHome("darwin", env(nil), home); got != filepath.Join(home, "Library", "Application Support", "docmatch", "local") {
		t.Fatal(got)
	}
	if got := DefaultHome("windows", env(map[string]string{"LOCALAPPDATA": `C:\L`}), home); got != filepath.Join(`C:\L`, "docmatch", "local") {
		t.Fatal(got)
	}
	if got := DefaultHome("linux", env(map[string]string{"DOCMATCH_HOME": "/x"}), home); got != "/x" {
		t.Fatal(got)
	}
	if ExpandHome("~/a", home) != filepath.Join(home, "a") || ExpandHome("~", home) != home || ExpandHome("/b", home) != "/b" {
		t.Fatal("ExpandHome")
	}
}

func TestHumanBytes(t *testing.T) {
	for n, want := range map[uint64]string{512: "512 B", 1500: "1,5 kB", 830_000_000: "830 MB", 12_100_000_000: "12,1 GB", 2_000_000_000_000: "2,0 TB"} {
		if got := HumanBytes(n); got != want {
			t.Errorf("HumanBytes(%d) = %q; quero %q", n, got, want)
		}
	}
}

func TestFreeBytes(t *testing.T) {
	n, err := FreeBytes(filepath.Join(t.TempDir(), "ainda", "não", "existe"))
	if err != nil || n == 0 {
		t.Fatalf("%d %v", n, err)
	}
}

func TestBrowser(t *testing.T) {
	if CanOpenBrowser(Info{OS: "linux"}, env(nil)) || !CanOpenBrowser(Info{OS: "linux"}, env(map[string]string{"DISPLAY": ":0"})) {
		t.Fatal("Linux sem tela")
	}
	if CanOpenBrowser(Info{OS: "darwin"}, env(map[string]string{"SSH_CONNECTION": "x"})) || !CanOpenBrowser(Info{OS: "linux", WSL: true}, env(nil)) {
		t.Fatal("SSH/WSL")
	}
	for _, c := range []struct {
		info Info
		want string
	}{{Info{OS: "darwin"}, "open"}, {Info{OS: "windows"}, "rundll32"}, {Info{OS: "linux"}, "xdg-open"}} {
		if name, args := browserCommand(c.info, "http://localhost:1"); name != c.want || args[len(args)-1] != "http://localhost:1" {
			t.Errorf("%v: %s %v", c.info, name, args)
		}
	}
}
