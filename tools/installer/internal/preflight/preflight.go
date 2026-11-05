// Package preflight checks this machine before installing: system, free disk, Docker (installed,
// running, Compose v2), the ports, git and, for Desenvolver, the toolchains (internal/toolchain).
// A failing check carries the fix in pt-BR; nothing is ever installed or elevated here (the
// Desenvolver tools screen shows the commands and asks first).
package preflight

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/mmtmn/docmatch/tools/installer/internal/docker"
	"github.com/mmtmn/docmatch/tools/installer/internal/platform"
	"github.com/mmtmn/docmatch/tools/installer/internal/ports"
	"github.com/mmtmn/docmatch/tools/installer/internal/state"
	"github.com/mmtmn/docmatch/tools/installer/internal/text"
	"github.com/mmtmn/docmatch/tools/installer/internal/toolchain"
)

// Status of a check.
type Status int

// Statuses.
const (
	OK Status = iota
	Warn
	Fail
	Skip
)

// Result of one check.
type Result struct {
	Status Status
	Detail string
	Fix    string
}

// Check is one line of the preflight.
type Check struct {
	ID   string
	Name string
	Run  func(ctx context.Context) Result
}

// Minimum Compose version: --wait and service_completed_successfully.
const MinCompose = "2.20"

// Disk needs, in bytes.
const (
	DiskBuild = 6_000_000_000 // build the images locally (base images + caches)
	DiskPull  = 2_000_000_000 // pull the images
	DiskDev   = 8_000_000_000 // toolchains' build caches
)

// Env is what the checks look at; every outside call can be replaced in tests.
type Env struct {
	Info      platform.Info
	Mode      string
	Dir       string
	Ports     ports.Set
	Ours      map[int]bool // ports held by this installation's own containers
	SourceDir string
	CloneDir  string // Desenvolver without a checkout: where it will be cloned
	Docker    docker.CLI
	Getenv    platform.Lookup // the toolchain checks read GOTOOLCHAIN
	Home      string

	FreeBytes func(path string) (uint64, error)
	PortFree  ports.Prober
	LookPath  func(string) (string, error)
	// Output runs a command and returns its trimmed output (versions of the tools).
	Output func(ctx context.Context, name string, args ...string) (string, error)
	// DockerOutput runs docker (version, compose version).
	DockerOutput func(ctx context.Context, args ...string) (string, error)
	DockerRoot   func(ctx context.Context) string
}

// Defaults fills the outside calls with the real ones.
func (e Env) Defaults() Env {
	if e.FreeBytes == nil {
		e.FreeBytes = platform.FreeBytes
	}
	if e.PortFree == nil {
		e.PortFree = ports.Free
	}
	if e.LookPath == nil {
		e.LookPath = exec.LookPath
	}
	if e.Output == nil {
		e.Output = func(ctx context.Context, name string, args ...string) (string, error) {
			return toolchain.System{}.Output(ctx, "", name, args...)
		}
	}
	if e.Getenv == nil {
		e.Getenv = os.LookupEnv
	}
	if e.DockerOutput == nil {
		e.DockerOutput = e.Docker.Output
	}
	if e.DockerRoot == nil {
		e.DockerRoot = e.Docker.RootDir
	}
	return e
}

// Failed reports whether any result failed.
func Failed(results []Result) bool {
	for _, r := range results {
		if r.Status == Fail {
			return true
		}
	}
	return false
}

// RunAll runs every check in order.
func RunAll(ctx context.Context, checks []Check) []Result {
	out := make([]Result, len(checks))
	for i, c := range checks {
		out[i] = c.Run(ctx)
	}
	return out
}

func ok(detail string) Result   { return Result{Status: OK, Detail: detail} }
func skip(detail string) Result { return Result{Status: Skip, Detail: detail} }
func fail(detail, fix string) Result {
	return Result{Status: Fail, Detail: detail, Fix: fix}
}
func warn(detail, fix string) Result { return Result{Status: Warn, Detail: detail, Fix: fix} }

