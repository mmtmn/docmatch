package toolchain

import (
	"bufio"
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/mmtmn/docmatch/tools/installer/internal/docker"
	"github.com/mmtmn/docmatch/tools/installer/internal/text"
)

// Package managers.
const (
	Apt    = "apt"
	Dnf    = "dnf"
	Brew   = "brew"
	Winget = "winget"
)

// Manager is the platform's package manager ("" ID when there is none).
type Manager struct {
	ID   string
	Name string // as shown: "apt", "dnf", "Homebrew", "winget"
	Bin  string // its path
}

// Manager finds the package manager of this platform: winget on Windows, Homebrew on macOS, apt
// (Debian, Ubuntu, WSL) or dnf (Fedora, RHEL) on Linux.
func (e Env) Manager() Manager {
	r := e.runner()
	try := func(id, name string, bins ...string) (Manager, bool) {
		for _, b := range bins {
			if p, err := r.LookPath(b); err == nil {
				return Manager{ID: id, Name: name, Bin: p}, true
			}
		}
		return Manager{}, false
	}
	switch e.Info.OS {
	case "windows":
		m, _ := try(Winget, "winget", "winget")
		return m
	case "darwin":
		m, _ := try(Brew, "Homebrew", "brew")
		return m
	default:
		if m, ok := try(Apt, "apt", "apt-get"); ok {
			return m
		}
		m, _ := try(Dnf, "dnf", "dnf")
		return m
	}
}

// Command is one step of a plan, exactly as it runs.
type Command struct {
	Args     []string
	Tools    []string // what it installs (tool ids)
	Elevated bool     // needs sudo or administrator; sudo/UAC asks by itself
}

// String is the command as a shell would show it.
func (c Command) String() string {
	parts := make([]string, len(c.Args))
	for i, a := range c.Args {
		parts[i] = quote(a)
	}
	return strings.Join(parts, " ")
}

func quote(s string) string {
	// A control character (a newline, an escape) would make the line on screen differ from the
	// argument that runs: those go as $'...' with every such byte spelled out.
	if strings.IndexFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
		var b strings.Builder
		b.WriteString("$'")
		for _, c := range []byte(s) {
			switch {
			case c == '\\' || c == '\'':
				b.WriteByte('\\')
				b.WriteByte(c)
			case c < 0x20 || c == 0x7f:
				fmt.Fprintf(&b, "\\x%02x", c)
			default:
				b.WriteByte(c)
			}
		}
		b.WriteString("'")
		return b.String()
	}
	if s != "" && !strings.ContainsAny(s, " \t\"'$`\\|&;<>()*?[]{}!#~") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// Manual is a tool the plan cannot install, with how to do it by hand.
type Manual struct {
	Tool string
	Fix  string
}

// Plan installs what is missing.
type Plan struct {
	Manager  Manager
	Commands []Command
	Manual   []Manual
	Notes    []string // after the commands (log in again for the docker group...)
	// PathFirst are folders that must come first in PATH once the plan ran: they hold the
	// replacement of a tool an older copy would otherwise shadow (snap's Node over an apt Node
	// 18 in /usr/bin, the pnpm installed into ~/.local).
	PathFirst []string
}

// Empty reports a plan with nothing to run.
func (p Plan) Empty() bool { return len(p.Commands) == 0 }

// Elevated counts the commands that need sudo or administrator.
func (p Plan) Elevated() int {
	n := 0
	for _, c := range p.Commands {
		if c.Elevated {
			n++
		}
	}
	return n
}

// planner accumulates a plan.
type planner struct {
	e        Env
	ctx      context.Context
	req      Requirements
	plan     Plan
	pkgs     []string // one install command for these
	pkgTools []string
	after    []Command // after the packages, in order
	canSudo  bool
}

func (p *planner) pkg(tool string, names ...string) {
	for _, n := range names {
		if !contains(p.pkgs, n) {
			p.pkgs = append(p.pkgs, n)
		}
	}
	if !contains(p.pkgTools, tool) {
		p.pkgTools = append(p.pkgTools, tool)
	}
}

