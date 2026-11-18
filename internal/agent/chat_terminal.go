package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/glamour/v2"
	"github.com/charmbracelet/colorprofile"

	"github.com/airohq/airo-cli/internal/ui"
)

// terminalPrinter delivers events in order. Only the Bubble Tea model touches
// the terminal; the network worker never writes over the person's draft.
type terminalPrinter struct {
	ctx    context.Context
	events chan tea.Msg
}

func (p *terminalPrinter) send(msg tea.Msg) {
	select {
	case p.events <- msg:
	case <-p.ctx.Done():
	}
}
func (p *terminalPrinter) write(block, text string) { p.send(answerMsg{block, text}) }
func (p *terminalPrinter) end()                     { p.send(answerEndMsg{}) }
func (p *terminalPrinter) Write(b []byte) (int, error) {
	p.send(noteMsg(string(b)))
	return len(b), nil
}

type answerMsg struct{ block, text string }
type answerEndMsg struct{}
type noteMsg string
type turnDoneMsg struct{ err error }
type cancelDoneMsg struct {
	turn  int
	state string
	err   error
}
type externalExitMsg struct{}
type printedMsg struct{ err error }
type historyLoadedMsg struct {
	turns []Turn
	err   error
}

// chat runs inline, never in the alternate screen. The same session.turn
// drives both printers, including errors, snapshots and remembering the ID.
func (s *session) chat(ctx context.Context, in io.Reader, prompt string) error {
	runCtx, closeChat := context.WithCancel(ctx)
	defer closeChat()
	p := &terminalPrinter{ctx: runCtx, events: make(chan tea.Msg, 64)}
	out, errOut := s.out, s.errOut
	s.printer, s.errOut = p, p
	s.observe = func(e event) { p.send(e) }
	defer func() { s.printer, s.observe, s.errOut = nil, nil, errOut }()

	var workers sync.WaitGroup
	m := newChatModel(runCtx, p.events, out, errOut, prompt)
	if s.id != "" && prompt == "" {
		m.load, m.loading = s.history, true
	}
	m.run = func(ctx context.Context, prompt string) {
		workers.Go(func() { p.send(turnDoneMsg{s.turn(ctx, prompt)}) })
	}
	m.cancel = func(ctx context.Context, id string) (string, error) {
		t, err := s.api.cancel(ctx, s.ns, s.agent, id)
		return t.Status.State, err
	}
	if s.id == "" {
		fmt.Fprintf(errOut, "A new session with agent %s.\n", ui.Text(s.agent))
	} else {
		fmt.Fprintf(errOut, "Session %s with agent %s.\n", ui.Text(s.id), ui.Text(s.agent))
	}
	_, err := tea.NewProgram(m, tea.WithInput(in), tea.WithOutput(errOut),
		tea.WithoutSignalHandler(), tea.WithColorProfile(colorprofile.ANSI)).Run()
	closeChat()    // detach the stream, never cancel the remote task on exit
	workers.Wait() // session fields belong to the worker until it has stopped
	s.errOut = errOut
	// A failed or interrupted history load says nothing about whether a
	// remote turn is still running, and has no closing hint.
	if !m.loading {
		if m.detached || ctx.Err() != nil {
			s.detached()
		} else {
			s.left()
		}
	}
	if err != nil {
		return err
	}
	return m.err
}

type chatModel struct {
	ctx                         context.Context
	events                      <-chan tea.Msg
	out, errOut                 io.Writer
	styles                      ui.Styles
	input                       textarea.Model
	spinner                     spinner.Model
	markdown                    *glamour.TermRenderer
	width, height               int
	initial                     string
	once                        bool
	load                        func(context.Context) ([]Turn, error)
	loading                     bool
	run                         func(context.Context, string)
	cancel                      func(context.Context, string) (string, error)
	stopTurn                    context.CancelFunc
	active, stopping, canceling bool
	cancelState                 string
	turn                        int
	taskID, state               string
	started                     time.Time
	history                     []string
	historyAt                   int
	draft                       string
	block, pending, preview     string
	answerPrinted               bool // the current message's marker is already in scrollback
	printing, exiting, detached bool
	interrupt                   bool
	hint                        string
	err                         error
}

