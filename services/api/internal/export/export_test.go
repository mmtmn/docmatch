package export

import (
	"archive/zip"
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestSafeCell(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"texto normal":             "texto normal",
		"=1+1":                     "'=1+1",
		"+55 11":                   "'+55 11",
		"-12,50":                   "'-12,50",
		"@SUM(A1)":                 "'@SUM(A1)",
		"  =cmd|' /C calc'!A0":     "'  =cmd|' /C calc'!A0",
		"\t=1":                     "'\t=1",
		"a\x00b\x07c":              "abc",
		"linha 1\nlinha 2":         "linha 1\nlinha 2",
		"|pipe":                    "'|pipe",
		"%percent":                 "'%percent",
		"":                         "",
		"   ":                      "   ",
		"R$ 1.234,56":              "R$ 1.234,56",
		"=HYPERLINK(\"http://x\")": "'=HYPERLINK(\"http://x\")",
		"\r\n=1":                   "'\n=1",
	}
	for in, want := range cases {
		if got := SafeCell(in); got != want {
			t.Errorf("SafeCell(%q) = %q, esperado %q", in, got, want)
		}
	}
}

func TestWriteCSV(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	err := WriteCSV(&buf, Table{Header: []string{"id", "=título"}, Rows: [][]string{{"fnd_1", "=1+1"}, {"fnd_2", "ok; com ponto e vírgula"}}})
	if err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.HasPrefix(out, "\xEF\xBB\xBF") {
		t.Fatal("BOM ausente")
	}
	if !strings.Contains(out, `id;'=título`) || !strings.Contains(out, `fnd_1;'=1+1`) || !strings.Contains(out, `"ok; com ponto e vírgula"`) {
		t.Fatalf("csv = %q", out)
	}
	if strings.Contains(out, "\r\n=") {
		t.Fatal("fórmula sem escape")
	}
}

func TestWriteXLSX(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	tables := []Table{
		{Name: "Achados [1]", Header: []string{"id", "valor"}, Rows: [][]string{{"fnd_1", "=SUM(A1)"}, {"fnd_2", "<b>&\"x\"\x01"}}},
		{Name: "", Header: []string{"x"}},
	}
	if err := WriteXLSX(&buf, tables); err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(rc)
		_ = rc.Close()
		files[f.Name] = string(data)
	}
	for _, name := range []string{"[Content_Types].xml", "_rels/.rels", "xl/workbook.xml", "xl/_rels/workbook.xml.rels", "xl/styles.xml", "xl/worksheets/sheet1.xml", "xl/worksheets/sheet2.xml"} {
		if _, ok := files[name]; !ok {
			t.Errorf("%s ausente", name)
		}
	}
	sheet := files["xl/worksheets/sheet1.xml"]
	if !strings.Contains(sheet, `t="inlineStr"`) || !strings.Contains(sheet, `'=SUM(A1)`) {
		t.Fatalf("célula sem escape: %s", sheet)
	}
	if !strings.Contains(sheet, "&lt;b&gt;&amp;&quot;x&quot;") || strings.Contains(sheet, "\x01") {
		t.Fatalf("xml sem escape: %s", sheet)
	}
	if !strings.Contains(files["xl/workbook.xml"], `name="Achados _1_"`) || !strings.Contains(files["xl/workbook.xml"], `name="Planilha2"`) {
		t.Fatalf("workbook = %s", files["xl/workbook.xml"])
	}
	if err := WriteXLSX(&buf, nil); err == nil {
		t.Fatal("sem planilhas deveria falhar")
	}
	if columnName(0) != "A" || columnName(25) != "Z" || columnName(26) != "AA" || columnName(701) != "ZZ" {
		t.Fatal("columnName")
	}
	long := sheetName(strings.Repeat("x", 40), 0)
	if len([]rune(long)) != 31 {
		t.Fatal("nome de aba longo")
	}
}

// A document's text is untrusted; only declared numeric cells and our own formulas
// may become executable spreadsheet cells. Excel must see money as numbers.
func TestTypedWorkbookKeepsSourceTextInert(t *testing.T) {
	var b strings.Builder
	table := Table{Header: []string{"Cliente", "Recebido", "Vencimento", "Saldo"}, Types: []CellType{Text, Currency, Date, Currency}, Rows: [][]string{{"=HYPERLINK(\"https://example.com\")", "1234,56", "2026-07-31", "10,00"}}, Formulas: map[string]Formula{"D2": {Expression: "B2-1224.56", Value: "10.00"}}}
	if err := writeSheet(&b, table); err != nil {
		t.Fatal(err)
	}
	sheet := b.String()
	for _, fragment := range []string{`<c r="A2" t="inlineStr" s="0">`, `'=HYPERLINK`, `<c r="B2" s="2"><v>1234.56</v></c>`, `<c r="C2" s="4"><v>46234</v></c>`, `<f>B2-1224.56</f><v>10.00</v>`, `state="frozen"`, `<autoFilter ref="A1:D2"/>`} {
		if !strings.Contains(sheet, fragment) {
			t.Errorf("missing %s", fragment)
		}
	}
}

func TestPDFContainsDocumentAndSurvivesLongEvidence(t *testing.T) {
	var b bytes.Buffer
	report := Report{Title: "Conciliação de recebíveis", Reference: "run_fixture", Notes: []string{"Originais preservados. Retenção a comprovar."}, Findings: []ReportFinding{{Title: "Pagamento parcial", Explanation: strings.Repeat("Evidência com acentuação e valores. ", 300)}}}
	if err := WritePDF(&b, report); err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(b.Bytes(), []byte("%PDF-")) || !bytes.Contains(b.Bytes(), []byte("%%EOF")) {
		t.Fatal("invalid PDF framing")
	}
	if !bytes.Contains(b.Bytes(), []byte("/ToUnicode")) {
		t.Fatal("searchable Unicode font mapping missing")
	}
}
