package cli

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/mmtmn/docmatch/tools/installer/internal/docker"
	"github.com/mmtmn/docmatch/tools/installer/internal/install"
	"github.com/mmtmn/docmatch/tools/installer/internal/selfupdate"
	"github.com/mmtmn/docmatch/tools/installer/internal/state"
	"github.com/mmtmn/docmatch/tools/installer/internal/text"
)

func (w world) filesystem() selfupdate.FS {
	if w.fs != nil {
		return w.fs
	}
	return selfupdate.OSFS{}
}

func (w world) httpClient() *http.Client {
	if w.http != nil {
		return w.http
	}
	return &http.Client{Timeout: 5 * time.Minute}
}

// probeVersion runs a binary with --version.
func probeVersion(ctx context.Context, path string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "--version").Output() //nolint:gosec // the binary we just verified
	return strings.TrimSpace(string(out)), err
}

// managedError explains a binary someone else installed (Homebrew, winget, go run).
func managedError(exe, verb string) error {
	update := verb == "update"
	var msg string
	switch selfupdate.Managed(exe) {
	case "brew":
		msg = text.T("cli.managed_brew", "path", exe, "cmd", map[bool]string{true: "brew upgrade docmatch", false: "brew uninstall docmatch"}[update])
	case "winget":
		msg = text.T("cli.managed_winget", "path", exe, "cmd", map[bool]string{true: "winget upgrade docmatch", false: "winget uninstall docmatch"}[update])
	case "go":
		if update {
			msg = text.T("cli.managed_go_update")
		} else {
			msg = text.T("cli.managed_go_uninstall")
		}
	default:
		return nil
	}
	return errors.New(msg)
}

// runUpdate downloads the newest docmatch (or --to VERSION) from the base install.sh came from,
// checks it against SHA256SUMS.txt and its own --version, and swaps it for the running binary.
func runUpdate(ctx context.Context, w world, args []string, b Build) (int, error) {
	var to string
	var force, check bool
	if _, err := subcommandFlags("update", args, func(fs *flag.FlagSet) {
		fs.StringVar(&to, "to", "", "")
		fs.BoolVar(&force, "force", false, "")
		fs.BoolVar(&check, "check", false, "")
	}); err != nil {
		return exitUsage, err
	}
	if err := managedError(w.exe, "update"); err != nil {
		return exitFailed, err
	}
	base := first(w.env.Get("DOCMATCH_INSTALL_BASE"), b.InstallBase)
	if base == "" {
		return exitFailed, errors.New(text.T("cli.update_no_base"))
	}
	probe := w.probe
	if probe == nil {
		probe = probeVersion
	}
	u := &selfupdate.Updater{
		Base: base, Current: b.Version, Exe: w.exe, GOOS: w.info.OS, GOARCH: w.info.Arch,
		HTTP: w.httpClient(), FS: w.filesystem(), Probe: probe,
	}
	if check {
		latest, err := u.Latest(ctx)
		if err != nil {
			return exitFailed, err
		}
		if latest == b.Version {
			outln(w.stdout, text.T("cli.update_current", "version", b.Version))
		} else {
			outln(w.stdout, text.T("cli.update_available", "version", latest, "current", b.Version))
		}
		return exitOK, nil
	}
	outln(w.stdout, text.T("cli.update_from", "base", strings.TrimRight(base, "/")))
	res, err := u.Update(ctx, to, force, func(file string) { outln(w.stdout, "  "+text.T("cli.update_fetching", "file", file)) })
	if err != nil {
		return exitFailed, err
	}
	if !res.Updated {
		outln(w.stdout, text.T("cli.update_current", "version", res.To))
		return exitOK, nil
	}
	outln(w.stdout, "  "+text.T("cli.update_verified"))
	outln(w.stdout, text.T("cli.update_done", "from", res.From, "to", res.To, "path", w.exe))
	return exitOK, nil
}

// uninstallOps are the outside effects of `docmatch uninstall` (a recorder in tests).
type uninstallOps interface {
	// Down stops (and with volumes, wipes) an Experimentar installation.
	Down(ctx context.Context, opts install.Options, st *state.State, volumes bool) error
	// StopDev runs `make local-down`; WipeDev removes the make local stack's volumes.
	StopDev(ctx context.Context, source string) error
	WipeDev(ctx context.Context, source string) error
	RemoveImages(ctx context.Context, images []string) error
	RemoveAll(dir string) error
	RemoveBinary(exe string) error
}

type realOps struct{ goos string }

func (realOps) Down(ctx context.Context, opts install.Options, st *state.State, volumes bool) error {
	return install.New(opts, st).Compose().Down(ctx, volumes, nil)
}

