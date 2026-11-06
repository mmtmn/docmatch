package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"

	dm "github.com/mmtmn/docmatch/sdks/go"
	"github.com/mmtmn/docmatch/tools/installer/internal/text"
	"github.com/mmtmn/docmatch/tools/installer/internal/ui"
)

type connection struct {
	API string `json:"api"`
	Key string `json:"key,omitempty"`
}

func loadConnection(dir string) (connection, error) {
	var c connection
	data, err := os.ReadFile(filepath.Join(dir, "connection.json")) //nolint:gosec // private local configuration
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if err = json.Unmarshal(data, &c); err != nil {
		return c, err
	}
	_, err = dm.New(c.API, c.Key)
	return c, err
}

func saveConnection(dir string, c connection) error {
	if _, err := dm.New(c.API, c.Key); err != nil {
		return err
	}
	if strings.ContainsAny(c.Key, "\r\n") {
		return errors.New(text.T("home.invalid_key"))
	}
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".connection-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if _, err = f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), filepath.Join(dir, "connection.json"))
}

func homeForm(w world, groups ...*huh.Group) *huh.Form {
	prefs := w.prefs(common{})
	theme := ui.NewTheme(prefs.ascii, prefs.animate, lipgloss.HasDarkBackground())
	return huh.NewForm(groups...).WithInput(w.stdin).WithOutput(w.stdout).
		WithTheme(theme.FormTheme()).WithAccessible(prefs.plain)
}

func runHome(ctx context.Context, w world, b Build) (int, error) {
	for {
		action := "workspace"
		form := homeForm(w, huh.NewGroup(huh.NewSelect[string]().
			Title("docmatch").Description(text.T("home.description")).Options(
			huh.NewOption(text.T("home.open"), "workspace"),
			huh.NewOption(text.T("home.install"), "install"),
			huh.NewOption(text.T("home.connect"), "config"),
			huh.NewOption(text.T("home.up"), "up"),
			huh.NewOption(text.T("home.down"), "down"),
			huh.NewOption(text.T("home.doctor"), "doctor"),
			huh.NewOption(text.T("home.quit"), "quit"),
		).Value(&action)))
		if err := form.RunWithContext(ctx); err != nil {
			if errors.Is(err, huh.ErrUserAborted) || ctx.Err() != nil {
				return exitCancelled, nil //nolint:nilerr // cancellation is represented by the exit code
			}
			return exitFailed, err
		}
		if action == "quit" {
			return exitOK, nil
		}
		// Use the same command handlers as scripts; the menu never constructs shell text.
		code := run(ctx, w, []string{action}, b)
		if ctx.Err() != nil {
			return exitCancelled, nil //nolint:nilerr // cancellation is represented by the exit code
		}
		if code != exitOK && code != exitCancelled {
			outln(w.stderr, text.T("home.retry"))
		}
	}
}

func runConnection(ctx context.Context, w world, args []string) (int, error) {
	c, err := subcommandFlags("config", args, nil)
	if err != nil {
		return exitUsage, err
	}
	if !w.isTTY {
		return exitUsage, fmt.Errorf("%w: %s", errUsage, text.T("home.terminal_required"))
	}
	dir := w.installDir(c.dir)
	profile, err := loadConnection(dir)
	if err != nil {
		return exitFailed, err
	}
	if profile.API == "" {
		profile.API = "http://localhost:8080"
	}
	form := homeForm(w, huh.NewGroup(
		huh.NewInput().Title(text.T("home.api")).Description(text.T("home.api_help")).Value(&profile.API).
			Validate(func(s string) error { _, err := dm.New(strings.TrimSpace(s), ""); return err }),
		huh.NewInput().Title(text.T("home.key")).Description(text.T("home.key_help")).
			EchoMode(huh.EchoModePassword).Value(&profile.Key),
	))
	if err := form.RunWithContext(ctx); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return exitCancelled, nil
		}
		return exitFailed, err
	}
	profile.API, profile.Key = strings.TrimSpace(profile.API), strings.TrimSpace(profile.Key)
	if err := saveConnection(dir, profile); err != nil {
		return exitFailed, err
	}
	outln(w.stdout, text.T("home.saved"))
	return exitOK, nil
}
