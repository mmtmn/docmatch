package main

import (
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"io"
	"math"
	"os"
	"strings"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/vt"
	"golang.org/x/image/font"
	"golang.org/x/image/math/fixed"
)

type renderOpts struct {
	font       string
	fontSize   float64
	lineHeight float64
	fps        float64
	idle       time.Duration
	speed      float64
	hold       time.Duration
	padding    int
	bar        bool
	title      string
	themePath  string
	maxWidth   int
	maxBytes   int64
	colors     int
	cursor     bool
}

func cmdRender(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("render", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var o renderOpts
	fs.StringVar(&o.font, "font", "", "arquivo .ttf/.otf ou família (padrão: "+strings.Join(fontFamilies, " ou ")+", pelo fc-match)")
	fs.Float64Var(&o.fontSize, "font-size", 13, "tamanho da fonte em px")
	fs.Float64Var(&o.lineHeight, "line-height", 1, "altura da linha (múltiplo da altura natural da fonte)")
	fs.Float64Var(&o.fps, "fps", 15, "quadros por segundo no máximo")
	fs.DurationVar(&o.idle, "idle", 1500*time.Millisecond, "pausa máxima entre saídas (0 mantém as pausas)")
	fs.Float64Var(&o.speed, "speed", 1, "velocidade geral (os marcadores speed=N da fita multiplicam)")
	fs.DurationVar(&o.hold, "hold", 3*time.Second, "quanto o último quadro fica parado")
	fs.IntVar(&o.padding, "padding", 14, "margem em px em volta do terminal")
	fs.BoolVar(&o.bar, "bar", true, "barra de janela com título")
	fs.StringVar(&o.title, "title", "", "título da barra (padrão: o do asciicast)")
	fs.StringVar(&o.themePath, "theme", "", "tema JSON (padrão: o embutido, das cores da docmatch)")
	fs.IntVar(&o.maxWidth, "max-width", 800, "falha se o GIF ficar mais largo (px; 0 não confere)")
	fs.Int64Var(&o.maxBytes, "max-bytes", 3<<20, "falha se o GIF ficar maior (bytes; 0 não confere)")
	fs.IntVar(&o.colors, "colors", 255, "cores da paleta (2 a 255; mais uma transparente)")
	fs.BoolVar(&o.cursor, "cursor", true, "desenha o cursor quando o programa o mostra")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 2 {
		return usagef("render: use media render [opções] ENTRADA.cast SAIDA.gif")
	}
	if o.fps <= 0 || o.fps > 50 || o.speed <= 0 || o.colors < 2 || o.colors > 255 || o.fontSize <= 0 {
		return usagef("render: --fps (0-50], --speed > 0, --colors 2-255 e --font-size > 0")
	}
	in, outPath := fs.Arg(0), fs.Arg(1)
	f, err := os.Open(in) //nolint:gosec // the recording the user asked for
	if err != nil {
		return err
	}
	h, events, err := readCast(f)
	_ = f.Close()
	if err != nil {
		return fmt.Errorf("%s: %w", in, err)
	}
	th, err := loadTheme(o.themePath)
	if err != nil {
		return err
	}
	fonts, err := openFonts(o.font, o.fontSize, o.lineHeight)
	if err != nil {
		return err
	}
	if o.title == "" {
		o.title = h.Title
	}
	frames, err := sampleFrames(h, events, th, o)
	if err != nil {
		return err
	}
	r := newRasterizer(h.Width, h.Height, fonts, th, o)
	if o.maxWidth > 0 && r.w > o.maxWidth {
		return fmt.Errorf("o GIF teria %d px de largura (máximo %d): diminua --font-size, --padding ou as colunas", r.w, o.maxWidth)
	}
	stats, err := writeGIF(outPath, frames, r, o.colors)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "%s: %dx%d, %d quadros, %.1f s, %d KB\n", outPath, r.w, r.h, stats.frames, stats.seconds, (stats.bytes+512)/1024)
	if o.maxBytes > 0 && stats.bytes > o.maxBytes {
		return fmt.Errorf("%s tem %d bytes (máximo %d): diminua --fps, --colors ou a duração", outPath, stats.bytes, o.maxBytes)
	}
	return nil
}

