package install

import (
	"bufio"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/mmtmn/docmatch/tools/installer/internal/assets"
	"github.com/mmtmn/docmatch/tools/installer/internal/envfile"
	"github.com/mmtmn/docmatch/tools/installer/internal/flow"
	"github.com/mmtmn/docmatch/tools/installer/internal/ports"
	"github.com/mmtmn/docmatch/tools/installer/internal/state"
)

func TestRemotes(t *testing.T) {
	for remote, want := range map[string][2]string{
		"git@github.com:Owner/docmatch.git":       {"ghcr.io/owner", "https://github.com/Owner/docmatch"},
		"ssh://git@github.com/Owner/docmatch":     {"ghcr.io/owner", "https://github.com/Owner/docmatch"},
		"https://github.com/owner/docmatch.git\n": {"ghcr.io/owner", "https://github.com/owner/docmatch"},
		"https://gitlab.example.com/o/docmatch":   {"", ""},
		"":                                        {"", ""},
		"https://github.com/onlyowner":            {"", ""},
	} {
		if got := RegistryFromRemote(remote); got != want[0] {
			t.Errorf("RegistryFromRemote(%q) = %q; quero %q", remote, got, want[0])
		}
		if got := CloneURL(remote); got != want[1] {
			t.Errorf("CloneURL(%q) = %q; quero %q", remote, got, want[1])
		}
	}
}

func TestServiceOf(t *testing.T) {
	for _, c := range [][3]string{
		{"docmatch-local", "docmatch-local-api-1", "api"},
		{"dm-instalador-teste", "dm-instalador-teste-harness-12", "harness"},
		{"docmatch-local", "docmatch-local-web", "web"},
		{"docmatch-local", "outro", "outro"},
	} {
		if got := ServiceOf(c[0], c[1]); got != c[2] {
			t.Errorf("ServiceOf(%q, %q) = %q; quero %q", c[0], c[1], got, c[2])
		}
	}
}

func repo(t *testing.T) string {
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

func TestFindSource(t *testing.T) {
	root := repo(t)
	deep := filepath.Join(root, "tools", "installer", "internal")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := FindSource("", t.TempDir(), deep); got != root {
		t.Fatalf("FindSource = %q", got)
	}
	if FindSource(t.TempDir()) != "" || IsSource("") {
		t.Fatal("fora do repositório")
	}
	// The real checkout this test runs in.
	if got := FindSource("."); got == "" || !IsSource(got) {
		t.Skip("fora do repositório da docmatch")
	}
}

func TestOptions(t *testing.T) {
	o := Options{Mode: state.ModeTry, Ports: ports.Set{ports.Web: 14321, ports.API: 18080, ports.Postgres: 15432, ports.Mailpit: 18025}, Registry: "ghcr.io/owner", Tag: "0.1.0"}
	u := o.URLs()
	if u.App != "http://localhost:14321/app" || u.API != "http://localhost:18080" || u.Mailpit != "http://localhost:18025" {
		t.Fatalf("%+v", u)
	}
	if o.Image("api") != "ghcr.io/owner/docmatch-api:0.1.0" || !o.Pullable() {
		t.Fatal(o.Image("api"))
	}
	o.Build = BuildAlways
	if o.Pullable() {
		t.Fatal("--build nunca baixa")
	}
	o.Registry, o.Build = "", BuildAuto
	if o.Image("web") != "docmatch.local/docmatch-web:0.1.0" || o.Pullable() {
		t.Fatal("sem registro: só local")
	}
}

func TestDevURLs(t *testing.T) {
	dir := t.TempDir()
	if u := DevURLs(dir); u.App != "http://localhost:4321/app" {
		t.Fatalf("sem .env: %+v", u)
	}
	env := "DM_PUBLIC_BASE_URL=http://localhost:5555/\nDM_API_BASE_URL=http://localhost:6666\nDM_DEV_MAILPIT_PORT=7777\n"
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(env), 0o600); err != nil {
		t.Fatal(err)
	}
	u := (Options{Mode: state.ModeDev, SourceDir: dir}).URLs()
	if u.App != "http://localhost:5555/app" || u.API != "http://localhost:6666" || u.Mailpit != "http://localhost:7777" {
		t.Fatalf("%+v", u)
	}
}

