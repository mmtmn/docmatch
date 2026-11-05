package main

import (
	"fmt"
	"image"
	"image/draw"
	"math"
	"os"
	"os/exec"
	"strings"
	"unicode/utf8"

	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
)

// Monospace families tried in order when --font is not given (found with fc-match; the fonts
// themselves are not in the repository).
var fontFamilies = []string{"DejaVu Sans Mono", "Noto Sans Mono"}

type loadedFace struct {
	path string
	sf   *sfnt.Font
	face font.Face
	buf  sfnt.Buffer
}

func (l *loadedFace) has(r rune) bool {
	i, err := l.sf.GlyphIndex(&l.buf, r)
	return err == nil && i != 0
}

// fontSet draws terminal cells: the regular, bold, italic and bold-italic faces of one family,
// fallbacks found per character, and the cell metrics.
type fontSet struct {
	size                   float64
	faces                  [4]*loadedFace // regular, bold, italic, bold italic (nil: none)
	byFile                 map[string]*loadedFace
	byRune                 map[rune]*loadedFace
	cellW, cellH, baseline int
	masks                  map[glyphKey]*image.Alpha
}

type glyphKey struct {
	content string
	style   int // 0 regular, 1 bold, 2 italic, 3 bold italic
	width   int
}

// openFonts loads the family of spec: a font file, a family name for fc-match, or empty for the
// first family of fontFamilies that is installed.
func openFonts(spec string, size, lineHeight float64) (*fontSet, error) {
	fs := &fontSet{size: size, byFile: map[string]*loadedFace{}, byRune: map[rune]*loadedFace{}, masks: map[glyphKey]*image.Alpha{}}
	var paths [4]string
	switch {
	case spec != "" && fileExists(spec):
		paths[0] = spec
	case spec != "":
		p, ok := fcFamily(spec, "")
		if !ok {
			return nil, fmt.Errorf("fonte %q não encontrada (fc-match)", spec)
		}
		paths[0] = p
	default:
		for _, fam := range fontFamilies {
			if p, ok := fcFamily(fam, ""); ok {
				spec, paths[0] = fam, p
				break
			}
		}
		if paths[0] == "" {
			return nil, fmt.Errorf("nenhuma fonte monoespaçada encontrada (%s): instale uma ou use --font ARQUIVO.ttf", strings.Join(fontFamilies, ", "))
		}
	}
	if !fileExists(spec) {
		for i, style := range []string{"", "bold", "oblique", "bold:oblique"} {
			if i == 0 {
				continue
			}
			if p, ok := fcFamily(spec, style); ok && p != paths[0] {
				paths[i] = p
			}
		}
	}
	for i, p := range paths {
		if p == "" {
			continue
		}
		lf, err := fs.load(p)
		if err != nil {
			if i == 0 {
				return nil, err
			}
			continue
		}
		fs.faces[i] = lf
	}
	m := fs.faces[0].face.Metrics()
	adv, ok := fs.faces[0].face.GlyphAdvance('0')
	if !ok {
		return nil, fmt.Errorf("a fonte %s não tem o algarismo 0", paths[0])
	}
	fs.cellW = int(math.Round(fixedToFloat(adv)))
	natural := fixedToFloat(m.Ascent + m.Descent)
	fs.cellH = int(math.Ceil(natural * lineHeight))
	fs.baseline = int(math.Round(fixedToFloat(m.Ascent) + (float64(fs.cellH)-natural)/2))
	if fs.cellW < 1 || fs.cellH < 1 {
		return nil, fmt.Errorf("métricas inválidas na fonte %s", paths[0])
	}
	return fs, nil
}

