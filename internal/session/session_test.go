package session

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/airohq/airo-cli/internal/agent"
)

func TestMain(m *testing.M) {
	// A conversation shows local time.
	time.Local = time.UTC
	os.Exit(m.Run())
}

var now = time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)

// call is one request the fake agents' API got.
type call struct {
	Agent  string
	Method string `json:"method"`
	Params struct {
		ID               string `json:"id"`
		ContextID        string `json:"contextId"`
		PageSize         int    `json:"pageSize"`
		PageToken        string `json:"pageToken"`
		IncludeArtifacts bool   `json:"includeArtifacts"`
	} `json:"params"`
}

type calls struct {
	mu   sync.Mutex
	list []call
}

func (c *calls) of(agent, method string) []call {
	c.mu.Lock()
	defer c.mu.Unlock()
	var found []call
	for _, call := range c.list {
		if call.Agent == agent && call.Method == method {
			found = append(found, call)
		}
	}
	return found
}

// project returns the agents' API of project team with the agents named. An
// agent answers a request with the JSON-RPC response its function returns,
// or with a server error for an empty one.
func project(t *testing.T, agents map[string]func(call) string) (*agent.API, *calls) {
	t.Helper()
	seen := &calls{}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name, ok := strings.CutPrefix(r.URL.Path, "/agents/team/")
		if r.Method != http.MethodPost || !ok {
			t.Errorf("request = %s %s, want POST /agents/team/NAME", r.Method, r.URL.Path)
		}
		for header, want := range map[string]string{
			"Authorization": "Bearer token",
			"A2A-Version":   "1.0",
			"Content-Type":  "application/json",
		} {
			if got := r.Header.Get(header); got != want {
				t.Errorf("header %s = %q, want %q", header, got, want)
			}
		}
		c := call{Agent: name}
		if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
			t.Errorf("request body: %v", err)
		}
		seen.mu.Lock()
		seen.list = append(seen.list, c)
		seen.mu.Unlock()
		answer := agents[name](c)
		if answer == "" {
			http.Error(w, "the agent is not ready", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, answer)
	}))
	t.Cleanup(srv.Close)

	var objects []client.Object
	for name := range agents {
		// The Agent kind, as internal/agent names it.
		a := &unstructured.Unstructured{}
		a.SetAPIVersion("api.kagent.dev/v1alpha3")
		a.SetKind("Agent")
		a.SetNamespace("team")
		a.SetName(name)
		objects = append(objects, a)
	}
	c := fake.NewClientBuilder().WithObjects(objects...).Build()
	return agent.NewAPI(c, "team", srv.URL, srv.Client(), func() (string, error) { return "token", nil }), seen
}

func result(object string) string {
	return `{"jsonrpc":"2.0","id":"1","result":` + object + `}`
}

func rpcError(code int, message string) string {
	return fmt.Sprintf(`{"jsonrpc":"2.0","id":"1","error":{"code":%d,"message":%q}}`, code, message)
}

func page(next string, tasks ...string) string {
	return result(fmt.Sprintf(`{"tasks":[%s],"totalSize":9,"pageSize":100,"nextPageToken":%q}`, strings.Join(tasks, ","), next))
}

// task is a task as kagent sends it: the prompt in the history, the answer
// in an artifact, sent in pieces.
func task(id, session, state, at, prompt string, answer ...string) string {
	artifacts := ""
	if len(answer) > 0 {
		var parts []string
		for _, text := range answer {
			parts = append(parts, fmt.Sprintf(`{"text":%q}`, text))
		}
		artifacts = fmt.Sprintf(`"artifacts":[{"artifactId":"a-%s","metadata":{"kagent.dev/a2a/timeline-position":"2026-11-17T%s:30Z"},"parts":[%s]}],`, id, at, strings.Join(parts, ","))
	}
	return fmt.Sprintf(`{"id":%q,"contextId":%q,%s
		"history":[{"messageId":"m-%s","role":"ROLE_USER","metadata":{"kagent.dev/a2a/timeline-position":"2026-11-17T%s:00Z"},"parts":[{"text":%q}]}],
		"metadata":{"kagent.dev/a2a/task-created-at":"2026-11-17T%s:01Z"},
		"status":{"state":%q,"timestamp":"2026-11-17T%s:40Z"}}`,
		id, session, artifacts, id, at, prompt, at, state, at)
}

var (
	first  = task("t1", "s1", "TASK_STATE_COMPLETED", "08:00", "Why does the build fail?\nSee the log.", "The lint step ", "fails.\n")
	second = task("t2", "s1", "TASK_STATE_FAILED", "09:00", "Fix it.")
	other  = task("t3", "s2", "TASK_STATE_INPUT_REQUIRED", "09:30", "Release it.")
	fresh  = task("t4", "s3", "TASK_STATE_WORKING", "09:59", strings.Repeat("Count slowly. ", 10))
)

