// Package ui holds the terminal's capabilities and the CLI's visual language.
package ui

import (
	"io"
	"os"

	"golang.org/x/term"
)

// IsTerminal checks the device, not just whether it is a character device
// (which would also accept /dev/null).
func IsTerminal(stream any) bool {
	f, ok := stream.(interface{ Fd() uintptr })
	return ok && term.IsTerminal(int(f.Fd()))
}

// Interactive means that both the question and the answer reach a person.
func Interactive(in io.Reader, out io.Writer) bool {
	return IsTerminal(in) && IsTerminal(out)
}

// Styled is per stream. Even an empty NO_COLOR disables terminal styling.
func Styled(out io.Writer) bool {
	_, noColor := os.LookupEnv("NO_COLOR")
	return IsTerminal(out) && !noColor && os.Getenv("TERM") != "dumb"
}

// Chat requires all three streams: the answer uses stdout, and the input,
// status and notes use stderr. Plain mode never emits redraw sequences.
func Chat(in io.Reader, out, errOut io.Writer) bool {
	return Interactive(in, out) && Styled(out) && Styled(errOut)
}
