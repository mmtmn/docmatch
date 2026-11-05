package ui

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/mmtmn/docmatch/tools/installer/internal/flow"
	"github.com/mmtmn/docmatch/tools/installer/internal/text"
)

type stepState int

const (
	statePending stepState = iota
	stateRunning
	stateDone
	stateSkipped
	stateWarned
	stateFailed
)

type stepView struct {
	id, title    string
	state        stepState
	note, detail string
	bars         []flow.Bar
	started      time.Time
	elapsed      time.Duration
	err          error
}

const maxLogLines = 2000

// progressModel follows the flow events: one line per step, the running step's bars and note,
// elapsed time per step and a log pane toggled with `l`.
type progressModel struct {
	t        Theme
	plan     Plan
	steps    []*stepView
	byID     map[string]*stepView
	logs     []string
	showLog  bool
	vp       viewport.Model
	spin     spinner.Model
	bar      progress.Model
	now      func() time.Time
	start    time.Time
	end      time.Time
	finished bool
	err      error
	height   int
}

func newProgress(t Theme, plan Plan, now func() time.Time, width, height int) progressModel {
	m := progressModel{t: t, plan: plan, byID: map[string]*stepView{}, now: now, start: now(), height: height}
	for _, s := range plan.Steps {
		v := &stepView{id: s.ID, title: s.Title}
		m.steps = append(m.steps, v)
		m.byID[s.ID] = v
	}
	m.spin = spinner.New(spinner.WithSpinner(t.Spinner), spinner.WithStyle(t.Accent))
	opts := []progress.Option{progress.WithoutPercentage(), progress.WithWidth(22)}
	if len(t.Gradient) >= 2 {
		opts = append(opts, progress.WithGradient(t.Gradient[0].Hex(), t.Gradient[len(t.Gradient)-1].Hex()))
	}
	if t.ASCII {
		opts = append(opts, progress.WithFillCharacters('#', '-'))
	}
	m.bar = progress.New(opts...)
	m.vp = viewport.New(width, 10)
	m.resize(width, height)
	return m
}

func clockTick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return clockMsg(t) })
}

func (m *progressModel) init() tea.Cmd {
	if m.t.Animate {
		return tea.Batch(m.spin.Tick, clockTick())
	}
	return clockTick()
}

func (m *progressModel) resize(width, height int) {
	m.height = height
	m.vp.Width = width - 4
	h := height / 3
	if h < 6 {
		h = 6
	}
	if h > 16 {
		h = 16
	}
	m.vp.Height = h
}

func (m *progressModel) event(e flow.Event) {
	s := m.byID[e.Step]
	if s == nil {
		return
	}
	switch e.Kind {
	case flow.Started:
		if s.state != stateRunning {
			s.state, s.started = stateRunning, m.now()
		}
		if e.Title != "" {
			s.title = e.Title
		}
	case flow.Progressed:
		if e.Bars != nil {
			s.bars = e.Bars
		}
		if e.Note != "" {
			s.note = e.Note
		}
	case flow.Logged:
		m.logs = append(m.logs, e.Line)
		if len(m.logs) > maxLogLines {
			m.logs = m.logs[len(m.logs)-maxLogLines:]
		}
		if m.showLog {
			m.refreshLog()
		}
	case flow.Finished:
		s.elapsed, s.detail, s.err = e.Elapsed, e.Detail, e.Err
		s.bars, s.note = nil, ""
		s.state = map[flow.Outcome]stepState{flow.OK: stateDone, flow.Skipped: stateSkipped, flow.Warned: stateWarned, flow.Failed: stateFailed}[e.Outcome]
	}
}

func (m *progressModel) refreshLog() {
	atBottom := m.vp.AtBottom() || m.vp.TotalLineCount() == 0
	m.vp.SetContent(strings.Join(m.logs, "\n"))
	if atBottom {
		m.vp.GotoBottom()
	}
}

func (m *progressModel) finish(err error) {
	m.finished, m.err, m.end = true, err, m.now()
	if err != nil {
		m.showLog = true
		m.refreshLog()
	}
}

func (m *progressModel) failed() bool { return m.finished && m.err != nil }

func (m *progressModel) total() time.Duration {
	if m.end.IsZero() {
		return m.now().Sub(m.start)
	}
	return m.end.Sub(m.start)
}

func (m *progressModel) update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case spinner.TickMsg:
		if m.finished || !m.t.Animate {
			return nil
		}
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return cmd
	case clockMsg:
		if m.finished {
			return nil
		}
		return clockTick()
	case tea.KeyMsg:
		if msg.String() == "l" {
			m.showLog = !m.showLog
			if m.showLog {
				m.refreshLog()
			}
			return nil
		}
		if m.showLog {
			var cmd tea.Cmd
			m.vp, cmd = m.vp.Update(msg)
			return cmd
		}
	case tea.MouseMsg:
		if m.showLog {
			var cmd tea.Cmd
			m.vp, cmd = m.vp.Update(msg)
			return cmd
		}
	}
	return nil
}

const stepTitleWidth = 36

