package ui

import (
	"image/color"
	"io"
	"os"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
)

// Palette combines terminal capabilities with optional theme reports. A nil
// foreground or background means the terminal has not reported that colour.
// The caller owns detection; constructing a palette never reads input or waits
// for terminal replies.
type Palette struct {
	Profile                colorprofile.Profile
	Foreground, Background color.Color
}

func NewPalette(out io.Writer) Palette {
	if !Styled(out) {
		return Palette{Profile: colorprofile.NoTTY}
	}
	// Env does no I/O. Bubble Tea can refine this profile once it starts.
	return Palette{Profile: colorprofile.Env(os.Environ())}
}

// Styles names meaning rather than shades. Outcomes still need a word or a
// symbol; none of these styles changes the text it renders.
type Styles struct {
	Default, Muted, Accent, Success, Warning, Error lipgloss.Style
}

func NewStyles(out io.Writer) Styles {
	return NewPalette(out).Styles()
}

func (p Palette) Styles() Styles {
	if p.Profile < colorprofile.ANSI {
		return Styles{}
	}
	s := Styles{
		Accent:  lipgloss.NewStyle().Bold(true),
		Warning: lipgloss.NewStyle().Bold(true),
		// Outcome colours belong to the user's terminal theme. Always pair
		// them with a word or marker; colour alone never carries meaning.
		Success: lipgloss.NewStyle().Foreground(ansi.Green),
		Error:   lipgloss.NewStyle().Foreground(ansi.Red),
	}
	// The first 16 palette entries are user-configurable. Without their
	// measured RGB values we cannot promise readable cyan, yellow or grey.
	if p.Background == nil || p.Profile == colorprofile.ANSI {
		return s
	}
	bg := rgb(p.Background)
	accent, warning := rgb(lipgloss.Color("#226D7A")), rgb(lipgloss.Color("#826021"))
	if luminance(bg) < 0.179 {
		accent, warning = rgb(lipgloss.Color("#6BB8C4")), rgb(lipgloss.Color("#C6A46A"))
	}
	s.Accent = lipgloss.NewStyle().Foreground(p.readable(accent, bg))
	s.Warning = lipgloss.NewStyle().Foreground(p.readable(warning, bg))
	if p.Foreground != nil {
		muted := blend(bg, rgb(p.Foreground), 0.6)
		s.Muted = lipgloss.NewStyle().Foreground(p.readable(muted, bg))
	}
	return s
}
