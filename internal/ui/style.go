package ui

import (
	"io"

	"charm.land/lipgloss/v2"
)

// The terminal's own palette keeps these roles readable in the user's theme.
const (
	mutedColor   = "8"
	accentColor  = "6"
	successColor = "2"
	warningColor = "3"
	errorColor   = "1"
)

// Styles names meaning rather than shades. Outcomes still need a word or a
// symbol; none of these styles changes the text it renders.
type Styles struct {
	Default, Muted, Accent, Success, Warning, Error lipgloss.Style
}

func NewStyles(out io.Writer) Styles {
	s := Styles{}
	if Styled(out) {
		s.Muted = lipgloss.NewStyle().Foreground(lipgloss.Color(mutedColor))
		s.Accent = lipgloss.NewStyle().Foreground(lipgloss.Color(accentColor))
		s.Success = lipgloss.NewStyle().Foreground(lipgloss.Color(successColor))
		s.Warning = lipgloss.NewStyle().Foreground(lipgloss.Color(warningColor))
		s.Error = lipgloss.NewStyle().Foreground(lipgloss.Color(errorColor))
	}
	return s
}
