package ui

import (
	"fmt"
	"io"
	"strings"
	"unicode"

	xansi "github.com/charmbracelet/x/ansi"
)

// Link makes the text open the address when clicked, in a terminal that
// knows OSC 8 hyperlinks, such as iTerm2. Elsewhere it is the text alone,
// so piped output stays plain.
func Link(out io.Writer, address, text string) string {
	if address == "" || !Styled(out) {
		return text
	}
	return Linked(address, text)
}

// Linked links the text whatever the stream, for a view that is styled as
// a whole. An address with terminal controls is left out. The text keeps
// its colour and gets a solid underline, as Codex shows links, rather than
// the dotted one some terminals draw for a link.
func Linked(address, text string) string {
	if address == "" || strings.ContainsFunc(address, func(r rune) bool {
		return unicode.IsControl(r) || unicode.IsSpace(r)
	}) {
		return text
	}
	return xansi.SetHyperlink(address) + xansi.SGR(xansi.UnderlineAttr) + text +
		xansi.SGR(xansi.NoUnderlineAttr) + xansi.ResetHyperlink()
}

// Table writes rows in columns three spaces apart, as a tabwriter would,
// but measures what a cell shows, so that a linked cell lines up.
func Table(out io.Writer, rows [][]string) error {
	var widths []int
	for _, row := range rows {
		for i, cell := range row {
			if i == len(widths) {
				widths = append(widths, 0)
			}
			widths[i] = max(widths[i], xansi.StringWidth(cell))
		}
	}
	var b strings.Builder
	for _, row := range rows {
		for i, cell := range row {
			b.WriteString(cell)
			if i < len(row)-1 {
				b.WriteString(strings.Repeat(" ", widths[i]-xansi.StringWidth(cell)+3))
			}
		}
		b.WriteByte('\n')
	}
	_, err := fmt.Fprint(out, b.String())
	return err
}