// cellSnap is one cell as it will be drawn, colours already resolved.
type cellSnap struct {
	content string
	width   int8
	style   int8 // 0 regular, 1 bold, 2 italic, 3 bold italic
	under   bool
	strike  bool
	fg, bg  color.RGBA
}

type frame struct {
	cells   []cellSnap
	cursor  image.Point // -1,-1 when hidden
	speed   float64     // > 1 while a "Marker speed=N" stretch plays
	seconds float64
}

func (f *frame) same(g *frame) bool {
	if f.cursor != g.cursor || f.speed != g.speed || len(f.cells) != len(g.cells) {
		return false
	}
	for i := range f.cells {
		if f.cells[i] != g.cells[i] {
			return false
		}
	}
	return true
}

// timed is an output event on the GIF's clock.
type timed struct {
	at    float64 // seconds on the GIF's clock
	real  float64 // seconds in the recording
	data  string
	speed float64 // speed in effect from this event on
	mark  string
}

// timeline maps the recording onto the GIF's clock: each gap is divided by the speed in effect
// and capped at the idle limit.
func timeline(events []castEvent, o renderOpts) []timed {
	var out []timed
	speed := 1.0
	at, prev := 0.0, 0.0
	first := true
	for _, e := range events {
		if e.Kind != "o" && e.Kind != "m" {
			continue
		}
		if first {
			prev, first = e.Time, false
		}
		gap := (e.Time - prev) / (o.speed * speed)
		if gap < 0 {
			gap = 0
		}
		if o.idle > 0 && gap > o.idle.Seconds() {
			gap = o.idle.Seconds()
		}
		at += gap
		prev = e.Time
		t := timed{at: at, real: e.Time, speed: speed}
		if e.Kind == "o" {
			t.data = e.Data
		} else {
			t.mark = e.Data
			if s, err := markerSpeed(e.Data); err == nil && s > 0 {
				speed = s
				t.speed = s
			}
		}
		out = append(out, t)
	}
	return out
}

// sampleFrames replays the recording and keeps the screen at each tick of the GIF's clock,
// merging ticks that show the same thing. A tick never cuts a burst of output in half (the
// program writes one screen in several chunks a few milliseconds apart). Markers "start" and
// "end" trim the GIF.
func sampleFrames(h castHeader, events []castEvent, th *theme, o renderOpts) ([]*frame, error) {
	tl := timeline(events, o)
	if len(tl) == 0 {
		return nil, fmt.Errorf("o asciicast não tem saída")
	}
	emu := vt.NewEmulator(h.Width, h.Height)
	go func() { _, _ = io.Copy(io.Discard, emu) }() // replies to the program's queries
	defer closeReplies(emu)

	start, end := -1.0, tl[len(tl)-1].at
	for _, t := range tl {
		if t.mark == "start" && start < 0 {
			start = t.at
		}
		if t.mark == "end" {
			end = t.at
			break
		}
	}
	if start < 0 {
		for _, t := range tl {
			if t.data != "" {
				start = t.at
				break
			}
		}
	}
	const burst = 0.004 // seconds between chunks of one screen
	step := 1 / o.fps
	var frames []*frame
	speed := 1.0
	i := 0
	for tick := start; tick <= end+step/2; tick += step {
		// Everything up to the tick, then the rest of the burst the last event belongs to (at
		// most 50 ms more of the recording, for programs that never pause).
		limit := -1.0
		for i < len(tl) {
			if tl[i].at > tick {
				if i == 0 || tl[i].real-tl[i-1].real >= burst {
					break
				}
				if limit < 0 {
					limit = tl[i-1].real + 0.05
				}
				if tl[i].real > limit {
					break
				}
			}
			if tl[i].data != "" {
				_, _ = emu.WriteString(tl[i].data)
			}
			speed = tl[i].speed
			i++
		}
		fr := snapshot(emu, th, o.cursor)
		fr.speed = speed
		if fr.speed <= 1.01 {
			fr.speed = 1
		}
		if n := len(frames); n > 0 && frames[n-1].same(fr) {
			frames[n-1].seconds += step
			continue
		}
		fr.seconds = step
		frames = append(frames, fr)
	}
	if len(frames) == 0 {
		return nil, fmt.Errorf("nenhum quadro entre start e end")
	}
	frames[len(frames)-1].seconds += o.hold.Seconds()
	return frames, nil
}

