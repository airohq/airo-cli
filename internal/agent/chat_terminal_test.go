package agent

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
)

func testChat(t *testing.T) (*chatModel, *[]string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	m := newChatModel(ctx, make(chan tea.Msg, 32), &bytes.Buffer{}, &bytes.Buffer{}, "")
	var prompts []string
	m.run = func(_ context.Context, prompt string) { prompts = append(prompts, prompt) }
	m.cancel = func(context.Context, string) (string, error) { return stateCanceled, nil }
	return m, &prompts
}

func press(m *chatModel, code rune, mod tea.KeyMod) tea.Cmd {
	_, cmd := m.Update(tea.KeyPressMsg{Code: code, Mod: mod})
	return cmd
}

func TestChatKeepsDraftWhileTurnRuns(t *testing.T) {
	m, prompts := testChat(t)
	m.input.SetValue("First")
	press(m, tea.KeyEnter, 0)
	m.Update(printedMsg{})
	m.input.SetValue("Next")
	press(m, 'j', tea.ModCtrl)
	m.input.InsertString("line")
	if got := m.input.Value(); got != "Next\nline" {
		t.Fatalf("multiline draft = %q", got)
	}
	press(m, tea.KeyEnter, 0)
	if len(*prompts) != 1 || m.input.Value() != "Next\nline" {
		t.Fatal("Enter during a turn sent or lost the draft")
	}
	m.Update(answerMsg{"a1", "**Hello**"})
	m.Update(tea.WindowSizeMsg{Width: 40, Height: 15})
	m.Update(turnDoneMsg{})
	if len(*prompts) != 1 || m.input.Value() != "Next\nline" {
		t.Fatal("turn completion sent or lost the draft")
	}
	press(m, tea.KeyEnter, 0)
	if len(*prompts) != 2 || (*prompts)[1] != "Next\nline" {
		t.Fatalf("prompts = %#v", *prompts)
	}
}

func TestChatThemeRepliesPreserveDraftAndStreaming(t *testing.T) {
	m, prompts := testChat(t)
	m.styled = true
	m.start("First")
	m.Update(printedMsg{})
	m.Update(answerMsg{"a1", "First paragraph.\n\nNext paragraph."})
	m.Update(printedMsg{})
	m.input.SetValue("unfinished\ndraft")
	wantPreview := ansi.Strip(m.preview)
	wantCursor := *m.input.Cursor()
	for _, msg := range []tea.Msg{
		tea.ColorProfileMsg{Profile: colorprofile.TrueColor},
		tea.ForegroundColorMsg{Color: lipgloss.Color("#E5E5E5")},
		tea.BackgroundColorMsg{Color: lipgloss.Color("#181A1F")},
		tea.BackgroundColorMsg{Color: lipgloss.Color("#F5F2EB")},
		tea.ForegroundColorMsg{Color: lipgloss.Color("#202020")},
		tea.ColorProfileMsg{Profile: colorprofile.ANSI256},
	} {
		if _, cmd := m.Update(msg); cmd != nil {
			t.Fatal("a theme reply started work or another event reader")
		}
		if len(*prompts) != 1 || m.input.Value() != "unfinished\ndraft" || *m.input.Cursor() != wantCursor {
			t.Fatal("a theme reply sent or changed the draft/cursor")
		}
		if !m.active || !m.answerPrinted || m.block != "a1" || ansi.Strip(m.preview) != wantPreview {
			t.Fatal("a theme reply changed streaming state or repeated a message marker")
		}
		if got := m.input.Styles().Focused.Prompt.Render(">"); got != m.styles.Accent.Render(">") {
			t.Fatal("prompt did not adopt the palette")
		}
		if got := m.spinner.Style.Render("spinner"); got != m.styles.Muted.Render("spinner") {
			t.Fatal("spinner did not adopt the palette")
		}
	}
	if strings.Contains(m.formatMessage(FromYou, "prompt"), "38;2;") {
		t.Fatal("scrollback ignored the downgraded profile")
	}
	if got := m.styles.Accent.Render(">"); !strings.Contains(got, "38;5;") {
		t.Fatalf("late replies did not colour the prompt: %q", got)
	}
	m.stopTurn()
}

