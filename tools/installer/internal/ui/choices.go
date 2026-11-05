package ui

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"

	"github.com/mmtmn/docmatch/tools/installer/internal/envfile"
	"github.com/mmtmn/docmatch/tools/installer/internal/install"
	"github.com/mmtmn/docmatch/tools/installer/internal/platform"
	"github.com/mmtmn/docmatch/tools/installer/internal/ports"
	"github.com/mmtmn/docmatch/tools/installer/internal/preflight"
	"github.com/mmtmn/docmatch/tools/installer/internal/state"
	"github.com/mmtmn/docmatch/tools/installer/internal/text"
)

type formStatus int

const (
	formRunning formStatus = iota
	formDone
	formAborted
)

// choiceValues are the fields the form edits (huh binds to pointers).
type choiceValues struct {
	llm, localURL, key  string
	web, api, pg, mail  string
	dir, source         string
	portByName          map[string]*string
	ours                map[int]bool
	free                ports.Prober
	homeForExpand, goos string
}

// choicesModel is the Huh form: model mode, ports and folder.
type choicesModel struct {
	t    Theme
	form *huh.Form
	v    *choiceValues
	mode string
}

func (t Theme) huhTheme() *huh.Theme {
	h := huh.ThemeBase()
	h.Focused.Base = h.Focused.Base.BorderForeground(t.Brand)
	if t.ASCII {
		h.Focused.Base = h.Focused.Base.BorderStyle(lipgloss.ASCIIBorder())
	}
	h.Focused.Card = h.Focused.Base
	h.Focused.Title = h.Focused.Title.Foreground(t.Brand).Bold(true)
	h.Focused.NoteTitle = h.Focused.NoteTitle.Foreground(t.Brand).Bold(true).MarginBottom(1)
	h.Focused.Description = h.Focused.Description.Foreground(t.Muted)
	h.Focused.ErrorIndicator = h.Focused.ErrorIndicator.Foreground(t.Danger)
	h.Focused.ErrorMessage = h.Focused.ErrorMessage.Foreground(t.Danger)
	h.Focused.SelectSelector = lipgloss.NewStyle().Foreground(t.Brand).SetString(t.G.Pointer + " ")
	h.Focused.Option = h.Focused.Option.Foreground(t.Text)
	h.Focused.SelectedOption = h.Focused.SelectedOption.Foreground(t.Brand)
	h.Focused.TextInput.Cursor = h.Focused.TextInput.Cursor.Foreground(t.Brand)
	h.Focused.TextInput.Prompt = h.Focused.TextInput.Prompt.Foreground(t.Brand)
	h.Focused.TextInput.Placeholder = h.Focused.TextInput.Placeholder.Foreground(t.Muted)
	h.Focused.FocusedButton = h.Focused.FocusedButton.Foreground(lipgloss.Color("#FFFFFF")).Background(t.Brand)
	h.Focused.Next = h.Focused.FocusedButton
	if t.ASCII {
		h.Focused.NextIndicator = h.Focused.NextIndicator.SetString("->")
		h.Focused.PrevIndicator = h.Focused.PrevIndicator.SetString("<-")
	}
	h.Blurred = h.Focused
	h.Blurred.Base = h.Focused.Base.BorderStyle(lipgloss.HiddenBorder())
	h.Blurred.Card = h.Blurred.Base
	h.Blurred.Title = h.Blurred.Title.Foreground(t.Subtle).Bold(false)
	h.Blurred.NextIndicator = lipgloss.NewStyle()
	h.Blurred.PrevIndicator = lipgloss.NewStyle()
	h.Help.ShortKey = h.Help.ShortKey.Foreground(t.Brand)
	h.Help.ShortDesc = h.Help.ShortDesc.Foreground(t.Muted)
	h.Help.ShortSeparator = h.Help.ShortSeparator.Foreground(t.Muted)
	return h
}

