package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/airohq/airo-cli/internal/picker"
	"github.com/airohq/airo-cli/internal/skill"
	"github.com/airohq/airo-cli/internal/ui"
	"github.com/airohq/airo-cli/internal/wizard"
)

// skillVersionsAnnotation holds, as a JSON object, the version each skill
// was asked for by name, such as a branch, when it was not the default. The
// Agent holds only the pinned commit, and upgrade resolves this version
// again. The name is the platform's, not a component's.
const skillVersionsAnnotation = "airohq.dev/skill-versions"

// pin resolves a skill to an immutable version. Tests replace it.
var pin = skill.Resolver{Client: &http.Client{Timeout: 30 * time.Second}}.Pin

// pinSkills reads and pins the sources of --skill.
func pinSkills(ctx context.Context, args []string) ([]skill.Pinned, error) {
	var pinned []skill.Pinned
	for _, arg := range args {
		p, err := parseAndPin(ctx, arg, pinned)
		if err != nil {
			return nil, err
		}
		pinned = append(pinned, p)
	}
	return pinned, nil
}

func parseAndPin(ctx context.Context, arg string, others []skill.Pinned) (skill.Pinned, error) {
	s, err := skill.Parse(arg)
	if err != nil {
		return skill.Pinned{}, err
	}
	p, err := pin(ctx, s)
	if err != nil {
		return skill.Pinned{}, err
	}
	for _, other := range others {
		if other.Name == p.Name {
			return skill.Pinned{}, fmt.Errorf("two skills are named %s: name one with NAME=SOURCE", p.Name)
		}
	}
	return p, nil
}

// askSkills asks for skill sources until an empty answer.
func askSkills(ctx context.Context, w *wizard.Wizard) ([]skill.Pinned, error) {
	var pinned []skill.Pinned
	for {
		label := "Skill source"
		if len(pinned) > 0 {
			label = "Another skill source"
		}
		answer, err := w.Optional(label)
		if err != nil || answer == "" {
			return pinned, err
		}
		p, err := parseAndPin(ctx, answer, pinned)
		if err != nil {
			w.Problem(err)
			continue
		}
		w.Note(fmt.Sprintf("  %s at %s", p.Name, p.Pin()))
		pinned = append(pinned, p)
	}
}

// setSkills puts the skills in a new Agent.
func setSkills(a *unstructured.Unstructured, pinned []skill.Pinned) error {
	if len(pinned) == 0 {
		return nil
	}
	entries := make([]any, 0, len(pinned))
	versions := map[string]string{}
	for _, p := range pinned {
		entries = append(entries, skillEntry(p))
		versions[p.Name] = p.Version
	}
	if err := unstructured.SetNestedSlice(a.Object, entries, "spec", "template", "inline", "skills"); err != nil {
		return err
	}
	return setVersions(a, versions)
}

// skillEntry is a skill as kagent's Agent v1alpha3 holds it, in
// spec.template.inline.skills.
func skillEntry(p skill.Pinned) map[string]any {
	source := map[string]any{}
	if p.Git != "" {
		source["git"] = map[string]any{"url": p.Git, "commit": p.Commit}
	} else {
		source["oci"] = p.OCI + "@" + p.Digest
	}
	if p.Path != "" {
		source["path"] = p.Path
	}
	return map[string]any{"name": p.Name, "source": source}
}

// pinnedOf reads an entry of spec.template.inline.skills. ok is false for a
// source airo does not read, such as a bucket.
func pinnedOf(entry map[string]any) (p skill.Pinned, ok bool) {
	p.Name, _, _ = unstructured.NestedString(entry, "name")
	p.Path, _, _ = unstructured.NestedString(entry, "source", "path")
	if url, _, _ := unstructured.NestedString(entry, "source", "git", "url"); url != "" {
		p.Git = url
		p.Commit, _, _ = unstructured.NestedString(entry, "source", "git", "commit")
		return p, true
	}
	if image, _, _ := unstructured.NestedString(entry, "source", "oci"); image != "" {
		repo, digest, found := strings.Cut(image, "@")
		p.OCI, p.Digest = repo, digest
		return p, found
	}
	return p, false
}

func entryName(entry any) string {
	m, _ := entry.(map[string]any)
	name, _, _ := unstructured.NestedString(m, "name")
	return name
}

// versions reads skillVersionsAnnotation. A value it cannot read counts as
// no versions: each skill then follows its default.
func versions(a *unstructured.Unstructured) map[string]string {
	v := map[string]string{}
	_ = json.Unmarshal([]byte(a.GetAnnotations()[skillVersionsAnnotation]), &v)
	return v
}

