// Package toolchain is the Desenvolver mode's view of this machine: which of the tools
// `make setup` and `make local` need are here (git, make, a C compiler, Go per
// services/api/go.mod with GOTOOLCHAIN=auto, Rust through rustup, Node per package.json, pnpm,
// Docker with Compose v2, openssl and curl), and how this platform's package manager installs the
// missing ones: winget on Windows, Homebrew on macOS, apt or dnf on Linux (WSL included).
//
// Nothing here runs an installer by itself. A Plan is a list of exact commands, the ones that
// need sudo or administrator marked as such; the caller shows it and asks before RunPlan runs it
// attached to the terminal (so sudo asks for the password itself).
package toolchain

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/mmtmn/docmatch/tools/installer/internal/platform"
)

// Runner runs the outside commands the detection needs (a fake in tests).
type Runner interface {
	LookPath(name string) (string, error)
	// Output runs name with args inside dir ("" = a folder without go.mod, so `go version`
	// reports the local toolchain) and returns the trimmed combined output.
	Output(ctx context.Context, dir, name string, args ...string) (string, error)
}

// System is the real Runner.
type System struct{}

// LookPath is exec.LookPath.
func (System) LookPath(name string) (string, error) { return exec.LookPath(name) }

// Output runs the command with a short environment tweak: no pagers, no color.
func (System) Output(ctx context.Context, dir, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // fixed tools with our own arguments
	if dir == "" {
		dir = os.TempDir()
	}
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "NO_COLOR=1", "PAGER=cat", "LC_ALL=C")
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// Env is the machine the detection and the plans look at.
type Env struct {
	Info      platform.Info
	Getenv    platform.Lookup
	Home      string
	User      string // login name (usermod -aG docker <user>)
	Root      bool   // running as root: no sudo in front of the commands
	SourceDir string // the checkout ("" before cloning: the requirements fall back to defaults)
	Runner    Runner
	// Exists reports whether a path exists (PATH augmentation); os.Stat by default.
	Exists func(path string) bool
}

func (e Env) runner() Runner {
	if e.Runner == nil {
		return System{}
	}
	return e.Runner
}

func (e Env) exists(path string) bool {
	if e.Exists != nil {
		return e.Exists(path)
	}
	_, err := os.Stat(path)
	return err == nil
}

// Unix reports whether `make local` can run here (Linux, macOS, WSL); native Windows hands
// over to WSL2.
func (e Env) Unix() bool { return e.Info.OS != "windows" }

// Requirements are the versions the checkout asks for. Without a checkout (before the clone)
// the defaults below apply; a test keeps them in step with the repository.
type Requirements struct {
	GoMod       string // go directive of services/api/go.mod ("1.27.0")
	GoToolchain string // toolchain directive ("go1.27.3")
	Node        string // minimum Node (package.json engines.node, else .nvmrc)
	NodeMajor   string // the major to install (.nvmrc)
	PNPM        string // pnpm to install (package.json packageManager)
	PNPMMin     string // minimum pnpm (package.json engines.pnpm)
	Rust        string // rust-toolchain.toml channel
}

// Defaults without a checkout.
var Defaults = Requirements{GoMod: "1.27.0", GoToolchain: "go1.27.3", Node: "22.23.4", NodeMajor: "22", PNPM: "11.5.3", PNPMMin: "11.0.0", Rust: "1.93.0"}

// MinGoAuto is the first Go that downloads the go.mod toolchain by itself (GOTOOLCHAIN=auto).
const MinGoAuto = "1.21"

var (
	goDirective   = regexp.MustCompile(`(?m)^go\s+(\d+\.\d+(?:\.\d+)?)\s*$`)
	toolDirective = regexp.MustCompile(`(?m)^toolchain\s+(go\S+)\s*$`)
	channelLine   = regexp.MustCompile(`(?m)^\s*channel\s*=\s*"([^"]+)"`)
	semverish     = regexp.MustCompile(`(\d+(?:\.\d+){0,2})`)
)

// ReadRequirements reads the checkout's pins, keeping the defaults for what it cannot read.
func ReadRequirements(source string) Requirements {
	r := Defaults
	if source == "" {
		return r
	}
	read := func(rel string) []byte {
		data, err := os.ReadFile(filepath.Join(source, filepath.FromSlash(rel))) //nolint:gosec // the checkout
		if err != nil {
			return nil
		}
		return data
	}
	if data := read("services/api/go.mod"); data != nil {
		if m := goDirective.FindSubmatch(data); m != nil {
			r.GoMod = string(m[1])
		}
		r.GoToolchain = ""
		if m := toolDirective.FindSubmatch(data); m != nil {
			r.GoToolchain = string(m[1])
		}
	}
	if data := read(".nvmrc"); data != nil {
		if m := semverish.FindString(strings.TrimPrefix(strings.TrimSpace(string(data)), "v")); m != "" {
			r.NodeMajor, _, _ = strings.Cut(m, ".")
			r.Node = m
		}
	}
	if data := read("package.json"); data != nil {
		var pkg struct {
			PackageManager string            `json:"packageManager"`
			Engines        map[string]string `json:"engines"`
		}
		if json.Unmarshal(data, &pkg) == nil {
			if v := minimum(pkg.Engines["node"]); v != "" {
				r.Node = v
			}
			if v := minimum(pkg.Engines["pnpm"]); v != "" {
				r.PNPMMin = v
			}
			if name, v, ok := strings.Cut(pkg.PackageManager, "@"); ok && name == "pnpm" {
				r.PNPM, _, _ = strings.Cut(v, "+") // "11.5.3+sha512..." -> "11.5.3"
			}
		}
	}
	if data := read("services/harness/rust-toolchain.toml"); data != nil {
		if m := channelLine.FindSubmatch(data); m != nil {
			r.Rust = string(m[1])
		}
	}
	return r
}

// minimum reads ">=22.23.4", "^22", "22.x" as the lowest version they accept.
func minimum(spec string) string {
	return semverish.FindString(spec)
}

// firstLine is the first line of s.
func firstLine(s string) string {
	sc := bufio.NewScanner(bytes.NewReader([]byte(strings.TrimSpace(s))))
	if sc.Scan() {
		return strings.TrimSpace(sc.Text())
	}
	return ""
}