// keyMap is Huh's default with the help in pt-BR.
func keyMap() *huh.KeyMap {
	k := huh.NewDefaultKeyMap()
	k.Input.Next.SetHelp("enter", text.T("form.key_next"))
	k.Input.Prev.SetHelp("shift+tab", text.T("form.key_back"))
	k.Input.Submit.SetHelp("enter", text.T("form.key_install"))
	k.Input.AcceptSuggestion.SetHelp("ctrl+e", text.T("form.key_suggestion"))
	k.Select.Next.SetHelp("enter", text.T("form.key_next"))
	k.Select.Prev.SetHelp("shift+tab", text.T("form.key_back"))
	k.Select.Submit.SetHelp("enter", text.T("form.key_install"))
	k.Select.Up.SetHelp("↑", text.T("form.key_up"))
	k.Select.Down.SetHelp("↓", text.T("form.key_down"))
	k.Select.Filter.SetEnabled(false) // three options: nothing to filter
	k.Note.Next.SetHelp("enter", text.T("form.key_next"))
	k.Note.Prev.SetHelp("shift+tab", text.T("form.key_back"))
	k.Note.Submit.SetHelp("enter", text.T("form.key_install"))
	k.Confirm.Next.SetHelp("enter", text.T("form.key_next"))
	k.Confirm.Prev.SetHelp("shift+tab", text.T("form.key_back"))
	k.Confirm.Submit.SetHelp("enter", text.T("form.key_install"))
	return k
}

func newChoices(t Theme, opts install.Options, ours map[int]bool, free ports.Prober, goos string) *choicesModel {
	home, _ := os.UserHomeDir()
	v := &choiceValues{
		llm: opts.LLMMode, localURL: opts.LocalLLM, key: opts.OpenRouter,
		web: strconv.Itoa(opts.Ports[ports.Web]), api: strconv.Itoa(opts.Ports[ports.API]),
		pg: strconv.Itoa(opts.Ports[ports.Postgres]), mail: strconv.Itoa(opts.Ports[ports.Mailpit]),
		dir: opts.Dir, source: opts.CloneDir, ours: ours, free: free, homeForExpand: home, goos: goos,
	}
	if v.llm == "" {
		v.llm = envfile.LLMOff
	}
	if v.localURL == "" {
		v.localURL = "http://localhost:11434/v1"
	}
	v.portByName = map[string]*string{ports.Web: &v.web, ports.API: &v.api, ports.Postgres: &v.pg, ports.Mailpit: &v.mail}

	var groups []*huh.Group
	switch {
	case opts.Mode == state.ModeDev && opts.SourceDir == "":
		groups = append(groups, huh.NewGroup(
			huh.NewInput().Title(text.T("form.clone_title")).Description(t.plainText(text.T("form.clone_desc"))).
				Value(&v.source).Validate(func(s string) error {
				dir := expandDir(strings.TrimSpace(s), v.homeForExpand)
				if strings.TrimSpace(s) == "" || !install.IsSource(dir) && !install.CanClone(dir) {
					return errors.New(text.T("form.err_clone"))
				}
				if len(preflight.HarnessSocket(dir)) > preflight.MaxSocketPath {
					return errors.New(text.T("form.err_clone_long"))
				}
				return nil
			}),
		))
	case opts.Mode == state.ModeDev:
		groups = append(groups, huh.NewGroup(
			huh.NewNote().Title(text.T("form.dev_title")).Description(t.plainText(text.T("form.dev_text", "dir", opts.SourceDir))),
		))
	default:
		groups = append(groups,
			huh.NewGroup(
				huh.NewSelect[string]().Title(text.T("form.llm_title")).Description(t.plainText(text.T("form.llm_desc"))).
					Options(
						huh.NewOption(text.T("form.llm_off"), envfile.LLMOff),
						huh.NewOption(text.T("form.llm_local"), envfile.LLMLocal),
						huh.NewOption(text.T("form.llm_openrouter"), envfile.LLMOpenRouter),
					).Value(&v.llm),
			),
			huh.NewGroup(
				huh.NewInput().Title(text.T("form.ollama_title")).Description(t.plainText(text.T("form.ollama_desc"))).
					Value(&v.localURL).Validate(func(s string) error {
					if envfile.ValidateLLMURL(s) != nil {
						return errors.New(text.T("form.err_url"))
					}
					return nil
				}),
			).WithHideFunc(func() bool { return v.llm != envfile.LLMLocal }),
			huh.NewGroup(
				huh.NewInput().Title(text.T("form.openrouter_title")).Description(t.plainText(text.T("form.openrouter_desc"))).
					EchoMode(huh.EchoModePassword).Value(&v.key).Validate(func(s string) error {
					if strings.TrimSpace(s) == "" {
						return errors.New(text.T("form.err_key"))
					}
					return nil
				}),
			).WithHideFunc(func() bool { return v.llm != envfile.LLMOpenRouter }),
			huh.NewGroup(
				v.portInput(ports.Web, text.T("form.ports_title"), t.plainText(text.T("form.ports_desc"))),
				v.portInput(ports.API, "", ""),
				v.portInput(ports.Postgres, "", ""),
				v.portInput(ports.Mailpit, "", ""),
			),
		)
	}
	groups = append(groups, huh.NewGroup(
		huh.NewInput().Title(text.T("form.dir_title")).Description(t.plainText(text.T("form.dir_desc"))).
			Value(&v.dir).Validate(func(s string) error {
			if strings.TrimSpace(s) == "" {
				return errors.New(text.T("form.err_dir"))
			}
			return nil
		}),
	))
	form := huh.NewForm(groups...).WithTheme(t.huhTheme()).WithKeyMap(keyMap()).WithShowHelp(true).WithShowErrors(true)
	return &choicesModel{t: t, form: form, v: v, mode: opts.Mode}
}

