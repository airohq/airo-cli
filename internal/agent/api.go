package agent

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"k8s.io/cli-runtime/pkg/genericclioptions"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/airohq/airo-cli/internal/bearer"
)

// API is the agents' API of one project, as the signed-in person. It is what
// the other commands, such as airo session, get of this package: the
// project's agents and the person's turns with them, without the wire.
type API struct {
	k8s client.Client
	ns  string
	a2a *a2aClient
}

// Open returns the agents' API of the project the flags name. It fails for a
// project without an agent endpoint.
func Open(ctx context.Context, flags *genericclioptions.ConfigFlags) (*API, error) {
	cfg, err := flags.ToRESTConfig()
	if err != nil {
		return nil, err
	}
	ns, _, err := flags.ToRawKubeConfigLoader().Namespace()
	if err != nil {
		return nil, err
	}
	c, err := client.New(cfg, client.Options{})
	if err != nil {
		return nil, err
	}
	address, err := endpoint(ctx, c, ns)
	if err != nil {
		return nil, err
	}
	return NewAPI(c, ns, address, http.DefaultClient, func() (string, error) { return bearer.Token(cfg) }), nil
}

// NewAPI returns the agents' API at address for the project ns. token
// returns the person's bearer token, and is called for every request.
func NewAPI(c client.Client, ns, address string, hc *http.Client, token func() (string, error)) *API {
	return &API{k8s: c, ns: ns, a2a: &a2aClient{http: hc, url: address, token: token}}
}

// Project is the project's namespace.
func (a *API) Project() string { return a.ns }

// Agents returns the names of the project's Agents, sorted, or only name
// when it is not empty. It fails for a name the project does not have.
func (a *API) Agents(ctx context.Context, name string) ([]string, error) {
	if name != "" {
		item, err := choose(ctx, a.k8s, a.ns, agents, name, nil)
		if err != nil {
			return nil, err
		}
		return []string{item.GetName()}, nil
	}
	items, err := list(ctx, a.k8s, a.ns, agents)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(items))
	for i := range items {
		names = append(names, items[i].GetName())
	}
	sort.Strings(names)
	return names, nil
}

// The states of a turn, in plain words.
const (
	StateQueued    = "queued"
	StateWorking   = "working"
	StateDone      = "done"
	StateFailed    = "failed"
	StateCanceled  = "canceled"
	StateRejected  = "rejected"
	StateNeedsYou  = "needs input"
	StateNeedsAuth = "needs authorization"
)

// Who a message is from.
const (
	FromYou   = "you"
	FromAgent = "agent"
)

// Turn is one prompt of a session and the agent's answer to it. On the wire
// it is a task, and the session is the task's context.
type Turn struct {
	ID      string `json:"id"`
	Session string `json:"session"`
	State   string `json:"state"`
	// Started is when the prompt arrived, and Updated when the turn last
	// changed. Either is zero when the API does not say.
	Started  time.Time `json:"started,omitzero"`
	Updated  time.Time `json:"updated,omitzero"`
	Messages []Message `json:"messages"`
}

// Message is a text of a turn: a prompt, or a part of the agent's answer.
type Message struct {
	From string    `json:"from"`
	Time time.Time `json:"time,omitzero"`
	Text string    `json:"text"`
}

// Final reports whether the turn takes no more work. A turn that waits for
// the person is not final.
func (t Turn) Final() bool {
	switch t.State {
	case StateDone, StateFailed, StateCanceled, StateRejected:
		return true
	}
	return false
}

// Turns returns the person's turns with the agent: those of one session, or
// of every session when session is empty. answers asks for the agent's
// answers too. Without them a turn holds the person's prompts. A session the
// agent does not have for the person is ErrNotFound.
func (a *API) Turns(ctx context.Context, agent, session string, answers bool) ([]Turn, error) {
	tasks, err := a.a2a.tasks(ctx, a.ns, agent, session, answers)
	if session != "" && errors.Is(err, errNoAccess) {
		// kagent answers a session of another agent as it answers one of
		// another person: not yours here.
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	turns := make([]Turn, 0, len(tasks))
	for _, t := range tasks {
		turns = append(turns, t.turn())
	}
	return turns, nil
}

// Cancel asks the agent to stop a turn, and returns the turn as it is then.
// The session stays.
func (a *API) Cancel(ctx context.Context, agent, turn string) (Turn, error) {
	t, err := a.a2a.cancel(ctx, a.ns, agent, turn)
	if err != nil {
		return Turn{}, err
	}
	return t.turn(), nil
}

// turn is the task as the commands read it.
func (t wireTask) turn() Turn {
	turn := Turn{
		ID:      t.ID,
		Session: t.ContextID,
		State:   stateWord(t.Status.State),
		Started: wireTime(t.Metadata[metaTaskCreated]),
		Updated: wireTime(t.Status.Timestamp),
	}
	add := func(from string, at time.Time, parts []wirePart) {
		if text := partsText(parts); strings.TrimSpace(text) != "" {
			turn.Messages = append(turn.Messages, Message{From: from, Time: at, Text: text})
		}
	}
	for _, m := range t.History {
		from := FromAgent
		if m.Role == roleUser {
			from = FromYou
		}
		add(from, wireTime(m.Metadata[metaPosition]), m.Parts)
	}
	for _, a := range t.Artifacts {
		add(FromAgent, wireTime(a.Metadata[metaPosition]), a.Parts)
	}
	// The state's message says why a turn failed, or what the agent waits for.
	if m := t.Status.Message; m != nil && m.Role != roleUser {
		add(FromAgent, turn.Updated, m.Parts)
	}

	// Oldest first. A message without a time stays behind the one before it.
	at := turn.Started
	keys := make([]time.Time, len(turn.Messages))
	for i, m := range turn.Messages {
		if !m.Time.IsZero() {
			at = m.Time
		}
		keys[i] = at
	}
	order := make([]int, len(keys))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(i, j int) bool { return keys[order[i]].Before(keys[order[j]]) })
	sorted := make([]Message, len(order))
	for i, j := range order {
		sorted[i] = turn.Messages[j]
	}
	turn.Messages = sorted

	for _, m := range turn.Messages {
		if turn.Started.IsZero() {
			turn.Started = m.Time
		}
		if m.Time.After(turn.Updated) {
			turn.Updated = m.Time
		}
	}
	if turn.Started.IsZero() {
		turn.Started = turn.Updated
	}
	return turn
}

// stateWord is a task's state in plain words.
func stateWord(state string) string {
	switch state {
	case stateSubmitted:
		return StateQueued
	case stateWorking:
		return StateWorking
	case stateCompleted:
		return StateDone
	case stateFailed:
		return StateFailed
	case stateCanceled:
		return StateCanceled
	case stateRejected:
		return StateRejected
	case stateInputRequired:
		return StateNeedsYou
	case stateAuthRequired:
		return StateNeedsAuth
	case "":
		return "unknown"
	}
	// A state this version does not know.
	return strings.ToLower(strings.ReplaceAll(strings.TrimPrefix(state, "TASK_STATE_"), "_", " "))
}