func TestChatThemeRepliesCannotEnableRedirectedColours(t *testing.T) {
	m, _ := testChat(t)
	m.Update(tea.ColorProfileMsg{Profile: colorprofile.TrueColor})
	m.Update(tea.BackgroundColorMsg{Color: lipgloss.Color("#181A1F")})
	m.Update(tea.ForegroundColorMsg{Color: lipgloss.Color("#E5E5E5")})
	if got := m.styles.Accent.Render(">"); got != ">" {
		t.Fatalf("a theme reply enabled styling on redirected output: %q", got)
	}
}

func TestChatMarkdownFollowsThemeAndMatchesHistory(t *testing.T) {
	m, _ := testChat(t)
	m.styled = true
	m.Update(tea.ColorProfileMsg{Profile: colorprofile.TrueColor})
	m.Update(answerMsg{"a1", "Use `file.go` for this change."})
	var previous string
	for _, bg := range []string{"#181A1F", "#F5F2EB"} {
		m.Update(tea.BackgroundColorMsg{Color: lipgloss.Color(bg)})
		m.Update(tea.WindowSizeMsg{Width: 60, Height: 24})
		if !strings.Contains(m.preview, "38;2;") || m.preview == previous {
			t.Fatalf("live Markdown did not adopt %s: %q", bg, m.preview)
		}
		if strings.Contains(m.preview, m.styles.Accent.Render("file.go")) {
			t.Fatal("inline code reused the UI accent instead of its own theme")
		}
		previous = m.preview
		if history := m.formatMessage(FromAgent, m.pending); history != m.preview {
			t.Fatalf("history differs from live Markdown:\nhistory: %q\nlive: %q", history, m.preview)
		}
	}
}

func TestChatParsesThemeRepliesAlongsideTyping(t *testing.T) {
	for _, replies := range []bool{false, true} {
		m, _ := testChat(t)
		m.styled = true
		m.palette.Profile = colorprofile.TrueColor
		input := "typed "
		if replies {
			input += "\x1b]11;rgb:1818/1a1a/1f1f\x1b\\"
		}
		input += "draft"
		if replies {
			input += "\x1b]10;rgb:e5e5/e5e5/e5e5\x1b\\"
		}
		input += "\r"
		ctx, cancel := context.WithTimeout(m.ctx, 3*time.Second)
		var output bytes.Buffer
		p := tea.NewProgram(m, tea.WithInput(strings.NewReader(input)), tea.WithOutput(&output),
			tea.WithContext(ctx), tea.WithoutRenderer(), tea.WithoutSignalHandler(), tea.WithColorProfile(colorprofile.TrueColor))
		var sent string
		m.run = func(_ context.Context, prompt string) {
			sent = prompt
			go p.Quit()
		}
		_, err := p.Run()
		cancel()
		if m.stopTurn != nil {
			m.stopTurn()
		}
		if err != nil || sent != "typed draft" {
			t.Fatalf("replies=%t: sent %q, err %v", replies, sent, err)
		}
		if replies && (m.palette.Background == nil || m.palette.Foreground == nil) {
			t.Fatal("terminal replies were not decoded as theme messages")
		}
	}
}

func TestChatSaysTheFirstTurnWakesTheAgent(t *testing.T) {
	m, _ := testChat(t)
	status := func() string { return ansi.Strip(m.View().Content) }
	m.input.SetValue("hi")
	press(m, tea.KeyEnter, 0)
	m.Update(printedMsg{})
	m.Update(event{state: stateWorking})
	if got := status(); !strings.Contains(got, "Waking up the agent") {
		t.Errorf("the first turn before its answer shows %q, want it to wake the agent", got)
	}
	m.Update(answerMsg{"a1", "Hello"})
	if got := status(); strings.Contains(got, "Waking up") || !strings.Contains(got, "Agent "+stateWord(stateWorking)) {
		t.Errorf("after the answer started: %q, want the agent's state", got)
	}
	m.Update(turnDoneMsg{})

	// The sandbox runs now, so a later turn shows the agent's state.
	m.input.SetValue("again")
	press(m, tea.KeyEnter, 0)
	m.Update(printedMsg{})
	m.Update(event{state: stateWorking})
	if got := status(); strings.Contains(got, "Waking up") {
		t.Errorf("the second turn shows %q, want no wake-up", got)
	}
}

