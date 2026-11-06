// Package workspace is the operational terminal, using the same public API as SDK users.
package workspace

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	dm "github.com/mmtmn/docmatch/sdks/go"
)

type Options struct {
	API, Key, Dir, Output   string
	Files                   []string
	Plain, Yes, NoAnimation bool
	In                      io.Reader
	Out                     io.Writer
}
type model struct {
	ctx           context.Context
	c             *dm.Client
	opts          Options
	dir           string
	entries       []os.DirEntry
	cursor        int
	selected      map[string]bool
	input         textinput.Model
	editing       bool
	spinner       spinner.Model
	stage         string
	message       string
	quote         dm.Quote
	request       dm.Input
	run           dm.Run
	findings      []dm.Finding
	err           error
	width, height int
	paths         []string
	uploadIndex   int
	ids           []string
}
type connected struct{ err error }
type uploaded struct {
	upload dm.Upload
	err    error
}
type quoted struct {
	quote dm.Quote
	err   error
}
type started struct {
	run dm.Run
	err error
}
type polled struct {
	run dm.Run
	err error
}
type finished struct {
	findings []dm.Finding
	err      error
}
type exported struct {
	path string
	err  error
}
type pollTick struct{}

var brand = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#1d4ed8", Dark: "#8ab4ff"}).Bold(true)
var muted = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#4b5563", Dark: "#a3abb5"})

