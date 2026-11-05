package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"image/color"
	"os"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

//go:embed theme.json
var defaultTheme []byte

type themeFile struct {
	Background string     `json:"background"`
	Foreground string     `json:"foreground"`
	Cursor     string     `json:"cursor"`
	Bar        string     `json:"bar"`
	BarTitle   string     `json:"bar_title"`
	Dots       [3]string  `json:"dots"`
	ANSI       [16]string `json:"ansi"`
}

// theme holds the colours of the recorded terminal and of the window around it.
type theme struct {
	bg, fg, cursor, bar, barTitle color.RGBA
	dots                          [3]color.RGBA
	ansi                          [16]color.RGBA
}

// loadTheme reads a theme file (the embedded one when path is empty).
func loadTheme(path string) (*theme, error) {
	data := defaultTheme
	if path != "" {
		var err error
		if data, err = os.ReadFile(path); err != nil { //nolint:gosec // the theme the user asked for
			return nil, err
		}
	}
	var tf themeFile
	if err := json.Unmarshal(data, &tf); err != nil {
		return nil, fmt.Errorf("tema: %w", err)
	}
	t := &theme{}
	fields := []struct {
		name string
		hex  string
		dst  *color.RGBA
	}{
		{"background", tf.Background, &t.bg}, {"foreground", tf.Foreground, &t.fg},
		{"cursor", tf.Cursor, &t.cursor}, {"bar", tf.Bar, &t.bar}, {"bar_title", tf.BarTitle, &t.barTitle},
	}
	for i := range tf.Dots {
		fields = append(fields, struct {
			name string
			hex  string
			dst  *color.RGBA
		}{fmt.Sprintf("dots[%d]", i), tf.Dots[i], &t.dots[i]})
	}
	for i := range tf.ANSI {
		fields = append(fields, struct {
			name string
			hex  string
			dst  *color.RGBA
		}{fmt.Sprintf("ansi[%d]", i), tf.ANSI[i], &t.ansi[i]})
	}
	for _, f := range fields {
		c, err := parseHex(f.hex)
		if err != nil {
			return nil, fmt.Errorf("tema: %s: %w", f.name, err)
		}
		*f.dst = c
	}
	return t, nil
}

func parseHex(s string) (color.RGBA, error) {
	h := strings.TrimPrefix(s, "#")
	if len(h) != 6 {
		return color.RGBA{}, fmt.Errorf("cor %q: use #RRGGBB", s)
	}
	v, err := strconv.ParseUint(h, 16, 32)
	if err != nil {
		return color.RGBA{}, fmt.Errorf("cor %q: %w", s, err)
	}
	return color.RGBA{R: uint8(v >> 16), G: uint8(v >> 8), B: uint8(v), A: 255}, nil
}

// resolve turns a cell colour into RGB: nil is the default, the 16 basic colours come from the
// theme, the rest of the 256 from the xterm palette, and true colour as it is.
func (t *theme) resolve(c color.Color, def color.RGBA) color.RGBA {
	switch v := c.(type) {
	case nil:
		return def
	case ansi.BasicColor:
		return t.ansi[int(v)&15]
	case ansi.IndexedColor:
		if v < 16 {
			return t.ansi[v]
		}
	}
	r, g, b, a := c.RGBA()
	if a == 0 {
		return def
	}
	return color.RGBA{R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(b >> 8), A: 255}
}

func blend(fg, bg color.RGBA, a float64) color.RGBA {
	mix := func(x, y uint8) uint8 { return uint8(float64(x)*a + float64(y)*(1-a) + 0.5) }
	return color.RGBA{R: mix(fg.R, bg.R), G: mix(fg.G, bg.G), B: mix(fg.B, bg.B), A: 255}
}