func (realOps) StopDev(ctx context.Context, source string) error {
	out, err := commandIn(ctx, source, "make", "local-down").CombinedOutput()
	if err != nil {
		return fmt.Errorf("make local-down: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (realOps) WipeDev(ctx context.Context, source string) error {
	_, err := docker.CLI{}.Output(ctx, "compose", "-f", filepath.Join(source, "tools", "dev", "compose.yaml"), "down", "--volumes")
	return err
}

func (realOps) RemoveImages(ctx context.Context, images []string) error {
	_, err := docker.CLI{}.Output(ctx, append([]string{"image", "rm"}, images...)...)
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "no such image") {
		return nil
	}
	return err
}

func (realOps) RemoveAll(dir string) error { return os.RemoveAll(dir) }

func (o realOps) RemoveBinary(exe string) error {
	return selfupdate.Remover{FS: selfupdate.OSFS{}, GOOS: o.goos}.Remove(exe)
}

func (w world) uninstaller() uninstallOps {
	if w.ops != nil {
		return w.ops
	}
	return realOps{goos: w.info.OS}
}

// removableDir guards the installation folder against an unlucky --dir: it must be the
// installer's own (state.json inside), never a checkout, the home folder or a root.
func removableDir(dir, home string, o install.Options) bool {
	clean := filepath.Clean(dir)
	if !filepath.IsAbs(clean) || clean == filepath.Dir(clean) || clean == filepath.Clean(home) {
		return false
	}
	if rel, err := filepath.Rel(clean, home); err == nil && !strings.HasPrefix(rel, "..") {
		return false // dir contains the home folder
	}
	if _, err := os.Stat(state.Path(clean)); err != nil {
		return false
	}
	if install.IsSource(clean) {
		return false
	}
	if _, err := os.Stat(filepath.Join(clean, ".git")); err == nil {
		return false
	}
	if o.Mode == state.ModeDev && o.SourceDir != "" {
		if rel, err := filepath.Rel(clean, o.SourceDir); err == nil && !strings.HasPrefix(rel, "..") {
			return false // the developer's checkout lives inside
		}
	}
	return true
}

// runUninstall stops the installation, asks before deleting its data, images and folder, and
// removes the binary last.
func runUninstall(ctx context.Context, w world, args []string, b Build) (int, error) {
	c, err := subcommandFlags("uninstall", args, nil)
	if err != nil {
		return exitUsage, err
	}
	if !c.y && !w.isTTY {
		return exitUsage, fmt.Errorf("%w: %s", errUsage, text.T("cli.uninstall_needs_tty"))
	}
	r, err := resolveOptions(ctx, w, installFlags{common: c}, b)
	if err != nil {
		return exitUsage, err
	}
	in := bufio.NewReader(w.stdin)
	ask := func(question string) bool {
		out(w.stdout, "  "+question+" [s/N] ")
		if c.y {
			outln(w.stdout, "s (--yes)")
			return true
		}
		answer, _ := in.ReadString('\n')
		return yes(answer)
	}
	ops := w.uninstaller()
	step := func(err error, done string) {
		if err != nil {
			outln(w.stdout, "  "+text.T("cli.uninstall_warn", "error", firstLineOf(err.Error())))
			return
		}
		outln(w.stdout, "  "+done)
	}
	o := r.opts
	outln(w.stdout, text.T("cli.uninstall_title"))
	if r.st == nil {
		outln(w.stdout, "  "+text.T("cli.uninstall_none", "dir", o.Dir))
	} else {
		outln(w.stdout, "  "+text.T("cli.status_header", "mode", text.T("mode."+modeKey(o.Mode)+"_name"), "project", o.Project, "dir", o.Dir))
		switch {
		case o.Mode == state.ModeDev && o.SourceDir == "":
			outln(w.stdout, "  "+text.T("cli.uninstall_no_source"))
		case o.Mode == state.ModeDev:
			step(ops.StopDev(ctx, o.SourceDir), text.T("cli.uninstall_stopped"))
			if ask(text.T("cli.uninstall_ask_dev_volumes", "dir", o.SourceDir)) {
				step(ops.WipeDev(ctx, o.SourceDir), text.T("cli.uninstall_volumes_done"))
			}
		default:
			wipe := ask(text.T("cli.uninstall_ask_volumes"))
			done := text.T("cli.uninstall_stopped")
			if wipe {
				done = text.T("cli.uninstall_volumes_done")
			}
			step(ops.Down(ctx, o, r.st, wipe), done)
			// The images this installation has: the saved tag, not this binary's version (an
			// update since then would point at images that were never pulled).
			o.Registry, o.Tag = first(r.st.Registry, o.Registry), first(r.st.Tag, o.Tag)
			images := make([]string, 0, len(install.ImageServices))
			for _, svc := range install.ImageServices {
				images = append(images, o.Image(svc))
			}
			if ask(text.T("cli.uninstall_ask_images", "images", strings.Join(images, ", "))) {
				step(ops.RemoveImages(ctx, images), text.T("cli.uninstall_images_done"))
			}
		}
		switch {
		case !removableDir(o.Dir, w.home, o):
			outln(w.stdout, "  "+text.T("cli.uninstall_dir_kept_unsafe", "dir", o.Dir))
		case ask(text.T("cli.uninstall_ask_dir", "dir", o.Dir)):
			step(ops.RemoveAll(o.Dir), text.T("cli.uninstall_dir_done", "dir", o.Dir))
		}
		if o.Mode == state.ModeDev && o.SourceDir != "" {
			outln(w.stdout, "  "+text.T("cli.uninstall_source_kept", "dir", o.SourceDir))
		}
	}
	// The binary goes last: everything above still needed it.
	if managed := managedError(w.exe, "uninstall"); managed != nil {
		outln(w.stdout, "  "+managed.Error())
		return exitOK, nil //nolint:nilerr // a package manager owns the binary: said above, not a failure
	}
	if err := ops.RemoveBinary(w.exe); err != nil {
		return exitFailed, fmt.Errorf("%s: %w", w.exe, err)
	}
	outln(w.stdout, "  "+text.T("cli.uninstall_binary_done", "path", w.exe))
	outln(w.stdout, text.T("cli.uninstall_done"))
	return exitOK, nil
}

func firstLineOf(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	return s
}
