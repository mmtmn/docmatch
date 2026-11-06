// Package export renders reconciled documents as CSV, XLSX and searchable PDF.
// Untrusted spreadsheet text is neutralized; only application-authored formulas execute.
package export

import (
	"encoding/csv"
	"fmt"
	"io"
	"strings"
	"unicode"
)

// Formats (openapi ExportFormat).
const (
	FormatXLSX = "xlsx"
	FormatCSV  = "csv"
	FormatPDF  = "pdf"
)

// Content types and extensions.
const (
	ContentTypeXLSX = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	ContentTypeCSV  = "text/csv; charset=utf-8"
	ContentTypePDF  = "application/pdf"
)

// Table is one sheet: a header and rows of strings.
type Table struct {
	Name   string
	Header []string
	Rows   [][]string
	Types  []CellType
	Widths []float64
	// Formulas are authored by the application, never copied from source documents.
	Formulas map[string]Formula
}

type CellType uint8

const (
	Text CellType = iota
	Currency
	Integer
	Date
)

type Formula struct {
	Expression string
	Value      string
}

// SafeCell neutralizes a value for spreadsheets. Values that would be
// interpreted as formulas (leading =, +, -, @, or a tab/carriage return,
// also after leading whitespace) get a leading apostrophe, which Excel,
// LibreOffice and Google Sheets show as text. Control characters other
// than tab and newline are dropped.
func SafeCell(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r == '\t' || r == '\n' {
			b.WriteRune(r)
			continue
		}
		if unicode.IsControl(r) {
			continue
		}
		b.WriteRune(r)
	}
	out := b.String()
	trimmed := strings.TrimLeft(out, " \t\n")
	if trimmed == "" {
		return out
	}
	switch trimmed[0] {
	case '=', '+', '-', '@', '\t', '\r', '|', '%':
		return "'" + out
	}
	return out
}

// sanitizeRow applies SafeCell to a row.
func sanitizeRow(row []string) []string {
	out := make([]string, len(row))
	for i, c := range row {
		out[i] = SafeCell(c)
	}
	return out
}

// WriteCSV writes t with a UTF-8 BOM and ";" as separator (what pt-BR
// Excel expects when opening a .csv directly).
func WriteCSV(w io.Writer, t Table) error {
	if _, err := w.Write([]byte{0xEF, 0xBB, 0xBF}); err != nil {
		return err
	}
	cw := csv.NewWriter(w)
	cw.Comma = ';'
	cw.UseCRLF = true
	if err := cw.Write(sanitizeRow(t.Header)); err != nil {
		return err
	}
	for i, row := range t.Rows {
		if err := cw.Write(sanitizeRow(row)); err != nil {
			return fmt.Errorf("export: linha %d: %w", i+1, err)
		}
	}
	cw.Flush()
	return cw.Error()
}