func snapshot(emu *vt.Emulator, th *theme, showCursor bool) *frame {
	w, h := emu.Width(), emu.Height()
	fr := &frame{cells: make([]cellSnap, w*h), cursor: image.Pt(-1, -1)}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			fr.cells[y*w+x] = snapCell(emu.CellAt(x, y), th)
		}
	}
	if showCursor && !emu.CursorHidden() {
		p := emu.CursorPosition()
		if p.X >= 0 && p.X < w && p.Y >= 0 && p.Y < h {
			fr.cursor = image.Pt(p.X, p.Y)
		}
	}
	return fr
}

func snapCell(c *uv.Cell, th *theme) cellSnap {
	if c == nil {
		return cellSnap{content: " ", width: 1, fg: th.fg, bg: th.bg}
	}
	s := c.Style
	fg := th.resolve(s.Fg, th.fg)
	bg := th.resolve(s.Bg, th.bg)
	if s.Attrs&uv.AttrReverse != 0 {
		fg, bg = bg, fg
	}
	if s.Attrs&uv.AttrFaint != 0 {
		fg = blend(fg, bg, 0.55)
	}
	content := c.Content
	if content == "" || s.Attrs&uv.AttrConceal != 0 {
		content = " "
	}
	style := 0
	if s.Attrs&uv.AttrBold != 0 {
		style |= 1
	}
	if s.Attrs&uv.AttrItalic != 0 {
		style |= 2
	}
	return cellSnap{
		content: content, width: int8(c.Width), style: int8(style), fg: fg, bg: bg,
		under: s.Underline != uv.UnderlineNone, strike: s.Attrs&uv.AttrStrikethrough != 0,
	}
}

// rasterizer draws frames: an optional window bar, the padding and the cells.
type rasterizer struct {
	cols, rows int
	fonts      *fontSet
	th         *theme
	o          renderOpts
	barH       int
	w, h       int
	ox, oy     int // where the cells start
}

func newRasterizer(cols, rows int, fonts *fontSet, th *theme, o renderOpts) *rasterizer {
	r := &rasterizer{cols: cols, rows: rows, fonts: fonts, th: th, o: o}
	if o.bar {
		r.barH = fonts.cellH + 16
	}
	r.w = cols*fonts.cellW + 2*o.padding
	r.h = r.barH + rows*fonts.cellH + 2*o.padding
	r.ox, r.oy = o.padding, r.barH+o.padding
	return r
}

