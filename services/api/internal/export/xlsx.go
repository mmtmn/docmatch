package export

import (
	"archive/zip"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// WriteXLSX writes styled, typed tables. Untrusted strings remain inline text;
// only application-authored formulas and explicitly typed columns become numeric cells.
func WriteXLSX(w io.Writer, tables []Table) error {
	if len(tables) == 0 {
		return fmt.Errorf("export: nenhuma planilha")
	}
	zw := zip.NewWriter(w)
	write := func(name, content string) error {
		f, err := zw.Create(name)
		if err != nil {
			return err
		}
		_, err = io.WriteString(f, content)
		return err
	}
	if err := write("[Content_Types].xml", contentTypes(len(tables))); err != nil {
		return err
	}
	if err := write("_rels/.rels", rootRels); err != nil {
		return err
	}
	if err := write("xl/workbook.xml", workbook(tables)); err != nil {
		return err
	}
	if err := write("xl/_rels/workbook.xml.rels", workbookRels(len(tables))); err != nil {
		return err
	}
	if err := write("xl/styles.xml", styles); err != nil {
		return err
	}
	for i, t := range tables {
		f, err := zw.Create(fmt.Sprintf("xl/worksheets/sheet%d.xml", i+1))
		if err != nil {
			return err
		}
		if err := writeSheet(f, t); err != nil {
			return err
		}
	}
	return zw.Close()
}

func contentTypes(n int) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`)
	b.WriteString(`<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">`)
	b.WriteString(`<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>`)
	b.WriteString(`<Default Extension="xml" ContentType="application/xml"/>`)
	b.WriteString(`<Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/>`)
	b.WriteString(`<Override PartName="/xl/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"/>`)
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, `<Override PartName="/xl/worksheets/sheet%d.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>`, i)
	}
	b.WriteString(`</Types>`)
	return b.String()
}

const rootRels = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
	`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
	`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/>` +
	`</Relationships>`

const styles = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">
<numFmts count="2"><numFmt numFmtId="164" formatCode="&quot;R$&quot; #,##0.00;[Red]-&quot;R$&quot; #,##0.00"/><numFmt numFmtId="165" formatCode="dd/mm/yyyy"/></numFmts>
<fonts count="2"><font><sz val="11"/><color rgb="FF182230"/><name val="Calibri"/></font><font><b/><sz val="11"/><color rgb="FFFFFFFF"/><name val="Calibri"/></font></fonts>
<fills count="3"><fill><patternFill patternType="none"/></fill><fill><patternFill patternType="gray125"/></fill><fill><patternFill patternType="solid"><fgColor rgb="FF1D4ED8"/><bgColor indexed="64"/></patternFill></fill></fills>
<borders count="1"><border><left/><right/><top/><bottom><color rgb="FFE2E8F0"/></bottom><diagonal/></border></borders>
<cellStyleXfs count="1"><xf numFmtId="0" fontId="0" fillId="0" borderId="0"/></cellStyleXfs>
<cellXfs count="5">
<xf numFmtId="0" fontId="0" fillId="0" borderId="0" xfId="0" applyAlignment="1"><alignment vertical="top" wrapText="1"/></xf>
<xf numFmtId="0" fontId="1" fillId="2" borderId="0" xfId="0" applyAlignment="1"><alignment vertical="center" wrapText="1"/></xf>
<xf numFmtId="164" fontId="0" fillId="0" borderId="0" xfId="0" applyNumberFormat="1" applyAlignment="1"><alignment horizontal="right" vertical="top"/></xf>
<xf numFmtId="1" fontId="0" fillId="0" borderId="0" xfId="0" applyNumberFormat="1" applyAlignment="1"><alignment horizontal="right" vertical="top"/></xf>
<xf numFmtId="165" fontId="0" fillId="0" borderId="0" xfId="0" applyNumberFormat="1" applyAlignment="1"><alignment vertical="top"/></xf>
</cellXfs><cellStyles count="1"><cellStyle name="Normal" xfId="0" builtinId="0"/></cellStyles>
</styleSheet>`

func workbook(tables []Table) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`)
	b.WriteString(`<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets>`)
	for i, t := range tables {
		fmt.Fprintf(&b, `<sheet name="%s" sheetId="%d" r:id="rId%d"/>`, xmlEscape(sheetName(t.Name, i)), i+1, i+1)
	}
	b.WriteString(`</sheets><calcPr calcId="191029" fullCalcOnLoad="1"/></workbook>`)
	return b.String()
}

func workbookRels(n int) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`)
	b.WriteString(`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">`)
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, `<Relationship Id="rId%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet%d.xml"/>`, i, i)
	}
	fmt.Fprintf(&b, `<Relationship Id="rId%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/>`, n+1)
	b.WriteString(`</Relationships>`)
	return b.String()
}

