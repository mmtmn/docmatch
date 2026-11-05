// Package cli is the `docmatch` command: with no subcommand it installs (or resumes) docmatch
// locally; afterwards the same binary runs `up`, `down`, `status`, `logs`, `doctor` and `demo`.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mmtmn/docmatch/tools/installer/internal/envfile"
	"github.com/mmtmn/docmatch/tools/installer/internal/install"
	"github.com/mmtmn/docmatch/tools/installer/internal/platform"
	"github.com/mmtmn/docmatch/tools/installer/internal/ports"
	"github.com/mmtmn/docmatch/tools/installer/internal/selfupdate"
	"github.com/mmtmn/docmatch/tools/installer/internal/state"
	"github.com/mmtmn/docmatch/tools/installer/internal/text"
	"github.com/mmtmn/docmatch/tools/installer/internal/toolchain"
)

// Build carries what the release script stamps into the binary (-ldflags -X).
type Build struct {
	Version  string // "0.1.0"; "dev" for local builds
	Registry string // default image registry ("ghcr.io/owner"); "" derives it from the checkout
	RepoURL  string // where to clone the code when the registry lacks the images
	// InstallBase is the download base of install.sh (<site>/install): `docmatch update` reads
	// VERSION.txt, SHA256SUMS.txt and the binaries from it. DOCMATCH_INSTALL_BASE overrides it.
	InstallBase string
}

// DefaultProject is the compose project of an installation.
const DefaultProject = "docmatch-local"

// common are the flags every command takes.
type common struct {
	dir, project            string
	plain, noAnim, ascii, y bool
}

func (c *common) register(fs *flag.FlagSet) {
	fs.StringVar(&c.dir, "dir", "", "")
	fs.StringVar(&c.project, "project", "", "")
	fs.BoolVar(&c.plain, "plain", false, "")
	fs.BoolVar(&c.noAnim, "no-animation", false, "")
	fs.BoolVar(&c.ascii, "ascii", false, "")
	fs.BoolVar(&c.y, "yes", false, "")
	fs.BoolVar(&c.y, "y", false, "")
}

// installFlags are the flags of the installation.
type installFlags struct {
	common
	mode                               string
	portWeb, portAPI, portPG, portMail int
	portList                           string
	llm, ollama, openRouterKey         string
	registry, tag, source              string
	build, noBuild, noDemo, noBrowser  bool
	verbose                            bool
	installTools                       bool
}

func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

func (f *installFlags) register(fs *flag.FlagSet) {
	f.common.register(fs)
	fs.StringVar(&f.mode, "mode", "", "")
	fs.IntVar(&f.portWeb, "port-web", 0, "")
	fs.IntVar(&f.portAPI, "port-api", 0, "")
	fs.IntVar(&f.portPG, "port-postgres", 0, "")
	fs.IntVar(&f.portMail, "port-mailpit", 0, "")
	fs.StringVar(&f.portList, "ports", "", "")
	fs.StringVar(&f.llm, "llm", "", "")
	fs.StringVar(&f.ollama, "ollama-url", "", "")
	fs.StringVar(&f.openRouterKey, "openrouter-key", "", "")
	fs.StringVar(&f.registry, "registry", "", "")
	fs.StringVar(&f.tag, "tag", "", "")
	fs.StringVar(&f.source, "source", "", "")
	fs.BoolVar(&f.build, "build", false, "")
	fs.BoolVar(&f.noBuild, "no-build", false, "")
	fs.BoolVar(&f.noDemo, "no-demo", false, "")
	fs.BoolVar(&f.noBrowser, "no-browser", false, "")
	fs.BoolVar(&f.verbose, "verbose", false, "")
	fs.BoolVar(&f.installTools, "install-tools", false, "")
}

