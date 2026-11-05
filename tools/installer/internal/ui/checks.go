package ui

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/mmtmn/docmatch/tools/installer/internal/preflight"
	"github.com/mmtmn/docmatch/tools/installer/internal/text"
)

// checksModel runs the preflight one check at a time, each line animating in.
type checksModel struct {
	t       Theme
	checks  []preflight.Check
	results []*preflight.Result
	current int
	spin    spinner.Model
}

func newChecks(t Theme, checks []preflight.Check) checksModel {
	return checksModel{t: t, checks: checks, results: make([]*preflight.Result, len(checks)), spin: spinner.New(spinner.WithSpinner(t.Spinner), spinner.WithStyle(t.Accent))}
}

// next runs the current check in the background.
func (m *checksModel) next(ctx context.Context) tea.Cmd {
	if m.current >= len(m.checks) {
		return nil
	}
	i, c, animate := m.current, m.checks[m.current], m.t.Animate
	run := func() tea.Msg {
		start := time.Now()
		r := c.Run(ctx)
		// A short beat per line, so the list reads as a sequence instead of appearing at once.
		if d := 90*time.Millisecond - time.Since(start); animate && d > 0 {
			time.Sleep(d)
		}
		return checkMsg{index: i, result: r}
	}
	if animate && i == 0 {
		return tea.Batch(run, m.spin.Tick)
	}
	return run
}

func (m *checksModel) finished() bool { return m.current >= len(m.checks) }

func (m *checksModel) failed() bool {
	for _, r := range m.results {
		if r != nil && r.Status == preflight.Fail {
			return true
		}
	}
	return false
}

func (m *checksModel) update(ctx context.Context, msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case checkMsg:
		r := msg.result
		m.results[msg.index] = &r
		m.current = msg.index + 1
		if m.finished() {
			return nil, true
		}
		return m.next(ctx), false
	case spinner.TickMsg:
		if m.finished() || !m.t.Animate {
			return nil, false
		}
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return cmd, false
	}
	return nil, false
}

const checkNameWidth = 26

func (m *checksModel) view(w int) string {
	t := m.t
	var b strings.Builder
	b.WriteString(t.Heading.Render(text.T("preflight.title")) + "\n\n")
	for i, c := range m.checks {
		name := lipgloss.NewStyle().Width(checkNameWidth).Render(t.plainText(c.Name))
		r := m.results[i]
		switch {
		case r == nil && i == m.current:
			frame := t.G.Pending
			if t.Animate {
				frame = m.spin.View()
			}
			b.WriteString(frame + " " + t.Body.Render(name) + "\n")
		case r == nil:
			b.WriteString(t.glyph("pending") + " " + t.Dim.Render(name) + "\n")
		default:
			kind := map[preflight.Status]string{preflight.OK: "ok", preflight.Warn: "warn", preflight.Fail: "fail", preflight.Skip: "skip"}[r.Status]
			detail := truncate(t.plainText(r.Detail), w-checkNameWidth-2)
			b.WriteString(t.glyph(kind) + " " + t.Body.Render(name) + t.Dim.Render(detail) + "\n")
			if r.Fix != "" && (r.Status == preflight.Fail || r.Status == preflight.Warn) {
				style := t.WarnS
				if r.Status == preflight.Fail {
					style = t.FailS
				}
				fix := style.Width(w - 4).Render(t.plainText(r.Fix))
				b.WriteString(indent(fix, 4) + "\n")
			}
		}
	}
	b.WriteString("\n")
	if m.finished() {
		if m.failed() {
			n := 0
			for _, r := range m.results {
				if r != nil && r.Status == preflight.Fail {
					n++
				}
			}
			b.WriteString(t.FailS.Render(text.T("preflight.summary_fail", "count", strconv.Itoa(n))) + "\n\n")
			b.WriteString(hints(t, "r", text.T("preflight.hint_retry"), "q", text.T("hint.quit")))
		} else {
			b.WriteString(hints(t, "Enter", text.T("hint.continue"), "q", text.T("hint.quit")))
		}
	}
	return b.String()
}

// indent prefixes every line of s with n spaces.
func indent(s string, n int) string {
	pad := strings.Repeat(" ", n)
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = pad + l
	}
	return strings.Join(lines, "\n")
}

// indentRest indents every line of s but the first (a hanging indent after a glyph).
func indentRest(s string, n int) string {
	first, rest, ok := strings.Cut(s, "\n")
	if !ok {
		return s
	}
	return first + "\n" + indent(rest, n)
}

// truncate shortens s to w cells with an ellipsis.
func truncate(s string, w int) string {
	if w <= 1 || lipgloss.Width(s) <= w {
		return s
	}
	r := []rune(s)
	for len(r) > 0 && lipgloss.Width(string(r))+1 > w {
		r = r[:len(r)-1]
	}
	return string(r) + "…"
}
