package login

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/airohq/airo-cli/internal/dryrun"
	"github.com/airohq/airo-cli/internal/generic"
	"github.com/airohq/airo-cli/internal/picker"
	"github.com/airohq/airo-cli/internal/ui"
)

// A project is a Namespace. The platform's API lists the ones the person
// may work in.
var projectResource = schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}

// projectLister asks the cluster for the person's projects.
type projectLister func(context.Context) (*unstructured.UnstructuredList, error)

// projectWriter creates one project in the cluster.
type projectWriter func(ctx context.Context, name string) error

// NewProjectCommand returns the project command.
func NewProjectCommand(flags *genericclioptions.ConfigFlags, selection *Selection) *cobra.Command {
	access := func() clientcmd.ConfigAccess { return flags.ToRawKubeConfigLoader().ConfigAccess() }
	namespaces := func() (dynamic.ResourceInterface, string, error) {
		cfg, err := flags.ToRESTConfig()
		if err != nil {
			return nil, "", err
		}
		c, err := dynamic.NewForConfig(cfg)
		if err != nil {
			return nil, "", err
		}
		return c.Resource(projectResource), cfg.Host, nil
	}
	list := func(ctx context.Context) (*unstructured.UnstructuredList, error) {
		r, host, err := namespaces()
		if err != nil {
			return nil, err
		}
		projects, err := r.List(ctx, metav1.ListOptions{LabelSelector: tenantSelector(selection.Tenant)})
		if apierrors.IsForbidden(err) {
			return nil, fmt.Errorf("the platform's API at %s does not list your projects: %w", host, err)
		}
		return projects, err
	}
	var dry *dryrun.Options
	// send creates the project with c, which records it in a dry run.
	send := func(ctx context.Context, c client.Client, name string) error {
		ns := &unstructured.Unstructured{}
		ns.SetGroupVersionKind(projectResource.GroupVersion().WithKind("Namespace"))
		ns.SetName(name)
		if selection.Tenant != "" {
			ns.SetLabels(map[string]string{tenantLabel: selection.Tenant})
		}
		err := c.Create(ctx, ns)
		if selection.Tenant == "" && apierrors.IsForbidden(err) && strings.Contains(err.Error(), tenantLabel) {
			return fmt.Errorf("%w: choose the owner with --tenant NAME", err)
		}
		return err
	}
	newClient := func() (client.Client, error) {
		cfg, err := flags.ToRESTConfig()
		if err != nil {
			return nil, err
		}
		c, err := client.New(cfg, client.Options{})
		return dry.Client(c), err
	}
	create := func(ctx context.Context, name string) error {
		c, err := newClient()
		if err != nil {
			return err
		}
		return send(ctx, c, name)
	}
	// The project airo login chooses at a first sign-in.
	defaultOf := func(ctx context.Context) (string, error) {
		if selection.Tenant != "" {
			return selection.Tenant + defaultProjectSuffix, nil
		}
		cfg, err := flags.ToRESTConfig()
		if err != nil {
			return "", err
		}
		me, err := currentUser(ctx, cfg)
		if err != nil {
			return "", err
		}
		return defaultProject(me.Groups), nil
	}

	cmd := &cobra.Command{
		Use:     "project [NAME|-]",
		Aliases: []string{"projects"},
		Short:   "Switch between your projects",
		Long: `Project makes another project the one your commands work in, for every
later command of airo and kubectl. Each tenant remembers its project.

Without a name, choose a project from a list. "-" switches back to the
previous project. Pass --project NAME to another command for a one-time choice.`,
		Example: `  airo project                # choose from a picker
  airo project acme-web       # switch to the project acme-web
  airo project -              # switch back to the previous one
  airo project list`,
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: generic.FirstArg(completeProjects(flags, selection)),
		RunE: func(cmd *cobra.Command, args []string) error {
			target := *flags.Namespace
			if len(args) == 1 {
				if target != "" && args[0] != target {
					return fmt.Errorf("choose one project: got %s and %s", args[0], target)
				}
				target = args[0]
			}
			var ask func([]string, int) (string, error)
			if ui.Interactive(cmd.InOrStdin(), cmd.OutOrStdout()) && ui.IsTerminal(cmd.ErrOrStderr()) {
				ask = func(names []string, current int) (string, error) {
					return picker.One(cmd.Context(), cmd.InOrStdin(), cmd.ErrOrStderr(), "project", names, current)
				}
			}
			return switchProject(cmd.Context(), cmd.OutOrStdout(), access(), *flags.Context, newPreviousProjectStore, list, target, ask, selection.Tenant)
		},
	}
	// Keep kubectl's server-side table and output formats. Combine the
	// tenant filter with any selector supplied by the caller.
	checkNamed := func(cmd *cobra.Command, args []string) error {
		if selection.Tenant == "" {
			return nil
		}
		if len(args) == 0 {
			selector, _ := cmd.Flags().GetString("selector")
			if selector != "" {
				selector += ","
			}
			return cmd.Flags().Set("selector", selector+tenantSelector(selection.Tenant))
		}
		c, err := selectionClient(flags)
		if err != nil {
			return err
		}
		return requireProject(cmd.Context(), c, selection.Tenant, args[0])
	}
	for _, verb := range generic.ReadCommands(flags, projectResource.Resource) {
		verb.PreRunE = checkNamed
		generic.AddBasic(cmd, verb)
	}
	createCmd := &cobra.Command{
		Use:   "create NAME",
		Short: "Create a project",
		Long: `Create makes a new project, a namespace of the platform. The platform
decides which names you may take, such as ones that start with your
tenant's name, and how many projects you may have.`,
		Example: `  airo project create acme-web
  airo project acme-web       # then work in it`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if dry.Active() {
				// A dry run's project never appears, so nothing waits for it.
				c, err := newClient()
				if err != nil {
					return err
				}
				if err := send(cmd.Context(), c, args[0]); err != nil {
					return err
				}
				_, err = dryrun.Finish(c)
				return err
			}
			switchTo := "airo project " + args[0]
			if cmd.Flags().Changed("tenant") {
				switchTo = "airo tenant " + selection.Tenant + " --project " + args[0]
			}
			return createProject(cmd.Context(), cmd.OutOrStdout(), args[0], create, list, time.Minute, switchTo)
		},
	}
	dry = dryrun.AddFlags(createCmd)
	ui.AddCommandGroup(cmd, "project", "Project Commands", createCmd)
	deleteCmd := generic.NewDeleteCommand(flags, projectResource.Resource, func(cmd *cobra.Command, name string) error {
		contextName := *flags.Context
		if selection.Tenant != "" {
			cfg, err := access().GetStartingConfig()
			if err != nil {
				return err
			}
			contextName, _, err = tenantContext(cfg, selectedContext(cfg, flags), selection.Tenant)
			if err != nil {
				return err
			}
		}
		return switchAfterProjectDeletion(cmd.Context(), cmd.ErrOrStderr(), access(), contextName, name, newPreviousProjectStore, list, defaultOf)
	})
	deleteCmd.PreRunE = checkNamed
	deleteCmd.Short = "Delete a project and everything in it"
	deleteCmd.Long = `Delete removes a project and everything in it. When your commands work in
it, airo switches them to the project you were in before, or else to your
default project, as airo login chooses it.`
	editCmd := generic.NewEditCommand(flags, projectResource.Resource)
	editCmd.PreRunE = checkNamed
	editCmd.Example = "  airo project edit acme-web"
	generic.AddBasic(cmd, editCmd, deleteCmd)
	// Complete the tenant's projects, as list shows them.
	for _, verb := range cmd.Commands() {
		if verb.ValidArgsFunction != nil {
			verb.ValidArgsFunction = generic.FirstArg(completeProjects(flags, selection))
		}
	}
	return cmd
}

