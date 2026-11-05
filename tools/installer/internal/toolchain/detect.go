package toolchain

import (
	"context"
	"regexp"
	"strings"

	"github.com/mmtmn/docmatch/tools/installer/internal/docker"
	"github.com/mmtmn/docmatch/tools/installer/internal/text"
)

// Tool ids (also the keys of their names under installer.tools.names).
const (
	Git     = "git"
	Make    = "make"
	CC      = "cc"
	Go      = "go"
	Rust    = "rust"
	Node    = "node"
	PNPM    = "pnpm"
	Docker  = "docker"
	OpenSSL = "openssl"
	Curl    = "curl"
	WSL     = "wsl"
)

// MinCompose is the Compose the stack needs (--wait, service_completed_successfully).
const MinCompose = "2.20"

// State of a tool.
type State int

// States.
const (
	OK State = iota
	Missing
	Outdated
)

// Status is what the detection found for one tool.
type Status struct {
	ID      string
	Name    string
	State   State
	Version string
	Detail  string // the check line: the version, or what is wrong
	Want    string // the version the checkout needs ("" = any)
	// Reason refines a problem the package manager cannot solve: "gotoolchain_local" (Go is
	// recent enough to switch but GOTOOLCHAIN=local forbids it), "no_compose" (Docker without
	// Compose v2), "rustup_no_toolchain" (rustup without a default toolchain).
	Reason string
}

// OK reports whether the tool is ready.
func (s Status) OK() bool { return s.State == OK }

// IDs are the tools this platform needs, in the order they are shown and installed. Native
// Windows only needs what WSL2 runs on (WSL itself and Docker Desktop): `make local` runs inside
// WSL, where the installer behaves as on Linux.
func (e Env) IDs() []string {
	if !e.Unix() {
		return []string{WSL, Docker}
	}
	return []string{Git, Make, CC, Go, Rust, Node, PNPM, Docker, OpenSSL, Curl}
}

// Name is the display name of a tool.
func Name(id string) string { return text.T("tools.names." + id) }

var versionRe = regexp.MustCompile(`(\d+\.\d+(?:\.\d+)?)`)

func versionOf(out string) string { return versionRe.FindString(firstLine(out)) }

// DetectAll checks every tool of this platform against the checkout's requirements.
func (e Env) DetectAll(ctx context.Context) ([]Status, Requirements) {
	req := ReadRequirements(e.SourceDir)
	ids := e.IDs()
	out := make([]Status, 0, len(ids))
	for _, id := range ids {
		out = append(out, e.Detect(ctx, id, req))
	}
	return out, req
}

// Detect checks one tool.
func (e Env) Detect(ctx context.Context, id string, req Requirements) Status {
	s := Status{ID: id, Name: Name(id)}
	r := e.runner()
	missing := func() Status {
		s.State, s.Detail = Missing, text.T("tools.missing")
		return s
	}
	found := func(v string) Status {
		s.State, s.Version, s.Detail = OK, v, v
		if v == "" {
			s.Detail = text.T("tools.found")
		}
		return s
	}
	outdated := func(v, want string) Status {
		s.State, s.Version, s.Want = Outdated, v, want
		s.Detail = text.T("tools.outdated", "version", v, "want", want)
		return s
	}
	simple := func(bin string, args ...string) Status {
		if _, err := r.LookPath(bin); err != nil {
			return missing()
		}
		out, _ := r.Output(ctx, "", bin, args...)
		return found(versionOf(out))
	}

	switch id {
	case Git:
		return simple("git", "--version")
	case OpenSSL:
		return simple("openssl", "version")
	case Curl:
		return simple("curl", "--version")
	case Make, CC:
		// On macOS both are shims of the Command Line Tools: present on the PATH, useless (they
		// open an install dialog) until `xcode-select --install` ran.
		if e.Info.OS == "darwin" {
			if _, err := r.Output(ctx, "", "xcode-select", "-p"); err != nil {
				return missing()
			}
		}
		if id == Make {
			return simple("make", "--version")
		}
		for _, bin := range []string{"cc", "gcc", "clang"} {
			if _, err := r.LookPath(bin); err == nil {
				out, _ := r.Output(ctx, "", bin, "--version")
				return found(versionOf(out))
			}
		}
		return missing()
	case Go:
		return e.detectGo(ctx, s, req)
	case Rust:
		return e.detectRust(ctx, s, req)
	case Node:
		if _, err := r.LookPath("node"); err != nil {
			return missing()
		}
		out, _ := r.Output(ctx, "", "node", "--version")
		v := versionOf(out)
		if v != "" && req.Node != "" && !docker.AtLeast(v, req.Node) {
			return outdated(v, req.Node)
		}
		return found(v)
	case PNPM:
		if _, err := r.LookPath("pnpm"); err != nil {
			return missing()
		}
		out, _ := r.Output(ctx, "", "pnpm", "--version")
		v := versionOf(out)
		if v != "" && req.PNPMMin != "" && !docker.AtLeast(v, req.PNPMMin) {
			return outdated(v, req.PNPMMin)
		}
		return found(v)
	case Docker:
		bin := "docker"
		if _, err := r.LookPath(bin); err != nil {
			return missing()
		}
		out, err := r.Output(ctx, "", bin, "compose", "version", "--short")
		v := strings.TrimPrefix(firstLine(out), "v")
		if err != nil || versionOf(v) == "" {
			s.State, s.Reason, s.Detail = Outdated, "no_compose", text.T("tools.no_compose")
			return s
		}
		if !docker.AtLeast(v, MinCompose) {
			s = outdated("Compose "+versionOf(v), MinCompose)
			s.Reason = "no_compose"
			return s
		}
		return found("Compose " + versionOf(v))
	case WSL:
		if _, err := r.LookPath("wsl"); err != nil {
			return missing()
		}
		// Without the feature, wsl.exe is a stub that explains how to install it and fails.
		if _, err := r.Output(ctx, "", "wsl", "--status"); err != nil {
			return missing()
		}
		return found("")
	}
	return missing()
}