func TestRemoveInstallation(t *testing.T) {
	fill := func(dir string, names ...string) {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for _, n := range names {
			p := filepath.Join(dir, n)
			if n == "logs" || n == "src" || n == "projeto" {
				if err := os.MkdirAll(filepath.Join(p, "x"), 0o755); err != nil {
					t.Fatal(err)
				}
				continue
			}
			if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	ours := filepath.Join(t.TempDir(), "local")
	fill(ours, "compose.yaml", ".env", "state.json", "logs", "src")
	if left, err := RemoveInstallation(ours); err != nil || left != nil {
		t.Fatalf("%v %v", left, err)
	}
	if _, err := os.Stat(ours); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("só com o que é nosso, a pasta sai")
	}
	// A --dir that pointed at a busy folder: only our files go; src/ may not be ours.
	busy := t.TempDir()
	fill(busy, "compose.yaml", ".env", "state.json", "logs", "src", "projeto", "notas.txt")
	left, err := RemoveInstallation(busy)
	if err != nil || strings.Join(left, ",") != "notas.txt,projeto,src" {
		t.Fatalf("%v %v", left, err)
	}
	for _, n := range []string{"projeto", "notas.txt", "src"} {
		if _, err := os.Stat(filepath.Join(busy, n)); err != nil {
			t.Fatalf("%s devia ficar", n)
		}
	}
	if _, err := os.Stat(filepath.Join(busy, ".env")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("o .env é nosso")
	}
}

func TestDevPorts(t *testing.T) {
	dir := t.TempDir()
	if DevPorts(dir).String() != ports.Defaults().String() {
		t.Fatal("sem .env, os padrões")
	}
	env := "DM_PUBLIC_BASE_URL=http://localhost:14321\nDM_API_BASE_URL=http://localhost:18080\n" +
		"DATABASE_URL=postgres://docmatch:docmatch@localhost:15432/docmatch?sslmode=disable\nDM_DEV_MAILPIT_PORT=18025\n"
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(env), 0o600); err != nil {
		t.Fatal(err)
	}
	p := DevPorts(dir)
	if p[ports.Web] != 14321 || p[ports.API] != 18080 || p[ports.Postgres] != 15432 || p[ports.Mailpit] != 18025 {
		t.Fatalf("%v", p)
	}
}

func TestStepsPerMode(t *testing.T) {
	ids := func(steps []flow.Step) string {
		out := make([]string, 0, len(steps))
		for _, s := range steps {
			out = append(out, s.ID)
			if s.Title == "" || strings.HasPrefix(s.Title, "[") {
				t.Errorf("%s sem título", s.ID)
			}
		}
		return strings.Join(out, ",")
	}
	try := New(Options{Mode: state.ModeTry, Ports: ports.Defaults()}, nil)
	if got := ids(try.Steps()); got != "prepare,base_images,images,database,migrate,services,verify,demo" {
		t.Fatal(got)
	}
	dev := New(Options{Mode: state.ModeDev, Ports: ports.Defaults(), CloneDir: filepath.Join(t.TempDir(), "docmatch")}, nil)
	if got := ids(dev.Steps()); got != "dev_clone,dev_setup,dev_local,verify,demo" {
		t.Fatal("sem checkout clona primeiro: " + got)
	}
	dev = New(Options{Mode: state.ModeDev, Ports: ports.Defaults(), SourceDir: repo(t)}, nil)
	if got := ids(dev.Steps()); got != "dev_setup,dev_local,verify,demo" {
		t.Fatal("com checkout: " + got)
	}
}

