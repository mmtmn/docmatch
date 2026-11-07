package ui

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/exp/teatest"

	"github.com/mmtmn/docmatch/tools/installer/internal/install"
	"github.com/mmtmn/docmatch/tools/installer/internal/platform"
	"github.com/mmtmn/docmatch/tools/installer/internal/preflight"
	"github.com/mmtmn/docmatch/tools/installer/internal/state"
	"github.com/mmtmn/docmatch/tools/installer/internal/toolchain"
)

func devStatuses(missing ...string) []toolchain.Status {
	versions := map[string]string{"git": "2.43.0", "make": "4.3", "cc": "13.3.0", "go": "1.22.2; o Go baixa o go1.27.3 do go.mod sozinho (GOTOOLCHAIN=auto)",
		"rust": "1.93.1 (o rustup segue o rust-toolchain.toml)", "node": "22.23.4", "pnpm": "11.5.3", "docker": "Compose 2.40.3", "openssl": "3.0.13", "curl": "8.5.0"}
	out := make([]toolchain.Status, 0, len(versions))
	for _, id := range []string{"git", "make", "cc", "go", "rust", "node", "pnpm", "docker", "openssl", "curl"} {
		s := toolchain.Status{ID: id, Name: toolchain.Name(id), Detail: versions[id]}
		for _, m := range missing {
			if m == id {
				s.State, s.Detail = toolchain.Missing, "não encontrado"
			}
		}
		out = append(out, s)
	}
	return out
}

func ubuntuPlan() toolchain.Plan {
	return toolchain.Plan{
		Manager: toolchain.Manager{ID: toolchain.Apt, Name: "apt"},
		Commands: []toolchain.Command{
			{Args: []string{"sudo", "apt-get", "update"}, Elevated: true},
			{Args: []string{"sudo", "apt-get", "install", "-y", "golang-go", "rustup"}, Elevated: true},
			{Args: []string{"rustup", "default", "stable"}},
			{Args: []string{"sudo", "snap", "install", "node", "--classic", "--channel=22/stable"}, Elevated: true},
			{Args: []string{"npm", "install", "--global", "--prefix", "/home/ana/.local", "pnpm@11.5.3"}},
		},
		Manual: []toolchain.Manual{{Tool: "docker", Fix: "Ligue a integração com o WSL no Docker Desktop (Settings > Resources > WSL integration) ou instale o Docker Engine dentro do WSL."}},
	}
}

func devDeps(t *testing.T) Deps {
	t.Helper()
	d := deps(t, false)
	d.Opts.Mode = state.ModeDev
	d.Opts.SourceDir = "/home/ana/docmatch"
	d.Tools = func(context.Context, install.Options) ToolsReport { return ToolsReport{} }
	return d
}

func toolsAt(t *testing.T, r ToolsReport, results []toolchain.Result) *App {
	t.Helper()
	a := sized(New(devDeps(t)), 90, 44)
	a.screen = screenTools
	a.tools = newTools(a.t)
	a.Update(toolsReportMsg(r))
	a.tools.results = results
	return a
}

func TestFrameTools(t *testing.T) {
	a := toolsAt(t, ToolsReport{Statuses: devStatuses("go", "rust", "node", "pnpm", "docker"), Plan: ubuntuPlan()}, nil)
	if !a.tools.canInstall() {
		t.Fatal("há o que instalar")
	}
	frame(t, a.View())
}

func TestFrameToolsAfterFailure(t *testing.T) {
	plan := ubuntuPlan()
	results := []toolchain.Result{
		{Command: plan.Commands[0]},
		{Command: plan.Commands[1], Err: errors.New("exit status 100"), ExitCode: 100},
		{Command: plan.Commands[2], Skipped: true},
		{Command: plan.Commands[3], Skipped: true},
		{Command: plan.Commands[4], Skipped: true},
	}
	a := toolsAt(t, ToolsReport{Statuses: devStatuses("go", "rust", "node", "pnpm"), Plan: plan}, results)
	frame(t, a.View())
}

func TestFrameToolsReady(t *testing.T) {
	plan := ubuntuPlan()
	results := make([]toolchain.Result, 0, len(plan.Commands))
	for _, c := range plan.Commands {
		results = append(results, toolchain.Result{Command: c})
	}
	a := toolsAt(t, ToolsReport{Statuses: devStatuses(), PathAdded: []string{"/home/ana/.cargo/bin", "/snap/bin", "/home/ana/.local/bin"}}, results)
	frame(t, a.View())
}