func (p *planner) manual(tool, fix string) {
	p.plan.Manual = append(p.plan.Manual, Manual{Tool: tool, Fix: fix})
}

// root is a command that needs root: sudo in front unless already root. Without sudo it
// becomes a manual step ("as root: ...").
func (p *planner) root(tool string, args ...string) (Command, bool) {
	if p.e.Root {
		return Command{Args: args, Tools: []string{tool}, Elevated: true}, true
	}
	if !p.canSudo {
		p.manual(tool, text.T("tools.fix_as_root", "cmd", Command{Args: args}.String()))
		return Command{}, false
	}
	return Command{Args: append([]string{"sudo"}, args...), Tools: []string{tool}, Elevated: true}, true
}

func (p *planner) first(dir string) {
	if !contains(p.plan.PathFirst, dir) {
		p.plan.PathFirst = append(p.plan.PathFirst, dir)
	}
}

// aptListsReady reports whether apt has package lists to answer from.
func (e Env) aptListsReady() bool {
	glob := e.Glob
	if glob == nil {
		glob = filepath.Glob
	}
	lists, _ := glob("/var/lib/apt/lists/*_Packages*")
	return len(lists) > 0
}

// needsPackages reports whether some missing tool would come from the distribution's packages.
func needsPackages(need map[string]Status, wsl bool) bool {
	for id, s := range need {
		switch {
		case id == PNPM, id == Rust && s.Reason == "rustup_no_toolchain", id == Docker && wsl:
		default:
			return true
		}
	}
	return false
}

func (p *planner) then(c Command, ok bool) {
	if ok {
		p.after = append(p.after, c)
	}
}

func (p *planner) user(tool string, args ...string) Command {
	return Command{Args: args, Tools: []string{tool}}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// PlanFor turns the tools that are not ready into commands for this platform's package manager,
// and what it cannot install into manual steps.
func (e Env) PlanFor(ctx context.Context, statuses []Status, req Requirements) Plan {
	p := &planner{e: e, ctx: ctx, req: req}
	p.plan.Manager = e.Manager()
	_, sudoErr := e.runner().LookPath("sudo")
	p.canSudo = sudoErr == nil
	need := map[string]Status{}
	var order []string
	for _, s := range statuses {
		if !s.OK() {
			need[s.ID] = s
			order = append(order, s.ID)
		}
	}
	if len(order) == 0 {
		return p.plan
	}
	// Problems no package solves.
	if s, ok := need[Go]; ok && s.Reason == "gotoolchain_local" {
		p.manual(Go, text.T("tools.fix_gotoolchain", "want", req.GoToolchain))
		delete(need, Go)
	}
	switch p.plan.Manager.ID {
	case Apt, Dnf:
		p.linux(need, order)
	case Brew:
		p.brew(need, order)
	case Winget:
		p.winget(need, order)
	default:
		for _, id := range order {
			if _, ok := need[id]; ok {
				p.manual(id, e.ManualFix(id))
			}
		}
	}
	return p.plan
}

// ManualFix is how to install a tool by hand on this platform.
func (e Env) ManualFix(id string) string {
	switch {
	case id == WSL:
		return text.T("tools.fix_wsl")
	case id == Docker && e.Info.OS == "windows":
		return text.T("preflight.fix_docker_windows")
	case id == Docker && e.Info.WSL:
		return text.T("preflight.fix_docker_wsl")
	case id == Docker && e.Info.OS == "darwin":
		return text.T("preflight.fix_docker_mac")
	case id == Docker:
		return text.T("preflight.fix_docker_linux")
	case (id == Make || id == CC || id == Git) && e.Info.OS == "darwin":
		return text.T("tools.fix_xcode")
	case e.Info.OS == "darwin" && e.Manager().ID == "":
		return text.T("tools.fix_brew", "tool", Name(id))
	case id == Git:
		return text.T("preflight.fix_git")
	}
	return text.T("tools.fix." + id)
}

// candidates asks the package manager which version of each package it would install ("" =
// none). known is false when it knows nothing at all (empty apt lists before an update): then
// every package is assumed available and the check after the installation has the last word.
func (p *planner) candidates(pkgs ...string) (map[string]string, bool) {
	r := p.e.runner()
	out := map[string]string{}
	switch p.plan.Manager.ID {
	case Apt:
		res, err := r.Output(p.ctx, "", "apt-cache", append([]string{"policy"}, pkgs...)...)
		if err != nil {
			return out, false
		}
		ParseAptPolicy(res, out)
	case Dnf:
		args := append([]string{"repoquery", "--quiet", "--latest-limit", "1", "--queryformat", "%{name} %{version}\n"}, pkgs...)
		res, err := r.Output(p.ctx, "", "dnf", args...)
		if err != nil {
			return out, false
		}
		sc := bufio.NewScanner(strings.NewReader(res))
		for sc.Scan() {
			if f := strings.Fields(sc.Text()); len(f) == 2 && contains(pkgs, f[0]) {
				out[f[0]] = f[1]
			}
		}
	default:
		return out, false
	}
	// With lists (checked before planning), a package missing from the answer does not exist.
	return out, true
}

// ParseAptPolicy reads `apt-cache policy` into package -> candidate version (without the epoch
// and the Debian revision noise; packages without a candidate are left out).
func ParseAptPolicy(res string, out map[string]string) {
	pkg := ""
	sc := bufio.NewScanner(strings.NewReader(res))
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, " ") && strings.HasSuffix(line, ":") {
			pkg = strings.TrimSuffix(line, ":")
			continue
		}
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "Candidate:"); ok && pkg != "" {
			v = strings.TrimSpace(v)
			if v == "" || v == "(none)" {
				continue
			}
			if _, rest, ok := strings.Cut(v, ":"); ok { // epoch
				v = rest
			}
			out[pkg] = v
		}
	}
}

