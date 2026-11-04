// Command covercheck enforces coverage floors from a Go cover profile.
//
// It parses the profile the same way `go tool cover` does (one block per
// line: file:start.col,end.col statements count), aggregates covered
// statements per package and fails when the overall ratio over internal/
// or a named package falls below its floor.
//
//	go run ./tools/covercheck -profile coverage/unit.out \
//	    -exclude internal/db/sqlc -floor 85 -pkg internal/credits=95
package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
)

const modulePath = "github.com/mmtmn/docmatch/services/api/"

type stats struct {
	statements int
	covered    int
}

func (s stats) percent() float64 {
	if s.statements == 0 {
		return 100
	}
	return 100 * float64(s.covered) / float64(s.statements)
}

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

func main() {
	var (
		profile  = flag.String("profile", "", "arquivo de cobertura (-coverprofile)")
		floor    = flag.Float64("floor", 85, "piso geral em % para internal/")
		excludes multiFlag
		pkgs     multiFlag
	)
	flag.Var(&excludes, "exclude", "pacote (relativo ao módulo) ignorado no piso geral; repetível")
	flag.Var(&pkgs, "pkg", "piso por pacote no formato caminho=percentual; repetível")
	flag.Parse()
	if *profile == "" {
		fmt.Fprintln(os.Stderr, "covercheck: -profile é obrigatório")
		os.Exit(2)
	}
	perPkg, err := parse(*profile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "covercheck:", err)
		os.Exit(2)
	}
	floors := map[string]float64{}
	for _, p := range pkgs {
		name, val, ok := strings.Cut(p, "=")
		f, err := strconv.ParseFloat(val, 64)
		if !ok || err != nil {
			fmt.Fprintf(os.Stderr, "covercheck: -pkg inválido %q (use caminho=percentual)\n", p)
			os.Exit(2)
		}
		floors[name] = f
	}
	if code := report(os.Stdout, perPkg, *floor, excludes, floors); code != 0 {
		os.Exit(code)
	}
}

// parse aggregates the profile per package. Blocks are keyed by position so
// the same block reported by several test binaries (-coverpkg) counts once,
// as covered when any of them covered it.
func parse(path string) (map[string]stats, error) {
	f, err := os.Open(path) //nolint:gosec // path comes from the developer's own command line
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	type block struct {
		pkg        string
		statements int
	}
	blocks := map[string]block{}
	counts := map[string]int{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	first := true
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if first {
			first = false
			if strings.HasPrefix(line, "mode:") {
				continue
			}
		}
		if line == "" {
			continue
		}
		// github.com/x/y/internal/p/file.go:10.2,12.3 2 1
		pos, rest, ok := strings.Cut(line, " ")
		if !ok {
			return nil, fmt.Errorf("linha inválida: %q", line)
		}
		fields := strings.Fields(rest)
		if len(fields) != 2 {
			return nil, fmt.Errorf("linha inválida: %q", line)
		}
		statements, err1 := strconv.Atoi(fields[0])
		count, err2 := strconv.Atoi(fields[1])
		if err1 != nil || err2 != nil {
			return nil, fmt.Errorf("linha inválida: %q", line)
		}
		file, _, ok := strings.Cut(pos, ":")
		if !ok {
			return nil, fmt.Errorf("linha inválida: %q", line)
		}
		pkg := strings.TrimPrefix(file[:strings.LastIndex(file, "/")], modulePath)
		blocks[pos] = block{pkg: pkg, statements: statements}
		if count > 0 {
			counts[pos] = 1
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	out := map[string]stats{}
	for pos, b := range blocks {
		s := out[b.pkg]
		s.statements += b.statements
		if counts[pos] > 0 {
			s.covered += b.statements
		}
		out[b.pkg] = s
	}
	return out, nil
}

func excluded(pkg string, excludes []string) bool {
	for _, e := range excludes {
		e = strings.TrimSuffix(e, "/")
		if pkg == e {
			return true
		}
	}
	return false
}

func report(out io.Writer, perPkg map[string]stats, floor float64, excludes []string, floors map[string]float64) int {
	w := bufio.NewWriter(out)
	defer func() { _ = w.Flush() }()
	names := make([]string, 0, len(perPkg))
	for name := range perPkg {
		names = append(names, name)
	}
	sort.Strings(names)
	var total stats
	failed := false
	_, _ = fmt.Fprintln(w, "cobertura por pacote:")
	for _, name := range names {
		s := perPkg[name]
		mark := ""
		switch {
		case !strings.HasPrefix(name, "internal/"):
			mark = " (fora de internal/, ignorado)"
		case excluded(name, excludes):
			mark = " (excluído do piso geral)"
		default:
			total.statements += s.statements
			total.covered += s.covered
		}
		if f, ok := floors[name]; ok {
			if s.percent() < f {
				mark += fmt.Sprintf("  ABAIXO DO PISO DE %.0f%%", f)
				failed = true
			} else {
				mark += fmt.Sprintf("  (piso %.0f%% ok)", f)
			}
		}
		_, _ = fmt.Fprintf(w, "  %-40s %6.1f%%%s\n", name, s.percent(), mark)
	}
	for name := range floors {
		if _, ok := perPkg[name]; !ok {
			_, _ = fmt.Fprintf(w, "  %-40s   ausente  ABAIXO DO PISO (pacote sem cobertura no perfil)\n", name)
			failed = true
		}
	}
	_, _ = fmt.Fprintf(w, "total internal/ (sem exclusões): %.1f%% (piso %.0f%%)\n", total.percent(), floor)
	if total.percent() < floor {
		_, _ = fmt.Fprintf(w, "cobertura geral abaixo do piso\n")
		failed = true
	}
	if failed {
		return 1
	}
	return 0
}
