package cli

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-isatty"

	"github.com/mmtmn/docmatch/tools/installer/internal/demo"
	"github.com/mmtmn/docmatch/tools/installer/internal/docker"
	"github.com/mmtmn/docmatch/tools/installer/internal/flow"
	"github.com/mmtmn/docmatch/tools/installer/internal/install"
	"github.com/mmtmn/docmatch/tools/installer/internal/platform"
	"github.com/mmtmn/docmatch/tools/installer/internal/ports"
	"github.com/mmtmn/docmatch/tools/installer/internal/preflight"
	"github.com/mmtmn/docmatch/tools/installer/internal/selfupdate"
	"github.com/mmtmn/docmatch/tools/installer/internal/state"
	"github.com/mmtmn/docmatch/tools/installer/internal/text"
	"github.com/mmtmn/docmatch/tools/installer/internal/toolchain"
	"github.com/mmtmn/docmatch/tools/installer/internal/ui"
)

// Exit codes.
const (
	exitOK        = 0
	exitFailed    = 1
	exitUsage     = 2
	exitCancelled = 130
)

// Main runs the command line and returns the exit code.
func Main(args []string, b Build) int {
	if b.Version == "" {
		b.Version = "dev"
	}
	home, _ := os.UserHomeDir()
	cwd, _ := os.Getwd()
	exe, _ := os.Executable()
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	if runtime.GOOS == "windows" {
		selfupdate.Cleanup(selfupdate.OSFS{}, exe) // the binary an earlier update replaced
	}
	w := world{
		env: envLookup(), info: platform.Detect(), home: home, cwd: cwd, exe: exe,
		stdout: os.Stdout, stderr: os.Stderr, stdin: os.Stdin,
		isTTY: isatty.IsTerminal(os.Stdout.Fd()) && isatty.IsTerminal(os.Stdin.Fd()) ||
			isatty.IsCygwinTerminal(os.Stdout.Fd()) && isatty.IsCygwinTerminal(os.Stdin.Fd()),
		now: time.Now, version: b.Version,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return run(ctx, w, args, b)
}

var commands = map[string]bool{
	"config":    true,
	"workspace": true, "install": true, "up": true, "down": true, "status": true, "logs": true, "doctor": true, "demo": true,
	"update": true, "uninstall": true, "version": true, "help": true,
}

func run(ctx context.Context, w world, args []string, b Build) int {
	// The subcommand may follow flags, but a flag value named "help", "up", etc. is data.
	leading := newFlagSet("command")
	var installOptions installFlags
	installOptions.register(leading)
	for _, name := range []string{"api", "output", "to", "tail"} {
		leading.String(name, "", "")
	}
	for _, name := range []string{"force", "check", "volumes", "f", "follow", "h", "help", "version"} {
		leading.Bool(name, false, "")
	}
	cmd := ""
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			break
		}
		if strings.HasPrefix(a, "-") {
			name, _, inline := strings.Cut(strings.TrimLeft(a, "-"), "=")
			f := leading.Lookup(name)
			if f == nil {
				break // the command's parser will report an unknown flag
			}
			boolean, ok := f.Value.(interface{ IsBoolFlag() bool })
			if !inline && (!ok || !boolean.IsBoolFlag()) {
				i++
			}
			continue
		}
		if commands[a] {
			cmd = a
			args = append(append([]string{}, args[:i]...), args[i+1:]...)
			break
		}
		if i == 0 {
			outln(w.stderr, text.T("cli.unknown_command", "cmd", a))
			printHelp(w.stderr)
			return exitUsage
		}
	}
	for _, a := range args {
		switch a {
		case "-h", "--help":
			printHelp(w.stdout)
			return exitOK
		case "--version":
			// The same line as `docmatch version`; update compares it with VERSION.txt.
			outln(w.stdout, "docmatch "+b.Version)
			return exitOK
		}
	}
	var code int
	var err error
	switch cmd {
	case "":
		if len(args) == 0 && w.isTTY {
			code, err = runHome(ctx, w, b)
		} else {
			code, err = runInstall(ctx, w, args, b)
		}
	case "config":
		code, err = runConnection(ctx, w, args)
	case "install":
		code, err = runInstall(ctx, w, args, b)
	case "up":
		code, err = runUp(ctx, w, args, b)
	case "down":
		code, err = runDown(ctx, w, args, b)
	case "status":
		code, err = runStatus(ctx, w, args, b)
	case "logs":
		code, err = runLogs(ctx, w, args, b)
	case "doctor":
		code, err = runDoctor(ctx, w, args, b)
	case "workspace":
		code, err = runWorkspace(ctx, w, args, b)
	case "demo":
		code, err = runDemo(ctx, w, args, b)
	case "update":
		code, err = runUpdate(ctx, w, args, b)
	case "uninstall":
		code, err = runUninstall(ctx, w, args, b)
	case "version":
		outln(w.stdout, "docmatch "+b.Version)
		return exitOK
	case "help":
		printHelp(w.stdout)
		return exitOK
	}
	if err != nil {
		if errors.Is(err, errUsage) {
			outln(w.stderr, "docmatch: "+strings.TrimPrefix(err.Error(), errUsage.Error()+": "))
			return exitUsage
		}
		outln(w.stderr, "docmatch: "+err.Error())
		if code == exitOK {
			code = exitFailed
		}
	}
	return code
}

