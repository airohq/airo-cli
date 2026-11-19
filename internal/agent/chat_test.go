package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func agentNamed(name string, isDefault bool) *unstructured.Unstructured {
	a := resource(agentGVK, name, map[string]any{})
	if isDefault {
		a.SetAnnotations(map[string]string{defaultAnnotation: "true"})
	}
	return a
}

// born returns u as created the given number of hours after a fixed time.
func born(u *unstructured.Unstructured, hours int) *unstructured.Unstructured {
	u.SetCreationTimestamp(metav1.NewTime(time.Date(2026, 1, 1, hours, 0, 0, 0, time.UTC)))
	return u
}

func TestPick(t *testing.T) {
	ctx := context.Background()
	never := func([]string) (string, error) {
		t.Error("asked, though the cluster answers")
		return "", nil
	}

	// The project's only agent.
	c := fake.NewClientBuilder().WithObjects(agentNamed("reviewer", false)).Build()
	if got, err := pick(ctx, c, "team", "", never); err != nil || got != "reviewer" {
		t.Errorf("only agent: %q, %v", got, err)
	}

	// The one marked as default, and the one named over it.
	c = fake.NewClientBuilder().WithObjects(agentNamed("coder", false), agentNamed("reviewer", true)).Build()
	if got, err := pick(ctx, c, "team", "", never); err != nil || got != "reviewer" {
		t.Errorf("default agent: %q, %v", got, err)
	}
	if got, err := pick(ctx, c, "team", "coder", never); err != nil || got != "coder" {
		t.Errorf("named agent: %q, %v", got, err)
	}
	if _, err := pick(ctx, c, "team", "writer", never); err == nil || !strings.Contains(err.Error(), "coder, reviewer") {
		t.Errorf("unknown name: %v, want the names", err)
	}

	// Of two defaults the newest wins: the project's own over the platform's.
	c = fake.NewClientBuilder().WithObjects(
		born(agentNamed("assistant", true), 1), born(agentNamed("reviewer", true), 2), born(agentNamed("coder", false), 3),
	).Build()
	if got, err := pick(ctx, c, "team", "", never); err != nil || got != "reviewer" {
		t.Errorf("newest default: %q, %v", got, err)
	}
	// The name decides between two of the same second.
	c = fake.NewClientBuilder().WithObjects(born(agentNamed("reviewer", true), 1), born(agentNamed("assistant", true), 1)).Build()
	if got, err := pick(ctx, c, "team", "", never); err != nil || got != "assistant" {
		t.Errorf("defaults of the same second: %q, %v", got, err)
	}

	// Several and no default: ask, or list the names when nobody is there.
	c = fake.NewClientBuilder().WithObjects(agentNamed("coder", false), agentNamed("reviewer", false)).Build()
	ask := func(names []string) (string, error) { return names[1], nil }
	if got, err := pick(ctx, c, "team", "", ask); err != nil || got != "reviewer" {
		t.Errorf("asked: %q, %v", got, err)
	}
	_, err := pick(ctx, c, "team", "", nil)
	if err == nil || !strings.Contains(err.Error(), "--agent: coder, reviewer") {
		t.Errorf("nobody to ask: %v, want the names and the flag", err)
	}

	// None.
	c = fake.NewClientBuilder().Build()
	if _, err := pick(ctx, c, "team", "", never); err == nil || !strings.Contains(err.Error(), "airo agent create") {
		t.Errorf("no agent: %v", err)
	}
}

