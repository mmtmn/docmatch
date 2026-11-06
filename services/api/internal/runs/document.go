package runs

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mmtmn/docmatch/services/api/internal/export"
)

// ReconciliationDocument is the preserved output of the engine, independent of AI advice.
// All monetary fields are centavos. Proposed receipts never reduce the confirmed balance.
type ReconciliationDocument struct {
	Version     int                    `json:"version"`
	Title       string                 `json:"title"`
	Currency    string                 `json:"currency"`
	PeriodStart string                 `json:"period_start"`
	PeriodEnd   string                 `json:"period_end"`
	Receivables []ReconciledReceivable `json:"receivables"`
	BankEntries []ReconciledBankEntry  `json:"bank_entries"`
	Totals      ReconciliationTotals   `json:"totals"`
}
type ReconciledReceivable struct {
	ID                 string   `json:"id"`
	SourceID           string   `json:"source_id"`
	SourceName         string   `json:"source_name"`
	SourceRow          int      `json:"source_row"`
	Reference          string   `json:"reference"`
	Customer           string   `json:"customer"`
	TaxID              string   `json:"tax_id"`
	DueDate            string   `json:"due_date"`
	PaidOn             string   `json:"paid_on"`
	OriginalAmount     int64    `json:"original_amount"`
	ReceivedAmount     int64    `json:"received_amount"`
	AppliedPrincipal   int64    `json:"applied_principal"`
	OpenAmount         int64    `json:"open_amount"`
	ProposedAmount     int64    `json:"proposed_amount"`
	FineReceived       int64    `json:"fine_received"`
	WithholdingPending int64    `json:"withholding_pending"`
	Status             string   `json:"status"`
	DuplicateOf        string   `json:"duplicate_of"`
	BankRows           []int    `json:"bank_rows"`
	Flags              []string `json:"flags"`
}
type ReconciledBankEntry struct {
	Row         int      `json:"row"`
	Date        string   `json:"date"`
	Description string   `json:"description"`
	Amount      int64    `json:"amount"`
	Kind        string   `json:"kind"`
	Proposed    bool     `json:"proposed"`
	MatchedIDs  []string `json:"matched_ids"`
	Rule        string   `json:"rule"`
	SourceID    string   `json:"source_id"`
	SourceName  string   `json:"source_name"`
}
type ReconciliationTotals struct {
	OriginalAmount       int64 `json:"original_amount"`
	DuplicateExcluded    int64 `json:"duplicate_excluded"`
	ReceivedConfirmed    int64 `json:"received_confirmed"`
	PrincipalConfirmed   int64 `json:"principal_confirmed"`
	OpenBalance          int64 `json:"open_balance"`
	ProposedAmount       int64 `json:"proposed_amount"`
	ProjectedOpenBalance int64 `json:"projected_open_balance"`
	UnappliedCash        int64 `json:"unapplied_cash"`
	BankFees             int64 `json:"bank_fees"`
	FinesReceived        int64 `json:"fines_received"`
	WithholdingPending   int64 `json:"withholding_pending"`
}

func receivableStatus(code string) string {
	switch code {
	case "paid":
		return "Liquidado"
	case "partial":
		return "Parcial"
	case "duplicate":
		return "Duplicado"
	case "review":
		return "Vínculo a confirmar"
	case "withholding":
		return "Retenção a comprovar"
	default:
		return "Em aberto"
	}
}
func bankStatus(line ReconciledBankEntry) string {
	if line.Proposed {
		return "Vínculo a confirmar"
	}
	switch line.Kind {
	case "receipt":
		return "Recebimento conciliado"
	case "fee":
		return "Tarifa bancária"
	case "supplier_payment":
		return "Pagamento a fornecedor"
	default:
		return "Sem vínculo confirmado"
	}
}
func bankRows(rows []int) string {
	v := make([]string, len(rows))
	for i, n := range rows {
		v[i] = strconv.Itoa(n)
	}
	return strings.Join(v, ", ")
}
func documentNotes() []string {
	return []string{
		"Versão conciliada dos dados enviados. Os arquivos originais foram preservados; nenhuma alteração foi enviada ao ERP ou ao banco.",
		"Recebimentos confirmados atualizam os saldos. Duplicatas permanecem visíveis, com a referência correspondente, e são excluídas da soma consolidada.",
		"Vínculos propostos, retenções sem comprovante e encargos estimados não são baixados automaticamente. O saldo projetado só vale se os vínculos propostos forem confirmados.",
		"Este documento cobre as fontes e o período apresentados. Não atesta o fechamento contábil completo da empresa.",
	}
}