// sheetName bounds the name to Excel's rules (31 chars, no []:*?/\).
func sheetName(name string, i int) string {
	if name == "" {
		name = "Planilha" + strconv.Itoa(i+1)
	}
	name = strings.Map(func(r rune) rune {
		if strings.ContainsRune(`[]:*?/\`, r) {
			return '_'
		}
		return r
	}, name)
	if utf8.RuneCountInString(name) > 31 {
		name = string([]rune(name)[:31])
	}
	return name
}

func writeSheet(w io.Writer, t Table) error {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`)
	b.WriteString(`<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetPr><pageSetUpPr fitToPage="1"/></sheetPr><sheetViews><sheetView workbookViewId="0"><pane ySplit="1" topLeftCell="A2" activePane="bottomLeft" state="frozen"/></sheetView></sheetViews><cols>`)
	for i := range t.Header {
		width := 24.0
		if i < len(t.Widths) {
			width = t.Widths[i]
		}
		fmt.Fprintf(&b, `<col min="%d" max="%d" width="%.1f" customWidth="1"/>`, i+1, i+1, width)
	}
	b.WriteString(`</cols><sheetData>`)
	writeRow(&b, 1, t.Header, t, true)
	for i, row := range t.Rows {
		writeRow(&b, i+2, row, t, false)
	}
	b.WriteString(`</sheetData>`)
	if len(t.Header) > 0 {
		fmt.Fprintf(&b, `<autoFilter ref="A1:%s%d"/>`, columnName(len(t.Header)-1), len(t.Rows)+1)
	}
	b.WriteString(`<pageMargins left="0.3" right="0.3" top="0.5" bottom="0.5" header="0.2" footer="0.2"/><pageSetup paperSize="9" orientation="landscape" fitToWidth="1" fitToHeight="0"/></worksheet>`)
	_, err := io.WriteString(w, b.String())
	return err
}

func writeRow(b *strings.Builder, n int, cells []string, t Table, header bool) {
	height := 26.0
	for i, c := range cells {
		width := 24.0
		if i < len(t.Widths) {
			width = t.Widths[i]
		}
		lines := 0.0
		for _, line := range strings.Split(c, "\n") {
			lines += math.Max(1, math.Ceil(float64(utf8.RuneCountInString(line))/math.Max(1, width-2)))
		}
		height = math.Max(height, lines*15+8)
	}
	fmt.Fprintf(b, `<row r="%d" ht="%.1f" customHeight="1">`, n, math.Min(height, 409))
	for i, c := range cells {
		ref := columnName(i) + strconv.Itoa(n)
		style := 0
		kind := Text
		if i < len(t.Types) {
			kind = t.Types[i]
		}
		if header {
			style = 1
		} else if kind == Currency {
			style = 2
		} else if kind == Integer {
			style = 3
		} else if kind == Date {
			style = 4
		}
		if f, ok := t.Formulas[ref]; ok && !header {
			fmt.Fprintf(b, `<c r="%s" s="%d"><f>%s</f><v>%s</v></c>`, ref, style, xmlEscape(f.Expression), xmlEscape(f.Value))
			continue
		}
		if !header && kind != Text && c != "" {
			number := strings.ReplaceAll(c, ",", ".")
			if kind == Date {
				if date, err := time.Parse("2006-01-02", c); err == nil {
					number = strconv.FormatInt(int64(date.Sub(time.Date(1899, 12, 30, 0, 0, 0, 0, time.UTC)).Hours()/24), 10)
				} else {
					number = ""
				}
			}
			if value, err := strconv.ParseFloat(number, 64); err == nil && !math.IsInf(value, 0) && !math.IsNaN(value) {
				fmt.Fprintf(b, `<c r="%s" s="%d"><v>%s</v></c>`, ref, style, number)
				continue
			}
		}
		fmt.Fprintf(b, `<c r="%s" t="inlineStr" s="%d"><is><t xml:space="preserve">%s</t></is></c>`, ref, style, xmlEscape(SafeCell(c)))
	}
	b.WriteString(`</row>`)
}

// columnName renders 0-based column indexes as A, B, ..., Z, AA, AB...
func columnName(i int) string {
	var s string
	i++
	for i > 0 {
		i--
		s = string(rune('A'+i%26)) + s
		i /= 26
	}
	return s
}

// xmlEscape escapes text for XML and drops characters XML 1.0 forbids.
func xmlEscape(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '&':
			b.WriteString("&amp;")
		case r == '<':
			b.WriteString("&lt;")
		case r == '>':
			b.WriteString("&gt;")
		case r == '"':
			b.WriteString("&quot;")
		case r == '\t' || r == '\n' || r == '\r':
			b.WriteRune(r)
		case r < 0x20 || r == 0xFFFE || r == 0xFFFF || (r >= 0xD800 && r <= 0xDFFF):
			// forbidden in XML 1.0
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
