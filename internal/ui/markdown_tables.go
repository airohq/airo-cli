package ui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/yuin/goldmark/ast"
	extast "github.com/yuin/goldmark/extension/ast"
)

// Tables follow Codex's open layout: one column of cell padding on each side,
// two columns between cells, a heavy header rule and light body-row rules.
func (r *MarkdownRenderer) renderTable(table *extast.Table, source []byte, width int) (string, error) {
	columns := len(table.Alignments)
	if columns == 0 {
		return "", nil
	}
	var rows [][]string
	for row := table.FirstChild(); row != nil; row = row.NextSibling() {
		cells := make([]string, columns)
		column := 0
		for cell := row.FirstChild(); cell != nil && column < columns; cell = cell.NextSibling() {
			doc, paragraph := ast.NewDocument(), ast.NewParagraph()
			for cell.FirstChild() != nil {
				paragraph.AppendChild(paragraph, cell.FirstChild())
			}
			doc.AppendChild(doc, paragraph)
			inline := *r
			if len(rows) == 0 {
				bold := true
				inline.options.Styles.Document.Color = markdownColor(r.colours.heading)
				inline.options.Styles.Document.Bold = &bold
			}
			out, err := inline.renderGroup(doc, source, 0)
			if err != nil {
				return "", err
			}
			cells[column] = out
			column++
		}
		rows = append(rows, cells)
	}
	if len(rows) == 0 {
		return "", nil
	}
	widths := tableWidths(rows, width)
	if widths == nil {
		return r.tableRecords(rows, width), nil
	}
	rule := func(character string) string {
		parts := make([]string, columns)
		for i, size := range widths {
			parts[i] = strings.Repeat(character, size+2)
		}
		return markdownStyle(r.colours.rule).Styled(strings.Join(parts, "  "))
	}
	var lines []string
	for i, row := range rows {
		if i == 1 {
			lines = append(lines, rule("━"))
		} else if i > 1 {
			lines = append(lines, rule("─"))
		}
		lines = append(lines, tableRow(row, widths, table.Alignments)...)
	}
	if len(rows) == 1 {
		lines = append(lines, rule("━"))
	}
	return strings.Join(lines, "\n"), nil
}

// Give short columns their natural width, then shrink the widest columns
// until the table fits. Keep words intact; when that is impossible, records
// are easier to read than narrow columns full of split identifiers.
func tableWidths(rows [][]string, width int) []int {
	columns := len(rows[0])
	available := width - 2*columns - 2*(columns-1)
	widths, minimum := make([]int, columns), make([]int, columns)
	for _, row := range rows {
		for column, cell := range row {
			plain := ansi.Strip(cell)
			widths[column] = max(widths[column], ansi.StringWidth(plain), 1)
			minimum[column] = max(minimum[column], 1)
			for _, word := range strings.Fields(plain) {
				minimum[column] = max(minimum[column], ansi.StringWidth(word))
			}
		}
	}
	total, minTotal := 0, 0
	for i := range widths {
		widths[i] = min(widths[i], max(1, available))
		total += widths[i]
		minTotal += minimum[i]
	}
	if minTotal > available {
		return nil
	}
	for total > available {
		widest := -1
		for i := range widths {
			if widths[i] > minimum[i] && (widest < 0 || widths[i] > widths[widest]) {
				widest = i
			}
		}
		widths[widest]--
		total--
	}
	return widths
}

func tableRow(row []string, widths []int, alignments []extast.Alignment) []string {
	cells := make([][]string, len(row))
	height := 1
	for i, cell := range row {
		cells[i] = strings.Split(lipgloss.Wrap(cell, widths[i], ""), "\n")
		height = max(height, len(cells[i]))
	}
	lines := make([]string, height)
	for line := range height {
		var out strings.Builder
		for column, cell := range cells {
			text := ""
			if line < len(cell) {
				text = cell[line]
			}
			remaining := max(0, widths[column]-ansi.StringWidth(text))
			left := 0
			switch alignments[column] {
			case extast.AlignRight:
				left = remaining
			case extast.AlignCenter:
				left = remaining / 2
			}
			if column > 0 {
				out.WriteString("  ")
			}
			out.WriteString(strings.Repeat(" ", left+1))
			out.WriteString(text)
			out.WriteString(strings.Repeat(" ", remaining-left+1))
		}
		lines[line] = strings.TrimRight(out.String(), " ")
	}
	return lines
}

func (r *MarkdownRenderer) tableRecords(rows [][]string, width int) string {
	var records []string
	for _, row := range rows[1:] {
		var fields []string
		for column, value := range row {
			label := rows[0][column]
			if strings.TrimSpace(ansi.Strip(label)) == "" {
				label = markdownStyle(r.colours.heading).Bold().Styled(fmt.Sprintf("Column %d", column+1))
			}
			fields = append(fields, lipgloss.Wrap(label+": "+value, max(1, width), ""))
		}
		records = append(records, strings.Join(fields, "\n"))
	}
	if len(rows) == 1 {
		return lipgloss.Wrap(strings.Join(rows[0], " | "), max(1, width), "")
	}
	return strings.Join(records, "\n\n")
}