func TestEndpoint(t *testing.T) {
	ctx := context.Background()
	cm := func(data map[string]string) *corev1.ConfigMap {
		return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "team", Name: "kagent"}, Data: data}
	}

	c := fake.NewClientBuilder().WithObjects(cm(map[string]string{"url": "https://agents.example.com/"})).Build()
	if got, err := endpoint(ctx, c, "team"); err != nil || got != "https://agents.example.com/" {
		t.Errorf("endpoint = %q, %v", got, err)
	}

	for name, tt := range map[string]struct {
		data map[string]string
		want string
	}{
		"no key":    {map[string]string{}, "no agent endpoint; ask a platform admin"},
		"not https": {map[string]string{"url": "http://agents.example.com"}, "unencrypted"},
		"no URL":    {map[string]string{"url": "agents"}, "no URL"},
	} {
		c := fake.NewClientBuilder().WithObjects(cm(tt.data)).Build()
		if _, err := endpoint(ctx, c, "team"); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: %v, want %q", name, err, tt.want)
		}
	}

	c = fake.NewClientBuilder().Build()
	if _, err := endpoint(ctx, c, "team"); err == nil || !strings.Contains(err.Error(), "no agent endpoint; ask a platform admin") {
		t.Errorf("no ConfigMap: %v", err)
	}
}

// api is a fake agents' API. It records the requests it gets.
type api struct {
	mu       sync.Mutex
	requests []rpcRequest
	tokens   []string
}

func (a *api) seen() ([]rpcRequest, []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.requests, a.tokens
}

// result is one SSE event with a JSON-RPC response that holds the result.
func result(object string) string {
	return "id: 1\ndata: {\"jsonrpc\":\"2.0\",\"id\":\"1\",\"result\":" + object + "}\n\n"
}

// talk returns a session with agent team/reviewer at a fake API, which
// answers each request with what answer writes.
func talk(t *testing.T, answer func(w http.ResponseWriter, r *http.Request)) (s *session, a *api, out, errOut *bytes.Buffer) {
	t.Helper()
	a = &api{}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/agents/team/reviewer" {
			t.Errorf("request = %s %s, want POST /agents/team/reviewer", r.Method, r.URL.Path)
		}
		for header, want := range map[string]string{
			"A2A-Version":  "1.0",
			"Content-Type": "application/json",
			"Accept":       "text/event-stream",
		} {
			if got := r.Header.Get(header); got != want {
				t.Errorf("header %s = %q, want %q", header, got, want)
			}
		}
		var req rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("request body: %v", err)
		}
		a.mu.Lock()
		a.requests = append(a.requests, req)
		a.tokens = append(a.tokens, r.Header.Get("Authorization"))
		a.mu.Unlock()
		answer(w, r)
	}))
	t.Cleanup(srv.Close)
	out, errOut = &bytes.Buffer{}, &bytes.Buffer{}
	calls := 0
	s = &session{
		api: &a2aClient{
			http: srv.Client(),
			url:  srv.URL + "/",
			token: func() (string, error) {
				calls++
				return fmt.Sprintf("token-%d", calls), nil
			},
		},
		ns:       "team",
		agent:    "reviewer",
		out:      out,
		errOut:   errOut,
		remember: func(string) {},
	}
	return s, a, out, errOut
}

// stream answers with server-sent events.
func stream(events ...string) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, e := range events {
			_, _ = io.WriteString(w, e)
			w.(http.Flusher).Flush()
		}
	}
}

const (
	submitted = `{"task":{"id":"task-1","contextId":"ctx-1","status":{"state":"TASK_STATE_SUBMITTED"}}}`
	working   = `{"statusUpdate":{"taskId":"task-1","contextId":"ctx-1","status":{"state":"TASK_STATE_WORKING"}}}`
	completed = `{"statusUpdate":{"taskId":"task-1","contextId":"ctx-1","status":{"state":"TASK_STATE_COMPLETED","timestamp":"2026-11-17T10:00:00Z"}}}`
)

func artifact(id, text string) string {
	return fmt.Sprintf(`{"artifactUpdate":{"taskId":"task-1","contextId":"ctx-1","append":true,"artifact":{"artifactId":%q,"parts":[{"text":%q}]}}}`, id, text)
}