func TestResolvedAndCanClone(t *testing.T) {
	src := repo(t)
	o := Options{Mode: state.ModeDev, CloneDir: src}.Resolved()
	if o.SourceDir != src {
		t.Fatal("um clone anterior na pasta de clone vira o código")
	}
	if (Options{Mode: state.ModeTry, CloneDir: src}).Resolved().SourceDir != "" {
		t.Fatal("Experimentar não usa a pasta de clone")
	}
	empty := t.TempDir()
	if !CanClone(empty) || !CanClone(filepath.Join(empty, "nova")) || CanClone(src) {
		t.Fatal("CanClone: nova ou vazia, nunca uma pasta com arquivos")
	}
}

func TestDevEnv(t *testing.T) {
	env := DevEnv([]string{"PATH=/bin", "HOME=/h"}, "")
	if env[len(env)-1] != "GOTOOLCHAIN=auto" || len(env) != 3 {
		t.Fatalf("%v", env)
	}
	if env = DevEnv(nil, "go1.27.3"); env[0] != "GOTOOLCHAIN=go1.27.3" {
		t.Fatalf("%v", env)
	}
	env = DevEnv([]string{"GOTOOLCHAIN=local"}, "go1.27.3")
	if len(env) != 1 || env[0] != "GOTOOLCHAIN=local" {
		t.Fatalf("a escolha do usuário fica: %v", env)
	}
}

func TestScanLinesCR(t *testing.T) {
	in := "Receiving objects:  10% (1/10)\rReceiving objects: 100% (10/10), done.\r\nlinha\nfim"
	sc := bufio.NewScanner(strings.NewReader(in))
	sc.Split(ScanLinesCR)
	var got []string
	for sc.Scan() {
		got = append(got, sc.Text())
	}
	want := []string{"Receiving objects:  10% (1/10)", "Receiving objects: 100% (10/10), done.", "linha", "fim"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("%q", got)
	}
}

func TestCloneProgress(t *testing.T) {
	var bars []flow.Bar
	var logs []string
	r := flow.NewReporter(StepDevClone, func(e flow.Event) {
		switch e.Kind {
		case flow.Progressed:
			bars = append(bars, e.Bars...)
		case flow.Logged:
			logs = append(logs, e.Line)
		}
	})
	feed := cloneProgress(r)
	for _, l := range []string{"Cloning into '/x'...", "remote: Counting objects:  50% (5/10)", "Receiving objects:  45% (450/1000), 1.2 MiB | 2 MiB/s", "Resolving deltas: 100% (30/30), done."} {
		feed(l)
	}
	if len(bars) != 2 || bars[0].Label != "objetos" || bars[0].Fraction != 0.45 || bars[1].Label != "deltas" || bars[1].Text != "100%" {
		t.Fatalf("%+v", bars)
	}
	if len(logs) != 2 || logs[0] != "Cloning into '/x'..." {
		t.Fatalf("%q", logs)
	}
}

