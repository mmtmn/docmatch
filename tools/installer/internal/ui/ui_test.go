package ui

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/exp/golden"
	"github.com/charmbracelet/x/exp/teatest"
	"github.com/muesli/termenv"

	"github.com/mmtmn/docmatch/tools/installer/internal/demo"
	"github.com/mmtmn/docmatch/tools/installer/internal/flow"
	"github.com/mmtmn/docmatch/tools/installer/internal/install"
	"github.com/mmtmn/docmatch/tools/installer/internal/platform"
	"github.com/mmtmn/docmatch/tools/installer/internal/ports"
	"github.com/mmtmn/docmatch/tools/installer/internal/preflight"
	"github.com/mmtmn/docmatch/tools/installer/internal/state"
)

func TestMain(m *testing.M) {
	// Golden frames are plain text: no colors, a fixed background guess.
	lipgloss.SetColorProfile(termenv.Ascii)
	lipgloss.SetHasDarkBackground(true)
	os.Exit(m.Run())
}

var t0 = time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)

// clock is a fake time source the tests move by hand.
type clock struct{ now time.Time }

func (c *clock) Now() time.Time          { return c.now }
func (c *clock) add(d time.Duration) any { c.now = c.now.Add(d); return nil }

func opts() install.Options {
	return install.Options{
		Mode: state.ModeTry, Dir: "/home/ana/.local/share/docmatch/local", Project: "docmatch-local",
		Ports: ports.Defaults(), LLMMode: "off", Tag: "dev", Demo: true, Info: platform.Info{OS: "linux", Arch: "amd64"},
	}
}

func fakeChecks(results ...preflight.Result) func(install.Options) []preflight.Check {
	names := []string{"Sistema", "Espaço em disco", "Docker", "Docker rodando", "Porta 4321 (site)"}
	return func(install.Options) []preflight.Check {
		out := make([]preflight.Check, 0, len(results))
		for i, r := range results {
			out = append(out, preflight.Check{ID: names[i], Name: names[i], Run: func(context.Context) preflight.Result { return r }})
		}
		return out
	}
}

var goodChecks = fakeChecks(
	preflight.Result{Status: preflight.OK, Detail: "linux/amd64"},
	preflight.Result{Status: preflight.OK, Detail: "48,2 GB livres em /var/lib/docker"},
	preflight.Result{Status: preflight.OK, Detail: "29.1.3"},
	preflight.Result{Status: preflight.OK, Detail: "servidor 29.1.3"},
	preflight.Result{Status: preflight.Warn, Detail: "em uso por outro programa", Fix: "Escolha outra porta na próxima tela (sugestão: 4322) ou use --port-web 4322."},
)

func twinResult() *demo.Result {
	return &demo.Result{RunID: "run_01TESTE", Findings: 23, Expected: 23, AmountAtRisk: 18336889, Currency: "BRL",
		Top: &demo.Finding{Title: "Pagamento parcial de exatamente 50%", AmountAtRisk: 4900000,
			Evidence: []demo.Evidence{{Label: "Linha 7 do extrato", Excerpt: "16/07/2026 · LIQUIDACAO COBRANCA SIMPLES · R$ 49.000,00"}}}}
}

func outcome(o install.Options) Outcome {
	return Outcome{Mode: o.Mode, Project: o.Project, Dir: o.Dir, URLs: o.URLs(), Demo: twinResult(), LogPath: o.Dir + "/logs/install.log"}
}

// fakePlan is two quick steps and the twin.
func fakePlan(fail bool) func(install.Options) Plan {
	return func(o install.Options) Plan {
		step := func(id, title, detail string) flow.Step {
			return flow.Step{ID: id, Title: title, Run: func(_ context.Context, r *flow.Reporter) (flow.Result, error) {
				r.Log("linha de " + id)
				r.Progress([]flow.Bar{{Label: "api", Fraction: 0.5, Text: "8/16"}}, "trabalhando")
				return flow.Done(detail), nil
			}}
		}
		steps := []flow.Step{step("prepare", "Preparar a pasta", o.Dir), step("images", "Imagens da docmatch", "api 110MB")}
		if fail {
			steps = append(steps, flow.Step{ID: "services", Title: "Harness, API, worker e site", Run: func(context.Context, *flow.Reporter) (flow.Result, error) {
				return flow.Result{}, errors.New("dependency failed to start: container docmatch-local-api-1 is unhealthy")
			}})
		}
		return Plan{Steps: steps, Outcome: func() Outcome { return outcome(o) }, LogPath: o.Dir + "/logs/install.log"}
	}
}

