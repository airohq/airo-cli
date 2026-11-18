package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// savedChat serves only history. Sending a prompt or canceling a turn is a
// test failure, including when loading history fails.
func savedChat(t *testing.T, status int, answer func(listParams) listResult) (*session, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/agents/team/reviewer" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		for header, want := range map[string]string{
			"Authorization": "Bearer token",
			"A2A-Version":   "1.0",
			"Accept":        "application/json",
		} {
			if got := r.Header.Get(header); got != want {
				t.Errorf("header %s = %q, want %q", header, got, want)
			}
		}
		var req struct {
			Method string     `json:"method"`
			Params listParams `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("request body: %v", err)
		}
		if req.Method != methodListTasks || req.Params.ContextID != "ctx-1" || !req.Params.IncludeArtifacts {
			t.Errorf("resume must read this session's history and answers: %+v", req)
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		if status != http.StatusOK {
			http.Error(w, "could not load history", status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": "1", "result": answer(req.Params)}); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	out, notes := &bytes.Buffer{}, &bytes.Buffer{}
	s := &session{
		api: &a2aClient{http: srv.Client(), url: srv.URL, token: func() (string, error) { return "token", nil }},
		ns:  "team", agent: "reviewer", id: "ctx-1", out: out, errOut: notes,
		remember: func(string) { t.Error("history replay sent a prompt") },
	}
	return s, out, notes
}

func savedTask(id, at, prompt, answer string) wireTask {
	return wireTask{
		ID: id, ContextID: "ctx-1", Status: wireStatus{State: stateCompleted},
		Metadata:  map[string]any{metaTaskCreated: at},
		History:   []wireMessage{{Role: roleUser, Parts: []wirePart{{Text: prompt}}}},
		Artifacts: []wireArtifact{{ArtifactID: id + "-answer", Parts: []wirePart{{Text: answer}}}},
	}
}

func TestResumeReadsEveryPageInConversationOrder(t *testing.T) {
	older := savedTask("first", "2026-11-17T10:00:00Z", "First prompt", "First answer")
	newer := savedTask("second", "2026-11-17T11:00:00Z", "Next prompt", "Next answer")
	s, out, notes := savedChat(t, http.StatusOK, func(p listParams) listResult {
		switch p.PageToken {
		case "":
			return listResult{Tasks: []wireTask{newer}, NextPageToken: "older"}
		case "older":
			return listResult{Tasks: []wireTask{older}}
		default:
			t.Errorf("unexpected page token: %q", p.PageToken)
			return listResult{}
		}
	})
	// EOF immediately after resuming should still show the conversation.
	if err := s.converse(context.Background(), &lines{r: bufio.NewReader(strings.NewReader(""))}); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "First answer\nNext answer\n" {
		t.Errorf("answers = %q", got)
	}
	if got := notes.String(); !strings.Contains(got, "> First prompt\n> Next prompt\n") || !strings.Contains(got, "airo agent -a reviewer -r ctx-1") {
		t.Errorf("prompts and resume hint = %q", got)
	}
}

func TestResumeStopsWhenHistoryCannotBeLoaded(t *testing.T) {
	for _, mode := range []string{"plain", "terminal"} {
		for _, tt := range []struct {
			name   string
			status int
			want   string
		}{
			{"missing", http.StatusOK, "see yours with airo session list"},
			{"not yours", http.StatusForbidden, "no session ctx-1 of yours"},
			{"unavailable", http.StatusServiceUnavailable, "load session ctx-1"},
		} {
			t.Run(mode+"/"+tt.name, func(t *testing.T) {
				s, out, notes := savedChat(t, tt.status, func(listParams) listResult { return listResult{} })
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				var err error
				if mode == "plain" {
					err = s.converse(ctx, &lines{r: bufio.NewReader(strings.NewReader("Do not send this\n"))})
				} else {
					err = s.chat(ctx, nil, "")
				}
				if err == nil || !strings.Contains(err.Error(), tt.want) {
					t.Fatalf("resume error = %v, want %q", err, tt.want)
				}
				if out.Len() != 0 || ctx.Err() != nil {
					t.Fatalf("failed resume produced answers or did not exit: %q, %v", out.String(), ctx.Err())
				}
				if strings.Contains(notes.String(), "Resume this session with:") || strings.Contains(notes.String(), "works on") {
					t.Fatalf("failed load printed a success or detach hint: %q", notes.String())
				}
			})
		}
	}
}

func TestTerminalResumeRendersSavedAnswers(t *testing.T) {
	task := savedTask("first", "2026-11-17T10:00:00Z", "Earlier prompt", "# Saved answer\n\nHello **again**.\n\n```go\nfmt.Println(\"saved\")\n```")
	task.Status.State = stateWorking
	s, out, notes := savedChat(t, http.StatusOK, func(listParams) listResult { return listResult{Tasks: []wireTask{task}} })
	in, keys, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	defer keys.Close()
	// Exit only after the saved answer is printed. No sleeps or live service.
	s.out = writerFunc(func(p []byte) (int, error) {
		n, err := out.Write(p)
		if strings.Contains(string(p), "saved") {
			if _, e := keys.WriteString("\x04"); e != nil {
				t.Errorf("send Ctrl-D: %v", e)
			}
		}
		return n, err
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.chat(ctx, in, ""); err != nil {
		t.Fatal(err)
	}
	if ctx.Err() != nil {
		t.Fatal("resume did not display history and exit on Ctrl-D")
	}
	text := ansi.Strip(out.String())
	for _, want := range []string{"Saved answer", "Hello again.", `fmt.Println("saved")`} {
		if strings.Count(text, want) != 1 {
			t.Errorf("saved answers %q should contain %q once", text, want)
		}
	}
	for _, raw := range []string{"# Saved", "**again**", "```", "Earlier prompt", "State when loaded"} {
		if strings.Contains(text, raw) {
			t.Errorf("saved answers contain raw Markdown or notes: %q", text)
		}
	}
	for _, want := range []string{"› Earlier prompt", "State when loaded: working", "Resume this session with:\nairo agent -a reviewer -r ctx-1"} {
		if !strings.Contains(ansi.Strip(notes.String()), want) {
			t.Errorf("notes lack %q: %q", want, notes.String())
		}
	}
}

func TestResumeKeepsDraftAndRestoresPromptRecall(t *testing.T) {
	m, prompts := testChat(t)
	m.loading = true
	m.input.SetValue("My next question")
	press(m, tea.KeyEnter, 0)
	if len(*prompts) != 0 || m.input.Value() != "My next question" {
		t.Fatal("Enter during history loading sent or lost the draft")
	}
	m.Update(historyLoadedMsg{turns: []Turn{{Messages: []Message{
		{From: FromYou, Text: "Previous question"},
		{From: FromAgent, Text: "Previous answer"},
	}}}})
	press(m, tea.KeyEnter, 0)
	if len(*prompts) != 0 {
		t.Fatal("Enter during history rendering sent the draft")
	}
	m.Update(printedMsg{})
	if m.loading || len(*prompts) != 0 || m.input.Value() != "My next question" {
		t.Fatal("finishing history replay sent or lost the draft")
	}
	press(m, tea.KeyUp, 0)
	if m.input.Value() != "Previous question" {
		t.Fatal("Up did not recall the saved prompt")
	}
	press(m, tea.KeyDown, 0)
	if m.input.Value() != "My next question" {
		t.Fatal("Down lost the draft typed while loading")
	}
	press(m, tea.KeyEnter, 0)
	if len(*prompts) != 1 || (*prompts)[0] != "My next question" {
		t.Fatalf("next prompt = %v", *prompts)
	}
}

func TestResumedSinglePromptPrintsOnlyItsNewAnswer(t *testing.T) {
	for _, mode := range []string{"plain", "terminal"} {
		t.Run(mode, func(t *testing.T) {
			s, requests, out, _ := talk(t, stream(result(submitted), result(artifact("a1", "New answer")), result(completed)))
			s.id = "ctx-1"
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var err error
			if mode == "plain" {
				err = s.once(ctx, "Follow-up")
			} else {
				err = s.chat(ctx, nil, "Follow-up")
			}
			if err != nil || ctx.Err() != nil {
				t.Fatalf("single prompt = %v, context = %v", err, ctx.Err())
			}
			want := "New answer"
			if mode == "terminal" {
				want = "• New answer"
			}
			if got := strings.TrimSpace(ansi.Strip(out.String())); got != want {
				t.Fatalf("stdout = %q", got)
			}
			if seen, _ := requests.seen(); len(seen) != 1 || seen[0].Method != methodStream || seen[0].Params.Message.ContextID != "ctx-1" {
				t.Fatalf("resumed one-shot requests = %+v", seen)
			}
		})
	}
}