func (m *progressModel) stepLine(s *stepView, w int) string {
	t := m.t
	var glyph, title string
	elapsed := ""
	switch s.state {
	case statePending:
		glyph, title = t.glyph("pending"), t.Dim.Render(fit(t.plainText(s.title), stepTitleWidth))
	case stateRunning:
		glyph = t.Accent.Render(t.G.Pending)
		if t.Animate {
			glyph = m.spin.View()
		}
		title = t.Heading.Render(fit(t.plainText(s.title), stepTitleWidth))
		elapsed = formatDuration(m.now().Sub(s.started))
	default:
		kind := map[stepState]string{stateDone: "ok", stateSkipped: "skip", stateWarned: "warn", stateFailed: "fail"}[s.state]
		glyph, title = t.glyph(kind), t.Body.Render(fit(t.plainText(s.title), stepTitleWidth))
		elapsed = formatDuration(s.elapsed)
	}
	detailW := w - 2 - stepTitleWidth - 1 - 8
	detail := ""
	if s.state != stateRunning && s.state != statePending {
		detail = t.plainText(truncate(s.detail, detailW))
	}
	left := glyph + " " + title + " " + t.Dim.Render(detail)
	gap := w - lipgloss.Width(left) - lipgloss.Width(elapsed)
	if gap < 1 {
		gap = 1
	}
	return left + strings.Repeat(" ", gap) + t.Dim.Render(elapsed)
}

func (m *progressModel) barLines(s *stepView, w int) string {
	t := m.t
	var b strings.Builder
	if s.note != "" {
		b.WriteString("    " + t.Dim.Render(t.plainText(truncate(s.note, w-4))) + "\n")
	}
	for _, bar := range s.bars {
		label := lipgloss.NewStyle().Width(10).Render(truncate(bar.Label, 9))
		var drawn string
		if bar.Fraction < 0 {
			drawn = t.Dim.Render(strings.Repeat(t.G.BarEmpty, 22))
		} else {
			drawn = m.bar.ViewAs(bar.Fraction)
		}
		txt := t.plainText(truncate(bar.Text, w-4-10-23-1))
		b.WriteString("    " + t.Body.Render(label) + drawn + " " + t.Dim.Render(txt) + "\n")
	}
	return b.String()
}

func (m *progressModel) view(w int) string {
	t := m.t
	var b strings.Builder
	b.WriteString(t.Heading.Render(text.T("progress.title")) + "\n\n")
	for _, s := range m.steps {
		b.WriteString(m.stepLine(s, w) + "\n")
		if s.state == stateRunning {
			b.WriteString(m.barLines(s, w))
		}
	}
	b.WriteString("\n")
	if m.failed() {
		b.WriteString(m.failureBox(w) + "\n")
	}
	if m.showLog {
		box := t.Panel.Width(w - 2).Render(m.vp.View())
		b.WriteString(t.Dim.Render(text.T("progress.log_title")) + "\n" + box + "\n")
	}
	toggle := text.T("progress.log_show")
	if m.showLog {
		toggle = text.T("progress.log_hide")
	}
	switch {
	case m.failed():
		b.WriteString(hints(t, "l", toggle, "q", text.T("hint.quit")))
	default:
		b.WriteString(hints(t, "l", toggle, "q", text.T("progress.cancel")))
	}
	return b.String()
}

func (m *progressModel) failureBox(w int) string {
	t := m.t
	var failedStep *stepView
	for _, s := range m.steps {
		if s.state == stateFailed {
			failedStep = s
		}
	}
	title := text.T("progress.failed_generic")
	if failedStep != nil {
		title = text.T("progress.failed", "step", failedStep.title)
	}
	msg := ""
	if m.err != nil {
		msg = m.err.Error()
	}
	if errors.Is(m.err, flow.ErrCancelled) {
		title = text.T("progress.cancelled")
		msg = ""
	}
	body := t.FailS.Bold(true).Render(t.G.Fail+" "+t.plainText(title)) + "\n"
	if msg != "" {
		body += t.Body.Width(w-6).Render(t.plainText(msg)) + "\n"
	}
	body += "\n" + t.Dim.Width(w-6).Render(t.plainText(text.T("progress.failed_hint", "path", m.plan.LogPath)))
	return t.Panel.BorderForeground(t.Danger).Width(w - 2).Render(body)
}

// fit pads or truncates s to exactly w cells.
func fit(s string, w int) string {
	s = truncate(s, w)
	if pad := w - lipgloss.Width(s); pad > 0 {
		s += strings.Repeat(" ", pad)
	}
	return s
}

// formatDuration renders "0,4 s", "12 s", "3:41".
func formatDuration(d time.Duration) string {
	switch {
	case d < 0:
		return ""
	case d < 10*time.Second:
		return strings.Replace(fmt.Sprintf("%.1f s", d.Seconds()), ".", ",", 1)
	case d < time.Minute:
		return fmt.Sprintf("%d s", int(d.Seconds()))
	default:
		s := int(d.Round(time.Second).Seconds())
		return fmt.Sprintf("%d:%02d", s/60, s%60)
	}
}