func newChatModel(ctx context.Context, events <-chan tea.Msg, out, errOut io.Writer, initial string) *chatModel {
	styles := ui.NewStyles(errOut)
	input := textarea.New()
	input.Prompt, input.Placeholder = "> ", "Ask the agent…"
	input.SetPromptFunc(2, func(info textarea.PromptInfo) string {
		if info.LineNumber == 0 {
			return "> "
		}
		return "  "
	})
	input.ShowLineNumbers = false
	input.DynamicHeight, input.MinHeight, input.MaxHeight = true, 1, 5
	input.SetVirtualCursor(false)
	input.KeyMap.InsertNewline = key.NewBinding(key.WithKeys("shift+enter", "ctrl+j"))
	input.SetStyles(textarea.Styles{
		Focused: textarea.StyleState{Prompt: styles.Accent, Placeholder: styles.Muted},
		Blurred: textarea.StyleState{Prompt: styles.Accent, Placeholder: styles.Muted},
	})
	input.Focus()
	m := &chatModel{ctx: ctx, events: events, out: out, errOut: errOut,
		styles: styles, input: input, spinner: spinner.New(spinner.WithSpinner(spinner.Dot), spinner.WithStyle(styles.Muted)),
		width: 80, height: 24, initial: initial, once: initial != ""}
	m.resize(80, 24)
	return m
}

func (m *chatModel) Init() tea.Cmd {
	watch := func() tea.Msg { <-m.ctx.Done(); return externalExitMsg{} }
	if m.loading {
		m.started = time.Now()
		return tea.Batch(watch, m.spinner.Tick, func() tea.Msg {
			turns, err := m.load(m.ctx)
			return historyLoadedMsg{turns, err}
		})
	}
	if m.initial != "" {
		return tea.Batch(watch, m.start(m.initial))
	}
	return watch
}

func (m *chatModel) wait() tea.Cmd {
	return func() tea.Msg {
		select {
		case msg := <-m.events:
			return msg
		case <-m.ctx.Done():
			return externalExitMsg{}
		}
	}
}

func (m *chatModel) start(prompt string) tea.Cmd {
	m.active, m.stopping, m.canceling = true, false, false
	m.cancelState = ""
	m.turn++
	m.taskID, m.state, m.block, m.hint = "", "", "", ""
	m.answerPrinted = false
	m.started = time.Now()
	m.history = append(m.history, prompt)
	m.historyAt, m.draft = len(m.history), ""
	m.input.Reset()
	turnCtx, cancel := context.WithCancel(m.ctx)
	m.stopTurn = cancel
	m.run(turnCtx, prompt)
	if m.once {
		return tea.Batch(m.wait(), m.spinner.Tick)
	}
	return tea.Batch(m.print(m.formatMessage(FromYou, prompt), m.errOut), m.spinner.Tick)
}

