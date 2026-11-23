package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/util/retry"
	"k8s.io/kubectl/pkg/cmd/util/editor"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/airohq/airo-cli/internal/ui"
	"github.com/airohq/airo-cli/internal/wizard"
)

// settings are the fields of an Agent that create sets and update changes.
type settings struct {
	prompt, description, harness, model string
}

// settingsOf reads an Agent's settings. A local Agent's harness is its
// relay, which update leaves alone.
func settingsOf(a *unstructured.Unstructured) (s settings, local bool) {
	s.prompt, _, _ = unstructured.NestedString(a.Object, "spec", "template", "inline", "systemPrompt")
	s.description, _, _ = unstructured.NestedString(a.Object, "spec", "template", "inline", "description")
	s.model, _, _ = unstructured.NestedString(a.Object, "spec", "template", "inline", "modelConfig", "name")
	s.harness, _, _ = unstructured.NestedString(a.Object, "spec", "harnessRef", "name")
	return s, a.GetLabels()[localAgentLabel] == "true"
}

// changes are what update changes: nil keeps a setting as it is.
type changes struct {
	prompt, description, harness, model *string
}

func (ch changes) none() bool {
	return ch.prompt == nil && ch.description == nil && ch.harness == nil && ch.model == nil
}

func newUpdateCommand(flags *genericclioptions.ConfigFlags) *cobra.Command {
	var o options
	cmd := &cobra.Command{
		Use:   "update [NAME]",
		Short: "Change an agent's instructions, description, harness or model",
		Long: `Update changes the settings of an Agent that the flags name, and only
those, then waits until the agent is ready again.

Without flags, on a terminal, it asks for each setting with the current
value filled in: Enter keeps it. Instructions of several lines open in
your editor, KUBE_EDITOR or EDITOR. Without NAME it asks for the agent.

--description "" removes the description, and --model "" the model, which
only a harness that runs its own image (byo) can do without. A local
agent's harness is its relay, which update does not change.

Change skills with airo agent skill, and anything else with
airo agent edit NAME.`,
		Example: `  airo agent update reviewer                 # asks, with the current values
  airo agent update reviewer --model sonnet-5.5
  airo agent update reviewer --prompt-file prompt.md --description "Reviews pull requests"`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			ch, err := flagChanges(cmd, o)
			if err != nil {
				return err
			}
			c, ns, err := connect(flags)
			if err != nil {
				return err
			}
			var name string
			if len(args) == 1 {
				name = args[0]
			}
			if !ch.none() {
				if name == "" {
					return errors.New("name the agent to update: airo agent update NAME")
				}
				return update(ctx, cmd.OutOrStdout(), c, ns, name, ch, readyTimeout)
			}
			header := fmt.Sprintf("Updating an agent in project %s", ui.Text(ns))
			if name != "" {
				header = fmt.Sprintf("Updating agent %s/%s", ui.Text(ns), ui.Text(name))
			}
			w := wizard.WithHeader(cmd, header)
			if w == nil {
				return errors.New("give what to change with --prompt, --prompt-file, --description, --harness or --model, or run on a terminal to be asked")
			}
			if name == "" {
				names, err := agentNames(ctx, c, ns)
				if err != nil {
					return err
				}
				if name, err = w.Pick("agent", names); err != nil {
					return err
				}
			}
			if ch, err = askChanges(ctx, w, c, ns, name); err != nil {
				return err
			}
			return update(ctx, cmd.OutOrStdout(), c, ns, name, ch, readyTimeout)
		},
	}
	cmd.Flags().StringVar(&o.prompt, "prompt", "", "the agent's new system prompt")
	cmd.Flags().StringVar(&o.promptFile, "prompt-file", "", "a file with the new system prompt, or - for standard input")
	cmd.Flags().StringVar(&o.description, "description", "", `what the agent is for, in a line; "" removes it`)
	cmd.Flags().StringVar(&o.harness, "harness", "", "name of the Harness to run on")
	cmd.Flags().StringVar(&o.model, "model", "", `name of the ModelConfig to use; "" removes it`)
	cmd.MarkFlagsMutuallyExclusive("prompt", "prompt-file")
	return cmd
}

