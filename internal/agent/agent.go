// Package agent implements airo agent for managing agents and airo chat
// for conversations with them.
package agent

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/airohq/airo-cli/internal/generic"
	"github.com/airohq/airo-cli/internal/skill"
	"github.com/airohq/airo-cli/internal/ui"
	"github.com/airohq/airo-cli/internal/wizard"
)

// The kinds behind airo agent, named only here: Airo-owned kinds are planned
// to replace them. The CLI reads them as unstructured and uses only the
// fields it needs, so it depends on the cluster's API and on no component's
// Go types.
var (
	agentGVK       = schema.GroupVersionKind{Group: "api.kagent.dev", Version: "v1alpha3", Kind: "Agent"}
	harnessGVK     = schema.GroupVersionKind{Group: "api.kagent.dev", Version: "v1alpha3", Kind: "Harness"}
	modelConfigGVK = schema.GroupVersionKind{Group: "api.kagent.dev", Version: "v1alpha3", Kind: "ModelConfig"}
	templateGVK    = schema.GroupVersionKind{Group: "api.kagent.dev", Version: "v1alpha3", Kind: "AgentTemplate"}
)

// The runtimes a Harness can select, as the fields of its spec. A Harness
// with byo runs its own image, which may need no model from the Agent.
var runtimes = []string{"claude", "codex", "kagent", "byo"}

const runtimeBYO = "byo"

// How long create waits for the Agent to be ready. The cluster builds the
// Agent's runtime first, which takes longer than a Tunnel's URLs.
const readyTimeout = 3 * time.Minute

// NewCommand returns the agent resource commands.
func NewCommand(flags *genericclioptions.ConfigFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "agent",
		Aliases: []string{"agents"},
		Short:   "Manage agents in the current project",
		Long:    "Create, list, inspect, update, edit or delete agents in the current project. Use airo chat to start a conversation.",
		Example: `  airo agent list
  airo agent get assistant
  airo agent create reviewer --prompt "You review pull requests."`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return cmd.Help()
			}
			return fmt.Errorf("unknown agent verb %q: see airo agent --help for verbs, or use airo chat to send a prompt", args[0])
		},
	}
	generic.AddBasic(cmd, generic.Commands(flags, cmd.Name())...)
	ui.AddCommandGroup(cmd, "agent", "Agent Commands", newCreateCommand(flags), newUpdateCommand(flags), newSkillCommand(flags))
	ui.AddCommandGroup(cmd, "machine", "Run on This Machine", newServeCommand(flags), newConnectCommand(flags))
	return cmd
}

// options are the flags of agent create.
type options struct {
	prompt      string
	promptFile  string
	description string
	model       string
	harness     string
	local       bool
	skills      []string
}

func newCreateCommand(flags *genericclioptions.ConfigFlags) *cobra.Command {
	var o options
	cmd := &cobra.Command{
		Use:   "create [NAME]",
		Short: "Create an agent from a prompt",
		Long: `Create makes an Agent in the project and waits until it is ready.

By default the agent uses an existing project Harness. The wizard offers
the project's Harness and model choices and highlights the defaults. Use
--harness or --model to supply a choice directly.
Use --local for a durable Agent served from your machine. The CLI creates
its private relay in the same project. Connect or reconnect with airo agent connect NAME.
In scripts, give NAME and --prompt or --prompt-file.
A script uses the project's only choice or its default when a flag is omitted.

--skill adds a skill, and may be repeated. airo pins a branch or a tag to
its commit, and an image tag to its digest, because the Agent holds only
immutable versions. Change the skills later with airo agent skill.

Tools and the other fields of an Agent go in a file, for airo apply -f.`,
		Example: `  airo agent create reviewer --prompt "You review pull requests."
  airo agent create laptop --local --prompt "You are a coding assistant."
  airo agent create reviewer --prompt-file prompt.md --model sonnet-5.5 --harness claude
  airo agent create reviewer --prompt-file prompt.md \
    --skill https://github.com/anthropics/skills/tree/main/skills/pdf`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ns, _, err := flags.ToRawKubeConfigLoader().Namespace()
			if err != nil {
				return err
			}
			var w *wizard.Wizard
			if o.promptFile != "-" {
				w = wizard.New(cmd, "agent", ns)
			}
			name, err := w.Name(args)
			if err != nil {
				return err
			}
			if w == nil && o.promptFile == "" && strings.TrimSpace(o.prompt) == "" {
				return fmt.Errorf("provide instructions with --prompt or --prompt-file")
			}
			// A source airo cannot read fails before anything is asked or sent.
			for _, arg := range o.skills {
				if _, err := skill.Parse(arg); err != nil {
					return err
				}
			}
			cfg, err := flags.ToRESTConfig()
			if err != nil {
				return err
			}
			c, err := client.New(cfg, client.Options{})
			if err != nil {
				return err
			}
			if o.promptFile != "" {
				if o.prompt, err = readPrompt(cmd.InOrStdin(), o.promptFile); err != nil {
					return err
				}
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return create(ctx, cmd.OutOrStdout(), c, ns, name, o, w, readyTimeout)
		},
	}
	cmd.Flags().StringVar(&o.prompt, "prompt", "", "the agent's system prompt")
	cmd.Flags().StringVar(&o.promptFile, "prompt-file", "", "a file with the agent's system prompt, or - for standard input")
	cmd.Flags().StringVar(&o.description, "description", "", "what the agent is for, in a line")
	cmd.Flags().StringVar(&o.model, "model", "", "name of the ModelConfig to use (default: the project's only one, or its default)")
	cmd.Flags().StringVar(&o.harness, "harness", "", "name of the Harness to run on (default: the project's only one, or its default)")
	cmd.Flags().BoolVar(&o.local, "local", false, "create a durable Agent with a dedicated relay for airo agent connect")
	cmd.MarkFlagsMutuallyExclusive("local", "harness")
	cmd.MarkFlagsMutuallyExclusive("prompt", "prompt-file")
	cmd.Flags().StringArrayVar(&o.skills, "skill", nil, "a skill's source, such as https://github.com/OWNER/REPO/tree/main/PATH; repeat for more")
	return cmd
}

