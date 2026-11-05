package main

import (
	"bytes"
	"encoding/json"
	"image"
	"image/gif"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func lookup(env map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := env[k]; return v, ok }
}

func TestParseTape(t *testing.T) {
	src := `# comentário
Output out.cast
Size 80x24
Env A=b c
Env PS1='~ $ '
Title docmatch
Run ${BIN} --flag "dois termos" 'aspas simples'
Sleep 1.5s
Type "oi\n" @10ms
Key Enter x2 @5ms
Key q
WaitFor "pronto \d+" 2m
Marker speed=20
Marker end
WaitExit 10s
`
	tp, err := parseTape(strings.NewReader(src), lookup(map[string]string{"BIN": "/bin/echo"}))
	if err != nil {
		t.Fatal(err)
	}
	if tp.Output != "out.cast" || tp.Cols != 80 || tp.Rows != 24 || tp.Title != "docmatch" {
		t.Fatalf("configuração: %+v", tp)
	}
	if got := strings.Join(tp.Run, "|"); got != "/bin/echo|--flag|dois termos|aspas simples" {
		t.Fatalf("Run = %q", got)
	}
	if len(tp.Env) != 2 || tp.Env[0] != "A=b c" || tp.Env[1] != "PS1=~ $ " {
		t.Fatalf("Env = %q", tp.Env)
	}
	kinds := []stepKind{stepSleep, stepType, stepKey, stepKey, stepWaitFor, stepMarker, stepMarker, stepWaitExit}
	if len(tp.Steps) != len(kinds) {
		t.Fatalf("%d passos, quero %d", len(tp.Steps), len(kinds))
	}
	for i, k := range kinds {
		if tp.Steps[i].Kind != k {
			t.Errorf("passo %d: tipo %d, quero %d", i, tp.Steps[i].Kind, k)
		}
	}
	if s := tp.Steps[1]; s.Text != "oi\n" || s.Delay != 10*time.Millisecond {
		t.Errorf("Type: %+v", s)
	}
	if s := tp.Steps[2]; s.Text != "\r" || s.Repeat != 2 || s.Delay != 5*time.Millisecond {
		t.Errorf("Key Enter: %+v", s)
	}
	if s := tp.Steps[4]; !s.Pattern.MatchString("tudo pronto 42") || s.Dur != 2*time.Minute {
		t.Errorf("WaitFor: %v %s", s.Pattern, s.Dur)
	}
}

func TestParseTapeErrors(t *testing.T) {
	cases := map[string]string{
		"sem Run":              "Size 80x24\n",
		"variável":             "Run ${NADA}\n",
		"passo antes do Run":   "Sleep 1s\nRun x\n",
		"ajuste depois":        "Run x\nSize 80x24\n",
		"tecla":                "Run x\nKey Hiper\n",
		"aspas":                "Run x \"aberta\n",
		"velocidade":           "Run x\nMarker speed=0\n",
		"tamanho":              "Size 3x2\nRun x\n",
		"regexp":               "Run x\nWaitFor \"(\"\n",
		"comando desconhecido": "Run x\nPula\n",
	}
	for name, src := range cases {
		if _, err := parseTape(strings.NewReader(src), lookup(nil)); err == nil {
			t.Errorf("%s: aceitou %q", name, src)
		}
	}
}

func TestCompleteUTF8(t *testing.T) {
	box := []byte("ab─") // ─ is 3 bytes
	for cut := 0; cut <= len(box); cut++ {
		got := completeUTF8(box[:cut])
		want := cut
		if cut == 3 || cut == 4 {
			want = 2 // inside ─
		}
		if got != want {
			t.Errorf("completeUTF8(%q) = %d, quero %d", box[:cut], got, want)
		}
	}
}

func TestCastRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	cw, err := newCastWriter(&buf, castHeader{Width: 10, Height: 3, Title: "t"})
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("ok ─ fim")
	cw.Output(0.1, data[:4]) // splits ─
	cw.Output(0.2, data[4:])
	cw.Marker(0.3, "speed=4")
	if err := cw.Close(0.4); err != nil {
		t.Fatal(err)
	}
	h, ev, err := readCast(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if h.Version != 2 || h.Width != 10 || h.Height != 3 || h.Title != "t" {
		t.Fatalf("cabeçalho %+v", h)
	}
	var out strings.Builder
	for _, e := range ev {
		if e.Kind == "o" {
			out.WriteString(e.Data)
		}
	}
	if out.String() != string(data) {
		t.Fatalf("saída %q, quero %q", out.String(), data)
	}
	if last := ev[len(ev)-1]; last.Kind != "m" || last.Data != "speed=4" {
		t.Fatalf("marcador %+v", last)
	}
}

func TestTimeline(t *testing.T) {
	ev := []castEvent{
		{0, "o", "a"}, {1, "o", "b"}, {11, "o", "c"}, // 10 s idle → 1.5 s
		{11, "m", "speed=10"}, {31, "o", "d"}, // 20 s at 10x → 2 s, capped to 1.5 s
		{31, "m", "speed=1"}, {31.5, "o", "e"},
	}
	tl := timeline(ev, renderOpts{speed: 1, idle: 1500 * time.Millisecond})
	want := []float64{0, 1, 2.5, 2.5, 4, 4, 4.5}
	for i, w := range want {
		if d := tl[i].at - w; d > 1e-9 || d < -1e-9 {
			t.Errorf("evento %d em %.3f s, quero %.3f", i, tl[i].at, w)
		}
	}
	tl = timeline(ev, renderOpts{speed: 2, idle: 0})
	if got := tl[len(tl)-1].at; got < 6.749 || got > 6.751 { // (1+10)/2 + 20/(2*10) + 0.5/2
		t.Errorf("fim em %.3f s, quero 6,75", got)
	}
}