func TestChatHistoryRestoresDraft(t *testing.T) {
	m, _ := testChat(t)
	m.history, m.historyAt = []string{"one", "two\nlines"}, 2
	m.input.SetValue("unfinished")
	press(m, tea.KeyUp, 0)
	if m.input.Value() != "two\nlines" {
		t.Fatal("Up did not recall the last prompt")
	}
	press(m, tea.KeyUp, 0)
	if m.input.Value() != "one" {
		t.Fatal("Up did not recall the earlier prompt")
	}
	press(m, tea.KeyDown, 0)
	press(m, tea.KeyDown, 0)
	if m.input.Value() != "unfinished" {
		t.Fatal("Down lost the unsent draft")
	}
}

func TestChatCtrlCAndEOF(t *testing.T) {
	m, _ := testChat(t)
	m.input.SetValue("draft")
	press(m, 'c', tea.ModCtrl)
	if m.input.Value() != "" || m.interrupt || m.exiting {
		t.Fatal("first Ctrl-C should only clear an idle draft")
	}
	press(m, 'c', tea.ModCtrl)
	if !m.interrupt || m.exiting {
		t.Fatal("empty Ctrl-C should explain how to leave")
	}
	press(m, 'c', tea.ModCtrl)
	if !m.exiting || m.detached {
		t.Fatal("second Ctrl-C should leave an idle chat")
	}

	m, _ = testChat(t)
	m.start("work")
	m.input.SetValue("next")
	press(m, 'c', tea.ModCtrl)
	if m.input.Value() != "next" || !strings.Contains(m.hint, "works on") || m.exiting {
		t.Fatal("active Ctrl-C must preserve draft and explain detaching")
	}
	press(m, 'c', tea.ModCtrl)
	if !m.detached || !m.exiting {
		t.Fatal("second Ctrl-C must detach")
	}

	m, _ = testChat(t)
	m.input.SetValue("draft")
	press(m, 'd', tea.ModCtrl)
	if m.exiting {
		t.Fatal("Ctrl-D with a draft must not leave")
	}
	m.input.Reset()
	press(m, 'd', tea.ModCtrl)
	if !m.exiting {
		t.Fatal("Ctrl-D on an empty prompt must leave")
	}
}

func TestChatTurnCompletionDoesNotDisarmCtrlC(t *testing.T) {
	m, _ := testChat(t)
	m.start("work")
	press(m, 'c', tea.ModCtrl)
	m.Update(turnDoneMsg{})
	press(m, 'c', tea.ModCtrl)
	if !m.exiting {
		t.Fatal("completion between two Ctrl-Cs must not require a third")
	}
}

func TestChatEscapeCancelsOnlyCurrentTask(t *testing.T) {
	m, _ := testChat(t)
	m.start("work")
	m.Update(printedMsg{})
	var canceled []string
	m.cancel = func(_ context.Context, id string) (string, error) {
		canceled = append(canceled, id)
		return stateCanceled, nil
	}
	if cmd := press(m, tea.KeyEscape, 0); cmd != nil {
		t.Fatal("Esc cannot call CancelTask before the task ID arrives")
	}
	_, cmd := m.Update(event{taskID: "task-123", contextID: "session-456", state: stateWorking})
	// The event schedules both the next stream read and the cancellation.
	batch := cmd().(tea.BatchMsg)
	m.Update(batch[1]())
	if len(canceled) != 1 || canceled[0] != "task-123" {
		t.Fatalf("canceled = %#v", canceled)
	}
	// Statuses can already be queued when CancelTask returns. Neither a
	// working status nor the final one may issue a second cancellation.
	for _, state := range []string{stateWorking, stateCanceled} {
		_, cmd := m.Update(event{taskID: "task-123", state: state})
		if !m.canceling || !m.stopping {
			t.Fatal("a successful cancellation must remain in flight until the turn ends")
		}
		if cmd == nil {
			t.Fatal("the stream must still be drained after cancellation")
		}
	}
	m.Update(turnDoneMsg{err: errDetached})
	if m.detached || m.exiting || m.active {
		t.Fatal("canceling a turn must keep the conversation open")
	}
	if m.cancelState != stateCanceled {
		t.Fatal("cancellation response was lost")
	}
}

