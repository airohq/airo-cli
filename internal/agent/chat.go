package agent

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/airohq/airo-cli/internal/bearer"
	"github.com/airohq/airo-cli/internal/picker"
	"github.com/airohq/airo-cli/internal/ui"
)

// An Agent with this annotation set to "true" is the one airo chat talks to
// when the project has several and --agent names none. The name is the
// platform's, not a component's.
const defaultAnnotation = "airohq.dev/default-agent"

// The address of the agents' API is the key url of this ConfigMap in the
// project's namespace. The platform puts it there.
const (
	endpointConfigMap = "kagent"
	endpointKey       = "url"
)

// chatOptions are the flags of airo chat.
type chatOptions struct {
	agent  string
	cont   bool
	resume string
}

// NewChatCommand starts or resumes a conversation with a project agent.
func NewChatCommand(flags *genericclioptions.ConfigFlags) *cobra.Command {
	var o chatOptions
	cmd := &cobra.Command{
		Use:   "chat [PROMPT]",
		Short: "Chat with an agent in the current project",
		Long: `Chat sends a prompt to an agent of the project and prints the answer as it
arrives. Without a prompt, on a terminal, it is a conversation: Enter sends
a prompt of the same session, Ctrl-J adds a new line, and Ctrl-D leaves on
an empty prompt. You can draft the next prompt while the agent works.

The prompt starts a new session. The session stays in the cluster, and
--continue or --resume sends the next prompt to it. In the terminal chat,
Esc stops the turn, and Ctrl-C twice detaches: the agent works on in the
cluster. Plain output uses one Ctrl-C to detach.

Without --agent, airo takes the project's only agent, then the one with the
annotation ` + defaultAnnotation + `: "true", then asks.`,
		Example: `  airo chat "Why does the build fail?"      # a new session
  airo chat -c "Fix it."                    # the same session
  airo chat -a reviewer                     # a conversation with one agent
  airo chat -r SESSION "And the tests?"     # a session by its ID`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Ctrl-C detaches from the session. It is not an error.
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return runChat(ctx, cmd, flags, o, strings.Join(args, " "))
		},
	}
	cmd.Flags().StringVarP(&o.agent, "agent", "a", "", "name of the Agent to talk to (default: the project's only one, or its default)")
	cmd.Flags().BoolVarP(&o.cont, "continue", "c", false, "continue your last session with the agent from this machine")
	cmd.Flags().StringVarP(&o.resume, "resume", "r", "", "continue the session with this ID")
	cmd.MarkFlagsMutuallyExclusive("continue", "resume")
	ui.Require(needsAgents, cmd)
	return cmd
}

