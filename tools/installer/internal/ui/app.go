package ui

import (
	"context"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/mmtmn/docmatch/tools/installer/internal/demo"
	"github.com/mmtmn/docmatch/tools/installer/internal/flow"
	"github.com/mmtmn/docmatch/tools/installer/internal/install"
	"github.com/mmtmn/docmatch/tools/installer/internal/platform"
	"github.com/mmtmn/docmatch/tools/installer/internal/ports"
	"github.com/mmtmn/docmatch/tools/installer/internal/preflight"
	"github.com/mmtmn/docmatch/tools/installer/internal/state"
	"github.com/mmtmn/docmatch/tools/installer/internal/text"
)

// Plan is a ready-to-run installation.
type Plan struct {
	Steps   []flow.Step
	Done    func(id string)    // records a finished step (state.json)
	Log     func(e flow.Event) // mirrors every event to the log file
	Outcome func() Outcome     // what the payoff shows, read after the steps
	LogPath string
}

// Outcome is the payoff of a finished installation.
type Outcome struct {
	Mode    string
	Project string
	Dir     string
	URLs    install.URLs
	Demo    *demo.Result
	DemoErr error
	LogPath string
}

// Deps are the outside world of the interface; tests replace all of it.
type Deps struct {
	Theme   Theme
	Info    platform.Info
	Version string
	// Opts are the defaults from flags and the saved state.
	Opts install.Options
	// Resume is the previous installation in the folder (nil when there is none).
	Resume *state.State
	// FixedMode skips the mode screen (--mode); Yes skips the questions (--yes).
	FixedMode, Yes bool
	// ExplicitPorts are ports given on the command line (never replaced by suggestions).
	ExplicitPorts map[string]bool
	Checks        func(opts install.Options) []preflight.Check
	Plan          func(opts install.Options) Plan
	// OpenBrowser is nil when no browser should open (--no-browser, SSH, CI).
	OpenBrowser func(url string) error
	Now         func() time.Time
	PortFree    ports.Prober
	Ours        map[int]bool
}

type screen int

const (
	screenSplash screen = iota
	screenMode
	screenChecks
	screenChoices
	screenProgress
	screenPayoff
)

// Messages.
type (
	frameMsg time.Time
	clockMsg time.Time
	checkMsg struct {
		index  int
		result preflight.Result
	}
	eventMsg   flow.Event
	runDoneMsg struct{ err error }
	browserMsg struct{ err error }
)

// App is the root model.
type App struct {
	deps   Deps
	t      Theme
	screen screen
	opts   install.Options

	width, height int

	splash   splashModel
	mode     modeModel
	checks   checksModel
	choices  *choicesModel
	progress progressModel
	payoff   payoffModel

	ctx    context.Context
	cancel context.CancelFunc
	msgs   chan tea.Msg

	// ExitCode is set when the program ends (0 ok, 1 failed, 130 cancelled).
	ExitCode int
	outcome  *Outcome
}

// New builds the interface.
func New(deps Deps) *App {
	if deps.Now == nil {
		deps.Now = time.Now
	}
	if deps.PortFree == nil {
		deps.PortFree = ports.Free
	}
	a := &App{deps: deps, t: deps.Theme, opts: deps.Opts, width: 80, height: 30}
	a.ctx, a.cancel = context.WithCancel(context.Background())
	a.splash = newSplash(a.t, deps.Yes)
	a.mode = newMode(a.t, deps.Info, deps.Opts.Mode, deps.Resume, deps.Opts.Dir)
	return a
}

// Init starts the splash.
func (a *App) Init() tea.Cmd { return a.splash.init() }

// Outcome is the result shown on the payoff (nil when the installation did not finish).
func (a *App) Outcome() *Outcome { return a.outcome }

// Total is how long the installation took (0 before it starts).
func (a *App) Total() time.Duration {
	if a.progress.now == nil {
		return 0
	}
	return a.progress.total()
}

// Cancel stops a running installation.
func (a *App) Cancel() { a.cancel() }

func (a *App) contentWidth() int {
	w := a.width - 4
	if w > 86 {
		w = 86
	}
	if w < 40 {
		w = 40
	}
	return w
}

