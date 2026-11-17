// Package session implements airo session: list and view the person's
// sessions with the project's agents, and cancel a running turn. A session
// is not a Kubernetes resource. It lives in the agents' API, which
// internal/agent talks to.
package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/util/duration"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/cli-runtime/pkg/printers"
	"sigs.k8s.io/yaml"

	"github.com/airohq/airo-cli/internal/agent"
)

const (
	// How many agents are asked at once. The agents' API has no call for
	// the sessions of a project, so each agent is asked for its own.
	parallel = 6
	// How much of a session's first prompt the list shows.
	promptWidth = 60
	// How a time is shown in a conversation, in the local time zone.
	timeLayout = "2006-01-02 15:04"
)

// Session is a conversation of the person with one agent: the turns that
// share an ID.
type Session struct {
	ID    string `json:"id"`
	Agent string `json:"agent"`
	// State is the state of the newest turn.
	State      string    `json:"state"`
	Started    time.Time `json:"started,omitzero"`
	LastActive time.Time `json:"lastActive,omitzero"`
	// FirstPrompt is the prompt that started the session.
	FirstPrompt string `json:"firstPrompt"`
	// Turns are the session's turns, oldest first. The list leaves them out.
	Turns []agent.Turn `json:"turns,omitempty"`
}

// options are the flags of the verbs.
type options struct {
	agent  string
	output string
}

// NewCommand returns the session command.
func NewCommand(flags *genericclioptions.ConfigFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "session",
		Short: "List and view your sessions with agents, or cancel a running turn",
		Long: `Session shows your sessions with the agents of the project. A session is a
conversation with one agent: airo agent starts one and prints its ID.

Sessions are private. You see only the ones you started. A session is not a
resource of the cluster, so it has no get or delete.

Without --agent, each command asks every agent of the project.`,
		Example: `  airo session list
  airo session list -a reviewer
  airo session view SESSION
  airo session cancel SESSION
  airo agent -a reviewer -r SESSION "And the tests?"   # go on in a session`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	verb := func(use, short string, args cobra.PositionalArgs, output bool, run func(context.Context, *cobra.Command, *agent.API, options, []string) error) {
		var o options
		c := &cobra.Command{
			Use:   use,
			Short: short,
			Args:  args,
			RunE: func(cmd *cobra.Command, args []string) error {
				if o.output != "" && o.output != "json" && o.output != "yaml" {
					return fmt.Errorf("-o %s is not supported: use yaml or json", o.output)
				}
				api, err := agent.Open(cmd.Context(), flags)
				if err != nil {
					return err
				}
				return run(cmd.Context(), cmd, api, o, args)
			},
		}
		c.Flags().StringVarP(&o.agent, "agent", "a", "", "name of the Agent (default: every agent of the project)")
		if output {
			c.Flags().StringVarP(&o.output, "output", "o", "", "print everything as yaml or json")
		}
		cmd.AddCommand(c)
	}
	verb("list", "List your sessions in the project, newest first", cobra.NoArgs, true,
		func(ctx context.Context, cmd *cobra.Command, api *agent.API, o options, _ []string) error {
			return list(ctx, cmd.OutOrStdout(), cmd.ErrOrStderr(), api, o, time.Now())
		})
	verb("view SESSION", "Show a session's conversation", cobra.ExactArgs(1), true,
		func(ctx context.Context, cmd *cobra.Command, api *agent.API, o options, args []string) error {
			return view(ctx, cmd.OutOrStdout(), cmd.ErrOrStderr(), api, o, args[0], time.Now())
		})
	verb("cancel SESSION", "Stop the turn that runs in a session, and keep the session", cobra.ExactArgs(1), false,
		func(ctx context.Context, cmd *cobra.Command, api *agent.API, o options, args []string) error {
			return cancel(ctx, cmd.OutOrStdout(), cmd.ErrOrStderr(), api, o, args[0])
		})
	return cmd
}