// runChat is airo chat [PROMPT]: one prompt, or a conversation on a
// terminal.
func runChat(ctx context.Context, cmd *cobra.Command, flags *genericclioptions.ConfigFlags, o chatOptions, prompt string) (err error) {
	cfg, err := flags.ToRESTConfig()
	if err != nil {
		return err
	}
	ns, _, err := flags.ToRawKubeConfigLoader().Namespace()
	if err != nil {
		return err
	}
	c, err := client.NewWithWatch(cfg, client.Options{})
	if err != nil {
		return err
	}
	errOut := cmd.ErrOrStderr()
	in := &lines{r: bufio.NewReader(cmd.InOrStdin())}
	terminal := ui.Interactive(cmd.InOrStdin(), cmd.OutOrStdout())

	var ask func([]string) (string, error)
	if terminal && ui.IsTerminal(errOut) {
		ask = func(names []string) (string, error) {
			input := cmd.InOrStdin()
			if !ui.Styled(errOut) {
				input = in.r
			}
			name, err := picker.One(ctx, input, errOut, "agent", names, -1)
			if err == nil && name == "" {
				err = errors.New("no agent chosen: name one with --agent")
			}
			return name, err
		}
	}
	config, err := chatProject(ctx, c, ns, errOut)
	if err != nil {
		return err
	}
	agent, err := pick(ctx, c, ns, o.agent, ask)
	if err != nil {
		return err
	}
	address, err := endpointURL(config)
	if err != nil {
		return err
	}
	selected, err := awaitAgent(ctx, c, ns, agent, cmd.InOrStdin(), errOut)
	if err != nil {
		if ctx.Err() != nil {
			fmt.Fprintln(errOut, "\nStopped waiting.")
			return nil
		}
		return err
	}
	ctx, cancel := context.WithCancelCause(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := watchChatAgent(ctx, c, selected); err != nil {
			cancel(err)
		}
	}()
	defer func() {
		cancel(nil)
		<-done
		if cause := context.Cause(ctx); isAgentChange(cause) {
			err = cause
		}
	}()
	if marker := config.Data["readinessConfigMap"]; marker != "" {
		if err := waitProjectCredentials(ctx, c, ns, marker, errOut); err != nil {
			return err
		}
	}
	store, err := newSessionStore()
	if err != nil {
		return err
	}
	key := sessionKey(cfg.Host, ns, agent)
	id := o.resume
	if o.cont {
		if id = store.get(key); id == "" {
			return fmt.Errorf("this machine remembers no session with agent %s/%s: start one with airo chat, or name one with --resume", ns, agent)
		}
	}

	s := &session{
		api: &a2aClient{
			http:  http.DefaultClient,
			url:   address,
			token: func() (string, error) { return bearer.Token(cfg) },
		},
		ns:     ns,
		agent:  agent,
		id:     id,
		out:    cmd.OutOrStdout(),
		errOut: errOut,
	}
	s.beforeTurn = func(ctx context.Context) error {
		_, err := refreshChatAgent(ctx, c, selected)
		if isAgentChange(err) {
			cancel(err)
		}
		return err
	}
	s.remember = func(id string) {
		if err := store.put(key, id); err != nil {
			fmt.Fprintf(s.errOut, "Could not remember the session for --continue: %v\n", err)
		}
	}
	switch {
	case ui.Chat(cmd.InOrStdin(), s.out, s.errOut):
		s.details = func(ctx context.Context) chatDetails { return readChatDetails(ctx, c, ns, agent) }
		return s.chat(ctx, cmd.InOrStdin(), prompt)
	case prompt != "":
		return s.once(ctx, prompt)
	case terminal:
		return s.converse(ctx, in)
	case ui.IsTerminal(cmd.InOrStdin()):
		return errors.New("no prompt: pass one as the argument or on standard input")
	}
	// Input that is no terminal, such as a pipe, is one prompt.
	data, err := readPipedPrompt(ctx, in.r)
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(data)) == "" {
		return errors.New("no prompt: pass one as the argument or on standard input")
	}
	return s.once(ctx, string(data))
}

// A pipe can stay open after the Agent disappears. Let the command exit
// while its input is still waiting for EOF, as it does at a terminal prompt.
func readPipedPrompt(ctx context.Context, in io.Reader) ([]byte, error) {
	type result struct {
		data []byte
		err  error
	}
	done := make(chan result, 1)
	go func() {
		data, err := io.ReadAll(in)
		done <- result{data, err}
	}()
	select {
	case <-ctx.Done():
		return nil, context.Cause(ctx)
	case r := <-done:
		return r.data, r.err
	}
}

// pick returns the name of the Agent to talk to: the one named, the
// project's only one, the one marked as default, or the one ask returns. ask
// is nil when there is nobody to ask.
func pick(ctx context.Context, c client.Client, ns, name string, ask func([]string) (string, error)) (string, error) {
	if name != "" {
		a, err := choose(ctx, c, ns, agents, name, nil)
		if err != nil {
			return "", err
		}
		return a.GetName(), nil
	}
	items, err := list(ctx, c, ns, agents)
	if err != nil {
		return "", err
	}
	var names []string
	for i := range items {
		names = append(names, items[i].GetName())
	}
	sort.Strings(names)
	switch {
	case len(names) == 0:
		return "", fmt.Errorf("project %s has no agent: make one with airo agent create", ns)
	case len(names) == 1:
		return names[0], nil
	}
	if d := newestDefault(items, defaultAnnotation); d != nil {
		return d.GetName(), nil
	}
	if ask != nil {
		return ask(names)
	}
	return "", fmt.Errorf("project %s has %d agents and no default, name one with --agent: %s", ns, len(names), strings.Join(names, ", "))
}

// endpoint returns the address of the agents' API for the project.
func endpoint(ctx context.Context, c client.Client, ns string) (string, error) {
	cm := &corev1.ConfigMap{}
	err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: endpointConfigMap}, cm)
	if apierrors.IsNotFound(err) {
		return "", noEndpoint(ns)
	}
	if err != nil {
		return "", needsAgents.Explain(ns, "agents", fmt.Errorf("read the agent endpoint of project %s: %w", ns, err))
	}
	return endpointURL(cm)
}

// noEndpoint explains a project without the endpoint that the agents
// capability gives each project.
func noEndpoint(ns string) error {
	return fmt.Errorf("project %s has no agent endpoint (ConfigMap %s), which the agents capability provides: ask a tenant administrator to enable it", ns, endpointConfigMap)
}

