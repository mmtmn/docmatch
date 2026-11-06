package export

import (
	"embed"
	"fmt"
	"io"
	"strings"
	"time"

	"codeberg.org/go-pdf/fpdf"
)

//go:embed fonts/*.ttf
var pdfFonts embed.FS

type ReportMetric struct{ Label, Value string }
type ReportFinding struct {
	Title, Amount, Status, Explanation string
	Evidence                           []string
}
type Report struct {
	Title, Subtitle, Reference string
	Notes                      []string
	Metrics                    []ReportMetric
	Tables                     []Table
	Findings                   []ReportFinding
}

// WritePDF produces a real, searchable document with embedded Unicode fonts. It never
// interprets uploaded text as HTML, JavaScript, a link or a drawing instruction.
func WritePDF(w io.Writer, report Report) error {
	p := fpdf.New("P", "mm", "A4", "")
	regular, err := pdfFonts.ReadFile("fonts/DejaVuSans.ttf")
	if err != nil {
		return err
	}
	bold, err := pdfFonts.ReadFile("fonts/DejaVuSans-Bold.ttf")
	if err != nil {
		return err
	}
	p.AddUTF8FontFromBytes("Docmatch", "", regular)
	p.AddUTF8FontFromBytes("Docmatch", "B", bold)
	p.SetMargins(16, 22, 16)
	p.SetAutoPageBreak(false, 18)
	p.SetTitle(report.Title, true)
	p.SetAuthor("docmatch", true)
	p.SetCreationDate(time.Now().UTC())
	p.SetModificationDate(time.Now().UTC())
	p.AliasNbPages("")
	p.SetHeaderFunc(func() {
		p.SetXY(16, 10)
		p.SetFont("Docmatch", "B", 10)
		p.SetTextColor(29, 78, 216)
		p.CellFormat(178, 6, "docmatch", "", 0, "L", false, 0, "")
		p.SetDrawColor(220, 226, 234)
		p.Line(16, 18, 194, 18)
		p.SetY(23)
	})
	p.SetFooterFunc(func() {
		p.SetXY(16, 282)
		p.SetFont("Docmatch", "", 7)
		p.SetTextColor(100, 110, 125)
		p.CellFormat(135, 5, report.Reference, "", 0, "L", false, 0, "")
		p.CellFormat(43, 5, fmt.Sprintf("Página %d de {nb}", p.PageNo()), "", 0, "R", false, 0, "")
	})
	p.AddPage()
	body := func(text string, size float64, bold bool) {
		style := ""
		if bold {
			style = "B"
		}
		p.SetFont("Docmatch", style, size)
		p.SetTextColor(27, 35, 49)
		lineH := size*0.45 + 0.8
		for _, line := range p.SplitText(cleanPDF(text), 178) {
			if p.GetY()+lineH > 276 {
				p.AddPage()
				p.SetFont("Docmatch", style, size)
				p.SetTextColor(27, 35, 49)
			}
			p.SetX(16)
			p.CellFormat(178, lineH, line, "", 1, "L", false, 0, "")
		}
	}
	body(report.Title, 22, true)
	p.Ln(3)
	body(report.Subtitle, 10, false)
	p.Ln(8)
	for i, m := range report.Metrics {
		if i%2 == 0 && p.GetY()+30 > 276 {
			p.AddPage()
		}
		x := 16.0
		if i%2 == 1 {
			x = 108
		}
		y := p.GetY()
		p.SetFillColor(244, 247, 252)
		p.RoundedRect(x, y, 86, 26, 2, "1234", "F")
		p.SetXY(x+4, y+4)
		p.SetFont("Docmatch", "", 8)
		p.SetTextColor(75, 85, 99)
		p.CellFormat(78, 5, m.Label, "", 1, "L", false, 0, "")
		p.SetXY(x+4, y+11)
		p.SetFont("Docmatch", "B", 15)
		p.SetTextColor(27, 35, 49)
		p.CellFormat(78, 9, m.Value, "", 0, "L", false, 0, "")
		if i%2 == 0 && i+1 < len(report.Metrics) {
			p.SetY(y)
		} else {
			p.SetY(y + 31)
		}
	}
	p.Ln(3)
	for _, note := range report.Notes {
		body(note, 9, false)
		p.Ln(3)
	}
	for _, table := range report.Tables {
		p.AddPage()
		body(table.Name, 17, true)
		p.Ln(6)
		widths := table.Widths
		if len(widths) != len(table.Header) {
			widths = make([]float64, len(table.Header))
			for i := range widths {
				widths[i] = 178 / float64(len(widths))
			}
		}
		total := 0.0
		for _, v := range widths {
			total += v
		}
		scale := 178 / total
		widths = append([]float64(nil), widths...)
		for i := range widths {
			widths[i] *= scale
		}
		row := func(values []string, header bool) {
			p.SetFont("Docmatch", "", 8)
			if header {
				p.SetFont("Docmatch", "B", 8)
			}
			lines := make([][]string, len(table.Header))
			maxLines := 1
			for i := range lines {
				value := ""
				if i < len(values) {
					value = cleanPDF(values[i])
				}
				lines[i] = p.SplitText(value, widths[i]-4)
				if len(lines[i]) > maxLines {
					maxLines = len(lines[i])
				}
			}
			height := float64(maxLines)*3.8 + 4
			y := p.GetY()
			x := 16.0
			for i, part := range lines {
				if header {
					p.SetFillColor(29, 78, 216)
					p.SetTextColor(255, 255, 255)
				} else {
					p.SetFillColor(247, 249, 252)
					p.SetTextColor(27, 35, 49)
				}
				p.Rect(x, y, widths[i], height, "F")
				p.SetXY(x+2, y+2)
				for _, line := range part {
					p.SetX(x + 2)
					p.CellFormat(widths[i]-4, 3.8, line, "", 1, "L", false, 0, "")
				}
				p.SetDrawColor(222, 228, 237)
				p.Line(x, y+height, x+widths[i], y+height)
				x += widths[i]
			}
			p.SetXY(16, y+height)
		}
		row(table.Header, true)
		nextTablePage := func() {
			p.AddPage()
			body(table.Name+" (continuação)", 13, true)
			p.Ln(4)
			row(table.Header, true)
		}
		for _, values := range table.Rows {
			p.SetFont("Docmatch", "", 8)
			parts := make([][]string, len(table.Header))
			count := 1
			for i := range parts {
				if i < len(values) {
					parts[i] = p.SplitText(cleanPDF(values[i]), widths[i]-4)
				}
				count = max(count, len(parts[i]))
			}
			height := float64(count)*3.8 + 4
			if p.GetY()+height > 276 {
				nextTablePage()
			}
			// Very long source fields continue on the next page, never outside its margins.
			for offset := 0; offset < count; {
				capacity := int((276 - p.GetY() - 4) / 3.8)
				if capacity < 1 {
					nextTablePage()
					continue
				}
				end := min(count, offset+capacity)
				chunk := make([]string, len(parts))
				for i, lines := range parts {
					if offset < len(lines) {
						chunk[i] = strings.Join(lines[offset:min(end, len(lines))], "\n")
					}
				}
				row(chunk, false)
				offset = end
				if offset < count {
					nextTablePage()
				}
			}
		}
	}
	if len(report.Findings) > 0 {
		p.AddPage()
		body("Pendências e evidências", 18, true)
		p.Ln(5)
		body("Os valores abaixo podem se sobrepor. Não os some como perda financeira. As análises da IA são sugestões para revisão.", 9, false)
		p.Ln(7)
		textHeight := func(text string, size float64, bold bool) float64 {
			style := ""
			if bold {
				style = "B"
			}
			p.SetFont("Docmatch", style, size)
			return float64(len(p.SplitText(cleanPDF(text), 178))) * (size*0.45 + 0.8)
		}
		for i, f := range report.Findings {
			title := fmt.Sprintf("%02d  %s", i+1, f.Title)
			height := textHeight(title, 11, true) + 1 + textHeight(f.Amount+"  ·  "+f.Status, 8, false) + 3 + textHeight(f.Explanation, 9, false) + 3 + 11
			for _, e := range f.Evidence {
				height += textHeight("Fonte: "+e, 8, false)
			}
			// Keep a finding and its evidence together when they fit on one page.
			// This avoids orphaned amounts and references at the top of the next page.
			if p.GetY() > 235 || (height <= 253 && p.GetY()+height > 276) {
				p.AddPage()
			}
			body(title, 11, true)
			p.Ln(1)
			body(f.Amount+"  ·  "+f.Status, 8, false)
			p.Ln(3)
			body(f.Explanation, 9, false)
			p.Ln(3)
			for _, e := range f.Evidence {
				body("Fonte: "+e, 8, false)
			}
			p.Ln(5)
			p.SetDrawColor(220, 226, 234)
			p.Line(16, p.GetY(), 194, p.GetY())
			p.Ln(6)
		}
	}
	if err := p.Error(); err != nil {
		return err
	}
	return p.Output(w)
}
func cleanPDF(text string) string {
	return strings.NewReplacer("\x00", "", "\t", "    ", "\u2011", "-", "\u2013", "-", "\u2014", "-").Replace(text)
}