// Update routes messages to the current screen.
func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.width, a.height = msg.Width, msg.Height
		a.progress.resize(a.contentWidth(), a.height)
		if a.choices != nil {
			a.choices.resize(a.contentWidth(), a.height)
		}
	case tea.KeyMsg:
		if msg.Type == tea.KeyCtrlC {
			return a, a.quit(130)
		}
	}
	switch a.screen {
	case screenSplash:
		return a.updateSplash(msg)
	case screenMode:
		return a.updateMode(msg)
	case screenChecks:
		return a.updateChecks(msg)
	case screenChoices:
		return a.updateChoices(msg)
	case screenProgress:
		return a.updateProgress(msg)
	case screenPayoff:
		return a.updatePayoff(msg)
	}
	return a, nil
}

func (a *App) quit(code int) tea.Cmd {
	a.ExitCode = code
	a.cancel()
	return tea.Quit
}

func isKey(msg tea.Msg, keys ...string) bool {
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return false
	}
	for _, s := range keys {
		if k.String() == s {
			return true
		}
	}
	return false
}

func (a *App) updateSplash(msg tea.Msg) (tea.Model, tea.Cmd) {
	if isKey(msg, "q", "esc") {
		return a, a.quit(0)
	}
	if _, ok := msg.(tea.KeyMsg); ok {
		if !a.splash.ready() {
			a.splash.finish()
			return a, nil
		}
		return a, a.afterSplash()
	}
	cmd, advance := a.splash.update(msg)
	if advance {
		return a, a.afterSplash()
	}
	return a, cmd
}

func (a *App) afterSplash() tea.Cmd {
	if a.deps.FixedMode || a.deps.Yes {
		return a.startChecks()
	}
	a.screen = screenMode
	return nil
}

func (a *App) updateMode(msg tea.Msg) (tea.Model, tea.Cmd) {
	if isKey(msg, "q", "esc") {
		return a, a.quit(0)
	}
	if chosen := a.mode.update(msg); chosen != "" {
		a.opts.Mode = chosen
		return a, a.startChecks()
	}
	return a, nil
}

func (a *App) startChecks() tea.Cmd {
	a.screen = screenChecks
	a.checks = newChecks(a.t, a.deps.Checks(a.opts))
	return a.checks.next(a.ctx)
}

func (a *App) updateChecks(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch {
	case isKey(msg, "q", "esc"):
		return a, a.quit(1)
	case isKey(msg, "r") && a.checks.finished():
		return a, a.startChecks()
	case isKey(msg, "enter") && a.checks.finished() && !a.checks.failed():
		return a, a.afterChecks()
	}
	cmd, done := a.checks.update(a.ctx, msg)
	if done && !a.checks.failed() && a.deps.Yes {
		return a, a.afterChecks()
	}
	return a, cmd
}

// suggestedPorts swaps busy ports (not ours, not given on the command line) for free ones.
func (a *App) suggestedPorts() ports.Set {
	keep := ports.Set{}
	for name := range a.deps.ExplicitPorts {
		keep[name] = a.opts.Ports[name]
	}
	resolved, _ := ports.Resolve(a.opts.Ports, a.deps.Ours, a.deps.PortFree)
	for name, n := range keep {
		resolved[name] = n
	}
	return resolved
}

func (a *App) afterChecks() tea.Cmd {
	if a.opts.Mode == state.ModeTry {
		a.opts.Ports = a.suggestedPorts()
	}
	if a.deps.Yes {
		return a.startProgress()
	}
	a.screen = screenChoices
	a.choices = newChoices(a.t, a.opts, a.deps.Ours, a.deps.PortFree, a.deps.Info.OS)
	a.choices.resize(a.contentWidth(), a.height)
	return a.choices.form.Init()
}

func (a *App) updateChoices(msg tea.Msg) (tea.Model, tea.Cmd) {
	cmd, status := a.choices.update(msg)
	switch status {
	case formDone:
		a.opts = a.choices.apply(a.opts)
		return a, a.startProgress()
	case formAborted:
		return a, a.quit(0)
	}
	return a, cmd
}