func (r *rasterizer) draw(fr *frame) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, r.w, r.h))
	draw.Draw(img, img.Bounds(), image.NewUniform(r.th.bg), image.Point{}, draw.Src)
	if r.o.bar {
		r.drawBar(img, fr.speed)
	}
	cw, ch := r.fonts.cellW, r.fonts.cellH
	for y := 0; y < r.rows; y++ {
		for x := 0; x < r.cols; x++ {
			c := fr.cells[y*r.cols+x]
			if c.width == 0 {
				continue // drawn by the wide character before it
			}
			width := int(c.width)
			if x+width > r.cols {
				width = r.cols - x
			}
			fg, bg := c.fg, c.bg
			if fr.cursor == image.Pt(x, y) {
				fg, bg = r.th.bg, r.th.cursor
			}
			rect := image.Rect(r.ox+x*cw, r.oy+y*ch, r.ox+(x+width)*cw, r.oy+(y+1)*ch)
			if bg != r.th.bg {
				draw.Draw(img, rect, image.NewUniform(bg), image.Point{}, draw.Src)
			}
			if c.content != " " {
				if m := r.fonts.mask(c.content, int(c.style), width); m != nil {
					draw.DrawMask(img, rect, image.NewUniform(fg), image.Point{}, m, image.Point{}, draw.Over)
				}
			}
			if c.under {
				y0 := rect.Min.Y + r.fonts.baseline + 2
				draw.Draw(img, image.Rect(rect.Min.X, y0, rect.Max.X, y0+1), image.NewUniform(fg), image.Point{}, draw.Src)
			}
			if c.strike {
				y0 := rect.Min.Y + ch/2
				draw.Draw(img, image.Rect(rect.Min.X, y0, rect.Max.X, y0+1), image.NewUniform(fg), image.Point{}, draw.Src)
			}
		}
	}
	return img
}

// drawBar draws the window bar: three dots, the title in the middle and, while a sped-up
// stretch plays, how much faster it is.
func (r *rasterizer) drawBar(img *image.RGBA, speed float64) {
	draw.Draw(img, image.Rect(0, 0, r.w, r.barH), image.NewUniform(r.th.bar), image.Point{}, draw.Src)
	cy := float64(r.barH) / 2
	for i, c := range r.th.dots {
		disc(img, float64(r.o.padding)+6+float64(i)*20, cy, 6, c)
	}
	face := r.fonts.faces[0].face
	if r.o.title != "" {
		r.text(img, face, r.o.title, -1, r.th.barTitle)
	}
	if speed > 1 {
		r.text(img, face, fmt.Sprintf("acelerado %s×", trimFloat(speed)), r.w-r.o.padding, r.th.barTitle)
	}
}

// text draws s on the bar, centred when right < 0, else ending at x = right.
func (r *rasterizer) text(img *image.RGBA, face font.Face, s string, right int, c color.RGBA) {
	d := font.Drawer{Dst: img, Src: image.NewUniform(c), Face: face}
	adv := d.MeasureString(s)
	x := fixed.I(right) - adv
	if right < 0 {
		x = (fixed.I(r.w) - adv) / 2
	}
	m := face.Metrics()
	y := (fixed.I(r.barH) + m.Ascent - m.Descent) / 2
	d.Dot = fixed.Point26_6{X: x, Y: y}
	d.DrawString(s)
}

func trimFloat(f float64) string {
	if f == math.Trunc(f) {
		return fmt.Sprintf("%d", int(f))
	}
	return strings.Replace(fmt.Sprintf("%.1f", f), ".", ",", 1)
}

// disc draws an anti-aliased filled circle.
func disc(img *image.RGBA, cx, cy, rad float64, c color.RGBA) {
	b := img.Bounds()
	for y := int(cy - rad - 1); y <= int(cy+rad+1); y++ {
		for x := int(cx - rad - 1); x <= int(cx+rad+1); x++ {
			if !image.Pt(x, y).In(b) {
				continue
			}
			d := math.Hypot(float64(x)+0.5-cx, float64(y)+0.5-cy)
			a := math.Max(0, math.Min(1, rad+0.5-d))
			if a <= 0 {
				continue
			}
			under := img.RGBAAt(x, y)
			img.SetRGBA(x, y, blend(c, under, a))
		}
	}
}

func alpha(a uint8) color.Alpha { return color.Alpha{A: a} }

// closeReplies ends the emulator's reply stream (whoever copies it sees EOF) without Close,
// which would race with that reader.
func closeReplies(emu *vt.Emulator) {
	if pw, ok := emu.InputPipe().(*io.PipeWriter); ok {
		_ = pw.Close()
	}
}