func TestTurnSendsThePromptAndPrintsTheAnswer(t *testing.T) {
	s, a, out, _ := talk(t, stream(
		": a comment\n\n",
		result(submitted),
		result(working),
		result(artifact("a1", "Hel")),
		result(artifact("a1", "lo.")),
		// A tool call: a part without text, in an artifact of its own.
		result(`{"artifactUpdate":{"contextId":"ctx-1","artifact":{"artifactId":"a2","parts":[{"data":{"name":"shell"}}]}}}`),
		result(artifact("a3", "Done.\n")),
		result(completed),
	))
	var remembered []string
	s.remember = func(id string) { remembered = append(remembered, id) }

	if err := s.turn(context.Background(), "Say hello"); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "Hello.\nDone.\n"; got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
	if s.id != "ctx-1" || len(remembered) != 1 || remembered[0] != "ctx-1" {
		t.Errorf("session = %q, remembered %v; want ctx-1 once", s.id, remembered)
	}
	if err := s.turn(context.Background(), "Again"); err != nil {
		t.Fatal(err)
	}

	requests, tokens := a.seen()
	if len(requests) != 2 {
		t.Fatalf("%d requests, want 2", len(requests))
	}
	first, second := requests[0], requests[1]
	if first.JSONRPC != "2.0" || first.Method != "SendStreamingMessage" || first.ID == "" {
		t.Errorf("request = %+v", first)
	}
	m := first.Params.Message
	if m.Role != "ROLE_USER" || len(m.Parts) != 1 || m.Parts[0].Text != "Say hello" || len(m.MessageID) != 36 {
		t.Errorf("message = %+v", m)
	}
	if m.ContextID != "" {
		t.Errorf("the first message has contextId %q, want none: it starts a session", m.ContextID)
	}
	if second.Params.Message.ContextID != "ctx-1" || second.Params.Message.MessageID == m.MessageID {
		t.Errorf("second message = %+v, want contextId ctx-1 and a new ID", second.Params.Message)
	}
	// A fresh token for each request.
	if tokens[0] != "Bearer token-1" || tokens[1] != "Bearer token-2" {
		t.Errorf("tokens = %v", tokens)
	}
}

// reviewerWith returns agent reviewer with the conditions, each a type,
// a status and a message.
func reviewerWith(conditions ...[3]string) *unstructured.Unstructured {
	a := agentNamed("reviewer", false)
	a.SetGeneration(1)
	var list []any
	for _, c := range conditions {
		list = append(list, map[string]any{"type": c[0], "status": c[1], "message": c[2], "observedGeneration": int64(1)})
	}
	if list != nil {
		_ = unstructured.SetNestedSlice(a.Object, list, "status", "conditions")
	}
	return a
}