func deps(t *testing.T, animate bool) Deps {
	t.Helper()
	c := &clock{now: t0}
	return Deps{
		Theme: NewTheme(false, animate, true), Info: platform.Info{OS: "linux", Arch: "amd64"}, Version: "0.1.0",
		Opts: opts(), Checks: goodChecks, Plan: fakePlan(false), Now: c.Now,
		PortFree: func(p int) bool { return p != 4321 },
	}
}

func frame(t *testing.T, view string) {
	t.Helper()
	// Trailing spaces are layout padding; trimming them keeps the golden files readable.
	lines := strings.Split(view, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " ")
	}
	golden.RequireEqual(t, []byte(strings.Join(lines, "\n")+"\n"))
}

func sized(a *App, w, h int) *App {
	a.Update(tea.WindowSizeMsg{Width: w, Height: h})
	return a
}

// ------------------------------------------------------------------ golden frames

func TestFrameSplash(t *testing.T) {
	a := sized(New(deps(t, false)), 90, 22)
	frame(t, a.View())
}

func TestFrameSplashShimmer(t *testing.T) {
	a := sized(New(deps(t, true)), 90, 22)
	for i := 0; i < 20; i++ { // a third of a second into the sweep
		a.Update(frameMsg(t0))
	}
	if a.splash.ready() {
		t.Fatal("a animação ainda devia estar rodando")
	}
	frame(t, a.View())
	for i := 0; i < 2*FPS && !a.splash.ready(); i++ {
		a.Update(frameMsg(t0))
	}
	if !a.splash.ready() || a.splash.frames >= maxFrames {
		t.Fatalf("o brilho para sozinho (quadros: %d)", a.splash.frames)
	}
}

// The wordmark in true color: the brand gradient, and the shimmer brightening the band it is on.
func TestWordmarkColors(t *testing.T) {
	r := lipgloss.NewRenderer(&bytes.Buffer{})
	r.SetColorProfile(termenv.TrueColor)
	old := lipgloss.DefaultRenderer()
	lipgloss.SetDefaultRenderer(r)
	defer lipgloss.SetDefaultRenderer(old)
	th := NewTheme(false, true, true)
	still, shimmer := th.renderWordmark(-1e6), th.renderWordmark(20)
	if still == shimmer || !strings.Contains(still, "\x1b[") {
		t.Fatal("o brilho precisa mudar as cores")
	}
	first, last := th.Gradient[0].Hex(), th.Gradient[len(th.Gradient)-1].Hex()
	if first != "#1d4ed8" || last != "#93c5fd" {
		t.Fatalf("gradiente fora dos tokens: %s -> %s", first, last)
	}
	golden.RequireEqualEscape(t, []byte(shimmer), true)
}

func TestFrameMode(t *testing.T) {
	d := deps(t, false)
	d.Resume = &state.State{Completed: []string{"prepare", "base_images", "images"}}
	a := sized(New(d), 90, 30)
	a.Update(tea.KeyMsg{Type: tea.KeyEnter})
	frame(t, a.View())
}

func TestFrameModeWindowsDev(t *testing.T) {
	d := deps(t, false)
	d.Info = platform.Info{OS: "windows", Arch: "amd64"}
	a := sized(New(d), 70, 30) // narrow: the cards stack
	a.Update(tea.KeyMsg{Type: tea.KeyEnter})
	a.Update(tea.KeyMsg{Type: tea.KeyRight})
	frame(t, a.View())
}

func runChecks(a *App) {
	cmd := a.startChecks()
	for cmd != nil {
		msg := cmd()
		if b, ok := msg.(tea.BatchMsg); ok {
			msg = b[0]()
		}
		_, cmd = a.Update(msg)
	}
}

