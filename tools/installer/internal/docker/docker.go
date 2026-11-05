// Package docker drives the Docker CLI: version and daemon checks for the preflight, and
// `docker compose` for the stack, reading compose's JSON progress so pulls and builds show real
// progress bars. Nothing here asks for elevated rights: when Docker needs them, the error says so
// and the preflight explains how to fix it.
package docker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// CLI runs the docker binary.
type CLI struct {
	Bin string   // "docker" by default
	Env []string // extra environment (KEY=value)
}

func (c CLI) bin() string {
	if c.Bin == "" {
		return "docker"
	}
	return c.Bin
}

// Name is the docker program the CLI runs.
func (c CLI) Name() string { return c.bin() }

func (c CLI) command(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, c.bin(), args...) //nolint:gosec // docker with our own arguments
	// BuildKit is the default since Docker 23; the build progress parser needs its solve status.
	cmd.Env = append(os.Environ(), "DOCKER_BUILDKIT=1")
	cmd.Env = append(cmd.Env, c.Env...)
	return cmd
}

// Installed reports whether the docker binary is on the PATH.
func (c CLI) Installed() bool {
	_, err := exec.LookPath(c.bin())
	return err == nil
}

// Output runs docker with args and returns trimmed stdout; on failure the error carries stderr.
func (c CLI) Output(ctx context.Context, args ...string) (string, error) {
	cmd := c.command(ctx, args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return strings.TrimSpace(out.String()), &Error{Msg: msg, Err: err}
	}
	return strings.TrimSpace(out.String()), nil
}

// Error is a failed docker command.
type Error struct {
	Msg string
	Err error
}

func (e *Error) Error() string { return e.Msg }
func (e *Error) Unwrap() error { return e.Err }

// DaemonProblem classifies why `docker info` failed.
type DaemonProblem int

// Daemon problems.
const (
	DaemonOK DaemonProblem = iota
	DaemonDown
	DaemonPermission
)

// ClassifyDaemonError tells a stopped daemon from a permission problem.
func ClassifyDaemonError(msg string) DaemonProblem {
	m := strings.ToLower(msg)
	if strings.Contains(m, "permission denied") {
		return DaemonPermission
	}
	return DaemonDown
}

// ServerVersion is the daemon's version; it fails when the daemon is not reachable.
func (c CLI) ServerVersion(ctx context.Context) (string, error) {
	return c.Output(ctx, "version", "--format", "{{.Server.Version}}")
}

// ClientVersion is the CLI's version.
func (c CLI) ClientVersion(ctx context.Context) (string, error) {
	return c.Output(ctx, "version", "--format", "{{.Client.Version}}")
}

// ComposeVersion is `docker compose version --short` ("2.40.3").
func (c CLI) ComposeVersion(ctx context.Context) (string, error) {
	v, err := c.Output(ctx, "compose", "version", "--short")
	return strings.TrimPrefix(v, "v"), err
}

// RootDir is where the daemon keeps images (empty when unknown).
func (c CLI) RootDir(ctx context.Context) string {
	v, _ := c.Output(ctx, "info", "--format", "{{.DockerRootDir}}")
	return v
}

// ImageLabel returns a label of a local image; ok is false when the image is not here.
func (c CLI) ImageLabel(ctx context.Context, image, label string) (string, bool) {
	v, err := c.Output(ctx, "image", "inspect", "--format", fmt.Sprintf("{{index .Config.Labels %q}}", label), image)
	if err != nil {
		return "", false
	}
	if v == "<no value>" {
		v = ""
	}
	return v, true
}

// ImageSize is the size of a local image as `docker images` shows it ("110MB"; "" when unknown).
// With the containerd image store `image inspect` reports the compressed size instead.
func (c CLI) ImageSize(ctx context.Context, image string) string {
	v, err := c.Output(ctx, "image", "ls", "--format", "{{.Size}}", image)
	if err != nil {
		return ""
	}
	first, _, _ := strings.Cut(v, "\n")
	return strings.TrimSpace(first)
}

var publishedPort = regexp.MustCompile(`:(\d+)->`)

