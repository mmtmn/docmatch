// Package install turns the user's choices into the steps of an installation: Experimentar
// (Docker only: deploy/local/compose.yaml with images pulled from the registry or built from the
// code) or Desenvolver (`make local` with this machine's toolchains), then the check of the
// services and the Vantara twin. The same Installer backs `docmatch up/down/status/demo`.
package install

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mmtmn/docmatch/tools/installer/internal/assets"
	"github.com/mmtmn/docmatch/tools/installer/internal/demo"
	"github.com/mmtmn/docmatch/tools/installer/internal/docker"
	"github.com/mmtmn/docmatch/tools/installer/internal/envfile"
	"github.com/mmtmn/docmatch/tools/installer/internal/flow"
	"github.com/mmtmn/docmatch/tools/installer/internal/platform"
	"github.com/mmtmn/docmatch/tools/installer/internal/ports"
	"github.com/mmtmn/docmatch/tools/installer/internal/state"
	"github.com/mmtmn/docmatch/tools/installer/internal/text"
	"github.com/mmtmn/docmatch/tools/installer/internal/toolchain"
)

// Build policies for the docmatch images.
const (
	BuildAuto   = "auto"   // pull from the registry, build when it does not have them
	BuildAlways = "always" // always build from the code
	BuildNever  = "never"  // only pull
)

// LocalRegistry names images that only exist on this machine (never pulled: the name cannot
// resolve to Docker Hub or any registry).
const LocalRegistry = "docmatch.local"

// Services with docmatch images, and the infrastructure ones.
var (
	ImageServices = []string{"api", "harness", "web"}
	BaseServices  = []string{"postgres", "mailpit"}
	AppServices   = []string{"harness", "api", "worker", "web"}
)

// Step ids (also the keys of their titles under installer.steps).
const (
	StepPrepare    = "prepare"
	StepBaseImages = "base_images"
	StepImages     = "images"
	StepDatabase   = "database"
	StepMigrate    = "migrate"
	StepServices   = "services"
	StepDevClone   = "dev_clone"
	StepDevSetup   = "dev_setup"
	StepDevLocal   = "dev_local"
	StepVerify     = "verify"
	StepDemo       = "demo"
)

// Options are the choices of an installation.
type Options struct {
	Mode        string
	Dir         string
	Project     string
	Ports       ports.Set
	LLMMode     string
	LocalLLM    string
	ReviewModel string
	OpenRouter  string
	Registry    string // "" or LocalRegistry: never pull
	Tag         string
	SourceDir   string // repository root ("" when none was found)
	CloneDir    string // Desenvolver without a checkout: where to clone it
	RepoURL     string // where to clone the code from when the registry lacks the images
	RepoRef     string // branch or tag to clone ("" = the default branch)
	Build       string
	Demo        bool
	Info        platform.Info
}

// Resolved adjusts the options to their mode: a Desenvolver installation without a checkout
// uses the clone folder when an earlier run (or the user) already cloned there.
func (o Options) Resolved() Options {
	if o.Mode == state.ModeDev && !IsSource(o.SourceDir) && IsSource(o.CloneDir) {
		o.SourceDir = o.CloneDir
	}
	return o
}

// CanClone reports whether dir can receive a clone: missing, or an empty folder.
func CanClone(dir string) bool {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return true
	}
	return err == nil && len(entries) == 0
}

// OwnEntries are what the installer writes into an installation folder (src/ is Experimentar's
// clone when it had no code to build from).
var OwnEntries = []string{"compose.yaml", ".env", "state.json", "state.json.tmp", "logs", "src"}

// RemoveInstallation deletes what the installer put in dir, then dir when nothing else is left.
// Anything else (a --dir that pointed at a busy folder) stays and is returned; src/ then stays
// too, since it may not be ours.
func RemoveInstallation(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var left []string
	for _, e := range entries {
		if !slices.Contains(OwnEntries, e.Name()) {
			left = append(left, e.Name())
		}
	}
	for _, name := range OwnEntries {
		p := filepath.Join(dir, name)
		if name == "src" && len(left) > 0 {
			if _, err := os.Stat(p); err == nil {
				left = append(left, name)
			}
			continue
		}
		if err := os.RemoveAll(p); err != nil {
			return left, err
		}
	}
	if len(left) > 0 {
		return left, nil
	}
	return nil, os.Remove(dir)
}

// URLs of a running installation.
type URLs struct {
	Site, App, API, Mailpit string
}

// URLs derives the addresses from the ports (Experimentar) or from the repository's .env
// (Desenvolver, like tools/dev/local.sh).
func (o Options) URLs() URLs {
	if o.Mode == state.ModeDev {
		return DevURLs(o.SourceDir)
	}
	return urlsFor(o.Ports)
}

