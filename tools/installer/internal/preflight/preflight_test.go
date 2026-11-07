package preflight

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/mmtmn/docmatch/tools/installer/internal/platform"
	"github.com/mmtmn/docmatch/tools/installer/internal/ports"
	"github.com/mmtmn/docmatch/tools/installer/internal/state"
)

// fake is a machine with Docker 29, Compose 2.40, git and every tool, 50 GB free.
func fake() Env {
	return Env{
		Info: platform.Info{OS: "linux", Arch: "amd64"}, Mode: state.ModeTry, Dir: "/home/u/.local/share/docmatch/local",
		Ports: ports.Defaults(), SourceDir: "/src",
		FreeBytes: func(string) (uint64, error) { return 50_000_000_000, nil },
		PortFree:  func(int) bool { return true },
		LookPath:  func(name string) (string, error) { return "/usr/bin/" + name, nil },
		Output: func(_ context.Context, name string, _ ...string) (string, error) {
			return map[string]string{"git": "git version 2.43.0", "go": "go version go1.27.3 linux/amd64", "node": "v22.23.4",
				"pnpm": "11.5.3", "cargo": "cargo 1.93.1", "make": "GNU Make 4.3", "openssl": "OpenSSL 3.0.13", "curl": "curl 8.5.0"}[name], nil
		},
		DockerOutput: func(_ context.Context, args ...string) (string, error) {
			switch strings.Join(args, " ") {
			case "version --format {{.Client.Version}}", "version --format {{.Server.Version}}":
				return "29.1.3", nil
			case "compose version --short":
				return "2.40.3", nil
			}
			return "", errors.New("?")
		},
		DockerRoot: func(context.Context) string { return "/var/lib/docker" },
	}
}

func run(env Env) map[string]Result {
	out := map[string]Result{}
	checks := Checks(env)
	for i, r := range RunAll(context.Background(), checks) {
		out[checks[i].ID] = r
	}
	return out
}

func TestAllGood(t *testing.T) {
	r := run(fake())
	for id, res := range r {
		if res.Status != OK {
			t.Errorf("%s: %+v", id, res)
		}
	}
	if r["docker_running"].Detail != "servidor 29.1.3" || r["compose"].Detail != "2.40.3" || r["git"].Detail != "2.43.0" {
		t.Fatalf("%+v", r)
	}
	if _, ok := r["tool_go"]; ok {
		t.Fatal("Experimentar não confere toolchains")
	}
}

func TestDockerMissingSkipsTheRest(t *testing.T) {
	env := fake()
	env.LookPath = func(name string) (string, error) {
		if name == "docker" {
			return "", errors.New("not found")
		}
		return "/usr/bin/" + name, nil
	}
	for _, os := range []string{"linux", "darwin", "windows"} {
		env.Info.OS = os
		r := run(env)
		if r["docker"].Status != Fail || r["docker_running"].Status != Skip || r["compose"].Status != Skip {
			t.Fatalf("%s: %+v", os, r)
		}
		want := map[string]string{"linux": "Docker Engine", "darwin": "Colima", "windows": "WSL2"}[os]
		if !strings.Contains(r["docker"].Fix, want) {
			t.Errorf("%s: correção sem %q: %s", os, want, r["docker"].Fix)
		}
	}
}

func TestDaemonProblems(t *testing.T) {
	env := fake()
	base := env.DockerOutput
	env.DockerOutput = func(ctx context.Context, args ...string) (string, error) {
		if strings.Contains(strings.Join(args, " "), "Server") {
			return "", errors.New("permission denied while trying to connect to the Docker daemon socket")
		}
		return base(ctx, args...)
	}
	r := run(env)
	if r["docker_running"].Status != Fail || !strings.Contains(r["docker_running"].Fix, "usermod") {
		t.Fatalf("permissão: %+v", r["docker_running"])
	}
	env.DockerOutput = func(ctx context.Context, args ...string) (string, error) {
		if strings.Contains(strings.Join(args, " "), "Server") {
			return "", errors.New("Cannot connect to the Docker daemon. Is the docker daemon running?")
		}
		return base(ctx, args...)
	}
	r = run(env)
	if r["docker_running"].Status != Fail || !strings.Contains(r["docker_running"].Fix, "systemctl") || !strings.Contains(r["docker_running"].Fix, "nunca usa sudo") {
		t.Fatalf("parado no Linux: %+v", r["docker_running"])
	}
	env.Info.OS = "darwin"
	if r = run(env); !strings.Contains(r["docker_running"].Fix, "Docker Desktop") {
		t.Fatalf("parado no Mac: %+v", r["docker_running"])
	}
}

func TestComposeOld(t *testing.T) {
	env := fake()
	base := env.DockerOutput
	env.DockerOutput = func(ctx context.Context, args ...string) (string, error) {
		if args[0] == "compose" {
			return "v2.17.0", nil
		}
		return base(ctx, args...)
	}
	r := run(env)
	if r["compose"].Status != Fail || !strings.Contains(r["compose"].Fix, "2.20") {
		t.Fatalf("%+v", r["compose"])
	}
}

