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
	"k8s.io/apimachinery/pkg/util/duration"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/cli-runtime/pkg/printers"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/airohq/airo-cli/internal/picker"
)

// A project is a Namespace. The platform's API lists the ones the person
// may work in.
var projectResource = schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}

// projectLister asks the cluster for the person's projects.
type projectLister func(context.Context) (*unstructured.UnstructuredList, error)

// NewProjectCommand returns the project command.
func NewProjectCommand(flags *genericclioptions.ConfigFlags) *cobra.Command {
	access := func() clientcmd.ConfigAccess { return flags.ToRawKubeConfigLoader().ConfigAccess() }
	list := func(ctx context.Context) (*unstructured.UnstructuredList, error) {
		cfg, err := flags.ToRESTConfig()
		if err != nil {
			return nil, err
		}
		c, err := dynamic.NewForConfig(cfg)
		if err != nil {
			return nil, err
		}
		projects, err := c.Resource(projectResource).List(ctx, metav1.ListOptions{})
		if apierrors.IsForbidden(err) {
			return nil, fmt.Errorf("the platform's API at %s does not list your projects: %w", cfg.Host, err)
		}
		return projects, err
	}

	cmd := &cobra.Command{
		Use:     "project [NAME|-]",
		Aliases: []string{"projects"},
		Short:   "Switch between your projects",
		Long: `Project makes another project the one your commands work in, for every
later command of airo and kubectl. A project is a namespace of the platform,
and the switch sets the namespace of your kubeconfig's context.

Without a name it shows a picker. Type to filter, and press Enter to choose
or Esc to leave. "-" switches back to the project you were in before.

It changes the current context, or the one --context names. For one command
in another project, pass -n to that command instead.`,
		Example: `  airo project                # choose from a picker
  airo project acme-web       # switch to the project acme-web
  airo project -              # switch back to the previous one
  airo project list`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			target := ""
			if len(args) == 1 {
				target = args[0]
			}
			var ask func([]string, int) (string, error)
			if picker.Terminal(cmd.InOrStdin()) {
				ask = func(names []string, current int) (string, error) {
					return picker.One(cmd.Context(), "project", names, current)
				}
			}
			return switchProject(cmd.Context(), cmd.OutOrStdout(), access(), *flags.Context, newPreviousProjectStore, list, target, ask)
		},
	}
	var output string
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List your projects",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return listProjects(cmd.Context(), cmd.OutOrStdout(), access(), *flags.Context, list, output, time.Now())
		},
	}
	listCmd.Flags().StringVarP(&output, "output", "o", "", "print the whole resources as yaml or json")
	cmd.AddCommand(listCmd)
	return cmd
}

// switchProject makes target the namespace of the context name, or of the
// current context when name is empty. An empty target is chosen with ask, as
// in switchContext. The project must be one the cluster lists.
func switchProject(ctx context.Context, out io.Writer, access clientcmd.ConfigAccess, name string, storeFor func(string) (previousStore, error), list projectLister, target string, ask func(names []string, current int) (string, error)) error {
	cfg, err := access.GetStartingConfig()
	if err != nil {
		return err
	}
	if name == "" {
		name = cfg.CurrentContext
	}
	// Only a sign-in is changed: another cluster's context is not airo's.
	if _, err := oidcOf(cfg, name); err != nil {
		return fmt.Errorf("%w: sign in with airo login", contextError(name, err))
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
	if target == kctx.Namespace {
		fmt.Fprintf(out, "Already in project %s\n", target)
		return nil
	}

	previous := kctx.Namespace
	kctx.Namespace = target
	if err := clientcmd.ModifyConfig(access, *cfg, true); err != nil {
		return err
	}
	if previous != "" {
		if err := store.put(previous); err != nil {
			return fmt.Errorf("switched to project %s, but could not remember %s for airo project -: %w", target, previous, err)
		}
	}
	fmt.Fprintf(out, "Switched to project %s\n", target)
	if name != cfg.CurrentContext {
		fmt.Fprintf(out, "  context: %s\n", name)
	}
	return nil
}

// listProjects prints the projects and marks the one the context name works
// in, or the current context when name is empty.
func listProjects(ctx context.Context, out io.Writer, access clientcmd.ConfigAccess, name string, list projectLister, output string, now time.Time) error {
	var p printers.ResourcePrinter
	switch output {
	case "":
	case "yaml":
		p = &printers.YAMLPrinter{}
	case "json":
		p = &printers.JSONPrinter{}
	default:
		return fmt.Errorf("-o %s is not supported: use yaml or json", output)
	}
	projects, err := list(ctx)
	if err != nil {
		return err
	}
	if p != nil {
		for i := range projects.Items {
			projects.Items[i].SetManagedFields(nil)
		}
		return p.PrintObj(projects, out)
	}
	if len(projects.Items) == 0 {
		fmt.Fprintln(out, "No projects")
		return nil
	}

	cfg, err := access.GetStartingConfig()
	if err != nil {
		return err
	}
	if name == "" {
		name = cfg.CurrentContext
	}
	current := ""
	if kctx := cfg.Contexts[name]; kctx != nil {
		current = kctx.Namespace
	}
	sort.Slice(projects.Items, func(i, j int) bool { return projects.Items[i].GetName() < projects.Items[j].GetName() })
	w := printers.GetNewTabWriter(out)
	fmt.Fprintln(w, "CURRENT\tNAME\tAGE")
	for _, project := range projects.Items {
		mark := ""
		if project.GetName() == current {
			mark = "*"
		}
		age := "<unknown>"
		if created := project.GetCreationTimestamp(); !created.IsZero() {
			age = duration.HumanDuration(now.Sub(created.Time))
		}
		fmt.Fprintf(w, "%s\t%s\t%s\n", mark, project.GetName(), age)
	}
	return w.Flush()
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