func TestSubsystems(t *testing.T) {
	dir := t.TempDir()
	mk := "X := 1\nSUBSYSTEMS := packages/contracts services/api \\\n              apps/web infra\n\nall:\n"
	if err := os.WriteFile(filepath.Join(dir, "Makefile"), []byte(mk), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(Subsystems(dir), ","); got != "packages/contracts,services/api,apps/web,infra" {
		t.Fatal(got)
	}
	if Subsystems(t.TempDir()) != nil {
		t.Fatal("sem Makefile")
	}
	// The real root Makefile still declares the list this step counts.
	if root := FindSource("."); root != "" && len(Subsystems(root)) < 5 {
		t.Fatalf("SUBSYSTEMS do Makefile da raiz: %v", Subsystems(root))
	}
}

// fakeMake puts a `make` on the PATH that prints what make setup would and exits with code.
func fakeMake(t *testing.T, script string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("make falso em sh")
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "make"), []byte("#!/bin/sh\n"+script), 0o755); err != nil { //nolint:gosec // test helper
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestDevSetupProgress(t *testing.T) {
	src := repo(t)
	if err := os.WriteFile(filepath.Join(src, "Makefile"), []byte("SUBSYSTEMS := services/api apps/web\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// make prints the physical path (pwd -P), as on macOS where /var is /private/var: the
	// checkout is reached through a symlink here.
	fakeMake(t, `echo "hooks ativados"
echo "make[1]: Entering directory '$(pwd -P)/services/api'"
echo "make[1]: Entering directory '$(pwd -P)/apps/web'"
test "$GOTOOLCHAIN" = auto || exit 3
`)
	t.Setenv("GOTOOLCHAIN", "") // empty counts as unset
	link := filepath.Join(t.TempDir(), "atalho")
	if err := os.Symlink(src, link); err != nil {
		t.Skip("sem symlink:", err)
	}
	dir := t.TempDir()
	in := New(Options{Mode: state.ModeDev, Dir: dir, SourceDir: link}, nil)
	var texts []string
	r := flow.NewReporter(StepDevSetup, func(e flow.Event) {
		for _, b := range e.Bars {
			texts = append(texts, b.Text)
		}
	})
	res, err := in.devSetup(context.Background(), r)
	if err != nil || res.Outcome != flow.OK {
		t.Fatalf("%+v %v", res, err)
	}
	if strings.Join(texts, "|") != "1/2 services/api|2/2 apps/web" {
		t.Fatalf("%q", texts)
	}
	// Resuming the same checkout skips it; another checkout runs it again.
	in.State.Mark(StepDevSetup)
	again := New(Options{Mode: state.ModeDev, Dir: dir, SourceDir: link}, in.State)
	if res, _ := again.devSetup(context.Background(), r); res.Outcome != flow.Skipped {
		t.Fatal("make setup já rodou nesta pasta: retomar pula")
	}
	other := New(Options{Mode: state.ModeDev, Dir: dir, SourceDir: src}, again.State)
	if res, _ := other.devSetup(context.Background(), r); res.Outcome != flow.OK {
		t.Fatal("outro checkout roda o make setup")
	}
}

func TestDevSetupFails(t *testing.T) {
	src := repo(t)
	fakeMake(t, "echo 'cargo: command not found' >&2\nexit 2\n")
	in := New(Options{Mode: state.ModeDev, Dir: t.TempDir(), SourceDir: src}, nil)
	_, err := in.devSetup(context.Background(), flow.NewReporter(StepDevSetup, func(flow.Event) {}))
	if err == nil || !strings.Contains(err.Error(), "cargo: command not found") {
		t.Fatalf("a última linha explica a falha: %v", err)
	}
}

func TestDevClone(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("sem git")
	}
	// A local repository with the files of a checkout stands in for GitHub.
	origin := repo(t)
	for _, args := range [][]string{{"init", "-q"}, {"add", "."}, {"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "x"}} {
		cmd := gitCommand(context.Background(), append([]string{"-C", origin}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	dir := t.TempDir()
	dest := filepath.Join(t.TempDir(), "docmatch")
	in := New(Options{Mode: state.ModeDev, Dir: dir, CloneDir: dest, RepoURL: origin}, nil)
	res, err := in.devClone(context.Background(), flow.NewReporter(StepDevClone, func(flow.Event) {}))
	if err != nil || res.Detail != dest || in.Opts.SourceDir != dest || !IsSource(dest) {
		t.Fatalf("%+v %v %q", res, err, in.Opts.SourceDir)
	}
	if st, _, _ := state.Load(dir); st == nil || st.SourceDir != dest {
		t.Fatalf("o estado lembra o clone: %+v", st)
	}
	in = New(Options{Mode: state.ModeDev, Dir: dir, CloneDir: dest, RepoURL: origin}, nil)
	if _, err := in.devClone(context.Background(), flow.NewReporter(StepDevClone, func(flow.Event) {})); err == nil || !strings.Contains(err.Error(), "não está vazia") {
		t.Fatalf("pasta ocupada: %v", err)
	}
	// A file where the clone would go stays untouched.
	file := filepath.Join(t.TempDir(), "docmatch")
	if err := os.WriteFile(file, []byte("meu"), 0o600); err != nil {
		t.Fatal(err)
	}
	in = New(Options{Mode: state.ModeDev, Dir: dir, CloneDir: file, RepoURL: origin}, nil)
	if _, err := in.devClone(context.Background(), flow.NewReporter(StepDevClone, func(flow.Event) {})); err == nil {
		t.Fatal("um arquivo não vira clone")
	}
	if data, err := os.ReadFile(file); err != nil || string(data) != "meu" { //nolint:gosec // test file
		t.Fatalf("o arquivo fica: %q %v", data, err)
	}
	in = New(Options{Mode: state.ModeDev, Dir: dir, CloneDir: filepath.Join(t.TempDir(), "x"), RepoURL: filepath.Join(t.TempDir(), "nao-existe")}, nil)
	_, err = in.devClone(context.Background(), flow.NewReporter(StepDevClone, func(flow.Event) {}))
	if err == nil || !strings.Contains(err.Error(), "SSH") {
		t.Fatalf("falha de clone explica o repositório privado: %v", err)
	}
}

func TestPrepareWritesTheFolder(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "inst")
	in := New(Options{Mode: state.ModeTry, Dir: dir, Project: "p", Ports: ports.Defaults(), Tag: "dev", LLMMode: envfile.LLMOff}, nil)
	if err := in.Prepare(); err != nil {
		t.Fatal(err)
	}
	compose, _ := os.ReadFile(in.ComposeFile())
	if string(compose) != string(assets.Compose) {
		t.Fatal("compose.yaml diferente do embarcado")
	}
	env, err := envfile.Read(in.EnvFile())
	if err != nil || env["DM_IMAGE_REGISTRY"] != LocalRegistry || env["DM_SESSION_SECRET"] == "" || env["DM_SOURCE_DIR"] != "" {
		t.Fatalf("%v %v", env, err)
	}
	st, found, err := state.Load(dir)
	if err != nil || !found || st.Project != "p" {
		t.Fatalf("%+v %v", st, err)
	}
	in.MarkDone(StepPrepare)
	st, _, _ = state.Load(dir)
	if !st.Done(StepPrepare) {
		t.Fatal("etapa não registrada")
	}
}

func TestDemoSkippedAndCloneWithoutRepo(t *testing.T) {
	in := New(Options{Mode: state.ModeTry, Dir: t.TempDir(), Ports: ports.Defaults()}, nil)
	res, err := in.demo(context.Background(), flow.NewReporter("demo", func(flow.Event) {}))
	if err != nil || res.Outcome != flow.Skipped {
		t.Fatalf("%+v %v", res, err)
	}
	if err := in.clone(context.Background(), flow.NewReporter("images", func(flow.Event) {})); !errors.Is(err, ErrNoSource) {
		t.Fatalf("sem código e sem repositório: %v", err)
	}
}

func TestSourceRevision(t *testing.T) {
	// Inside a git hook GIT_DIR points at the repository being pushed; -C must still win.
	if out, err := exec.Command("git", "rev-parse", "--absolute-git-dir").Output(); err == nil {
		t.Setenv("GIT_DIR", strings.TrimSpace(string(out)))
	}
	if SourceRevision(context.Background(), "") != "" || SourceRevision(context.Background(), t.TempDir()) != "" {
		t.Fatal("fora de um checkout não há revisão")
	}
	if Remote(context.Background(), t.TempDir()) != "" {
		t.Fatal("fora de um checkout não há remoto")
	}
	if cmd := gitCommand(context.Background(), "status"); strings.Contains(strings.Join(cmd.Env, "\n"), "GIT_DIR=") {
		t.Fatal("GIT_DIR não pode chegar ao git")
	}
}