func urlsFor(p ports.Set) URLs {
	site := "http://localhost:" + strconv.Itoa(p[ports.Web])
	return URLs{
		Site:    site,
		App:     site + "/app",
		API:     "http://localhost:" + strconv.Itoa(p[ports.API]),
		Mailpit: "http://localhost:" + strconv.Itoa(p[ports.Mailpit]),
	}
}

// DevURLs reads DM_PUBLIC_BASE_URL, DM_API_BASE_URL and DM_DEV_MAILPIT_PORT from the
// repository's .env (the defaults of .env.example when there is none).
func DevURLs(source string) URLs {
	u := urlsFor(ports.Defaults())
	env, err := envfile.Read(filepath.Join(source, ".env"))
	if err != nil {
		return u
	}
	if v := strings.TrimRight(env["DM_PUBLIC_BASE_URL"], "/"); v != "" {
		u.Site, u.App = v, v+"/app"
	}
	if v := strings.TrimRight(env["DM_API_BASE_URL"], "/"); v != "" {
		u.API = v
	}
	if v := env["DM_DEV_MAILPIT_PORT"]; v != "" {
		u.Mailpit = "http://localhost:" + v
	}
	return u
}

// DevPorts are the ports `make local` takes: the repository's .env (DM_PUBLIC_BASE_URL,
// DM_API_BASE_URL, DATABASE_URL, DM_DEV_MAILPIT_PORT), the defaults where it says nothing.
func DevPorts(source string) ports.Set {
	p := ports.Defaults()
	env, err := envfile.Read(filepath.Join(source, ".env"))
	if err != nil {
		return p
	}
	fromURL := func(name, raw string) {
		if u, err := url.Parse(strings.TrimSpace(raw)); err == nil {
			if n, err := strconv.Atoi(u.Port()); err == nil {
				p[name] = n
			}
		}
	}
	fromURL(ports.Web, env["DM_PUBLIC_BASE_URL"])
	fromURL(ports.API, env["DM_API_BASE_URL"])
	fromURL(ports.Postgres, env["DATABASE_URL"])
	if n, err := strconv.Atoi(strings.TrimSpace(env["DM_DEV_MAILPIT_PORT"])); err == nil {
		p[ports.Mailpit] = n
	}
	return p
}

// Image is the full name of a docmatch image ("ghcr.io/owner/docmatch-api:0.1.0").
func (o Options) Image(service string) string {
	reg := o.Registry
	if reg == "" {
		reg = LocalRegistry
	}
	return reg + "/docmatch-" + service + ":" + o.Tag
}

// Pullable reports whether the registry can be asked for the images.
func (o Options) Pullable() bool {
	return o.Registry != "" && o.Registry != LocalRegistry && o.Build != BuildAlways
}

// Installer runs one installation.
type Installer struct {
	Opts   Options
	Docker docker.CLI
	State  *state.State

	// Filled by the steps.
	Demo     *demo.Result
	DemoErr  error
	Revision string

	// prevSource is the checkout of the saved state, before these options replaced it (make
	// setup is skipped only when it already ran for the same checkout).
	prevSource string

	mu sync.Mutex
}

// New prepares an installer; st is the saved state (nil for a fresh installation).
func New(opts Options, st *state.State) *Installer {
	if st == nil {
		st = &state.State{}
	}
	prev := st.SourceDir
	st.Mode, st.Project, st.Ports = opts.Mode, opts.Project, opts.Ports.Clone()
	st.LLMMode, st.LocalLLM = opts.LLMMode, opts.LocalLLM
	st.ReviewModel = opts.ReviewModel
	st.Registry, st.Tag, st.SourceDir = opts.Registry, opts.Tag, opts.SourceDir
	return &Installer{Opts: opts, State: st, prevSource: prev}
}

// ComposeFile and EnvFile are the installation's copies.
func (in *Installer) ComposeFile() string { return filepath.Join(in.Opts.Dir, "compose.yaml") }

// EnvFile is the generated .env.
func (in *Installer) EnvFile() string { return filepath.Join(in.Opts.Dir, ".env") }

// LogDir holds the installer's logs.
func (in *Installer) LogDir() string { return filepath.Join(in.Opts.Dir, "logs") }

// Compose is the project's compose runner.
func (in *Installer) Compose() docker.Compose {
	return docker.Compose{CLI: in.Docker, Project: in.Opts.Project, File: in.ComposeFile(), EnvFile: in.EnvFile()}
}

// MarkDone records a finished step in state.json.
func (in *Installer) MarkDone(id string) {
	in.mu.Lock()
	defer in.mu.Unlock()
	in.State.Mark(id)
	_ = in.State.Save(in.Opts.Dir)
}