// Checks lists the checks for env's mode. Docker checks share what they learn: when Docker is
// missing, the daemon and Compose checks are skipped instead of failing three times.
func Checks(env Env) []Check {
	env = env.Defaults()
	d := &dockerFacts{env: env}
	checks := []Check{
		{ID: "os", Name: text.T("preflight.os"), Run: env.checkOS},
		{ID: "disk", Name: text.T("preflight.disk"), Run: env.checkDisk},
		{ID: "docker", Name: text.T("preflight.docker"), Run: d.installed},
		{ID: "docker_running", Name: text.T("preflight.docker_running"), Run: d.running},
		{ID: "compose", Name: text.T("preflight.compose"), Run: d.compose},
	}
	if env.Mode == state.ModeDev {
		tc := env.Toolchain()
		if tc.Unix() {
			checks = append(checks, Check{ID: "source", Name: text.T("preflight.source"), Run: env.checkSourceDev})
		}
		req := toolchain.ReadRequirements(env.SourceDir)
		for _, id := range tc.IDs() {
			if id == toolchain.Docker { // the three Docker lines above say more
				continue
			}
			checks = append(checks, Check{ID: "tool_" + id, Name: toolchain.Name(id), Run: toolCheck(tc, id, req)})
		}
	} else {
		checks = append(checks,
			Check{ID: "source", Name: text.T("preflight.source"), Run: env.checkSourceTry},
			Check{ID: "git", Name: "git", Run: env.checkGit},
		)
	}
	for _, name := range ports.Order {
		checks = append(checks, Check{
			ID:   "port_" + name,
			Name: text.T("preflight.port", "port", env.Ports[name], "name", text.T("ports."+name)),
			Run:  env.portCheck(name),
		})
	}
	return checks
}

func (e Env) checkOS(_ context.Context) Result {
	if !e.Info.Supported() {
		return fail(text.T("preflight.os_unsupported", "os", e.Info.Name()), text.T("preflight.fix_os"))
	}
	if e.Mode == state.ModeDev && e.Info.OS == "windows" {
		return warn(e.Info.Name(), text.T("preflight.fix_wsl"))
	}
	return ok(e.Info.Name())
}

func (e Env) diskNeed() uint64 {
	switch {
	case e.Mode == state.ModeDev:
		return DiskDev
	case e.SourceDir != "":
		return DiskBuild
	default:
		return DiskPull
	}
}

func (e Env) checkDisk(ctx context.Context) Result {
	path := e.Dir
	free, err := e.FreeBytes(path)
	if err != nil {
		return warn(err.Error(), "")
	}
	// On Linux the images live under Docker's root dir, often another disk.
	if e.Info.OS == "linux" && !e.Info.WSL {
		if root := e.DockerRoot(ctx); root != "" {
			if f, err := e.FreeBytes(root); err == nil && f < free {
				free, path = f, root
			}
		}
	}
	need := e.diskNeed()
	detail := text.T("preflight.disk_detail", "free", platform.HumanBytes(free), "path", path)
	switch {
	case free < need/2:
		return fail(detail, text.T("preflight.fix_disk", "need", platform.HumanBytes(need)))
	case free < need:
		return warn(detail, text.T("preflight.fix_disk", "need", platform.HumanBytes(need)))
	}
	return ok(detail)
}

// dockerFacts remembers what the first Docker check found.
type dockerFacts struct {
	env       Env
	missing   bool
	daemonErr bool
}

func (d *dockerFacts) installFix() string {
	switch {
	case d.env.Info.OS == "darwin":
		return text.T("preflight.fix_docker_mac")
	case d.env.Info.OS == "windows":
		return text.T("preflight.fix_docker_windows")
	case d.env.Info.WSL:
		return text.T("preflight.fix_docker_wsl")
	default:
		return text.T("preflight.fix_docker_linux")
	}
}

func (d *dockerFacts) installed(ctx context.Context) Result {
	if _, err := d.env.LookPath(d.env.Docker.Name()); err != nil {
		d.missing = true
		return fail(text.T("preflight.not_found"), d.installFix())
	}
	v, err := d.env.DockerOutput(ctx, "version", "--format", "{{.Client.Version}}")
	if err != nil || v == "" {
		return ok(text.T("preflight.found"))
	}
	return ok(v)
}

func (d *dockerFacts) running(ctx context.Context) Result {
	if d.missing {
		return skip(text.T("preflight.needs_docker"))
	}
	v, err := d.env.DockerOutput(ctx, "version", "--format", "{{.Server.Version}}")
	if err == nil && v != "" {
		return ok(text.T("preflight.daemon_detail", "version", v))
	}
	d.daemonErr = true
	msg := text.T("preflight.daemon_down")
	if err != nil {
		msg = firstLine(err.Error())
	}
	if docker.ClassifyDaemonError(msg) == docker.DaemonPermission {
		return fail(text.T("preflight.daemon_permission"), text.T("preflight.fix_permission"))
	}
	fix := text.T("preflight.fix_daemon_desktop")
	if d.env.Info.OS == "linux" && !d.env.Info.WSL {
		fix = text.T("preflight.fix_daemon_linux")
	}
	return fail(text.T("preflight.daemon_down"), fix)
}