// reviewer has two sessions, on two pages. It answers a session of another
// agent as kagent does.
func reviewer(c call) string {
	switch {
	case c.Method != "ListTasks":
		return rpcError(-32601, "method not found")
	case c.Params.ContextID == "s1":
		return page("", first, second)
	case c.Params.ContextID == "s2":
		return page("", other)
	case c.Params.ContextID != "":
		return rpcError(-31403, "context does not belong to this Agent")
	case c.Params.PageToken == "":
		return page("next", first, other)
	}
	return page("", second)
}

func coder(c call) string {
	switch {
	case c.Method == "CancelTask":
		return result(task(c.Params.ID, "s3", "TASK_STATE_CANCELED", "09:59", "Count slowly."))
	case c.Params.ContextID == "" || c.Params.ContextID == "s3":
		return page("", fresh)
	}
	return rpcError(-32001, "task not found")
}

func broken(call) string { return "" }

func TestListGroupsTheTurnsOfEveryAgentIntoSessions(t *testing.T) {
	api, seen := project(t, map[string]func(call) string{"reviewer": reviewer, "coder": coder, "broken": broken})
	var out, errOut bytes.Buffer
	if err := list(context.Background(), &out, &errOut, api, options{}, now); err != nil {
		t.Fatal(err)
	}
	want := `SESSION   AGENT      LAST ACTIVE   STATE         FIRST PROMPT
s3        coder      20s           working       Count slowly. Count slowly. Count slowly. Count slowly. Cou…
s2        reviewer   29m           needs input   Release it.
s1        reviewer   59m           failed        Why does the build fail? See the log.
`
	if out.String() != want {
		t.Errorf("list =\n%s\nwant\n%s", out.String(), want)
	}
	// The agent that fails is a note, not the command's error.
	if got := errOut.String(); !strings.Contains(got, "Could not list the sessions with agent broken") || strings.Count(got, "\n") != 1 {
		t.Errorf("notes = %q, want one about agent broken", got)
	}

	pages := seen.of("reviewer", "ListTasks")
	if len(pages) != 2 || pages[0].Params.PageToken != "" || pages[1].Params.PageToken != "next" {
		t.Fatalf("reviewer's pages = %+v, want two, the second with the token", pages)
	}
	if p := pages[0].Params; p.PageSize != 100 || p.ContextID != "" || p.IncludeArtifacts {
		t.Errorf("list params = %+v, want pageSize 100, no contextId and no artifacts", p)
	}
}

func TestListOfOneAgentAsJSON(t *testing.T) {
	api, seen := project(t, map[string]func(call) string{"reviewer": reviewer, "broken": broken})
	var out, errOut bytes.Buffer
	if err := list(context.Background(), &out, &errOut, api, options{agent: "reviewer", output: "json"}, now); err != nil {
		t.Fatal(err)
	}
	var sessions []Session
	if err := json.Unmarshal(out.Bytes(), &sessions); err != nil {
		t.Fatalf("%v in %s", err, out.String())
	}
	if len(sessions) != 2 || sessions[0].ID != "s2" || sessions[1].ID != "s1" {
		t.Fatalf("sessions = %+v, want s2 and s1", sessions)
	}
	s := sessions[1]
	if s.Agent != "reviewer" || s.State != "failed" || s.FirstPrompt != "Why does the build fail?\nSee the log." ||
		!s.Started.Equal(time.Date(2026, 11, 17, 8, 0, 1, 0, time.UTC)) ||
		!s.LastActive.Equal(time.Date(2026, 11, 17, 9, 0, 40, 0, time.UTC)) || s.Turns != nil {
		t.Errorf("session = %+v", s)
	}
	if len(seen.of("broken", "ListTasks")) != 0 || errOut.Len() != 0 {
		t.Errorf("asked agent broken, though --agent names reviewer: %s", errOut.String())
	}

	out.Reset()
	if err := list(context.Background(), &out, &errOut, api, options{agent: "reviewer", output: "yaml"}, now); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "- agent: reviewer\n") || !strings.Contains(out.String(), "  id: s2\n") {
		t.Errorf("yaml = %s", out.String())
	}
}

