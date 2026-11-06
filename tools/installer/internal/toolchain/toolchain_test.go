package toolchain

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mmtmn/docmatch/tools/installer/internal/platform"
)

// fake is a machine: which programs exist, what their version commands print, which commands
// fail. Keys of out and fail are "name arg arg".
type fake struct {
	have map[string]string // program -> output of any command not listed in out
	out  map[string]string
	fail map[string]bool
	dirs []string // dir argument of each Output call
}

func (f *fake) LookPath(name string) (string, error) {
	if _, ok := f.have[name]; ok {
		return "/usr/bin/" + name, nil
	}
	return "", errors.New("executable file not found in $PATH")
}

func (f *fake) Output(_ context.Context, dir, name string, args ...string) (string, error) {
	f.dirs = append(f.dirs, dir)
	key := strings.TrimSpace(name + " " + strings.Join(args, " "))
	if f.fail[key] {
		return f.out[key], errors.New("exit status 1")
	}
	if o, ok := f.out[key]; ok {
		return o, nil
	}
	if o, ok := f.have[name]; ok {
		return o, nil
	}
	return "", errors.New("not found")
}

// ubuntu is Ubuntu 24.04 with everything make local needs (Go from apt, Node from NodeSource).
func ubuntu() *fake {
	return &fake{
		have: map[string]string{
			"git": "git version 2.43.0", "make": "GNU Make 4.3", "cc": "cc (Ubuntu 13.3.0) 13.3.0", "go": "go version go1.22.2 linux/amd64",
			"rustup": "rustup 1.29.2 (d8c92c676 2026-10-21)", "cargo": "cargo 1.93.0 (083ac5135 2025-12-15)", "node": "v22.23.4",
			"pnpm": "11.5.3", "docker": "", "openssl": "OpenSSL 3.0.13 30 Jan 2024", "curl": "curl 8.5.0 (x86_64-pc-linux-gnu)",
			"apt-get": "", "sudo": "", "snap": "", "npm": "10.9.2", "systemctl": "",
		},
		out:  map[string]string{"docker compose version --short": "2.40.3"},
		fail: map[string]bool{},
	}
}

func env(f *fake, os string) Env {
	return Env{Info: platform.Info{OS: os, Arch: "amd64"}, Getenv: func(string) (string, bool) { return "", false },
		Home: "/home/ana", User: "ana", Runner: f, Glob: aptLists}
}

// aptLists is a machine whose apt has package lists.
func aptLists(string) ([]string, error) {
	return []string{"/var/lib/apt/lists/archive.ubuntu.com_ubuntu_dists_noble_main_binary-amd64_Packages"}, nil
}

func detect(t *testing.T, e Env) map[string]Status {
	t.Helper()
	statuses, _ := e.DetectAll(context.Background())
	out := map[string]Status{}
	for _, s := range statuses {
		out[s.ID] = s
	}
	return out
}

func remove(f *fake, names ...string) {
	for _, n := range names {
		delete(f.have, n)
	}
}

func TestDefaultsFollowTheRepository(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "services", "api", "go.mod")); err != nil {
		t.Skip("fora do repositório da docmatch")
	}
	if got := ReadRequirements(root); got != Defaults {
		t.Fatalf("os padrões sem checkout divergem do repositório: atualize toolchain.Defaults\nrepo:    %+v\npadrões: %+v", got, Defaults)
	}
}

func TestReadRequirements(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"services/api/go.mod":                  "module x\n\ngo 1.30.1\n\ntoolchain go1.30.4\n",
		".nvmrc":                               "v24\n",
		"package.json":                         `{"packageManager": "pnpm@12.0.1+sha512.abc", "engines": {"node": ">=24.1.0", "pnpm": "^12"}}`,
		"services/harness/rust-toolchain.toml": "[toolchain]\nchannel = \"1.99.0\"\n",
	}
	for name, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want := Requirements{GoMod: "1.30.1", GoToolchain: "go1.30.4", Node: "24.1.0", NodeMajor: "24", PNPM: "12.0.1", PNPMMin: "12", Rust: "1.99.0"}
	if got := ReadRequirements(dir); got != want {
		t.Fatalf("%+v", got)
	}
	if ReadRequirements("") != Defaults || ReadRequirements(t.TempDir()) != Defaults {
		t.Fatal("sem checkout valem os padrões")
	}
}