// pick is the first package whose candidate satisfies minimum (any version when minimum is "").
func (p *planner) pick(minimum string, names ...string) (string, bool) {
	cand, known := p.candidates(names...)
	if !known {
		return names[0], true
	}
	for _, n := range names {
		v, ok := cand[n]
		if !ok {
			continue
		}
		if minimum == "" || docker.AtLeast(versionRe.FindString(v), minimum) {
			return n, true
		}
	}
	return "", false
}

func (p *planner) linux(need map[string]Status, order []string) {
	e := p.e
	apt := p.plan.Manager.ID == Apt
	// Empty apt lists (a fresh WSL or container) say nothing about what apt would install: the
	// first plan only refreshes them, and the next check plans with what they hold.
	if apt && !e.aptListsReady() && needsPackages(need, e.Info.WSL) {
		if c, ok := p.root(order[0], "apt-get", "update"); ok {
			p.plan.Commands = []Command{c}
		}
		p.plan.Notes = append(p.plan.Notes, text.T("tools.note_apt_lists"))
		return
	}
	names := func(a, d string) string {
		if apt {
			return a
		}
		return d
	}
	nodeVia := "" // "pkg", "snap" or "": how Node arrives (pnpm needs npm afterwards)
	for _, id := range order {
		s, ok := need[id]
		if !ok {
			continue
		}
		switch id {
		case Git, OpenSSL, Curl, Make:
			p.pkg(id, id)
		case CC:
			p.pkg(id, names("build-essential", "gcc"))
		case Go:
			if pkg, ok := p.pick(MinGoAuto, names("golang-go", "golang")); ok {
				p.pkg(id, pkg)
			} else {
				p.manual(id, e.ManualFix(id))
			}
		case Rust:
			if s.Reason == "rustup_no_toolchain" {
				p.after = append(p.after, p.user(id, "rustup", "default", "stable"))
				continue
			}
			// An old cargo from the distribution conflicts with its rustup package: swapping
			// them removes packages, which the user decides by hand.
			if _, ok := p.pick("", "rustup"); !ok || s.State == Outdated {
				p.manual(id, e.ManualFix(id))
				continue
			}
			p.pkg(id, "rustup")
			if apt {
				p.after = append(p.after, p.user(id, "rustup", "default", "stable"))
			} else {
				p.after = append(p.after, p.user(id, "rustup-init", "-y", "--no-modify-path", "--default-toolchain", "stable"))
			}
		case Node:
			candidates := []string{"nodejs"}
			if !apt {
				candidates = append(candidates, "nodejs"+p.req.NodeMajor)
			}
			if pkg, ok := p.pick(p.req.Node, candidates...); ok {
				p.pkg(id, pkg)
				nodeVia = "pkg"
				// Debian and Ubuntu ship npm apart (NodeSource's nodejs already carries it).
				cand, _ := p.candidates(pkg, "npm")
				if _, hasNPM := cand["npm"]; hasNPM && !strings.Contains(cand[pkg], "nodesource") {
					p.pkg(id, "npm")
				}
			} else if _, err := e.runner().LookPath("snap"); err == nil {
				c, ok := p.root(id, "snap", "install", "node", "--classic", "--channel="+p.req.NodeMajor+"/stable")
				p.then(c, ok)
				nodeVia = "snap"
				p.first("/snap/bin") // after /usr/bin in Ubuntu's PATH, where the old Node is
			} else {
				p.manual(id, text.T("tools.fix_node_old", "want", p.req.Node))
			}
		case PNPM:
			p.pnpm(nodeVia != "")
		case Docker:
			p.linuxDocker(s)
		}
	}
	var cmds []Command
	if len(p.pkgs) > 0 {
		if apt {
			if c, ok := p.root(p.pkgTools[0], "apt-get", "update"); ok {
				c.Tools = append([]string{}, p.pkgTools...)
				cmds = append(cmds, c)
			}
			if c, ok := p.root(p.pkgTools[0], append([]string{"apt-get", "install", "-y", "--no-remove"}, p.pkgs...)...); ok {
				c.Tools = append([]string{}, p.pkgTools...)
				cmds = append(cmds, c)
			}
		} else if c, ok := p.root(p.pkgTools[0], append([]string{"dnf", "install", "-y"}, p.pkgs...)...); ok {
			c.Tools = append([]string{}, p.pkgTools...)
			cmds = append(cmds, c)
		}
	}
	cmds = append(cmds, p.after...)
	p.plan.Commands = cmds
}

