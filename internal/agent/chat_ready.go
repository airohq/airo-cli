package agent

import (
	"context"
	"fmt"
	"io"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"

	"github.com/airohq/airo-cli/internal/ui"
)

// readyWhyMsg is what the wait for the Agent waits for now.
type readyWhyMsg string

// readyDoneMsg ends the wait.
type readyDoneMsg struct{ err error }

// readyModel is the spinner line while the Agent is not ready, as the chat's
// own status line looks (DESIGN.md, Terminal). It leaves no trace.
type readyModel struct {
	agent, why string
	started    time.Time
	spinner    spinner.Model
	styles     ui.Styles
	cancel     context.CancelFunc
	err        error
	done       bool
}

func (m *readyModel) Init() tea.Cmd { return m.spinner.Tick }

func (m *readyModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case readyWhyMsg:
		m.why = string(msg)
	case readyDoneMsg:
		m.err, m.done = msg.err, true
		return m, tea.Quit
	case tea.KeyPressMsg:
		// The wait holds nothing in the cluster, so one Ctrl-C ends it.
		if msg.String() == "ctrl+c" {
			m.cancel()
		}
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m *readyModel) View() tea.View {
	if m.done {
		return tea.NewView("")
	}
	line := fmt.Sprintf("%s Waiting for agent %s to become ready: %s · %s",
		m.spinner.View(), ui.Text(m.agent), ui.Text(m.why), time.Since(m.started).Truncate(time.Second))
	return tea.NewView(m.styles.Muted.Render(line))
}

// spinUntilReady shows the spinner on errOut while wait runs, and reads
// Ctrl-C from in. wait reports what it waits for.
func spinUntilReady(ctx context.Context, in io.Reader, errOut io.Writer, agent string, wait func(context.Context, func(string)) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	styles := ui.NewStyles(errOut)
	m := &readyModel{agent: agent, why: "asking the cluster", started: time.Now(), cancel: cancel, styles: styles,
		spinner: spinner.New(spinner.WithSpinner(spinner.Dot), spinner.WithStyle(styles.Muted))}
	p := tea.NewProgram(m, tea.WithInput(in), tea.WithOutput(errOut),
		tea.WithoutSignalHandler(), tea.WithColorProfile(colorprofile.ANSI))
	go func() {
		err := wait(ctx, func(why string) { p.Send(readyWhyMsg(why)) })
		p.Send(readyDoneMsg{err})
	}()
	if _, err := p.Run(); err != nil {
		return err
	}
	return m.err
}
