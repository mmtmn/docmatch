package ui

import (
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/mmtmn/docmatch/tools/installer/internal/demo"
	"github.com/mmtmn/docmatch/tools/installer/internal/state"
	"github.com/mmtmn/docmatch/tools/installer/internal/text"
)

// payoffModel is the last screen: the stack is up, the twin is reconciled, here is what next.
type payoffModel struct {
	t          Theme
	out        Outcome
	total      time.Duration
	browser    bool // a browser is being opened
	opened     bool
	browserErr error
}

func newPayoff(t Theme, out Outcome, total time.Duration, browser bool) payoffModel {
	return payoffModel{t: t, out: out, total: total, browser: browser}
}

// NextCommands are the commands the payoff suggests, with what they do.
func NextCommands(mode string) [][2]string {
	if mode == state.ModeDev {
		return [][2]string{
			{"docmatch demo", text.T("payoff.next_demo")},
			{"make e2e-local", text.T("payoff.next_e2e")},
			{"make local-down", text.T("payoff.next_down")},
		}
	}
	return [][2]string{
		{"docmatch status", text.T("payoff.next_status")},
		{"docmatch logs", text.T("payoff.next_logs")},
		{"docmatch demo", text.T("payoff.next_demo")},
		{"docmatch down", text.T("payoff.next_down")},
	}
}

// DemoLines describes the reconciled twin (or why it did not reconcile).
func DemoLines(out Outcome) []string {
	switch {
	case out.Demo != nil:
		d := out.Demo
		lines := []string{text.T("payoff.findings", "count", d.Findings, "amount", demo.FormatMoney(d.AmountAtRisk, d.Currency))}
		if d.Expected > 0 {
			lines[0] += " · " + text.T("payoff.score", "got", d.Findings, "want", d.Expected)
		}
		if d.Top != nil {
			lines = append(lines, text.T("payoff.evidence", "title", d.Top.Title, "evidence", demo.EvidenceLine(d.Top)))
		}
		return lines
	case out.DemoErr != nil:
		return []string{text.T("payoff.demo_failed", "error", out.DemoErr.Error())}
	default:
		return []string{text.T("payoff.demo_skipped")}
	}
}

// Summary is the payoff as plain lines (printed after the full screen closes, and by --plain).
func Summary(out Outcome, total time.Duration) []string {
	lines := []string{text.T("payoff.title") + " (" + formatDuration(total) + ")", ""}
	lines = append(lines, text.T("payoff.twin_title"))
	for _, l := range DemoLines(out) {
		lines = append(lines, "  "+l)
	}
	if out.Demo != nil {
		lines = append(lines, "  "+text.T("payoff.results", "url", out.URLs.Site+"/resultados?run="+out.Demo.RunID))
	}
	lines = append(lines, "",
		"  "+pad(text.T("payoff.site"), 9)+out.URLs.App+"  "+text.T("payoff.signed_in"),
		"  "+pad(text.T("payoff.api"), 9)+out.URLs.API+"/v1/version",
		"  "+pad(text.T("payoff.mailpit"), 9)+out.URLs.Mailpit,
		"", text.T("payoff.next_title"))
	for _, c := range NextCommands(out.Mode) {
		lines = append(lines, "  "+pad(c[0], 18)+c[1])
	}
	if out.LogPath != "" {
		lines = append(lines, "", text.T("payoff.log", "path", out.LogPath))
	}
	return lines
}

func pad(s string, w int) string {
	if n := w - lipgloss.Width(s); n > 0 {
		return s + strings.Repeat(" ", n)
	}
	return s + " "
}

func (m *payoffModel) view(w int) string {
	t := m.t
	out := m.out
	var b strings.Builder
	b.WriteString(t.OKs.Bold(true).Render(t.G.OK+" "+text.T("payoff.title")) + t.Dim.Render(t.sep()+formatDuration(m.total)) + "\n\n")

	twin := t.Heading.Render(text.T("payoff.twin_title")) + "\n"
	for i, l := range DemoLines(out) {
		var style lipgloss.Style
		switch {
		case out.Demo == nil:
			style = t.WarnS
		case i == 0:
			style = t.Accent.Bold(true)
		default:
			style = t.Dim
		}
		twin += style.Width(w-6).Render(t.plainText(l)) + "\n"
	}
	if out.Demo != nil {
		twin += t.Dim.Width(w - 6).Render(t.plainText(text.T("payoff.results", "url", out.URLs.Site+"/resultados?run="+out.Demo.RunID)))
	}
	b.WriteString(t.Panel.BorderForeground(t.Brand).Width(w-2).Render(strings.TrimRight(twin, "\n")) + "\n\n")

	siteNote := text.T("payoff.signed_in")
	switch {
	case m.opened:
		siteNote = text.T("payoff.opened")
	case m.browserErr != nil:
		siteNote = text.T("payoff.open_failed")
	}
	rows := [][3]string{
		{text.T("payoff.site"), out.URLs.App, siteNote},
		{text.T("payoff.api"), out.URLs.API + "/v1/version", ""},
		{text.T("payoff.mailpit"), out.URLs.Mailpit, ""},
	}
	for _, r := range rows {
		b.WriteString("  " + t.Dim.Render(pad(r[0], 9)) + t.Accent.Underline(true).Render(r[1]))
		if r[2] != "" {
			b.WriteString("  " + t.Dim.Render(t.plainText(r[2])))
		}
		b.WriteString("\n")
	}
	b.WriteString("\n" + t.Heading.Render(text.T("payoff.next_title")) + "\n")
	for _, c := range NextCommands(out.Mode) {
		b.WriteString("  " + t.Key.Render(pad(c[0], 18)) + t.Dim.Render(c[1]) + "\n")
	}
	b.WriteString("\n")
	pairs := []string{"Enter", text.T("hint.exit")}
	if m.browser {
		pairs = append(pairs, "o", text.T("payoff.open_again"))
	}
	b.WriteString(hints(t, pairs...))
	return b.String()
}
