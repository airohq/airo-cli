package ui

import (
	"bytes"
	"fmt"
	"image/color"
	"strings"

	"charm.land/glamour/v2/ansi"
	xansi "github.com/charmbracelet/x/ansi"
	"github.com/yuin/goldmark/ast"
	extast "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/util"
)

// Keep Glamour's inline and code rendering, but lay out lists ourselves so
// wrapping accounts for the marker. Work on parsed nodes, never rendered text:
// a numbered line inside a code block must remain code.
func (r *MarkdownRenderer) renderBlocks(parent ast.Node, source []byte, width int) (string, error) {
	var blocks []string
	group := ast.NewDocument()
	flush := func() error {
		if group.ChildCount() == 0 {
			return nil
		}
		out, err := r.renderGroup(group, source, width)
		if out != "" {
			blocks = append(blocks, out)
		}
		group = ast.NewDocument()
		return err
	}
	for node := parent.FirstChild(); node != nil; {
		next := node.NextSibling()
		if !layoutBlock(node) {
			group.AppendChild(group, node)
			node = next
			continue
		}
		if err := flush(); err != nil {
			return "", err
		}
		var out string
		var err error
		switch node := node.(type) {
		case *ast.List:
			out, err = r.renderList(node, source, width)
		case *ast.Blockquote:
			quoted := *r
			quoted.options.Styles.Document.Color = markdownColor(r.colours.quote)
			out, err = quoted.renderBlocks(node, source, max(1, width-2))
			out = prefixLines(out, markdownStyle(r.colours.quote).Styled("│ "))
		}
		if err != nil {
			return "", err
		}
		blocks = append(blocks, out)
		node = next
	}
	if err := flush(); err != nil {
		return "", err
	}
	return strings.Join(blocks, "\n\n"), nil
}

func layoutBlock(node ast.Node) bool {
	if node.Kind() == ast.KindList {
		return true
	}
	if node.Kind() == ast.KindBlockquote {
		for child := node.FirstChild(); child != nil; child = child.NextSibling() {
			if layoutBlock(child) {
				return true
			}
		}
	}
	return false
}

func (r *MarkdownRenderer) renderGroup(doc ast.Node, source []byte, width int) (string, error) {
	options := r.options
	options.WordWrap = max(1, width)
	render := renderer.NewRenderer(renderer.WithNodeRenderers(util.Prioritized(ansi.NewRenderer(options), 1000)))
	var buf bytes.Buffer
	if err := render.Render(&buf, source, doc); err != nil {
		return "", err
	}
	lines := strings.Split(buf.String(), "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " ")
		if strings.TrimSpace(xansi.Strip(line)) == "" {
			lines[i] = ""
		}
	}
	return strings.Trim(strings.Join(lines, "\n"), "\n"), nil
}

func (r *MarkdownRenderer) renderList(list *ast.List, source []byte, width int) (string, error) {
	var items []string
	number := list.Start
	for item := list.FirstChild(); item != nil; item = item.NextSibling() {
		marker, colour := "• ", r.colours.bullet
		if list.IsOrdered() {
			marker, colour = fmt.Sprintf("%d. ", number), r.colours.link
			number++
		}
		if first := item.FirstChild(); first != nil {
			if check, ok := first.FirstChild().(*extast.TaskCheckBox); ok {
				marker = "[ ] "
				if check.IsChecked {
					marker = "[x] "
				}
				first.RemoveChild(first, check)
			}
		}
		indent := xansi.StringWidth(marker)
		var parts []string
		for child := item.FirstChild(); child != nil; {
			// Nested lists start four columns beyond their parent's marker.
			if nested, ok := child.(*ast.List); ok {
				out, err := r.renderList(nested, source, max(1, width-4))
				if err != nil {
					return "", err
				}
				parts = append(parts, prefixLines(out, "    "))
				child = child.NextSibling()
				continue
			}
			group := ast.NewDocument()
			for child != nil && child.Kind() != ast.KindList {
				next := child.NextSibling()
				// Tight lists use TextBlock; paragraphs provide normal wrapping.
				if child.Kind() == ast.KindTextBlock {
					paragraph := ast.NewParagraph()
					for child.FirstChild() != nil {
						paragraph.AppendChild(paragraph, child.FirstChild())
					}
					group.AppendChild(group, paragraph)
				} else {
					group.AppendChild(group, child)
				}
				child = next
			}
			out, err := r.renderBlocks(group, source, max(1, width-indent))
			if err != nil {
				return "", err
			}
			out = prefixLines(out, strings.Repeat(" ", indent))
			if len(parts) == 0 {
				out = markdownStyle(colour).Styled(marker) + strings.TrimPrefix(out, strings.Repeat(" ", indent))
			}
			parts = append(parts, out)
		}
		if len(parts) == 0 {
			parts = append(parts, markdownStyle(colour).Styled(strings.TrimSpace(marker)))
		}
		items = append(items, strings.Join(parts, "\n"))
	}
	separator := "\n"
	if !list.IsTight {
		separator = "\n\n"
	}
	return strings.Join(items, separator), nil
}

func prefixLines(text, prefix string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		if line != "" {
			lines[i] = prefix + line
		}
	}
	return strings.Join(lines, "\n")
}

func markdownStyle(colour color.Color) xansi.Style {
	style := xansi.Style{}
	if colour != nil {
		style = style.ForegroundColor(colour)
	}
	return style
}
