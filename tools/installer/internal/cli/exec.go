package cli

import (
	"context"
	"fmt"
	"io"
	"os/exec"
)

// commandIn prepares name with args to run inside dir.
func commandIn(ctx context.Context, dir, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // fixed programs of the repository
	cmd.Dir = dir
	return cmd
}

// out, outln and outf write to the terminal; a failed write there has nowhere to be reported.
func out(w io.Writer, a ...any)                 { _, _ = fmt.Fprint(w, a...) }
func outln(w io.Writer, a ...any)               { _, _ = fmt.Fprintln(w, a...) }
func outf(w io.Writer, format string, a ...any) { _, _ = fmt.Fprintf(w, format, a...) }
