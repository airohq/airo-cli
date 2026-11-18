package ui

import (
	"image/color"

	"charm.land/lipgloss/v2"
	"github.com/alecthomas/chroma/v2"
	chromastyles "github.com/alecthomas/chroma/v2/styles"
	"github.com/charmbracelet/colorprofile"
)

// Markdown has its own theme, independent of the prompt/focus accent. Codex
// 0.169.0 defaults to Catppuccin Mocha/Latte; the markup scopes are documented
// in DESIGN.md and the syntax styles come from our pinned Chroma dependency.
type markdownTheme struct {
	heading1, heading2, heading color.Color
	code, link, emphasis        color.Color
	quote, bullet, rule         color.Color
	syntax                      *chroma.Style
}

func newMarkdownTheme(p Palette) (markdownTheme, error) {
	if p.Background == nil || p.Profile < colorprofile.ANSI256 {
		return markdownTheme{}, nil
	}
	bg := rgb(p.Background)
	resolve := func(hex string) color.Color { return p.readable(rgb(lipgloss.Color(hex)), bg) }
	name := "catppuccin-mocha"
	red, peach, yellow := "#F38BA8", "#FAB387", "#F9E2AF"
	green, blue, pink, teal := "#A6E3A1", "#89B4FA", "#F5C2E7", "#94E2D5"
	comment := "#6C7086"
	if luminance(bg) >= 0.179 {
		name = "catppuccin-latte"
		red, peach, yellow = "#D20F39", "#FE640B", "#DF8E1D"
		green, blue, pink, teal = "#40A02B", "#1E66F5", "#EA76CB", "#179299"
		comment = "#9CA0B0"
	}
	theme := markdownTheme{
		heading1: resolve(red), heading2: resolve(peach), heading: resolve(yellow),
		code: resolve(green), link: resolve(blue), emphasis: resolve(red),
		quote: resolve(pink), bullet: resolve(teal), rule: resolve(comment),
	}
	// Render temporarily registers its theme in this same map. The bundled
	// theme is immutable, so only the lookup needs to share the registry lock.
	syntaxMu.Lock()
	base := chromastyles.Get(name)
	syntaxMu.Unlock()
	entries := make(chroma.StyleEntries)
	colours := make(map[chroma.Colour]chroma.Colour)
	for _, token := range base.Types() {
		entry := base.Get(token)
		// Keep the terminal's canvas, including inside diff/code tokens that
		// would normally have a theme-specific background or border.
		entry.Background, entry.Border = 0, 0
		if entry.Colour.IsSet() {
			adjusted, ok := colours[entry.Colour]
			if !ok {
				preferred := color.RGBA{entry.Colour.Red(), entry.Colour.Green(), entry.Colour.Blue(), 255}
				c := rgb(p.readable(preferred, bg))
				adjusted = chroma.NewColour(c.R, c.G, c.B)
				colours[entry.Colour] = adjusted
			}
			entry.Colour = adjusted
		}
		entries[token] = entry.String()
	}
	var err error
	theme.syntax, err = chroma.NewStyle(syntaxThemeName, entries)
	return theme, err
}
