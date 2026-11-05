package cli

import (
	"bufio"
	"context"
	"io"
	"os"
	"os/user"
	"slices"
	"strings"
	"sync"

	"github.com/mmtmn/docmatch/tools/installer/internal/install"
	"github.com/mmtmn/docmatch/tools/installer/internal/state"
	"github.com/mmtmn/docmatch/tools/installer/internal/text"
	"github.com/mmtmn/docmatch/tools/installer/internal/toolchain"
	"github.com/mmtmn/docmatch/tools/installer/internal/ui"
)

// devTools is the Desenvolver toolchain of this run: detection, plans and the folders added to
// this process's PATH (what the installers just put there is found without a new shell; make
// inherits the same PATH).
type devTools struct {
	w     world
	b     Build
	mu    sync.Mutex
	added []string
	// front are the folders the plans asked to put first (toolchain.Plan.PathFirst).
	front []string
	// exec replaces toolchain.SystemExec in tests.
	exec toolchain.Exec
}

func (w world) toolchainEnv(source string) toolchain.Env {
	name := w.env.Get("USER")
	if u, err := user.Current(); err == nil && u.Username != "" {
		name = u.Username
	}
	return toolchain.Env{
		Info: w.info, Getenv: w.env, Home: w.home, User: name, Root: os.Geteuid() == 0, SourceDir: source,
		Runner: w.runner, Exists: w.exists, Glob: w.glob,
	}
}

// refreshPath adds the toolchain folders that exist now to PATH.
func (d *devTools) refreshPath(env toolchain.Env, req toolchain.Requirements) {
	d.mu.Lock()
	defer d.mu.Unlock()
	old := os.Getenv("PATH")
	path, added := env.AugmentPath(old, req, d.front...)
	if path != old {
		_ = os.Setenv("PATH", path)
	}
	for _, a := range added {
		if !slices.Contains(d.added, a) {
			d.added = append(d.added, a)
		}
	}
}

// remember keeps a plan's PathFirst for the next refreshes.
func (d *devTools) remember(plan toolchain.Plan) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, dir := range plan.PathFirst {
		if !slices.Contains(d.front, dir) {
			d.front = append(d.front, dir)
		}
	}
}

func (d *devTools) pathAdded() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string{}, d.added...)
}

// report detects the tools and plans what is missing.
func (d *devTools) report(ctx context.Context, opts install.Options) ui.ToolsReport {
	env := d.w.toolchainEnv(opts.Resolved().SourceDir)
	d.refreshPath(env, toolchain.ReadRequirements(env.SourceDir))
	statuses, req := env.DetectAll(ctx)
	plan := env.PlanFor(ctx, statuses, req)
	d.remember(plan)
	r := ui.ToolsReport{Statuses: statuses, Plan: plan, PathAdded: d.pathAdded(), Windows: !env.Unix()}
	if r.Windows {
		r.Handoff = d.handoff()
	}
	return r
}

// handoff is what to run inside WSL: this installer again, from the same base.
func (d *devTools) handoff() string {
	if base := strings.TrimRight(first(d.w.env.Get("DOCMATCH_INSTALL_BASE"), d.b.InstallBase), "/"); base != "" {
		return "curl -fsSL " + base + "/install.sh | sh -s -- --mode " + state.ModeDev
	}
	return text.T("tools.handoff_clone")
}

// runPlan runs the commands attached to t, refreshing PATH before each one.
func (d *devTools) runPlan(ctx context.Context, plan toolchain.Plan, t toolchain.Terminal, source string) []toolchain.Result {
	env := d.w.toolchainEnv(source)
	req := toolchain.ReadRequirements(source)
	return toolchain.RunPlan(ctx, plan.Commands, t, d.exec, func(toolchain.Command) { d.refreshPath(env, req) })
}

// planExec runs a plan while Bubble Tea has handed the terminal over.
type planExec struct {
	d       *devTools
	ctx     context.Context
	plan    toolchain.Plan
	source  string
	in      io.Reader
	out     io.Writer
	errw    io.Writer
	results []toolchain.Result
}

func (p *planExec) SetStdin(r io.Reader)  { p.in = r }
func (p *planExec) SetStdout(w io.Writer) { p.out = w }
func (p *planExec) SetStderr(w io.Writer) { p.errw = w }

// Results are the outcomes of the last Run.
func (p *planExec) Results() []toolchain.Result { return p.results }

// Run runs the plan. After a failure it waits for Enter, so the output stays readable before
// the full screen comes back.
func (p *planExec) Run() error {
	outln(p.out, text.T("tools.terminal_intro"))
	p.results = p.d.runPlan(p.ctx, p.plan, toolchain.Terminal{In: p.in, Out: p.out, Err: p.errw}, p.source)
	if toolchain.Failed(p.results) {
		out(p.out, "\n"+text.T("tools.terminal_back"))
		_, _ = bufio.NewReader(p.in).ReadString('\n')
	}
	return nil
}

// toolsPlain is the tools screen as lines. It returns whether the installation can go on and,
// when it cannot, the exit code.
func (d *devTools) toolsPlain(ctx context.Context, p *ui.Plain, opts install.Options, f installFlags) (bool, int) {
	r := d.report(ctx, opts)
	title := text.T("tools.title")
	if r.Windows {
		title = text.T("tools.title_windows")
	}
	p.Section(title)
	ui.PlainTools(p, r, nil)
	if r.Ready() {
		return !r.Windows, exitOK
	}
	if r.Plan.Empty() {
		return false, exitFailed
	}
	switch {
	case f.installTools:
	case d.w.isTTY && !f.y:
		out(d.w.stdout, "\n  "+text.T("tools.confirm"))
		answer, _ := bufio.NewReader(d.w.stdin).ReadString('\n')
		if !yes(answer) {
			p.Line(text.T("tools.declined"))
			return false, exitFailed
		}
	default:
		p.Line("")
		p.Line(text.T("tools.need_consent"))
		return false, exitFailed
	}
	results := d.runPlan(ctx, r.Plan, toolchain.Terminal{In: d.w.stdin, Out: d.w.stdout, Err: d.w.stderr}, opts.Resolved().SourceDir)
	r = d.report(ctx, opts)
	p.Section(title)
	ui.PlainTools(p, r, results)
	switch {
	case toolchain.Failed(results) || !r.Ready():
		return false, exitFailed
	case r.Windows:
		return false, exitOK
	}
	return true, exitOK
}

func yes(answer string) bool {
	a := strings.ToLower(strings.TrimSpace(answer))
	return a == "s" || a == "sim" || a == "y" || a == "yes"
}
