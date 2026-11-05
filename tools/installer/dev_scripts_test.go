package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// The scripts the Desenvolver mode runs (make setup-local, make local) also run on macOS, whose
// sed, readlink, date, grep and stat are BSD's: none of the GNU-only spellings below.
var gnuOnly = map[string]*regexp.Regexp{
	"sed -i sem sufixo (BSD pede -i.bak)": regexp.MustCompile(`\bsed\s+(-[a-zA-Z]*\s+)*-i(\s|$|["'])`),
	"readlink -f":                         regexp.MustCompile(`\breadlink\s+-[a-zA-Z]*f`),
	"date -d":                             regexp.MustCompile(`\bdate\s+-d\b`),
	"grep -P":                             regexp.MustCompile(`\bgrep\s+-[a-zA-Z]*P`),
	"stat -c":                             regexp.MustCompile(`\bstat\s+-c\b`),
}

func TestDevScriptsArePortable(t *testing.T) {
	for _, rel := range []string{"../dev/local.sh", "../dev/env.sh", "../git-hooks/install.sh"} {
		data, err := os.ReadFile(rel)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "#") {
				continue
			}
			for what, re := range gnuOnly {
				if re.MatchString(line) {
					t.Errorf("%s:%d: %s: %s", rel, i+1, what, strings.TrimSpace(line))
				}
			}
		}
	}
}

// local.sh fills the empty secrets of .env with `sed -i.bak ... && rm -f .env.bak`: the same
// command, here, leaves the new value and no backup behind.
func TestLocalShSecretEdit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sed")
	}
	data, err := os.ReadFile("../dev/local.sh")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `sed -i.bak "s|^${name}=.*|${name}=${value}|" .env && rm -f .env.bak`) {
		t.Fatal("o comando que preenche os segredos mudou: ajuste este teste")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("A=1\nDM_SESSION_SECRET=\nB=2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "-c", `name=DM_SESSION_SECRET value='abc/+=' && sed -i.bak "s|^${name}=.*|${name}=${value}|" .env && rm -f .env.bak`)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	got, _ := os.ReadFile(filepath.Join(dir, ".env")) //nolint:gosec // test file
	if string(got) != "A=1\nDM_SESSION_SECRET=abc/+=\nB=2\n" {
		t.Fatalf("%q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, ".env.bak")); err == nil {
		t.Fatal("sobrou o .env.bak")
	}
}