// pnpm goes through npm into ~/.local (no sudo, the version of packageManager); corepack is the
// fallback when there is no npm (it left Node in version 25).
func (p *planner) pnpm(nodeComing bool) {
	r := p.e.runner()
	version := "pnpm"
	if p.req.PNPM != "" {
		version += "@" + p.req.PNPM
	}
	local := p.e.join(p.e.Home, ".local")
	if _, err := r.LookPath("npm"); err == nil || nodeComing {
		p.after = append(p.after, p.user(PNPM, "npm", "install", "--global", "--prefix", local, version))
		p.first(p.e.join(local, "bin"))
		return
	}
	if _, err := r.LookPath("corepack"); err == nil {
		p.after = append(p.after,
			p.user(PNPM, "corepack", "enable", "--install-directory", p.e.join(local, "bin")),
			p.user(PNPM, "corepack", "prepare", version, "--activate"))
		p.first(p.e.join(local, "bin"))
		return
	}
	p.manual(PNPM, p.e.ManualFix(PNPM))
}

func (p *planner) linuxDocker(s Status) {
	e := p.e
	if e.Info.WSL {
		// Docker Desktop's WSL integration is the usual way; a second engine inside WSL fights it.
		p.manual(Docker, e.ManualFix(Docker))
		return
	}
	apt := p.plan.Manager.ID == Apt
	compose, ok := p.pick(MinCompose, "docker-compose-v2", "docker-compose", "docker-compose-plugin")
	if !apt {
		compose, ok = p.pick(MinCompose, "docker-compose", "docker-compose-plugin")
	}
	if s.Reason == "no_compose" {
		if ok {
			p.pkg(Docker, compose)
		} else {
			p.manual(Docker, text.T("preflight.fix_compose"))
		}
		return
	}
	engine := "docker.io"
	if !apt {
		engine = "moby-engine"
	}
	p.pkg(Docker, engine)
	if ok {
		p.pkg(Docker, compose)
	} else {
		p.manual(Docker, text.T("preflight.fix_compose"))
	}
	if _, err := e.runner().LookPath("systemctl"); err == nil {
		p.then(p.root(Docker, "systemctl", "enable", "--now", "docker"))
	}
	if !e.Root && e.User != "" {
		p.then(p.root(Docker, "usermod", "-aG", "docker", e.User))
		p.plan.Notes = append(p.plan.Notes, text.T("tools.note_docker_group"))
	}
}