// createProject creates the project name.
func createProject(ctx context.Context, out io.Writer, name string, create projectWriter, list projectLister, wait time.Duration, switchTo string) error {
	err := create(ctx, name)
	if apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("a project or namespace named %s exists already", name)
	}
	if err != nil {
		return fmt.Errorf("could not create project %s: %w", name, err)
	}
	fmt.Fprintf(out, "Created project %s\n", name)
	// The platform's API lists a project only once the person's role in it
	// exists, a few seconds later. Until then airo project NAME refuses it.
	deadline := time.Now().Add(wait)
	for {
		if names, err := projectNames(ctx, list); err == nil && slices.Contains(names, name) {
			fmt.Fprintf(out, "  work in it: %s\n", switchTo)
			return nil
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			fmt.Fprintf(out, "  the platform does not list it yet: in a moment, work in it with %s\n", switchTo)
			return nil
		}
		select {
		case <-ctx.Done():
		case <-time.After(time.Second):
		}
	}
}

// switchAfterProjectDeletion moves a context off its deleted namespace.
// Prefer the previous project, then the default chosen at login.
func switchAfterProjectDeletion(ctx context.Context, out io.Writer, access clientcmd.ConfigAccess, kctxName, name string, storeFor func(string) (previousStore, error), list projectLister, fallback func(context.Context) (string, error)) error {
	cfg, err := access.GetStartingConfig()
	if err != nil {
		return nil
	}
	edit := configEdit{access, cfg.DeepCopy()}
	if kctxName == "" {
		kctxName = cfg.CurrentContext
	}
	kctx := cfg.Contexts[kctxName]
	if kctx == nil || kctx.Namespace != name {
		return nil
	}
	// The context worked in the deleted project: move it to the project it
	// was in before, or else to the one airo login would choose.
	stay := func() error {
		tenant, err := storedTenant(kctx)
		if err != nil {
			return err
		}
		if tenant != "" {
			kctx.Namespace = ""
			if err := edit.save(cfg); err != nil {
				return err
			}
			fmt.Fprintf(out, "  tenant %s has no project selected: choose one with airo project\n", tenant)
			return nil
		}
		fmt.Fprintln(out, "  your commands still work in it: choose another with airo project")
		return nil
	}
	if _, err := oidcOf(cfg, kctxName); err != nil {
		return stay()
	}
	names, err := projectNames(ctx, list)
	if err != nil {
		return stay()
	}
	usable := func(project string) bool { return project != "" && project != name && slices.Contains(names, project) }
	store, err := storeFor(kctxName)
	if err != nil {
		return stay()
	}
	target := store.get()
	if !usable(target) {
		if target, err = fallback(ctx); err != nil || !usable(target) {
			return stay()
		}
	}
	kctx.Namespace = target
	if err := edit.save(cfg); err != nil {
		return err
	}
	// The remembered project is used up, and "-" must not lead back to the
	// deleted one.
	_ = store.put("")
	fmt.Fprintf(out, "Switched to project %s\n", target)
	return nil
}

