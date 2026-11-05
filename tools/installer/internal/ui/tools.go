package ui

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/mmtmn/docmatch/tools/installer/internal/text"
	"github.com/mmtmn/docmatch/tools/installer/internal/toolchain"
)

// ToolsReport is the Desenvolver toolchain of this machine and how to install what is missing.
type ToolsReport struct {
	Statuses  []toolchain.Status
	Plan      toolchain.Plan
	PathAdded []string // folders added to this run's PATH (installed tools live there)
	// Handoff is what to run inside WSL after preparing native Windows ("" elsewhere).
	Handoff string
	Windows bool // native Windows: the screen prepares WSL2 instead of the make local tools
}

// Missing lists the tools that are not ready.
func (r ToolsReport) Missing() []string {
	var out []string
	for _, s := range r.Statuses {
		if !s.OK() {
			out = append(out, s.Name)
		}
	}
	return out
}

// Ready reports whether every tool is there.
func (r ToolsReport) Ready() bool { return len(r.Missing()) == 0 }

// ToolsRun runs a plan with the terminal handed over (sudo asks for the password itself); its
// results are read after the full screen comes back.
type ToolsRun interface {
	tea.ExecCommand
	Results() []toolchain.Result
}

// toolsModel is the Desenvolver screen between the checks and the choices: what each tool is,
// the exact commands that install the missing ones (the elevated ones marked) and, after they
// ran, how each ended.
type toolsModel struct {
	t       Theme
	report  *ToolsReport
	results []toolchain.Result
	busy    string // "checking", "running" or ""
	spin    spinner.Model
}

type (
	toolsReportMsg ToolsReport
	toolsRanMsg    struct{ results []toolchain.Result }
)

func newTools(t Theme) toolsModel {
	return toolsModel{t: t, busy: "checking", spin: spinner.New(spinner.WithSpinner(t.Spinner), spinner.WithStyle(t.Accent))}
}

func (m *toolsModel) spinTick() tea.Cmd {
	if !m.t.Animate {
		return nil
	}
	return m.spin.Tick
}

func (m *toolsModel) spinUpdate(msg spinner.TickMsg) tea.Cmd {
	if m.busy == "" || !m.t.Animate {
		return nil
	}
	var cmd tea.Cmd
	m.spin, cmd = m.spin.Update(msg)
	return cmd
}

func (m *toolsModel) canInstall() bool {
	return m.busy == "" && m.report != nil && !m.report.Plan.Empty() && !m.report.Ready()
}

const toolNameWidth = 16

func (m *toolsModel) view(w int) string {
	t := m.t
	var b strings.Builder
	title := text.T("tools.title")
	if m.report != nil && m.report.Windows {
		title = text.T("tools.title_windows")
	}
	b.WriteString(t.Heading.Render(title) + "\n\n")
	if m.report == nil {
		b.WriteString(m.busyLine())
		return b.String()
	}
	r := m.report
	for _, s := range r.Statuses {
		kind := "ok"
		if !s.OK() {
			kind = "fail"
		}
		name := lipgloss.NewStyle().Width(toolNameWidth).Render(t.plainText(s.Name))
		b.WriteString(t.glyph(kind) + " " + t.Body.Render(name) + t.Dim.Render(truncate(t.plainText(s.Detail), w-toolNameWidth-2)) + "\n")
	}
	b.WriteString("\n")

	if len(m.results) > 0 {
		b.WriteString(t.Heading.Render(text.T("tools.results_title")) + "\n")
		for _, res := range m.results {
			cmd := res.Command.String()
			switch {
			case res.Skipped:
				b.WriteString(t.glyph("skip") + " " + t.Dim.Render(truncate(t.plainText(text.T("tools.result_skipped", "cmd", cmd)), w-2)) + "\n")
			case res.Err != nil:
				b.WriteString(t.glyph("fail") + " " + t.FailS.Render(truncate(t.plainText(text.T("tools.result_failed", "cmd", cmd, "code", strconv.Itoa(res.ExitCode))), w-2)) + "\n")
			default:
				b.WriteString(t.glyph("ok") + " " + t.Body.Render(truncate(cmd, w-2)) + "\n")
			}
		}
		if toolchain.Failed(m.results) {
			b.WriteString(t.FailS.Width(w).Render(t.plainText(text.T("tools.failed"))) + "\n")
		}
		b.WriteString("\n")
	}

	if r.Ready() {
		b.WriteString(t.OKs.Bold(true).Render(t.G.OK+" "+text.T("tools.all_ok")) + "\n")
	} else {
		m.planView(&b, w)
	}
	if len(r.PathAdded) > 0 {
		b.WriteString("\n" + t.Dim.Width(w).Render(t.plainText(text.T("tools.path_added", "dirs", strings.Join(r.PathAdded, ", ")))) + "\n")
	}
	if r.Handoff != "" {
		b.WriteString("\n" + t.Accent.Width(w).Render(t.plainText(text.T("tools.handoff", "cmd", r.Handoff))) + "\n")
	}
	b.WriteString("\n")
	if m.busy != "" {
		b.WriteString(m.busyLine())
		return b.String()
	}
	switch {
	case r.Ready() && r.Windows:
		b.WriteString(hints(t, "Enter", text.T("hint.exit")))
	case r.Ready():
		b.WriteString(hints(t, "Enter", text.T("hint.continue"), "q", text.T("hint.quit")))
	case m.canInstall():
		b.WriteString(hints(t, "i", text.T("tools.hint_install"), "r", text.T("preflight.hint_retry"), "q", text.T("hint.quit")))
	default:
		b.WriteString(hints(t, "r", text.T("preflight.hint_retry"), "q", text.T("hint.quit")))
	}
	return b.String()
}