func endpointURL(cm *corev1.ConfigMap) (string, error) {
	ns := cm.Namespace
	address := strings.TrimSpace(cm.Data[endpointKey])
	if address == "" {
		return "", fmt.Errorf("this project has no agent endpoint; ask a platform admin (ConfigMap %s/%s has no key %s)", ns, endpointConfigMap, endpointKey)
	}
	u, err := url.Parse(address)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("the agent endpoint of project %s is %q, which is no URL; ask a platform admin", ns, address)
	}
	// The request carries your Kubernetes token, which anyone who sees
	// unencrypted traffic could reuse.
	if u.Scheme != "https" {
		return "", fmt.Errorf("the agent endpoint of project %s is %s, which would send your Kubernetes token unencrypted", ns, address)
	}
	return address, nil
}

// session is a conversation with one agent. Its ID is empty until the first
// answer starts to arrive.
type session struct {
	api       *a2aClient
	ns, agent string
	id        string
	// The agent's text goes to out. Everything about the session goes to
	// errOut, so out can be piped.
	out, errOut io.Writer
	// Check the selected Agent before sending, including across watch reconnects.
	beforeTurn func(context.Context) error
	// remember is called once with the ID of the session, when the agents'
	// API has answered for it: it is then the last session with the agent.
	remember   func(id string)
	remembered bool
	// The terminal supplies another printer and observes task IDs for Esc.
	printer printer
	observe func(event)
	styles  *ui.Styles // the terminal chat's resolved theme, for its closing hint
	details func(context.Context) chatDetails
}

// errDetached is the turn's error when the person stopped it.
var errDetached = errors.New("detached")

// taskError is a turn the agent did not complete.
type taskError struct {
	state, reason string
}

func (e *taskError) Error() string {
	what := "the agent failed"
	switch e.state {
	case stateRejected:
		what = "the agent turned the prompt down"
	case stateCanceled:
		what = "the turn was canceled"
	}
	if reason := strings.TrimSpace(e.reason); reason != "" {
		return what + ": " + reason
	}
	return what
}

// turn sends one prompt and prints the answer as it arrives.
func (s *session) turn(ctx context.Context, prompt string) error {
	if s.beforeTurn != nil {
		if err := s.beforeTurn(ctx); err != nil {
			if ctx.Err() != nil && !isAgentChange(err) {
				return errDetached
			}
			return err
		}
	}
	var (
		p       = s.printer
		state   string
		reason  string
		replied bool
		printed bool
		// The text of each block printed so far.
		blocks = map[string]string{}
	)
	if p == nil {
		p = &plainPrinter{out: s.out, errOut: s.errOut}
	}
	unread, err := s.api.send(ctx, s.ns, s.agent, s.id, prompt, func(e event) bool {
		if s.observe != nil {
			s.observe(e)
		}
		if e.contextID != "" && !s.remembered {
			if s.id == "" {
				s.id = e.contextID
			}
			s.remember(s.id)
			s.remembered = true
		}
		replied = replied || e.reply
		if e.state != "" {
			state = e.state
		}
		switch {
		case e.tool != nil:
			p.tool(e.tool)
		case e.text == "":
		case e.state == stateFailed || e.state == stateRejected || e.state == stateCanceled:
			// The text says why. It goes into the error.
			reason = e.text
		case e.snapshot && printed:
			// The stream carried this text already.
		default:
			text := e.text
			if e.block != "" {
				text = newText(blocks[e.block], e.text, e.whole)
				blocks[e.block] += text
			}
			if text != "" {
				p.write(e.block, text)
				printed = true
			}
		}
		return !ended(state)
	})
	p.end()
	switch {
	case ctx.Err() != nil:
		return errDetached
	case err != nil:
		return err
	}
	switch state {
	case stateCompleted:
		return nil
	case stateFailed, stateRejected, stateCanceled:
		return &taskError{state: state, reason: reason}
	case stateInputRequired:
		fmt.Fprintln(s.errOut, "The agent waits for your answer.")
		return nil
	case stateAuthRequired:
		fmt.Fprintln(s.errOut, "The agent waits for an authorization it does not have.")
		return nil
	}
	switch {
	case state == "" && replied:
		// The agent answered with a message and made no task.
		return nil
	case unread > 0:
		return fmt.Errorf("the answer ended without a result, and airo could not read %d of its parts: this airo may not match the platform, update it", unread)
	case state == "":
		return errors.New("the agents' API sent no answer")
	}
	return errors.New("the connection ended before the agent finished: the turn goes on in the cluster")
}

