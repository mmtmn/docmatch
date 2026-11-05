package export

import (
	"archive/zip"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"
)

// WriteXLSX writes a minimal OOXML workbook with one sheet per table. All
// cells are inline strings, so nothing is ever evaluated as a formula or
// coerced into a number; SafeCell is applied on top of that so the file
// stays safe when re-exported as CSV by the user.
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

const styles = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
	`<styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">` +
	`<fonts count="2"><font><sz val="11"/><name val="Calibri"/></font><font><b/><sz val="11"/><name val="Calibri"/></font></fonts>` +
	`<fills count="2"><fill><patternFill patternType="none"/></fill><fill><patternFill patternType="gray125"/></fill></fills>` +
	`<borders count="1"><border><left/><right/><top/><bottom/><diagonal/></border></borders>` +
	`<cellStyleXfs count="1"><xf numFmtId="0" fontId="0" fillId="0" borderId="0"/></cellStyleXfs>` +
	`<cellXfs count="2"><xf numFmtId="0" fontId="0" fillId="0" borderId="0" xfId="0"/><xf numFmtId="0" fontId="1" fillId="0" borderId="0" xfId="0" applyFont="1"/></cellXfs>` +
	`</styleSheet>`

func workbook(tables []Table) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`)
	b.WriteString(`<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets>`)
	for i, t := range tables {
		fmt.Fprintf(&b, `<sheet name="%s" sheetId="%d" r:id="rId%d"/>`, xmlEscape(sheetName(t.Name, i)), i+1, i+1)
	}
	b.WriteString(`</sheets></workbook>`)
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
	b.WriteString(`<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>`)
	writeRow(&b, 1, t.Header, 1)
	for i, row := range t.Rows {
		writeRow(&b, i+2, row, 0)
	}
	b.WriteString(`</sheetData></worksheet>`)
	_, err := io.WriteString(w, b.String())
	return err
}

func writeRow(b *strings.Builder, n int, cells []string, style int) {
	fmt.Fprintf(b, `<row r="%d">`, n)
	for i, c := range cells {
		ref := columnName(i) + strconv.Itoa(n)
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