// ProjectPorts are the host ports held by the running containers of a compose project (read
// from the container labels, so it works before the installation folder exists).
func (c CLI) ProjectPorts(ctx context.Context, project string) map[int]bool {
	out := map[int]bool{}
	v, err := c.Output(ctx, "ps", "--filter", "label=com.docker.compose.project="+project, "--format", "{{.Ports}}")
	if err != nil {
		return out
	}
	for _, m := range publishedPort.FindAllStringSubmatch(v, -1) {
		if n, err := strconv.Atoi(m[1]); err == nil {
			out[n] = true
		}
	}
	return out
}

// AtLeast compares dotted versions ("2.40.3" >= "2.20").
func AtLeast(version, minimum string) bool {
	parse := func(s string) []int {
		s = strings.TrimPrefix(strings.TrimSpace(s), "v")
		if i := strings.IndexAny(s, "-+ "); i >= 0 {
			s = s[:i]
		}
		var out []int
		for _, p := range strings.Split(s, ".") {
			n, err := strconv.Atoi(p)
			if err != nil {
				break
			}
			out = append(out, n)
		}
		return out
	}
	a, b := parse(version), parse(minimum)
	if len(a) == 0 {
		return false
	}
	for i := 0; i < len(b); i++ {
		x := 0
		if i < len(a) {
			x = a[i]
		}
		if x != b[i] {
			return x > b[i]
		}
	}
	return true
}

// Compose runs `docker compose` for one project.
type Compose struct {
	CLI
	Project string
	File    string
	EnvFile string
}

func (c Compose) args(json bool, sub ...string) []string {
	args := []string{"compose", "-p", c.Project, "-f", c.File}
	if c.EnvFile != "" {
		args = append(args, "--env-file", c.EnvFile)
	}
	if json {
		args = append(args, "--progress", "json")
	}
	return append(args, sub...)
}

// Line is one line of output: Stream 1 is stdout, 2 is stderr.
type Line struct {
	Stream int
	Text   string
}