func (m *model) readDir() {
	entries, err := os.ReadDir(m.dir)
	m.err = err
	m.entries = nil
	m.cursor = 0
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if e.IsDir() || MIME(e.Name()) != "" {
			m.entries = append(m.entries, e)
		}
	}
}
func (m *model) add(paths []string) {
	for _, path := range paths {
		if !filepath.IsAbs(path) {
			path = filepath.Join(m.dir, path)
		}
		p, err := Validate(path)
		if err != nil {
			m.err = err
			continue
		}
		m.selected[p] = true
	}
	if len(m.selected) > 40 {
		m.err = fmt.Errorf("selecione até 40 arquivos; o limite do plano será validado pela API")
	}
}
func Run(ctx context.Context, opts Options) error {
	c, err := dm.New(opts.API, opts.Key)
	if err != nil {
		return err
	}
	if opts.Dir == "" {
		opts.Dir, _ = os.Getwd()
	}
	if opts.Output == "" {
		opts.Output = filepath.Join(opts.Dir, "docmatch-output")
	}
	if opts.Plain {
		return plain(ctx, c, opts)
	}
	// Bind colors to this interactive output; respect NO_COLOR in the CLI's plain path.
	lipgloss.SetDefaultRenderer(lipgloss.NewRenderer(opts.Out))
	if os.Getenv("COLORTERM") == "truecolor" || os.Getenv("COLORTERM") == "24bit" {
		lipgloss.SetColorProfile(termenv.TrueColor)
	}
	brand = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#1d4ed8", Dark: "#8ab4ff"}).Bold(true)
	muted = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#4b5563", Dark: "#a3abb5"})
	input := textinput.New()
	input.Placeholder = "Cole ou arraste vários arquivos aqui"
	input.CharLimit = 16000
	input.Width = 75
	spin := spinner.New()
	spin.Spinner = spinner.Dot
	spin.Style = brand
	m := &model{ctx: ctx, c: c, opts: opts, dir: opts.Dir, selected: map[string]bool{}, input: input, spinner: spin, stage: "connecting", width: 100, height: 30}
	m.readDir()
	m.add(opts.Files)
	p := tea.NewProgram(m, tea.WithContext(ctx), tea.WithInput(opts.In), tea.WithOutput(opts.Out), tea.WithAltScreen())
	_, err = p.Run()
	return err
}
func (m *model) Init() tea.Cmd {
	connect := func() tea.Msg {
		var err error
		if m.opts.Key == "" {
			err = m.c.DevLogin(m.ctx)
		}
		return connected{err}
	}
	if m.opts.NoAnimation {
		return connect
	}
	return tea.Batch(connect, m.spinner.Tick)
}
func (m *model) fail(err error) { m.err = err; m.stage = "select" }
func (m *model) upload() tea.Cmd {
	index := m.uploadIndex
	path := m.paths[index]
	m.message = fmt.Sprintf("Enviando e verificando %d/%d · %s", index+1, len(m.paths), Safe(filepath.Base(path)))
	return func() tea.Msg {
		data, err := os.ReadFile(path) // #nosec G304 -- intentional local file selection, validated by Validate before upload.
		if err != nil {
			return uploaded{err: err}
		}
		u, err := m.c.Upload(m.ctx, filepath.Base(path), MIME(path), data)
		return uploaded{u, err}
	}
}
func (m *model) poll() tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return pollTick{} })
}
func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch v := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = v.Width
		m.height = v.Height
		m.input.Width = max(20, min(90, v.Width-8))
	case spinner.TickMsg:
		if !m.opts.NoAnimation {
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(msg)
			return m, cmd
		}
	case connected:
		if v.err != nil {
			m.fail(v.err)
		} else {
			m.stage = "select"
			m.message = "Pronto para selecionar documentos."
		}
	case uploaded:
		if v.err != nil {
			m.fail(v.err)
			break
		}
		m.ids = append(m.ids, v.upload.ID)
		m.uploadIndex++
		if m.uploadIndex < len(m.paths) {
			return m, m.upload()
		}
		m.stage = "estimating"
		m.message = "Calculando a estimativa…"
		m.request = m.c.Input(m.ids...)
		return m, func() tea.Msg { q, e := m.c.Estimate(m.ctx, m.request); return quoted{q, e} }
	case quoted:
		if v.err != nil {
			m.fail(v.err)
			break
		}
		m.quote = v.quote
		m.stage = "confirm"
		m.message = "Confira o limite antes de iniciar. Nenhuma execução começa sem aprovação."
		if m.opts.Yes {
			return m, m.start()
		}
	case started:
		if v.err != nil {
			m.fail(v.err)
			break
		}
		m.run = v.run
		m.stage = "processing"
		m.message = "Execução iniciada · aguardando processamento"
		return m, m.poll()
	case pollTick:
		return m, func() tea.Msg { r, e := m.c.GetRun(m.ctx, m.run.ID); return polled{r, e} }
	case polled:
		if v.err != nil {
			m.fail(v.err)
			break
		}
		m.run = v.run
		if v.run.Progress != nil {
			m.message = Safe(v.run.Progress.Message)
		}
		switch v.run.Status {
		case "completed":
			return m, func() tea.Msg { f, e := m.c.Findings(m.ctx, m.run.ID); return finished{f, e} }
		case "failed", "cancelled", "needs_approval":
			m.fail(fmt.Errorf("execução %s. Consulte o resultado antes de tentar novamente", v.run.Status))
		default:
			return m, m.poll()
		}
	case finished:
		if v.err != nil {
			m.fail(v.err)
			break
		}
		m.findings = v.findings
		m.stage = "done"
		m.message = "Documentos processados. Baixe os dados corrigidos e revise os achados."
		m.cursor = 0
	case exported:
		if v.err != nil {
			m.err = v.err
		} else {
			m.message = "Salvo em " + Safe(v.path)
		}
	case tea.KeyMsg:
		if v.String() == "ctrl+c" {
			return m, tea.Quit
		}
		if m.editing {
			switch v.String() {
			case "esc":
				m.editing = false
				m.input.Blur()
			case "enter":
				paths, err := ParsePaths(m.input.Value())
				if err != nil {
					m.err = err
				} else {
					m.add(paths)
					m.input.SetValue("")
					m.editing = false
					m.input.Blur()
				}
			default:
				var cmd tea.Cmd
				m.input, cmd = m.input.Update(msg)
				return m, cmd
			}
			return m, nil
		}
		if v.Paste {
			m.editing = true
			m.input.Focus()
			m.input.SetValue(string(v.Runes))
			return m, nil
		}
		if v.String() == "q" || v.String() == "esc" {
			return m, tea.Quit
		}
		if m.stage == "confirm" {
			if v.String() == "y" || v.String() == "s" {
				return m, m.start()
			}
			if v.String() == "n" {
				m.stage = "select"
				m.message = "Estimativa cancelada."
			}
			return m, nil
		}
		if m.stage == "done" {
			switch v.String() {
			case "x", "c", "p":
				format := map[string]string{"x": "xlsx", "c": "csv", "p": "pdf"}[v.String()]
				m.message = "Preparando download…"
				return m, func() tea.Msg {
					data, e := m.c.Export(m.ctx, m.run.ID, format)
					path := filepath.Join(m.opts.Output, m.run.ID+"."+format)
					if e == nil {
						e = os.MkdirAll(m.opts.Output, 0o700)
					}
					if e == nil {
						e = os.WriteFile(path, data, 0o600)
					}
					return exported{path, e}
				}
			case "j", "down":
				m.cursor = min(m.cursor+1, len(m.findings)-1)
			case "k", "up":
				m.cursor = max(0, m.cursor-1)
			case "n":
				m.stage = "select"
				m.selected = map[string]bool{}
				m.readDir()
				m.err = nil
			}
			return m, nil
		}
		if m.stage != "select" {
			return m, nil
		}
		switch v.String() {
		case "up", "k":
			m.cursor = max(0, m.cursor-1)
		case "down", "j":
			m.cursor = min(len(m.entries)-1, m.cursor+1)
		case "backspace", "left":
			m.dir = filepath.Dir(m.dir)
			m.readDir()
		case "enter", "right":
			if m.cursor >= 0 && m.cursor < len(m.entries) {
				e := m.entries[m.cursor]
				if e.IsDir() {
					m.dir = filepath.Join(m.dir, e.Name())
					m.readDir()
				} else {
					m.toggle(e)
				}
			}
		case " ":
			if m.cursor >= 0 && m.cursor < len(m.entries) && !m.entries[m.cursor].IsDir() {
				m.toggle(m.entries[m.cursor])
			}
		case "a":
			for _, e := range m.entries {
				if !e.IsDir() {
					m.add([]string{filepath.Join(m.dir, e.Name())})
				}
			}
		case "d":
			m.selected = map[string]bool{}
		case "tab", "i":
			m.editing = true
			return m, m.input.Focus()
		case "r":
			if len(m.selected) > 0 && len(m.selected) <= 40 {
				m.paths = nil
				for p := range m.selected {
					m.paths = append(m.paths, p)
				}
				sort.Strings(m.paths)
				m.ids = nil
				m.uploadIndex = 0
				m.err = nil
				m.stage = "uploading"
				return m, m.upload()
			}
		}
	}
	return m, nil
}
func (m *model) toggle(e os.DirEntry) {
	p := filepath.Join(m.dir, e.Name())
	if m.selected[p] {
		delete(m.selected, p)
	} else {
		m.add([]string{p})
	}
}
func (m *model) start() tea.Cmd {
	m.stage = "starting"
	m.message = "Iniciando com a estimativa aprovada…"
	return func() tea.Msg { r, e := m.c.Run(m.ctx, m.quote, m.request); return started{r, e} }
}
func (m *model) View() string {
	var b strings.Builder
	b.WriteString(brand.Render("docmatch  /  workspace") + "\n" + muted.Render("Seus arquivos → evidências → dados corrigidos") + "\n\n")
	b.WriteString(muted.Render("API "+Safe(m.opts.API)) + "\n")
	if m.err != nil {
		b.WriteString("\n! " + Safe(m.err.Error()) + "\n")
	}
	switch m.stage {
	case "select", "connecting":
		b.WriteString("\n" + brand.Render(Safe(m.dir)) + "\n")
		rows := max(3, m.height-15)
		start := max(0, m.cursor-rows+1)
		end := min(len(m.entries), start+rows)
		for i := start; i < end; i++ {
			e := m.entries[i]
			check := "[ ]"
			if e.IsDir() {
				check = " ▸ "
			} else if m.selected[filepath.Join(m.dir, e.Name())] {
				check = "[✓]"
			}
			line := fmt.Sprintf(" %s %s", check, Safe(e.Name()))
			if i == m.cursor {
				line = brand.Render("›" + line)
			} else {
				line = " " + line
			}
			b.WriteString(line + "\n")
		}
		fmt.Fprintf(&b, "\n%d arquivos selecionados\n", len(m.selected))
		if m.editing {
			b.WriteString("\n" + m.input.View() + "\nEnter adiciona · Esc volta\n")
		} else {
			b.WriteString(muted.Render("↑↓ navegar · Espaço selecionar · a todos · d limpar\nEnter abrir · ← pasta anterior · Tab colar/arrastar arquivos\nr processar · q sair") + "\n")
		}
	case "confirm":
		fmt.Fprintf(&b, "\n%s\n\nEstimativa: %d tokens · limite: %d tokens\n\n%s\n", brand.Render("Aprovar esta execução?"), m.quote.ExpectedCredits, m.quote.CapCredits, "s / y confirmar · n voltar")
	case "done":
		b.WriteString("\n" + brand.Render("✓ Concluído") + "  " + Safe(m.run.ID) + "\n")
		var summary struct {
			Reconciliation struct {
				Receivables []json.RawMessage `json:"receivables"`
				BankEntries []json.RawMessage `json:"bank_entries"`
			} `json:"reconciliation"`
		}
		_ = json.Unmarshal(m.run.Summary, &summary)
		fmt.Fprintf(&b, "\n%d registros · %d movimentos · %d achados\n", len(summary.Reconciliation.Receivables), len(summary.Reconciliation.BankEntries), len(m.findings))
		b.WriteString(muted.Render("x baixar XLSX corrigido · c CSV · p relatório PDF\n↑↓ percorrer achados · n nova execução · q sair") + "\n\n")
		for i := m.cursor; i < min(len(m.findings), m.cursor+max(2, m.height-17)); i++ {
			f := m.findings[i]
			fmt.Fprintf(&b, "%s  %s\n", brand.Render(Safe(f.Code)), Safe(f.Title))
		}
	default:
		indicator := "…"
		if !m.opts.NoAnimation {
			indicator = m.spinner.View()
		}
		b.WriteString("\n" + indicator + " " + brand.Render("Processando seus documentos") + "\n\n")
		if m.run.Progress != nil {
			percent := max(0, min(100, m.run.Progress.Percent))
			filled := percent * 40 / 100
			b.WriteString(brand.Render(strings.Repeat("━", filled)) + muted.Render(strings.Repeat("─", 40-filled)) + fmt.Sprintf("  %d%%\n", percent))
		}
	}
	b.WriteString("\n" + muted.Render(Safe(m.message)) + "\n")
	return lipgloss.NewStyle().Padding(1, 2).Width(max(30, m.width-4)).Render(b.String())
}
func plain(ctx context.Context, c *dm.Client, o Options) error {
	if len(o.Files) == 0 {
		return fmt.Errorf("informe arquivos após workspace ou use um terminal interativo")
	}
	if !o.Yes {
		return fmt.Errorf("use a interface para aprovar a estimativa, ou --yes para autorizar o limite retornado pela API")
	}
	if o.Key == "" {
		if err := c.DevLogin(ctx); err != nil {
			return err
		}
	}
	var ids []string
	for i, path := range o.Files {
		p, err := Validate(path)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(o.Out, "Enviando %d/%d: %s\n", i+1, len(o.Files), Safe(filepath.Base(p)))
		data, err := os.ReadFile(p) // #nosec G304 -- intentional local file selection, validated by Validate before upload.
		if err != nil {
			return err
		}
		u, err := c.Upload(ctx, filepath.Base(p), MIME(p), data)
		if err != nil {
			return err
		}
		ids = append(ids, u.ID)
	}
	input := c.Input(ids...)
	quote, err := c.Estimate(ctx, input)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(o.Out, "Estimativa %d tokens; limite aprovado com --yes: %d\n", quote.ExpectedCredits, quote.CapCredits)
	run, err := c.Run(ctx, quote, input)
	if err != nil {
		return err
	}
	run, err = c.Wait(ctx, run.ID, func(r dm.Run) {
		if r.Progress != nil {
			_, _ = fmt.Fprintln(o.Out, Safe(r.Progress.Message))
		}
	})
	if err != nil {
		return err
	}
	data, err := c.Export(ctx, run.ID, "xlsx")
	if err != nil {
		return err
	}
	if err = os.MkdirAll(o.Output, 0o700); err != nil {
		return err
	}
	path := filepath.Join(o.Output, run.ID+".xlsx")
	if err = os.WriteFile(path, data, 0o600); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(o.Out, "Concluído: %s\nXLSX corrigido: %s\n", run.ID, Safe(path))
	return nil
}
