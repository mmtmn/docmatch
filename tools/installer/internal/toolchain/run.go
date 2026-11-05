package toolchain

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// ExtraPaths are folders package managers and toolchain installers put programs in, which a
// shell opened before the installation may not have on its PATH yet. first go in front of the
// PATH (they must win over older copies: rustup's proxies, Homebrew's versioned Node), rest at
// the end.
func (e Env) ExtraPaths(req Requirements) (first, rest []string) {
	home := e.Home
	switch e.Info.OS {
	case "windows":
		local := e.Getenv.Get("LOCALAPPDATA")
		if local == "" {
			local = e.join(home, "AppData", "Local")
		}
		rest = append(rest, e.join(local, "Microsoft", "WinGet", "Links"))
		if pf := e.Getenv.Get("ProgramFiles"); pf != "" {
			rest = append(rest, e.join(pf, "Docker", "Docker", "resources", "bin"))
		}
		return first, rest
	case "darwin":
		for _, prefix := range []string{"/opt/homebrew", "/usr/local"} {
			first = append(first, prefix+"/opt/rustup/bin", prefix+"/opt/node@"+req.NodeMajor+"/bin")
			rest = append(rest, prefix+"/bin")
		}
	default:
		rest = append(rest, "/snap/bin", "/usr/local/go/bin")
	}
	first = append([]string{e.join(home, ".cargo", "bin")}, first...)
	rest = append(rest, e.join(home, ".local", "bin"))
	return first, rest
}

// AugmentPath adds the existing extra folders to path (a PATH value) and reports which it
// added. front are folders a plan put replacements in (Plan.PathFirst): they move to the front
// even when the PATH already has them further back.
func (e Env) AugmentPath(path string, req Requirements, front ...string) (string, []string) {
	sep := string(os.PathListSeparator)
	if e.Info.OS == "windows" {
		sep = ";"
	} else if e.Info.OS != "" {
		sep = ":"
	}
	first, rest := e.ExtraPaths(req)
	lead := map[string]bool{}
	var head []string
	for _, dir := range append(append([]string{}, front...), first...) {
		if c := e.clean(dir); !lead[c] && e.exists(dir) {
			lead[c] = true
			head = append(head, dir)
		}
	}
	have := map[string]bool{}
	var parts []string
	for _, p := range strings.Split(path, sep) {
		if p == "" {
			continue
		}
		have[e.clean(p)] = true
		if !lead[e.clean(p)] {
			parts = append(parts, p)
		}
	}
	var added []string
	for _, dir := range head {
		if !have[e.clean(dir)] {
			added = append(added, dir)
		}
	}
	for _, dir := range rest {
		if c := e.clean(dir); !have[c] && !lead[c] && e.exists(dir) {
			have[c] = true
			added = append(added, dir)
			parts = append(parts, dir)
		}
	}
	return strings.Join(append(head, parts...), sep), added
}

// Terminal is where a plan runs: the user's terminal, so sudo, UAC and package managers talk
// to the user directly.
type Terminal struct {
	In       io.Reader
	Out, Err io.Writer
}

// Result is how one command of a plan ended.
type Result struct {
	Command  Command
	Err      error
	ExitCode int
	Skipped  bool // not run: an earlier command failed
}

// Exec runs one command attached to the terminal (replaced in tests).
type Exec func(ctx context.Context, args []string, t Terminal) error

// SystemExec runs the command for real.
func SystemExec(ctx context.Context, args []string, t Terminal) error {
	cmd := exec.CommandContext(ctx, args[0], args[1:]...) //nolint:gosec // the plan the user confirmed
	cmd.Stdin, cmd.Stdout, cmd.Stderr = t.In, t.Out, t.Err
	return cmd.Run()
}

// RunPlan runs the commands in order and stops at the first failure (the next ones usually
// depend on it). before runs ahead of each command (the caller refreshes the PATH there, so a
// tool installed by one command is found by the next).
func RunPlan(ctx context.Context, cmds []Command, t Terminal, run Exec, before func(Command)) []Result {
	if run == nil {
		run = SystemExec
	}
	results := make([]Result, len(cmds))
	failed := false
	for i, c := range cmds {
		results[i].Command = c
		if failed || ctx.Err() != nil {
			results[i].Skipped = true
			continue
		}
		if before != nil {
			before(c)
		}
		_, _ = fmt.Fprintf(t.Out, "\n==> %s\n", c)
		err := run(ctx, c.Args, t)
		if err != nil {
			results[i].Err = err
			results[i].ExitCode = -1
			var exit *exec.ExitError
			if errors.As(err, &exit) {
				results[i].ExitCode = exit.ExitCode()
			}
			failed = true
		}
	}
	return results
}

// Failed reports whether any command failed.
func Failed(results []Result) bool {
	for _, r := range results {
		if r.Err != nil {
			return true
		}
	}
	return false
}
