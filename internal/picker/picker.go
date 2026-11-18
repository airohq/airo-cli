// Package picker is the picker every command shows when the person has to
// choose one name from a list.
package picker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/ktr0731/go-fuzzyfinder"
	"golang.org/x/term"
)

// One shows the names in a picker the person filters by typing, with the
// prompt in front of what they type. The cursor starts on names[current],
// which is marked as the current one; pass -1 when there is none. It returns
// an empty name when the person left without choosing.
//
// The picker opens the terminal itself, so call it only when Terminal says
// someone is there.
func One(ctx context.Context, prompt string, names []string, current int) (string, error) {
	i, err := fuzzyfinder.Find(names,
		func(i int) string {
			if i == current {
				return names[i] + " (current)"
			}
			return names[i]
		},
		fuzzyfinder.WithContext(ctx),
		fuzzyfinder.WithPromptString(prompt+"> "),
		fuzzyfinder.WithPreselected(func(i int) bool { return i == current }),
	)
	if errors.Is(err, fuzzyfinder.ErrAbort) || ctx.Err() != nil {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("the picker did not open: %w", err)
	}
	return names[i], nil
}

// Terminal reports whether a person types the input.
func Terminal(in io.Reader) bool {
	f, ok := in.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}
