// Package generic implements the verbs every kind has: list, get, describe, edit and delete,
// for any kind the cluster serves, and apply for files. It asks the cluster
// what a kind is and how to show it, so a new kind needs no new airo.
package generic

import (
	"fmt"

	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/cli-runtime/pkg/genericiooptions"
	"k8s.io/kubectl/pkg/cmd/describe"
	"k8s.io/kubectl/pkg/cmd/get"
	cmdutil "k8s.io/kubectl/pkg/cmd/util"

	"github.com/airohq/airo-cli/internal/ui"
)

// Verbs is the help line for a kind without a command of its own.
const Verbs = "list, get [NAME], describe NAME, edit NAME or delete NAME"

// AddResourceHandler handles kinds that do not have a dedicated Airo command.
func AddResourceHandler(cmd *cobra.Command, flags *genericclioptions.ConfigFlags) {
	o := addReadFlags(cmd)
	runDescribe := describeRunner(cmd, flags)
	runDelete := deleteRunner(cmd, flags, nil)
	runEdit := editRunner(cmd, flags)
	cmd.Args = cobra.ArbitraryArgs
	// airo KIND -h shows the kind's verbs, as airo agent -h shows its own,
	// not the help of airo.
	rootHelp := cmd.HelpFunc()
	cmd.SetHelpFunc(func(c *cobra.Command, args []string) {
		words := c.Flags().Args()
		if c != cmd || len(words) == 0 {
			rootHelp(c, args)
			return
		}
		if shown, err := showKindHelp(cmd, flags, words); !shown {
			fmt.Fprintf(c.ErrOrStderr(), "Error: %v\n\n", err)
			rootHelp(c, args)
		} else if err != nil {
			fmt.Fprintf(c.ErrOrStderr(), "Error: %v\n", err)
		}
	})
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 {
			return cmd.Help()
		}
		kind := args[0]
		if len(args) == 1 {
			// Without a verb, a kind shows its verbs, as airo agent does.
			_, err := showKindHelp(cmd, flags, args)
			return err
		}
		switch args[1] {
		case "list":
			if len(args) == 2 {
				return read(cmd, flags, o, []string{kind})
			}
		case "get":
			if len(args) <= 3 {
				return read(cmd, flags, o, append([]string{kind}, args[2:]...))
			}
		case "describe":
			if len(args) == 3 {
				return runDescribe(kind, args[2])
			}
		case "edit":
			if len(args) == 3 {
				return runEdit(kind, args[2])
			}
		case "delete":
			if len(args) == 3 {
				return runDelete(kind, args[2])
			}
		default:
			return fmt.Errorf("airo %s has no verb %q: use %s", kind, args[1], Verbs)
		}
		return fmt.Errorf("usage: airo %s %s", kind, Verbs)
	}
}

func addReadFlags(cmd *cobra.Command) *get.GetOptions {
	o := get.NewGetOptions("airo", genericiooptions.IOStreams{})
	o.PrintFlags.AddFlags(cmd)
	cmdutil.AddLabelSelectorFlagVar(cmd, &o.LabelSelector)
	cmd.Flags().StringVar(&o.FieldSelector, "field-selector", "", "filter by fields supported by the API")
	cmd.Flags().BoolVarP(&o.AllNamespaces, "all-namespaces", "A", false, "list across all namespaces")
	return o
}

// read passes kubectl the same resource and optional name as kubectl get.
func read(cmd *cobra.Command, flags *genericclioptions.ConfigFlags, o *get.GetOptions, args []string) error {
	o.IOStreams = genericiooptions.IOStreams{In: cmd.InOrStdin(), Out: cmd.OutOrStdout(), ErrOut: cmd.ErrOrStderr()}
	f := cmdutil.NewFactory(flags)
	if err := o.Complete(f, cmd, args); err != nil {
		return err
	}
	if err := o.Validate(); err != nil {
		return err
	}
	return o.Run(f, args)
}

// describeRunner uses kubectl's describers and its generic fallback for CRDs.
func describeRunner(cmd *cobra.Command, flags *genericclioptions.ConfigFlags) func(string, string) error {
	showEvents := cmd.Flags().Bool("show-events", true, "include related events when describing a resource")
	return func(resource, name string) error {
		streams := genericiooptions.IOStreams{In: cmd.InOrStdin(), Out: cmd.OutOrStdout(), ErrOut: cmd.ErrOrStderr()}
		d := describe.NewDescribeFlags(cmdutil.NewFactory(flags), streams)
		d.DescriberSettings.ShowEvents = *showEvents
		d.ShowEventsExplicitlySet = cmd.Flags().Changed("show-events")
		o, err := d.ToOptions("airo", []string{resource, name})
		if err != nil {
			return err
		}
		if err := o.Validate(); err != nil {
			return err
		}
		return o.Run()
	}
}

// ReadCommands adds the same list, get and describe operations to built-in nouns.
// A platform synonym such as project supplies its Kubernetes resource.
func ReadCommands(flags *genericclioptions.ConfigFlags, resource string) []*cobra.Command {
	newRead := func(use, short string, args cobra.PositionalArgs) *cobra.Command {
		cmd := &cobra.Command{Use: use, Short: short, Args: args}
		o := addReadFlags(cmd)
		cmd.RunE = func(cmd *cobra.Command, names []string) error {
			return read(cmd, flags, o, append([]string{resource}, names...))
		}
		return cmd
	}
	describeCmd := &cobra.Command{
		Use:   "describe NAME",
		Short: "Describe a resource and its related events",
		Args:  cobra.ExactArgs(1),
	}
	runDescribe := describeRunner(describeCmd, flags)
	describeCmd.RunE = func(_ *cobra.Command, args []string) error {
		return runDescribe(resource, args[0])
	}
	return []*cobra.Command{
		newRead("list", "List resources", cobra.NoArgs),
		newRead("get [NAME]", "Show one resource or list the collection", cobra.MaximumNArgs(1)),
		describeCmd,
	}
}

// Commands returns the resource verbs for a built-in noun.
func Commands(flags *genericclioptions.ConfigFlags, resource string) []*cobra.Command {
	return append(ReadCommands(flags, resource), NewEditCommand(flags, resource), NewDeleteCommand(flags, resource, nil))
}

// AddBasic adds the verbs every kind has to a noun under Basic Commands, as
// kubectl heads its own. A noun's own commands go in groups below them.
func AddBasic(noun *cobra.Command, verbs ...*cobra.Command) {
	ui.AddCommandGroup(noun, "basic", "Basic Commands", verbs...)
}

// mapping asks the cluster which kind the word means. It takes what kubectl
// takes: tunnel, tunnels, a short name, or tunnels.tunnels.airohq.dev.
func mapping(flags *genericclioptions.ConfigFlags, kind string) (*meta.RESTMapping, error) {
	mapper, err := flags.ToRESTMapper()
	if err != nil {
		return nil, err
	}
	// The word is resource.version.group or resource.group: try both.
	gvr, gr := schema.ParseResourceArg(kind)
	var res schema.GroupVersionResource
	err = &meta.NoResourceMatchError{}
	if gvr != nil {
		res, err = mapper.ResourceFor(*gvr)
	}
	if err != nil {
		res, err = mapper.ResourceFor(gr.WithVersion(""))
	}
	if meta.IsNoMatchError(err) {
		return nil, fmt.Errorf("%q is not an airo command or a kind in the cluster", kind)
	}
	if err != nil {
		return nil, err
	}
	gvk, err := mapper.KindFor(res)
	if err != nil {
		return nil, err
	}
	return mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
}
