package main

import (
	"bufio"
	"image"
	"image/color"
	"image/gif"
	"math"
	"os"
	"path/filepath"
	"sort"
)

type gifStats struct {
	frames  int
	seconds float64
	bytes   int64
}

// writeGIF encodes the frames with one palette for the whole GIF: the exact colours of the cells
// first (so text and backgrounds never shift), then a median cut of the anti-aliasing shades.
// Every frame after the first only carries the rectangle that changed, with the unchanged pixels
// in it transparent, which keeps a terminal GIF small.
func writeGIF(path string, frames []*frame, r *rasterizer, colors int) (gifStats, error) {
	pal := buildPalette(frames, r, colors)
	transparent := uint8(len(pal))
	full := append(color.Palette{}, pal...)
	full = append(full, color.RGBA{})
	q := newQuantizer(pal)

	g := &gif.GIF{Config: image.Config{ColorModel: full, Width: r.w, Height: r.h}}
	var prev []uint8
	var st gifStats
	clock, emitted := 0.0, 0
	for _, fr := range frames {
		img := r.draw(fr)
		cur := make([]uint8, r.w*r.h)
		for i := range cur {
			o := i * 4
			cur[i] = q.index(img.Pix[o], img.Pix[o+1], img.Pix[o+2])
		}
		start := clock
		clock += fr.seconds
		if prev != nil {
			box := diffBox(prev, cur, r.w, r.h)
			if box.Empty() {
				continue // the shades quantised to the same picture: the previous frame lasts longer
			}
			pm := image.NewPaletted(box, full)
			for y := box.Min.Y; y < box.Max.Y; y++ {
				for x := box.Min.X; x < box.Max.X; x++ {
					i := y*r.w + x
					if cur[i] == prev[i] {
						pm.Pix[pm.PixOffset(x, y)] = transparent
					} else {
						pm.Pix[pm.PixOffset(x, y)] = cur[i]
					}
				}
			}
			setDelay(g, start, &emitted) // the previous frame lasts until this one
			g.Image = append(g.Image, pm)
		} else {
			pm := image.NewPaletted(image.Rect(0, 0, r.w, r.h), full)
			copy(pm.Pix, cur)
			g.Image = append(g.Image, pm)
		}
		g.Delay = append(g.Delay, 0)
		g.Disposal = append(g.Disposal, gif.DisposalNone)
		prev = cur
	}
	setDelay(g, clock, &emitted)
	st.frames, st.seconds = len(g.Image), clock

	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return st, err
	}
	f, err := os.Create(path)
	if err != nil {
		return st, err
	}
	w := bufio.NewWriter(f)
	if err := gif.EncodeAll(w, g); err != nil {
		_ = f.Close()
		return st, err
	}
	if err := w.Flush(); err != nil {
		_ = f.Close()
		return st, err
	}
	if err := f.Close(); err != nil {
		return st, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return st, err
	}
	st.bytes = info.Size()
	return st, nil
}

// setDelay closes the last frame: its delay runs until clock, in hundredths of a second, carrying
// the rounding so the GIF's total length stays right. Browsers stretch delays under 2 to 10.
func setDelay(g *gif.GIF, clock float64, emitted *int) {
	n := len(g.Delay)
	if n == 0 {
		return
	}
	total := int(math.Round(clock * 100))
	d := total - *emitted
	if d < 2 {
		d = 2
	}
	g.Delay[n-1] = d
	*emitted += d
}

func diffBox(a, b []uint8, w, h int) image.Rectangle {
	minX, minY, maxX, maxY := w, h, -1, -1
	for y := 0; y < h; y++ {
		row := y * w
		for x := 0; x < w; x++ {
			if a[row+x] != b[row+x] {
				if x < minX {
					minX = x
				}
				if x > maxX {
					maxX = x
				}
				if y < minY {
					minY = y
				}
				maxY = y
			}
		}
	}
	if maxX < 0 {
		return image.Rectangle{}
	}
	return image.Rect(minX, minY, maxX+1, maxY+1)
}

// buildPalette counts the colours of the frames (the first one whole, then only what changed)
// and picks at most n: the colours the cells ask for, by frequency, then a median cut of the
// rest.
func buildPalette(frames []*frame, r *rasterizer, n int) color.Palette {
	hist := map[uint32]int{}
	exact := map[uint32]bool{pack(r.th.bg): true, pack(r.th.bar): true, pack(r.th.barTitle): true, pack(r.th.cursor): true}
	var prev *image.RGBA
	for _, fr := range frames {
		for _, c := range fr.cells {
			exact[pack(c.fg)] = true
			exact[pack(c.bg)] = true
		}
		img := r.draw(fr)
		box := img.Bounds()
		if prev != nil {
			box = rgbaDiffBox(prev, img)
		}
		for y := box.Min.Y; y < box.Max.Y; y++ {
			o := img.PixOffset(box.Min.X, y)
			for x := box.Min.X; x < box.Max.X; x++ {
				hist[uint32(img.Pix[o])<<16|uint32(img.Pix[o+1])<<8|uint32(img.Pix[o+2])]++
				o += 4
			}
		}
		prev = img
	}
	// Exact colours that are actually drawn, most frequent first, at most half the palette.
	var fixedCols []colorCount
	for c := range exact {
		if hist[c] > 0 {
			fixedCols = append(fixedCols, colorCount{c, hist[c]})
		}
	}
	sort.Slice(fixedCols, func(i, j int) bool {
		if fixedCols[i].n != fixedCols[j].n {
			return fixedCols[i].n > fixedCols[j].n
		}
		return fixedCols[i].c < fixedCols[j].c
	})
	if len(hist) <= n {
		fixedCols = fixedCols[:0]
		for c, k := range hist {
			fixedCols = append(fixedCols, colorCount{c, k})
		}
		sort.Slice(fixedCols, func(i, j int) bool { return fixedCols[i].c < fixedCols[j].c })
	} else if len(fixedCols) > n/2 {
		fixedCols = fixedCols[:n/2]
	}
	var pal color.Palette
	taken := map[uint32]bool{}
	for _, c := range fixedCols {
		pal = append(pal, unpack(c.c))
		taken[c.c] = true
	}
	if left := n - len(pal); left > 0 && len(hist) > len(pal) {
		var rest []colorCount
		for c, k := range hist {
			if !taken[c] {
				rest = append(rest, colorCount{c, k})
			}
		}
		sort.Slice(rest, func(i, j int) bool { return rest[i].c < rest[j].c }) // deterministic
		pal = append(pal, medianCut(rest, left)...)
	}
	return pal
}