// Steps lists the steps of the installation for the chosen mode.
func (in *Installer) Steps() []flow.Step {
	t := func(id string) string { return text.T("steps." + id) }
	if in.Opts.Mode == state.ModeDev {
		var steps []flow.Step
		if !IsSource(in.Opts.SourceDir) {
			steps = append(steps, flow.Step{ID: StepDevClone, Title: t(StepDevClone), Run: in.devClone})
		}
		return append(steps,
			flow.Step{ID: StepDevSetup, Title: t(StepDevSetup), Run: in.devSetup},
			flow.Step{ID: StepDevLocal, Title: t(StepDevLocal), Run: in.devLocal},
			flow.Step{ID: StepVerify, Title: t(StepVerify), Run: in.verify},
			flow.Step{ID: StepDemo, Title: t(StepDemo), Run: in.demo},
		)
	}
	return []flow.Step{
		{ID: StepPrepare, Title: t(StepPrepare), Run: in.prepare},
		{ID: StepBaseImages, Title: t(StepBaseImages), Run: in.baseImages},
		{ID: StepImages, Title: t(StepImages), Run: in.images},
		{ID: StepDatabase, Title: t(StepDatabase), Run: in.database},
		{ID: StepMigrate, Title: t(StepMigrate), Run: in.migrate},
		{ID: StepServices, Title: t(StepServices), Run: in.services},
		{ID: StepVerify, Title: t(StepVerify), Run: in.verify},
		{ID: StepDemo, Title: t(StepDemo), Run: in.demo},
	}
}

// Prepare writes the installation folder without running anything (used by `docmatch up`).
func (in *Installer) Prepare() error {
	_, err := in.prepare(context.Background(), flow.NewReporter(StepPrepare, func(flow.Event) {}))
	return err
}

func (in *Installer) prepare(ctx context.Context, r *flow.Reporter) (flow.Result, error) {
	if err := os.MkdirAll(in.LogDir(), 0o700); err != nil {
		return flow.Result{}, err
	}
	if err := os.WriteFile(in.ComposeFile(), assets.Compose, 0o644); err != nil { //nolint:gosec // not secret
		return flow.Result{}, err
	}
	in.Revision = SourceRevision(ctx, in.Opts.SourceDir)
	generated, err := envfile.Write(in.EnvFile(), envfile.Settings{
		Ports: in.Opts.Ports, Registry: in.registryForCompose(), Tag: in.Opts.Tag,
		SourceDir: in.Opts.SourceDir, Revision: in.Revision,
		LLMMode: in.Opts.LLMMode, LocalLLM: in.Opts.LocalLLM, OpenRouter: in.Opts.OpenRouter, ReviewModel: in.Opts.ReviewModel,
	})
	if err != nil {
		return flow.Result{}, err
	}
	if len(generated) > 0 {
		r.Log(text.T("steps.secrets_generated", "names", strings.Join(generated, ", ")))
	}
	if err := in.State.Save(in.Opts.Dir); err != nil {
		return flow.Result{}, err
	}
	return flow.Done(in.Opts.Dir), nil
}

func (in *Installer) registryForCompose() string {
	if in.Opts.Registry == "" {
		return LocalRegistry
	}
	return in.Opts.Registry
}

func pullBars(p docker.PullProgress) []flow.Bar {
	bars := make([]flow.Bar, 0, len(p.Services))
	for _, s := range p.Services {
		b := flow.Bar{Label: s.Name, Fraction: s.Fraction()}
		switch {
		case s.Done:
			b.Text = text.T("steps.ready")
		case s.Total > 0:
			b.Text = platform.HumanBytes(uint64(s.Current)) + "/" + platform.HumanBytes(uint64(s.Total)) //nolint:gosec // sizes are positive
		default:
			b.Fraction = -1
		}
		bars = append(bars, b)
	}
	return bars
}

func (in *Installer) baseImages(ctx context.Context, r *flow.Reporter) (flow.Result, error) {
	err := in.Compose().Pull(ctx, BaseServices, true, func(p docker.PullProgress) { r.Progress(pullBars(p), "") }, r.Log)
	if err != nil {
		return flow.Result{}, err
	}
	return flow.Done(strings.Join(BaseServices, ", ")), nil
}

// imagesPresent reports whether all docmatch images exist locally; with revision set, they
// must also have been built from it.
func (in *Installer) imagesPresent(ctx context.Context, revision string) bool {
	for _, svc := range ImageServices {
		rev, ok := in.Docker.ImageLabel(ctx, in.Opts.Image(svc), "org.opencontainers.image.revision")
		if !ok || (revision != "" && rev != revision) {
			return false
		}
	}
	return true
}

func (in *Installer) imageSizes(ctx context.Context) string {
	parts := make([]string, 0, len(ImageServices))
	for _, svc := range ImageServices {
		if size := in.Docker.ImageSize(ctx, in.Opts.Image(svc)); size != "" {
			parts = append(parts, svc+" "+size)
		}
	}
	return strings.Join(parts, " · ")
}

// ErrNoSource means the images must be built and there is no code to build them from.
var ErrNoSource = errors.New("sem o código da docmatch para compilar as imagens")

