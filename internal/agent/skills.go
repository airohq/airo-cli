package agent

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"slices"
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

var resolver = skill.Resolver{Client: &http.Client{Timeout: 30 * time.Second}}

// pin resolves a skill to an immutable version, and skillsIn lists the
// skills of a source. Tests replace them.
var (
	pin      = resolver.Pin
	skillsIn = resolver.Skills
)

// pinSkills reads and pins the sources of --skill.
func pinSkills(ctx context.Context, args []string) ([]skill.Pinned, error) {
	var pinned []skill.Pinned
	for _, arg := range args {
		p, err := parseAndPin(ctx, arg, pinned, nil)
		if err != nil {
			return nil, err
		}
		pinned = append(pinned, p)
	}
	return pinned, nil
}

// parseAndPin reads and pins a source. For a source of several skills it
// asks for one, when ask is not nil.
func parseAndPin(ctx context.Context, arg string, others []skill.Pinned, ask func([]string) (string, error)) (skill.Pinned, error) {
	s, err := skill.Parse(arg)
	if err != nil {
		return skill.Pinned{}, err
	}
	p, err := pinChosen(ctx, s, ask)
	if err != nil {
		return skill.Pinned{}, err
	}
	return p, sameNames(append(others, p))
}

// pinChosen pins a skill. For a source of several skills it asks for one,
// when ask is not nil, and returns the *skill.Choice otherwise.
func pinChosen(ctx context.Context, s skill.Skill, ask func([]string) (string, error)) (skill.Pinned, error) {
	p, err := pin(ctx, s)
	var choice *skill.Choice
	if ask == nil || !errors.As(err, &choice) {
		return p, err
	}
	if s.Find, err = ask(choice.Names); err != nil {
		return p, err
	}
	return pin(ctx, s)
}

// sameNames returns an error when two of the skills have one name.
func sameNames(pinned []skill.Pinned) error {
	seen := map[string]bool{}
	for _, p := range pinned {
		if seen[p.Name] {
			return fmt.Errorf("two skills are named %s: name one with NAME=SOURCE", p.Name)
		}
		seen[p.Name] = true
	}
	return nil
}

// addOptions are the flags of add that choose skills, as npx skills add
// takes them.
type addOptions struct {
	// skills are names of skills in the source, or * for all.
	skills []string
	// all adds every skill of the source.
	all bool
	// rename names the one skill added.
	rename string
}