func TestFrameToolsWindows(t *testing.T) {
	d := devDeps(t)
	d.Info = platform.Info{OS: "windows", Arch: "amd64"}
	a := sized(New(d), 90, 40)
	a.screen = screenTools
	a.tools = newTools(a.t)
	a.Update(toolsReportMsg(ToolsReport{
		Windows: true,
		Statuses: []toolchain.Status{
			{ID: "wsl", Name: "WSL2", State: toolchain.Missing, Detail: "não encontrado"},
			{ID: "docker", Name: "Docker", Detail: "Compose 2.40.3"},
		},
		Plan: toolchain.Plan{Manager: toolchain.Manager{ID: toolchain.Winget, Name: "winget"},
			Commands: []toolchain.Command{{Args: []string{"wsl", "--install", "-d", "Ubuntu"}, Elevated: true},
				{Args: []string{"winget", "install", "--exact", "--id", "Docker.DockerDesktop", "--accept-package-agreements", "--accept-source-agreements"}, Elevated: true}},
			Notes: []string{"O wsl --install pode pedir para reiniciar o Windows."}},
		Handoff: "curl -fsSL https://site.invalid/install/install.sh | sh -s -- --mode desenvolver",
	}))
	frame(t, a.View())
}

func TestFrameChecksToolsHint(t *testing.T) {
	d := devDeps(t)
	d.Checks = func(install.Options) []preflight.Check {
		return []preflight.Check{
			{ID: "os", Name: "Sistema", Run: func(context.Context) preflight.Result { return preflight.Result{Detail: "linux/amd64"} }},
			{ID: "docker", Name: "Docker", Run: func(context.Context) preflight.Result { return preflight.Result{Detail: "29.1.3"} }},
			{ID: "tool_go", Name: "Go", Run: func(context.Context) preflight.Result {
				return preflight.Result{Status: preflight.Fail, Detail: "não encontrado", Fix: "Instale o Go 1.21 ou mais novo (go.dev/dl)."}
			}},
		}
	}
	a := sized(New(d), 90, 30)
	runChecks(a)
	if !a.checks.toolsHint || !a.checks.failedAny(installable) {
		t.Fatal("uma ferramenta faltando leva à tela de ferramentas")
	}
	frame(t, a.View())
	a.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if a.screen != screenTools {
		t.Fatalf("Enter: tela %d", a.screen)
	}
}

func TestToolsKeys(t *testing.T) {
	a := toolsAt(t, ToolsReport{Statuses: devStatuses("go"), Plan: ubuntuPlan()}, nil)
	a.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if a.screen != screenTools || a.tools.busy != "" {
		t.Fatal("Enter não instala nada")
	}
	// Nothing to install and nothing missing: Enter goes back through the checks.
	a = toolsAt(t, ToolsReport{Statuses: devStatuses()}, nil)
	a.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if a.screen != screenChecks {
		t.Fatalf("tela %d", a.screen)
	}
	// Ready on Windows: Enter ends with the handoff on screen.
	a = toolsAt(t, ToolsReport{Windows: true, Statuses: []toolchain.Status{{ID: "wsl", Name: "WSL2"}}}, nil)
	_, cmd := a.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil || a.ExitCode != 0 {
		t.Fatal("Windows pronto: Enter sai")
	}
}

// fakeRun is a ToolsRun that "installs" by flipping ready (it runs on the program's goroutine).
type fakeRun struct {
	plan    toolchain.Plan
	ready   *atomic.Bool
	mu      sync.Mutex
	results []toolchain.Result
}

func (f *fakeRun) Run() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.plan.Commands {
		f.results = append(f.results, toolchain.Result{Command: c})
	}
	f.ready.Store(true)
	return nil
}
func (f *fakeRun) SetStdin(io.Reader)  {}
func (f *fakeRun) SetStdout(io.Writer) {}
func (f *fakeRun) SetStderr(io.Writer) {}
func (f *fakeRun) Results() []toolchain.Result {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]toolchain.Result{}, f.results...)
}