func TestChatCancellationFailureAndStaleResult(t *testing.T) {
	m, _ := testChat(t)
	m.start("work")
	m.taskID = "task-1"
	m.cancel = func(context.Context, string) (string, error) { return "", errors.New("denied") }
	m.Update(press(m, tea.KeyEscape, 0)())
	if m.stopping || !m.active || !strings.Contains(m.hint, "denied") {
		t.Fatal("failed cancellation must leave the stream running and allow retry")
	}
	m.Update(cancelDoneMsg{turn: m.turn - 1, state: stateCanceled})
	if m.cancelState != "" {
		t.Fatal("late cancellation of an earlier turn affected the current one")
	}
}

func TestChatSlashCommands(t *testing.T) {
	m, prompts := testChat(t)
	m.input.SetValue("/help")
	press(m, tea.KeyEnter, 0)
	if len(*prompts) != 0 || !strings.Contains(m.hint, "/exit") {
		t.Fatal("/help should be local")
	}
	for _, key := range []string{"Enter sends", "Ctrl-J", "Shift-Enter", "↑↓", "Esc", "Ctrl-D", "Ctrl-C twice"} {
		if !strings.Contains(m.hint, key) {
			t.Errorf("/help omitted %s", key)
		}
	}
	m.input.SetValue("/review main")
	press(m, tea.KeyEnter, 0)
	if len(*prompts) != 1 || (*prompts)[0] != "/review main" {
		t.Fatal("unknown slash commands must reach the agent unchanged")
	}
	m.input.SetValue("/exit")
	press(m, tea.KeyEnter, 0)
	if !m.exiting || !m.detached {
		t.Fatal("/exit should detach from a running turn")
	}
}

func TestChatStatusIdentifiesTheSession(t *testing.T) {
	const id = "01a1eb36-0cf7-78d5-989d-31b1458abaa4"
	m, _ := testChat(t)
	m.styled = true
	m.Update(tea.ColorProfileMsg{Profile: colorprofile.TrueColor})
	m.Update(tea.BackgroundColorMsg{Color: lipgloss.Color("#181A1F")})
	m.Update(tea.ForegroundColorMsg{Color: lipgloss.Color("#E5E5E5")})
	m.details = chatDetails{agent: "reviewer"}
	m.loadDetails = func(context.Context) chatDetails { t.Fatal("details loaded synchronously"); return chatDetails{} }
	m.Init()
	m.input.SetValue("Keep this draft")
	m.Update(chatDetails{"reviewer", "codex", "gpt-5.4"})
	if m.input.Value() != "Keep this draft" {
		t.Fatal("metadata update lost the draft")
	}
	if strings.Contains(m.sessionStatus(), "Session") {
		t.Fatal("invented a session ID before the first event")
	}
	m.Update(event{contextID: id})
	m.Update(event{taskID: "task-1", contextID: "different-context", state: stateWorking})
	if m.sessionID != id {
		t.Fatal("a later event replaced the conversation ID")
	}
	for _, width := range []int{140, 80, 40} {
		m.resize(width, 24)
		for _, active := range []bool{false, true} {
			m.active = active
			m.started = time.Now()
			view := ansi.Strip(m.View().Content)
			if !strings.Contains(view, id) {
				t.Fatalf("width %d split or lost the ID: %q", width, view)
			}
			if !strings.Contains(strings.Join(strings.Fields(view), " "), "reviewer · codex · gpt-5.4") {
				t.Errorf("view lost the unlabelled settings: %q", view)
			}
			if strings.Contains(view, "Enter sends") || strings.Contains(view, "Ctrl-C twice") {
				t.Fatalf("default view still shows key guidance: %q", view)
			}
			for _, line := range strings.Split(view, "\n") {
				if ansi.StringWidth(line) > width {
					t.Errorf("status overflowed %d columns: %q", width, line)
				}
				if strings.Contains(line, id) && ansi.StringWidth(line) != width {
					t.Errorf("session ID is not right aligned: %q", line)
				}
			}
			if active && !strings.Contains(view, "Agent working") {
				t.Fatal("lost the working status")
			}
		}
	}
}

func TestChatStatusSanitizesMetadata(t *testing.T) {
	m, _ := testChat(t)
	m.details = chatDetails{"reviewer\nspoof", "\x1b[31mcodex", "model\tname"}
	m.resize(120, 24)
	got := m.sessionStatus()
	if strings.ContainsAny(got, "\n\t\x1b") || !strings.Contains(got, "reviewer spoof · codex · model name") {
		t.Fatalf("unsafe status text: %q", got)
	}
}