func (v *choiceValues) portInput(name, title, desc string) *huh.Input {
	label := text.T("ports." + name)
	in := huh.NewInput().Prompt(lipgloss.NewStyle().Width(10).Render(label) + " ").CharLimit(5).Value(v.portByName[name]).
		Validate(func(s string) error { return v.validatePort(name, s) })
	if title != "" {
		in = in.Title(title).Description(desc)
	}
	return in
}

func (v *choiceValues) validatePort(name, s string) error {
	n, err := ports.Parse(s)
	if err != nil {
		return errors.New(text.T("form.err_port"))
	}
	for other, p := range v.portByName {
		if other != name && strings.TrimSpace(*p) == strconv.Itoa(n) {
			return errors.New(text.T("form.err_port_dup"))
		}
	}
	if !v.ours[n] && v.free != nil && !v.free(n) {
		return errors.New(text.T("form.err_port_busy"))
	}
	return nil
}

// resize sets the form's width and lets Huh size its groups for it (Huh measures the fields on a
// WindowSizeMsg, and the real one arrived before this form existed).
func (m *choicesModel) resize(w, h int) {
	m.form = m.form.WithWidth(w)
	if h > 12 {
		h -= 8 // header, title and margins
	}
	if model, _ := m.form.Update(tea.WindowSizeMsg{Width: w, Height: h}); model != nil {
		if f, ok := model.(*huh.Form); ok {
			m.form = f
		}
	}
}

func (m *choicesModel) update(msg tea.Msg) (tea.Cmd, formStatus) {
	model, cmd := m.form.Update(msg)
	if f, ok := model.(*huh.Form); ok {
		m.form = f
	}
	switch m.form.State {
	case huh.StateCompleted:
		return cmd, formDone
	case huh.StateAborted:
		return cmd, formAborted
	}
	return cmd, formRunning
}

// apply copies the answers into opts.
func (m *choicesModel) apply(opts install.Options) install.Options {
	v := m.v
	opts.Dir = expandDir(strings.TrimSpace(v.dir), v.homeForExpand)
	if m.mode == state.ModeDev {
		if opts.SourceDir == "" {
			opts.CloneDir = expandDir(strings.TrimSpace(v.source), v.homeForExpand)
		}
		return opts.Resolved()
	}
	opts.LLMMode = v.llm
	opts.LocalLLM, opts.OpenRouter = "", ""
	switch v.llm {
	case envfile.LLMLocal:
		opts.LocalLLM = strings.TrimSpace(v.localURL)
	case envfile.LLMOpenRouter:
		opts.OpenRouter = strings.TrimSpace(v.key)
	}
	p := opts.Ports.Clone()
	for name, s := range v.portByName {
		if n, err := ports.Parse(*s); err == nil {
			p[name] = n
		}
	}
	opts.Ports = p
	return opts
}

func expandDir(dir, home string) string {
	dir = platform.ExpandHome(dir, home)
	if abs, err := filepath.Abs(dir); err == nil {
		return abs
	}
	return dir
}

func (m *choicesModel) view() string {
	return m.t.Heading.Render(text.T("form.title")) + "\n\n" + m.form.View()
}