func TestDetectUbuntuReady(t *testing.T) {
	f := ubuntu()
	s := detect(t, env(f, "linux"))
	for id, st := range s {
		if !st.OK() {
			t.Errorf("%s: %+v", id, st)
		}
	}
	if !strings.Contains(s[Go].Detail, "1.22.2") || !strings.Contains(s[Go].Detail, "go1.27.3") || !strings.Contains(s[Go].Detail, "GOTOOLCHAIN=auto") {
		t.Fatalf("Go antigo que baixa o do go.mod: %q", s[Go].Detail)
	}
	if s[Docker].Detail != "Compose 2.40.3" || s[Rust].Version != "1.93.0" || s[Node].Version != "22.23.4" {
		t.Fatalf("%+v %+v %+v", s[Docker], s[Rust], s[Node])
	}
	for _, d := range f.dirs {
		if d != "" {
			t.Fatalf("as versões rodam fora do checkout (o rustup baixaria o Rust fixado): %q", d)
		}
	}
	if got := strings.Join(env(f, "linux").IDs(), ","); got != "git,make,cc,go,rust,node,pnpm,docker,openssl,curl" {
		t.Fatal(got)
	}
}

func TestDetectProblems(t *testing.T) {
	f := ubuntu()
	f.have["go"] = "go version go1.19.8 linux/amd64"
	f.have["node"] = "v20.11.0"
	f.have["pnpm"] = "9.1.0"
	f.fail["docker compose version --short"] = true
	remove(f, "rustup", "git")
	f.have["cargo"] = "cargo 1.75.0"
	s := detect(t, env(f, "linux"))
	if s[Go].State != Outdated || s[Go].Want != MinGoAuto {
		t.Fatalf("Go sem troca de toolchain: %+v", s[Go])
	}
	if s[Node].State != Outdated || s[Node].Want != "22.23.4" || s[PNPM].State != Outdated {
		t.Fatalf("%+v %+v", s[Node], s[PNPM])
	}
	if s[Docker].State != Outdated || s[Docker].Reason != "no_compose" {
		t.Fatalf("%+v", s[Docker])
	}
	if s[Rust].State != Outdated || s[Rust].Want != "1.93.0" {
		t.Fatalf("cargo da distribuição antigo, sem rustup: %+v", s[Rust])
	}
	if s[Git].State != Missing || s[Git].Detail != "não encontrado" {
		t.Fatalf("%+v", s[Git])
	}

	f = ubuntu()
	f.fail["cargo --version"] = true
	if s = detect(t, env(f, "linux")); s[Rust].State != Missing || s[Rust].Reason != "rustup_no_toolchain" {
		t.Fatalf("rustup sem toolchain: %+v", s[Rust])
	}

	f = ubuntu()
	e := env(f, "linux")
	e.Getenv = func(k string) (string, bool) { return map[string]string{"GOTOOLCHAIN": "local"}[k], k == "GOTOOLCHAIN" }
	if s = detect(t, e); s[Go].State != Outdated || s[Go].Reason != "gotoolchain_local" {
		t.Fatalf("GOTOOLCHAIN=local impede a troca: %+v", s[Go])
	}
	e.Getenv = func(k string) (string, bool) { return "go1.27.3+auto", k == "GOTOOLCHAIN" }
	if s = detect(t, e); !s[Go].OK() {
		t.Fatalf("go1.27.3+auto troca: %+v", s[Go])
	}
}

