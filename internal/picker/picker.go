// Package picker lets a person choose from a list inline in the terminal.
package picker

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/airohq/airo-cli/internal/ui"
)

// One returns the original name, or an empty string if the person cancels.
// current marks the active entry. With no active entry (-1), Enter selects
// the first option. Callers put the project default first when there is one.
func One(ctx context.Context, in io.Reader, out io.Writer, noun string, names []string, current int) (string, error) {
	return Linked(ctx, in, out, noun, names, nil, current)
}

// Linked is One with a web page for each name, which a click on the name
// opens in a terminal that knows hyperlinks. An empty page links nothing.
func Linked(ctx context.Context, in io.Reader, out io.Writer, noun string, names, pages []string, current int) (string, error) {
	if len(names) == 0 {
		return "", fmt.Errorf("no %s choices available", noun)
	}
	m := &menu{label: "Choose a " + noun, names: names, current: current, palette: ui.NewPalette(out),
		filter: textinput.New(), visible: 6}
	m.filter.Prompt = ""
	m.resetMatches()
	if !ui.Styled(out) {
		name, err := m.plain(ctx, bufio.NewReader(in), out)
		if name != "" {
			fmt.Fprintf(out, "? %s › %s\n", m.label, ui.Text(name))
		}
		return name, err
	}
	m.pages = pages
	_, err := tea.NewProgram(m, tea.WithInput(in), tea.WithOutput(out), tea.WithContext(ctx),
		tea.WithColorProfile(m.palette.Profile)).Run()
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	return m.answer, err
}

type menu struct {
	label           string
	names           []string
	pages           []string // linked only in the styled view
	current         int
	palette         ui.Palette
	filter          textinput.Model
	matches         []int
	cursor, visible int
	answer          string
	done            bool
}

func (m *menu) resetMatches() {
	query := strings.ToLower(m.filter.Value())
	m.matches = m.matches[:0]
	m.cursor = 0
	for i, name := range m.names {
		if strings.Contains(strings.ToLower(name), query) {
			if i == m.current && query == "" {
				m.cursor = len(m.matches)
			}
			m.matches = append(m.matches, i)
		}
	}
}

func (m *menu) option(i int) string {
	name := ui.Text(m.names[i])
	if i < len(m.pages) && m.pages[i] != "" {
		name = ui.Linked(m.pages[i], name)
	}
	if i == m.current {
		name += " (current)"
	}
	return name
}

func (m *menu) Init() tea.Cmd {
	return tea.Batch(m.filter.Focus(), tea.RequestBackgroundColor, tea.RequestForegroundColor)
}

func (m *menu) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.ColorProfileMsg:
		m.palette.Profile = msg.Profile
	case tea.BackgroundColorMsg:
		m.palette.Background = msg.Color
	case tea.ForegroundColorMsg:
		m.palette.Foreground = msg.Color
	case tea.WindowSizeMsg:
		m.visible = max(1, min(6, msg.Height-3))
	case tea.KeyPressMsg:
		switch msg.String() {
		case "esc", "ctrl+c", "ctrl+d":
			m.done = true
			return m, tea.Quit
		case "up":
			m.cursor = max(0, m.cursor-1)
			return m, nil
		case "down":
			if m.cursor+1 < len(m.matches) {
				m.cursor++
			}
			return m, nil
		case "enter":
			if len(m.matches) > 0 {
				m.answer = m.names[m.matches[m.cursor]]
				m.done = true
				return m, tea.Quit
			}
			return m, nil
		}
	}
	before := m.filter.Value()
	var cmd tea.Cmd
	m.filter, cmd = m.filter.Update(msg)
	if m.filter.Value() != before {
		m.resetMatches()
	}
	return m, cmd
}

func (m *menu) View() tea.View {
	s := m.palette.Styles()
	prompt := s.Accent.Render("?") + " " + m.label
	if m.done {
		if m.answer == "" {
			return tea.NewView("")
		}
		return tea.NewView(prompt + " › " + ui.Text(m.answer))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s › %s", prompt, m.filter.View())
	start := max(0, m.cursor-m.visible+1)
	end := min(len(m.matches), start+m.visible)
	for pos := start; pos < end; pos++ {
		name := m.option(m.matches[pos])
		if pos == m.cursor {
			fmt.Fprintf(&b, "\n%s %s", s.Accent.Render("›"), name)
		} else {
			fmt.Fprintf(&b, "\n  %s", name)
		}
	}
	if len(m.matches) == 0 {
		b.WriteString("\n  No matches")
	}
	if len(m.matches) > m.visible {
		fmt.Fprintf(&b, "\n  %d–%d of %d", start+1, end, len(m.matches))
	}
	fmt.Fprintf(&b, "\n%s", s.Muted.Render("↑/↓ move · type to filter · Enter choose · Esc cancel"))
	return tea.NewView(b.String())
}

// A plain terminal keeps the same choices and default using ordinary lines.
func (m *menu) plain(ctx context.Context, in *bufio.Reader, out io.Writer) (string, error) {
	fmt.Fprintf(out, "? %s\n", m.label)
	for i := range m.names {
		fmt.Fprintf(out, "  %d. %s\n", i+1, m.option(i))
	}
	for {
		fmt.Fprintf(out, "  Number [%d] (q to cancel) › ", m.cursor+1)
		line, err := ui.ReadLine(ctx, in)
		if errors.Is(err, io.EOF) {
			return "", nil
		}
		if err != nil {
			return "", err
		}
		line = strings.TrimSpace(line)
		switch line {
		case "":
			return m.names[m.cursor], nil
		case "q", "\x1b":
			return "", nil
		}
		n, err := strconv.Atoi(line)
		if err == nil && n > 0 && n <= len(m.names) {
			return m.names[n-1], nil
		}
		fmt.Fprintf(out, "  Enter a number from 1 to %d.\n", len(m.names))
	}
}