// flagChanges are the changes that the flags given name.
func flagChanges(cmd *cobra.Command, o options) (changes, error) {
	var ch changes
	f := cmd.Flags()
	if f.Changed("prompt") {
		if strings.TrimSpace(o.prompt) == "" {
			return ch, errors.New("--prompt is empty: an agent needs instructions")
		}
		ch.prompt = &o.prompt
	}
	if f.Changed("prompt-file") {
		prompt, err := readPrompt(cmd.InOrStdin(), o.promptFile)
		if err != nil {
			return ch, err
		}
		ch.prompt = &prompt
	}
	if f.Changed("description") {
		ch.description = &o.description
	}
	if f.Changed("harness") {
		if o.harness == "" {
			return ch, errors.New("--harness is empty: an agent needs a harness")
		}
		ch.harness = &o.harness
	}
	if f.Changed("model") {
		ch.model = &o.model
	}
	return ch, nil
}

// editText opens text in the person's editor, as airo edit does, and
// returns what they saved. Tests replace it.
var editText = func(text string) (string, error) {
	e := editor.NewDefaultEditor([]string{"KUBE_EDITOR", "EDITOR"})
	edited, file, err := e.LaunchTempFile("airo-instructions-", ".md", strings.NewReader(text))
	if file != "" {
		_ = os.Remove(file)
	}
	return string(edited), err
}

// askChanges asks for each setting with its current value, and returns
// the ones the person changed.
func askChanges(ctx context.Context, w *wizard.Wizard, c client.Client, ns, name string) (changes, error) {
	var ch changes
	a, err := getAgent(ctx, c, ns, name)
	if err != nil {
		return ch, err
	}
	cur, local := settingsOf(a)
	next := cur

	if strings.Contains(strings.TrimSpace(cur.prompt), "\n") {
		lines := strings.Count(strings.TrimSpace(cur.prompt), "\n") + 1
		edit, err := w.Confirm(fmt.Sprintf("Change the instructions (%d lines) in your editor?", lines), false)
		if err != nil {
			return ch, err
		}
		if edit {
			if next.prompt, err = editText(cur.prompt); err != nil {
				return ch, fmt.Errorf("edit the instructions: %w", err)
			}
			if strings.TrimSpace(next.prompt) == "" {
				w.Note("  Empty instructions: kept the current ones")
				next.prompt = cur.prompt
			}
		}
	} else {
		for {
			if next.prompt, err = w.Edit("Instructions", cur.prompt); err != nil {
				return ch, err
			}
			if strings.TrimSpace(next.prompt) != "" {
				break
			}
			w.Problem(errors.New("an agent needs instructions"))
		}
	}
	if next.description, err = w.Edit("Description", cur.description); err != nil {
		return ch, err
	}

	harnessRuntime := ""
	if local {
		w.Note(fmt.Sprintf("  harness: %s, the relay of this local agent", cur.harness))
	} else {
		h, err := pickCurrent(ctx, w, c, ns, harnesses, cur.harness)
		if err != nil {
			return ch, err
		}
		next.harness, harnessRuntime = h.GetName(), runtime(h)
	}
	// A harness that runs its own image may do without a model.
	useModel := cur.model != "" || (harnessRuntime != runtimeBYO && !local)
	if !useModel {
		if useModel, err = w.Confirm("Choose a project model for this harness?", false); err != nil {
			return ch, err
		}
	}
	if useModel {
		m, err := pickCurrent(ctx, w, c, ns, models, cur.model)
		if err != nil {
			return ch, err
		}
		next.model = m.GetName()
	}

	changed := func(from, to string) *string {
		if from == to {
			return nil
		}
		return &to
	}
	return changes{
		prompt:      changed(cur.prompt, next.prompt),
		description: changed(cur.description, next.description),
		harness:     changed(cur.harness, next.harness),
		model:       changed(cur.model, next.model),
	}, nil
}