func rgbaDiffBox(a, b *image.RGBA) image.Rectangle {
	w, h := a.Bounds().Dx(), a.Bounds().Dy()
	minX, minY, maxX, maxY := w, h, -1, -1
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			o := a.PixOffset(x, y)
			if a.Pix[o] != b.Pix[o] || a.Pix[o+1] != b.Pix[o+1] || a.Pix[o+2] != b.Pix[o+2] {
				if x < minX {
					minX = x
				}
				if x > maxX {
					maxX = x
				}
				if y < minY {
					minY = y
				}
				maxY = y
			}
		}
	}
	if maxX < 0 {
		return image.Rectangle{}
	}
	return image.Rect(minX, minY, maxX+1, maxY+1)
}

type colorCount struct {
	c uint32
	n int
}

func pack(c color.RGBA) uint32 { return uint32(c.R)<<16 | uint32(c.G)<<8 | uint32(c.B) }

func unpack(c uint32) color.RGBA {
	return color.RGBA{R: uint8(c >> 16), G: uint8(c >> 8), B: uint8(c), A: 255}
}

func channel(c uint32, ch int) int { return int(c>>(16-8*ch)) & 0xFF }

// medianCut splits the colours into at most k boxes, always the one with the widest weighted
// range, at its weighted median, and returns each box's weighted mean.
func medianCut(cols []colorCount, k int) color.Palette {
	if len(cols) == 0 || k <= 0 {
		return nil
	}
	boxes := [][]colorCount{cols}
	for len(boxes) < k {
		best, bestScore, bestCh := -1, 0.0, 0
		for i, b := range boxes {
			if len(b) < 2 {
				continue
			}
			ch, rng := widest(b)
			total := 0
			for _, c := range b {
				total += c.n
			}
			score := float64(rng) * math.Sqrt(float64(total))
			if score > bestScore {
				best, bestScore, bestCh = i, score, ch
			}
		}
		if best < 0 {
			break
		}
		b := boxes[best]
		sort.Slice(b, func(i, j int) bool {
			ci, cj := channel(b[i].c, bestCh), channel(b[j].c, bestCh)
			if ci != cj {
				return ci < cj
			}
			return b[i].c < b[j].c
		})
		total := 0
		for _, c := range b {
			total += c.n
		}
		cut, acc := 1, 0
		for i, c := range b {
			acc += c.n
			if acc*2 >= total {
				cut = i + 1
				break
			}
		}
		if cut >= len(b) {
			cut = len(b) - 1
		}
		boxes[best] = b[:cut]
		boxes = append(boxes, b[cut:])
	}
	pal := make(color.Palette, 0, len(boxes))
	for _, b := range boxes {
		var r, g, bl, total float64
		for _, c := range b {
			w := float64(c.n)
			r += w * float64(channel(c.c, 0))
			g += w * float64(channel(c.c, 1))
			bl += w * float64(channel(c.c, 2))
			total += w
		}
		pal = append(pal, color.RGBA{R: uint8(r/total + 0.5), G: uint8(g/total + 0.5), B: uint8(bl/total + 0.5), A: 255})
	}
	return pal
}

func widest(b []colorCount) (int, int) {
	lo := [3]int{255, 255, 255}
	hi := [3]int{}
	for _, c := range b {
		for ch := 0; ch < 3; ch++ {
			v := channel(c.c, ch)
			lo[ch] = min(lo[ch], v)
			hi[ch] = max(hi[ch], v)
		}
	}
	best, rng := 0, -1
	for ch := 0; ch < 3; ch++ {
		if d := hi[ch] - lo[ch]; d > rng {
			best, rng = ch, d
		}
	}
	return best, rng
}

// quantizer maps colours to the nearest palette entry, remembering the answers.
type quantizer struct {
	pal   []color.RGBA
	cache map[uint32]uint8
}

func newQuantizer(p color.Palette) *quantizer {
	q := &quantizer{cache: map[uint32]uint8{}}
	for _, c := range p {
		q.pal = append(q.pal, c.(color.RGBA))
	}
	return q
}

func (q *quantizer) index(r, g, b uint8) uint8 {
	key := uint32(r)<<16 | uint32(g)<<8 | uint32(b)
	if i, ok := q.cache[key]; ok {
		return i
	}
	best, bestD := 0, math.MaxInt
	for i, c := range q.pal {
		dr, dg, db := int(r)-int(c.R), int(g)-int(c.G), int(b)-int(c.B)
		d := 3*dr*dr + 4*dg*dg + 2*db*db
		if d < bestD {
			best, bestD = i, d
			if d == 0 {
				break
			}
		}
	}
	q.cache[key] = uint8(best)
	return uint8(best)
}