func (m *chatModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.resize(msg.Width, msg.Height)
	case externalExitMsg:
		return m, m.leave()
	case historyLoadedMsg:
		if m.exiting {
			return m, nil
		}
		if msg.err != nil {
			m.err = msg.err
			return m, m.leave()
		}
		m.loading = false
		if !m.interrupt {
			m.hint = ""
		}
		for _, turn := range msg.turns {
			for _, message := range turn.Messages {
				if message.From == FromYou {
					m.history = append(m.history, ui.Text(message.Text))
				}
			}
		}
		m.historyAt = len(m.history)
		return m, m.printWith(func() error {
			return writeHistory(msg.turns, m.out, m.errOut, m.formatMessage)
		})
	case printedMsg:
		m.printing = false
		if msg.err != nil {
			m.err = msg.err
			return m, m.leave()
		}
		if m.exiting {
			return m, tea.Quit
		}
		if m.active {
			return m, m.wait()
		}
	case answerMsg:
		var committed []string
		if msg.block == "" || msg.block != m.block {
			if text := m.commitAnswer(m.pending); text != "" {
				committed = append(committed, text)
			}
			m.pending, m.answerPrinted = "", false
		}
		m.block = msg.block
		m.pending += msg.text
		if n := paragraphEnd(m.pending); n > 0 {
			if text := m.commitAnswer(m.pending[:n]); text != "" {
				committed = append(committed, text)
			}
			m.pending = m.pending[n:]
		}
		m.render()
		if len(committed) > 0 {
			return m, m.print(strings.Join(committed, "\n"), m.out)
		}
		return m, m.wait()
	case answerEndMsg:
		if m.pending != "" {
			return m, m.flush()
		}
		return m, m.wait()
	case noteMsg:
		return m, m.print(m.styles.Muted.Render(ui.Wrap(ui.Text(string(msg)), m.width)), m.errOut)
	case event:
		if msg.taskID != "" {
			m.taskID = msg.taskID
		}
		if msg.state != "" {
			m.state = msg.state
		}
		if m.stopping && m.taskID != "" && !m.canceling && !ended(m.state) {
			return m, tea.Batch(m.wait(), m.stop())
		}
		return m, m.wait()
	case cancelDoneMsg:
		if msg.turn != m.turn || !m.active {
			return m, nil
		}
		if msg.err != nil {
			m.canceling = false
			m.stopping = false
			m.hint = "Could not stop the agent: " + ui.Text(msg.err.Error())
		} else if ended(msg.state) {
			m.cancelState = msg.state
			m.stopTurn()
		} else {
			m.hint = "Stop requested; waiting for the agent."
		}
	case turnDoneMsg:
		m.stopTurn()
		m.active, m.stopping = false, false
		if m.cancelState != "" && errors.Is(msg.err, errDetached) {
			msg.err = nil
			if m.cancelState == stateCanceled || m.cancelState == stateFailed || m.cancelState == stateRejected {
				msg.err = &taskError{state: m.cancelState}
			}
		}
		if errors.Is(msg.err, errDetached) {
			return m, m.leave()
		}
		var failed *taskError
		if m.once || (msg.err != nil && !errors.As(msg.err, &failed)) {
			m.err = msg.err
			m.exiting = true
			return m, tea.Quit
		}
		m.hint = ""
		if m.interrupt {
			m.hint = "Ctrl-C again leaves; the session stays"
		}
		if msg.err != nil {
			return m, m.print(m.styles.Error.Render("✗ Error: "+ui.Text(msg.err.Error())), m.errOut)
		}
	case tea.KeyPressMsg:
		return m, m.key(msg)
	case tea.PasteMsg:
		m.interrupt, m.hint = false, ""
		if !m.once {
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(msg)
			return m, cmd
		}
	case spinner.TickMsg:
		if m.active || m.loading {
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(msg)
			return m, cmd
		}
	default:
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m *chatModel) key(msg tea.KeyPressMsg) tea.Cmd {
	if msg.String() == "ctrl+c" {
		if m.interrupt {
			return m.leave()
		}
		if !m.active && m.input.Value() != "" {
			m.input.Reset()
			m.historyAt, m.draft = len(m.history), ""
			m.hint = ""
			return nil
		}
		m.interrupt = true
		if m.active {
			m.hint = "Esc stops the agent · Ctrl-C again leaves, the agent works on"
		} else {
			m.hint = "Ctrl-C again leaves; the session stays"
		}
		return nil
	}
	m.interrupt, m.hint = false, ""
	switch msg.String() {
	case "esc":
		if m.active && !m.stopping && !ended(m.state) {
			m.stopping = true
			if m.taskID != "" {
				return m.stop()
			}
			m.hint = "Waiting for the turn's ID to stop it."
		}
		return nil
	case "ctrl+d":
		if m.input.Value() == "" {
			return m.leave()
		}
	case "enter":
		prompt := m.input.Value()
		if m.once || strings.TrimSpace(prompt) == "" {
			return nil
		}
		switch strings.TrimSpace(prompt) {
		case "/help":
			m.input.Reset()
			m.hint = "/help shows keys · /exit leaves · other slash commands go to the agent"
			return nil
		case "/exit":
			return m.leave()
		}
		if m.loading || (m.printing && !m.active) {
			m.hint = "Draft kept. Enter sends it when the conversation is ready."
			return nil
		}
		if m.active {
			m.hint = "Draft kept. Enter sends it when this turn finishes."
			return nil
		}
		return m.start(prompt)
	case "up", "down":
		if len(m.history) == 0 {
			return nil
		}
		if m.historyAt == len(m.history) {
			m.draft = m.input.Value()
		}
		if msg.String() == "up" {
			m.historyAt = max(0, m.historyAt-1)
		} else {
			m.historyAt = min(len(m.history), m.historyAt+1)
		}
		if m.historyAt == len(m.history) {
			m.input.SetValue(m.draft)
		} else {
			m.input.SetValue(m.history[m.historyAt])
		}
		m.input.MoveToEnd()
		return nil
	}
	if m.once {
		return nil
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return cmd
}

func (m *chatModel) stop() tea.Cmd {
	m.canceling = true
	turn, id := m.turn, m.taskID
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, 15*time.Second)
		defer cancel()
		state, err := m.cancel(ctx, id)
		return cancelDoneMsg{turn, state, err}
	}
}