func documentTables(d *ReconciliationDocument) []export.Table {
	t := d.Totals
	summary := export.Table{Name: "Resumo", Header: []string{"Indicador", "Valor (BRL)"}, Types: []export.CellType{export.Text, export.Currency}, Widths: []float64{56, 24}, Formulas: map[string]export.Formula{}}
	metrics := []struct {
		name  string
		value int64
	}{
		{"Carteira original", t.OriginalAmount}, {"Duplicidade excluída da soma", t.DuplicateExcluded}, {"Principal baixado por recebimentos confirmados", t.PrincipalConfirmed}, {"Saldo em aberto confirmado", t.OpenBalance}, {"Recebimentos propostos a confirmar", t.ProposedAmount}, {"Saldo projetado após confirmação", t.ProjectedOpenBalance}, {"Recebido confirmado, incluindo multas", t.ReceivedConfirmed}, {"Caixa sem vínculo confirmado", t.UnappliedCash}, {"Retenção a comprovar (incluída no saldo aberto)", t.WithholdingPending}, {"Tarifas bancárias identificadas", t.BankFees}, {"Multas recebidas", t.FinesReceived},
	}
	for _, m := range metrics {
		summary.Rows = append(summary.Rows, []string{m.name, Money(m.value)})
	}
	ledger := export.Table{Name: "Carteira conciliada", Header: []string{"ID", "Documento", "Cliente", "Fonte", "Vencimento", "Valor original", "Recebido confirmado", "Principal baixado", "Saldo em aberto", "Proposta a confirmar", "Situação", "Duplicado de", "Último recebimento", "Linhas do extrato", "CNPJ informado"}, Types: []export.CellType{export.Text, export.Text, export.Text, export.Text, export.Date, export.Currency, export.Currency, export.Currency, export.Currency, export.Currency, export.Text, export.Text, export.Date, export.Text, export.Text}, Widths: []float64{10, 22, 42, 44, 16, 20, 22, 22, 22, 22, 27, 15, 20, 20, 23}, Formulas: map[string]export.Formula{}}
	for i, r := range d.Receivables {
		ledger.Rows = append(ledger.Rows, []string{r.ID, r.Reference, r.Customer, r.SourceName, r.DueDate, Money(r.OriginalAmount), Money(r.ReceivedAmount), Money(r.AppliedPrincipal), Money(r.OpenAmount), Money(r.ProposedAmount), receivableStatus(r.Status), r.DuplicateOf, r.PaidOn, bankRows(r.BankRows), r.TaxID})
		n := i + 2
		ledger.Formulas[fmt.Sprintf("I%d", n)] = export.Formula{Expression: fmt.Sprintf(`IF(K%d="Duplicado",0,MAX(0,F%d-H%d))`, n, n, n), Value: decimal(r.OpenAmount)}
	}
	if n := len(d.Receivables) + 1; n > 1 {
		for _, v := range []struct {
			cell, col string
			value     int64
		}{{"B2", "F", t.OriginalAmount}, {"B4", "H", t.PrincipalConfirmed}, {"B5", "I", t.OpenBalance}, {"B6", "J", t.ProposedAmount}, {"B8", "G", t.ReceivedConfirmed}} {
			summary.Formulas[v.cell] = export.Formula{Expression: fmt.Sprintf("SUM('Carteira conciliada'!%s2:%s%d)", v.col, v.col, n), Value: decimal(v.value)}
		}
		summary.Formulas["B3"] = export.Formula{Expression: fmt.Sprintf(`SUMIF('Carteira conciliada'!K2:K%d,"Duplicado",'Carteira conciliada'!F2:F%d)`, n, n), Value: decimal(t.DuplicateExcluded)}
		summary.Formulas["B7"] = export.Formula{Expression: "B5-B6", Value: decimal(t.ProjectedOpenBalance)}
	}
	bank := export.Table{Name: "Extrato conciliado", Header: []string{"Linha", "Data", "Descrição original", "Movimento (BRL)", "Classificação", "Títulos vinculados", "Fonte"}, Types: []export.CellType{export.Integer, export.Date, export.Text, export.Currency}, Widths: []float64{10, 16, 58, 24, 30, 26, 48}}
	for _, r := range d.BankEntries {
		bank.Rows = append(bank.Rows, []string{strconv.Itoa(r.Row), r.Date, r.Description, Money(r.Amount), bankStatus(r), strings.Join(r.MatchedIDs, ", "), r.SourceName})
	}
	notes := export.Table{Name: "Como usar", Header: []string{"Orientação"}, Widths: []float64{110}}
	for _, n := range documentNotes() {
		notes.Rows = append(notes.Rows, []string{n})
	}
	notes.Rows = append(notes.Rows, []string{"Na carteira, valores originais e referências identificam a fonte. Principal baixado e situação podem ser revisados; o saldo e o resumo usam fórmulas. Recebido inclui multas; principal baixado não inclui multas nem retenções pendentes."})
	return []export.Table{summary, ledger, bank, notes}
}
func decimal(cents int64) string { return strings.ReplaceAll(Money(cents), ",", ".") }