func TestAwaitAgent(t *testing.T) {
	ctx := context.Background()
	poll := readyPoll
	readyPoll = 10 * time.Millisecond
	t.Cleanup(func() { readyPoll = poll })
	// No terminal: the plain lines.
	in := &bytes.Buffer{}
	pending := reviewerWith(
		[3]string{"Accepted", "True", ""},
		[3]string{"Ready", "False", "waiting for the ActorTemplate golden snapshot"},
	)
	c := fake.NewClientBuilder().WithObjects(pending).Build()
	go func() {
		time.Sleep(50 * time.Millisecond)
		ready := reviewerWith([3]string{"Accepted", "True", ""}, [3]string{"Ready", "True", ""})
		a := &unstructured.Unstructured{}
		a.SetGroupVersionKind(agentGVK)
		if err := c.Get(ctx, client.ObjectKey{Namespace: "team", Name: "reviewer"}, a); err != nil {
			t.Error(err)
			return
		}
		a.Object["status"] = ready.Object["status"]
		if err := c.Update(ctx, a); err != nil {
			t.Error(err)
		}
	}()
	var errOut bytes.Buffer
	if err := awaitAgent(ctx, c, "team", "reviewer", in, &errOut); err != nil {
		t.Fatal(err)
	}
	// Said once, however often it looked.
	want := "Waiting for agent reviewer to become ready: waiting for the ActorTemplate golden snapshot\nAgent reviewer is ready.\n"
	if errOut.String() != want {
		t.Errorf("errOut = %q, want %q", errOut.String(), want)
	}

	for name, tt := range map[string]struct {
		agent *unstructured.Unstructured
		want  string
	}{
		"ready":       {reviewerWith([3]string{"Ready", "True", ""}), ""},
		"turned down": {reviewerWith([3]string{"Accepted", "False", "model sonnet cannot run on harness codex"}, [3]string{"Ready", "False", ""}), "agent reviewer cannot run: model sonnet cannot run on harness codex"},
	} {
		var errOut bytes.Buffer
		c := fake.NewClientBuilder().WithObjects(tt.agent).Build()
		err := awaitAgent(ctx, c, "team", "reviewer", in, &errOut)
		switch {
		case tt.want == "" && (err != nil || errOut.Len() != 0):
			t.Errorf("%s: %v, said %q; want no wait", name, err, errOut.String())
		case tt.want != "" && (err == nil || err.Error() != tt.want):
			t.Errorf("%s: %v, want %q", name, err, tt.want)
		}
	}

	// A new Agent, which kagent has not read yet, and Ctrl-C ends the wait.
	cancelled, cancel := context.WithCancel(ctx)
	defer cancel()
	c = fake.NewClientBuilder().WithObjects(reviewerWith()).Build()
	var said []string
	done := make(chan error, 1)
	go func() {
		done <- waitForSessions(cancelled, c, "team", "reviewer", func(why string) { said = append(said, why) }, time.Hour)
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled: %v, want context.Canceled", err)
	}
	if len(said) != 1 || said[0] != "the cluster has not reported on it yet" {
		t.Errorf("a new agent: said %q, want that the cluster has not reported", said)
	}
}

