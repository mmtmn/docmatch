package ui

import (
	"math"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/lucasb-eyer/go-colorful"
)

// Wordmark is the splash logo (plain ASCII, so it draws on every console).
var Wordmark = []string{
	`     _                                 _          _`,
	`  __| |  ___    ___  _ __ ___    __ _ | |_   ___ | |__`,
	` / _` + "`" + ` | / _ \  / __|| '_ ` + "`" + ` _ \  / _` + "`" + ` || __| / __|| '_ \`,
	`| (_| || (_) || (__ | | | | | || (_| || |_ | (__ | | | |`,
	` \__,_| \___/  \___||_| |_| |_| \__,_| \__| \___||_| |_|`,
}

// WordmarkWidth is the widest line of the wordmark.
var WordmarkWidth = func() int {
	w := 0
	for _, l := range Wordmark {
		if len(l) > w {
			w = len(l)
		}
	}
	return w
}()

// gradientAt interpolates the stops at f (0..1).
func gradientAt(stops []colorful.Color, f float64) colorful.Color {
	if len(stops) == 0 {
		return colorful.Color{}
	}
	if len(stops) == 1 || f <= 0 {
		return stops[0]
	}
	if f >= 1 {
		return stops[len(stops)-1]
	}
	seg := f * float64(len(stops)-1)
	i := int(seg)
	return stops[i].BlendLuv(stops[i+1], seg-float64(i)).Clamped()
}

// ShimmerBand is the half-width of the shimmer's highlight, in columns.
const ShimmerBand = 7.0

// renderWordmark draws the wordmark in the brand gradient. pos is the shimmer's center column
// (anything far outside the logo means no shimmer); the band leans with the lines, like light
// sweeping across embossed letters.
func (t Theme) renderWordmark(pos float64) string {
	var b strings.Builder
	width := float64(WordmarkWidth - 1)
	for row, line := range Wordmark {
		var run strings.Builder
		runHex := ""
		flush := func() {
			if run.Len() == 0 {
				return
			}
			if runHex == "" {
				b.WriteString(run.String())
			} else {
				b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(runHex)).Bold(true).Render(run.String()))
			}
			run.Reset()
		}
		for col, ch := range line {
			if ch == ' ' {
				if runHex != "" {
					flush()
					runHex = ""
				}
				run.WriteRune(ch)
				continue
			}
			c := gradientAt(t.Gradient, float64(col)/width)
			d := math.Abs(float64(col) - pos + float64(row)*1.5)
			if d < ShimmerBand {
				k := 1 - d/ShimmerBand
				c = c.BlendLuv(t.Highlight, k*k).Clamped()
			}
			h := c.Hex()
			if h != runHex {
				flush()
				runHex = h
			}
			run.WriteRune(ch)
		}
		flush()
		// Same width on every line: centering a block aligns it line by line.
		b.WriteString(strings.Repeat(" ", WordmarkWidth-len(line)))
		if row < len(Wordmark)-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// asciiOnly replaces the typographic characters of the pt-BR strings that old consoles cannot
// draw. Accented letters stay: every Windows code page in use for Portuguese has them.
func asciiOnly(s string) string {
	return strings.NewReplacer(
		"·", "-", "—", "-", "–", "-", "…", "...", "→", "->", "←", "<-", "↑", "^", "↓", "v",
		"“", `"`, "”", `"`, "‘", "'", "’", "'", "•", "*", "✓", "+", "✗", "x", "›", ">",
	).Replace(s)
}