func (m *chatModel) leave() tea.Cmd {
	m.detached = m.active
	m.exiting = true
	if m.pending != "" {
		return m.flush()
	}
	return tea.Quit
}

func (m *chatModel) resize(width, height int) {
	if width <= 0 {
		width = m.width
	}
	if height <= 0 {
		height = m.height
	}
	m.width, m.height = max(4, width), max(4, height)
	m.input.MaxHeight = min(5, max(1, height-4))
	m.input.SetWidth(m.width)
	m.markdown, _ = ui.Markdown(m.width - 2)
	m.render()
}

func (m *chatModel) renderMarkdown(text string) string {
	text = ui.Text(text)
	if m.markdown != nil {
		if rendered, err := m.markdown.Render(text); err == nil {
			return strings.Trim(ui.Wrap(rendered, m.width-2), "\n")
		}
	}
	return strings.Trim(ui.Wrap(text, m.width-2), "\n")
}

func (m *chatModel) render() { m.preview = m.formatAnswer(m.pending) }

func (m *chatModel) formatMessage(from, text string) string {
	if from == FromYou {
		return messageLines(ui.Wrap(ui.Text(text), m.width-2), m.styles.Accent.Render("> "))
	}
	return messageLines(m.renderMarkdown(text), m.styles.Muted.Render("• "))
}

// Each message gets one marker. Later streamed paragraphs keep the same
// two-column indent; rendering a preview never consumes the marker.
func (m *chatModel) formatAnswer(text string) string {
	marker := "  "
	if !m.answerPrinted {
		marker = m.styles.Muted.Render("• ")
	}
	return messageLines(m.renderMarkdown(text), marker)
}

func (m *chatModel) commitAnswer(text string) string {
	formatted := m.formatAnswer(text)
	if formatted != "" {
		m.answerPrinted = true
	}
	return formatted
}