func TestTheFirstMessageHasNoContextIDOnTheWire(t *testing.T) {
	data, err := json.Marshal(sendParams{Message: wireMessage{MessageID: "m", Role: roleUser, Parts: []wirePart{{Text: "hi"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"message":{"messageId":"m","role":"ROLE_USER","parts":[{"text":"hi"}]}}`; string(data) != want {
		t.Errorf("params = %s, want %s", data, want)
	}
}

func TestTurnToleratesWhatItDoesNotKnow(t *testing.T) {
	s, _, out, _ := talk(t, stream(
		result(`{"task":{"contextId":"ctx-1","kind":"new","status":{"state":"TASK_STATE_SUBMITTED","extra":1},"history":[{"role":"ROLE_USER"}]}}`),
		result(`{"somethingNew":{"contextId":"ctx-1"}}`),
		"data: not json\n\n",
		"event: ping\ndata: {\"jsonrpc\":\"2.0\",\"id\":\"1\"}\n\n",
		result(`{"statusUpdate":{"contextId":"ctx-1","status":{"state":"TASK_STATE_NEWER"}}}`),
		// The person's own message, echoed.
		result(`{"statusUpdate":{"contextId":"ctx-1","status":{"state":"TASK_STATE_WORKING","message":{"role":"ROLE_USER","parts":[{"text":"Say hello"}]}}}}`),
		result(`{"statusUpdate":{"contextId":"ctx-1","status":{"state":"TASK_STATE_WORKING","message":{"role":"ROLE_AGENT","parts":[{"text":"Thinking."}]}}}}`),
		result(artifact("a1", "Hello.")),
		// The last event without the blank line that ends it.
		strings.TrimSuffix(result(completed), "\n"),
	))
	if err := s.turn(context.Background(), "Say hello"); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "Thinking.\nHello.\n"; got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
}

func TestTurnPrintsAMessageAndATaskThatAnswerAtOnce(t *testing.T) {
	s, _, out, _ := talk(t, stream(
		result(`{"message":{"messageId":"m1","contextId":"ctx-1","role":"ROLE_AGENT","parts":[{"text":"Hi."}]}}`),
	))
	if err := s.turn(context.Background(), "Hi"); err != nil {
		t.Fatal(err)
	}
	if out.String() != "Hi.\n" || s.id != "ctx-1" {
		t.Errorf("output = %q, session %q", out.String(), s.id)
	}

	s, _, out, _ = talk(t, stream(
		result(`{"task":{"contextId":"ctx-2","status":{"state":"TASK_STATE_COMPLETED"},"artifacts":[{"artifactId":"a1","parts":[{"text":"All of it."}]}]}}`),
	))
	if err := s.turn(context.Background(), "Hi"); err != nil {
		t.Fatal(err)
	}
	if out.String() != "All of it.\n" {
		t.Errorf("output = %q", out.String())
	}
}

func TestTurnFails(t *testing.T) {
	failed := `{"statusUpdate":{"contextId":"ctx-1","status":{"state":"TASK_STATE_FAILED","message":{"role":"ROLE_AGENT","parts":[{"text":"the model is gone"}]}}}}`
	rpcError := func(code int, message string) string {
		return fmt.Sprintf(`{"jsonrpc":"2.0","id":"1","error":{"code":%d,"message":%q,"data":[{"@type":"x"}]}}`, code, message)
	}
	status := func(code int) func(http.ResponseWriter, *http.Request) {
		return func(w http.ResponseWriter, _ *http.Request) { http.Error(w, http.StatusText(code), code) }
	}
	plain := func(body string) func(http.ResponseWriter, *http.Request) {
		return func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, body)
		}
	}
	for name, tt := range map[string]struct {
		answer func(http.ResponseWriter, *http.Request)
		want   string
	}{
		"failed task":          {stream(result(submitted), result(artifact("a1", "I try.")), result(failed)), "the agent failed: the model is gone"},
		"rejected task":        {stream(result(`{"task":{"contextId":"ctx-1","status":{"state":"TASK_STATE_REJECTED"}}}`)), "turned the prompt down"},
		"error in the stream":  {stream(result(submitted), "data: "+rpcError(-32603, "it broke")+"\n\n"), "it broke (code -32603)"},
		"error before it":      {plain(rpcError(-32001, "no such session")), "no such session"},
		"unauthenticated call": {plain(rpcError(-31401, "no")), "airo login"},
		"unauthorized call":    {plain(rpcError(-31403, "no")), "no access to this tenant's agents"},
		"another version":      {plain(rpcError(-32009, "served 1.1")), "update airo"},
		"401":                  {status(401), "sign in again with airo login"},
		"403":                  {status(403), "no access to this tenant's agents"},
		"404":                  {status(404), "/agents/team/reviewer"},
		"500":                  {status(500), "500 Internal Server Error: Internal Server Error"},
		"cut off":              {stream(result(submitted), result(working)), "before the agent finished"},
		"nothing":              {stream(), "sent no answer"},
		"only the unknown":     {stream(result(`{"somethingNew":{}}`)), "update it"},
	} {
		s, _, out, _ := talk(t, tt.answer)
		err := s.turn(context.Background(), "Do it")
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: %v, want %q", name, err, tt.want)
		}
		if strings.Contains(out.String(), "the model is gone") {
			t.Errorf("%s: the reason is in the output %q, want it only in the error", name, out.String())
		}
	}
}

func TestTurnEndsWhenTheAgentWaitsForThePerson(t *testing.T) {
	s, _, out, errOut := talk(t, func(w http.ResponseWriter, r *http.Request) {
		stream(
			result(submitted),
			result(`{"statusUpdate":{"contextId":"ctx-1","status":{"state":"TASK_STATE_INPUT_REQUIRED","message":{"role":"ROLE_AGENT","parts":[{"text":"Which branch?"}]}}}}`),
		)(w, r)
		// A server that keeps the stream open.
		<-r.Context().Done()
	})
	if err := s.turn(context.Background(), "Fix it"); err != nil {
		t.Fatal(err)
	}
	if out.String() != "Which branch?\n" || !strings.Contains(errOut.String(), "waits for your answer") {
		t.Errorf("output = %q, notes %q", out.String(), errOut.String())
	}
}

func TestOnceSaysHowToResume(t *testing.T) {
	s, _, _, errOut := talk(t, stream(result(submitted), result(artifact("a1", "Hello.")), result(completed)))
	if err := s.once(context.Background(), "Say hello"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errOut.String(), "airo chat -a reviewer -r ctx-1") {
		t.Errorf("notes %q lack how to resume", errOut.String())
	}
}

func TestCtrlCDetaches(t *testing.T) {
	ctx, interrupt := context.WithCancel(context.Background())
	defer interrupt()
	s, _, out, errOut := talk(t, func(w http.ResponseWriter, r *http.Request) {
		stream(result(submitted), result(artifact("a1", "I start")))(w, r)
		// The agent works on.
		<-r.Context().Done()
	})
	// The person presses ctrl-C when the first text shows.
	s.out = writerFunc(func(p []byte) (int, error) {
		interrupt()
		return out.Write(p)
	})
	if err := s.once(ctx, "Take your time"); err != nil {
		t.Fatalf("once = %v, want no error: detaching is no failure", err)
	}
	if out.String() != "I start\n" {
		t.Errorf("output = %q", out.String())
	}
	if !strings.Contains(errOut.String(), "airo chat -a reviewer -r ctx-1") {
		t.Errorf("notes %q lack how to resume", errOut.String())
	}
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

func TestConverseSendsEachLineToOneSession(t *testing.T) {
	s, a, out, errOut := talk(t, stream(result(submitted), result(artifact("a1", "Yes.")), result(completed)))
	in := &lines{r: bufio.NewReader(strings.NewReader("First\n\nSecond"))}
	if err := s.converse(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	requests, _ := a.seen()
	if len(requests) != 2 {
		t.Fatalf("%d requests, want 2: an empty line is no prompt", len(requests))
	}
	if got := requests[1].Params.Message; got.Parts[0].Text != "Second" || got.ContextID != "ctx-1" {
		t.Errorf("second message = %+v", got)
	}
	if out.String() != "Yes.\nYes.\n" {
		t.Errorf("output = %q", out.String())
	}
	if !strings.Contains(errOut.String(), "airo chat -a reviewer -r ctx-1") {
		t.Errorf("notes %q lack how to resume", errOut.String())
	}
}

func TestConverseGoesOnAfterAFailedTurn(t *testing.T) {
	failed := `{"statusUpdate":{"contextId":"ctx-1","status":{"state":"TASK_STATE_FAILED"}}}`
	s, a, _, errOut := talk(t, stream(result(submitted), result(failed)))
	in := &lines{r: bufio.NewReader(strings.NewReader("First\nSecond\n"))}
	if err := s.converse(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if requests, _ := a.seen(); len(requests) != 2 {
		t.Errorf("%d requests, want 2", len(requests))
	}
	if !strings.Contains(errOut.String(), "the agent failed") {
		t.Errorf("notes %q lack the failure", errOut.String())
	}
}

func TestTaskErrorIsTheTurnsError(t *testing.T) {
	var failed *taskError
	if err := error(&taskError{state: stateCanceled}); !errors.As(err, &failed) || err.Error() != "the turn was canceled" {
		t.Errorf("error = %v", err)
	}
}

func TestSessionStore(t *testing.T) {
	// os.UserCacheDir reads these.
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CACHE_HOME", dir)
	store, err := newSessionStore()
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(filepath.Dir(store.path)) != "airo" {
		t.Errorf("path = %s, want it in the directory airo", store.path)
	}

	reviewer := sessionKey("https://cluster.example.com", "team", "reviewer")
	coder := sessionKey("https://cluster.example.com", "team", "coder")
	elsewhere := sessionKey("https://other.example.com", "team", "reviewer")
	if got := store.get(reviewer); got != "" {
		t.Errorf("no file: %q", got)
	}
	for key, id := range map[string]string{reviewer: "old", coder: "ctx-2"} {
		if err := store.put(key, id); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.put(reviewer, "ctx-1"); err != nil {
		t.Fatal(err)
	}
	if store.get(reviewer) != "ctx-1" || store.get(coder) != "ctx-2" || store.get(elsewhere) != "" {
		t.Errorf("sessions = %v", store.load())
	}
	if info, err := os.Stat(store.path); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("file mode = %v, %v; want 0600", info.Mode(), err)
	}

	// A broken file is an empty one.
	if err := os.WriteFile(store.path, []byte("null"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.put(reviewer, "ctx-3"); err != nil || store.get(reviewer) != "ctx-3" {
		t.Errorf("after a broken file: %q, %v", store.get(reviewer), err)
	}
}

// root is the command as cmd/airo builds it, with kubectl's flags.
func root() *cobra.Command {
	flags := genericclioptions.NewConfigFlags(true)
	root := &cobra.Command{Use: "airo"}
	flags.AddFlags(root.PersistentFlags())
	root.AddCommand(NewCommand(flags), NewChatCommand(flags))
	return root
}

func TestChatAcceptsAgentVerbsAsPrompts(t *testing.T) {
	for line, want := range map[string]string{
		"agent list":               "list",
		"chat list":                "chat",
		"chat -a reviewer -- list": "chat",
		"chat fix the build":       "chat",
		"chat -c again":            "chat",
		"agent create reviewer":    "create",
	} {
		cmd, _, err := root().Find(strings.Fields(line))
		if err != nil || cmd.Name() != want {
			t.Errorf("airo %s runs %q, %v; want %q", line, cmd.Name(), err, want)
		}
	}
}

func TestFlagsFitKubectls(t *testing.T) {
	// A shorthand that kubectl's flags have too would panic here.
	r := root()
	r.SetArgs([]string{"chat", "--help"})
	r.SetOut(io.Discard)
	if err := r.Execute(); err != nil {
		t.Fatal(err)
	}
	r = root()
	r.SetArgs([]string{"chat", "-c", "-r", "ctx-1", "hi"})
	r.SetOut(io.Discard)
	r.SetErr(io.Discard)
	if err := r.Execute(); err == nil || !strings.Contains(err.Error(), "continue") {
		t.Errorf("-c with -r: %v, want them refused together", err)
	}
}

func TestProjectCredentialWatch(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c := fake.NewClientBuilder().WithIndex(&corev1.ConfigMap{}, "metadata.name", func(o client.Object) []string {
		return []string{o.GetName()}
	}).Build()
	done := make(chan error, 1)
	go func() { done <- waitProjectCredentials(ctx, c, "team", "agent-credentials-ready", io.Discard) }()
	marker := func(ns string) *corev1.ConfigMap {
		return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "agent-credentials-ready"}}
	}
	if err := c.Create(ctx, marker("other")); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		t.Fatalf("another project's grant released the wait: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := c.Create(ctx, marker("team")); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestChatProjectDuringProvisioning(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	oldPoll := readyPoll
	readyPoll = time.Millisecond
	defer func() { readyPoll = oldPoll }()
	c := fake.NewClientBuilder().WithObjects(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team", CreationTimestamp: metav1.Now()}}).Build()
	done := make(chan error, 1)
	go func() { _, err := chatProject(ctx, c, "team", io.Discard); done <- err }()
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "kagent", Namespace: "team"}}
	if err := c.Create(ctx, cm); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		t.Fatalf("endpoint alone released the wait: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	a := agentNamed("assistant", true)
	a.SetNamespace("team")
	if err := c.Create(ctx, a); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
