package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"
)

// Variables that would make a program drop colours or switch to a plain, non-interactive output.
var droppedEnv = []string{"NO_COLOR", "CI", "GITHUB_ACTIONS", "GITLAB_CI", "BUILDKITE", "TF_BUILD", "TERM_PROGRAM", "COLUMNS", "LINES"}

func cmdRecord(args []string, stderr io.Writer) error {
	fs := flag.NewFlagSet("record", flag.ContinueOnError)
	fs.SetOutput(stderr)
	out := fs.String("out", "", "asciicast de saída (padrão: o Output da fita)")
	size := fs.String("size", "", "COLUNASxLINHAS (padrão: o Size da fita ou 92x30)")
	timeout := fs.Duration("timeout", 0, "prazo da gravação inteira (padrão: o Timeout da fita ou 30m)")
	themePath := fs.String("theme", "", "tema JSON cujas cores o terminal informa ao programa (padrão: o embutido)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	var t *tape
	switch {
	case len(rest) == 1 && strings.HasSuffix(rest[0], ".tape") && !contains(args, "--"):
		var err error
		if t, err = parseTapeFile(rest[0], os.LookupEnv); err != nil {
			return fmt.Errorf("%s: %w", rest[0], err)
		}
		if t.Output != "" && !filepath.IsAbs(t.Output) {
			t.Output = filepath.Join(filepath.Dir(rest[0]), t.Output)
		}
	case len(rest) >= 1:
		t = &tape{Cols: 92, Rows: 30, Timeout: 30 * time.Minute, Run: rest}
	default:
		return usagef("record: falta a fita ou o comando (media help)")
	}
	if *out != "" {
		t.Output = *out
	}
	if t.Output == "" {
		return usagef("record: falta --out (ou Output na fita)")
	}
	if *size != "" {
		c, r, err := parseSize(*size)
		if err != nil {
			return usagef("record: %v", err)
		}
		t.Cols, t.Rows = c, r
	}
	if *timeout > 0 {
		t.Timeout = *timeout
	}
	th, err := loadTheme(*themePath)
	if err != nil {
		return err
	}
	return record(t, th, stderr)
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// session is a running command in a pseudo-terminal, mirrored by an emulator.
type session struct {
	ptmx  *os.File
	emu   *vt.Emulator
	mu    sync.Mutex // guards emu
	cast  *castWriter
	start time.Time

	readDone chan struct{}
	exited   chan struct{}
	exitErr  error
}

func (s *session) now() float64 { return time.Since(s.start).Seconds() }

func record(t *tape, th *theme, logw io.Writer) error {
	if err := os.MkdirAll(filepath.Dir(t.Output), 0o750); err != nil {
		return err
	}
	f, err := os.Create(t.Output)
	if err != nil {
		return err
	}
	defer f.Close()
	cw, err := newCastWriter(f, castHeader{
		Width: t.Cols, Height: t.Rows, Timestamp: time.Now().Unix(), Title: t.Title,
		Env: map[string]string{"TERM": "xterm-256color", "SHELL": os.Getenv("SHELL")},
	})
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), t.Timeout)
	defer cancel()
	cmd := exec.Command(t.Run[0], t.Run[1:]...) //nolint:gosec // the command the tape asked for
	cmd.Dir = t.Dir
	cmd.Env = recordEnv(os.Environ(), t.Env)
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(t.Cols), Rows: uint16(t.Rows)}) //nolint:gosec // sizes checked by parseSize
	if err != nil {
		return fmt.Errorf("iniciar %s: %w", t.Run[0], err)
	}
	defer ptmx.Close()

	emu := vt.NewEmulator(t.Cols, t.Rows)
	emu.SetDefaultForegroundColor(th.fg)
	emu.SetDefaultBackgroundColor(th.bg)
	emu.SetDefaultCursorColor(th.cursor)
	s := &session{ptmx: ptmx, emu: emu, cast: cw, start: time.Now(), readDone: make(chan struct{}), exited: make(chan struct{})}

	// What the program asks the terminal (cursor position, attributes, colours) goes back to it.
	go func() { _, _ = io.Copy(ptmx, emu) }()
	go s.readLoop()
	go func() {
		s.exitErr = cmd.Wait()
		close(s.exited)
	}()

	fmt.Fprintf(logw, "media: gravando %s (%dx%d) em %s\n", strings.Join(t.Run, " "), t.Cols, t.Rows, t.Output)
	stepErr := s.play(ctx, t.Steps, logw)
	if stepErr == nil {
		// The tape is over: the command should end by itself.
		stepErr = s.waitExit(ctx, time.Minute)
	}
	if stepErr != nil {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-s.exited:
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			<-s.exited
		}
	}
	// Let the last output arrive, then close.
	select {
	case <-s.readDone:
	case <-time.After(2 * time.Second):
	}
	closeReplies(emu)
	if err := cw.Close(s.now()); err != nil {
		return err
	}
	if stepErr != nil {
		return fmt.Errorf("%w\ntela:\n%s", stepErr, s.screen())
	}
	if s.exitErr != nil {
		return fmt.Errorf("%s terminou com erro: %w\ntela:\n%s", t.Run[0], s.exitErr, s.screen())
	}
	fmt.Fprintf(logw, "media: %s gravado (%.1f s)\n", t.Output, s.now())
	return nil
}

