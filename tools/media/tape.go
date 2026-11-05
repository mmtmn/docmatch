package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// A tape describes one recording, one command per line ("#" starts a comment):
//
//	Output instalador.cast        where to write the asciicast (relative to the tape)
//	Size 92x30                    columns x rows
//	Env DOCMATCH_THEME=dark       extra environment (repeatable; quote a value to keep spaces)
//	Dir ${MEDIA_WORK}             working directory of the command
//	Title docmatch                title stored in the file (the GIF's window bar shows it)
//	Timeout 30m                   the whole recording fails after this
//	Run docmatch --no-browser     starts the command; everything below talks to it
//	Sleep 1.5s                    waits
//	Type "docmatch doctor" [@40ms]  types text, one key at a time
//	Key Enter [x3] [@200ms]       presses a key (Enter Tab Space Esc Backspace Up Down Left Right
//	                              Home End PageUp PageDown Ctrl+C Ctrl+D, or one character)
//	WaitFor "regexp" [10m]        waits until the screen matches (default 60 s)
//	Marker speed=20               a marker; render plays what follows 20x faster (speed=1 stops)
//	WaitExit [2m]                 waits for the command to end (implicit at the end, 60 s)
//
// ${NAME} is replaced by the environment variable NAME; an unset variable is an error.
type tape struct {
	Output  string
	Cols    int
	Rows    int
	Env     []string
	Dir     string
	Title   string
	Timeout time.Duration
	Run     []string
	Steps   []step
}

type stepKind int

const (
	stepSleep stepKind = iota
	stepType
	stepKey
	stepWaitFor
	stepMarker
	stepWaitExit
)

type step struct {
	Kind    stepKind
	Line    int
	Text    string         // Type text, Key bytes, Marker label
	Pattern *regexp.Regexp // WaitFor
	Dur     time.Duration  // Sleep length, WaitFor/WaitExit timeout
	Delay   time.Duration  // Type/Key delay between keys
	Repeat  int            // Key
}

var keyNames = map[string]string{
	"enter": "\r", "return": "\r", "tab": "\t", "space": " ", "esc": "\x1b", "escape": "\x1b",
	"backspace": "\x7f", "up": "\x1b[A", "down": "\x1b[B", "right": "\x1b[C", "left": "\x1b[D",
	"home": "\x1b[H", "end": "\x1b[F", "pageup": "\x1b[5~", "pagedown": "\x1b[6~",
	"ctrl+c": "\x03", "ctrl+d": "\x04", "ctrl+l": "\x0c", "ctrl+u": "\x15", "shift+tab": "\x1b[Z",
}

func parseTapeFile(path string, lookup func(string) (string, bool)) (*tape, error) {
	f, err := os.Open(path) //nolint:gosec // the tape the user asked for
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return parseTape(f, lookup)
}