func (d *dockerFacts) compose(ctx context.Context) Result {
	if d.missing {
		return skip(text.T("preflight.needs_docker"))
	}
	v, err := d.env.DockerOutput(ctx, "compose", "version", "--short")
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if err != nil || v == "" {
		return fail(text.T("preflight.not_found"), text.T("preflight.fix_compose"))
	}
	if !docker.AtLeast(v, MinCompose) {
		return fail(v, text.T("preflight.fix_compose_old", "version", v, "min", MinCompose))
	}
	return ok(v)
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	return s
}

func (e Env) checkSourceTry(_ context.Context) Result {
	if e.SourceDir != "" {
		return ok(e.SourceDir)
	}
	return warn(text.T("preflight.source_none"), text.T("preflight.fix_source_try"))
}

// MaxSocketPath is the longest Unix socket path every system takes (sun_path is 104 bytes on
// macOS, 108 on Linux, with the final NUL): make local puts the harness socket in the checkout.
const MaxSocketPath = 103

// HarnessSocket is where `make local` (tools/dev/local.sh) puts the harness socket.
func HarnessSocket(source string) string {
	return filepath.Join(source, ".cache", "local", "harness.sock")
}

func (e Env) checkSourceDev(_ context.Context) Result {
	dir := e.SourceDir
	if dir == "" {
		dir = e.CloneDir
	}
	if dir == "" {
		return fail(text.T("preflight.not_found"), text.T("preflight.fix_source_dev"))
	}
	if sock := HarnessSocket(dir); len(sock) > MaxSocketPath {
		return fail(text.T("preflight.source_too_long", "dir", dir), text.T("preflight.fix_source_too_long", "count", len(sock), "max", MaxSocketPath))
	}
	if e.SourceDir != "" {
		return ok(e.SourceDir)
	}
	return warn(text.T("preflight.source_clone", "dir", e.CloneDir), "")
}

func (e Env) checkGit(ctx context.Context) Result {
	if _, err := e.LookPath("git"); err != nil {
		if e.SourceDir == "" {
			return warn(text.T("preflight.not_found"), text.T("preflight.fix_git"))
		}
		return skip(text.T("preflight.git_optional"))
	}
	v, _ := e.Output(ctx, "git", "--version")
	return ok(strings.TrimPrefix(v, "git version "))
}

// Toolchain is the toolchain view of the same machine, through the same outside calls (so the
// fakes of the tests drive both).
func (e Env) Toolchain() toolchain.Env {
	return toolchain.Env{
		Info: e.Info, Getenv: e.Getenv, Home: e.Home, SourceDir: e.SourceDir,
		Runner: runner{lookPath: e.LookPath, output: e.Output},
	}
}

// runner adapts the preflight's outside calls to toolchain.Runner.
type runner struct {
	lookPath func(string) (string, error)
	output   func(ctx context.Context, name string, args ...string) (string, error)
}

func (r runner) LookPath(name string) (string, error) { return r.lookPath(name) }

func (r runner) Output(ctx context.Context, _ string, name string, args ...string) (string, error) {
	return r.output(ctx, name, args...)
}

func toolCheck(tc toolchain.Env, id string, req toolchain.Requirements) func(context.Context) Result {
	return func(ctx context.Context) Result {
		s := tc.Detect(ctx, id, req)
		if s.OK() {
			return ok(s.Detail)
		}
		return fail(s.Detail, tc.ManualFix(id))
	}
}

func (e Env) portCheck(name string) func(context.Context) Result {
	return func(_ context.Context) Result {
		n := e.Ports[name]
		if e.Ours[n] {
			return ok(text.T("preflight.port_ours"))
		}
		if e.PortFree(n) {
			return ok(text.T("preflight.port_free"))
		}
		taken := map[int]bool{}
		for _, p := range e.Ports {
			taken[p] = true
		}
		suggest := ports.Suggest(n, taken, e.PortFree)
		if e.Mode == state.ModeDev { // make local takes its ports from the repository's .env
			return warn(text.T("preflight.port_busy"), text.T("preflight.fix_port_dev"))
		}
		return warn(text.T("preflight.port_busy"), text.T("preflight.fix_port", "suggest", strconv.Itoa(suggest), "flag", "--port-"+name))
	}
}