// setVersions writes the versions that are not the default.
func setVersions(a *unstructured.Unstructured, v map[string]string) error {
	for name, version := range v {
		if version == "" {
			delete(v, name)
		}
	}
	annotations := a.GetAnnotations()
	if len(v) == 0 {
		delete(annotations, skillVersionsAnnotation)
	} else {
		data, err := json.Marshal(v)
		if err != nil {
			return err
		}
		if annotations == nil {
			annotations = map[string]string{}
		}
		annotations[skillVersionsAnnotation] = string(data)
	}
	a.SetAnnotations(annotations)
	return nil
}

// inlineSkills returns the Agent's skills, or an error for an Agent whose
// skills are not its own.
func inlineSkills(a *unstructured.Unstructured) ([]any, error) {
	if ref, _, _ := unstructured.NestedString(a.Object, "spec", "template", "ref", "name"); ref != "" {
		return nil, fmt.Errorf("agent %s takes its skills from the AgentTemplate %s: change the template with airo apply -f", a.GetName(), ref)
	}
	if _, found, _ := unstructured.NestedMap(a.Object, "spec", "template", "inline"); !found {
		return nil, fmt.Errorf("agent %s has no spec.template.inline: this airo may not match the cluster, update it", a.GetName())
	}
	entries, _, err := unstructured.NestedSlice(a.Object, "spec", "template", "inline", "skills")
	if err != nil {
		return nil, fmt.Errorf("read the skills of agent %s: %w", a.GetName(), err)
	}
	return entries, nil
}

func getAgent(ctx context.Context, c client.Client, ns, name string) (*unstructured.Unstructured, error) {
	a := &unstructured.Unstructured{}
	a.SetGroupVersionKind(agentGVK)
	err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, a)
	if apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("project %s has no agent named %s", ns, name)
	}
	if err != nil {
		return nil, fmt.Errorf("read agent %s/%s: %w", ns, name, err)
	}
	return a, nil
}

// changeSkills applies change to the Agent's skills and saves the Agent,
// again from a fresh read when someone else changed it meanwhile.
func changeSkills(ctx context.Context, c client.Client, ns, name string, change func(a *unstructured.Unstructured, entries []any) ([]any, error)) (*unstructured.Unstructured, error) {
	var a *unstructured.Unstructured
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var err error
		if a, err = getAgent(ctx, c, ns, name); err != nil {
			return err
		}
		entries, err := inlineSkills(a)
		if err != nil {
			return err
		}
		if entries, err = change(a, entries); err != nil {
			return err
		}
		if len(entries) == 0 {
			unstructured.RemoveNestedField(a.Object, "spec", "template", "inline", "skills")
		} else if err := unstructured.SetNestedSlice(a.Object, entries, "spec", "template", "inline", "skills"); err != nil {
			return err
		}
		if err := c.Update(ctx, a); err != nil {
			// Wrapped, a conflict is still one to retry.
			return fmt.Errorf("save agent %s/%s: %w", ns, name, err)
		}
		return nil
	})
	return a, err
}

// find returns the index of the skill named name, or an error that names
// the skills there are.
func find(agent string, entries []any, name string) (int, error) {
	var names []string
	for i, entry := range entries {
		if entryName(entry) == name {
			return i, nil
		}
		names = append(names, entryName(entry))
	}
	if len(names) == 0 {
		return -1, fmt.Errorf("agent %s has no skills", agent)
	}
	sort.Strings(names)
	return -1, fmt.Errorf("agent %s has no skill named %s: it has %s", agent, name, strings.Join(names, ", "))
}

func skillStays(agent string) string {
	return fmt.Sprintf("The agent keeps the change: see its skills with airo agent skill list --agent %s", agent)
}

// listSkills prints the Agent's skills as a table.
func listSkills(ctx context.Context, out, errOut io.Writer, c client.Client, ns, agent string) error {
	a, err := getAgent(ctx, c, ns, agent)
	if err != nil {
		return err
	}
	entries, err := inlineSkills(a)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		fmt.Fprintf(errOut, "Agent %s has no skills. Add one with airo agent skill add SOURCE --agent %s\n", agent, agent)
		return nil
	}
	v := versions(a)
	tw := tabwriter.NewWriter(out, 0, 0, 3, ' ', 0)
	fmt.Fprintln(tw, "NAME\tVERSION\tSOURCE")
	for _, entry := range entries {
		m, _ := entry.(map[string]any)
		p, ok := pinnedOf(m)
		version := v[p.Name]
		if version == "" {
			version = "default"
		}
		source := p.String()
		if !ok {
			version, source = "-", "a source airo does not read"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\n", p.Name, version, source)
	}
	return tw.Flush()
}