// pinForAdd pins the skills of a source that add adds: the ones o names,
// all of them, or the one the source names. For a source of several skills
// it asks for one, when ask is not nil.
func pinForAdd(ctx context.Context, s skill.Skill, o addOptions, ask func([]string) (string, error)) ([]skill.Pinned, error) {
	if o.rename != "" {
		if err := skill.ValidName(o.rename); err != nil {
			return nil, err
		}
		s.Name = o.rename
	}
	names := o.skills
	if s.Find != "" {
		names = append([]string{s.Find}, names...)
	}
	all := o.all || slices.Contains(names, "*")
	var pinned []skill.Pinned
	switch {
	case !all && len(names) <= 1:
		if len(names) == 1 {
			s.Find = names[0]
		}
		p, err := pinChosen(ctx, s, ask)
		var choice *skill.Choice
		if errors.As(err, &choice) {
			return nil, fmt.Errorf("%s holds %d skills: choose one with --skill NAME, or add them all with --all: %s", choice.Source, len(choice.Names), strings.Join(choice.Names, ", "))
		}
		if err != nil {
			return nil, err
		}
		pinned = append(pinned, p)
	case s.Name != "":
		return nil, errors.New("a name given with NAME=SOURCE or --rename names one skill: add the others one by one")
	default:
		// One listing finds them all, since GitHub limits how often airo
		// may list a repository.
		s.Find = ""
		at, found, err := skillsIn(ctx, s)
		if err != nil {
			return nil, err
		}
		chosen := found
		if !all {
			chosen = nil
			for _, name := range names {
				i := slices.IndexFunc(found, func(f skill.Found) bool { return skill.SameName(f.Name, name) })
				if i < 0 {
					return nil, fmt.Errorf("%s has no skill named %s", at.Location(), name)
				}
				chosen = append(chosen, found[i])
			}
		}
		if len(chosen) == 0 {
			return nil, fmt.Errorf("%s has no SKILL.md", at.Location())
		}
		for _, f := range chosen {
			one := s
			one.Path = f.Path
			if skill.ValidName(f.Name) == nil {
				one.Name = f.Name
			}
			p, err := pin(ctx, one)
			if err != nil {
				return nil, err
			}
			pinned = append(pinned, p)
		}
	}
	return pinned, sameNames(pinned)
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
		p, err := parseAndPin(ctx, answer, pinned, func(names []string) (string, error) { return w.Pick("Skill", names) })
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

// addSkill pins the skills of a source and adds them to the Agent.
func addSkill(ctx context.Context, out io.Writer, c client.Client, ns, agent, arg string, o addOptions, timeout time.Duration) error {
	s, err := skill.Parse(arg)
	if err != nil {
		return err
	}
	pinned, err := pinForAdd(ctx, s, o, nil)
	if err != nil {
		return err
	}
	return addPinned(ctx, out, c, ns, agent, pinned, timeout)
}

// askSkills asks for a skill source until its skills pin.
func askSource(ctx context.Context, w *wizard.Wizard, o addOptions) ([]skill.Pinned, error) {
	for {
		answer, err := w.Required("Skill source")
		if err != nil {
			return nil, err
		}
		s, err := skill.Parse(answer)
		if err != nil {
			w.Problem(err)
			continue
		}
		pinned, err := pinForAdd(ctx, s, o, func(names []string) (string, error) { return w.Pick("Skill", names) })
		if err != nil {
			w.Problem(err)
			continue
		}
		for _, p := range pinned {
			w.Note(fmt.Sprintf("  %s at %s", p.Name, p.Pin()))
		}
		return pinned, nil
	}
}

// addPinned adds pinned skills to the Agent.
func addPinned(ctx context.Context, out io.Writer, c client.Client, ns, agent string, pinned []skill.Pinned, timeout time.Duration) error {
	a, err := changeSkills(ctx, c, ns, agent, func(a *unstructured.Unstructured, entries []any) ([]any, error) {
		v := versions(a)
		for _, p := range pinned {
			for _, entry := range entries {
				if entryName(entry) == p.Name {
					return nil, fmt.Errorf("agent %s has a skill named %s: upgrade it with airo agent skill upgrade %s --agent %s, or name the new one with NAME=SOURCE", agent, p.Name, p.Name, agent)
				}
			}
			v[p.Name] = p.Version
			entries = append(entries, skillEntry(p))
		}
		return entries, setVersions(a, v)
	})
	if err != nil {
		return err
	}
	for _, p := range pinned {
		fmt.Fprintf(out, "Added skill %s to agent %s/%s\n", p.Name, ns, agent)
		fmt.Fprintf(out, "  source:   %s\n", p)
	}
	return finish(ctx, out, c, a, timeout)
}

// listSource prints the skills a source holds, as npx skills add --list.
func listSource(ctx context.Context, out, errOut io.Writer, arg string) error {
	s, err := skill.Parse(arg)
	if err != nil {
		return err
	}
	at, found, err := skillsIn(ctx, s)
	if err != nil {
		return err
	}
	if len(found) == 0 {
		return fmt.Errorf("%s has no SKILL.md", at.Location())
	}
	fmt.Fprintf(errOut, "Skills in %s at %s\n", at.Location(), at.Commit)
	tw := tabwriter.NewWriter(out, 0, 0, 3, ' ', 0)
	fmt.Fprintln(tw, "NAME\tPATH")
	for _, f := range found {
		fmt.Fprintf(tw, "%s\t%s\n", f.Name, cmp.Or(f.Path, "."))
	}
	return tw.Flush()
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

// pickToChange returns the Agent a skill command changes: the one named, or
// else the one ask returns. Unlike airo chat, it takes no default, not even
// the project's only agent, because the command changes the Agent. ask is
// nil when there is nobody to ask.
func pickToChange(ctx context.Context, c client.Client, ns, name string, ask func([]string) (string, error)) (string, error) {
	if name != "" {
		return name, nil
	}
	names, err := agentNames(ctx, c, ns)
	if err != nil {
		return "", err
	}
	if ask != nil {
		return ask(names)
	}
	return "", fmt.Errorf("name the agent to change with --agent: project %s has %s", ns, strings.Join(names, ", "))
}

// agentNames returns the names of the project's Agents, sorted, or an error
// when it has none.
func agentNames(ctx context.Context, c client.Client, ns string) ([]string, error) {
	items, err := list(ctx, c, ns, agents)
	if err != nil {
		return nil, err
	}
	var names []string
	for i := range items {
		names = append(names, items[i].GetName())
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("project %s has no agent: make one with airo agent create", ns)
	}
	sort.Strings(names)
	return names, nil
}

// connect returns a client and the project of the kubeconfig.
func connect(flags *genericclioptions.ConfigFlags) (client.Client, string, error) {
	ns, _, err := flags.ToRawKubeConfigLoader().Namespace()
	if err != nil {
		return nil, "", err
	}
	cfg, err := flags.ToRESTConfig()
	if err != nil {
		return nil, "", err
	}
	c, err := client.New(cfg, client.Options{})
	return c, ns, err
}

// target resolves --agent: the one named, or the one the person picks.
func target(ctx context.Context, cmd *cobra.Command, flags *genericclioptions.ConfigFlags, agent string) (skillTarget, error) {
	c, ns, err := connect(flags)
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
	agent, err = pickToChange(ctx, c, ns, agent, ask)
	return skillTarget{c: c, ns: ns, agent: agent}, err
}

// addWizard asks for the source of a skill, then for the Agent unless agent
// names it, and adds the skill.
func addWizard(cmd *cobra.Command, flags *genericclioptions.ConfigFlags, agent string, o addOptions) error {
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	c, ns, err := connect(flags)
	if err != nil {
		return err
	}
	header := fmt.Sprintf("Adding a skill to an agent in project %s", ui.Text(ns))
	if agent != "" {
		header = fmt.Sprintf("Adding a skill to agent %s/%s", ui.Text(ns), ui.Text(agent))
	}
	w := wizard.WithHeader(cmd, header)
	if w == nil {
		return errors.New("provide SOURCE as an argument when running without a terminal")
	}
	var names []string
	if agent == "" {
		// A project without agents fails before anything is asked.
		if names, err = agentNames(ctx, c, ns); err != nil {
			return err
		}
	}
	pinned, err := askSource(ctx, w, o)
	if err != nil {
		return err
	}
	if agent == "" {
		if agent, err = w.Pick("Agent", names); err != nil {
			return err
		}
	}
	return addPinned(ctx, cmd.OutOrStdout(), c, ns, agent, pinned, readyTimeout)
}

func newSkillCommand(flags *genericclioptions.ConfigFlags) *cobra.Command {
	var agent string
	cmd := &cobra.Command{
		Use:     "skill",
		Aliases: []string{"skills"},
		Short:   "Manage the skills of an agent",
		Long: `Skill lists, adds, upgrades and removes the skills of an Agent.

A skill's source is a Git repository or an OCI image:

  OWNER/REPO[@SKILL]                            a GitHub repository
  https://github.com/OWNER/REPO/tree/REF/PATH   a GitHub page
  https://HOST/REPO[//PATH][@VERSION]           any Git host
  oci://IMAGE[:TAG|@sha256:DIGEST][//PATH]      an image

Add takes the sources and flags of npx skills add, so a command from a
skill's page works with npx replaced by airo agent:

  npx skills add anthropics/skills@frontend-design
  airo agent skills add anthropics/skills@frontend-design

In a GitHub repository, airo finds a skill by the name in its SKILL.md, and
asks which one to add when the source holds several. NAME=SOURCE or
--rename names the skill in the Agent. The name is otherwise the one in its
SKILL.md, or its directory's. The cluster fetches skills without
credentials, so a repository must be public.

The Agent holds only immutable versions: airo pins a branch or a tag to its
commit, and an image tag to its digest. Upgrade pins the skill again, at the
version it was added with, or at --version.

Every verb acts on the Agent named with --agent (-a). Unlike airo chat, it
has no default agent, because the verbs change the Agent: without --agent,
airo asks on a terminal and fails elsewhere.`,
		Example: `  airo agent skill add anthropics/skills@frontend-design --agent reviewer
  airo agent skill add https://github.com/anthropics/skills --skill pdf --skill docx -a reviewer
  airo agent skill add https://github.com/anthropics/skills/tree/main/skills/pdf -a reviewer
  airo agent skill add docs=https://git.example.com/team/skills//docs@v1.2 -a reviewer
  airo agent skill list -a reviewer
  airo agent skill upgrade pdf -a reviewer
  airo agent skill upgrade docs --version v1.3 -a reviewer
  airo agent skill remove pdf -a reviewer`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return cmd.Help()
			}
			return fmt.Errorf("unknown skill verb %q: see airo agent skill --help for verbs", args[0])
		},
	}

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
		Use:     "list --agent NAME",
		Aliases: []string{"ls"},
		Short:   "List the agent's skills",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return run(cmd, func(ctx context.Context, t skillTarget) error {
				return listSkills(ctx, cmd.OutOrStdout(), cmd.ErrOrStderr(), t.c, t.ns, t.agent)
			})
		},
	}
	var o addOptions
	var listOnly bool
	add := &cobra.Command{
		Use:     "add SOURCE --agent NAME",
		Aliases: []string{"a"},
		Short:   "Add a skill to the agent, pinned to its current version",
		Long: `Add pins a skill's source to an immutable version and adds it to the
Agent. It takes the sources and flags of npx skills add:

  OWNER/REPO                 a GitHub repository: airo asks which skill
  OWNER/REPO@SKILL           the skill named SKILL in its SKILL.md
  OWNER/REPO@v1.2.3          a repository at a version, or a full commit
  OWNER/REPO/PATH            the skill, or skills, under PATH
  OWNER/REPO#REF[@SKILL]     at a branch, a tag or a commit
  github:OWNER/REPO, gitlab:OWNER/REPO, git@HOST:OWNER/REPO

A URL takes #REF[@SKILL] too. airo finds a skill by name only in a GitHub
repository. On another host, give the skill's directory. A local directory
does not work, because the cluster fetches the skill.

Without SOURCE, airo asks for the source on a terminal, and then for the
agent unless --agent names it.`,
		Example: `  airo agent skills add anthropics/skills@frontend-design -a reviewer
  airo agent skills add https://github.com/openai/skills --skill security-best-practices -a reviewer
  airo agent skills add anthropics/skills --skill pdf --skill docx -a reviewer
  airo agent skills add anthropics/skills --list`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if listOnly {
				if len(args) == 0 {
					return errors.New("give the SOURCE to list the skills of")
				}
				ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
				defer stop()
				return listSource(ctx, cmd.OutOrStdout(), cmd.ErrOrStderr(), args[0])
			}
			if len(args) == 0 {
				return addWizard(cmd, flags, agent, o)
			}
			s, err := skill.Parse(args[0])
			if err != nil {
				return err
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			// The skills pin before the agent is asked for, so that a
			// source airo cannot use fails first.
			var ask func([]string) (string, error)
			if ui.Interactive(cmd.InOrStdin(), cmd.OutOrStdout()) && ui.IsTerminal(cmd.ErrOrStderr()) {
				ask = func(names []string) (string, error) {
					name, err := picker.One(ctx, cmd.InOrStdin(), cmd.ErrOrStderr(), "skill", names, -1)
					if err == nil && name == "" {
						err = errors.New("no skill chosen: name one with --skill")
					}
					return name, err
				}
			}
			pinned, err := pinForAdd(ctx, s, o, ask)
			if err != nil {
				return err
			}
			t, err := target(ctx, cmd, flags, agent)
			if err != nil {
				return err
			}
			return addPinned(ctx, cmd.OutOrStdout(), t.c, t.ns, t.agent, pinned, readyTimeout)
		},
	}
	add.Flags().StringArrayVar(&o.skills, "skill", nil, "the name of a skill in the source to add, or '*' for all; repeat for more")
	add.Flags().BoolVar(&o.all, "all", false, "add every skill of the source")
	add.Flags().StringVar(&o.rename, "rename", "", "the name of the skill in the agent")
	add.Flags().BoolVarP(&listOnly, "list", "l", false, "list the skills of the source, and add none")
	// npx skills add -y skips its questions. airo asks only for what is
	// missing, so a command copied with -y works as it is.
	add.Flags().BoolP("yes", "y", false, "")
	_ = add.Flags().MarkHidden("yes")
	var version string
	upgrade := &cobra.Command{
		Use:     "upgrade SKILL --agent NAME",
		Aliases: []string{"update"},
		Short:   "Pin a skill to the newest commit of its version, or to --version",
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
		Use:     "remove SKILL --agent NAME",
		Aliases: []string{"rm"},
		Short:   "Remove a skill from the agent",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return run(cmd, func(ctx context.Context, t skillTarget) error {
				return removeSkill(ctx, cmd.OutOrStdout(), t.c, t.ns, t.agent, args[0], readyTimeout)
			})
		},
	}
	// Each verb holds --agent itself, so that its help lists the flag.
	for _, verb := range []*cobra.Command{list, add, upgrade, remove} {
		verb.Flags().StringVarP(&agent, "agent", "a", "", "name of the Agent to change; asked on a terminal when not given")
	}
	cmd.AddCommand(list, add, upgrade, remove)
	return cmd
}