func (in *Installer) images(ctx context.Context, r *flow.Reporter) (flow.Result, error) {
	clean := in.Revision != "" && !strings.HasSuffix(in.Revision, "-dirty")
	if in.Opts.Build != BuildAlways {
		if in.State.Images == "built" && clean && in.imagesPresent(ctx, in.Revision) {
			return flow.Skip(text.T("steps.images_present", "revision", in.Revision)), nil
		}
		if in.State.Images == "pulled" && in.imagesPresent(ctx, "") {
			return flow.Skip(text.T("steps.images_pulled_before")), nil
		}
	}
	if in.Opts.Pullable() {
		r.Note(text.T("steps.pulling_from", "registry", in.Opts.Registry))
		err := in.Compose().Pull(ctx, ImageServices, false, func(p docker.PullProgress) { r.Progress(pullBars(p), "") }, r.Log)
		if err == nil {
			in.setImages("pulled", "")
			return flow.Done(text.T("steps.images_pulled", "registry", in.Opts.Registry)), nil
		}
		if ctx.Err() != nil {
			return flow.Result{}, ctx.Err()
		}
		if in.Opts.Build == BuildNever {
			return flow.Result{}, fmt.Errorf("%s: %w", in.Opts.Registry, err)
		}
		r.Log(text.T("steps.pull_failed", "error", err.Error()))
	}
	if in.Opts.SourceDir == "" {
		if err := in.clone(ctx, r); err != nil {
			return flow.Result{}, err
		}
	}
	r.Retitle(text.T("steps.images_build"))
	r.Note(text.T("steps.building_from", "dir", in.Opts.SourceDir))
	err := in.Compose().Build(ctx, ImageServices, func(p docker.BuildProgress) { r.Progress(buildBars(p), "") }, r.Log)
	if err != nil {
		return flow.Result{}, err
	}
	in.setImages("built", in.Revision)
	return flow.Done(in.imageSizes(ctx)), nil
}

func (in *Installer) setImages(how, revision string) {
	in.mu.Lock()
	defer in.mu.Unlock()
	in.State.Images, in.State.Revision = how, revision
	_ = in.State.Save(in.Opts.Dir)
}

func buildBars(p docker.BuildProgress) []flow.Bar {
	bars := make([]flow.Bar, 0, len(p.Services))
	for _, s := range p.Services {
		b := flow.Bar{Label: s.Name, Fraction: s.Fraction(), Text: fmt.Sprintf("%d/%d", s.Done, s.Total)}
		if s.Current != "" {
			cur := s.Current
			if s.TotalBytes > 0 {
				cur = platform.HumanBytes(uint64(s.CurrentBytes)) + "/" + platform.HumanBytes(uint64(s.TotalBytes)) + " " + cur //nolint:gosec // sizes are positive
			}
			b.Text += "  " + cur
		}
		bars = append(bars, b)
	}
	return bars
}

// clone fetches the code when there is none and the registry lacks the images.
func (in *Installer) clone(ctx context.Context, r *flow.Reporter) error {
	if in.Opts.RepoURL == "" {
		return ErrNoSource
	}
	if _, err := exec.LookPath("git"); err != nil {
		return fmt.Errorf("%w (git não encontrado)", ErrNoSource)
	}
	dest := filepath.Join(in.Opts.Dir, "src")
	if IsSource(dest) {
		in.Opts.SourceDir = dest
	} else {
		r.Retitle(text.T("steps.cloning"))
		// A clone interrupted in an earlier run: src/ is the installer's own folder.
		if err := os.RemoveAll(dest); err != nil {
			return err
		}
		args := []string{"clone", "--depth", "1"}
		if in.Opts.RepoRef != "" {
			args = append(args, "--branch", in.Opts.RepoRef)
		}
		cmd := gitCommand(ctx, append(args, in.Opts.RepoURL, dest)...)
		cmd.Env = append(cmd.Env, "GIT_TERMINAL_PROMPT=0")
		if err := streamCmd(cmd, r.Log); err != nil {
			return fmt.Errorf("%w: git clone %s: %w", ErrNoSource, in.Opts.RepoURL, err)
		}
		if !IsSource(dest) {
			return fmt.Errorf("%w: %s não tem deploy/local (versão antiga do código?)", ErrNoSource, in.Opts.RepoURL)
		}
		in.Opts.SourceDir = dest
	}
	in.State.SourceDir = dest
	in.Revision = SourceRevision(ctx, dest)
	// The .env must point the builds at the new checkout.
	_, err := envfile.Write(in.EnvFile(), envfile.Settings{
		Ports: in.Opts.Ports, Registry: in.registryForCompose(), Tag: in.Opts.Tag,
		SourceDir: dest, Revision: in.Revision,
		LLMMode: in.Opts.LLMMode, LocalLLM: in.Opts.LocalLLM, OpenRouter: in.Opts.OpenRouter, ReviewModel: in.Opts.ReviewModel,
	})
	return err
}

