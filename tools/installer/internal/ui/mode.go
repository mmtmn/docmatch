package ui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/mmtmn/docmatch/tools/installer/internal/platform"
	"github.com/mmtmn/docmatch/tools/installer/internal/state"
	"github.com/mmtmn/docmatch/tools/installer/internal/text"
)

var modes = []string{state.ModeTry, state.ModeDev}

// modeModel chooses between Experimentar and Desenvolver.
type modeModel struct {
	t      Theme
	info   platform.Info
	cursor int
	resume *state.State
	dir    string
}

func newMode(t Theme, info platform.Info, current string, resume *state.State, dir string) modeModel {
	m := modeModel{t: t, info: info, resume: resume, dir: dir}
	if current == state.ModeDev {
		m.cursor = 1
	}
	return m
}

// update returns the chosen mode once Enter is pressed.
func (m *modeModel) update(msg tea.Msg) string {
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return ""
	}
	switch k.String() {
	case "left", "h", "up", "k", "shift+tab":
		m.cursor = 0
	case "right", "l", "down", "j", "tab":
		m.cursor = 1
	case "1":
		return modes[0]
	case "2":
		return modes[1]
	case "enter", " ":
		return modes[m.cursor]
	}
	return ""
}

// cardLines is how many lines the taller card's content needs at width.
func (m *modeModel) cardLines(width int) int {
	h := 0
	for i := range modes {
		if n := lipgloss.Height(m.cardContent(i, width)); n > h {
			h = n
		}
	}
	return h
}

func (m *modeModel) cardContent(i, width int) string {
	t := m.t
	key := "mode.try"
	if modes[i] == state.ModeDev {
		key = "mode.dev"
	}
	pointer, name := "  ", t.Heading.Render(text.T(key+"_name"))
	if i == m.cursor {
		pointer, name = t.Accent.Render(t.G.Pointer+" "), t.Title.Render(text.T(key+"_name"))
	}
	return pointer + name + "\n" + t.Dim.Width(width-6).Render(t.plainText(text.T(key+"_text")))
}

func (m *modeModel) card(i, width, lines int) string {
	style := m.t.Card
	if i == m.cursor {
		style = m.t.CardActive
	}
	return style.Width(width - 2).Height(lines).Render(m.cardContent(i, width))
}

func (m *modeModel) view(w int) string {
	t := m.t
	var b strings.Builder
	b.WriteString(t.Heading.Render(text.T("mode.title")) + "\n\n")
	if w >= 76 {
		cw := (w - 2) / 2
		lines := m.cardLines(cw)
		b.WriteString(lipgloss.JoinHorizontal(lipgloss.Top, m.card(0, cw, lines), "  ", m.card(1, cw, lines)))
	} else {
		b.WriteString(m.card(0, w, 0) + "\n" + m.card(1, w, 0))
	}
	b.WriteString("\n")
	if modes[m.cursor] == state.ModeDev && m.info.OS == "windows" {
		b.WriteString("\n" + t.WarnS.Render(t.G.Warn) + " " + indentRest(t.WarnS.Width(w-2).Render(t.plainText(text.T("mode.dev_windows"))), 2) + "\n")
	}
	if m.resume != nil {
		last := m.resume.LastDone()
		if last == "" {
			last = "-"
		} else {
			last = text.T("steps." + last)
		}
		b.WriteString("\n" + t.Accent.Width(w).Render(t.plainText(text.T("mode.resume", "dir", m.dir, "step", last))) + "\n")
	}
	b.WriteString("\n" + hints(t, "←/→", text.T("mode.hint_move"), "Enter", text.T("mode.hint_pick"), "q", text.T("hint.quit")))
	return b.String()
}

// hints renders "key action · key action".
func hints(t Theme, pairs ...string) string {
	parts := make([]string, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		parts = append(parts, t.Key.Render(t.plainText(pairs[i]))+" "+t.Dim.Render(t.plainText(pairs[i+1])))
	}
	return strings.Join(parts, t.Dim.Render(t.sep()))
}
