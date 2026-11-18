package ui

import (
	"fmt"
	"io"
)

// ResumeHint keeps the command alone on a line so the whole line can be
// copied. The caller separates this hint from the preceding output.
func ResumeHint(out io.Writer, command string) {
	fmt.Fprintln(out, NewStyles(out).Muted.Render("Resume this session with:"))
	fmt.Fprintln(out, command)
}
