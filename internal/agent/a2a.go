package agent

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// This file holds everything airo knows about the wire of the agents' API:
// the requests it sends, the shapes it reads and how they become events and
// turns. The values are
// copied on purpose. The CLI depends on the protocol, never on a component's
// or a library's code, and a released binary pins the protocol anyway.

// A2A protocol 1.0, JSON-RPC binding.
const (
	a2aVersionHeader = "A2A-Version"
	a2aVersion       = "1.0"
	jsonrpcVersion   = "2.0"
	methodStream     = "SendStreamingMessage"
	methodListTasks  = "ListTasks"
	methodCancelTask = "CancelTask"
	roleUser         = "ROLE_USER"

	// The largest page ListTasks gives.
	maxPageSize = 100

	// The states of a task that is worked on.
	stateSubmitted = "TASK_STATE_SUBMITTED"
	stateWorking   = "TASK_STATE_WORKING"

	// The states that end a turn. In the last two the agent waits for the
	// person. A task in any other state is still worked on.
	stateCompleted     = "TASK_STATE_COMPLETED"
	stateFailed        = "TASK_STATE_FAILED"
	stateCanceled      = "TASK_STATE_CANCELED"
	stateRejected      = "TASK_STATE_REJECTED"
	stateInputRequired = "TASK_STATE_INPUT_REQUIRED"
	stateAuthRequired  = "TASK_STATE_AUTH_REQUIRED"

	// Error codes of the binding that get their own message.
	codeUnauthenticated     = -31401
	codeUnauthorized        = -31403
	codeVersionNotSupported = -32009
	codeTaskNotFound        = -32001
)

// kagent agent path: an Agent's A2A endpoint is <url>/agents/<namespace>/<name>.
const agentPathPrefix = "/agents/"

// kagent's A2A metadata, read from a task and from its messages and
// artifacts: when it was created, as RFC 3339. The protocol itself only has
// the time of a task's last state.
const (
	metaTaskCreated = "kagent.dev/a2a/task-created-at"
	metaPosition    = "kagent.dev/a2a/timeline-position"
)

// The request that sends a prompt.
type rpcRequest struct {
	JSONRPC string     `json:"jsonrpc"`
	ID      string     `json:"id"`
	Method  string     `json:"method"`
	Params  sendParams `json:"params"`
}

type sendParams struct {
	Message wireMessage `json:"message"`
}

// The request of any other method.
type callRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      string `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params"`
}

// listParams are the parameters of ListTasks. Without a contextId it lists
// the caller's tasks with the agent in every session. A task comes with its
// whole history, and with its artifacts only when asked.
type listParams struct {
	ContextID        string `json:"contextId,omitempty"`
	PageSize         int    `json:"pageSize,omitempty"`
	PageToken        string `json:"pageToken,omitempty"`
	IncludeArtifacts bool   `json:"includeArtifacts,omitempty"`
}

type listResult struct {
	Tasks         []wireTask `json:"tasks"`
	NextPageToken string     `json:"nextPageToken"`
}

// taskParams are the parameters of CancelTask.
type taskParams struct {
	ID string `json:"id"`
}

// The response: each SSE data block is one of these.
type rpcResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// streamResult is the result of a response: an object with exactly one of
// these keys.
type streamResult struct {
	Task           *wireTask           `json:"task"`
	Message        *wireMessage        `json:"message"`
	StatusUpdate   *wireStatusUpdate   `json:"statusUpdate"`
	ArtifactUpdate *wireArtifactUpdate `json:"artifactUpdate"`
}