func parseTape(r io.Reader, lookup func(string) (string, bool)) (*tape, error) {
	t := &tape{Cols: 92, Rows: 30, Timeout: 30 * time.Minute}
	sc := bufio.NewScanner(r)
	n := 0
	for sc.Scan() {
		n++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line, err := expandVars(line, lookup)
		if err != nil {
			return nil, fmt.Errorf("linha %d: %w", n, err)
		}
		cmd, rest, _ := strings.Cut(line, " ")
		rest = strings.TrimSpace(rest)
		if err := t.add(n, cmd, rest); err != nil {
			return nil, fmt.Errorf("linha %d (%s): %w", n, cmd, err)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(t.Run) == 0 {
		return nil, fmt.Errorf("a fita não tem Run")
	}
	return t, nil
}

func (t *tape) add(n int, cmd, rest string) error {
	started := len(t.Run) > 0
	setting := func() error {
		if started {
			return fmt.Errorf("vem antes do Run")
		}
		if rest == "" {
			return fmt.Errorf("falta o valor")
		}
		return nil
	}
	switch cmd {
	case "Output":
		if err := setting(); err != nil {
			return err
		}
		t.Output = rest
	case "Size":
		if err := setting(); err != nil {
			return err
		}
		c, r, err := parseSize(rest)
		if err != nil {
			return err
		}
		t.Cols, t.Rows = c, r
	case "Env":
		if err := setting(); err != nil {
			return err
		}
		name, value, ok := strings.Cut(rest, "=")
		if !ok || name == "" {
			return fmt.Errorf("use NOME=valor")
		}
		if strings.HasPrefix(value, "'") || strings.HasPrefix(value, `"`) {
			// Quotes keep spaces at the ends (a prompt ending in a space).
			args, err := splitArgs(value)
			if err != nil || len(args) != 1 {
				return fmt.Errorf("valor entre aspas inválido")
			}
			value = args[0]
		}
		t.Env = append(t.Env, name+"="+value)
	case "Dir":
		if err := setting(); err != nil {
			return err
		}
		t.Dir = rest
	case "Title":
		if err := setting(); err != nil {
			return err
		}
		t.Title = rest
	case "Timeout":
		if err := setting(); err != nil {
			return err
		}
		d, err := time.ParseDuration(rest)
		if err != nil {
			return err
		}
		t.Timeout = d
	case "Run":
		if started {
			return fmt.Errorf("só um Run por fita")
		}
		args, err := splitArgs(rest)
		if err != nil {
			return err
		}
		if len(args) == 0 {
			return fmt.Errorf("falta o comando")
		}
		t.Run = args
	default:
		if !started {
			return fmt.Errorf("vem depois do Run")
		}
		s, err := parseStep(cmd, rest)
		if err != nil {
			return err
		}
		s.Line = n
		t.Steps = append(t.Steps, s)
	}
	return nil
}

func parseStep(cmd, rest string) (step, error) {
	args, err := splitArgs(rest)
	if err != nil {
		return step{}, err
	}
	switch cmd {
	case "Sleep":
		if len(args) != 1 {
			return step{}, fmt.Errorf("use Sleep DURAÇÃO")
		}
		d, err := time.ParseDuration(args[0])
		return step{Kind: stepSleep, Dur: d}, err
	case "Type":
		s := step{Kind: stepType, Delay: 55 * time.Millisecond}
		if len(args) < 1 || len(args) > 2 {
			return s, fmt.Errorf(`use Type "texto" [@atraso]`)
		}
		s.Text = args[0]
		if len(args) == 2 {
			if s.Delay, err = parseDelay(args[1]); err != nil {
				return s, err
			}
		}
		return s, nil
	case "Key":
		s := step{Kind: stepKey, Repeat: 1, Delay: 150 * time.Millisecond}
		if len(args) < 1 {
			return s, fmt.Errorf("use Key NOME [xN] [@atraso]")
		}
		seq, ok := keyNames[strings.ToLower(args[0])]
		if !ok {
			if len([]rune(args[0])) != 1 {
				return s, fmt.Errorf("tecla desconhecida %q", args[0])
			}
			seq = args[0]
		}
		s.Text = seq
		for _, a := range args[1:] {
			switch {
			case strings.HasPrefix(a, "x"):
				if s.Repeat, err = strconv.Atoi(a[1:]); err != nil || s.Repeat < 1 {
					return s, fmt.Errorf("repetição inválida %q", a)
				}
			case strings.HasPrefix(a, "@"):
				if s.Delay, err = parseDelay(a); err != nil {
					return s, err
				}
			default:
				return s, fmt.Errorf("argumento inesperado %q", a)
			}
		}
		return s, nil
	case "WaitFor":
		s := step{Kind: stepWaitFor, Dur: time.Minute}
		if len(args) < 1 || len(args) > 2 {
			return s, fmt.Errorf(`use WaitFor "regexp" [prazo]`)
		}
		if s.Pattern, err = regexp.Compile(args[0]); err != nil {
			return s, err
		}
		if len(args) == 2 {
			if s.Dur, err = time.ParseDuration(args[1]); err != nil {
				return s, err
			}
		}
		return s, nil
	case "Marker":
		if len(args) != 1 {
			return step{}, fmt.Errorf("use Marker RÓTULO")
		}
		if _, err := markerSpeed(args[0]); err != nil {
			return step{}, err
		}
		return step{Kind: stepMarker, Text: args[0]}, nil
	case "WaitExit":
		s := step{Kind: stepWaitExit, Dur: time.Minute}
		if len(args) > 1 {
			return s, fmt.Errorf("use WaitExit [prazo]")
		}
		if len(args) == 1 {
			if s.Dur, err = time.ParseDuration(args[0]); err != nil {
				return s, err
			}
		}
		return s, nil
	}
	return step{}, fmt.Errorf("comando desconhecido")
}

func parseDelay(s string) (time.Duration, error) {
	if !strings.HasPrefix(s, "@") {
		return 0, fmt.Errorf("atraso deve começar com @ (%q)", s)
	}
	return time.ParseDuration(s[1:])
}

// markerSpeed reads a "speed=N" label (other labels are kept and ignored by render).
func markerSpeed(label string) (float64, error) {
	v, ok := strings.CutPrefix(label, "speed=")
	if !ok {
		return 0, nil
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f <= 0 {
		return 0, fmt.Errorf("velocidade inválida %q", label)
	}
	return f, nil
}

func parseSize(s string) (int, int, error) {
	c, r, ok := strings.Cut(strings.ToLower(s), "x")
	if !ok {
		return 0, 0, fmt.Errorf("tamanho %q: use COLUNASxLINHAS", s)
	}
	cols, err1 := strconv.Atoi(strings.TrimSpace(c))
	rows, err2 := strconv.Atoi(strings.TrimSpace(r))
	if err1 != nil || err2 != nil || cols < 10 || rows < 3 || cols > 500 || rows > 200 {
		return 0, 0, fmt.Errorf("tamanho %q inválido", s)
	}
	return cols, rows, nil
}

var varRe = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

func expandVars(s string, lookup func(string) (string, bool)) (string, error) {
	var missing []string
	out := varRe.ReplaceAllStringFunc(s, func(m string) string {
		name := m[2 : len(m)-1]
		v, ok := lookup(name)
		if !ok {
			missing = append(missing, name)
		}
		return v
	})
	if len(missing) > 0 {
		return "", fmt.Errorf("variável não definida: %s", strings.Join(missing, ", "))
	}
	return out, nil
}

// splitArgs splits a line into words, honouring "double" (with \" \\ \n \t \e escapes; any other
// backslash stays) and 'single' quotes.
func splitArgs(s string) ([]string, error) {
	var args []string
	var cur strings.Builder
	inWord := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == ' ' || c == '\t':
			if inWord {
				args = append(args, cur.String())
				cur.Reset()
				inWord = false
			}
		case c == '\'':
			j := strings.IndexByte(s[i+1:], '\'')
			if j < 0 {
				return nil, fmt.Errorf("aspas simples sem fechar")
			}
			cur.WriteString(s[i+1 : i+1+j])
			i += j + 1
			inWord = true
		case c == '"':
			i++
			closed := false
			for ; i < len(s); i++ {
				if s[i] == '"' {
					closed = true
					break
				}
				if s[i] == '\\' && i+1 < len(s) {
					i++
					switch s[i] {
					case 'n':
						cur.WriteByte('\n')
					case 't':
						cur.WriteByte('\t')
					case 'e':
						cur.WriteByte(0x1b)
					case '"', '\\':
						cur.WriteByte(s[i])
					default: // \d, \s... stay as written, for WaitFor patterns
						cur.WriteByte('\\')
						cur.WriteByte(s[i])
					}
					continue
				}
				cur.WriteByte(s[i])
			}
			if !closed {
				return nil, fmt.Errorf("aspas duplas sem fechar")
			}
			inWord = true
		default:
			cur.WriteByte(c)
			inWord = true
		}
	}
	if inWord {
		args = append(args, cur.String())
	}
	return args, nil
}
