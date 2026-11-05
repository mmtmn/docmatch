// Package ui is the installer's full-screen interface (Bubble Tea): splash, mode, preflight,
// choices, progress and payoff, plus the plain line output used without a terminal, in CI, with
// NO_COLOR or --plain. Both render the same flow events.
package ui

import (
	"encoding/json"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/lipgloss"
	"github.com/lucasb-eyer/go-colorful"

	"github.com/mmtmn/docmatch/tools/installer/internal/assets"
)

// tokens is the part of packages/design-tokens/tokens.json the terminal uses.
type tokens struct {
	Color map[string]struct {
		Text   map[string]string `json:"text"`
		Border map[string]string `json:"border"`
		Brand  map[string]string `json:"brand"`
		Status map[string]string `json:"status"`
		BG     map[string]string `json:"bg"`
	} `json:"color"`
}

func loadTokens() tokens {
	var t tokens
	_ = json.Unmarshal(assets.Tokens, &t)
	return t
}

// adaptive picks the light or dark token of the same name.
func adaptive(t tokens, group, name string) lipgloss.AdaptiveColor {
	get := func(mode string) string {
		c := t.Color[mode]
		var m map[string]string
		switch group {
		case "text":
			m = c.Text
		case "border":
			m = c.Border
		case "brand":
			m = c.Brand
		case "status":
			m = c.Status
		case "bg":
			m = c.BG
		}
		return m[name]
	}
	return lipgloss.AdaptiveColor{Light: get("light"), Dark: get("dark")}
}

func hex(s, fallback string) colorful.Color {
	c, err := colorful.Hex(s)
	if err != nil {
		c, _ = colorful.Hex(fallback)
	}
	return c
}

// Glyphs are the symbols of the interface, Unicode or ASCII.
type Glyphs struct {
	OK, Warn, Fail, Skip, Pending, Bullet, Pointer, Sep, BarFull, BarEmpty string
}

var unicodeGlyphs = Glyphs{OK: "✓", Warn: "!", Fail: "✗", Skip: "–", Pending: "·", Bullet: "•", Pointer: "›", Sep: "·", BarFull: "█", BarEmpty: "░"}
var asciiGlyphs = Glyphs{OK: "+", Warn: "!", Fail: "x", Skip: "-", Pending: ".", Bullet: "*", Pointer: ">", Sep: "-", BarFull: "#", BarEmpty: "-"}

// Theme is everything a screen needs to draw itself.
type Theme struct {
	ASCII   bool
	Animate bool
	Dark    bool
	G       Glyphs

	Brand, Success, Warning, Danger, Text, Muted, Subtle, Border lipgloss.AdaptiveColor

	// Wordmark gradient (left to right) and the shimmer's peak color.
	Gradient  []colorful.Color
	Highlight colorful.Color

	Title, Heading, Body, Dim, Accent, OKs, WarnS, FailS, Key lipgloss.Style
	Card, CardActive, Panel                                   lipgloss.Style
	Spinner                                                   spinner.Spinner
}

// NewTheme builds the theme from the design tokens.
func NewTheme(ascii, animate, dark bool) Theme {
	tk := loadTokens()
	t := Theme{ASCII: ascii, Animate: animate, Dark: dark, G: unicodeGlyphs}
	if ascii {
		t.G = asciiGlyphs
	}
	t.Brand = adaptive(tk, "brand", "primary")
	t.Success = adaptive(tk, "status", "success")
	t.Warning = adaptive(tk, "status", "warning")
	t.Danger = adaptive(tk, "status", "danger")
	t.Text = adaptive(tk, "text", "primary")
	t.Muted = adaptive(tk, "text", "muted")
	t.Subtle = adaptive(tk, "text", "secondary")
	t.Border = adaptive(tk, "border", "strong")

	light, darkC := tk.Color["light"], tk.Color["dark"]
	if dark {
		// Deep brand blue to the light primary: readable on dark backgrounds.
		t.Gradient = []colorful.Color{
			hex(light.Brand["primary"], "#1D4ED8"),
			hex(darkC.Brand["primary"], "#60A5FA"),
			hex(darkC.Brand["primary_hover"], "#93C5FD"),
		}
		t.Highlight = hex(darkC.Brand["on_subtle"], "#DBEAFE")
	} else {
		t.Gradient = []colorful.Color{
			hex(light.Brand["on_subtle"], "#1E3A8A"),
			hex(light.Brand["primary_hover"], "#1E40AF"),
			hex(light.Brand["primary"], "#1D4ED8"),
		}
		t.Highlight = hex(darkC.Brand["primary"], "#60A5FA")
	}

	t.Title = lipgloss.NewStyle().Foreground(t.Brand).Bold(true)
	t.Heading = lipgloss.NewStyle().Foreground(t.Text).Bold(true)
	t.Body = lipgloss.NewStyle().Foreground(t.Text)
	t.Dim = lipgloss.NewStyle().Foreground(t.Muted)
	t.Accent = lipgloss.NewStyle().Foreground(t.Brand)
	t.OKs = lipgloss.NewStyle().Foreground(t.Success)
	t.WarnS = lipgloss.NewStyle().Foreground(t.Warning)
	t.FailS = lipgloss.NewStyle().Foreground(t.Danger)
	t.Key = lipgloss.NewStyle().Foreground(t.Brand).Bold(true)

	border := lipgloss.RoundedBorder()
	if ascii {
		border = lipgloss.ASCIIBorder()
	}
	t.Card = lipgloss.NewStyle().Border(border).BorderForeground(t.Border).Padding(0, 2)
	t.CardActive = t.Card.BorderForeground(t.Brand)
	t.Panel = lipgloss.NewStyle().Border(border).BorderForeground(t.Border).Padding(0, 1)

	t.Spinner = spinner.MiniDot
	if ascii {
		t.Spinner = spinner.Line
	}
	return t
}

// Glyph renders a status symbol in its color.
func (t Theme) glyph(kind string) string {
	switch kind {
	case "ok":
		return t.OKs.Render(t.G.OK)
	case "warn":
		return t.WarnS.Render(t.G.Warn)
	case "fail":
		return t.FailS.Render(t.G.Fail)
	case "skip":
		return t.Dim.Render(t.G.Skip)
	default:
		return t.Dim.Render(t.G.Pending)
	}
}

// sep is the middle dot between items ("Enter continuar · q sair"), "-" in ASCII.
func (t Theme) sep() string { return " " + t.G.Sep + " " }

// plainText swaps the Unicode punctuation of a pt-BR string for ASCII when needed.
func (t Theme) plainText(s string) string {
	if !t.ASCII {
		return s
	}
	return asciiOnly(s)
}