// messageLines reserves two columns for the speaker, with one blank line
// before the message or paragraph. Markdown's own indentation stays inside.
func messageLines(text, marker string) string {
	lines := strings.Split(text, "\n")
	// Glamour pads some empty lines to the render width. Normalize them
	// before trimming so a code block does not add a second paragraph gap.
	for i, line := range lines {
		if strings.TrimSpace(ui.Text(line)) == "" {
			lines[i] = ""
		}
	}
	for len(lines) > 0 && lines[0] == "" {
		lines = lines[1:]
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 0 {
		return ""
	}
	lines[0] = marker + lines[0]
	for i := 1; i < len(lines); i++ {
		if lines[i] != "" {
			lines[i] = "  " + lines[i]
		}
	}
	return "\n" + strings.Join(lines, "\n")
}

func (m *chatModel) flush() tea.Cmd {
	text := m.commitAnswer(m.pending)
	m.pending, m.preview = "", ""
	if text == "" {
		if m.exiting {
			return tea.Quit
		}
		return m.wait()
	}
	return m.print(text, m.out)
}

// The renderer uses stderr. Releasing it before writing completed paragraphs
// to stdout keeps the streams separate without racing its cursor movements.
func (m *chatModel) print(text string, out io.Writer) tea.Cmd {
	return m.printWith(func() error {
		_, err := fmt.Fprintln(out, strings.TrimRight(text, "\n"))
		return err
	})
}

func (m *chatModel) printWith(write func() error) tea.Cmd {
	m.printing = true
	return tea.Exec(scrollback(write), func(err error) tea.Msg { return printedMsg{err} })
}

type scrollback func() error

func (s scrollback) Run() error        { return s() }
func (scrollback) SetStdin(io.Reader)  {}
func (scrollback) SetStdout(io.Writer) {}
func (scrollback) SetStderr(io.Writer) {}

func (m *chatModel) View() tea.View {
	if m.exiting || m.printing {
		return tea.NewView("")
	}
	var rows []string
	statusLines := 0
	if m.active || m.loading {
		status := "Waiting for the agent"
		if m.loading {
			status = "Loading the conversation"
		} else if m.state != "" {
			status = "Agent " + ui.Text(stateWord(m.state))
		}
		if m.stopping {
			status = "Stopping the agent"
		}
		line := ui.Wrap(fmt.Sprintf("%s %s · %s", m.spinner.View(), status, time.Since(m.started).Truncate(time.Second)), m.width)
		rows = append(rows, m.styles.Muted.Render(line))
		statusLines = strings.Count(line, "\n") + 1
	}
	if !m.once {
		rows = append(rows, m.input.View())
	}
	keys := "Enter sends · Ctrl-J / Shift-Enter newline · ↑↓ history · Ctrl-D leaves"
	if m.loading {
		keys = "Draft the next prompt · Ctrl-C twice leaves"
	} else if m.active {
		keys = "Draft the next prompt · Esc stops · Ctrl-C twice leaves"
		if m.once {
			keys = "Esc stops · Ctrl-C twice leaves"
		}
	}
	rows = append(rows, m.styles.Muted.Render(ui.Wrap(keys, m.width)))
	if m.hint != "" {
		rows = append(rows, m.styles.Muted.Render(ui.Wrap(m.hint, m.width)))
	}
	footer := ui.Wrap(strings.Join(rows, "\n"), m.width)
	preview := m.preview
	if preview != "" {
		lines := strings.Split(preview, "\n")
		space := max(1, m.height-strings.Count(footer, "\n")-3)
		if len(lines) > space {
			lines = lines[len(lines)-space:]
		}
		preview = strings.Join(lines, "\n") + "\n"
	}
	v := tea.NewView(preview + "\n" + footer)
	if !m.once {
		v.Cursor = m.input.Cursor()
		if v.Cursor != nil {
			v.Cursor.Y += strings.Count(preview, "\n") + 1
			v.Cursor.Y += statusLines
		}
	}
	return v
}

// Only complete paragraphs outside a fenced code block leave the live region.
// A split SSE event may contain half a delimiter, so scan complete lines only.
func paragraphEnd(text string) int {
	end, offset, fenceSize := 0, 0, 0
	var fence byte
	for _, line := range strings.SplitAfter(text, "\n") {
		if !strings.HasSuffix(line, "\n") {
			break
		}
		offset += len(line)
		trimmed := strings.TrimSpace(line)
		if len(trimmed) >= 3 && (trimmed[0] == '`' || trimmed[0] == '~') {
			n := 0
			for n < len(trimmed) && trimmed[n] == trimmed[0] {
				n++
			}
			if n >= 3 {
				if fence == 0 {
					fence, fenceSize = trimmed[0], n
				} else if trimmed[0] == fence && n >= fenceSize && strings.TrimSpace(trimmed[n:]) == "" {
					fence = 0
				}
			}
		}
		if trimmed == "" && fence == 0 {
			end = offset
		}
	}
	return end
}
