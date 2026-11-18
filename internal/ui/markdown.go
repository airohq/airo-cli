package ui

import (
	"strings"
	"unicode"

	"charm.land/glamour/v2"
	"charm.land/glamour/v2/ansi"
	xansi "github.com/charmbracelet/x/ansi"
)

// Text strips terminal controls from text supplied by an agent or a prompt.
// Plain output keeps the original bytes; only the terminal needs this guard.
func Text(text string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, xansi.Strip(text))
}

// Markdown uses the default foreground for prose and code. It deliberately
// avoids a syntax-highlighting theme with its own colours or background.
func Markdown(width int) (*glamour.TermRenderer, error) {
	bold, italic, underline := true, true, true
	indent := uint(2)
	quote, column, row, center := "│ ", "│", "─", "┼"
	return glamour.NewTermRenderer(
		glamour.WithWordWrap(max(1, width)),
		glamour.WithTableWrap(true),
		glamour.WithStyles(ansi.StyleConfig{
			Document:              ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{BlockSuffix: "\n"}},
			Heading:               ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Bold: &bold, BlockSuffix: "\n"}},
			Strong:                ansi.StylePrimitive{Bold: &bold},
			Emph:                  ansi.StylePrimitive{Italic: &italic},
			Strikethrough:         ansi.StylePrimitive{CrossedOut: &bold},
			BlockQuote:            ansi.StyleBlock{Indent: &indent, IndentToken: &quote},
			List:                  ansi.StyleList{LevelIndent: 2},
			Item:                  ansi.StylePrimitive{BlockPrefix: "• "},
			Enumeration:           ansi.StylePrimitive{BlockPrefix: ". "},
			Task:                  ansi.StyleTask{Ticked: "[x] ", Unticked: "[ ] "},
			Link:                  ansi.StylePrimitive{Underline: &underline},
			LinkText:              ansi.StylePrimitive{Bold: &bold},
			Code:                  ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Prefix: "`", Suffix: "`"}},
			CodeBlock:             ansi.StyleCodeBlock{StyleBlock: ansi.StyleBlock{Indent: &indent}},
			HorizontalRule:        ansi.StylePrimitive{Format: "────────\n"},
			ImageText:             ansi.StylePrimitive{Format: "Image: {{.text}} → "},
			Table:                 ansi.StyleTable{CenterSeparator: &center, ColumnSeparator: &column, RowSeparator: &row},
			DefinitionDescription: ansi.StylePrimitive{BlockPrefix: "\n  "},
		}),
	)
}

// Wrap also wraps long words, without losing any characters.
func Wrap(text string, width int) string {
	return xansi.Wrap(text, max(1, width), "")
}