func reconciliationReport(r Run, d *ReconciliationDocument, findings []Finding, sourceNames map[string]string) export.Report {
	report := export.Report{Title: "Relatório de conciliação", Subtitle: "Resultados e evidências dos documentos enviados", Reference: r.PublicID(), Notes: documentNotes()}
	if d != nil {
		report.Title = d.Title
		if d.PeriodStart != "" && d.PeriodEnd != "" {
			report.Subtitle = fmt.Sprintf("Período: %s a %s | %d títulos | %d movimentos bancários", dateBR(d.PeriodStart), dateBR(d.PeriodEnd), len(d.Receivables), len(d.BankEntries))
		}
		report.Metrics = []export.ReportMetric{{Label: "Saldo em aberto confirmado", Value: currency(d.Totals.OpenBalance)}, {Label: "Principal baixado", Value: currency(d.Totals.PrincipalConfirmed)}, {Label: "Duplicidade excluída da soma", Value: currency(d.Totals.DuplicateExcluded)}, {Label: "Recebimentos a confirmar", Value: currency(d.Totals.ProposedAmount)}}
		report.Notes = append(report.Notes, fmt.Sprintf("Saldo projetado após confirmar vínculos: %s. Caixa sem vínculo confirmado: %s. Retenção ainda a comprovar, incluída no saldo aberto: %s.", currency(d.Totals.ProjectedOpenBalance), currency(d.Totals.UnappliedCash), currency(d.Totals.WithholdingPending)))
		ledger := export.Table{Name: "Carteira conciliada", Header: []string{"Documento / ID", "Cliente", "Original", "Recebido", "Em aberto", "Situação"}, Widths: []float64{32, 42, 27, 27, 27, 23}}
		sources := map[string]bool{}
		for _, v := range d.Receivables {
			ledger.Rows = append(ledger.Rows, []string{v.Reference + "\n" + v.ID, v.Customer, currency(v.OriginalAmount), currency(v.ReceivedAmount), currency(v.OpenAmount), receivableStatus(v.Status)})
			sources[v.SourceName] = true
		}
		if len(ledger.Rows) > 0 {
			report.Tables = append(report.Tables, ledger)
		}
		bank := export.Table{Name: "Extrato conciliado", Header: []string{"Linha / data", "Descrição", "Movimento", "Classificação / títulos"}, Widths: []float64{25, 79, 29, 45}}
		for _, v := range d.BankEntries {
			bank.Rows = append(bank.Rows, []string{fmt.Sprintf("%d / %s", v.Row, dateBR(v.Date)), v.Description, currency(v.Amount), bankStatus(v) + "\n" + strings.Join(v.MatchedIDs, ", ")})
			sources[v.SourceName] = true
		}
		if len(bank.Rows) > 0 {
			report.Tables = append(report.Tables, bank)
		}
		names := make([]string, 0, len(sources))
		for name := range sources {
			if name != "" {
				names = append(names, name)
			}
		}
		sort.Strings(names)
		for _, name := range names {
			report.Notes = append(report.Notes, "Fonte: "+name)
		}
	} else {
		report.Notes = []string{"Esta execução preservou os achados e as evidências, mas não a carteira reconciliada. Para gerar a versão atualizada dos documentos, execute uma nova conciliação com os arquivos originais."}
	}
	for _, f := range findings {
		status := map[string]string{"open": "A revisar", "confirmed": "Confirmado na revisão", "dismissed": "Descartado na revisão", "resolved": "Resolvido na revisão"}[f.Status]
		if status == "" {
			status = "A revisar"
		}
		item := export.ReportFinding{Title: f.Title, Amount: currency(f.AmountAtRisk), Status: status, Explanation: f.Explanation}
		if f.Feedback != nil && f.Feedback.Note != "" {
			item.Explanation += "\nNota da revisão: " + f.Feedback.Note
		}
		for _, e := range parseEvidence(f.Evidence) {
			name := e.SourceID
			if sourceNames[e.SourceID] != "" {
				name = sourceNames[e.SourceID]
			}
			if d != nil {
				for _, row := range d.Receivables {
					if row.SourceID == e.SourceID {
						name = row.SourceName
						break
					}
				}
				for _, row := range d.BankEntries {
					if row.SourceID == e.SourceID {
						name = row.SourceName
						break
					}
				}
			}
			item.Evidence = append(item.Evidence, strings.TrimSpace(name+" "+locatorText(e.Locator)+" - "+e.Excerpt))
		}
		report.Findings = append(report.Findings, item)
	}
	return report
}
func dateBR(value string) string {
	if d, err := time.Parse("2006-01-02", value); err == nil {
		return d.Format("02/01/2006")
	}
	return value
}
func currency(cents int64) string {
	s := Money(cents)
	whole, fraction, _ := strings.Cut(s, ",")
	sign := ""
	if strings.HasPrefix(whole, "-") {
		sign = "-"
		whole = strings.TrimPrefix(whole, "-")
	}
	for i := len(whole) - 3; i > 0; i -= 3 {
		whole = whole[:i] + "." + whole[i:]
	}
	return sign + "R$ " + whole + "," + fraction
}
