package ui

import (
	"fmt"
	"image/color"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"charm.land/glamour/v2/ansi"
	"charm.land/lipgloss/v2"
	"github.com/alecthomas/chroma/v2"
	chromastyles "github.com/alecthomas/chroma/v2/styles"
	"github.com/charmbracelet/colorprofile"
	xansi "github.com/charmbracelet/x/ansi"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
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

// MarkdownRenderer keeps prose on the terminal's canvas and gives Markdown
// and code their own theme, separate from the surrounding chat's accent.
type MarkdownRenderer struct {
	options ansi.Options
	parser  parser.Parser
	colours markdownTheme
	syntax  *chroma.Style
}

const syntaxThemeName = "airo-terminal"

// Glamour looks syntax themes up in Chroma's global registry. Scope our entry
// to a render so theme updates cannot leave stale colours or grow that registry.
var syntaxMu sync.Mutex

func (r *MarkdownRenderer) Render(input string) (string, error) {
	if r.syntax == nil {
		return r.render(input)
	}
	syntaxMu.Lock()
	defer syntaxMu.Unlock()
	previous, exists := chromastyles.Registry[syntaxThemeName]
	chromastyles.Register(r.syntax)
	defer func() {
		if exists {
			chromastyles.Registry[syntaxThemeName] = previous
		} else {
			delete(chromastyles.Registry, syntaxThemeName)
		}
	}()
	return r.render(input)
}

func (r *MarkdownRenderer) render(input string) (string, error) {
	source := []byte(input)
	doc := r.parser.Parse(text.NewReader(source))
	return r.renderBlocks(doc, source, r.options.WordWrap)
}

func Markdown(width int, palette Palette) (*MarkdownRenderer, error) {
	bold, italic, underline := true, true, true
	indent, quoteIndent := uint(2), uint(1)
	colours, err := newMarkdownTheme(palette)
	if err != nil {
		return nil, err
	}
	quote, column, row, center := "│ ", "│", "─", "┼"
	r := &MarkdownRenderer{syntax: colours.syntax, colours: colours,
		parser: goldmark.New(goldmark.WithExtensions(extension.GFM, extension.DefinitionList),
			goldmark.WithParserOptions(parser.WithAutoHeadingID())).Parser(),
	}
	theme, formatter := "", "terminal16m"
	if r.syntax != nil {
		theme = syntaxThemeName
		if palette.Profile == colorprofile.ANSI256 {
			formatter = "terminal256"
		}
	}
	r.options = ansi.Options{
		WordWrap: max(1, width), ChromaFormatter: formatter,
		Styles: ansi.StyleConfig{
			Document:              ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{BlockSuffix: "\n"}},
			Heading:               ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Color: markdownColor(colours.heading), BlockSuffix: "\n"}},
			H1:                    ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Color: markdownColor(colours.heading1), Bold: &bold, Underline: &underline}},
			H2:                    ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Color: markdownColor(colours.heading2), Bold: &bold}},
			H3:                    ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Bold: &bold, Italic: &italic}},
			H4:                    ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Italic: &italic}},
			H5:                    ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Italic: &italic}},
			H6:                    ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Italic: &italic}},
			Strong:                ansi.StylePrimitive{Color: markdownColor(colours.emphasis), Bold: &bold},
			Emph:                  ansi.StylePrimitive{Color: markdownColor(colours.emphasis), Italic: &italic},
			Strikethrough:         ansi.StylePrimitive{CrossedOut: &bold},
			BlockQuote:            ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Color: markdownColor(colours.quote)}, Indent: &quoteIndent, IndentToken: &quote},
			List:                  ansi.StyleList{LevelIndent: 2},
			Item:                  ansi.StylePrimitive{Color: markdownColor(colours.bullet), Prefix: "• "},
			Enumeration:           ansi.StylePrimitive{BlockPrefix: ". "},
			Task:                  ansi.StyleTask{Ticked: "[x] ", Unticked: "[ ] "},
			Link:                  ansi.StylePrimitive{Color: markdownColor(colours.link), Underline: &underline},
			LinkText:              ansi.StylePrimitive{Color: markdownColor(colours.link), Underline: &underline},
			Code:                  ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Color: markdownColor(colours.code)}},
			CodeBlock:             ansi.StyleCodeBlock{StyleBlock: ansi.StyleBlock{Indent: &indent}, Theme: theme},
			HorizontalRule:        ansi.StylePrimitive{Color: markdownColor(colours.rule), Format: "────────\n"},
			ImageText:             ansi.StylePrimitive{Format: "Image: {{.text}} → "},
			Table:                 ansi.StyleTable{CenterSeparator: &center, ColumnSeparator: &column, RowSeparator: &row},
			DefinitionDescription: ansi.StylePrimitive{BlockPrefix: "\n  "},
		},
	}
	return r, nil
}

func markdownColor(c color.Color) *string {
	if c == nil {
		return nil
	}
	var value string
	switch c := c.(type) {
	case lipgloss.NoColor:
		return nil
	case xansi.BasicColor:
		value = strconv.Itoa(int(c))
	case xansi.IndexedColor:
		value = strconv.Itoa(int(c))
	default:
		value = syntaxColor(c)
	}
	return &value
}

func syntaxColor(c color.Color) string {
	if c == nil {
		return ""
	}
	if _, none := c.(lipgloss.NoColor); none {
		return ""
	}
	v := rgb(c)
	return fmt.Sprintf("#%02x%02x%02x", v.R, v.G, v.B)
}

// Wrap also wraps long words, without losing any characters.
func Wrap(text string, width int) string {
	return xansi.Wrap(text, max(1, width), "")
}