func TestFrameChecks(t *testing.T) {
	d := deps(t, false)
	d.Checks = fakeChecks(
		preflight.Result{Status: preflight.OK, Detail: "darwin/arm64"},
		preflight.Result{Status: preflight.Warn, Detail: "4,1 GB livres em /Users/ana", Fix: "São necessários uns 6,0 GB. Libere espaço (docker system prune remove imagens sem uso) ou escolha outra pasta."},
		preflight.Result{Status: preflight.OK, Detail: "29.1.3"},
		preflight.Result{Status: preflight.Fail, Detail: "instalado, mas não está rodando", Fix: "Abra o Docker Desktop (ou rode colima start), espere ele ficar pronto e tente de novo."},
		preflight.Result{Status: preflight.Skip, Detail: "depende do Docker"},
	)
	a := sized(New(d), 90, 30)
	runChecks(a)
	if !a.checks.failed() {
		t.Fatal("falha esperada")
	}
	frame(t, a.View())
}

func TestFrameChoices(t *testing.T) {
	a := sized(New(deps(t, false)), 90, 40)
	runChecks(a)
	a.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if a.screen != screenChoices || a.opts.Ports[ports.Web] != 4322 {
		t.Fatalf("tela %d, portas %v", a.screen, a.opts.Ports)
	}
	frame(t, a.View())
}

func TestFrameChoicesPorts(t *testing.T) {
	a := sized(New(deps(t, false)), 90, 40)
	runChecks(a)
	a.Update(tea.KeyMsg{Type: tea.KeyEnter})
	_, cmd := a.Update(tea.KeyMsg{Type: tea.KeyEnter}) // model: off -> the ports
	for cmd != nil {
		msg := cmd()
		if b, ok := msg.(tea.BatchMsg); ok {
			for _, c := range b {
				if c != nil {
					a.Update(c())
				}
			}
			break
		}
		_, cmd = a.Update(msg)
	}
	frame(t, a.View())
}

func progressAt(t *testing.T, fail bool) (*App, *clock) {
	t.Helper()
	c := &clock{now: t0}
	d := deps(t, false)
	d.Now = c.Now
	a := sized(New(d), 90, 40)
	a.screen = screenProgress
	p := d.Plan(a.opts)
	if fail {
		p = fakePlan(true)(a.opts)
	}
	a.progress = newProgress(a.t, p, c.Now, a.contentWidth(), 40)
	feed := func(e flow.Event) { a.progress.event(e) }
	feed(flow.Event{Kind: flow.Started, Step: "prepare", Title: "Preparar a pasta"})
	c.add(120 * time.Millisecond)
	feed(flow.Event{Kind: flow.Finished, Step: "prepare", Outcome: flow.OK, Detail: "/home/ana/.local/share/docmatch/local", Elapsed: 120 * time.Millisecond})
	feed(flow.Event{Kind: flow.Started, Step: "images", Title: "Compilar as imagens da docmatch"})
	feed(flow.Event{Kind: flow.Progressed, Step: "images", Note: "compilando a partir de /home/ana/docmatch (as próximas vezes usam o cache)",
		Bars: []flow.Bar{{Label: "api", Fraction: 0.75, Text: "12/16  RUN go build"}, {Label: "harness", Fraction: 0.33, Text: "2/6  RUN cargo build"}, {Label: "web", Fraction: -1}}})
	for i := 0; i < 30; i++ {
		feed(flow.Event{Kind: flow.Logged, Step: "images", Line: "#12 [harness build 6/6] Compiling dm-core v0.1.0 (" + string(rune('a'+i%26)) + ")"})
	}
	c.add(83 * time.Second)
	return a, c
}

func TestFrameProgress(t *testing.T) {
	a, _ := progressAt(t, false)
	frame(t, a.View())
}

func TestFrameProgressLog(t *testing.T) {
	a, _ := progressAt(t, false)
	a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("l")})
	if !a.progress.showLog {
		t.Fatal("l mostra o log")
	}
	frame(t, a.View())
}

func TestFrameFailure(t *testing.T) {
	a, c := progressAt(t, true)
	a.progress.event(flow.Event{Kind: flow.Finished, Step: "images", Outcome: flow.OK, Detail: "api 110MB · harness 23.5MB · web 22MB", Elapsed: 83 * time.Second})
	a.progress.event(flow.Event{Kind: flow.Started, Step: "services", Title: "Harness, API, worker e site"})
	c.add(4 * time.Second)
	err := errors.New("dependency failed to start: container docmatch-local-api-1 is unhealthy")
	a.progress.event(flow.Event{Kind: flow.Finished, Step: "services", Outcome: flow.Failed, Err: err, Elapsed: 4 * time.Second})
	a.Update(runDoneMsg{err: err})
	if !a.progress.failed() || a.screen != screenProgress {
		t.Fatal("falha fica na tela de progresso")
	}
	frame(t, a.View())
}