// switchProject makes target the namespace of the context name, or of the
// current context when name is empty. An empty target is chosen with ask, as
// in switchContext. The project must be one the cluster lists.
func switchProject(ctx context.Context, out io.Writer, access clientcmd.ConfigAccess, name string, storeFor func(string) (previousStore, error), list projectLister, target string, ask func(names []string, current int) (string, error), tenant string) error {
	cfg, err := access.GetStartingConfig()
	if err != nil {
		return err
	}
	edit := configEdit{access, cfg.DeepCopy()}
	if name == "" {
		name = cfg.CurrentContext
	}
	// Only a sign-in is changed: another cluster's context is not airo's.
	if _, err := oidcOf(cfg, name); err != nil {
		return fmt.Errorf("%w: sign in with airo login", contextError(name, err))
	}
	created := false
	if tenant != "" {
		name, created, err = tenantContext(cfg, name, tenant)
		if err != nil {
			return err
		}
	}
	kctx := cfg.Contexts[name]
	store, err := storeFor(name)
	if err != nil {
		return err
	}

	if target == previousArg {
		if target = store.get(); target == "" {
			return errors.New("this machine remembers no previous project: name one, or choose with airo project")
		}
	}
	names, err := projectNames(ctx, list)
	if err != nil {
		return err
	}
	if target == "" {
		if ask == nil {
			return fmt.Errorf("name the project to switch to: %s", strings.Join(names, ", "))
		}
		if target, err = ask(names, slices.Index(names, kctx.Namespace)); err != nil {
			return err
		}
		if target == "" {
			return nil
		}
	}
	if !slices.Contains(names, target) {
		return fmt.Errorf("you have no project %q: your projects are %s", target, strings.Join(names, ", "))
	}
	if target == kctx.Namespace && !created {
		fmt.Fprintf(out, "Already in project %s\n", target)
		return nil
	}

	previous := kctx.Namespace
	kctx.Namespace = target
	if err := edit.save(cfg); err != nil {
		return err
	}
	if previous != "" {
		if err := store.put(previous); err != nil {
			return fmt.Errorf("switched to project %s, but could not remember %s for airo project -: %w", target, previous, err)
		}
	}
	fmt.Fprintf(out, "Switched to project %s\n", target)
	if tenant != "" {
		fmt.Fprintf(out, "  tenant: %s\n", tenant)
	} else if name != cfg.CurrentContext {
		fmt.Fprintf(out, "  context: %s\n", name)
	}
	return nil
}

// projectNames returns the names of the person's projects, sorted.
func projectNames(ctx context.Context, list projectLister) ([]string, error) {
	projects, err := list(ctx)
	if err != nil {
		return nil, err
	}
	if len(projects.Items) == 0 {
		return nil, errors.New("you have no project: ask the platform's admin for access to one")
	}
	names := make([]string, len(projects.Items))
	for i, project := range projects.Items {
		names[i] = project.GetName()
	}
	sort.Strings(names)
	return names, nil
}

// newPreviousProjectStore remembers the project the person switched away
// from in one context, for airo project -. The file is named by the
// context's name, encoded, because that name can hold any character.
func newPreviousProjectStore(context string) (previousStore, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return previousStore{}, err
	}
	file := base64.RawURLEncoding.EncodeToString([]byte(context))
	return previousStore{path: filepath.Join(dir, "airo", "previous-project", file)}, nil
}