func TestDisk(t *testing.T) {
	env := fake()
	env.FreeBytes = func(p string) (uint64, error) {
		if p == "/var/lib/docker" {
			return 2_500_000_000, nil
		}
		return 40_000_000_000, nil
	}
	r := run(env)
	if r["disk"].Status != Fail || !strings.Contains(r["disk"].Detail, "/var/lib/docker") || !strings.Contains(r["disk"].Detail, "2,5 GB") {
		t.Fatalf("pouco disco para compilar: %+v", r["disk"])
	}
	env.SourceDir = "" // pulling needs less
	if r = run(env); r["disk"].Status != OK {
		t.Fatalf("baixar cabe: %+v", r["disk"])
	}
	env.SourceDir = "/src"
	env.FreeBytes = func(string) (uint64, error) { return 4_000_000_000, nil }
	if r = run(env); r["disk"].Status != Warn {
		t.Fatalf("aviso: %+v", r["disk"])
	}
}

func TestPorts(t *testing.T) {
	env := fake()
	env.PortFree = func(p int) bool { return p != 4321 && p != 8080 }
	env.Ours = map[int]bool{8080: true}
	r := run(env)
	if r["port_web"].Status != Warn || !strings.Contains(r["port_web"].Fix, "4322") || !strings.Contains(r["port_web"].Fix, "--port-web") {
		t.Fatalf("web: %+v", r["port_web"])
	}
	if r["port_api"].Status != OK || !strings.Contains(r["port_api"].Detail, "esta instalação") {
		t.Fatalf("api é nossa: %+v", r["port_api"])
	}
	env.Mode = state.ModeDev
	if r = run(env); !strings.Contains(r["port_web"].Fix, ".env") || strings.Contains(r["port_web"].Fix, "--port-web") {
		t.Fatalf("no Desenvolver as portas vêm do .env: %+v", r["port_web"])
	}
	env.Mode = state.ModeTry
	names := map[string]string{}
	for _, c := range Checks(env) {
		names[c.ID] = c.Name
	}
	if names["port_postgres"] != "Porta 5432 (Postgres)" {
		t.Fatal(names["port_postgres"])
	}
}

func TestSourceAndGit(t *testing.T) {
	env := fake()
	env.SourceDir = ""
	env.LookPath = func(name string) (string, error) {
		if name == "git" {
			return "", errors.New("no")
		}
		return "/usr/bin/" + name, nil
	}
	r := run(env)
	if r["source"].Status != Warn || r["git"].Status != Warn || Failed(RunAll(context.Background(), Checks(env))) {
		t.Fatalf("sem código nem git: %+v %+v", r["source"], r["git"])
	}
	env.SourceDir = "/src"
	if r = run(env); r["git"].Status != Skip {
		t.Fatalf("git opcional com o código: %+v", r["git"])
	}
}

func TestDevMode(t *testing.T) {
	env := fake()
	env.Mode = state.ModeDev
	r := run(env)
	for _, tool := range []string{"git", "make", "cc", "go", "rust", "node", "pnpm", "openssl", "curl"} {
		if r["tool_"+tool].Status != OK {
			t.Errorf("%s: %+v", tool, r["tool_"+tool])
		}
	}
	if _, ok := r["tool_docker"]; ok {
		t.Fatal("o Docker já tem as três linhas dele")
	}
	old := env.Output
	env.Output = func(ctx context.Context, name string, args ...string) (string, error) {
		if name == "node" {
			return "v20.11.0", nil
		}
		return old(ctx, name, args...)
	}
	env.LookPath = func(name string) (string, error) {
		if name == "cargo" || name == "rustup" {
			return "", errors.New("no")
		}
		return "/usr/bin/" + name, nil
	}
	r = run(env)
	if r["tool_node"].Status != Fail || !strings.Contains(r["tool_node"].Detail, "22.23.4") || r["tool_rust"].Status != Fail || !strings.Contains(r["tool_rust"].Fix, "rustup") {
		t.Fatalf("%+v %+v", r["tool_node"], r["tool_rust"])
	}
	env.SourceDir = ""
	if r = run(env); r["source"].Status != Fail {
		t.Fatal("sem checkout nem pasta para clonar")
	}
	env.CloneDir = "/home/u/docmatch"
	if r = run(env); r["source"].Status != Warn || !strings.Contains(r["source"].Detail, "/home/u/docmatch") {
		t.Fatalf("sem checkout, o instalador clona: %+v", r["source"])
	}
	// The harness socket lives in the checkout: a path too deep for a Unix socket fails up front.
	env.CloneDir = "/home/u/" + strings.Repeat("pasta-funda/", 8) + "docmatch"
	if r = run(env); r["source"].Status != Fail || !strings.Contains(r["source"].Fix, "limite é 103") {
		t.Fatalf("%+v", r["source"])
	}
	env.CloneDir = "/home/u/docmatch"
	env = fake()
	env.Mode, env.Info.OS = state.ModeDev, "windows"
	r = run(env)
	if r["os"].Status != Warn || !strings.Contains(r["os"].Fix, "WSL2") {
		t.Fatalf("Windows: %+v", r["os"])
	}
	if _, ok := r["tool_wsl"]; !ok {
		t.Fatalf("Windows confere o WSL: %v", r)
	}
	if _, ok := r["tool_go"]; ok {
		t.Fatal("as toolchains rodam dentro do WSL")
	}
	env.Info = platform.Info{OS: "freebsd", Arch: "amd64"}
	if r = run(env); r["os"].Status != Fail {
		t.Fatal("sistema sem versão")
	}
}