func (in *Installer) database(ctx context.Context, r *flow.Reporter) (flow.Result, error) {
	if err := in.Compose().Up(ctx, BaseServices, in.statusLog(r)); err != nil {
		return flow.Result{}, err
	}
	return flow.Done(fmt.Sprintf("Postgres :%d · Mailpit :%d", in.Opts.Ports[ports.Postgres], in.Opts.Ports[ports.Mailpit])), nil
}

func (in *Installer) migrate(ctx context.Context, r *flow.Reporter) (flow.Result, error) {
	if err := in.Compose().RunOnce(ctx, "migrate", r.Log); err != nil {
		return flow.Result{}, err
	}
	return flow.Done(""), nil
}

func (in *Installer) services(ctx context.Context, r *flow.Reporter) (flow.Result, error) {
	if err := in.Compose().Up(ctx, AppServices, in.statusLog(r)); err != nil {
		return flow.Result{}, err
	}
	return flow.Done(strings.Join(AppServices, ", ")), nil
}

// statusLog sends compose's lines to the log and shows the latest container status as the note
// ("Container docmatch-local-api-1  Healthy" -> "api healthy").
func (in *Installer) statusLog(r *flow.Reporter) func(string) {
	return func(line string) {
		r.Log(line)
		if f := strings.Fields(line); len(f) >= 3 && f[0] == "Container" {
			r.Note(ServiceOf(in.Opts.Project, f[1]) + " " + strings.ToLower(f[len(f)-1]))
		}
	}
}

// ServiceOf turns a container name ("<project>-api-1") into its service ("api").
func ServiceOf(project, container string) string {
	s := strings.TrimPrefix(container, project+"-")
	if i := strings.LastIndex(s, "-"); i > 0 {
		if _, err := strconv.Atoi(s[i+1:]); err == nil {
			s = s[:i]
		}
	}
	return s
}

// WaitHTTP polls url until it answers 2xx or the timeout passes.
func WaitHTTP(ctx context.Context, url string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 5 * time.Second}
	var last error
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		res, err := client.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, res.Body)
			_ = res.Body.Close()
			if res.StatusCode < 300 {
				return nil
			}
			err = fmt.Errorf("%s respondeu %d", url, res.StatusCode)
		}
		last = err
		if time.Now().After(deadline) {
			return last
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func (in *Installer) verify(ctx context.Context, r *flow.Reporter) (flow.Result, error) {
	u := in.Opts.URLs()
	r.Note(u.API)
	c := demo.New(u.API, u.Site)
	if err := WaitHTTP(ctx, u.API+"/v1/version", 60*time.Second); err != nil {
		return flow.Result{}, err
	}
	v, err := c.Version(ctx)
	if err != nil {
		return flow.Result{}, err
	}
	r.Note(u.Site)
	if err := WaitHTTP(ctx, u.Site+"/", 60*time.Second); err != nil {
		return flow.Result{}, err
	}
	return flow.Done(text.T("steps.verified", "version", v.Version)), nil
}

func (in *Installer) demo(ctx context.Context, r *flow.Reporter) (flow.Result, error) {
	if !in.Opts.Demo {
		return flow.Skip(text.T("steps.demo_skipped")), nil
	}
	u := in.Opts.URLs()
	res, err := demo.New(u.API, u.Site).Twin(ctx, func(p demo.Progress) {
		switch p.Phase {
		case "upload", "estimate":
			r.Progress([]flow.Bar{{Label: text.T("steps.demo_files"), Fraction: float64(p.Done) / float64(max(p.Total, 1)), Text: fmt.Sprintf("%d/%d", p.Done, p.Total)}}, "")
		case "run", "findings":
			note := p.Message
			if note == "" {
				note = text.T("steps.demo_running")
			}
			r.Progress([]flow.Bar{{Label: text.T("steps.demo_run"), Fraction: float64(p.Percent) / 100, Text: fmt.Sprintf("%d%%", p.Percent)}}, note)
		default:
			r.Note(text.T("steps.demo_login"))
		}
	})
	if err != nil {
		if ctx.Err() != nil {
			return flow.Result{}, ctx.Err()
		}
		in.DemoErr = err
		return flow.Warn(err.Error()), nil
	}
	in.Demo = res
	return flow.Done(text.T("payoff.findings", "count", res.Findings, "amount", demo.FormatMoney(res.AmountAtRisk, res.Currency))), nil
}

// DevEnv is the environment of `make setup` and `make local`: this process's (with the PATH the
// tools screen may have extended) plus goToolchain as GOTOOLCHAIN unless the user chose one, so a
// Go older than the go.mod downloads the pinned toolchain even where the distribution's go.env
// says local (toolchain.GoToolchainFor).
func DevEnv(environ []string, goToolchain string) []string {
	out := append([]string{}, environ...)
	for _, kv := range environ {
		if strings.HasPrefix(kv, "GOTOOLCHAIN=") && kv != "GOTOOLCHAIN=" {
			return out
		}
	}
	if goToolchain == "" {
		goToolchain = "auto"
	}
	return append(out, "GOTOOLCHAIN="+goToolchain)
}

// DevEnviron is DevEnv for this checkout and this machine's Go: what every make of the
// Desenvolver mode runs with (the installation, docmatch up, down and uninstall).
func (in *Installer) DevEnviron(ctx context.Context) []string {
	local, _ := toolchain.System{}.Output(ctx, "", "go", "env", "GOVERSION")
	env := DevEnv(os.Environ(), toolchain.GoToolchainFor(firstLine(local), toolchain.ReadRequirements(in.Opts.SourceDir)))
	return ModelEnv(env, in.Opts)
}

// ModelEnv carries the wizard's provider choices into make local. Keys stay in the
// child environment, never in command arguments or state.json.
func ModelEnv(environ []string, opts Options) []string {
	values := map[string]string{"DM_LOCAL_LLM_MODE": opts.LLMMode}
	if opts.LLMMode == envfile.LLMLocal {
		values["DM_LOCAL_LLM_BASE_URL"] = opts.LocalLLM
		values["DM_MODEL_EXTRACTION_LOCAL"] = opts.ReviewModel
		values["DM_LOCAL_LLM_REASONING_EFFORT"] = "none"
		for _, item := range environ {
			if value, ok := strings.CutPrefix(item, "DM_LOCAL_LLM_REASONING_EFFORT="); ok {
				values["DM_LOCAL_LLM_REASONING_EFFORT"] = value
			}
		}
	}
	if opts.LLMMode == envfile.LLMOpenRouter {
		if opts.OpenRouter != "" {
			values["OPENROUTER_API_KEY"] = opts.OpenRouter
		}
		if opts.ReviewModel != "" {
			values["DM_MODEL_EXTRACTION_PRIMARY"] = opts.ReviewModel
		}
	}
	out := make([]string, 0, len(environ)+len(values))
	for _, item := range environ {
		key, _, _ := strings.Cut(item, "=")
		if _, replaced := values[key]; !replaced {
			out = append(out, item)
		}
	}
	for key, value := range values {
		out = append(out, key+"="+value)
	}
	return out
}

func (in *Installer) devEnv(ctx context.Context) []string { return in.DevEnviron(ctx) }

func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	return s
}