func printHelp(out io.Writer) {
	for _, l := range text.L("cli.help") {
		outln(out, l)
	}
}

func parse(fs *flag.FlagSet, args []string) error {
	_, err := parseArgs(fs, args)
	return err
}

// parseArgs parses flags anywhere among the arguments ("logs api -f" as well as "logs -f api")
// and returns the positional ones.
func parseArgs(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, fmt.Errorf("%w: %w", errUsage, err)
		}
		if fs.NArg() == 0 {
			return positional, nil
		}
		positional = append(positional, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

// logFile opens the installation's log file (logs/install-<time>.log).
func logFile(dir string, now time.Time) (*os.File, string) {
	logs := filepath.Join(dir, "logs")
	if err := os.MkdirAll(logs, 0o700); err != nil {
		return nil, ""
	}
	path := filepath.Join(logs, "install-"+now.Format("20060102-150405")+".log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) //nolint:gosec // our own log
	if err != nil {
		return nil, ""
	}
	return f, path
}

// eventLogger writes flow events to the log file.
func eventLogger(f io.Writer) func(flow.Event) {
	var mu sync.Mutex
	return func(e flow.Event) {
		if f == nil {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		ts := time.Now().Format("15:04:05")
		switch e.Kind {
		case flow.Started:
			outf(f, "%s ==> %s (%s)\n", ts, e.Title, e.Step)
		case flow.Logged:
			outf(f, "%s [%s] %s\n", ts, e.Step, e.Line)
		case flow.Progressed:
			if e.Note != "" {
				outf(f, "%s [%s] %s\n", ts, e.Step, e.Note)
			}
		case flow.Finished:
			status := map[flow.Outcome]string{flow.OK: "ok", flow.Skipped: "pulado", flow.Warned: "aviso", flow.Failed: "FALHOU"}[e.Outcome]
			msg := e.Detail
			if e.Err != nil {
				msg = e.Err.Error()
			}
			outf(f, "%s <== %s %s: %s (%s)\n", ts, e.Step, status, msg, e.Elapsed.Round(time.Millisecond))
		}
	}
}

func (w world) preflightEnv(opts install.Options, ours map[int]bool) preflight.Env {
	opts = opts.Resolved()
	if opts.Mode == state.ModeDev && opts.SourceDir != "" {
		opts.Ports = install.DevPorts(opts.SourceDir) // what make local will take
	}
	return preflight.Env{
		Info: opts.Info, Mode: opts.Mode, Dir: opts.Dir, Ports: opts.Ports, Ours: ours, SourceDir: opts.SourceDir,
		CloneDir: opts.CloneDir, Docker: docker.CLI{}, Getenv: w.env, Home: w.home,
	}
}

func runInstall(ctx context.Context, w world, args []string, b Build) (int, error) {
	var f installFlags
	fs := newFlagSet("docmatch")
	f.register(fs)
	if err := parse(fs, args); err != nil {
		return exitUsage, err
	}
	r, err := resolveOptions(ctx, w, f, b)
	if err != nil {
		return exitUsage, err
	}
	prefs := w.prefs(f.common)
	cli := docker.CLI{}
	ours := map[int]bool{}
	if cli.Installed() {
		ours = cli.ProjectPorts(ctx, r.opts.Project)
	}
	var logOut *os.File
	var logPath string
	tools := &devTools{w: w, b: b}
	plan := func(opts install.Options) ui.Plan {
		opts = opts.Resolved()
		if logOut == nil {
			logOut, logPath = logFile(opts.Dir, w.now())
		}
		inst := install.New(opts, r.st)
		return ui.Plan{
			Steps: inst.Steps(), Done: inst.MarkDone, Log: eventLogger(logOut), LogPath: logPath,
			Outcome: func() ui.Outcome {
				o := inst.Opts // the clone step fills SourceDir
				return ui.Outcome{Mode: o.Mode, Project: o.Project, Dir: o.Dir, URLs: o.URLs(), Demo: inst.Demo, DemoErr: inst.DemoErr, LogPath: logPath, PathHint: tools.pathAdded()}
			},
		}
	}
	defer func() {
		if logOut != nil {
			_ = logOut.Close()
		}
	}()
	var openBrowser func(string) error
	if !f.noBrowser && platform.CanOpenBrowser(w.info, w.env) {
		openBrowser = func(url string) error { return platform.OpenBrowser(w.info, url) }
	}
	if prefs.plain {
		return installPlain(ctx, w, r, f, ours, plan, tools)
	}

	// Bubble Tea's init already asked the terminal for its background (once, cached).
	dark := lipgloss.HasDarkBackground()
	if v, ok := platform.ThemeOverride(w.env); ok {
		dark = v
		lipgloss.SetHasDarkBackground(dark)
	}
	app := ui.New(ui.Deps{
		Theme: ui.NewTheme(prefs.ascii, prefs.animate, dark), Info: w.info, Version: b.Version,
		Opts: r.opts, Resume: r.st, FixedMode: r.fixedMode, Yes: f.y, ExplicitPorts: r.explicit,
		Checks:      func(opts install.Options) []preflight.Check { return preflight.Checks(w.preflightEnv(opts, ours)) },
		Plan:        plan,
		OpenBrowser: openBrowser, Now: w.now, Ours: ours,
		Tools: tools.report,
		RunTools: func(p toolchain.Plan) ui.ToolsRun {
			return &planExec{d: tools, ctx: ctx, plan: p, source: r.opts.Resolved().SourceDir}
		},
		InstallTools: f.installTools,
	})
	program := tea.NewProgram(app, tea.WithAltScreen(), tea.WithContext(ctx))
	if _, err := program.Run(); err != nil && !errors.Is(err, tea.ErrProgramKilled) && !errors.Is(err, context.Canceled) {
		return exitFailed, err
	}
	app.Cancel()
	if ctx.Err() != nil && app.ExitCode == 0 {
		app.ExitCode = exitCancelled
	}
	if outcome := app.Outcome(); outcome != nil {
		outln(w.stdout)
		for _, l := range ui.Summary(*outcome, app.Total()) {
			outln(w.stdout, l)
		}
	} else if app.ExitCode == exitFailed && logPath != "" {
		outln(w.stderr, text.T("progress.failed_hint", "path", logPath))
	}
	return app.ExitCode, nil
}

// installPlain is the same installation as lines, with no questions: flags and defaults decide,
// busy ports move to the next free one (unless given on the command line).
func installPlain(ctx context.Context, w world, r resolved, f installFlags, ours map[int]bool, plan func(install.Options) ui.Plan, tools *devTools) (int, error) {
	p := ui.NewPlain(w.stdout)
	p.Verbose = f.verbose
	p.Header(w.version)
	opts := r.opts
	p.Section(text.T("plain.mode", "mode", text.T("mode."+modeKey(opts.Mode)+"_name"), "dir", opts.Dir))

	p.Section(text.T("preflight.title"))
	checks := preflight.Checks(w.preflightEnv(opts, ours))
	results := preflight.RunAll(ctx, checks)
	if opts.Mode == state.ModeTry {
		// Busy ports are not failures in plain mode: the next free ones are taken.
		resolvedPorts, changed := ports.Resolve(opts.Ports, ours, ports.Free)
		for _, name := range changed {
			if r.explicit[name] {
				continue
			}
			for i, c := range checks {
				if c.ID == "port_"+name {
					results[i].Fix = text.T("plain.port_moved", "port", resolvedPorts[name])
				}
			}
			opts.Ports[name] = resolvedPorts[name]
		}
		for name := range r.explicit {
			for i, c := range checks {
				if c.ID == "port_"+name && results[i].Status == preflight.Warn {
					results[i].Status = preflight.Fail
				}
			}
		}
	}
	p.Checks(checks, results)
	if opts.Mode == state.ModeDev && (opts.Info.OS == "windows" || failedInstallable(checks, results)) {
		goOn, code := tools.toolsPlain(ctx, p, opts, f)
		if !goOn {
			return code, nil
		}
		p.Section(text.T("preflight.title"))
		checks = preflight.Checks(w.preflightEnv(opts, ours))
		results = preflight.RunAll(ctx, checks)
		p.Checks(checks, results)
	}
	if preflight.Failed(results) {
		p.Line("")
		p.Line(text.T("plain.preflight_failed"))
		return exitFailed, nil
	}

	pl := plan(opts)
	p.Section(text.T("progress.title"))
	start := w.now()
	err := flow.Run(ctx, pl.Steps, func(e flow.Event) {
		if pl.Log != nil {
			pl.Log(e)
		}
		p.Event(e)
	}, pl.Done)
	if err != nil {
		p.Tail()
		p.Line("")
		p.Line(text.T("progress.failed_hint", "path", pl.LogPath))
		if errors.Is(err, flow.ErrCancelled) || ctx.Err() != nil {
			return exitCancelled, nil //nolint:nilerr // reported above, as lines
		}
		return exitFailed, nil //nolint:nilerr // reported above, as lines
	}
	outcome := pl.Outcome()
	p.Section(text.T("nav.done"))
	for _, l := range ui.Summary(outcome, w.now().Sub(start)) {
		p.Line(l)
	}
	return exitOK, nil
}

// failedInstallable reports a failed check the tools step can install (a tool, Docker).
func failedInstallable(checks []preflight.Check, results []preflight.Result) bool {
	for i, c := range checks {
		if results[i].Status == preflight.Fail && (strings.HasPrefix(c.ID, "tool_") || c.ID == "docker" || c.ID == "compose") {
			return true
		}
	}
	return false
}

func modeKey(mode string) string {
	if mode == state.ModeDev {
		return "dev"
	}
	return "try"
}

// lineReporter prints a subcommand's progress as plain lines.
func lineReporter(out io.Writer) func(string) {
	return func(s string) { outln(out, "  "+s) }
}

func subcommandFlags(name string, args []string, extra func(*flag.FlagSet)) (common, error) {
	var c common
	fs := newFlagSet(name)
	c.register(fs)
	if extra != nil {
		extra(fs)
	}
	return c, parse(fs, args)
}

func runUp(ctx context.Context, w world, args []string, b Build) (int, error) {
	c, err := subcommandFlags("up", args, nil)
	if err != nil {
		return exitUsage, err
	}
	r, err := loadInstalled(ctx, w, c, b)
	if err != nil {
		return exitFailed, err
	}
	inst := install.New(r.opts, r.st)
	outln(w.stdout, text.T("cli.up_starting", "project", r.opts.Project))
	if r.opts.Mode == state.ModeDev {
		return makeTarget(ctx, w, r.opts.SourceDir, "local", w.devEnviron(ctx, r.opts))
	}
	if err := inst.Prepare(); err != nil {
		return exitFailed, err
	}
	services := append(append([]string{}, install.BaseServices...), install.AppServices...)
	if err := inst.Compose().Up(ctx, services, lineReporter(w.stdout)); err != nil {
		return exitFailed, fmt.Errorf("%w\n%s", err, text.T("cli.up_hint"))
	}
	u := r.opts.URLs()
	if err := install.WaitHTTP(ctx, u.API+"/v1/version", 60*time.Second); err != nil {
		return exitFailed, err
	}
	outln(w.stdout, text.T("cli.up_done", "url", u.App))
	return exitOK, nil
}

func runDown(ctx context.Context, w world, args []string, b Build) (int, error) {
	var volumes bool
	c, err := subcommandFlags("down", args, func(fs *flag.FlagSet) { fs.BoolVar(&volumes, "volumes", false, "") })
	if err != nil {
		return exitUsage, err
	}
	r, err := loadInstalled(ctx, w, c, b)
	if err != nil {
		return exitFailed, err
	}
	if r.opts.Mode == state.ModeDev {
		return makeTarget(ctx, w, r.opts.SourceDir, "local-down", w.devEnviron(ctx, r.opts))
	}
	if volumes && !c.y {
		out(w.stdout, text.T("cli.down_confirm"))
		answer, _ := bufio.NewReader(w.stdin).ReadString('\n')
		if !yes(answer) {
			outln(w.stdout, text.T("cli.down_kept"))
			volumes = false
		}
	}
	inst := install.New(r.opts, r.st)
	if err := inst.Compose().Down(ctx, volumes, lineReporter(w.stdout)); err != nil {
		return exitFailed, err
	}
	if volumes {
		outln(w.stdout, text.T("cli.down_wiped"))
	} else {
		outln(w.stdout, text.T("cli.down_done"))
	}
	return exitOK, nil
}

func runStatus(ctx context.Context, w world, args []string, b Build) (int, error) {
	c, err := subcommandFlags("status", args, nil)
	if err != nil {
		return exitUsage, err
	}
	r, err := loadInstalled(ctx, w, c, b)
	if err != nil {
		return exitFailed, err
	}
	o := r.opts
	outln(w.stdout, text.T("cli.status_header", "mode", text.T("mode."+modeKey(o.Mode)+"_name"), "project", o.Project, "dir", o.Dir))
	if o.Mode == state.ModeDev {
		cmd := commandIn(ctx, o.SourceDir, "tools/dev/local.sh", "status")
		cmd.Stdout, cmd.Stderr = w.stdout, w.stderr
		_ = cmd.Run()
	} else {
		inst := install.New(o, r.st)
		list, err := inst.Compose().PS(ctx)
		if err != nil {
			return exitFailed, err
		}
		if len(list) == 0 {
			outln(w.stdout, "  "+text.T("cli.status_none"))
		}
		for _, c := range list {
			state := c.State
			if c.Health != "" {
				state += " (" + c.Health + ")"
			}
			if c.State == "exited" {
				state += fmt.Sprintf(" [%d]", c.ExitCode)
			}
			var pub []string
			for _, p := range c.Publishers {
				if p.PublishedPort > 0 {
					pub = append(pub, fmt.Sprintf("%d", p.PublishedPort))
				}
			}
			outf(w.stdout, "  %-10s %-22s %s\n", c.Service, state, strings.Join(dedupe(pub), ","))
		}
	}
	u := o.URLs()
	v, err := demo.New(u.API, u.Site).Version(ctx)
	if err == nil {
		outln(w.stdout, text.T("cli.status_api", "version", v.Version, "url", u.App))
	} else {
		outln(w.stdout, text.T("cli.status_api_down", "url", u.API))
	}
	return exitOK, nil
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	out := in[:0]
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func runLogs(ctx context.Context, w world, args []string, b Build) (int, error) {
	var follow bool
	var tail int
	var c common
	fs := newFlagSet("logs")
	c.register(fs)
	fs.BoolVar(&follow, "f", false, "")
	fs.BoolVar(&follow, "follow", false, "")
	fs.IntVar(&tail, "tail", 200, "")
	positional, err := parseArgs(fs, args)
	if err != nil {
		return exitUsage, err
	}
	r, err := loadInstalled(ctx, w, c, b)
	if err != nil {
		return exitFailed, err
	}
	if r.opts.Mode == state.ModeDev {
		outln(w.stdout, text.T("cli.logs_dev", "dir", filepath.Join(r.opts.SourceDir, ".cache", "local", "logs")))
		return exitOK, nil
	}
	service := ""
	if len(positional) > 0 {
		service = positional[0]
	}
	inst := install.New(r.opts, r.st)
	if err := inst.Compose().Logs(ctx, w.stdout, service, follow, tail); err != nil && ctx.Err() == nil {
		return exitFailed, err
	}
	return exitOK, nil
}

func runDoctor(ctx context.Context, w world, args []string, b Build) (int, error) {
	var f installFlags
	fs := newFlagSet("doctor")
	f.register(fs)
	if err := parse(fs, args); err != nil {
		return exitUsage, err
	}
	r, err := resolveOptions(ctx, w, f, b)
	if err != nil {
		return exitUsage, err
	}
	p := ui.NewPlain(w.stdout)
	p.Header(b.Version)
	o := r.opts
	ours := docker.CLI{}.ProjectPorts(ctx, o.Project)
	p.Section(text.T("preflight.title"))
	checks := preflight.Checks(w.preflightEnv(o, ours))
	results := preflight.RunAll(ctx, checks)
	p.Checks(checks, results)

	p.Section(text.T("cli.doctor_install"))
	if r.st == nil {
		p.Line(text.T("cli.not_installed", "dir", o.Dir))
	} else {
		p.Line(text.T("cli.doctor_state", "mode", text.T("mode."+modeKey(o.Mode)+"_name"), "project", o.Project, "dir", o.Dir))
		p.Line(text.T("cli.doctor_images", "registry", o.Registry, "tag", o.Tag, "how", first(r.st.Images, "-")))
		u := o.URLs()
		if v, err := demo.New(u.API, u.Site).Version(ctx); err == nil {
			p.Line(ui.PlainMark("ok") + " " + text.T("cli.status_api", "version", v.Version, "url", u.App))
		} else {
			p.Line(ui.PlainMark("warn") + " " + text.T("cli.status_api_down", "url", u.API))
		}
	}
	if preflight.Failed(results) {
		return exitFailed, nil
	}
	return exitOK, nil
}

func runDemo(ctx context.Context, w world, args []string, b Build) (int, error) {
	c, err := subcommandFlags("demo", args, nil)
	if err != nil {
		return exitUsage, err
	}
	r, err := loadInstalled(ctx, w, c, b)
	if err != nil {
		return exitFailed, err
	}
	u := r.opts.URLs()
	outln(w.stdout, text.T("cli.demo_start", "url", u.API))
	last := ""
	res, err := demo.New(u.API, u.Site).Twin(ctx, func(p demo.Progress) {
		line := text.T("cli.demo_phase_" + p.Phase)
		if p.Phase == "upload" {
			line += fmt.Sprintf(" %d/%d", p.Done+1, p.Total)
		}
		if line != last {
			last = line
			outln(w.stdout, "  "+line)
		}
	})
	outcome := ui.Outcome{Mode: r.opts.Mode, URLs: u, Demo: res, DemoErr: err}
	for _, l := range ui.DemoLines(outcome) {
		outln(w.stdout, l)
	}
	if err != nil {
		return exitFailed, nil //nolint:nilerr // the failure is in the printed lines
	}
	outln(w.stdout, text.T("payoff.results", "url", u.Site+"/resultados?run="+res.RunID))
	return exitOK, nil
}

// devEnviron is the environment of every make of the Desenvolver mode outside the installation
// (docmatch up, down, uninstall): the toolchain folders the installation may have added to
// PATH, and GOTOOLCHAIN as during the installation.
func (w world) devEnviron(ctx context.Context, opts install.Options) []string {
	d := &devTools{w: w}
	d.refreshPath(w.toolchainEnv(opts.SourceDir), toolchain.ReadRequirements(opts.SourceDir))
	return install.New(opts, nil).DevEnviron(ctx)
}

// makeTarget runs `make <target>` in the repository (Desenvolver installs).
func makeTarget(ctx context.Context, w world, dir, target string, env []string) (int, error) {
	cmd := commandIn(ctx, dir, "make", target)
	cmd.Env = env
	cmd.Stdout, cmd.Stderr, cmd.Stdin = w.stdout, w.stderr, w.stdin
	if err := cmd.Run(); err != nil {
		return exitFailed, err
	}
	return exitOK, nil
}
