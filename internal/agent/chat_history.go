package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"

	"github.com/airohq/airo-cli/internal/ui"
)

// history reads the saved conversation through the same API as session view.
// Loading it never sends a prompt or changes a remote turn.
func (s *session) history(ctx context.Context) ([]Turn, error) {
	api := &API{ns: s.ns, a2a: s.api}
	turns, err := api.Turns(ctx, s.agent, s.id, true)
	if errors.Is(err, ErrNotFound) || (err == nil && len(turns) == 0) {
		return nil, fmt.Errorf("no session %s of yours with agent %s in project %s: see yours with airo session list", s.id, s.agent, s.ns)
	}
	if err != nil {
		return nil, fmt.Errorf("load session %s: %w", s.id, err)
	}
	sort.SliceStable(turns, func(i, j int) bool { return turns[i].Started.Before(turns[j].Started) })
	return turns, nil
}

// writeHistory keeps prompts and state on stderr and answers on stdout, as in
// a live conversation. format supplies the terminal's Markdown and styles;
// plain conversations pass nil. History is replayed only in interactive mode.
func writeHistory(turns []Turn, out, errOut io.Writer, format func(from, text string) string) error {
	for _, turn := range turns {
		for _, message := range turn.Messages {
			text, dest := ui.Text(message.Text), out
			if format != nil {
				text = format(message.From, text)
			} else if message.From == FromYou {
				text = "> " + text
			}
			if message.From == FromYou {
				dest = errOut
			}
			if _, err := fmt.Fprintln(dest, text); err != nil {
				return err
			}
		}
		if turn.State != StateDone {
			if _, err := fmt.Fprintf(errOut, "State when loaded: %s\n", ui.Text(turn.State)); err != nil {
				return err
			}
		}
	}
	return nil
}
