package runs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/google/uuid"

	contracts "github.com/mmtmn/docmatch/packages/contracts/gen"
	"github.com/mmtmn/docmatch/services/api/internal/export"
	"github.com/mmtmn/docmatch/services/api/internal/runs/apix"
)

// Export builds a reconciled output document and its supporting findings.
func (s *Service) Export(ctx context.Context, orgID, runID uuid.UUID, format string) (Export, error) {
	switch format {
	case export.FormatXLSX, export.FormatCSV, export.FormatPDF:
	default:
		return Export{}, apix.Ef(contracts.ErrExportFormatUnsupported, "formato %q", format)
	}
	r, err := s.Get(ctx, orgID, runID)
	if err != nil {
		return Export{}, err
	}
	if r.Status != StatusCompleted {
		return Export{}, apix.Ef(contracts.ErrRunNotCompleted, "status %s", r.Status)
	}
	findings, err := s.d.Store.ListAllFindings(ctx, orgID, runID)
	if err != nil {
		return Export{}, fmt.Errorf("runs: achados: %w", err)
	}
	name := "docmatch-" + r.PublicID()
	var doc *ReconciliationDocument
	if r.Summary != nil {
		doc = r.Summary.Reconciliation
	}
	if format == export.FormatCSV {
		table := findingsTable(findings)
		if doc != nil && len(doc.Receivables) > 0 {
			table = documentTables(doc)[1]
			name += "-carteira-conciliada"
		}
		return Export{ContentType: export.ContentTypeCSV, FileName: name + ".csv", Write: func(w io.Writer) error { return export.WriteCSV(w, table) }}, nil
	}
	matches, err := s.d.Store.ListAllMatches(ctx, orgID, runID)
	if err != nil {
		return Export{}, fmt.Errorf("runs: casamentos: %w", err)
	}
	tables := []export.Table{findingsTable(findings), matchesTable(matches), evidenceTable(findings, matches)}
	if doc != nil {
		tables = append(documentTables(doc), tables...)
		name += "-conciliacao"
	}
	if format == export.FormatPDF {
		sourceNames := map[string]string{}
		for _, id := range r.SourceIDs() {
			if source, err := s.d.Uploads.Get(ctx, orgID, id); err == nil {
				sourceNames[source.PublicID()] = source.FileName
			}
		}
		report := reconciliationReport(r, doc, findings, sourceNames)
		var buf bytes.Buffer
		if err := export.WritePDF(&buf, report); err != nil {
			return Export{}, fmt.Errorf("runs: gerar PDF: %w", err)
		}
		data := buf.Bytes()
		return Export{ContentType: export.ContentTypePDF, FileName: name + ".pdf", Write: func(w io.Writer) error { _, err := w.Write(data); return err }}, nil
	}
	return Export{ContentType: export.ContentTypeXLSX, FileName: name + ".xlsx", Write: func(w io.Writer) error { return export.WriteXLSX(w, tables) }}, nil
}

// Money renders centavos as "1234,56".
func Money(centavos int64) string {
	neg := centavos < 0
	if neg {
		centavos = -centavos
	}
	s := fmt.Sprintf("%d,%02d", centavos/100, centavos%100)
	if neg {
		return "-" + s
	}
	return s
}

func findingsTable(findings []Finding) export.Table {
	t := export.Table{Name: "Achados", Header: []string{"id", "codigo", "tipo", "severidade", "valor_em_risco", "moeda", "titulo", "explicacao", "confianca", "rotulo", "status", "fontes", "evidencias", "criado_em"}, Types: []export.CellType{export.Text, export.Text, export.Text, export.Text, export.Currency}, Widths: []float64{30, 30, 20, 18, 22, 12, 55, 100, 14, 20, 20, 35, 65, 26}}
	for _, f := range findings {
		v := f.View()
		t.Rows = append(t.Rows, []string{
			v.ID, v.Code, v.Kind, v.Severity, Money(v.AmountAtRisk), v.Currency, v.Title, v.Explanation,
			strconv.FormatFloat(v.Confidence, 'f', 2, 64), v.ConfidenceLabel, v.Status, strings.Join(v.SourceIDs, " "),
			summarizeEvidence(f.Evidence), v.CreatedAt,
		})
	}
	return t
}

func matchesTable(matches []Match) export.Table {
	t := export.Table{Name: "Casamentos", Header: []string{"id", "tipo", "confianca", "valor", "moeda", "regra", "explicacao", "esquerda", "direita", "criado_em"}}
	for _, m := range matches {
		v := m.View()
		amount := ""
		if v.Amount != nil {
			amount = Money(*v.Amount)
		}
		rule := ""
		if v.RuleID != nil {
			rule = *v.RuleID
		}
		t.Rows = append(t.Rows, []string{v.ID, v.Kind, strconv.FormatFloat(v.Confidence, 'f', 2, 64), amount, v.Currency, rule, v.Explanation, summarizeEvidence(m.Left), summarizeEvidence(m.Right), v.CreatedAt})
	}
	return t
}

func evidenceTable(findings []Finding, matches []Match) export.Table {
	t := export.Table{Name: "Evidências", Header: []string{"registro", "fonte", "localizador", "trecho", "rotulo"}}
	add := func(owner string, raw json.RawMessage) {
		for _, e := range parseEvidence(raw) {
			t.Rows = append(t.Rows, []string{owner, e.SourceID, locatorText(e.Locator), e.Excerpt, e.Label})
		}
	}
	for _, f := range findings {
		add(f.PublicID(), f.Evidence)
	}
	for _, m := range matches {
		add(m.PublicID(), m.Left)
		add(m.PublicID(), m.Right)
	}
	return t
}

func parseEvidence(raw json.RawMessage) []evidenceJSON {
	var items []evidenceJSON
	if len(raw) == 0 || json.Unmarshal(raw, &items) != nil {
		return nil
	}
	return items
}

// locatorText renders a locator compactly: "aba=X linha=4", "pagina=3", ...
func locatorText(l map[string]any) string {
	if l == nil {
		return ""
	}
	switch str(l, "type") {
	case "page_box":
		return "pagina=" + strconv.Itoa(int(num(l, "page")))
	case "byte_range":
		return fmt.Sprintf("bytes=%d-%d", int(num(l, "start")), int(num(l, "end")))
	case "time_range":
		return fmt.Sprintf("tempo=%dms-%dms", int(num(l, "start_ms")), int(num(l, "end_ms")))
	default:
		parts := []string{}
		if sheet := str(l, "sheet"); sheet != "" {
			parts = append(parts, "aba="+sheet)
		}
		parts = append(parts, "linha="+strconv.Itoa(int(num(l, "row"))))
		if col := str(l, "column"); col != "" {
			parts = append(parts, "coluna="+col)
		}
		return strings.Join(parts, " ")
	}
}

func summarizeEvidence(raw json.RawMessage) string {
	items := parseEvidence(raw)
	parts := make([]string, 0, len(items))
	for _, e := range items {
		parts = append(parts, e.SourceID+" "+locatorText(e.Locator))
	}
	return strings.Join(parts, "; ")
}
