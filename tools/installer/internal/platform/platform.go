// Package platform answers what the installer needs to know about this machine: system and
// architecture, terminal abilities, where to keep the installation, free disk space and how to
// open a browser. Everything that reads the environment takes a Lookup so tests can pin it.
package platform

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Lookup reads an environment variable (os.LookupEnv in production).
type Lookup func(string) (string, bool)

// Get returns the value of name, or "" when unset.
func (l Lookup) Get(name string) string {
	if l == nil {
		return ""
	}
	v, _ := l(name)
	return v
}

// Truthy reports whether name is set to something other than "", "0", "false" or "no".
func (l Lookup) Truthy(name string) bool {
	v := strings.ToLower(strings.TrimSpace(l.Get(name)))
	return v != "" && v != "0" && v != "false" && v != "no"
}

// Info is the machine the installer runs on.
type Info struct {
	OS   string // runtime.GOOS
	Arch string // runtime.GOARCH
	WSL  bool   // Linux inside the Windows Subsystem for Linux
}

// Detect describes this machine.
func Detect() Info {
	info := Info{OS: runtime.GOOS, Arch: runtime.GOARCH}
	if info.OS == "linux" {
		if data, err := os.ReadFile("/proc/sys/kernel/osrelease"); err == nil {
			info.WSL = strings.Contains(strings.ToLower(string(data)), "microsoft")
		}
	}
	return info
}

// Supported reports whether there is a release of the installer for this system.
func (i Info) Supported() bool {
	switch i.OS {
	case "linux", "darwin", "windows":
	default:
		return false
	}
	return i.Arch == "amd64" || i.Arch == "arm64"
}

// Name is "linux/amd64", plus " (WSL)" inside WSL.
func (i Info) Name() string {
	s := i.OS + "/" + i.Arch
	if i.WSL {
		s += " (WSL)"
	}
	return s
}

// IsCI reports whether a CI system runs the installer (CI, GITHUB_ACTIONS, BUILDKITE...).
func IsCI(env Lookup) bool {
	for _, name := range []string{"CI", "GITHUB_ACTIONS", "GITLAB_CI", "BUILDKITE", "TF_BUILD", "JENKINS_URL"} {
		if env.Truthy(name) {
			return true
		}
	}
	return false
}

// NoColor reports the NO_COLOR convention (any non-empty value) or TERM=dumb.
func NoColor(env Lookup) bool {
	return env.Get("NO_COLOR") != "" || env.Get("TERM") == "dumb"
}

// SupportsUnicode guesses whether the terminal draws box and braille characters. Windows
// Terminal, VS Code and ConEmu do; the legacy console host does not. On Unix the locale decides.
func SupportsUnicode(goos string, env Lookup) bool {
	if goos == "windows" {
		return env.Get("WT_SESSION") != "" || env.Get("TERM_PROGRAM") == "vscode" ||
			env.Get("ConEmuANSI") == "ON" || strings.Contains(env.Get("TERM"), "xterm")
	}
	if env.Get("TERM") == "linux" { // the Linux virtual console has no braille
		return false
	}
	for _, name := range []string{"LC_ALL", "LC_CTYPE", "LANG"} {
		if v := env.Get(name); v != "" {
			v = strings.ToLower(v)
			return strings.Contains(v, "utf-8") || strings.Contains(v, "utf8")
		}
	}
	// No locale at all: macOS terminals are UTF-8; elsewhere, be safe.
	return goos == "darwin"
}

// ThemeOverride reads DOCMATCH_THEME=light|dark; ok is false when the terminal's own answer
// (asked once at start by Bubble Tea) should be used.
func ThemeOverride(env Lookup) (dark, ok bool) {
	switch strings.ToLower(env.Get("DOCMATCH_THEME")) {
	case "light":
		return false, true
	case "dark":
		return true, true
	}
	return false, false
}

// DefaultHome is where an installation lives unless DOCMATCH_HOME or --dir says otherwise:
// Linux $XDG_DATA_HOME/docmatch/local (~/.local/share/docmatch/local), macOS
// ~/Library/Application Support/docmatch/local, Windows %LOCALAPPDATA%\docmatch\local.
func DefaultHome(goos string, env Lookup, userHome string) string {
	if v := env.Get("DOCMATCH_HOME"); v != "" {
		return v
	}
	switch goos {
	case "windows":
		base := env.Get("LOCALAPPDATA")
		if base == "" {
			base = filepath.Join(userHome, "AppData", "Local")
		}
		return filepath.Join(base, "docmatch", "local")
	case "darwin":
		return filepath.Join(userHome, "Library", "Application Support", "docmatch", "local")
	default:
		base := env.Get("XDG_DATA_HOME")
		if base == "" {
			base = filepath.Join(userHome, ".local", "share")
		}
		return filepath.Join(base, "docmatch", "local")
	}
}

// ExpandHome turns a leading ~ into the user's home folder.
func ExpandHome(path, userHome string) string {
	if path == "~" {
		return userHome
	}
	if strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`) {
		return filepath.Join(userHome, path[2:])
	}
	return path
}

// existingParent walks up from path until it finds something that exists.
func existingParent(path string) (string, error) {
	p, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
		parent := filepath.Dir(p)
		if parent == p {
			return "", errors.New("nenhuma pasta existente em " + path)
		}
		p = parent
	}
}

// FreeBytes is the space available to this user on the disk that holds path (or, when path does
// not exist yet, its nearest existing parent).
func FreeBytes(path string) (uint64, error) {
	p, err := existingParent(path)
	if err != nil {
		return 0, err
	}
	return freeBytes(p)
}

// HumanBytes renders a size the pt-BR way: "12,1 GB", "830 MB".
func HumanBytes(n uint64) string {
	const unit = 1000
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	units := []string{"kB", "MB", "GB", "TB"}
	value := float64(n) / unit
	i := 0
	for value >= unit && i < len(units)-1 {
		value /= unit
		i++
	}
	s := fmt.Sprintf("%.1f", value)
	if value >= 100 {
		s = fmt.Sprintf("%.0f", value)
	}
	return strings.Replace(s, ".", ",", 1) + " " + units[i]
}

// CanOpenBrowser reports whether opening a browser makes sense here: not over SSH, not in CI and,
// on Linux outside WSL, only with a graphical session.
func CanOpenBrowser(info Info, env Lookup) bool {
	if IsCI(env) || env.Get("SSH_CONNECTION") != "" || env.Get("SSH_TTY") != "" {
		return false
	}
	if info.OS == "linux" && !info.WSL {
		return env.Get("DISPLAY") != "" || env.Get("WAYLAND_DISPLAY") != ""
	}
	return true
}

// browserCommand is the command that opens url on this system.
func browserCommand(info Info, url string) (string, []string) {
	switch {
	case info.OS == "darwin":
		return "open", []string{url}
	case info.OS == "windows":
		return "rundll32", []string{"url.dll,FileProtocolHandler", url}
	case info.WSL:
		if _, err := exec.LookPath("wslview"); err == nil {
			return "wslview", []string{url}
		}
		return "cmd.exe", []string{"/c", "start", "", url}
	default:
		return "xdg-open", []string{url}
	}
}

// OpenBrowser opens url without waiting for the browser.
func OpenBrowser(info Info, url string) error {
	name, args := browserCommand(info, url)
	cmd := exec.Command(name, args...) //nolint:gosec,noctx // fixed program; url is ours
	cmd.Stdout, cmd.Stderr = nil, nil
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