// readPrompt reads the system prompt from a file, or from in for "-".
func readPrompt(in io.Reader, path string) (string, error) {
	var (
		data []byte
		err  error
	)
	if path == "-" {
		data, err = io.ReadAll(in)
	} else {
		data, err = os.ReadFile(path)
	}
	if err != nil {
		return "", fmt.Errorf("read the prompt: %w", err)
	}
	if strings.TrimSpace(string(data)) == "" {
		return "", fmt.Errorf("the prompt in %s is empty", path)
	}
	return string(data), nil
}

// create resolves the flags against the project, creates the Agent and waits
// until it is ready.
func create(ctx context.Context, out io.Writer, c client.Client, ns, name string, o options, w *wizard.Wizard, timeout time.Duration) error {
	if w != nil && strings.TrimSpace(o.prompt) == "" {
		var err error
		if o.prompt, err = w.Required("Instructions"); err != nil {
			return err
		}
	}
	skills, err := pinSkills(ctx, o.skills)
	if err != nil {
		return err
	}
	if w != nil && len(o.skills) == 0 {
		if skills, err = askSkills(ctx, w); err != nil {
			return err
		}
	}

	var pick func(string, []string) (string, error)
	if w != nil {
		pick = w.Pick
	}
	var substrate map[string]any
	harnessRuntime := runtimeBYO
	if o.local {
		if problems := validation.IsDNS1123Label(name); len(problems) != 0 {
			return fmt.Errorf("a local Agent shares its Tunnel name, which must be a DNS label: %s", strings.Join(problems, ", "))
		}
		substrate, err = localSubstrate(ctx, c, ns)
		if err != nil {
			return err
		}
		o.harness = name
	} else {
		h, err := choose(ctx, c, ns, harnesses, o.harness, pick)
		if err != nil {
			return err
		}
		o.harness, harnessRuntime = h.GetName(), runtime(h)
	}
	// A Harness with its own image may need no model. The others do.
	useModel := o.model != "" || harnessRuntime != runtimeBYO
	if w != nil && !o.local && !useModel {
		useModel, err = w.Confirm("Choose a project model for this harness?", false)
		if err != nil {
			return err
		}
	}
	if useModel {
		model, err := choose(ctx, c, ns, models, o.model, pick)
		if err != nil {
			return err
		}
		o.model = model.GetName()
	}

	a := build(ns, name, o)
	if err := setSkills(a, skills); err != nil {
		return err
	}
	if err := c.Create(ctx, a); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("agent %s/%s exists: pick another name, or delete it with airo agent delete %s", ns, name, name)
		}
		return fmt.Errorf("create agent %s/%s: %w", ns, name, err)
	}
	if o.local {
		if err := provisionLocalRelay(ctx, c, a, substrate); err != nil {
			return err
		}
	}
	fmt.Fprintf(out, "Created agent %s/%s\n", ns, name)
	if r := harnessRuntime; r != "" {
		fmt.Fprintf(out, "  harness:  %s (%s)\n", o.harness, r)
	} else {
		fmt.Fprintf(out, "  harness:  %s\n", o.harness)
	}
	if o.model != "" {
		fmt.Fprintf(out, "  model:    %s\n", o.model)
	}
	for _, p := range skills {
		fmt.Fprintf(out, "  skill:    %s  %s\n", p.Name, p)
	}
	stays := fmt.Sprintf("It stays in the project: delete it with airo agent delete %s, or change it with airo agent update %[1]s", a.GetName())
	if err := waitReady(ctx, c, a, timeout, stays); err != nil {
		return err
	}
	fmt.Fprintln(out, "  ready")
	return nil
}