func TestFramePayoff(t *testing.T) {
	a := sized(New(deps(t, false)), 90, 40)
	a.screen = screenPayoff
	a.payoff = newPayoff(a.t, outcome(a.opts), 3*time.Minute+41*time.Second, true)
	a.Update(browserMsg{})
	frame(t, a.View())
}

func TestFramePayoffASCII(t *testing.T) {
	d := deps(t, false)
	d.Theme = NewTheme(true, false, true)
	a := sized(New(d), 90, 40)
	a.screen = screenPayoff
	out := outcome(a.opts)
	out.Demo, out.DemoErr = nil, errors.New("login local: HTTP 404 not_found")
	a.payoff = newPayoff(a.t, out, 95*time.Second, false)
	view := a.View()
	for _, r := range view {
		if r > 127 && !strings.ContainsRune("áàâãéêíóôõúçÁÉÍÓÚÇü", r) {
			t.Fatalf("caractere fora do ASCII (além dos acentos): %q", r)
		}
	}
	frame(t, view)
}

func TestFramePlain(t *testing.T) {
	var buf bytes.Buffer
	c := &clock{now: t0}
	p := NewPlain(&buf)
	p.Now = c.Now
	p.Header("0.1.0")
	p.Section("Verificando esta máquina")
	checks := goodChecks(opts())
	p.Checks(checks, preflight.RunAll(context.Background(), checks))
	p.Section("Instalando a docmatch")
	p.Event(flow.Event{Kind: flow.Started, Step: "images", Title: "Imagens da docmatch"})
	p.Event(flow.Event{Kind: flow.Progressed, Step: "images", Note: "baixando de ghcr.io/owner"})
	p.Event(flow.Event{Kind: flow.Started, Step: "images", Title: "Compilar as imagens da docmatch"})
	c.add(6 * time.Second)
	p.Event(flow.Event{Kind: flow.Progressed, Step: "images", Bars: []flow.Bar{{Label: "api", Fraction: 0.5, Text: "8/16"}, {Label: "web", Fraction: 0.25}, {Label: "harness", Fraction: -1}}})
	p.Event(flow.Event{Kind: flow.Progressed, Step: "images", Bars: []flow.Bar{{Label: "api", Fraction: 0.6}}}) // throttled
	p.Event(flow.Event{Kind: flow.Logged, Step: "images", Line: "#5 DONE 0.1s"})
	p.Event(flow.Event{Kind: flow.Finished, Step: "images", Outcome: flow.OK, Detail: "api 110MB · web 22MB", Elapsed: 72 * time.Second})
	p.Event(flow.Event{Kind: flow.Started, Step: "demo", Title: "Conciliar o gêmeo da Vantara"})
	p.Event(flow.Event{Kind: flow.Finished, Step: "demo", Outcome: flow.Warned, Err: nil, Detail: "login local: HTTP 404", Elapsed: 300 * time.Millisecond})
	p.Tail()
	p.Section("pronto")
	for _, l := range Summary(outcome(opts()), 3*time.Minute+41*time.Second) {
		p.Line(l)
	}
	for _, r := range buf.String() {
		if r > 127 && !strings.ContainsRune("áàâãéêíóôõúçÁÉÍÓÚÇü", r) {
			t.Fatalf("saída simples com %q", r)
		}
	}
	frame(t, buf.String())
}

func TestFormatDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{400 * time.Millisecond: "0,4 s", 9500 * time.Millisecond: "9,5 s", 12 * time.Second: "12 s", 221 * time.Second: "3:41", -1: ""} {
		if got := formatDuration(d); got != want {
			t.Errorf("%v: %q", d, got)
		}
	}
}

// ------------------------------------------------------------- interactive flows

func waitFor(t *testing.T, tm *teatest.TestModel, text string) {
	t.Helper()
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool { return bytes.Contains(b, []byte(text)) },
		teatest.WithDuration(10*time.Second), teatest.WithCheckInterval(5*time.Millisecond))
}

func enter(tm *teatest.TestModel) { tm.Send(tea.KeyMsg{Type: tea.KeyEnter}) }

