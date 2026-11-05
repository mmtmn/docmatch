// Package harnesstest is an in-process fake of the harness gRPC server. It
// replays a recorded event stream built from the public Vantara twin
// (testdata/golden/vantara-twin/expected.yaml): one finding per expected
// finding, one match per reconciled bank line, token usage per stage and a
// completed summary. Tests and the dev environment (cmd/fakeharness) use it
// so the whole pipeline runs without the Rust harness.
package harnesstest

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"
)

// GoldenRelPath is the twin's answer key, relative to the repo root.
const GoldenRelPath = "testdata/golden/vantara-twin/expected.yaml"

// Script is the recorded case the fake replays.
type Script struct {
	Case      string
	Findings  []ScriptFinding
	BankLines []ScriptBankLine
	Titles    []ScriptTitle
	// Sources maps the twin's logical sources to roles (protheus, omie, bank, ap_inbox).
	Sources map[string]string
}

// ScriptFinding is one expected finding.
type ScriptFinding struct {
	Code     string   `yaml:"code"`
	Kind     string   `yaml:"kind"`
	Severity string   `yaml:"severity"`
	Amount   float64  `yaml:"amount_at_risk_brl"`
	Evidence []string `yaml:"evidence"`
	Title    string   `yaml:"title_ptbr"`
}

// ScriptBankLine is one bank statement line.
type ScriptBankLine struct {
	N        int      `yaml:"n"`
	Date     string   `yaml:"date"`
	Amount   float64  `yaml:"amount"`
	Side     string   `yaml:"side"`
	Kind     string   `yaml:"kind"`
	Matched  []string `yaml:"matched"`
	Rule     string   `yaml:"rule"`
	Proposed bool     `yaml:"proposed"`
	Findings []string `yaml:"findings"`
}

// ScriptTitle is one receivable.
type ScriptTitle struct {
	ID     string  `yaml:"id"`
	Source string  `yaml:"source"`
	Ref    string  `yaml:"ref"`
	Amount float64 `yaml:"amount"`
	Status string  `yaml:"status"`
}

type goldenFile struct {
	Case      string            `yaml:"case"`
	Sources   map[string]string `yaml:"sources"`
	Titles    []ScriptTitle     `yaml:"titles"`
	BankLines []ScriptBankLine  `yaml:"bank_lines"`
	Findings  []ScriptFinding   `yaml:"findings"`
}

// LoadScript parses an expected.yaml.
func LoadScript(path string) (*Script, error) {
	data, err := os.ReadFile(path) //nolint:gosec // test fixture path
	if err != nil {
		return nil, err
	}
	return ParseScript(data)
}

// ParseScript parses the answer key's YAML.
func ParseScript(data []byte) (*Script, error) {
	var g goldenFile
	if err := yaml.Unmarshal(data, &g); err != nil {
		return nil, fmt.Errorf("harnesstest: gabarito inválido: %w", err)
	}
	if len(g.Findings) == 0 || len(g.BankLines) == 0 {
		return nil, errors.New("harnesstest: gabarito sem achados ou linhas de extrato")
	}
	return &Script{Case: g.Case, Findings: g.Findings, BankLines: g.BankLines, Titles: g.Titles, Sources: g.Sources}, nil
}

// GoldenPath finds the twin's answer key by walking up from the working
// directory (tests run inside their package directory).
func GoldenPath() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		candidate := filepath.Join(dir, filepath.FromSlash(GoldenRelPath))
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("harnesstest: " + GoldenRelPath + " não encontrado acima de " + dir)
		}
		dir = parent
	}
}

// MustLoadGolden loads the twin or panics (tests).
func MustLoadGolden() *Script {
	path, err := GoldenPath()
	if err != nil {
		panic(err)
	}
	s, err := LoadScript(path)
	if err != nil {
		panic(err)
	}
	return s
}

// Centavos converts a BRL amount to integer centavos.
func Centavos(v float64) int64 { return int64(math.Round(v * 100)) }

// TotalAtRisk sums the findings' amounts, in centavos.
func (s *Script) TotalAtRisk() int64 {
	var total int64
	for _, f := range s.Findings {
		total += Centavos(f.Amount)
	}
	return total
}

// MatchedLines counts bank lines with at least one match.
func (s *Script) MatchedLines() int {
	n := 0
	for _, l := range s.BankLines {
		if len(l.Matched) > 0 {
			n++
		}
	}
	return n
}

// sourceOfRef maps an evidence reference ("bank:4", "title:T03", "email:1")
// to the twin's logical source name.
func (s *Script) sourceOfRef(ref string) string {
	kind, id, _ := strings.Cut(ref, ":")
	switch kind {
	case "bank":
		return "bank"
	case "email", "chat":
		return "ap_inbox"
	case "title":
		for _, t := range s.Titles {
			if t.ID == id {
				return t.Source
			}
		}
		if strings.HasPrefix(id, "N") {
			return "omie"
		}
		return "protheus"
	}
	return "protheus"
}
