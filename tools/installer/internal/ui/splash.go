package ui

import (
	"math"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/harmonica"
	"github.com/charmbracelet/lipgloss"

	"github.com/mmtmn/docmatch/tools/installer/internal/text"
)

// FPS of the splash animation.
const FPS = 60

const (
	shimmerStart  = -2 * ShimmerBand
	shimmerTarget = 86.0 // past the logo: the spring decelerates out of view
	shimmerEnd    = 70.0 // the band (leaning 1.5 columns per row) has left the logo
	maxFrames     = 2 * FPS
)

// splashModel is the wordmark with a spring-driven shimmer that sweeps once and stops.
type splashModel struct {
	t         Theme
	spring    harmonica.Spring
	pos, vel  float64
	frames    int
	animating bool
	auto      bool // --yes: continue by itself
}

func newSplash(t Theme, auto bool) splashModel {
	m := splashModel{t: t, spring: harmonica.NewSpring(harmonica.FPS(FPS), 3.2, 1.0), pos: shimmerStart, auto: auto}
	m.animating = t.Animate
	if !m.animating {
		m.pos = 1e6
	}
	return m
}

func frameTick(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(t time.Time) tea.Msg { return frameMsg(t) })
}

func (m *splashModel) init() tea.Cmd {
	switch {
	case m.animating:
		return frameTick(time.Second / FPS)
	case m.auto:
		return frameTick(300 * time.Millisecond)
	}
	return nil
}

func (m *splashModel) ready() bool { return !m.animating }

// finish ends the animation (a key press skips it).
func (m *splashModel) finish() {
	m.animating = false
	m.pos = 1e6
}

// update advances one frame; advance is true when the splash should give way by itself.
func (m *splashModel) update(msg tea.Msg) (tea.Cmd, bool) {
	if _, ok := msg.(frameMsg); !ok {
		return nil, false
	}
	if !m.animating {
		return nil, m.auto
	}
	m.pos, m.vel = m.spring.Update(m.pos, m.vel, shimmerTarget)
	m.frames++
	if m.pos >= shimmerEnd || m.frames >= maxFrames {
		m.finish()
		if m.auto {
			return frameTick(400 * time.Millisecond), false
		}
		return nil, false
	}
	return frameTick(time.Second / FPS), false
}

// reveal is how much of the tagline is typed in (it follows the shimmer).
func (m *splashModel) reveal() float64 {
	if !m.animating {
		return 1
	}
	return math.Max(0, math.Min(1, (m.pos-shimmerStart)/(shimmerEnd-shimmerStart)))
}

func (m *splashModel) view(width, height int) string {
	t := m.t
	var logo string
	if width >= WordmarkWidth+4 {
		logo = t.renderWordmark(m.pos)
	} else {
		logo = t.Title.Render("docmatch")
	}
	tagline := t.plainText(text.T("tagline"))
	runes := []rune(tagline)
	shown := int(math.Round(float64(len(runes)) * m.reveal()))
	line := t.Dim.Render(string(runes[:shown])) + strings.Repeat(" ", len(runes)-shown)
	hint := " "
	if m.ready() && !m.auto {
		hint = t.Key.Render("Enter") + t.Dim.Render(" "+text.T("splash.start")+t.sep()) + t.Key.Render("q") + t.Dim.Render(" "+text.T("splash.quit"))
	}
	block := lipgloss.JoinVertical(lipgloss.Center, logo, "", line, "", hint)
	if width <= 0 || height <= 0 {
		return block
	}
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, block)
}