// stream runs the command and hands each output line to onLine (from one goroutine at a time).
func (c Compose) stream(ctx context.Context, args []string, onLine func(Line)) error {
	cmd := c.command(ctx, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	var mu sync.Mutex
	var tail []string
	var wg sync.WaitGroup
	read := func(r io.Reader, n int) {
		defer wg.Done()
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
		for sc.Scan() {
			mu.Lock()
			if n == 2 {
				tail = append(tail, sc.Text())
				if len(tail) > 20 {
					tail = tail[1:]
				}
			}
			if onLine != nil {
				onLine(Line{Stream: n, Text: sc.Text()})
			}
			mu.Unlock()
		}
	}
	wg.Add(2)
	go read(stdout, 1)
	go read(stderr, 2)
	wg.Wait()
	if err := cmd.Wait(); err != nil {
		return &Error{Msg: lastMeaningful(tail, err), Err: err}
	}
	return nil
}

// lastMeaningful picks the most useful error text from the end of stderr (JSON or plain).
func lastMeaningful(tail []string, err error) string {
	for i := len(tail) - 1; i >= 0; i-- {
		line := strings.TrimSpace(tail[i])
		if line == "" {
			continue
		}
		var ev struct {
			Error   bool   `json:"error"`
			Message string `json:"message"`
			Level   string `json:"level"`
			Msg     string `json:"msg"`
		}
		if json.Unmarshal([]byte(line), &ev) == nil {
			if ev.Error && ev.Message != "" {
				return ev.Message
			}
			if ev.Level == "error" && ev.Msg != "" {
				return ev.Msg
			}
			continue
		}
		return line
	}
	return err.Error()
}

// Pull pulls services' images with byte-level progress per service.
func (c Compose) Pull(ctx context.Context, services []string, missingOnly bool, onProgress func(PullProgress), onLog func(string)) error {
	sub := []string{"pull"}
	if missingOnly {
		sub = append(sub, "--policy", "missing")
	}
	tracker := NewPullTracker()
	return c.stream(ctx, c.args(true, append(sub, services...)...), func(l Line) {
		if changed, ok := tracker.Feed(l.Text); ok {
			if changed && onProgress != nil {
				onProgress(tracker.Snapshot())
			}
			return
		}
		if onLog != nil {
			onLog(l.Text)
		}
	})
}

// Build builds services with per-service step progress; build output goes to onLog.
func (c Compose) Build(ctx context.Context, services []string, onProgress func(BuildProgress), onLog func(string)) error {
	tracker := NewBuildTracker()
	return c.stream(ctx, c.args(true, append([]string{"build"}, services...)...), func(l Line) {
		logs, changed, ok := tracker.Feed(l.Text)
		if !ok {
			if onLog != nil {
				onLog(l.Text)
			}
			return
		}
		if onLog != nil {
			for _, s := range logs {
				onLog(s)
			}
		}
		if changed && onProgress != nil {
			onProgress(tracker.Snapshot())
		}
	})
}

// Up starts services (all when empty) detached and waits until they are healthy.
func (c Compose) Up(ctx context.Context, services []string, onLog func(string)) error {
	sub := append([]string{"up", "-d", "--wait", "--no-build", "--pull", "never"}, services...)
	return c.stream(ctx, c.args(false, sub...), func(l Line) {
		if onLog != nil {
			onLog(l.Text)
		}
	})
}

// RunOnce runs a one-shot service attached (its dependencies must already be up) and fails
// with its exit status.
func (c Compose) RunOnce(ctx context.Context, service string, onLog func(string)) error {
	sub := []string{"up", "--no-build", "--pull", "never", "--no-deps", "--exit-code-from", service, service}
	return c.stream(ctx, c.args(false, sub...), func(l Line) {
		if onLog != nil {
			onLog(l.Text)
		}
	})
}

// Down stops and removes the containers (and the volumes, when asked).
func (c Compose) Down(ctx context.Context, volumes bool, onLog func(string)) error {
	sub := []string{"down"}
	if volumes {
		sub = append(sub, "--volumes")
	}
	return c.stream(ctx, c.args(false, sub...), func(l Line) {
		if onLog != nil {
			onLog(l.Text)
		}
	})
}

// Container is one row of `docker compose ps`.
type Container struct {
	Service    string      `json:"Service"`
	State      string      `json:"State"`
	Health     string      `json:"Health"`
	Status     string      `json:"Status"`
	ExitCode   int         `json:"ExitCode"`
	Publishers []Publisher `json:"Publishers"`
}

// Publisher is a published port.
type Publisher struct {
	URL           string `json:"URL"`
	TargetPort    int    `json:"TargetPort"`
	PublishedPort int    `json:"PublishedPort"`
}

// PS lists the project's containers (stopped ones too).
func (c Compose) PS(ctx context.Context) ([]Container, error) {
	out, err := c.Output(ctx, c.args(false, "ps", "-a", "--format", "json")...)
	if err != nil {
		return nil, err
	}
	return ParsePS(out)
}

// ParsePS reads `compose ps --format json`: one object per line (Compose >= 2.21) or an array.
func ParsePS(out string) ([]Container, error) {
	out = strings.TrimSpace(out)
	if out == "" {
		return nil, nil
	}
	if strings.HasPrefix(out, "[") {
		var list []Container
		err := json.Unmarshal([]byte(out), &list)
		return list, err
	}
	var list []Container
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var c Container
		if err := json.Unmarshal([]byte(line), &c); err != nil {
			return nil, err
		}
		list = append(list, c)
	}
	return list, nil
}

// PublishedPorts are the host ports the running containers hold.
func PublishedPorts(list []Container) map[int]bool {
	out := map[int]bool{}
	for _, c := range list {
		if c.State != "running" {
			continue
		}
		for _, p := range c.Publishers {
			if p.PublishedPort > 0 {
				out[p.PublishedPort] = true
			}
		}
	}
	return out
}

// Logs copies the services' logs to w (all services when service is empty).
func (c Compose) Logs(ctx context.Context, w io.Writer, service string, follow bool, tail int) error {
	sub := []string{"logs", "--no-color", "--tail", strconv.Itoa(tail)}
	if follow {
		sub = append(sub, "--follow")
	}
	if service != "" {
		sub = append(sub, service)
	}
	cmd := c.command(ctx, c.args(false, sub...)...)
	cmd.Stdout, cmd.Stderr = w, w
	return cmd.Run()
}

// IsNotFound reports a pull that failed because the registry does not have (or does not share)
// the image: the cue to build locally.
func IsNotFound(err error) bool {
	if err == nil {
		return false
	}
	var de *Error
	msg := err.Error()
	if errors.As(err, &de) {
		msg = de.Msg
	}
	m := strings.ToLower(msg)
	for _, s := range []string{"denied", "not found", "manifest unknown", "unauthorized", "does not exist", "repository name must"} {
		if strings.Contains(m, s) {
			return true
		}
	}
	return false
}
