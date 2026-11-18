package ui

import (
	"fmt"
	"io"
)

// ResumeHint keeps the command alone on a line so the whole line can be
// copied. The caller separates this hint from the preceding output.
func ResumeHint(out io.Writer, command string) {
	NewStyles(out).ResumeHint(out, command)
}

// ResumeHint retains a theme already detected by an interactive command.
func (s Styles) ResumeHint(out io.Writer, command string) {
	fmt.Fprintln(out, s.Muted.Render("Resume this session with:"))
	fmt.Fprintln(out, command)
}