// build returns a new Agent with only the fields the flags set, so the API's
// defaults decide the rest.
func build(ns, name string, o options) *unstructured.Unstructured {
	a := &unstructured.Unstructured{}
	a.SetGroupVersionKind(agentGVK)
	a.SetNamespace(ns)
	a.SetName(name)
	inline := map[string]any{}
	if o.prompt != "" {
		inline["systemPrompt"] = o.prompt
	}
	if o.description != "" {
		inline["description"] = o.description
	}
	if o.model != "" {
		inline["modelConfig"] = map[string]any{"name": o.model}
	}
	spec := map[string]any{
		"harnessRef": map[string]any{"name": o.harness},
		"template":   map[string]any{"inline": inline},
	}
	if o.local {
		a.SetLabels(map[string]string{localAgentLabel: "true"})
		spec["egressPolicyRefs"] = relayPolicies(o.harness)
	}
	a.Object["spec"] = spec
	return a
}

const localAgentLabel = "airohq.dev/local"

func relayPolicies(name string) []any {
	return []any{map[string]any{"name": name}}
}

// choice is a kind an Agent refers to, as a person reads and names it.
type choice struct {
	gvk          schema.GroupVersionKind
	noun, plural string
	flag         string
	// marker is the annotation that marks the project's default of the kind
	// with "true", or empty for a kind without a default. The names are the
	// platform's, not a component's.
	marker string
	// note adds a word to a name in the list of choices. It may be nil.
	note func(*unstructured.Unstructured) string
}

var (
	agents    = choice{gvk: agentGVK, noun: "agent", plural: "agents", flag: "--agent"}
	harnesses = choice{gvk: harnessGVK, noun: "harness", plural: "harnesses", flag: "--harness", marker: "airohq.dev/default-harness", note: runtime}
	models    = choice{gvk: modelConfigGVK, noun: "model", plural: "models", flag: "--model", marker: "airohq.dev/default-model"}
)

// newestDefault returns the item marked as the default with the annotation
// marker, or nil when none is. Of several, the newest wins, as with
// Kubernetes' default StorageClass: a project's people override the
// platform's default by marking their own, and two defaults are a normal
// state while someone moves from one to the other. The name decides between
// two of the same second, so the answer is the same on every run.
func newestDefault(items []unstructured.Unstructured, marker string) *unstructured.Unstructured {
	var newest *unstructured.Unstructured
	for i := range items {
		item := &items[i]
		if marker == "" || item.GetAnnotations()[marker] != "true" {
			continue
		}
		if newest == nil {
			newest = item
			continue
		}
		at, than := item.GetCreationTimestamp(), newest.GetCreationTimestamp()
		if than.Before(&at) || (at.Equal(&than) && item.GetName() < newest.GetName()) {
			newest = item
		}
	}
	return newest
}

// choose honours an explicit name. The wizard offers the project's resources
// with the default first. A script accepts the only choice or the default.
func choose(ctx context.Context, c client.Client, ns string, k choice, name string, pick func(string, []string) (string, error)) (*unstructured.Unstructured, error) {
	items, err := list(ctx, c, ns, k)
	if err != nil {
		return nil, err
	}
	def := newestDefault(items, k.marker)
	var defaultLabel string
	var names []string
	byLabel := make(map[string]*unstructured.Unstructured, len(items))
	for i := range items {
		item := &items[i]
		if item.GetName() == name {
			return item, nil
		}
		n := item.GetName()
		if k.note != nil && k.note(item) != "" {
			n += " (" + k.note(item) + ")"
		}
		if pick != nil && name == "" && item == def {
			defaultLabel = n + " (default)"
			byLabel[defaultLabel] = item
		} else {
			byLabel[n] = item
			names = append(names, n)
		}
	}
	sort.Strings(names)
	if defaultLabel != "" {
		names = append([]string{defaultLabel}, names...)
	}
	switch {
	case name != "" && len(names) == 0:
		return nil, fmt.Errorf("project %s has no %s named %s, and no other", ns, k.noun, name)
	case name != "":
		return nil, fmt.Errorf("project %s has no %s named %s: it has %s", ns, k.noun, name, strings.Join(names, ", "))
	case len(names) == 0:
		return nil, fmt.Errorf("project %s has no %s, and an agent needs one: ask who runs the platform, or add one with airo apply -f", ns, k.noun)
	case len(names) == 1 && pick == nil:
		return &items[0], nil
	}
	if pick != nil {
		selected, err := pick(k.noun, names)
		if err != nil {
			return nil, err
		}
		return byLabel[selected], nil
	}
	if def != nil {
		return def, nil
	}
	return nil, fmt.Errorf("project %s has %d %s and no default, name one with %s: %s", ns, len(names), k.plural, k.flag, strings.Join(names, ", "))
}

