// Package wizard asks for missing create arguments in the terminal.
package wizard

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/spf13/cobra"

	"github.com/airohq/airo-cli/internal/picker"
	"github.com/airohq/airo-cli/internal/ui"
)

type Wizard struct {
	ctx     context.Context
	in      io.Reader
	out     io.Writer
	lines   *bufio.Reader
	header  string
	palette ui.Palette
}

// New returns nil when input, results or questions are redirected. The
// heading appears only if the command actually needs to ask a question.
func New(cmd *cobra.Command, kind, project string) *Wizard {
	return WithHeader(cmd, fmt.Sprintf("Creating %s in project %s", kind, ui.Text(project)))
}

// WithHeader is New for a task other than creating a resource, such as
// adding a skill to an agent.
func WithHeader(cmd *cobra.Command, header string) *Wizard {
	in, out := cmd.InOrStdin(), cmd.ErrOrStderr()
	if !ui.Interactive(in, cmd.OutOrStdout()) || !ui.IsTerminal(out) {
		return nil
	}
	return &Wizard{ctx: cmd.Context(), in: in, out: out, lines: bufio.NewReader(in),
		header: header, palette: ui.NewPalette(out)}
}

// Plain asks on ordinary lines, as on a terminal without styling, so that
// a test can answer.
func Plain(ctx context.Context, in io.Reader, out io.Writer) *Wizard {
	return &Wizard{ctx: ctx, in: in, out: out, lines: bufio.NewReader(in), palette: ui.NewPalette(out)}
}

var errCanceled = errors.New("creation canceled")

// Canceled says whether the person left a question or a choice, with Esc
// or Ctrl+C. Leaving a choice that answers a question may go back to the
// question rather than end the wizard.
func Canceled(err error) bool {
	return errors.Is(err, errCanceled)
}

func (w *Wizard) begin() {
	if w.header != "" {
		fmt.Fprintln(w.out, w.header)
		fmt.Fprintln(w.out)
		w.header = ""
	}
}

func (w *Wizard) Name(args []string) (string, error) {
	if len(args) == 1 {
		if strings.TrimSpace(args[0]) == "" {
			return "", errors.New("NAME cannot be empty")
		}
		return args[0], nil
	}
	if w == nil {
		return "", errors.New("provide NAME as an argument when running without a terminal")
	}
	return w.Required("Name")
}

func (w *Wizard) Required(label string) (string, error) {
	for {
		answer, err := w.ask(label + " (required)")
		if err != nil || answer != "" {
			return answer, err
		}
		fmt.Fprintln(w.out, "Enter a value.")
	}
}

// Optional asks a question that an empty answer skips.
func (w *Wizard) Optional(label string) (string, error) {
	return w.ask(label + " (Enter to skip)")
}

// Problem says why an answer did not work, before the question is asked
// again.
func (w *Wizard) Problem(err error) {
	fmt.Fprintln(w.out, err)
}

// Note shows a result of an answer, such as the version it resolved to.
func (w *Wizard) Note(text string) {
	fmt.Fprintln(w.out, w.palette.Styles().Muted.Render(text))
}

func (w *Wizard) Confirm(label string, def bool) (bool, error) {
	hint := " (y/N)"
	if def {
		hint = " (Y/n)"
	}
	for {
		answer, err := w.ask(label + hint)
		if err != nil {
			return false, err
		}
		switch strings.ToLower(answer) {
		case "":
			return def, nil
		case "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		}
		fmt.Fprintln(w.out, "Enter y or n.")
	}
}

func (w *Wizard) Pick(label string, names []string) (string, error) {
	return w.PickLinked(label, names, nil)
}

// PickLinked is Pick with a web page for each name, which a click on the
// name opens in a terminal that knows hyperlinks.
func (w *Wizard) PickLinked(label string, names, pages []string) (string, error) {
	w.begin()
	input := w.in
	if !ui.Styled(w.out) {
		input = w.lines
	}
	name, err := picker.Linked(w.ctx, input, w.out, label, names, pages, -1)
	if err == nil && name == "" {
		return "", errCanceled
	}
	return name, err
}

func (w *Wizard) ask(label string) (string, error) {
	w.begin()
	if !ui.Styled(w.out) {
		fmt.Fprintf(w.out, "? %s › ", label)
		line, err := w.lines.ReadString('\n')
		if errors.Is(err, io.EOF) {
			return "", errCanceled
		}
		return strings.TrimSpace(line), err
	}
	m := &question{input: textinput.New(), label: label, palette: w.palette}
	m.input.Prompt = ""
	_, err := tea.NewProgram(m, tea.WithContext(w.ctx), tea.WithInput(w.in), tea.WithOutput(w.out),
		tea.WithColorProfile(w.palette.Profile)).Run()
	w.palette = m.palette
	if err != nil {
		return "", err
	}
	if !m.answered {
		return "", errCanceled
	}
	return strings.TrimSpace(m.input.Value()), nil
}

// Read theme replies and keystrokes together, as chat does, so the first
// question is editable even in a terminal that never reports its colours.
type question struct {
	input    textinput.Model
	label    string
	palette  ui.Palette
	answered bool
}

func (q *question) Init() tea.Cmd { return tea.Batch(q.input.Focus(), tea.RequestBackgroundColor) }

func (q *question) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.BackgroundColorMsg:
		q.palette.Background = msg.Color
	case tea.ColorProfileMsg:
		q.palette.Profile = msg.Profile
	case tea.KeyPressMsg:
		switch msg.String() {
		case "enter":
			q.answered = true
			return q, tea.Quit
		case "ctrl+c", "esc", "ctrl+d":
			return q, tea.Quit
		}
	}
	var cmd tea.Cmd
	q.input, cmd = q.input.Update(msg)
	return q, cmd
}

func (q *question) View() tea.View {
	prefix := q.palette.Styles().Accent.Render("?") + " " + q.label + " › "
	if q.answered {
		return tea.NewView(prefix + ui.Text(q.input.Value()))
	}
	return tea.NewView(prefix + q.input.View())
}
