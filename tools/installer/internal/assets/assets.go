// Package assets embeds the files the installer carries inside its binary. go:embed cannot read
// outside the module, so these are verbatim copies of canonical files elsewhere in the repository,
// refreshed by `make -C tools/installer sync` and checked by `make -C tools/installer sync-check`
// (part of `ci`):
//
//	locales/pt-BR.json   <- packages/locales/pt-BR.json (the installer.* section)
//	compose/compose.yaml <- deploy/local/compose.yaml
//	tokens/tokens.json   <- packages/design-tokens/tokens.json (the brand colors)
//	twin/*               <- testdata/golden/vantara-twin (the four inputs and the answer key)
package assets

import (
	"embed"
	"regexp"
)

//go:embed locales/pt-BR.json
var LocalesPTBR []byte

//go:embed compose/compose.yaml
var Compose []byte

//go:embed tokens/tokens.json
var Tokens []byte

//go:embed twin
var twin embed.FS

// TwinFile is one input of the public Vantara twin.
type TwinFile struct {
	Name string
	MIME string
	Data []byte
}

// TwinInputs are the files the site's e2e flow uploads (apps/web/e2e-local/flow.spec.ts).
var TwinInputs = []struct{ Name, MIME string }{
	{"01_contas_a_receber_protheus_jul2026.csv", "text/csv"},
	{"02_titulos_unidade_norte_omie_jul2026.csv", "text/csv"},
	{"03_extrato_bancario_jul2026.csv", "text/csv"},
	{"04_caixa_de_entrada_contas_a_pagar.txt", "text/plain"},
}

// Twin returns the twin's inputs.
func Twin() ([]TwinFile, error) {
	out := make([]TwinFile, 0, len(TwinInputs))
	for _, in := range TwinInputs {
		data, err := twin.ReadFile("twin/" + in.Name)
		if err != nil {
			return nil, err
		}
		out = append(out, TwinFile{Name: in.Name, MIME: in.MIME, Data: data})
	}
	return out, nil
}

var findingLine = regexp.MustCompile(`(?m)^ {2}- \{ code: F\d+`)
var sectionHead = regexp.MustCompile(`(?m)^[a-z_]+:`)
var findingsHead = regexp.MustCompile(`(?m)^findings:\s*$`)

// ExpectedFindings counts the entries of the `findings:` section of the answer key, the same
// way the e2e flow does.
func ExpectedFindings() int {
	data, err := twin.ReadFile("twin/expected.yaml")
	if err != nil {
		return 0
	}
	loc := findingsHead.FindIndex(data)
	if loc == nil {
		return 0
	}
	section := data[loc[1]:]
	if next := sectionHead.FindIndex(section); next != nil {
		section = section[:next[0]]
	}
	return len(findingLine.FindAll(section, -1))
}