func (a *App) startProgress() tea.Cmd {
	a.screen = screenProgress
	plan := a.deps.Plan(a.opts)
	a.progress = newProgress(a.t, plan, a.deps.Now, a.contentWidth(), a.height)
	a.msgs = make(chan tea.Msg, 512)
	go func(ctx context.Context, plan Plan, out chan<- tea.Msg) {
		err := flow.Run(ctx, plan.Steps, func(e flow.Event) {
			if plan.Log != nil {
				plan.Log(e)
			}
			out <- eventMsg(e)
		}, plan.Done)
		out <- runDoneMsg{err: err}
	}(a.ctx, plan, a.msgs)
	return tea.Batch(a.waitMsg(), a.progress.init())
}

func (a *App) waitMsg() tea.Cmd {
	ch := a.msgs
	return func() tea.Msg { return <-ch }
}

func (a *App) updateProgress(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m := msg.(type) {
	case eventMsg:
		a.progress.event(flow.Event(m))
		return a, a.waitMsg()
	case runDoneMsg:
		a.progress.finish(m.err)
		if m.err == nil {
			out := a.progress.plan.Outcome()
			a.outcome = &out
			a.screen = screenPayoff
			a.payoff = newPayoff(a.t, out, a.progress.total(), a.deps.OpenBrowser != nil)
			return a, a.openBrowser(out.URLs.App)
		}
		return a, nil
	}
	if a.progress.failed() && isKey(msg, "q", "esc", "enter") {
		return a, a.quit(1)
	}
	if !a.progress.failed() && isKey(msg, "q") {
		return a, a.quit(130)
	}
	return a, a.progress.update(msg)
}

func (a *App) openBrowser(url string) tea.Cmd {
	if a.deps.OpenBrowser == nil {
		return nil
	}
	open := a.deps.OpenBrowser
	return func() tea.Msg { return browserMsg{err: open(url)} }
}

func (a *App) updatePayoff(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m, ok := msg.(browserMsg); ok {
		a.payoff.browserErr = m.err
		a.payoff.opened = m.err == nil
		return a, nil
	}
	switch {
	case isKey(msg, "q", "esc", "enter"):
		return a, a.quit(0)
	case isKey(msg, "o") && a.outcome != nil:
		return a, a.openBrowser(a.outcome.URLs.App)
	}
	return a, nil
}

// View draws the current screen.
func (a *App) View() string {
	w := a.contentWidth()
	var body string
	switch a.screen {
	case screenSplash:
		return a.splash.view(a.width, a.height)
	case screenMode:
		body = a.mode.view(w)
	case screenChecks:
		body = a.checks.view(w)
	case screenChoices:
		body = a.choices.view()
	case screenProgress:
		body = a.progress.view(w)
	case screenPayoff:
		body = a.payoff.view(w)
	}
	return lipgloss.NewStyle().Padding(1, 2).Render(a.header(w) + "\n\n" + body)
}

// header is the small brand line with the breadcrumb of the screens.
func (a *App) header(w int) string {
	brand := a.t.Title.Render("docmatch")
	if a.deps.Version != "" {
		brand += a.t.Dim.Render(" " + a.deps.Version)
	}
	stages := []struct {
		s   screen
		key string
	}{{screenMode, "nav.mode"}, {screenChecks, "nav.checks"}, {screenChoices, "nav.choices"}, {screenProgress, "nav.install"}, {screenPayoff, "nav.done"}}
	parts := make([]string, 0, len(stages))
	for _, st := range stages {
		label := text.T(st.key)
		switch {
		case st.s == a.screen:
			parts = append(parts, a.t.Accent.Bold(true).Render(label))
		case st.s < a.screen:
			parts = append(parts, a.t.Dim.Render(label))
		default:
			parts = append(parts, a.t.Dim.Faint(true).Render(label))
		}
	}
	crumbs := strings.Join(parts, a.t.Dim.Render(a.t.sep()))
	gap := w - lipgloss.Width(brand) - lipgloss.Width(crumbs)
	if gap < 2 {
		return brand + "\n" + crumbs
	}
	return brand + strings.Repeat(" ", gap) + crumbs
}