// world is the machine and its environment (replaced in tests).
type world struct {
	env     platform.Lookup
	info    platform.Info
	home    string
	cwd     string
	exe     string
	stdout  io.Writer
	stderr  io.Writer
	stdin   io.Reader
	isTTY   bool // stdin and stdout are terminals
	now     func() time.Time
	version string

	// Lifecycle (update, uninstall); nil means the real ones.
	fs    selfupdate.FS
	http  *http.Client
	probe func(ctx context.Context, path string) (string, error)
	ops   uninstallOps
	// Desenvolver toolchain (nil means the machine's).
	runner toolchain.Runner
	exists func(path string) bool
}

func (w world) installDir(flagDir string) string {
	if flagDir != "" {
		return platform.ExpandHome(flagDir, w.home)
	}
	return platform.DefaultHome(w.info.OS, w.env, w.home)
}

// uiPrefs decides plain output, animation and ASCII art.
type uiPrefs struct {
	plain, animate, ascii bool
}

func (w world) prefs(c common) uiPrefs {
	plain := c.plain || platform.NoColor(w.env) || platform.IsCI(w.env) || !w.isTTY
	return uiPrefs{
		plain:   plain,
		animate: !plain && !c.noAnim && !w.env.Truthy("DOCMATCH_NO_ANIMATION"),
		ascii:   c.ascii || !platform.SupportsUnicode(w.info.OS, w.env),
	}
}

// resolved is the installation the flags, the environment and the saved state describe.
type resolved struct {
	opts      install.Options
	st        *state.State // nil when the folder has no installation yet
	explicit  map[string]bool
	fixedMode bool
}

var errUsage = errors.New("uso")