type wireMessage struct {
	MessageID string         `json:"messageId,omitempty"`
	Role      string         `json:"role,omitempty"`
	Parts     []wirePart     `json:"parts"`
	ContextID string         `json:"contextId,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

// wirePart is a part's text. A part with a file or data has none.
type wirePart struct {
	Text string `json:"text,omitempty"`
}

type wireTask struct {
	ID        string         `json:"id"`
	ContextID string         `json:"contextId"`
	Status    wireStatus     `json:"status"`
	History   []wireMessage  `json:"history"`
	Artifacts []wireArtifact `json:"artifacts"`
	Metadata  map[string]any `json:"metadata"`
}

type wireStatus struct {
	State     string       `json:"state"`
	Timestamp string       `json:"timestamp"`
	Message   *wireMessage `json:"message"`
}

type wireStatusUpdate struct {
	ContextID string     `json:"contextId"`
	Status    wireStatus `json:"status"`
}

type wireArtifactUpdate struct {
	ContextID string        `json:"contextId"`
	Artifact  *wireArtifact `json:"artifact"`
}

type wireArtifact struct {
	ArtifactID string         `json:"artifactId"`
	Parts      []wirePart     `json:"parts"`
	Metadata   map[string]any `json:"metadata"`
}

// event is what one stream object means to the command.
type event struct {
	// contextID is the session's ID.
	contextID string
	// state is the task's state, or empty when the object has none.
	state string
	// text is the agent's text in this object.
	text string
	// block names what the text belongs to, such as an artifact's ID. Text of
	// one block is one run, sent in pieces. Empty for a whole message.
	block string
	// snapshot is true for text that a task holds already, which earlier
	// objects of the stream may have carried.
	snapshot bool
	// reply is true for a message that answers in place of a task.
	reply bool
}

// events returns what a result holds. It returns nothing for a result this
// version cannot read.
func (r streamResult) events() []event {
	switch {
	case r.Task != nil:
		var es []event
		for _, a := range r.Task.Artifacts {
			if text := partsText(a.Parts); text != "" {
				es = append(es, event{contextID: r.Task.ContextID, text: text, block: a.ArtifactID, snapshot: true})
			}
		}
		return append(es, event{
			contextID: r.Task.ContextID,
			state:     r.Task.Status.State,
			text:      agentText(r.Task.Status.Message),
			snapshot:  true,
		})
	case r.Message != nil:
		return []event{{contextID: r.Message.ContextID, text: agentText(r.Message), reply: r.Message.Role != roleUser}}
	case r.StatusUpdate != nil:
		return []event{{
			contextID: r.StatusUpdate.ContextID,
			state:     r.StatusUpdate.Status.State,
			text:      agentText(r.StatusUpdate.Status.Message),
		}}
	case r.ArtifactUpdate != nil:
		e := event{contextID: r.ArtifactUpdate.ContextID}
		if a := r.ArtifactUpdate.Artifact; a != nil {
			e.text, e.block = partsText(a.Parts), a.ArtifactID
		}
		return []event{e}
	}
	return nil
}

// agentText is a message's text, unless the person wrote it.
func agentText(m *wireMessage) string {
	if m == nil || m.Role == roleUser {
		return ""
	}
	return partsText(m.Parts)
}

func partsText(parts []wirePart) string {
	var b strings.Builder
	for _, p := range parts {
		b.WriteString(p.Text)
	}
	return b.String()
}

// a2aClient sends prompts to the agents' API as the signed-in person.
type a2aClient struct {
	http *http.Client
	// url is the API's address, without the agent's path.
	url string
	// token returns the person's bearer token. It is called for every
	// request, because the token expires.
	token func() (string, error)
}

// send sends a prompt to the agent and calls handle for each event of the
// answer, until the stream ends or handle returns false. An empty contextID
// starts a session. unread counts the stream objects this version could not
// read.
func (c *a2aClient) send(ctx context.Context, ns, agent, contextID, prompt string, handle func(event) bool) (unread int, err error) {
	resp, target, err := c.post(ctx, ns, agent, "text/event-stream", rpcRequest{
		JSONRPC: jsonrpcVersion,
		ID:      "1",
		Method:  methodStream,
		Params: sendParams{Message: wireMessage{
			MessageID: newID(),
			Role:      roleUser,
			Parts:     []wirePart{{Text: prompt}},
			ContextID: contextID,
		}},
	})
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, httpError(resp, target)
	}

	each := func(data []byte) (bool, error) {
		var r rpcResponse
		if json.Unmarshal(data, &r) != nil {
			unread++
			return true, nil
		}
		if r.Error != nil {
			return false, r.Error.err()
		}
		var result streamResult
		if len(r.Result) == 0 || json.Unmarshal(r.Result, &result) != nil {
			unread++
			return true, nil
		}
		es := result.events()
		if len(es) == 0 {
			unread++
		}
		for _, e := range es {
			if !handle(e) {
				return false, nil
			}
		}
		return true, nil
	}
	// The API refuses a request before it opens a stream with a plain
	// JSON-RPC response.
	if mediaType, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type")); mediaType == "application/json" {
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			return unread, err
		}
		_, err = each(data)
		return unread, err
	}
	return unread, readSSE(resp.Body, each)
}

// post sends a JSON-RPC request to the agent's endpoint as the person, and
// returns the response and the address it went to.
func (c *a2aClient) post(ctx context.Context, ns, agent, accept string, request any) (*http.Response, string, error) {
	body, err := json.Marshal(request)
	if err != nil {
		return nil, "", err
	}
	target := strings.TrimRight(c.url, "/") + agentPathPrefix + url.PathEscape(ns) + "/" + url.PathEscape(agent)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return nil, "", err
	}
	tok, err := c.token()
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set(a2aVersionHeader, a2aVersion)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", accept)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("reach the agents' API at %s: %w", c.url, err)
	}
	return resp, target, nil
}

// call sends a request that has one answer, and reads its result.
func (c *a2aClient) call(ctx context.Context, ns, agent, method string, params, result any) error {
	resp, target, err := c.post(ctx, ns, agent, "application/json", callRequest{
		JSONRPC: jsonrpcVersion,
		ID:      "1",
		Method:  method,
		Params:  params,
	})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return httpError(resp, target)
	}
	var r rpcResponse
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return fmt.Errorf("the agents' API answered %s with what airo cannot read: this airo may not match the platform, update it (%v)", method, err)
	}
	if r.Error != nil {
		return r.Error.err()
	}
	if len(r.Result) == 0 {
		return fmt.Errorf("the agents' API answered %s without a result: this airo may not match the platform, update it", method)
	}
	if err := json.Unmarshal(r.Result, result); err != nil {
		return fmt.Errorf("the agents' API answered %s with a result airo cannot read: this airo may not match the platform, update it (%v)", method, err)
	}
	return nil
}

// tasks returns the person's tasks with the agent, page by page: those of
// one session, or of every session when contextID is empty. artifacts asks
// for the agent's answers too.
func (c *a2aClient) tasks(ctx context.Context, ns, agent, contextID string, artifacts bool) ([]wireTask, error) {
	var (
		all  []wireTask
		seen = map[string]bool{}
	)
	p := listParams{ContextID: contextID, PageSize: maxPageSize, IncludeArtifacts: artifacts}
	for {
		var page listResult
		if err := c.call(ctx, ns, agent, methodListTasks, p, &page); err != nil {
			return nil, err
		}
		all = append(all, page.Tasks...)
		// A token that comes back again would never end.
		if page.NextPageToken == "" || seen[page.NextPageToken] {
			return all, nil
		}
		seen[page.NextPageToken] = true
		p.PageToken = page.NextPageToken
	}
}

// cancel asks the agent to stop a task, and returns the task as it is then.
func (c *a2aClient) cancel(ctx context.Context, ns, agent, id string) (wireTask, error) {
	var t wireTask
	err := c.call(ctx, ns, agent, methodCancelTask, taskParams{ID: id}, &t)
	return t, err
}

// wireTime reads a time of the wire, RFC 3339. It is zero for anything else.
func wireTime(v any) time.Time {
	s, _ := v.(string)
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// readSSE calls each for every event of a server-sent event stream, with the
// event's data lines joined by newlines. It stops when each returns false.
func readSSE(r io.Reader, each func(data []byte) (bool, error)) error {
	br := bufio.NewReader(r)
	var (
		data []byte
		has  bool
	)
	flush := func() (bool, error) {
		if !has {
			return true, nil
		}
		d := data
		data, has = nil, false
		return each(d)
	}
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			line = bytes.TrimRight(line, "\r\n")
			switch {
			case len(line) == 0:
				// A blank line ends an event.
				if more, err := flush(); err != nil || !more {
					return err
				}
			case bytes.HasPrefix(line, []byte("data:")):
				if has {
					data = append(data, '\n')
				}
				data = append(data, bytes.TrimPrefix(line[len("data:"):], []byte(" "))...)
				has = true
			}
			// Other fields, such as id, and comments carry nothing.
		}
		if errors.Is(err, io.EOF) {
			_, err := flush()
			return err
		}
		if err != nil {
			return err
		}
	}
}

var (
	errSignIn   = errors.New("the agents' API did not accept your sign-in: sign in again with airo login")
	errNoAccess = errors.New("you have no access to this tenant's agents: ask a platform admin")
	// ErrNotFound is the error for a session or a turn the agent does not
	// have for the person.
	ErrNotFound = errors.New("the agent has no such session")
)

// httpError explains a response that is not a stream.
func httpError(resp *http.Response, target string) error {
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return errSignIn
	case http.StatusForbidden:
		return errNoAccess
	case http.StatusNotFound:
		return fmt.Errorf("the agents' API has nothing at %s: the agent may be gone, or this airo does not match the platform, update it", target)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
	if text := strings.TrimSpace(string(body)); text != "" {
		return fmt.Errorf("the agents' API answered %s: %s", resp.Status, text)
	}
	return fmt.Errorf("the agents' API answered %s", resp.Status)
}

// err explains a JSON-RPC error.
func (e *rpcError) err() error {
	switch e.Code {
	case codeUnauthenticated:
		return errSignIn
	case codeUnauthorized:
		return errNoAccess
	case codeTaskNotFound:
		return ErrNotFound
	case codeVersionNotSupported:
		return fmt.Errorf("the agents' API does not speak A2A %s, which this airo does: update airo (%s)", a2aVersion, e.Message)
	}
	return fmt.Errorf("the agents' API refused the request: %s (code %d)", e.Message, e.Code)
}

// newID returns a random UUID, for a message's ID.
func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}