// list returns the project's resources of a kind.
func list(ctx context.Context, c client.Client, ns string, k choice) ([]unstructured.Unstructured, error) {
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(k.gvk.GroupVersion().WithKind(k.gvk.Kind + "List"))
	if err := c.List(ctx, list, client.InNamespace(ns)); err != nil {
		if meta.IsNoMatchError(err) {
			// A cluster whose kinds differ from what this version knows.
			return nil, fmt.Errorf("the cluster does not serve %s in %s: agents may be turned off, or this airo does not match the cluster, update it", k.gvk.Kind, k.gvk.GroupVersion())
		}
		return nil, fmt.Errorf("list the %s of %s: %w", k.plural, ns, err)
	}
	return list.Items, nil
}

// runtime is what a Harness runs agents with, such as claude, or empty for a
// Harness this version cannot read.
func runtime(h *unstructured.Unstructured) string {
	for _, r := range runtimes {
		if _, found, _ := unstructured.NestedMap(h.Object, "spec", r); found {
			return r
		}
	}
	return ""
}

// waitReady reads the Agent into a until the cluster has made it ready.
// stays tells the person what became of the change when the Agent is not
// ready.
func waitReady(ctx context.Context, c client.Client, a *unstructured.Unstructured, timeout time.Duration, stays string) error {
	var (
		reason  string
		seen    bool
		ready   bool
		refused bool
	)
	local := a.GetLabels()[localAgentLabel] == "true"
	waiting, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	err := wait.PollUntilContextCancel(waiting, time.Second, true, func(ctx context.Context) (bool, error) {
		if err := c.Get(ctx, client.ObjectKeyFromObject(a), a); err != nil {
			return false, err
		}
		s := stateOf(a)
		ready, refused, seen, reason = s.ready, s.refused, s.seen, s.reason
		if local {
			// Kagent may still report missing dependencies from before the
			// CLI finished creating them. Wait for its next reconciliation.
			if ready {
				if _, err := resolveAgentTunnel(ctx, c, a); err != nil {
					ready, reason = false, err.Error()
				}
			}
			return ready, nil
		}
		return ready || refused, nil
	})
	name := a.GetNamespace() + "/" + a.GetName()
	switch {
	case ready:
		return nil
	case ctx.Err() != nil:
		// Interrupted. The Agent stays, as any created resource does.
		return ctx.Err()
	case local && waiting.Err() != nil:
		if reason == "" {
			reason = "waiting for the dedicated relay"
		}
		return fmt.Errorf("local agent %s is not ready after %s: %s. Check the Agent and its relay resources in the project. %s", name, timeout, reason, stays)
	case refused:
		return fmt.Errorf("agent %s cannot run: %s. %s", name, reason, stays)
	case waiting.Err() == nil:
		return err
	case !seen:
		return fmt.Errorf("agent %s has no status after %s: the cluster has not answered; are agents turned on in this project? %s", name, timeout, stays)
	case reason == "":
		// A cluster whose Agents differ from what this version knows.
		return fmt.Errorf("agent %s has no Ready condition after %s: this airo may be too old for the cluster, update it", name, timeout)
	}
	return fmt.Errorf("agent %s is not ready after %s: %s. %s", name, timeout, reason, stays)
}

// agentState is what an Agent's conditions say.
type agentState struct {
	// ready: the Agent takes sessions.
	ready bool
	// refused: the cluster has read the Agent and turned it down, such as a
	// model its Harness cannot use. Waiting does not change that.
	refused bool
	// seen: the Agent has conditions at all.
	seen bool
	// reason is the message of the first condition that is not met.
	reason string
}

func stateOf(a *unstructured.Unstructured) agentState {
	var s agentState
	conditions, _, _ := unstructured.NestedSlice(a.Object, "status", "conditions")
	for _, item := range conditions {
		cond, _ := item.(map[string]any)
		s.seen = true
		if cond["status"] == "True" {
			s.ready = s.ready || (cond["type"] == "Ready" && current(cond, a.GetGeneration()))
			continue
		}
		if s.reason == "" {
			s.reason, _ = cond["message"].(string)
		}
		if cond["type"] != "Ready" && cond["status"] == "False" && current(cond, a.GetGeneration()) {
			s.refused = true
		}
	}
	return s
}

// current reports whether a condition is about the Agent as it is now. A
// condition without a generation counts as current.
func current(cond map[string]any, generation int64) bool {
	observed, found := cond["observedGeneration"]
	return !found || fmt.Sprint(observed) == fmt.Sprint(generation)
}
