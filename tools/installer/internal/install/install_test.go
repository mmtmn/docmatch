package install

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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
	dev := New(Options{Mode: state.ModeDev, Ports: ports.Defaults()}, nil)
	if got := ids(dev.Steps()); got != "dev_local,verify,demo" {
		t.Fatal(got)
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
	if SourceRevision(context.Background(), "") != "" || SourceRevision(context.Background(), t.TempDir()) != "" {
		t.Fatal("fora de um checkout não há revisão")
	}
}