func (p *planner) brew(need map[string]Status, order []string) {
	var clt, casks []Command
	nodeComing := false
	for _, id := range order {
		s, ok := need[id]
		if !ok {
			continue
		}
		switch id {
		case Make, CC:
			if len(clt) == 0 {
				// Opens the system's dialog; Homebrew itself needs these tools, so it goes first.
				clt = append(clt, p.user(id, "xcode-select", "--install"))
				p.plan.Notes = append(p.plan.Notes, text.T("tools.note_xcode"))
			}
		case Git, Go, Curl:
			p.pkg(id, id)
		case OpenSSL:
			p.pkg(id, "openssl@3")
		case Rust:
			if s.Reason != "rustup_no_toolchain" {
				p.pkg(id, "rustup")
			}
			p.after = append(p.after, p.user(id, "rustup", "default", "stable"))
		case Node:
			p.pkg(id, "node@"+p.req.NodeMajor)
			nodeComing = true
		case PNPM:
			p.pnpm(nodeComing)
		case Docker:
			if s.Reason == "no_compose" {
				p.manual(id, text.T("preflight.fix_compose"))
				continue
			}
			// Docker Desktop: the cask may ask for the password to install its helper.
			casks = append(casks, Command{Args: []string{"brew", "install", "--cask", "docker"}, Tools: []string{id}, Elevated: true},
				p.user(id, "open", "-a", "Docker"))
		}
	}
	if len(clt) > 0 {
		// xcode-select only opens the system's dialog and returns; Homebrew needs what it
		// installs. The rest of the plan comes on the next check.
		p.plan.Commands = clt
		return
	}
	var cmds []Command
	if len(p.pkgs) > 0 {
		cmds = append(cmds, Command{Args: append([]string{"brew", "install"}, p.pkgs...), Tools: append([]string{}, p.pkgTools...)})
	}
	if len(casks) > 0 {
		cmds = append(cmds, casks...)
		p.plan.Notes = append(p.plan.Notes, text.T("tools.note_docker_desktop"))
	}
	cmds = append(cmds, p.after...)
	p.plan.Commands = cmds
}

func (p *planner) winget(need map[string]Status, order []string) {
	for _, id := range order {
		if _, ok := need[id]; !ok {
			continue
		}
		switch id {
		case WSL:
			// wsl.exe installs itself and Ubuntu; it asks for administrator and may need a restart.
			p.after = append(p.after, Command{Args: []string{"wsl", "--install", "-d", "Ubuntu"}, Tools: []string{id}, Elevated: true})
			p.plan.Notes = append(p.plan.Notes, text.T("tools.note_wsl_restart"))
		case Docker:
			p.after = append(p.after, Command{Args: []string{"winget", "install", "--exact", "--id", "Docker.DockerDesktop",
				"--accept-package-agreements", "--accept-source-agreements"}, Tools: []string{id}, Elevated: true})
			p.plan.Notes = append(p.plan.Notes, text.T("tools.note_docker_wsl"))
		default:
			p.manual(id, p.e.ManualFix(id))
		}
	}
	p.plan.Commands = p.after
}