// devClone clones the repository for Desenvolver (a full clone: it is a working copy).
func (in *Installer) devClone(ctx context.Context, r *flow.Reporter) (flow.Result, error) {
	dest := in.Opts.CloneDir
	if dest == "" {
		return flow.Result{}, ErrNoSource
	}
	if in.Opts.RepoURL == "" {
		return flow.Result{}, fmt.Errorf("%w: %s", ErrNoSource, text.T("steps.clone_no_repo"))
	}
	if !CanClone(dest) {
		return flow.Result{}, errors.New(text.T("steps.clone_not_empty", "dir", dest))
	}
	_, statErr := os.Stat(dest)
	created := errors.Is(statErr, os.ErrNotExist)
	r.Note(in.Opts.RepoURL + " -> " + dest)
	cmd := gitCommand(ctx, "clone", "--progress", "--", in.Opts.RepoURL, dest)
	cmd.Env = append(cmd.Env, "GIT_TERMINAL_PROMPT=0")
	if err := streamCmd(cmd, cloneProgress(r)); err != nil {
		// A half clone would block the next try; only what this step created goes.
		if created {
			_ = os.RemoveAll(dest)
		} else if entries, _ := os.ReadDir(dest); len(entries) > 0 {
			for _, e := range entries {
				_ = os.RemoveAll(filepath.Join(dest, e.Name()))
			}
		}
		return flow.Result{}, fmt.Errorf("%s: %w. %s", in.Opts.RepoURL, err, text.T("steps.clone_hint"))
	}
	if !IsSource(dest) {
		return flow.Result{}, fmt.Errorf("%w: %s", ErrNoSource, dest)
	}
	in.mu.Lock()
	in.Opts.SourceDir = dest
	in.State.SourceDir = dest
	_ = in.State.Save(in.Opts.Dir)
	in.mu.Unlock()
	return flow.Done(dest), nil
}

var gitPhase = regexp.MustCompile(`^(?:remote: )?(Counting objects|Compressing objects|Receiving objects|Resolving deltas|Updating files):\s+(\d+)%`)