// resolveOptions merges, in order of precedence: flags, environment, saved state, defaults.
func resolveOptions(ctx context.Context, w world, f installFlags, b Build) (resolved, error) {
	dir := w.installDir(f.dir)
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	st, found, err := state.Load(dir)
	if err != nil {
		return resolved{}, fmt.Errorf("%s: %w", state.Path(dir), err)
	}
	if !found {
		st = nil
	}
	r := resolved{st: st, explicit: map[string]bool{}}
	o := install.Options{Dir: dir, Info: w.info, Build: install.BuildAuto, Demo: !f.noDemo}

	switch {
	case f.mode != "":
		m := strings.ToLower(f.mode)
		switch m {
		case state.ModeTry, "try":
			o.Mode = state.ModeTry
		case state.ModeDev, "dev":
			o.Mode = state.ModeDev
		default:
			return resolved{}, fmt.Errorf("%w: --mode %s (experimentar ou desenvolver)", errUsage, f.mode)
		}
		r.fixedMode = true
	case st != nil && st.Mode != "":
		o.Mode = st.Mode
	default:
		o.Mode = state.ModeTry
	}

	o.Project = first(f.project, w.env.Get("DOCMATCH_PROJECT"), stateField(st, func(s *state.State) string { return s.Project }), DefaultProject)

	p := ports.Defaults()
	if st != nil && st.Ports != nil {
		for k, v := range st.Ports {
			p[k] = v
		}
	}
	if f.portList != "" {
		parsed, err := ports.ParseList(p, f.portList)
		if err != nil {
			return resolved{}, fmt.Errorf("%w: --ports: %w", errUsage, err)
		}
		for _, name := range ports.Order {
			if parsed[name] != p[name] {
				r.explicit[name] = true
			}
		}
		p = parsed
	}
	for name, v := range map[string]int{ports.Web: f.portWeb, ports.API: f.portAPI, ports.Postgres: f.portPG, ports.Mailpit: f.portMail} {
		if v == 0 {
			continue
		}
		if _, err := ports.Parse(fmt.Sprint(v)); err != nil {
			return resolved{}, fmt.Errorf("%w: --port-%s: %w", errUsage, name, err)
		}
		p[name] = v
		r.explicit[name] = true
	}
	if err := p.Validate(); err != nil {
		return resolved{}, fmt.Errorf("%w: %w", errUsage, err)
	}
	o.Ports = p

	o.LLMMode = first(strings.ToLower(f.llm), stateField(st, func(s *state.State) string { return s.LLMMode }), envfile.LLMOff)
	switch o.LLMMode {
	case envfile.LLMOff, envfile.LLMLocal, envfile.LLMOpenRouter:
	default:
		return resolved{}, fmt.Errorf("%w: --llm %s (off, local ou openrouter)", errUsage, f.llm)
	}
	o.LocalLLM = first(f.ollama, stateField(st, func(s *state.State) string { return s.LocalLLM }))
	if o.LLMMode == envfile.LLMLocal && o.LocalLLM == "" {
		o.LocalLLM = "http://localhost:11434/v1"
	}
	if o.LocalLLM != "" {
		if err := envfile.ValidateLLMURL(o.LocalLLM); err != nil {
			return resolved{}, fmt.Errorf("%w: --ollama-url: %w", errUsage, err)
		}
	}
	saved := map[string]string{}
	if env, err := envfile.Read(filepath.Join(dir, ".env")); err == nil {
		saved = env
	}
	o.OpenRouter = first(f.openRouterKey, w.env.Get("OPENROUTER_API_KEY"), saved["OPENROUTER_API_KEY"])

	o.SourceDir = first(f.source, w.env.Get("DOCMATCH_SOURCE"))
	o.CloneDir = filepath.Join(w.home, "docmatch")
	if o.SourceDir != "" {
		o.SourceDir = platform.ExpandHome(o.SourceDir, w.home)
		if abs, err := filepath.Abs(o.SourceDir); err == nil {
			o.SourceDir = abs
		}
		if !install.IsSource(o.SourceDir) {
			// Desenvolver clones into a folder that is not a checkout yet.
			if o.Mode != state.ModeDev || !install.CanClone(o.SourceDir) {
				return resolved{}, fmt.Errorf("%w: %s", errUsage, text.T("cli.not_source", "dir", o.SourceDir))
			}
			o.CloneDir, o.SourceDir = o.SourceDir, ""
		}
	} else if st != nil && install.IsSource(st.SourceDir) {
		o.SourceDir = st.SourceDir
	} else {
		o.SourceDir = install.FindSource(w.cwd, filepath.Dir(w.exe))
	}

	remote := install.Remote(ctx, o.SourceDir)
	o.Registry = first(f.registry, w.env.Get("DOCMATCH_REGISTRY"), stateField(st, func(s *state.State) string { return s.Registry }), b.Registry, install.RegistryFromRemote(remote), install.LocalRegistry)
	o.RepoURL = first(w.env.Get("DOCMATCH_REPO"), b.RepoURL, install.CloneURL(remote))
	o.RepoRef = w.env.Get("DOCMATCH_REF")

	release := b.Version != "" && b.Version != "dev"
	tag := first(f.tag, w.env.Get("DOCMATCH_TAG"))
	if tag == "" && release {
		tag = b.Version
	}
	o.Tag = first(tag, stateField(st, func(s *state.State) string { return s.Tag }), "dev")

	switch {
	case f.build && f.noBuild:
		return resolved{}, fmt.Errorf("%w: --build e --no-build juntos", errUsage)
	case f.build:
		o.Build = install.BuildAlways
	case f.noBuild:
		o.Build = install.BuildNever
	}
	r.opts = o.Resolved()
	return r, nil
}

func first(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func stateField(st *state.State, get func(*state.State) string) string {
	if st == nil {
		return ""
	}
	return get(st)
}

// stateOrExit loads the installation of the folder for the subcommands.
func loadInstalled(ctx context.Context, w world, c common, b Build) (resolved, error) {
	f := installFlags{common: c}
	r, err := resolveOptions(ctx, w, f, b)
	if err != nil {
		return r, err
	}
	if r.st == nil {
		return r, errors.New(text.T("cli.not_installed", "dir", r.opts.Dir))
	}
	return r, nil
}

func envLookup() platform.Lookup { return os.LookupEnv }