// newText is what a block's update adds to the text printed for it. An update
// that is the whole text so far, such as the ADK's last chunk, adds only what
// is beyond the printed text. One that does not start with the printed text
// is added as it is: what is printed stays, and some servers leave out append
// on every piece.
func newText(printed, text string, whole bool) string {
	if whole && printed != "" && strings.HasPrefix(text, printed) {
		return text[len(printed):]
	}
	return text
}

// ended reports whether a task in the state takes no more work in this turn.
func ended(state string) bool {
	switch state {
	case stateCompleted, stateFailed, stateCanceled, stateRejected, stateInputRequired, stateAuthRequired:
		return true
	}
	return false
}

// once sends one prompt and says how to go on.
func (s *session) once(ctx context.Context, prompt string) error {
	err := s.turn(ctx, prompt)
	if cause := context.Cause(ctx); isAgentChange(cause) {
		return cause
	}
	if errors.Is(err, errDetached) {
		s.detached()
		return nil
	}
	s.left()
	return err
}

// converse sends each line the person types as a prompt of one session,
// until the input ends.
func (s *session) converse(ctx context.Context, in *lines) error {
	if s.id == "" {
		fmt.Fprintf(s.errOut, "A new session with agent %s. Ctrl-D ends the conversation, and the session stays.\n", s.agent)
	} else {
		fmt.Fprintf(s.errOut, "Session %s with agent %s. Ctrl-D ends the conversation, and the session stays.\n", s.id, s.agent)
		turns, err := s.history(ctx)
		if err != nil {
			return err
		}
		if err := writeHistory(turns, s.out, s.errOut, nil); err != nil {
			return err
		}
	}
	for {
		fmt.Fprint(s.errOut, "> ")
		line, err := in.read(ctx)
		if err != nil {
			// Ctrl-D, or Ctrl-C while no turn runs.
			fmt.Fprintln(s.errOut)
			if cause := context.Cause(ctx); isAgentChange(cause) {
				return cause
			}
			s.left()
			if errors.Is(err, io.EOF) || ctx.Err() != nil {
				return nil
			}
			return err
		}
		if strings.TrimSpace(line) == "" {
			continue
		}
		err = s.turn(ctx, line)
		if cause := context.Cause(ctx); isAgentChange(cause) {
			return cause
		}
		var failed *taskError
		switch {
		case errors.Is(err, errDetached):
			s.detached()
			return nil
		case errors.As(err, &failed):
			// The session takes the next prompt.
			fmt.Fprintf(s.errOut, "Error: %v\n", err)
		case err != nil:
			s.left()
			return err
		}
	}
}

// detached says how to come back after ctrl-C. Nothing is deleted: the turn
// goes on in the cluster.
func (s *session) detached() {
	if s.id == "" {
		fmt.Fprintln(s.errOut, "\nDetached before the session's ID arrived.")
		return
	}
	fmt.Fprintln(s.errOut, "\nDetached. The agent works on in the cluster.")
	s.resumeHint()
}

// left says how to come back to the session of a conversation.
func (s *session) left() {
	if s.id != "" {
		fmt.Fprintln(s.errOut)
		s.resumeHint()
	}
}

// resumeHint names this session explicitly, even if another run has since
// changed which session --continue would choose.
func (s *session) resumeHint() {
	styles := ui.NewStyles(s.errOut)
	if s.styles != nil {
		styles = *s.styles
	}
	styles.ResumeHint(s.errOut, fmt.Sprintf("airo chat -a %s -r %s", s.agent, s.id))
}

// printer is the output of a turn. The wire and snapshot handling are the
// same for a terminal conversation and for a plain answer in a pipe.
type printer interface {
	write(block, text string)
	// tool shows the agent's use of a tool. It ends the text before it.
	tool(t *toolUse)
	end()
}

// plainPrinter writes the agent's own text with a new line between blocks,
// and its tool calls to errOut: they are notes about the answer.
type plainPrinter struct {
	out, errOut io.Writer
	block       string
	open        bool // the last write did not end a line
}

func (p *plainPrinter) write(block, text string) {
	if p.open && (block == "" || block != p.block) {
		fmt.Fprintln(p.out)
	}
	fmt.Fprint(p.out, text)
	p.block, p.open = block, !strings.HasSuffix(text, "\n")
}