func TestFlowInteractive(t *testing.T) {
	d := deps(t, true)
	var opened string
	d.OpenBrowser = func(url string) error { opened = url; return nil }
	var planned install.Options
	d.Plan = func(o install.Options) Plan { planned = o; return fakePlan(false)(o) }
	app := New(d)
	tm := teatest.NewTestModel(t, app, teatest.WithInitialTermSize(100, 44))
	waitFor(t, tm, "começar")
	enter(tm)
	waitFor(t, tm, "Como você quer rodar")
	enter(tm)
	waitFor(t, tm, "continuar")
	enter(tm)
	waitFor(t, tm, "Modelo de linguagem")
	for i := 0; i < 6; i++ { // model, four ports, folder
		time.Sleep(30 * time.Millisecond)
		enter(tm)
	}
	waitFor(t, tm, "Próximos comandos")
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	tm.WaitFinished(t, teatest.WithFinalTimeout(5*time.Second))
	final := tm.FinalModel(t).(*App)
	if final.ExitCode != 0 || final.Outcome() == nil || final.Outcome().Demo.Findings != 23 {
		t.Fatalf("fim: %d %+v", final.ExitCode, final.Outcome())
	}
	if planned.Ports[ports.Web] != 4322 || planned.Ports[ports.API] != 8080 || planned.LLMMode != "off" || planned.Dir != opts().Dir {
		t.Fatalf("escolhas: %+v", planned)
	}
	if opened != "http://localhost:4322/app" {
		t.Fatalf("navegador: %q", opened)
	}
}

func TestFlowYes(t *testing.T) {
	d := deps(t, true)
	d.Yes = true
	tm := teatest.NewTestModel(t, New(d), teatest.WithInitialTermSize(100, 44))
	waitFor(t, tm, "Próximos comandos")
	enter(tm)
	tm.WaitFinished(t, teatest.WithFinalTimeout(5*time.Second))
	if a := tm.FinalModel(t).(*App); a.ExitCode != 0 || a.Outcome() == nil || a.opts.Ports[ports.Web] != 4322 {
		t.Fatalf("%d %v", a.ExitCode, a.opts.Ports)
	}
}

func TestFlowChecksFailQuit(t *testing.T) {
	d := deps(t, false)
	d.FixedMode = true
	d.Checks = fakeChecks(preflight.Result{Status: preflight.Fail, Detail: "não encontrado", Fix: "Instale o Docker."})
	tm := teatest.NewTestModel(t, New(d), teatest.WithInitialTermSize(100, 30))
	waitFor(t, tm, "começar")
	enter(tm)
	waitFor(t, tm, "verificar de novo")
	enter(tm) // Enter does nothing while a check fails
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	tm.WaitFinished(t, teatest.WithFinalTimeout(5*time.Second))
	if a := tm.FinalModel(t).(*App); a.ExitCode != 1 || a.screen != screenChecks {
		t.Fatalf("%d %d", a.ExitCode, a.screen)
	}
}

func TestFlowFailureAndCancel(t *testing.T) {
	d := deps(t, false)
	d.Yes = true
	d.Plan = fakePlan(true)
	tm := teatest.NewTestModel(t, New(d), teatest.WithInitialTermSize(100, 44))
	waitFor(t, tm, "Falhou")
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	tm.WaitFinished(t, teatest.WithFinalTimeout(5*time.Second))
	if a := tm.FinalModel(t).(*App); a.ExitCode != 1 || a.Outcome() != nil {
		t.Fatalf("falha: %d", a.ExitCode)
	}

	d = deps(t, false)
	d.Yes = true
	block := make(chan struct{})
	d.Plan = func(o install.Options) Plan {
		return Plan{Steps: []flow.Step{{ID: "images", Title: "Imagens", Run: func(ctx context.Context, _ *flow.Reporter) (flow.Result, error) {
			close(block)
			<-ctx.Done()
			return flow.Result{}, ctx.Err()
		}}}, Outcome: func() Outcome { return Outcome{} }}
	}
	tm = teatest.NewTestModel(t, New(d), teatest.WithInitialTermSize(100, 44))
	<-block
	tm.Send(tea.KeyMsg{Type: tea.KeyCtrlC})
	tm.WaitFinished(t, teatest.WithFinalTimeout(5*time.Second))
	if a := tm.FinalModel(t).(*App); a.ExitCode != 130 {
		t.Fatalf("ctrl+c: %d", a.ExitCode)
	}
}