func TestListWithoutSessions(t *testing.T) {
	none := func(call) string { return page("") }
	api, _ := project(t, map[string]func(call) string{"reviewer": none, "coder": none})
	for _, tt := range []struct {
		o    options
		want string
	}{
		{options{}, "No sessions in team\n"},
		{options{agent: "coder"}, "No sessions with agent coder in team\n"},
		{options{output: "json"}, "[]\n"},
	} {
		var out, errOut bytes.Buffer
		if err := list(context.Background(), &out, &errOut, api, tt.o, now); err != nil {
			t.Fatal(err)
		}
		if out.String() != tt.want || errOut.Len() != 0 {
			t.Errorf("%+v: %q and %q, want %q", tt.o, out.String(), errOut.String(), tt.want)
		}
	}

	// No agent answers: the command fails. No agent at all: nothing to list.
	api, _ = project(t, map[string]func(call) string{"broken": broken})
	var out bytes.Buffer
	if err := list(context.Background(), &out, &out, api, options{}, now); err == nil || !strings.Contains(err.Error(), "not ready") {
		t.Errorf("every agent fails: %v and %q", err, out.String())
	}
	api, _ = project(t, nil)
	if err := list(context.Background(), &out, &out, api, options{}, now); err != nil || out.String() != "No sessions in team\n" {
		t.Errorf("no agents: %v and %q", err, out.String())
	}
	if err := list(context.Background(), &out, &out, api, options{agent: "writer"}, now); err == nil || !strings.Contains(err.Error(), "no agent named writer") {
		t.Errorf("unknown agent: %v", err)
	}
}

func TestViewPrintsTheConversation(t *testing.T) {
	api, seen := project(t, map[string]func(call) string{"reviewer": reviewer, "coder": coder})
	var out, errOut bytes.Buffer
	if err := view(context.Background(), &out, &errOut, api, options{}, "s1", now); err != nil {
		t.Fatal(err)
	}
	want := `Session s1 with agent reviewer

You, 2026-11-17 08:00
  Why does the build fail?
  See the log.

Agent, 2026-11-17 08:00
  The lint step fails.

You, 2026-11-17 09:00
  Fix it.

State: failed, last active 59m ago
`
	if out.String() != want {
		t.Errorf("view =\n%s\nwant\n%s", out.String(), want)
	}
	if got, want := errOut.String(), "\nResume this session with:\nairo chat -a reviewer -r s1\n"; got != want {
		t.Errorf("hint = %q, want %q", got, want)
	}
	asked := seen.of("reviewer", "ListTasks")
	if len(asked) != 1 || asked[0].Params.ContextID != "s1" || !asked[0].Params.IncludeArtifacts {
		t.Errorf("reviewer was asked %+v, want the session with its artifacts", asked)
	}
	// Every agent is asked, because only the agent knows its sessions.
	if len(seen.of("coder", "ListTasks")) != 1 {
		t.Error("coder was not asked for the session")
	}
}

func TestViewMarksATurnThatDidNotComplete(t *testing.T) {
	canceled := task("t1", "s1", "TASK_STATE_CANCELED", "08:00", "Count.")
	done := task("t2", "s1", "TASK_STATE_COMPLETED", "08:10", "Stop.", "Stopped.")
	api, _ := project(t, map[string]func(call) string{"reviewer": func(call) string { return page("", done, canceled) }})
	var out bytes.Buffer
	if err := view(context.Background(), &out, io.Discard, api, options{}, "s1", now); err != nil {
		t.Fatal(err)
	}
	// Oldest first, whatever the API's order.
	want := `Session s1 with agent reviewer

You, 2026-11-17 08:00
  Count.

(this turn: canceled)

You, 2026-11-17 08:10
  Stop.

Agent, 2026-11-17 08:10
  Stopped.

State: done, last active 109m ago
`
	if out.String() != want {
		t.Errorf("view =\n%s\nwant\n%s", out.String(), want)
	}
}

func TestViewAsJSON(t *testing.T) {
	api, _ := project(t, map[string]func(call) string{"reviewer": reviewer, "coder": coder})
	var out, errOut bytes.Buffer
	if err := view(context.Background(), &out, &errOut, api, options{agent: "reviewer", output: "json"}, "s1", now); err != nil {
		t.Fatal(err)
	}
	var s Session
	if err := json.Unmarshal(out.Bytes(), &s); err != nil {
		t.Fatalf("%v in %s", err, out.String())
	}
	if s.ID != "s1" || s.Agent != "reviewer" || s.State != "failed" || len(s.Turns) != 2 {
		t.Fatalf("session = %+v", s)
	}
	turn := s.Turns[0]
	if turn.ID != "t1" || turn.State != "done" || len(turn.Messages) != 2 {
		t.Fatalf("turn = %+v", turn)
	}
	if m := turn.Messages[1]; m.From != "agent" || m.Text != "The lint step fails.\n" || !m.Time.Equal(time.Date(2026, 11, 17, 8, 0, 30, 0, time.UTC)) {
		t.Errorf("answer = %+v", m)
	}
	if errOut.Len() != 0 {
		t.Errorf("notes with -o json: %q", errOut.String())
	}
}

