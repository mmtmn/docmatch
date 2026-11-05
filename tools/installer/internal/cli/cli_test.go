package cli

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mmtmn/docmatch/tools/installer/internal/envfile"
	"github.com/mmtmn/docmatch/tools/installer/internal/install"
	"github.com/mmtmn/docmatch/tools/installer/internal/platform"
	"github.com/mmtmn/docmatch/tools/installer/internal/ports"
	"github.com/mmtmn/docmatch/tools/installer/internal/state"
)

func testWorld(t *testing.T, env map[string]string) (world, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	var out, errb bytes.Buffer
	home := t.TempDir()
	return world{
		env:  func(k string) (string, bool) { v, ok := env[k]; return v, ok },
		info: platform.Info{OS: "linux", Arch: "amd64"}, home: home, cwd: t.TempDir(), exe: filepath.Join(t.TempDir(), "docmatch"),
		stdout: &out, stderr: &errb, stdin: strings.NewReader(""), isTTY: true, now: time.Now, version: "dev",
	}, &out, &errb
}

func flags(t *testing.T, args ...string) installFlags {
	t.Helper()
	var f installFlags
	fs := newFlagSet("t")
	f.register(fs)
	if err := fs.Parse(args); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestResolveDefaults(t *testing.T) {
	w, _, _ := testWorld(t, nil)
	r, err := resolveOptions(context.Background(), w, flags(t), Build{Version: "dev"})
	if err != nil {
		t.Fatal(err)
	}
	o := r.opts
	if o.Mode != state.ModeTry || o.Project != DefaultProject || o.Ports.String() != ports.Defaults().String() || o.LLMMode != envfile.LLMOff {
		t.Fatalf("%+v", o)
	}
	if o.Dir != filepath.Join(w.home, ".local", "share", "docmatch", "local") || o.Tag != "dev" || o.Registry != install.LocalRegistry || !o.Demo || o.Build != install.BuildAuto {
		t.Fatalf("%+v", o)
	}
	if r.st != nil || r.fixedMode || len(r.explicit) != 0 {
		t.Fatalf("%+v", r)
	}
}

func TestResolvePrecedence(t *testing.T) {
	w, _, _ := testWorld(t, map[string]string{"DOCMATCH_PROJECT": "do-ambiente", "OPENROUTER_API_KEY": "sk-ambiente", "DOCMATCH_REGISTRY": "ghcr.io/amb"})
	dir := filepath.Join(t.TempDir(), "inst")
	saved := &state.State{Mode: state.ModeTry, Project: "salvo", Ports: ports.Set{ports.Web: 24321, ports.API: 28080, ports.Postgres: 25432, ports.Mailpit: 28025},
		LLMMode: envfile.LLMOpenRouter, Registry: "ghcr.io/salvo", Tag: "0.0.9"}
	if err := saved.Save(dir); err != nil {
		t.Fatal(err)
	}
	r, err := resolveOptions(context.Background(), w, flags(t, "--dir", dir, "--port-api", "19090", "--ports", "web=14321"), Build{Version: "0.1.0", Registry: "ghcr.io/build"})
	if err != nil {
		t.Fatal(err)
	}
	o := r.opts
	if r.st == nil || o.Project != "do-ambiente" {
		t.Fatalf("o ambiente vence o estado: %+v", o)
	}
	if o.Ports[ports.Web] != 14321 || o.Ports[ports.API] != 19090 || o.Ports[ports.Postgres] != 25432 || !r.explicit[ports.Web] || !r.explicit[ports.API] || r.explicit[ports.Postgres] {
		t.Fatalf("portas: %v %v", o.Ports, r.explicit)
	}
	if o.LLMMode != envfile.LLMOpenRouter || o.OpenRouter != "sk-ambiente" || o.Registry != "ghcr.io/amb" {
		t.Fatalf("%+v", o)
	}
	// A release binary pulls its own version, not the saved tag.
	if o.Tag != "0.1.0" {
		t.Fatalf("tag %q", o.Tag)
	}
	r, _ = resolveOptions(context.Background(), w, flags(t, "--dir", dir, "--project", "da-flag", "--tag", "x", "--mode", "dev", "--build"), Build{Version: "dev"})
	if r.opts.Project != "da-flag" || r.opts.Tag != "x" || r.opts.Mode != state.ModeDev || !r.fixedMode || r.opts.Build != install.BuildAlways {
		t.Fatalf("%+v", r.opts)
	}
}

func TestResolveErrors(t *testing.T) {
	w, _, _ := testWorld(t, nil)
	for _, args := range [][]string{
		{"--mode", "talvez"}, {"--port-web", "80"}, {"--ports", "web=8080"}, {"--llm", "gpt"},
		{"--llm", "local", "--ollama-url", "localhost:11434"}, {"--build", "--no-build"}, {"--source", "/nao/existe"},
	} {
		if _, err := resolveOptions(context.Background(), w, flags(t, args...), Build{}); !errors.Is(err, errUsage) {
			t.Errorf("%v: %v", args, err)
		}
	}
	r, err := resolveOptions(context.Background(), w, flags(t, "--llm", "local"), Build{})
	if err != nil || r.opts.LocalLLM != "http://localhost:11434/v1" {
		t.Fatalf("Ollama padrão: %+v %v", r.opts, err)
	}
}

func TestPrefs(t *testing.T) {
	w, _, _ := testWorld(t, map[string]string{"LANG": "pt_BR.UTF-8"})
	if p := w.prefs(common{}); p.plain || !p.animate || p.ascii {
		t.Fatalf("terminal: %+v", p)
	}
	if p := w.prefs(common{noAnim: true}); p.animate {
		t.Fatal("--no-animation")
	}
	if p := w.prefs(common{ascii: true}); !p.ascii {
		t.Fatal("--ascii")
	}
	for _, env := range []map[string]string{{"NO_COLOR": "1"}, {"CI": "true"}, {"TERM": "dumb"}} {
		w2, _, _ := testWorld(t, env)
		if p := w2.prefs(common{}); !p.plain || p.animate {
			t.Fatalf("%v: %+v", env, p)
		}
	}
	w3, _, _ := testWorld(t, map[string]string{"DOCMATCH_NO_ANIMATION": "1", "LANG": "C"})
	if p := w3.prefs(common{}); p.animate || !p.ascii {
		t.Fatalf("%+v", p)
	}
	w.isTTY = false
	if p := w.prefs(common{}); !p.plain {
		t.Fatal("sem terminal")
	}
	if p := w.prefs(common{plain: true}); !p.plain {
		t.Fatal("--plain")
	}
}

func TestCommands(t *testing.T) {
	w, out, errb := testWorld(t, nil)
	if code := run(context.Background(), w, []string{"version"}, Build{Version: "0.1.0"}); code != 0 || out.String() != "docmatch 0.1.0\n" {
		t.Fatalf("%d %q", code, out)
	}
	out.Reset()
	if code := run(context.Background(), w, []string{"--help"}, Build{}); code != 0 || !strings.Contains(out.String(), "uso: docmatch") {
		t.Fatalf("%d %q", code, out)
	}
	if code := run(context.Background(), w, []string{"instalar"}, Build{}); code != exitUsage || !strings.Contains(errb.String(), "comando desconhecido: instalar") {
		t.Fatalf("%d %q", code, errb)
	}
	errb.Reset()
	if code := run(context.Background(), w, []string{"--nao-existe"}, Build{}); code != exitUsage {
		t.Fatalf("flag desconhecida: %d", code)
	}
	for _, cmd := range []string{"up", "down", "status", "logs", "demo"} {
		errb.Reset()
		code := run(context.Background(), w, []string{cmd, "--dir", filepath.Join(t.TempDir(), "vazia")}, Build{})
		if code != exitFailed || !strings.Contains(errb.String(), "nenhuma instalação") {
			t.Errorf("%s: %d %q", cmd, code, errb)
		}
	}
}