// list prints the person's sessions with the project's agents, newest first.
func list(ctx context.Context, out, errOut io.Writer, api *agent.API, o options, now time.Time) error {
	names, err := api.Agents(ctx, o.agent)
	if err != nil {
		return err
	}
	turns := make([][]agent.Turn, len(names))
	errs := each(ctx, names, func(i int, name string) (err error) {
		// The prompts are enough for a list. The answers are the large part.
		turns[i], err = api.Turns(ctx, name, "", false)
		return err
	})
	var sessions []Session
	failed := 0
	for i, name := range names {
		if errs[i] != nil {
			failed++
			continue
		}
		sessions = append(sessions, group(name, turns[i])...)
	}
	// An error of every agent is one problem, such as a sign-in that ended.
	if failed > 0 && failed == len(names) {
		return fmt.Errorf("list the sessions with agent %s: %w", names[0], errs[0])
	}
	// One agent that does not answer must not hide the sessions of the rest.
	for i, name := range names {
		if errs[i] != nil {
			fmt.Fprintf(errOut, "Could not list the sessions with agent %s: %v\n", name, errs[i])
		}
	}
	sort.SliceStable(sessions, func(i, j int) bool { return sessions[i].LastActive.After(sessions[j].LastActive) })

	if o.output != "" {
		for i := range sessions {
			sessions[i].Turns = nil
		}
		if sessions == nil {
			sessions = []Session{}
		}
		return print(out, o.output, sessions)
	}
	if len(sessions) == 0 {
		if o.agent != "" {
			fmt.Fprintf(out, "No sessions with agent %s in %s\n", names[0], api.Project())
		} else {
			fmt.Fprintf(out, "No sessions in %s\n", api.Project())
		}
		return nil
	}
	w := printers.GetNewTabWriter(out)
	fmt.Fprintln(w, "SESSION\tAGENT\tLAST ACTIVE\tSTATE\tFIRST PROMPT")
	for _, s := range sessions {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", s.ID, s.Agent, age(now, s.LastActive), s.State, oneLine(s.FirstPrompt, promptWidth))
	}
	return w.Flush()
}

// view prints a session's conversation, oldest first, and then its state.
func view(ctx context.Context, out, errOut io.Writer, api *agent.API, o options, id string, now time.Time) error {
	s, err := find(ctx, api, o.agent, id, true)
	if err != nil {
		return err
	}
	if o.output != "" {
		return print(out, o.output, s)
	}
	fmt.Fprintf(out, "Session %s with agent %s\n", s.ID, s.Agent)
	for i, t := range s.Turns {
		for _, m := range t.Messages {
			who := "Agent"
			if m.From == agent.FromYou {
				who = "You"
			}
			if m.Time.IsZero() {
				fmt.Fprintf(out, "\n%s\n", who)
			} else {
				fmt.Fprintf(out, "\n%s, %s\n", who, m.Time.Local().Format(timeLayout))
			}
			for _, line := range strings.Split(strings.TrimRight(m.Text, "\r\n"), "\n") {
				fmt.Fprintln(out, strings.TrimRight("  "+line, " \r"))
			}
		}
		// The last turn's state is the session's, printed below.
		if i < len(s.Turns)-1 && t.State != agent.StateDone {
			fmt.Fprintf(out, "\n(this turn: %s)\n", t.State)
		}
	}
	fmt.Fprintf(out, "\nState: %s, last active %s ago\n", s.State, age(now, s.LastActive))
	fmt.Fprintf(errOut, "  resume: airo agent -a %s -r %s\n", s.Agent, s.ID)
	return nil
}

// cancel stops the turns of a session that are not over. The session stays.
func cancel(ctx context.Context, out, errOut io.Writer, api *agent.API, o options, id string) error {
	s, err := find(ctx, api, o.agent, id, false)
	if err != nil {
		return err
	}
	canceled := 0
	for _, t := range s.Turns {
		if t.Final() {
			continue
		}
		after, err := api.Cancel(ctx, s.Agent, t.ID)
		if err != nil {
			return fmt.Errorf("cancel the %s turn of session %s with agent %s: %w", t.State, s.ID, s.Agent, err)
		}
		canceled++
		if after.State == agent.StateCanceled {
			fmt.Fprintf(out, "Canceled the %s turn of session %s with agent %s\n", t.State, s.ID, s.Agent)
		} else {
			// The agent may have finished first, or stops a moment later.
			fmt.Fprintf(out, "Asked agent %s to cancel the %s turn of session %s\n", s.Agent, t.State, s.ID)
		}
		if prompt := firstPrompt(t); prompt != "" {
			fmt.Fprintf(out, "  prompt: %s\n", oneLine(prompt, promptWidth))
		}
		fmt.Fprintf(out, "  state:  %s\n", after.State)
	}
	if canceled == 0 {
		fmt.Fprintf(out, "Session %s with agent %s has no running turn: its last turn is %s\n", s.ID, s.Agent, s.State)
		return nil
	}
	fmt.Fprintf(errOut, "The session stays.\n  resume: airo agent -a %s -r %s\n", s.Agent, s.ID)
	return nil
}