// recordEnv is the parent's environment without the variables that turn colours or the
// interactive interface off, with the terminal's own variables and the tape's on top.
func recordEnv(parent, extra []string) []string {
	env := make([]string, 0, len(parent)+len(extra)+2)
	for _, kv := range parent {
		name, _, _ := strings.Cut(kv, "=")
		if contains(droppedEnv, name) {
			continue
		}
		env = append(env, kv)
	}
	env = append(env, "TERM=xterm-256color", "COLORTERM=truecolor")
	return append(env, extra...) // exec keeps the last value of a repeated name
}

func (s *session) readLoop() {
	defer close(s.readDone)
	buf := make([]byte, 64<<10)
	for {
		n, err := s.ptmx.Read(buf)
		if n > 0 {
			s.cast.Output(s.now(), buf[:n])
			s.mu.Lock()
			_, _ = s.emu.Write(buf[:n])
			s.mu.Unlock()
		}
		if err != nil {
			return // EIO once the command and its children closed the terminal
		}
	}
}

func (s *session) play(ctx context.Context, steps []step, logw io.Writer) error {
	for _, st := range steps {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("linha %d: prazo da gravação esgotado", st.Line)
		}
		switch st.Kind {
		case stepSleep:
			if err := sleepCtx(ctx, st.Dur); err != nil {
				return err
			}
		case stepType:
			for _, r := range st.Text {
				if err := s.send(string(r)); err != nil {
					return err
				}
				if err := sleepCtx(ctx, st.Delay); err != nil {
					return err
				}
			}
		case stepKey:
			for i := 0; i < st.Repeat; i++ {
				if err := s.send(st.Text); err != nil {
					return err
				}
				if err := sleepCtx(ctx, st.Delay); err != nil {
					return err
				}
			}
		case stepWaitFor:
			fmt.Fprintf(logw, "media: linha %d: esperando %q\n", st.Line, st.Pattern.String())
			if err := s.waitFor(ctx, st); err != nil {
				return err
			}
		case stepMarker:
			s.cast.Marker(s.now(), st.Text)
		case stepWaitExit:
			if err := s.waitExit(ctx, st.Dur); err != nil {
				return fmt.Errorf("linha %d: %w", st.Line, err)
			}
		}
	}
	return nil
}

func (s *session) send(keys string) error {
	select {
	case <-s.exited:
		return fmt.Errorf("o comando já terminou; não dá para digitar %q", keys)
	default:
	}
	_, err := io.WriteString(s.ptmx, keys)
	return err
}

func (s *session) waitFor(ctx context.Context, st step) error {
	deadline := time.Now().Add(st.Dur)
	for {
		if st.Pattern.MatchString(s.screen()) {
			return nil
		}
		select {
		case <-s.exited:
			// The last output may still be on its way.
			select {
			case <-s.readDone:
			case <-time.After(time.Second):
			}
			if st.Pattern.MatchString(s.screen()) {
				return nil
			}
			return fmt.Errorf("linha %d: o comando terminou antes de aparecer %q", st.Line, st.Pattern.String())
		case <-ctx.Done():
			return fmt.Errorf("linha %d: prazo da gravação esgotado esperando %q", st.Line, st.Pattern.String())
		case <-time.After(100 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("linha %d: %q não apareceu em %s", st.Line, st.Pattern.String(), st.Dur)
		}
	}
}

func (s *session) waitExit(ctx context.Context, d time.Duration) error {
	select {
	case <-s.exited:
		return nil
	case <-ctx.Done():
		return errors.New("prazo da gravação esgotado esperando o comando terminar")
	case <-time.After(d):
		return fmt.Errorf("o comando não terminou em %s", d)
	}
}

// screen is the emulator's screen as text, trailing spaces removed.
func (s *session) screen() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return screenText(s.emu)
}

func screenText(emu *vt.Emulator) string {
	var b strings.Builder
	for y := 0; y < emu.Height(); y++ {
		var line strings.Builder
		for x := 0; x < emu.Width(); x++ {
			c := emu.CellAt(x, y)
			switch {
			case c == nil:
				line.WriteByte(' ')
			case c.Width == 0:
				// the second half of a wide character
			case c.Content == "":
				line.WriteByte(' ')
			default:
				line.WriteString(c.Content)
			}
		}
		b.WriteString(strings.TrimRight(line.String(), " "))
		b.WriteByte('\n')
	}
	return b.String()
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	select {
	case <-ctx.Done():
		return errors.New("prazo da gravação esgotado")
	case <-time.After(d):
		return nil
	}
}