// cloneProgress turns the progress lines of the clone ("Receiving objects:  45% (..)") into a
// bar; everything else goes to the log.
func cloneProgress(r *flow.Reporter) func(string) {
	labels := map[string]string{"Receiving objects": "objects", "Resolving deltas": "deltas", "Updating files": "files"}
	return func(line string) {
		m := gitPhase.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			r.Log(line)
			return
		}
		key, ok := labels[m[1]]
		if !ok {
			return
		}
		pct, _ := strconv.Atoi(m[2])
		r.Progress([]flow.Bar{{Label: text.T("steps.clone_" + key), Fraction: float64(pct) / 100, Text: m[2] + "%"}}, "")
		if pct == 100 {
			r.Log(strings.TrimSpace(line))
		}
	}
}

// enteringDir is make's "Entering directory" line: GNU make 4 quotes '...', the make 3.81 of
// macOS `...' (make runs with LC_ALL=C, so the words stay English).
var enteringDir = regexp.MustCompile("^make(?:\\[\\d+\\])?: Entering directory [`'](.+)'$")

// Subsystems reads a list variable of the root Makefile (SUBSYSTEMS: what `make setup` walks
// through).
func Subsystems(source string) []string { return makeList(source, "SUBSYSTEMS") }

func makeList(source, name string) []string {
	data, err := os.ReadFile(filepath.Join(source, "Makefile")) //nolint:gosec // the checkout
	if err != nil {
		return nil
	}
	re := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(name) + `\s*:?=((?:.*\\\n)*.*)$`)
	m := re.FindSubmatch(data)
	if m == nil {
		return nil
	}
	return strings.Fields(strings.ReplaceAll(string(m[1]), "\\\n", " "))
}

// SetupTarget is what the Desenvolver mode prepares the checkout with: `make setup-local` (the
// hooks and only the subsystems make local builds, LOCAL_SUBSYSTEMS) when the Makefile has it,
// else `make setup` (every subsystem) in older checkouts.
func SetupTarget(source string) (string, []string) {
	data, _ := os.ReadFile(filepath.Join(source, "Makefile")) //nolint:gosec // the checkout
	if regexp.MustCompile(`(?m)^setup-local:`).Match(data) {
		return "setup-local", makeList(source, "LOCAL_SUBSYSTEMS")
	}
	return "setup", Subsystems(source)
}

// devSetup runs SetupTarget (the git hooks and the subsystems' tools), one bar step per
// subsystem. A resumed installation of the same checkout skips it.
func (in *Installer) devSetup(ctx context.Context, r *flow.Reporter) (flow.Result, error) {
	if in.State.Done(StepDevSetup) && in.prevSource == in.Opts.SourceDir {
		return flow.Skip(text.T("steps.setup_done_before")), nil
	}
	target, subs := SetupTarget(in.Opts.SourceDir)
	index := map[string]int{}
	for i, s := range subs {
		index[s] = i
	}
	// make prints physical paths (macOS's /var is /private/var): compare them resolved.
	root := realPath(in.Opts.SourceDir)
	cmd := exec.CommandContext(ctx, "make", target) //nolint:gosec // fixed command
	cmd.Dir = in.Opts.SourceDir
	// English messages: the progress reads make's "Entering directory".
	cmd.Env = append(in.devEnv(ctx), "LC_ALL=C", "LANGUAGE=")
	err := streamCmd(cmd, func(line string) {
		r.Log(line)
		m := enteringDir.FindStringSubmatch(line)
		if m == nil {
			return
		}
		rel, err := filepath.Rel(root, realPath(m[1]))
		if err != nil {
			return
		}
		rel = filepath.ToSlash(rel)
		if i, ok := index[rel]; ok {
			r.Progress([]flow.Bar{{Label: "make " + target, Fraction: float64(i) / float64(len(subs)), Text: fmt.Sprintf("%d/%d %s", i+1, len(subs), rel)}}, rel)
		}
	})
	if err != nil {
		return flow.Result{}, err
	}
	return flow.Done(text.T("steps.setup_done", "count", len(subs))), nil
}

// realPath resolves symlinks (the cleaned path when it cannot).
func realPath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

// devLocal runs `make local` in the repository (Desenvolver mode).
func (in *Installer) devLocal(ctx context.Context, r *flow.Reporter) (flow.Result, error) {
	cmd := exec.CommandContext(ctx, "make", "local") //nolint:gosec // fixed command
	cmd.Dir = in.Opts.SourceDir
	cmd.Env = in.devEnv(ctx)
	err := streamCmd(cmd, func(line string) {
		r.Log(line)
		if strings.HasPrefix(line, "==> ") {
			r.Note(strings.TrimPrefix(line, "==> "))
		}
	})
	if err != nil {
		return flow.Result{}, err
	}
	return flow.Done(in.Opts.SourceDir), nil
}

// streamCmd runs cmd sending stdout and stderr lines to onLine.
func streamCmd(cmd *exec.Cmd, onLine func(string)) error {
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	if err := cmd.Start(); err != nil {
		return err
	}
	var last string
	done := make(chan struct{})
	go func() {
		defer close(done)
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
		sc.Split(ScanLinesCR)
		for sc.Scan() {
			if s := strings.TrimSpace(sc.Text()); s != "" {
				last = s
			}
			onLine(sc.Text())
		}
	}()
	err := cmd.Wait()
	_ = pw.Close()
	<-done
	if err != nil && last != "" {
		return fmt.Errorf("%s (%w)", last, err)
	}
	return err
}