// pickCurrent offers the project's items of a kind with the current one
// marked, which Enter keeps.
func pickCurrent(ctx context.Context, w *wizard.Wizard, c client.Client, ns string, k choice, current string) (*unstructured.Unstructured, error) {
	items, err := list(ctx, c, ns, k)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("project %s has no %s, and an agent needs one: ask who runs the platform, or add one with airo apply -f", ns, k.noun)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].GetName() < items[j].GetName() })
	labels := make([]string, len(items))
	at := -1
	for i := range items {
		labels[i] = items[i].GetName()
		if k.note != nil && k.note(&items[i]) != "" {
			labels[i] += " (" + k.note(&items[i]) + ")"
		}
		if items[i].GetName() == current {
			at = i
		}
	}
	label, err := w.PickCurrent(k.noun, labels, at)
	if err != nil {
		return nil, err
	}
	for i := range labels {
		if labels[i] == label {
			return &items[i], nil
		}
	}
	return nil, fmt.Errorf("no %s chosen", k.noun)
}

// update changes the Agent's settings that ch names, and only those, then
// waits until the cluster runs the changed Agent.
func update(ctx context.Context, out io.Writer, c client.Client, ns, name string, ch changes, timeout time.Duration) error {
	if ch.harness != nil {
		h, err := choose(ctx, c, ns, harnesses, *ch.harness, nil)
		if err != nil {
			return err
		}
		if ch.model != nil && *ch.model == "" && runtime(h) != runtimeBYO {
			return fmt.Errorf("harness %s runs %s, which needs a model: keep one or name another with --model", h.GetName(), runtime(h))
		}
	}
	if ch.model != nil && *ch.model != "" {
		if _, err := choose(ctx, c, ns, models, *ch.model, nil); err != nil {
			return err
		}
	}
	var (
		a       *unstructured.Unstructured
		before  settings
		changed bool
	)
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var err error
		if a, err = getAgent(ctx, c, ns, name); err != nil {
			return err
		}
		if _, found, _ := unstructured.NestedMap(a.Object, "spec", "template", "inline"); !found {
			return fmt.Errorf("agent %s has no spec.template.inline: change it with airo agent edit %s", name, name)
		}
		var local bool
		before, local = settingsOf(a)
		if local && ch.harness != nil && *ch.harness != before.harness {
			return fmt.Errorf("agent %s is local: its harness is its relay, which update does not change", name)
		}
		if changed = applyChanges(a, ch); !changed {
			return nil
		}
		if err := c.Update(ctx, a); err != nil {
			// Wrapped, a conflict is still one to retry.
			return fmt.Errorf("save agent %s/%s: %w", ns, name, err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if !changed {
		fmt.Fprintf(out, "Agent %s/%s is unchanged\n", ns, name)
		return nil
	}
	after, _ := settingsOf(a)
	fmt.Fprintf(out, "Updated agent %s/%s\n", ns, name)
	show := func(label, from, to string) {
		switch {
		case from == to:
		case to == "":
			fmt.Fprintf(out, "  %-13s removed\n", label+":")
		default:
			fmt.Fprintf(out, "  %-13s %s\n", label+":", to)
		}
	}
	if before.prompt != after.prompt {
		fmt.Fprintf(out, "  %-13s changed\n", "instructions:")
	}
	show("description", before.description, after.description)
	show("harness", before.harness, after.harness)
	show("model", before.model, after.model)
	stays := fmt.Sprintf("The agent keeps the change: see it with airo agent get %s, or change it again with airo agent update %s", name, name)
	if err := waitReady(ctx, c, a, timeout, stays); err != nil {
		return err
	}
	fmt.Fprintln(out, "  ready")
	return nil
}

// applyChanges sets the changed fields of the Agent, and only those, and
// says whether any differs from before.
func applyChanges(a *unstructured.Unstructured, ch changes) bool {
	before, _ := settingsOf(a)
	set := func(value *string, fields ...string) {
		if value == nil {
			return
		}
		if *value == "" {
			unstructured.RemoveNestedField(a.Object, fields...)
			return
		}
		_ = unstructured.SetNestedField(a.Object, *value, fields...)
	}
	set(ch.prompt, "spec", "template", "inline", "systemPrompt")
	set(ch.description, "spec", "template", "inline", "description")
	set(ch.harness, "spec", "harnessRef", "name")
	if ch.model != nil && *ch.model == "" {
		unstructured.RemoveNestedField(a.Object, "spec", "template", "inline", "modelConfig")
	} else {
		set(ch.model, "spec", "template", "inline", "modelConfig", "name")
	}
	after, _ := settingsOf(a)
	return before != after
}