func TestGoToolchainFor(t *testing.T) {
	pinned := Requirements{GoMod: "1.27.0", GoToolchain: "go1.27.3"}
	noLine := Requirements{GoMod: "1.27.0"} // a go.mod without a toolchain line: the go line
	for _, c := range []struct {
		local string
		req   Requirements
		want  string
	}{
		{"go1.22.2", pinned, "go1.27.3"}, // Ubuntu's Go: the tools get built with the pinned one
		{"go1.27.3", pinned, "auto"},
		{"go1.28.1", pinned, "auto"}, // newer local Go: no download
		{"", pinned, "auto"},
		{"go1.22.2", noLine, "go1.27.0"},
		{"go1.22.2", Requirements{}, "auto"},
	} {
		if got := GoToolchainFor(c.local, c.req); got != c.want {
			t.Errorf("%q %+v: %q", c.local, c.req, got)
		}
	}
}

func TestCanReach(t *testing.T) {
	for v, want := range map[string]bool{
		"": true, "auto": true, "go1.27.3+auto": true, "go1.20+auto": true,
		"go1.27.3": true, "go1.28.0": true, // an exact name that is new enough
		"go1.25.0": false, "local": false, "path": false, "go1.27.3+path": false, "nada": false,
	} {
		if got := canReach(v, "1.27.0"); got != want {
			t.Errorf("GOTOOLCHAIN=%q: %v", v, got)
		}
	}
}

func TestDetectMacWithoutCommandLineTools(t *testing.T) {
	f := ubuntu()
	f.fail["xcode-select -p"] = true
	f.have["xcode-select"] = ""
	s := detect(t, env(f, "darwin"))
	if s[Make].State != Missing || s[CC].State != Missing {
		t.Fatalf("os atalhos do macOS sem as ferramentas não contam: %+v %+v", s[Make], s[CC])
	}
	delete(f.fail, "xcode-select -p")
	if s = detect(t, env(f, "darwin")); !s[Make].OK() || !s[CC].OK() {
		t.Fatalf("%+v %+v", s[Make], s[CC])
	}
}

func TestDetectWindows(t *testing.T) {
	f := &fake{have: map[string]string{"wsl": "", "winget": ""}, out: map[string]string{}, fail: map[string]bool{"wsl --status": true}}
	e := env(f, "windows")
	if got := strings.Join(e.IDs(), ","); got != "wsl,docker" {
		t.Fatal(got)
	}
	s := detect(t, e)
	if s[WSL].State != Missing || s[Docker].State != Missing {
		t.Fatalf("%+v", s)
	}
	// The feature on but no distribution: --status succeeds, the list is empty.
	delete(f.fail, "wsl --status")
	f.out["wsl --list --quiet"] = ""
	if s = detect(t, e); s[WSL].State != Missing || s[WSL].Reason != "wsl_no_distro" {
		t.Fatalf("%+v", s[WSL])
	}
	if p := plan(t, e); len(p.Commands) == 0 || p.Commands[0].String() != "wsl --install -d Ubuntu" {
		t.Fatalf("sem distribuição, o plano instala o Ubuntu: %+v", p.Commands)
	}
	f.out["wsl --list --quiet"] = "\ufeffU\x00b\x00u\x00n\x00t\x00u\x00\r\x00\n\x00" // UTF-16, as wsl.exe prints it
	if s = detect(t, e); !s[WSL].OK() || s[WSL].Detail != "Ubuntu" {
		t.Fatalf("%+v", s[WSL])
	}
}

func TestDetectMacGitStub(t *testing.T) {
	f := ubuntu()
	f.have["xcode-select"] = ""
	f.fail["xcode-select -p"] = true
	// fake.LookPath answers /usr/bin/<name>: macOS's git stub.
	if s := detect(t, env(f, "darwin")); s[Git].State != Missing {
		t.Fatalf("o git de /usr/bin sem as Command Line Tools é um atalho: %+v", s[Git])
	}
	delete(f.fail, "xcode-select -p")
	if s := detect(t, env(f, "darwin")); !s[Git].OK() {
		t.Fatalf("%+v", s[Git])
	}
}