func fixedToFloat(v fixed.Int26_6) float64 { return float64(v) / 64 }

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// fcFamily asks fontconfig for family (with an optional style such as "bold") and returns the
// file only when fontconfig really has that family (it otherwise answers with any font).
func fcFamily(family, style string) (string, bool) {
	pattern := family
	if style != "" {
		pattern += ":" + style
	}
	out, err := exec.Command("fc-match", "-f", "%{family}\t%{style}\t%{file}", pattern).Output() //nolint:gosec // fixed program
	if err != nil {
		return "", false
	}
	parts := strings.SplitN(string(out), "\t", 3)
	if len(parts) != 3 || !strings.Contains(strings.ToLower(parts[0]), strings.ToLower(family)) {
		return "", false
	}
	st := strings.ToLower(parts[1])
	if strings.Contains(style, "bold") && !strings.Contains(st, "bold") {
		return "", false
	}
	if strings.Contains(style, "oblique") && !strings.Contains(st, "oblique") && !strings.Contains(st, "italic") {
		return "", false
	}
	return strings.TrimSpace(parts[2]), true
}

// fcFallback asks fontconfig for any font with r.
func fcFallback(r rune) string {
	out, err := exec.Command("fc-match", "-f", "%{file}", fmt.Sprintf("monospace:charset=%x", r)).Output() //nolint:gosec // fixed program
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func (fs *fontSet) load(path string) (*loadedFace, error) {
	if lf, ok := fs.byFile[path]; ok {
		return lf, nil
	}
	data, err := os.ReadFile(path) //nolint:gosec // a system font
	if err != nil {
		return nil, err
	}
	var f *sfnt.Font
	lower := strings.ToLower(path)
	if strings.HasSuffix(lower, ".ttc") || strings.HasSuffix(lower, ".otc") {
		c, err := opentype.ParseCollection(data)
		if err != nil {
			return nil, fmt.Errorf("fonte %s: %w", path, err)
		}
		if f, err = c.Font(0); err != nil {
			return nil, fmt.Errorf("fonte %s: %w", path, err)
		}
	} else if f, err = opentype.Parse(data); err != nil {
		return nil, fmt.Errorf("fonte %s: %w", path, err)
	}
	face, err := opentype.NewFace(f, &opentype.FaceOptions{Size: fs.size, DPI: 72, Hinting: font.HintingFull})
	if err != nil {
		return nil, fmt.Errorf("fonte %s: %w", path, err)
	}
	lf := &loadedFace{path: path, sf: f, face: face}
	fs.byFile[path] = lf
	return lf, nil
}

// pick returns the face that draws r in style (or nil when no installed font has it).
func (fs *fontSet) pick(r rune, style int) *loadedFace {
	order := []int{style, 0}
	if style == 3 {
		order = []int{3, 1, 2, 0}
	}
	for _, i := range order {
		if lf := fs.faces[i]; lf != nil && lf.has(r) {
			return lf
		}
	}
	if lf, ok := fs.byRune[r]; ok {
		return lf
	}
	var lf *loadedFace
	if p := fcFallback(r); p != "" {
		if l, err := fs.load(p); err == nil && l.has(r) {
			lf = l
		}
	}
	fs.byRune[r] = lf
	return lf
}

// mask is the coverage of one cell's content (width cells wide), cached.
func (fs *fontSet) mask(content string, style, width int) *image.Alpha {
	key := glyphKey{content, style, width}
	if m, ok := fs.masks[key]; ok {
		return m
	}
	w := fs.cellW * width
	m := image.NewAlpha(image.Rect(0, 0, w, fs.cellH))
	r, _ := utf8.DecodeRuneInString(content)
	if !drawBlock(m, r) && !drawBraille(m, r) {
		if lf := fs.pick(r, style); lf != nil {
			d := font.Drawer{Dst: m, Src: image.Opaque, Face: lf.face}
			adv, _ := lf.face.GlyphAdvance(r)
			x := (fixed.I(w) - adv) / 2
			if x < 0 {
				x = 0
			}
			d.Dot = fixed.Point26_6{X: x, Y: fixed.I(fs.baseline)}
			d.DrawString(string(r))
			if style&1 == 1 && lf != fs.faces[1] && lf != fs.faces[3] {
				// No bold face for this character: thicken it by one pixel.
				d.Dot = fixed.Point26_6{X: x + fixed.I(1), Y: fixed.I(fs.baseline)}
				d.DrawString(string(r))
			}
		} else {
			m = nil
		}
	}
	fs.masks[key] = m
	return m
}

// drawBlock draws the block elements (U+2580–U+259F) as exact rectangles, so bars and blocks
// tile without the gaps a font leaves between cells. Shades become uniform coverage.
func drawBlock(m *image.Alpha, r rune) bool {
	if r < 0x2580 || r > 0x259F {
		return false
	}
	b := m.Bounds()
	w, h := b.Dx(), b.Dy()
	fill := func(x0, y0, x1, y1 float64, a uint8) {
		rect := image.Rect(int(math.Round(x0*float64(w))), int(math.Round(y0*float64(h))),
			int(math.Round(x1*float64(w))), int(math.Round(y1*float64(h))))
		draw.Draw(m, rect, image.NewUniform(alpha(a)), image.Point{}, draw.Src)
	}
	switch {
	case r == 0x2580:
		fill(0, 0, 1, 0.5, 255)
	case r >= 0x2581 && r <= 0x2588: // lower one eighth .. full block
		fill(0, 1-float64(r-0x2580)/8, 1, 1, 255)
	case r >= 0x2589 && r <= 0x258F: // left seven eighths .. left one eighth
		fill(0, 0, float64(0x2590-r)/8, 1, 255)
	case r == 0x2590:
		fill(0.5, 0, 1, 1, 255)
	case r >= 0x2591 && r <= 0x2593:
		fill(0, 0, 1, 1, uint8(64*(r-0x2590)))
	case r == 0x2594:
		fill(0, 0, 1, 0.125, 255)
	case r == 0x2595:
		fill(0.875, 0, 1, 1, 255)
	default: // quadrants
		quads := map[rune]string{
			0x2596: "LL", 0x2597: "LR", 0x2598: "UL", 0x2599: "UL LL LR", 0x259A: "UL LR",
			0x259B: "UL UR LL", 0x259C: "UL UR LR", 0x259D: "UR", 0x259E: "UR LL", 0x259F: "UR LL LR",
		}
		for _, q := range strings.Fields(quads[r]) {
			x0, y0 := 0.0, 0.0
			if q[1] == 'R' {
				x0 = 0.5
			}
			if q[0] == 'L' {
				y0 = 0.5
			}
			fill(x0, y0, x0+0.5, y0+0.5, 255)
		}
	}
	return true
}

// drawBraille draws the braille patterns (U+2800–U+28FF, the spinners) as round dots.
func drawBraille(m *image.Alpha, r rune) bool {
	if r < 0x2800 || r > 0x28FF {
		return false
	}
	bits := int(r - 0x2800)
	b := m.Bounds()
	w, h := float64(b.Dx()), float64(b.Dy())
	// Dot numbers 1-8 as (column, row) on a 2x4 grid.
	pos := [8][2]int{{0, 0}, {0, 1}, {0, 2}, {1, 0}, {1, 1}, {1, 2}, {0, 3}, {1, 3}}
	rad := math.Min(w/2, h/4) * 0.32
	for i, p := range pos {
		if bits&(1<<i) == 0 {
			continue
		}
		cx := w * (0.3 + 0.4*float64(p[0]))
		cy := h * (0.17 + 0.22*float64(p[1]))
		for y := int(cy - rad - 1); y <= int(cy+rad+1); y++ {
			for x := int(cx - rad - 1); x <= int(cx+rad+1); x++ {
				d := math.Hypot(float64(x)+0.5-cx, float64(y)+0.5-cy)
				a := math.Max(0, math.Min(1, rad+0.5-d))
				if a > 0 && image.Pt(x, y).In(b) {
					if cur := m.AlphaAt(x, y).A; uint8(a*255) > cur {
						m.SetAlpha(x, y, alpha(uint8(a*255)))
					}
				}
			}
		}
	}
	return true
}