// IsSource reports whether dir is a docmatch repository root.
func IsSource(dir string) bool {
	if dir == "" {
		return false
	}
	for _, f := range []string{"deploy/local/compose.yaml", "services/api/go.mod", "services/harness/Cargo.toml"} {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(f))); err != nil {
			return false
		}
	}
	return true
}

// FindSource walks up from each start until it finds a repository root.
func FindSource(starts ...string) string {
	for _, start := range starts {
		if start == "" {
			continue
		}
		dir, err := filepath.Abs(start)
		if err != nil {
			continue
		}
		for {
			if IsSource(dir) {
				return dir
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}
	return ""
}

// gitLocationVars point git at a repository regardless of -C (git sets them for hooks): they
// would make the installer read, or clone over, the wrong repository.
var gitLocationVars = []string{"GIT_DIR=", "GIT_WORK_TREE=", "GIT_INDEX_FILE=", "GIT_COMMON_DIR=", "GIT_PREFIX=", "GIT_OBJECT_DIRECTORY="}

// gitCommand runs git without the location variables of the environment.
func gitCommand(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", args...) //nolint:gosec // our own arguments
	for _, kv := range os.Environ() {
		keep := true
		for _, prefix := range gitLocationVars {
			if strings.HasPrefix(kv, prefix) {
				keep = false
				break
			}
		}
		if keep {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	return cmd
}

// SourceRevision is the short commit of the checkout, with "-dirty" when it has local changes;
// "" without git or outside a checkout.
func SourceRevision(ctx context.Context, dir string) string {
	if dir == "" {
		return ""
	}
	out, err := gitCommand(ctx, "-C", dir, "rev-parse", "--short=12", "HEAD").Output()
	if err != nil {
		return ""
	}
	rev := strings.TrimSpace(string(out))
	status, err := gitCommand(ctx, "-C", dir, "status", "--porcelain", "--untracked-files=no").Output()
	if err == nil && strings.TrimSpace(string(status)) != "" {
		rev += "-dirty"
	}
	return rev
}

// parseRemote splits a git remote (URL or scp-like "git@host:owner/repo") into host, owner and
// repository name.
func parseRemote(remote string) (host, owner, repo string) {
	r := strings.TrimSuffix(strings.TrimSpace(remote), ".git")
	var path string
	if u, err := url.Parse(r); err == nil && u.Host != "" {
		host, path = u.Hostname(), strings.TrimPrefix(u.Path, "/")
	} else if at := strings.Index(r, "@"); at >= 0 {
		h, p, ok := strings.Cut(r[at+1:], ":")
		if !ok {
			return "", "", ""
		}
		host, path = h, p
	} else {
		return "", "", ""
	}
	owner, repo, ok := strings.Cut(path, "/")
	if !ok || owner == "" || repo == "" || strings.Contains(repo, "/") {
		return "", "", ""
	}
	return strings.ToLower(host), owner, repo
}

// RegistryFromRemote derives the GHCR namespace from a GitHub remote (owner -> "ghcr.io/owner");
// "" for other hosts.
func RegistryFromRemote(remote string) string {
	host, owner, _ := parseRemote(remote)
	if host != "github.com" {
		return ""
	}
	return "ghcr.io/" + strings.ToLower(owner)
}

// CloneURL is the https address of a GitHub remote ("" for other hosts), what `git clone` uses
// when the installer runs without the code.
func CloneURL(remote string) string {
	host, owner, repo := parseRemote(remote)
	if host != "github.com" {
		return ""
	}
	return (&url.URL{Scheme: "https", Host: host, Path: "/" + owner + "/" + repo}).String()
}

// ScanLinesCR splits like bufio.ScanLines but also at a lone \r: progress lines rewritten in
// place (the clone's progress, curl) arrive one by one instead of as a single huge line.
func ScanLinesCR(data []byte, atEOF bool) (int, []byte, error) {
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}
	if i := bytes.IndexAny(data, "\r\n"); i >= 0 {
		advance := i + 1
		if data[i] == '\r' {
			switch {
			case i+1 < len(data) && data[i+1] == '\n':
				advance++
			case i+1 == len(data) && !atEOF:
				return 0, nil, nil // a \n may follow
			}
		}
		return advance, data[:i], nil
	}
	if atEOF {
		return len(data), data, nil
	}
	return 0, nil, nil
}

// Remote is the origin remote of the checkout ("" when unknown).
func Remote(ctx context.Context, dir string) string {
	if dir == "" {
		return ""
	}
	out, err := gitCommand(ctx, "-C", dir, "remote", "get-url", "origin").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