func (p *plainPrinter) tool(t *toolUse) {
	line := toolLine(t)
	if line == "" || p.errOut == nil {
		return
	}
	p.end()
	p.block = ""
	fmt.Fprintln(p.errOut, line)
}

// toolLine is a tool call, or a failed result, in a line of plain words. It
// is empty for a result that worked: the call said it already.
func toolLine(t *toolUse) string {
	if t.result {
		if !t.failed {
			return ""
		}
		what := "A tool"
		if t.name != "" {
			what = t.name
		}
		if out := firstLine(fmt.Sprint(t.output)); t.output != nil && out != "" {
			return what + " failed: " + out
		}
		return what + " failed"
	}
	// A shell command is what it runs, as in Codex.
	if c, ok := t.args["command"].(string); ok && firstLine(c) != "" {
		return "Ran " + firstLine(c)
	}
	name := t.name
	if name == "" {
		name = "a tool"
	}
	for _, k := range []string{"file_path", "path", "pattern", "query", "url", "description"} {
		if v, ok := t.args[k].(string); ok && firstLine(v) != "" {
			return "Used " + name + " " + firstLine(v)
		}
	}
	return "Used " + name
}

// firstLine is the text's first line, marked when there is more.
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i]) + " …"
	}
	return s
}

func (p *plainPrinter) end() {
	if p.open {
		fmt.Fprintln(p.out)
		p.open = false
	}
}

// How often airo looks at an Agent that is not ready. A variable for tests.
var readyPoll = 2 * time.Second

// readiness says whether the Agent takes sessions now, and else why not. It
// is an error when the cluster has turned the Agent down, because waiting
// does not change that. A new Agent has no conditions until kagent has read
// it, and takes no session before.
func readiness(a *unstructured.Unstructured) (ready bool, why string, err error) {
	if a.GetDeletionTimestamp() != nil {
		return false, "", deletedAgent(a)
	}
	s := stateOf(a)
	switch {
	case s.ready:
		return true, "", nil
	case s.refused:
		return false, "", agentChange(fmt.Sprintf("agent %s cannot run: %s", a.GetName(), s.reason))
	case !s.seen:
		return false, "the cluster has not reported on it yet", nil
	case strings.TrimSpace(s.reason) == "":
		return false, "the cluster gives no reason yet", nil
	}
	return false, strings.TrimSpace(s.reason), nil
}

// waitForSessions returns when the Agent takes sessions. It calls report
// each time what it waits for changes, and looks again every poll, until
// ctx ends.
func waitForSessions(ctx context.Context, check func(context.Context) (bool, string, error), report func(why string), poll time.Duration) error {
	said := ""
	for {
		ready, why, err := check(ctx)
		if err != nil || ready {
			return err
		}
		if why != said {
			report(why)
			said = why
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(poll):
		}
	}
}

// awaitAgent waits until the Agent takes sessions. On a styled terminal a
// spinner says why and for how long, and leaves no trace. Elsewhere each
// reason is a line on errOut. The spinner reads Ctrl-C from in, so it needs
// in to be a terminal: a pipe holds the prompt.
func awaitAgent(ctx context.Context, c client.Reader, ns, name string, in io.Reader, errOut io.Writer) (*unstructured.Unstructured, error) {
	a := &unstructured.Unstructured{}
	a.SetGroupVersionKind(agentGVK)
	a.SetNamespace(ns)
	a.SetName(name)
	check := func(ctx context.Context) (bool, string, error) {
		next, err := getChatAgent(ctx, c, a)
		if err != nil {
			return false, "", err
		}
		a = next
		return readiness(a)
	}
	ready, _, err := check(ctx)
	if err != nil || ready {
		return a, err
	}
	if ui.Styled(errOut) && ui.IsTerminal(in) {
		err = spinUntilReady(ctx, in, errOut, name, func(ctx context.Context, report func(string)) error {
			return waitForSessions(ctx, check, report, readyPoll)
		})
	} else {
		err = waitForSessions(ctx, check, func(why string) {
			fmt.Fprintf(errOut, "Waiting for agent %s to become ready: %s\n", name, why)
		}, readyPoll)
		if err == nil {
			fmt.Fprintf(errOut, "Agent %s is ready.\n", name)
		}
	}
	if err != nil {
		return nil, err
	}
	return a, nil
}

// lines reads the person's input a line at a time.
type lines struct {
	r *bufio.Reader
}

// read returns the next line, or ctx's error when ctx is done first.
func (l *lines) read(ctx context.Context) (string, error) {
	return ui.ReadLine(ctx, l.r)
}