// detectGo accepts any Go that can switch to the go.mod toolchain by itself: Go 1.21+ with
// GOTOOLCHAIN=auto (the default, and what the installer passes to make when the environment
// does not say otherwise; some distributions write GOTOOLCHAIN=local into their go.env).
func (e Env) detectGo(ctx context.Context, s Status, req Requirements) Status {
	r := e.runner()
	if _, err := r.LookPath("go"); err != nil {
		s.State, s.Detail = Missing, text.T("tools.missing")
		return s
	}
	out, _ := r.Output(ctx, "", "go", "version")
	v := versionOf(strings.Replace(out, "go version go", "", 1))
	s.Version, s.Want = v, req.GoMod
	want := req.GoToolchain
	if want == "" {
		want = "go" + req.GoMod
	}
	switch {
	case v == "":
		s.State, s.Detail = OK, text.T("tools.found")
	case req.GoMod == "" || docker.AtLeast(v, req.GoMod):
		s.State, s.Detail = OK, v
	case docker.AtLeast(v, MinGoAuto) && !forcesLocal(e.Getenv.Get("GOTOOLCHAIN")):
		s.State, s.Detail = OK, text.T("tools.go_auto", "version", v, "want", want)
	case docker.AtLeast(v, MinGoAuto):
		s.State, s.Reason = Outdated, "gotoolchain_local"
		s.Detail = text.T("tools.go_local", "version", v, "want", want)
	default:
		s.State, s.Want = Outdated, MinGoAuto
		s.Detail = text.T("tools.outdated", "version", v, "want", MinGoAuto)
	}
	return s
}

// GoToolchainFor is the GOTOOLCHAIN for `make setup` and `make local` when the user set none:
// the go.mod toolchain ("go1.27.3+auto") when the local Go is older, "auto" otherwise. With plain
// auto and an old local Go, `go install tool@version` builds each tool with that tool's own
// minimum Go, and a golangci-lint built that way refuses the repository's code.
func GoToolchainFor(local, pinned string) string {
	l := versionOf(strings.TrimPrefix(local, "go"))
	p := versionOf(strings.TrimPrefix(pinned, "go"))
	if l == "" || p == "" || docker.AtLeast(l, p) {
		return "auto"
	}
	return "go" + p + "+auto"
}

// forcesLocal reports a GOTOOLCHAIN that forbids downloading the go.mod toolchain.
func forcesLocal(v string) bool {
	v = strings.TrimSpace(v)
	return v == "local" || (strings.HasPrefix(v, "go") && !strings.HasSuffix(v, "+auto") && !strings.HasSuffix(v, "+path"))
}

// detectRust wants cargo to run. With rustup the rust-toolchain.toml of the harness picks (and
// downloads) the pinned Rust; without it, the cargo on the PATH must be at least that version.
func (e Env) detectRust(ctx context.Context, s Status, req Requirements) Status {
	r := e.runner()
	_, rustupErr := r.LookPath("rustup")
	cargoV := ""
	if _, err := r.LookPath("cargo"); err == nil {
		// Outside the checkout: inside, rustup would download the pinned toolchain right now.
		if out, err := r.Output(ctx, "", "cargo", "--version"); err == nil {
			cargoV = versionOf(out)
		}
	}
	pinned := versionRe.MatchString(req.Rust)
	switch {
	case rustupErr == nil && cargoV != "":
		s.State, s.Version, s.Detail = OK, cargoV, text.T("tools.rustup", "version", cargoV)
	case rustupErr == nil:
		s.State, s.Reason, s.Detail = Missing, "rustup_no_toolchain", text.T("tools.rustup_no_toolchain")
	case cargoV == "":
		s.State, s.Detail = Missing, text.T("tools.missing")
	case pinned && !docker.AtLeast(cargoV, req.Rust):
		s.State, s.Version, s.Want = Outdated, cargoV, req.Rust
		s.Detail = text.T("tools.outdated", "version", cargoV, "want", req.Rust)
	default:
		s.State, s.Version, s.Detail = OK, cargoV, cargoV
	}
	return s
}
