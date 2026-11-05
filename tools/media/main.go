// Command media records terminal sessions as asciicast v2 files and renders them as optimised
// GIFs, so every terminal GIF in docs/media can be regenerated from a tape (make media):
//
//	media record [--out F] [--size COLSxROWS] [--timeout D] TAPE
//	media record --out F [--size COLSxROWS] -- COMMAND [ARGS...]
//	media render [flags] IN.cast OUT.gif
//
// record runs the command in a pseudo-terminal of a fixed size (TERM=xterm-256color,
// COLORTERM=truecolor), answers the terminal queries a real terminal would (cursor position,
// device attributes, background colour) through an in-memory emulator, types the tape's keys and
// writes every output chunk with its time. render replays the file through the same emulator,
// samples frames at a fixed rate, shortens idle pauses (and the stretches the tape marked with
// "Marker speed=N"), draws the cells with a monospace TTF found on the system and encodes a GIF
// with one shared palette and frames that only carry the pixels that changed. See README.md.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

const usage = `uso:
  media record [--out ARQ.cast] [--size COLxLIN] [--timeout D] FITA.tape
  media record --out ARQ.cast [--size COLxLIN] -- COMANDO [ARGS...]
  media render [opções] ENTRADA.cast SAIDA.gif   (media render -h lista as opções)`

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	var err error
	switch args[0] {
	case "record":
		err = cmdRecord(args[1:], stderr)
	case "render":
		err = cmdRender(args[1:], stdout, stderr)
	case "help", "-h", "--help":
		fmt.Fprintln(stdout, usage)
		return 0
	default:
		fmt.Fprintf(stderr, "media: comando desconhecido %q\n%s\n", args[0], usage)
		return 2
	}
	if err == nil {
		return 0
	}
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	fmt.Fprintln(stderr, "media: "+err.Error())
	var u usageError
	if errors.As(err, &u) {
		return 2
	}
	return 1
}

// usageError marks a bad command line (exit status 2).
type usageError struct{ msg string }

func (u usageError) Error() string { return u.msg }

func usagef(format string, a ...any) error { return usageError{fmt.Sprintf(format, a...)} }
