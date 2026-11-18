package agent

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
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
		result(artifact("a1", "# Answer\n\nHello **world**.\n\n```go\nfmt.Println(\"hi\")\n```")),
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
	for _, want := range []string{"Answer", "Hello world.", `fmt.Println("hi")`} {
		if strings.Count(text, want) != 1 {
			t.Errorf("stdout %q should contain %q once", text, want)
		}
	}
	if strings.Contains(text, "Session") || strings.Contains(text, "DUPLICATE") || strings.Contains(text, "Waiting") {
		t.Fatalf("stdout contains notes or duplicate snapshot: %q", text)
	}
	if !strings.Contains(notes.String(), "Resume this session with:\nairo agent -a reviewer -r ctx-1\n") {
		t.Fatal("chat did not print how to resume")
	}
	if strings.Contains(notes.String(), "\x1b[?1049h") {
		t.Fatal("chat entered the alternate screen")
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
