package runs

import (
	"strings"
	"testing"

	pb "github.com/mmtmn/docmatch/services/api/internal/harness/pb"
)

func TestSummaryCarriesTheSourceWarnings(t *testing.T) {
	t.Parallel()
	s := SummaryFromProto(&pb.RunSummary{SourceWarnings: []string{
		"extrato.pdf: PDF sem camada de texto (o OCR ainda não está disponível).",
		"  ",
		"remessa.rem: arquivo de remessa CNAB 400 não entra na conciliação.",
	}})
	if len(s.SourceWarnings) != 2 || !strings.HasPrefix(s.SourceWarnings[0], "extrato.pdf:") {
		t.Fatalf("avisos: %q", s.SourceWarnings)
	}
	if none := SummaryFromProto(&pb.RunSummary{}); none.SourceWarnings != nil {
		t.Fatalf("sem avisos, o campo some do JSON: %q", none.SourceWarnings)
	}
}

func TestSourceWarningsAreBounded(t *testing.T) {
	t.Parallel()
	many := make([]string, maxSourceWarnings+20)
	for i := range many {
		many[i] = strings.Repeat("á", maxSourceWarningRunes+50)
	}
	out := sourceWarnings(many)
	if len(out) != maxSourceWarnings {
		t.Fatalf("%d avisos, quer %d", len(out), maxSourceWarnings)
	}
	if n := len([]rune(out[0])); n != maxSourceWarningRunes || !strings.HasSuffix(out[0], "…") {
		t.Fatalf("aviso com %d caracteres", n)
	}
}