// find returns the session with the ID, with its turns. It asks the agent
// named, or every agent of the project: only the agent knows its sessions.
func find(ctx context.Context, api *agent.API, name, id string, answers bool) (Session, error) {
	names, err := api.Agents(ctx, name)
	if err != nil {
		return Session{}, err
	}
	turns := make([][]agent.Turn, len(names))
	errs := each(ctx, names, func(i int, n string) (err error) {
		turns[i], err = api.Turns(ctx, n, id, answers)
		return err
	})
	var (
		failed   error
		failures int
	)
	for i, n := range names {
		switch {
		case errors.Is(errs[i], agent.ErrNotFound):
		case errs[i] != nil:
			failures++
			if failed == nil {
				failed = errs[i]
				if len(names) > 1 {
					failed = fmt.Errorf("agent %s: %w", n, errs[i])
				}
			}
		default:
			if sessions := group(n, turns[i]); len(sessions) > 0 {
				return sessions[0], nil
			}
		}
	}
	where := "project " + api.Project()
	if name != "" {
		where = "agent " + names[0] + " in " + where
	}
	// An error of every agent is one problem, such as an ID the API cannot
	// read.
	if failures == len(names) && failed != nil {
		return Session{}, fmt.Errorf("look for session %s with %s: %w", id, where, errs[0])
	}
	if failed != nil {
		return Session{}, fmt.Errorf("found no session %s of yours with %s, and %d of %d agents did not answer: %w", id, where, failures, len(names), failed)
	}
	return Session{}, fmt.Errorf("no session %s of yours with %s: see yours with airo session list", id, where)
}

// group makes the sessions of one agent from its turns: a session is the
// turns with one session ID.
func group(name string, turns []agent.Turn) []Session {
	sort.SliceStable(turns, func(i, j int) bool { return turns[i].Started.Before(turns[j].Started) })
	var (
		sessions []Session
		index    = map[string]int{}
	)
	for _, t := range turns {
		i, ok := index[t.Session]
		if !ok {
			i = len(sessions)
			index[t.Session] = i
			sessions = append(sessions, Session{ID: t.Session, Agent: name, Started: t.Started})
		}
		s := &sessions[i]
		s.Turns = append(s.Turns, t)
		// The turns are oldest first, so the last one seen is the newest.
		s.State = t.State
		if t.Updated.After(s.LastActive) {
			s.LastActive = t.Updated
		}
		if s.FirstPrompt == "" {
			s.FirstPrompt = firstPrompt(t)
		}
	}
	return sessions
}

// firstPrompt is the first text of the person in a turn.
func firstPrompt(t agent.Turn) string {
	for _, m := range t.Messages {
		if m.From == agent.FromYou {
			return m.Text
		}
	}
	return ""
}

// each calls fn for every name, a few at a time, and returns the errors in
// the order of the names.
func each(ctx context.Context, names []string, fn func(i int, name string) error) []error {
	errs := make([]error, len(names))
	slots := make(chan struct{}, parallel)
	var wg sync.WaitGroup
	for i, name := range names {
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case slots <- struct{}{}:
				defer func() { <-slots }()
				errs[i] = fn(i, name)
			case <-ctx.Done():
				errs[i] = ctx.Err()
			}
		}()
	}
	wg.Wait()
	return errs
}

// print writes v as json or yaml.
func print(out io.Writer, format string, v any) error {
	if format == "yaml" {
		data, err := yaml.Marshal(v)
		if err != nil {
			return err
		}
		_, err = out.Write(data)
		return err
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "    ")
	return enc.Encode(v)
}

// age is how long ago t was, as the lists of resources print it.
func age(now, t time.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	return duration.HumanDuration(now.Sub(t))
}

// oneLine is text on one line, cut to width characters.
func oneLine(text string, width int) string {
	runes := []rune(strings.Join(strings.Fields(text), " "))
	if len(runes) > width {
		return string(runes[:width-1]) + "…"
	}
	return string(runes)
}