func plan(t *testing.T, e Env) Plan {
	t.Helper()
	statuses, req := e.DetectAll(context.Background())
	return e.PlanFor(context.Background(), statuses, req)
}

func commands(p Plan) []string {
	out := make([]string, 0, len(p.Commands))
	for _, c := range p.Commands {
		out = append(out, c.String())
	}
	return out
}

func same(t *testing.T, got []string, want ...string) {
	t.Helper()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("comandos:\n%s\n\nesperados:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// aptPolicy is `apt-cache policy` on Ubuntu 24.04 for the packages the plans ask about.
const aptPolicy = `golang-go:
  Installed: (none)
  Candidate: 2:1.22~2build1
  Version table:
rustup:
  Installed: (none)
  Candidate: 1.26.0-5ubuntu0.1
nodejs:
  Installed: (none)
  Candidate: 18.19.1+dfsg-6ubuntu5
npm:
  Installed: (none)
  Candidate: 9.2.0~ds1-2
docker-compose-v2:
  Installed: (none)
  Candidate: 2.40.3+ds1-0ubuntu1~24.04.1
docker-compose:
  Installed: (none)
  Candidate: 1.29.2-6ubuntu1
`

func TestPlanUbuntu(t *testing.T) {
	f := ubuntu()
	remove(f, "go", "rustup", "cargo", "node", "npm", "pnpm", "docker", "cc", "gcc", "clang")
	f.have["apt-cache"] = ""
	f.out["apt-cache policy golang-go"] = aptPolicy
	for _, k := range []string{"rustup", "nodejs", "nodejs npm", "docker-compose-v2 docker-compose docker-compose-plugin"} {
		f.out["apt-cache policy "+k] = aptPolicy
	}
	p := plan(t, env(f, "linux"))
	if p.Manager.ID != Apt {
		t.Fatalf("%+v", p.Manager)
	}
	same(t, commands(p),
		"sudo apt-get update",
		"sudo apt-get install -y --no-remove build-essential golang-go rustup docker.io docker-compose-v2",
		"rustup default stable",
		"sudo snap install node --classic --channel=22/stable",
		"npm install --global --prefix "+"/home/ana/.local"+" pnpm@11.5.3",
		"sudo systemctl enable --now docker",
		"sudo usermod -aG docker ana",
	)
	if p.Elevated() != 5 || len(p.Manual) != 0 || len(p.Notes) != 1 || !strings.Contains(p.Notes[0], "grupo docker") {
		t.Fatalf("%d %+v %+v", p.Elevated(), p.Manual, p.Notes)
	}

	// Node 18 from apt and no snap: Node by hand, with the version the checkout needs (and pnpm,
	// which needs its npm).
	remove(f, "snap")
	p = plan(t, env(f, "linux"))
	if len(p.Manual) != 2 || p.Manual[0].Tool != Node || !strings.Contains(p.Manual[0].Fix, "22.23.4") || p.Manual[1].Tool != PNPM {
		t.Fatalf("%+v", p.Manual)
	}
}

func TestPlanAptNodeSourceAndRoot(t *testing.T) {
	f := ubuntu()
	remove(f, "node", "npm", "pnpm", "sudo")
	f.have["apt-cache"] = ""
	f.out["apt-cache policy nodejs"] = "nodejs:\n  Installed: (none)\n  Candidate: 22.23.4-1nodesource1\n"
	f.out["apt-cache policy nodejs npm"] = f.out["apt-cache policy nodejs"] + "npm:\n  Candidate: 9.2.0~ds1-2\n"
	e := env(f, "linux")
	e.Root = true
	p := plan(t, e)
	// NodeSource's nodejs carries npm (Ubuntu's npm would conflict); as root, no sudo.
	same(t, commands(p),
		"apt-get update",
		"apt-get install -y --no-remove nodejs",
		"npm install --global --prefix "+"/home/ana/.local"+" pnpm@11.5.3",
	)
	// Not root and no sudo: the root commands become manual steps.
	e.Root = false
	p = plan(t, e)
	same(t, commands(p), "npm install --global --prefix "+"/home/ana/.local"+" pnpm@11.5.3")
	if len(p.Manual) != 2 || !strings.Contains(p.Manual[1].Fix, "Como root: apt-get install -y --no-remove nodejs") {
		t.Fatalf("%+v", p.Manual)
	}
}

func TestPlanAptEmptyLists(t *testing.T) {
	// A fresh WSL or container: no apt lists, so apt cannot say what it would install. Assuming
	// would pick Ubuntu's Node 18 without npm; the first plan only refreshes the lists.
	f := ubuntu()
	remove(f, "node", "npm", "snap", "git")
	f.have["apt-cache"] = ""
	e := env(f, "linux")
	e.Glob = func(string) ([]string, error) { return nil, nil }
	p := plan(t, e)
	same(t, commands(p), "sudo apt-get update")
	if len(p.Notes) != 1 || !strings.Contains(p.Notes[0], "listas do apt") {
		t.Fatalf("%+v", p.Notes)
	}
	// With the lists, a package apt does not know is missing, not assumed.
	e.Glob = aptLists
	f.out["apt-cache policy git"] = "git:\n  Installed: (none)\n  Candidate: 1:2.43.0-1ubuntu7\n"
	f.out["apt-cache policy nodejs"] = ""
	p = plan(t, e)
	same(t, commands(p), "sudo apt-get update", "sudo apt-get install -y --no-remove git")
	if len(p.Manual) != 1 || p.Manual[0].Tool != Node {
		t.Fatalf("%+v", p.Manual)
	}
	// Only pnpm missing: nothing comes from apt, no refresh needed.
	f = ubuntu()
	remove(f, "pnpm")
	e = env(f, "linux")
	e.Glob = func(string) ([]string, error) { return nil, nil }
	same(t, commands(plan(t, e)), "npm install --global --prefix /home/ana/.local pnpm@11.5.3")
}

func TestPlanFedora(t *testing.T) {
	f := ubuntu()
	remove(f, "apt-get", "snap", "go", "rustup", "cargo", "node", "npm", "pnpm", "docker", "cc", "gcc", "clang")
	f.have["dnf"] = ""
	f.out["dnf repoquery --quiet --latest-limit 1 --queryformat %{name} %{version}\n golang"] = "golang 1.25.3"
	f.out["dnf repoquery --quiet --latest-limit 1 --queryformat %{name} %{version}\n rustup"] = "rustup 1.28.2"
	f.out["dnf repoquery --quiet --latest-limit 1 --queryformat %{name} %{version}\n nodejs nodejs22"] = "nodejs 20.19.5\nnodejs22 22.23.4"
	f.out["dnf repoquery --quiet --latest-limit 1 --queryformat %{name} %{version}\n nodejs22 npm"] = "nodejs22 22.23.4\nnpm 10.9.4"
	f.out["dnf repoquery --quiet --latest-limit 1 --queryformat %{name} %{version}\n docker-compose docker-compose-plugin"] = "docker-compose 2.39.2"
	p := plan(t, env(f, "linux"))
	if p.Manager.ID != Dnf {
		t.Fatalf("%+v", p.Manager)
	}
	same(t, commands(p),
		"sudo dnf install -y gcc golang rustup nodejs22 npm moby-engine docker-compose",
		"rustup-init -y --no-modify-path --default-toolchain stable",
		"npm install --global --prefix "+"/home/ana/.local"+" pnpm@11.5.3",
		"sudo systemctl enable --now docker",
		"sudo usermod -aG docker ana",
	)
}

func TestPlanMac(t *testing.T) {
	f := ubuntu()
	remove(f, "apt-get", "go", "rustup", "cargo", "node", "npm", "pnpm", "docker")
	f.have["brew"], f.have["xcode-select"] = "", ""
	f.fail["xcode-select -p"] = true
	p := plan(t, env(f, "darwin"))
	if p.Manager.ID != Brew || p.Manager.Name != "Homebrew" {
		t.Fatalf("%+v", p.Manager)
	}
	// Without the Command Line Tools only their dialog runs: Homebrew needs them, and
	// xcode-select returns before they are installed. The next check plans the rest.
	same(t, commands(p), "xcode-select --install")
	if len(p.Notes) != 1 || !strings.Contains(p.Notes[0], "janela") {
		t.Fatalf("%+v", p.Notes)
	}
	delete(f.fail, "xcode-select -p")
	p = plan(t, env(f, "darwin"))
	same(t, commands(p),
		"brew install go rustup node@22",
		"brew install --cask docker",
		"open -a Docker",
		"rustup default stable",
		"npm install --global --prefix "+"/home/ana/.local"+" pnpm@11.5.3",
	)
	if p.Elevated() != 1 || len(p.Notes) != 1 {
		t.Fatalf("só o cask pode pedir senha: %d %+v", p.Elevated(), p.Notes)
	}
	f.fail["xcode-select -p"] = true

	// Without Homebrew everything is by hand, and the fix says how to get Homebrew.
	remove(f, "brew")
	p = plan(t, env(f, "darwin"))
	if !p.Empty() || p.Manager.ID != "" || len(p.Manual) != 8 {
		t.Fatalf("%+v", p)
	}
	for _, m := range p.Manual {
		if want := map[string]string{Make: "xcode-select", CC: "xcode-select", Git: "xcode-select", Docker: "Docker Desktop"}[m.Tool]; want != "" && !strings.Contains(m.Fix, want) {
			t.Errorf("%s: %s", m.Tool, m.Fix)
		} else if want == "" && !strings.Contains(m.Fix, "brew.sh") {
			t.Errorf("%s: %s", m.Tool, m.Fix)
		}
	}
}

func TestPlanWindows(t *testing.T) {
	f := &fake{have: map[string]string{"wsl": "", "winget": ""}, out: map[string]string{}, fail: map[string]bool{"wsl --status": true}}
	p := plan(t, env(f, "windows"))
	same(t, commands(p),
		"wsl --install -d Ubuntu",
		"winget install --exact --id Docker.DockerDesktop --accept-package-agreements --accept-source-agreements",
	)
	if p.Elevated() != 2 || len(p.Notes) != 2 {
		t.Fatalf("%+v", p)
	}
	delete(f.have, "winget")
	if p = plan(t, env(f, "windows")); !p.Empty() || len(p.Manual) != 2 || !strings.Contains(p.Manual[0].Fix, "wsl --install") {
		t.Fatalf("sem winget: %+v", p)
	}
}

func TestPlanInsideWSL(t *testing.T) {
	f := ubuntu()
	remove(f, "docker", "go")
	f.have["apt-cache"] = ""
	f.out["apt-cache policy golang-go"] = aptPolicy
	e := env(f, "linux")
	e.Info.WSL = true
	p := plan(t, e)
	same(t, commands(p), "sudo apt-get update", "sudo apt-get install -y --no-remove golang-go")
	if len(p.Manual) != 1 || p.Manual[0].Tool != Docker || !strings.Contains(p.Manual[0].Fix, "WSL integration") {
		t.Fatalf("no WSL, o Docker vem do Docker Desktop: %+v", p.Manual)
	}
}

func TestPlanGoToolchainLocal(t *testing.T) {
	f := ubuntu()
	e := env(f, "linux")
	e.Getenv = func(k string) (string, bool) { return "local", k == "GOTOOLCHAIN" }
	p := plan(t, e)
	if !p.Empty() || len(p.Manual) != 1 || !strings.Contains(p.Manual[0].Fix, "GOTOOLCHAIN=auto") {
		t.Fatalf("instalar outro Go não resolve: %+v", p)
	}
	if p := plan(t, env(ubuntu(), "linux")); !p.Empty() || len(p.Manual) != 0 {
		t.Fatalf("nada a fazer: %+v", p)
	}
}

func TestParseAptPolicy(t *testing.T) {
	out := map[string]string{}
	ParseAptPolicy(aptPolicy+"ghost:\n  Installed: (none)\n  Candidate: (none)\n", out)
	if out["golang-go"] != "1.22~2build1" || out["nodejs"] != "18.19.1+dfsg-6ubuntu5" || len(out) != 6 {
		t.Fatalf("%v", out)
	}
}

func TestCommandString(t *testing.T) {
	c := Command{Args: []string{"npm", "install", "--prefix", "/home/ana maria/.local", "it's"}}
	if got := c.String(); got != `npm install --prefix '/home/ana maria/.local' 'it'\''s'` {
		t.Fatal(got)
	}
}

func TestAugmentPath(t *testing.T) {
	exists := map[string]bool{
		"/home/ana/.cargo/bin": true, "/snap/bin": true, "/home/ana/.local/bin": true,
		"/opt/homebrew/opt/node@22/bin": true, "/opt/homebrew/bin": true,
	}
	e := env(ubuntu(), "linux")
	e.Exists = func(p string) bool { return exists[p] }
	path, added := e.AugmentPath("/usr/bin:/snap/bin", Defaults)
	cargo, local := "/home/ana/.cargo/bin", "/home/ana/.local/bin"
	if path != cargo+":/usr/bin:/snap/bin:"+local || strings.Join(added, ",") != cargo+","+local {
		t.Fatalf("%q %q", path, added)
	}
	if again, more := e.AugmentPath(path, Defaults); again != path || len(more) != 0 {
		t.Fatalf("de novo não muda: %q %v", again, more)
	}
	e.Info.OS = "darwin"
	path, _ = e.AugmentPath("/usr/bin", Defaults)
	if !strings.HasPrefix(path, cargo+":/opt/homebrew/opt/node@22/bin:/usr/bin") || !strings.Contains(path, ":/opt/homebrew/bin") {
		t.Fatalf("o Node do Homebrew (keg) vem antes: %q", path)
	}
	w := Env{Info: platform.Info{OS: "windows"}, Home: `C:\Users\ana`, Getenv: func(k string) (string, bool) {
		return map[string]string{"LOCALAPPDATA": `C:\Users\ana\AppData\Local`}[k], k == "LOCALAPPDATA"
	}, Exists: func(string) bool { return true }}
	path, added = w.AugmentPath(`C:\Windows`, Defaults)
	if !strings.HasPrefix(path, `C:\Windows;`) || len(added) != 1 || !strings.Contains(added[0], "WinGet") {
		t.Fatalf("%q %v", path, added)
	}
}

func TestRunPlan(t *testing.T) {
	cmds := []Command{{Args: []string{"sudo", "apt-get", "update"}, Elevated: true}, {Args: []string{"sudo", "apt-get", "install", "-y", "go"}}, {Args: []string{"rustup", "default", "stable"}}}
	var ran []string
	var before int
	var out bytes.Buffer
	results := RunPlan(context.Background(), cmds, Terminal{Out: &out}, func(_ context.Context, args []string, term Terminal) error {
		ran = append(ran, strings.Join(args, " "))
		if args[1] == "apt-get" && args[2] == "install" {
			return errors.New("E: Unable to locate package go")
		}
		return nil
	}, func(Command) { before++ })
	if len(ran) != 2 || before != 2 || !Failed(results) {
		t.Fatalf("para na primeira falha: %v %d", ran, before)
	}
	if results[0].Err != nil || results[1].Err == nil || results[1].ExitCode != -1 || !results[2].Skipped {
		t.Fatalf("%+v", results)
	}
	if !strings.Contains(out.String(), "==> sudo apt-get update\n") {
		t.Fatalf("cada comando aparece antes de rodar: %q", out.String())
	}
}