// The whole Desenvolver path: a missing tool, the plan, i runs it with the terminal handed
// over, the checks again, the choices and the installation.
func TestFlowDevTools(t *testing.T) {
	var ready atomic.Bool
	d := devDeps(t)
	d.FixedMode = true
	d.Checks = func(install.Options) []preflight.Check {
		return []preflight.Check{{ID: "tool_go", Name: "Go", Run: func(context.Context) preflight.Result {
			if ready.Load() {
				return preflight.Result{Detail: "1.27.3"}
			}
			return preflight.Result{Status: preflight.Fail, Detail: "não encontrado", Fix: "Instale o Go."}
		}}}
	}
	d.Tools = func(context.Context, install.Options) ToolsReport {
		if ready.Load() {
			return ToolsReport{Statuses: devStatuses(), PathAdded: []string{"/home/ana/.local/bin"}}
		}
		return ToolsReport{Statuses: devStatuses("go"), Plan: toolchain.Plan{Manager: toolchain.Manager{ID: "apt", Name: "apt"},
			Commands: []toolchain.Command{{Args: []string{"sudo", "apt-get", "install", "-y", "golang-go"}, Elevated: true}}}}
	}
	ranCh := make(chan *fakeRun, 1)
	d.RunTools = func(p toolchain.Plan) ToolsRun { r := &fakeRun{plan: p, ready: &ready}; ranCh <- r; return r }
	tm := teatest.NewTestModel(t, New(d), teatest.WithInitialTermSize(100, 44))
	waitFor(t, tm, "começar")
	enter(tm)
	waitFor(t, tm, "ver como instalar")
	enter(tm)
	waitFor(t, tm, "instalar agora")
	enter(tm) // Enter never installs: only i does
	time.Sleep(30 * time.Millisecond)
	select {
	case <-ranCh:
		t.Fatal("Enter não roda o plano")
	default:
	}
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("i")})
	// The terminal goes to the plan (tea.Exec); teatest's fake terminal cannot be taken back, so
	// the rest of the path is driven on the model below.
	var ran *fakeRun
	select {
	case ran = <-ranCh:
	case <-time.After(5 * time.Second):
		t.Fatal("i roda o plano")
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(ran.Results()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	tm.Send(tea.KeyMsg{Type: tea.KeyCtrlC})
	tm.WaitFinished(t, teatest.WithFinalTimeout(5*time.Second))
	a := tm.FinalModel(t).(*App)
	if len(ran.Results()) != 1 || a.screen != screenTools || a.tools.report == nil || !a.tools.report.Ready() || len(a.tools.results) != 1 {
		t.Fatalf("depois do plano, verificado de novo: %+v %+v", ran.Results(), a.tools.report)
	}

	// Ready: Enter goes back through the checks (now green) and on to the Desenvolver choices.
	b := sized(New(d), 100, 44)
	b.opts.Mode = state.ModeDev
	b.screen, b.tools = screenTools, newTools(b.t)
	b.Update(toolsReportMsg(d.Tools(context.Background(), b.opts)))
	_, cmd := b.Update(tea.KeyMsg{Type: tea.KeyEnter})
	for cmd != nil && b.screen == screenChecks && !b.checks.finished() {
		_, cmd = b.Update(cmd())
	}
	if b.screen != screenChecks || b.checks.failed() {
		t.Fatalf("tela %d", b.screen)
	}
	b.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if b.screen != screenChoices || !strings.Contains(b.View(), "Roda em /home/ana/docmatch") {
		t.Fatalf("tela %d\n%s", b.screen, b.View())
	}
}

func TestFlowDevInstallToolsFlag(t *testing.T) {
	var ready atomic.Bool
	d := devDeps(t)
	d.InstallTools = true
	d.Tools = func(context.Context, install.Options) ToolsReport {
		if ready.Load() {
			return ToolsReport{Statuses: devStatuses()}
		}
		return ToolsReport{Statuses: devStatuses("go"), Plan: toolchain.Plan{Commands: []toolchain.Command{{Args: []string{"brew", "install", "go"}}}}}
	}
	d.RunTools = func(p toolchain.Plan) ToolsRun { return &fakeRun{plan: p, ready: &ready} }
	a := sized(New(d), 90, 40)
	a.screen = screenTools
	a.tools = newTools(a.t)
	_, cmd := a.Update(toolsReportMsg(d.Tools(context.Background(), a.opts)))
	if cmd == nil || a.tools.busy != "running" {
		t.Fatal("--install-tools roda o plano assim que ele aparece")
	}
}