// addSkill pins a source and adds it to the Agent.
func addSkill(ctx context.Context, out io.Writer, c client.Client, ns, agent, arg string, timeout time.Duration) error {
	p, err := parseAndPin(ctx, arg, nil)
	if err != nil {
		return err
	}
	a, err := changeSkills(ctx, c, ns, agent, func(a *unstructured.Unstructured, entries []any) ([]any, error) {
		for _, entry := range entries {
			if entryName(entry) == p.Name {
				return nil, fmt.Errorf("agent %s has a skill named %s: upgrade it with airo agent skill upgrade %s, or name the new one with NAME=SOURCE", agent, p.Name, p.Name)
			}
		}
		v := versions(a)
		v[p.Name] = p.Version
		return append(entries, skillEntry(p)), setVersions(a, v)
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "Added skill %s to agent %s/%s\n", p.Name, ns, agent)
	fmt.Fprintf(out, "  source:   %s\n", p)
	return finish(ctx, out, c, a, timeout)
}

// upgradeSkill pins the skill again: at version when it is given, or else
// at the version it was added with, which by default is the newest commit
// of the default branch or the image tag latest.
func upgradeSkill(ctx context.Context, out io.Writer, c client.Client, ns, agent, name, version string, timeout time.Duration) error {
	a, err := getAgent(ctx, c, ns, agent)
	if err != nil {
		return err
	}
	entries, err := inlineSkills(a)
	if err != nil {
		return err
	}
	i, err := find(agent, entries, name)
	if err != nil {
		return err
	}
	entry, _ := entries[i].(map[string]any)
	old, ok := pinnedOf(entry)
	if !ok {
		return fmt.Errorf("skill %s comes from a source airo does not read: change it with airo apply -f", name)
	}
	s := old.Skill
	s.Version = versions(a)[name]
	if version != "" {
		s.Version = version
	}
	next, err := pin(ctx, s)
	if err != nil {
		return err
	}
	if next.Pin() == old.Pin() && s.Version == versions(a)[name] {
		follows := "the default branch"
		if s.OCI != "" {
			follows = "the tag latest"
		}
		if s.Version != "" {
			follows = s.Version
		}
		fmt.Fprintf(out, "Skill %s on agent %s/%s is up to date with %s\n", name, ns, agent, follows)
		fmt.Fprintf(out, "  source:   %s\n", old)
		return nil
	}
	a, err = changeSkills(ctx, c, ns, agent, func(a *unstructured.Unstructured, entries []any) ([]any, error) {
		i, err := find(agent, entries, name)
		if err != nil {
			return nil, err
		}
		entry, _ := entries[i].(map[string]any)
		if current, _ := pinnedOf(entry); current.Location() != old.Location() {
			return nil, fmt.Errorf("skill %s on agent %s changed meanwhile: run the upgrade again", name, agent)
		}
		// Only the version changes, so fields this airo does not know stay.
		if next.Git != "" {
			if err := unstructured.SetNestedField(entry, next.Commit, "source", "git", "commit"); err != nil {
				return nil, err
			}
		} else if err := unstructured.SetNestedField(entry, next.OCI+"@"+next.Digest, "source", "oci"); err != nil {
			return nil, err
		}
		entries[i] = entry
		v := versions(a)
		v[name] = s.Version
		return entries, setVersions(a, v)
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "Upgraded skill %s on agent %s/%s\n", name, ns, agent)
	fmt.Fprintf(out, "  from:     %s\n", old)
	fmt.Fprintf(out, "  to:       %s\n", next)
	return finish(ctx, out, c, a, timeout)
}

// removeSkill removes the skill from the Agent.
func removeSkill(ctx context.Context, out io.Writer, c client.Client, ns, agent, name string, timeout time.Duration) error {
	a, err := changeSkills(ctx, c, ns, agent, func(a *unstructured.Unstructured, entries []any) ([]any, error) {
		i, err := find(agent, entries, name)
		if err != nil {
			return nil, err
		}
		v := versions(a)
		delete(v, name)
		return append(entries[:i], entries[i+1:]...), setVersions(a, v)
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "Removed skill %s from agent %s/%s\n", name, ns, agent)
	return finish(ctx, out, c, a, timeout)
}

// finish waits until the cluster runs the changed Agent.
func finish(ctx context.Context, out io.Writer, c client.Client, a *unstructured.Unstructured, timeout time.Duration) error {
	if err := waitReady(ctx, c, a, timeout, skillStays(a.GetName())); err != nil {
		return err
	}
	fmt.Fprintln(out, "  ready")
	return nil
}

// skillTarget is the project and the Agent a skill command acts on.
type skillTarget struct {
	c         client.Client
	ns, agent string
}

// target resolves --agent as airo chat does: the one named, the project's
// only one, the one marked as default, or the one the person picks.
func target(ctx context.Context, cmd *cobra.Command, flags *genericclioptions.ConfigFlags, agent string) (skillTarget, error) {
	ns, _, err := flags.ToRawKubeConfigLoader().Namespace()
	if err != nil {
		return skillTarget{}, err
	}
	cfg, err := flags.ToRESTConfig()
	if err != nil {
		return skillTarget{}, err
	}
	c, err := client.New(cfg, client.Options{})
	if err != nil {
		return skillTarget{}, err
	}
	var ask func([]string) (string, error)
	errOut := cmd.ErrOrStderr()
	if ui.Interactive(cmd.InOrStdin(), cmd.OutOrStdout()) && ui.IsTerminal(errOut) {
		ask = func(names []string) (string, error) {
			name, err := picker.One(ctx, cmd.InOrStdin(), errOut, "agent", names, -1)
			if err == nil && name == "" {
				err = errors.New("no agent chosen: name one with --agent")
			}
			return name, err
		}
	}
	agent, err = pick(ctx, c, ns, agent, ask)
	return skillTarget{c: c, ns: ns, agent: agent}, err
}

func newSkillCommand(flags *genericclioptions.ConfigFlags) *cobra.Command {
	var agent string
	cmd := &cobra.Command{
		Use:     "skill",
		Aliases: []string{"skills"},
		Short:   "Manage the skills of an agent",
		Long: `Skill lists, adds, upgrades and removes the skills of an Agent.

A skill's source is a Git repository or an OCI image:

  https://github.com/OWNER/REPO/tree/REF/PATH   a GitHub page
  https://HOST/REPO[//PATH][@VERSION]           any Git host
  oci://IMAGE[:TAG|@sha256:DIGEST][//PATH]      an image

NAME=SOURCE names the skill. The name is otherwise the last directory of
the path, or the repository's. The cluster fetches skills without
credentials, so a repository must be public.

The Agent holds only immutable versions: airo pins a branch or a tag to its
commit, and an image tag to its digest. Upgrade pins the skill again, at the
version it was added with, or at --version.

Without --agent, airo takes the project's only agent, then the one with the
annotation ` + defaultAnnotation + `: "true", then asks.`,
		Example: `  airo agent skill add https://github.com/anthropics/skills/tree/main/skills/pdf -a reviewer
  airo agent skill add docs=https://git.example.com/team/skills//docs@v1.2
  airo agent skill list
  airo agent skill upgrade pdf
  airo agent skill upgrade docs --version v1.3
  airo agent skill remove pdf`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return cmd.Help()
			}
			return fmt.Errorf("unknown skill verb %q: see airo agent skill --help for verbs", args[0])
		},
	}
	cmd.PersistentFlags().StringVarP(&agent, "agent", "a", "", "name of the Agent (default: the project's only one, or its default)")

	// run resolves the Agent, then runs the verb until Ctrl-C.
	run := func(cmd *cobra.Command, verb func(ctx context.Context, t skillTarget) error) error {
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		t, err := target(ctx, cmd, flags, agent)
		if err != nil {
			return err
		}
		return verb(ctx, t)
	}

	list := &cobra.Command{
		Use:   "list",
		Short: "List the agent's skills",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return run(cmd, func(ctx context.Context, t skillTarget) error {
				return listSkills(ctx, cmd.OutOrStdout(), cmd.ErrOrStderr(), t.c, t.ns, t.agent)
			})
		},
	}
	add := &cobra.Command{
		Use:   "add SOURCE",
		Short: "Add a skill to the agent, pinned to its current version",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// A source airo cannot read fails before the cluster is asked.
			if _, err := skill.Parse(args[0]); err != nil {
				return err
			}
			return run(cmd, func(ctx context.Context, t skillTarget) error {
				return addSkill(ctx, cmd.OutOrStdout(), t.c, t.ns, t.agent, args[0], readyTimeout)
			})
		},
	}
	var version string
	upgrade := &cobra.Command{
		Use:   "upgrade NAME",
		Short: "Pin a skill to the newest commit of its version, or to --version",
		Long: `Upgrade pins the skill again. Without --version it resolves the version the
skill was added with: a branch moves to its newest commit, and a skill added
without a version follows the default branch, or the image tag latest. A
tag or a commit stays where it is. --version picks another branch, tag,
commit, image tag or digest, and later upgrades follow it.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return run(cmd, func(ctx context.Context, t skillTarget) error {
				return upgradeSkill(ctx, cmd.OutOrStdout(), t.c, t.ns, t.agent, args[0], version, readyTimeout)
			})
		},
	}
	upgrade.Flags().StringVar(&version, "version", "", "the branch, tag, commit, image tag or digest to pin the skill to")
	remove := &cobra.Command{
		Use:   "remove NAME",
		Short: "Remove a skill from the agent",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return run(cmd, func(ctx context.Context, t skillTarget) error {
				return removeSkill(ctx, cmd.OutOrStdout(), t.c, t.ns, t.agent, args[0], readyTimeout)
			})
		},
	}
	cmd.AddCommand(list, add, upgrade, remove)
	return cmd
}