func TestViewOfASessionNobodyHas(t *testing.T) {
	api, _ := project(t, map[string]func(call) string{"reviewer": reviewer, "coder": coder, "broken": broken})
	ctx := context.Background()

	err := view(ctx, io.Discard, io.Discard, api, options{agent: "coder"}, "s1", now)
	if err == nil || !strings.Contains(err.Error(), "no session s1 of yours with agent coder in project team") {
		t.Errorf("another agent's session: %v", err)
	}
	// An agent that did not answer may have it.
	err = view(ctx, io.Discard, io.Discard, api, options{}, "s9", now)
	if err == nil || !strings.Contains(err.Error(), "1 of 3 agents did not answer: agent broken") {
		t.Errorf("unknown session: %v", err)
	}
	// The session is found though another agent fails.
	if err := view(ctx, io.Discard, io.Discard, api, options{}, "s3", now); err != nil {
		t.Errorf("a session of coder, while broken fails: %v", err)
	}

	api, _ = project(t, map[string]func(call) string{"reviewer": reviewer, "coder": coder})
	err = view(ctx, io.Discard, io.Discard, api, options{}, "s9", now)
	if err == nil || !strings.Contains(err.Error(), "no session s9 of yours with project team: see yours with airo session list") {
		t.Errorf("unknown session: %v", err)
	}
	invalid := func(call) string { return rpcError(-32600, "Session identifier is invalid") }
	api, _ = project(t, map[string]func(call) string{"reviewer": invalid, "coder": invalid})
	err = view(ctx, io.Discard, io.Discard, api, options{}, "x", now)
	if err == nil || !strings.Contains(err.Error(), "look for session x with project team") || !strings.Contains(err.Error(), "Session identifier is invalid") {
		t.Errorf("an ID the API cannot read: %v", err)
	}
}

func TestCancelStopsTheRunningTurn(t *testing.T) {
	api, seen := project(t, map[string]func(call) string{"reviewer": reviewer, "coder": coder})
	var out, errOut bytes.Buffer
	if err := cancel(context.Background(), &out, &errOut, api, options{}, "s3"); err != nil {
		t.Fatal(err)
	}
	want := `Canceled the working turn of session s3 with agent coder
  prompt: Count slowly. Count slowly. Count slowly. Count slowly. Cou…
  state:  canceled
`
	if out.String() != want {
		t.Errorf("cancel =\n%s\nwant\n%s", out.String(), want)
	}
	if got, want := errOut.String(), "\nThe session stays.\nResume this session with:\nairo chat -a coder -r s3\n"; got != want {
		t.Errorf("hint = %q, want %q", got, want)
	}
	canceled := seen.of("coder", "CancelTask")
	if len(canceled) != 1 || canceled[0].Params.ID != "t4" {
		t.Errorf("CancelTask calls = %+v, want one for t4", canceled)
	}
}

func TestCancelATurnThatWaitsOrIsOver(t *testing.T) {
	waits := func(c call) string {
		if c.Method == "CancelTask" {
			// The agent has not stopped yet.
			return result(task(c.Params.ID, "s2", "TASK_STATE_INPUT_REQUIRED", "09:30", "Release it."))
		}
		return reviewer(c)
	}
	api, seen := project(t, map[string]func(call) string{"reviewer": waits})
	var out, errOut bytes.Buffer
	if err := cancel(context.Background(), &out, &errOut, api, options{agent: "reviewer"}, "s2"); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.String(), "Asked agent reviewer to cancel the needs input turn of session s2\n") || !strings.Contains(out.String(), "state:  needs input\n") {
		t.Errorf("cancel = %q", out.String())
	}

	// Nothing runs: no call, a note and no error.
	out.Reset()
	errOut.Reset()
	if err := cancel(context.Background(), &out, &errOut, api, options{}, "s1"); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "Session s1 with agent reviewer has no running turn: its last turn is failed\n"; got != want || errOut.Len() != 0 {
		t.Errorf("cancel = %q and %q, want %q", got, errOut.String(), want)
	}
	if n := len(seen.of("reviewer", "CancelTask")); n != 1 {
		t.Errorf("%d CancelTask calls, want only the one for s2", n)
	}

	if err := cancel(context.Background(), &out, &errOut, api, options{}, "s9"); err == nil || !strings.Contains(err.Error(), "no session s9") {
		t.Errorf("unknown session: %v", err)
	}
}