func TestChatMarkdownParagraphBoundaries(t *testing.T) {
	for _, tt := range []struct{ text, complete string }{
		{"first\n\nsecond", "first\n\n"},
		{"first\n", ""},
		{"first\n\n```go\nx\n\ny\n", "first\n\n"},
		{"```go\nx\n\ny\n```\n\nnext", "```go\nx\n\ny\n```\n\n"},
		{"~~~~\nx\n~~~\n\ny\n", ""},
		{"~~~\nx\n\n~~~\n\nnext", "~~~\nx\n\n~~~\n\n"},
	} {
		if got := tt.text[:paragraphEnd(tt.text)]; got != tt.complete {
			t.Errorf("boundary in %q = %q, want %q", tt.text, got, tt.complete)
		}
	}
}

func TestTerminalChatPrintsMarkdownOnceAndKeepsStreamsSeparate(t *testing.T) {
	s, _, out, notes := talk(t, stream(result(submitted),
		result(artifact("a1", "# Answer\n\nHello **wor")),
		result(artifact("a1", "ld**.\n\n```go\nfmt.Println(\"hi\")\n```")),
		result(artifact("a2", "Another message.")),
		result(`{"task":{"id":"task-1","contextId":"ctx-1","status":{"state":"TASK_STATE_COMPLETED"},"artifacts":[{"artifactId":"a1","parts":[{"text":"DUPLICATE"}]}]}}`),
	))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.chat(ctx, nil, "Hello"); err != nil {
		t.Fatal(err)
	}
	if ctx.Err() != nil {
		t.Fatal("chat did not exit after its one turn")
	}
	text := ansi.Strip(out.String())
	for _, want := range []string{"Answer", "Hello world.", `fmt.Println("hi")`, "Another message."} {
		if strings.Count(text, want) != 1 {
			t.Errorf("stdout %q should contain %q once", text, want)
		}
	}
	if strings.Contains(text, "Session") || strings.Contains(text, "DUPLICATE") || strings.Contains(text, "Waiting") {
		t.Fatalf("stdout contains notes or duplicate snapshot: %q", text)
	}
	if !strings.Contains(notes.String(), "Resume this session with:\nairo chat -a reviewer -r ctx-1\n") {
		t.Fatal("chat did not print how to resume")
	}
	if strings.Contains(notes.String(), "\x1b[?1049h") {
		t.Fatal("chat entered the alternate screen")
	}
	// A marker belongs to a message, not to each streamed paragraph. A block
	// change must keep both the preceding unfinished text and the new marker.
	if strings.Count(text, "\n• ") != 2 {
		t.Errorf("message markers repeated or missing: %q", text)
	}
	for _, line := range strings.Split(text, "\n") {
		if line != "" && !strings.HasPrefix(line, "• ") && !strings.HasPrefix(line, "  ") {
			t.Errorf("continuation left the message's margin: %q", line)
		}
	}
	var restored bytes.Buffer
	m, _ := testChat(t)
	turns := []Turn{{State: StateDone, Messages: []Message{
		{From: FromAgent, Text: "# Answer\n\nHello **world**.\n\n```go\nfmt.Println(\"hi\")\n```"},
		{From: FromAgent, Text: "Another message."},
	}}}
	if err := writeHistory(turns, &restored, &bytes.Buffer{}, m.formatMessage); err != nil {
		t.Fatal(err)
	}
	if want := ansi.Strip(restored.String()); text != want {
		t.Errorf("streamed layout differs from restored history:\nstreamed: %q\nrestored: %q", text, want)
	}
}

func TestPlainPrinterPreservesMarkdownAndBlocks(t *testing.T) {
	s, _, out, _ := talk(t, stream(result(submitted),
		result(artifact("a1", "**raw**")), result(artifact("a1", " markdown")),
		result(artifact("a2", "```go\ncode\n```")), result(completed)))
	if err := s.once(context.Background(), "/help"); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "**raw** markdown\n```go\ncode\n```\n"; got != want {
		t.Fatalf("plain stdout = %q, want %q", got, want)
	}
}