func (m *toolsModel) planView(b *strings.Builder, w int) {
	t := m.t
	p := m.report.Plan
	if !p.Empty() {
		b.WriteString(t.Heading.Render(text.T("tools.plan_title", "manager", p.Manager.Name)) + "\n")
		for _, c := range p.Commands {
			mark := "  "
			if c.Elevated {
				mark = t.WarnS.Render(t.G.Warn) + " "
			}
			// Wrapped between arguments, never cut: the user consents to every character.
			for i, line := range wrapWords("$ "+c.String(), w-6) {
				if i == 0 {
					b.WriteString("  " + mark + t.Accent.Render(line) + "\n")
				} else {
					b.WriteString("        " + t.Accent.Render(line) + "\n")
				}
			}
		}
		if n := p.Elevated(); n > 0 {
			b.WriteString("\n" + t.WarnS.Render(t.G.Warn) + " " + indentRest(t.WarnS.Width(w-2).Render(t.plainText(text.T("tools.elevated_note", "count", strconv.Itoa(n)))), 2) + "\n")
		}
	}
	if len(p.Manual) > 0 {
		head := text.T("tools.manual_title")
		if p.Manager.ID == "" {
			head = text.T("tools.no_manager")
		}
		b.WriteString("\n" + t.Heading.Render(head) + "\n")
		for _, mf := range p.Manual {
			line := t.Body.Render(toolchain.Name(mf.Tool)+": ") + t.Dim.Render(t.plainText(mf.Fix))
			b.WriteString(indentRest(lipgloss.NewStyle().Width(w-4).Render("  "+t.G.Bullet+" "+line), 4) + "\n")
		}
	}
	for _, n := range p.Notes {
		b.WriteString("\n" + t.Dim.Width(w).Render(t.plainText(n)))
	}
	if len(p.Notes) > 0 {
		b.WriteString("\n")
	}
}

// wrapWords splits s at spaces into lines of at most width cells (a longer word keeps a line of
// its own, whole).
func wrapWords(s string, width int) []string {
	var lines []string
	cur := ""
	for _, word := range strings.Fields(s) {
		switch {
		case cur == "":
			cur = word
		case lipgloss.Width(cur)+1+lipgloss.Width(word) <= width:
			cur += " " + word
		default:
			lines = append(lines, cur)
			cur = word
		}
	}
	return append(lines, cur)
}

func (m *toolsModel) busyLine() string {
	t := m.t
	frame := t.G.Pending
	if t.Animate {
		frame = m.spin.View()
	}
	msg := text.T("tools.checking")
	if m.busy == "running" {
		msg = text.T("tools.running")
	}
	return frame + " " + t.Dim.Render(t.plainText(msg))
}