func TestThemeMatchesTokens(t *testing.T) {
	th, err := loadTheme("")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join("..", "..", "packages", "design-tokens", "tokens.json"))
	if err != nil {
		t.Fatal(err)
	}
	var tokens struct {
		Color map[string]struct {
			Bg   map[string]string `json:"bg"`
			Text map[string]string `json:"text"`
		} `json:"color"`
	}
	if err := json.Unmarshal(raw, &tokens); err != nil {
		t.Fatal(err)
	}
	dark := tokens.Color["dark"]
	for name, pair := range map[string]struct {
		got  [3]uint8
		want string
	}{
		"background": {[3]uint8{th.bg.R, th.bg.G, th.bg.B}, dark.Bg["canvas"]},
		"foreground": {[3]uint8{th.fg.R, th.fg.G, th.fg.B}, dark.Text["primary"]},
		"bar":        {[3]uint8{th.bar.R, th.bar.G, th.bar.B}, dark.Bg["surface"]},
		"bar_title":  {[3]uint8{th.barTitle.R, th.barTitle.G, th.barTitle.B}, dark.Text["muted"]},
	} {
		want, err := parseHex(pair.want)
		if err != nil {
			t.Fatalf("%s: token %q: %v", name, pair.want, err)
		}
		if pair.got != [3]uint8{want.R, want.G, want.B} {
			t.Errorf("%s do tema = %v, token = %s", name, pair.got, pair.want)
		}
	}
}

func TestBlockAndBraille(t *testing.T) {
	m := image.NewAlpha(image.Rect(0, 0, 8, 16))
	if !drawBlock(m, '█') || m.AlphaAt(0, 0).A != 255 || m.AlphaAt(7, 15).A != 255 {
		t.Fatal("█ deve cobrir a célula inteira")
	}
	m = image.NewAlpha(image.Rect(0, 0, 8, 16))
	drawBlock(m, '▄')
	if m.AlphaAt(3, 3).A != 0 || m.AlphaAt(3, 12).A != 255 {
		t.Fatal("▄ deve cobrir só a metade de baixo")
	}
	m = image.NewAlpha(image.Rect(0, 0, 8, 16))
	drawBlock(m, '░')
	if a := m.AlphaAt(4, 8).A; a != 64 {
		t.Fatalf("░ com cobertura %d, quero 64", a)
	}
	m = image.NewAlpha(image.Rect(0, 0, 8, 16))
	if !drawBraille(m, '⠁') || m.AlphaAt(2, 2).A == 0 || m.AlphaAt(5, 13).A != 0 {
		t.Fatal("⠁ deve ter só o ponto de cima à esquerda")
	}
	if drawBlock(m, 'a') || drawBraille(m, 'a') {
		t.Fatal("letras não são blocos")
	}
}

// TestRender draws a small recording end to end (skipped without a monospace font).
func TestRender(t *testing.T) {
	if _, err := openFonts("", 13, 1); err != nil {
		t.Skipf("sem fonte: %v", err)
	}
	dir := t.TempDir()
	in := filepath.Join(dir, "a.cast")
	var buf bytes.Buffer
	cw, _ := newCastWriter(&buf, castHeader{Width: 20, Height: 4, Title: "teste"})
	cw.Output(0, []byte("\x1b[1;32m✓\x1b[0m pronto ░█\r\n"))
	cw.Output(0.5, []byte("\x1b[38;2;96;165;250mazul\x1b[0m ⠋"))
	cw.Marker(0.6, "speed=10")
	cw.Output(5, []byte("\r\nfim"))
	_ = cw.Close(5)
	if err := os.WriteFile(in, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "a.gif")
	var stdout, stderr bytes.Buffer
	if code := run([]string{"render", "--hold", "1s", in, out}, &stdout, &stderr); code != 0 {
		t.Fatalf("render saiu com %d: %s", code, stderr.String())
	}
	f, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	g, err := gif.DecodeAll(f)
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Image) < 3 {
		t.Fatalf("%d quadros, quero pelo menos 3", len(g.Image))
	}
	total := 0
	for _, d := range g.Delay {
		total += d
	}
	// 0.5 s, then 4.4 s at 10x (0.44 s), the last tick (1/15 s) and the 1 s hold.
	if total < 200 || total > 220 {
		t.Fatalf("duração %d cs, quero ~211", total)
	}
	if g.Config.Width > 800 {
		t.Fatalf("largura %d", g.Config.Width)
	}
}

func TestRecord(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "pergunta.sh")
	if err := os.WriteFile(script, []byte("printf 'qual? '\nread -r x\nprintf 'ok %s\\n' \"$x\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tapePath := filepath.Join(dir, "t.tape")
	src := "Output t.cast\nSize 40x5\nRun sh " + script + "\nWaitFor 'qual\\?'\nType \"sim\" @5ms\nKey Enter\nWaitExit 10s\n"
	if err := os.WriteFile(tapePath, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"record", tapePath}, &stdout, &stderr); code != 0 {
		t.Fatalf("record saiu com %d: %s", code, stderr.String())
	}
	f, err := os.Open(filepath.Join(dir, "t.cast"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	h, ev, err := readCast(f)
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	for _, e := range ev {
		out.WriteString(e.Data)
	}
	if h.Width != 40 || !strings.Contains(out.String(), "ok sim") {
		t.Fatalf("gravação: %dx%d %q", h.Width, h.Height, out.String())
	}
}
