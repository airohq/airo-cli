package generic

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/cli-runtime/pkg/genericiooptions"
	kubectldelete "k8s.io/kubectl/pkg/cmd/delete"
	cmdutil "k8s.io/kubectl/pkg/cmd/util"
)

// NewDeleteCommand deletes one named resource. Projects also update the local
// context after a successful deletion.
func NewDeleteCommand(flags *genericclioptions.ConfigFlags, resource string, afterDelete func(*cobra.Command, string) error) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delete NAME",
		Short: "Delete one named resource",
		Args:  cobra.ExactArgs(1),

		ValidArgsFunction: FirstArg(Names(flags, resource, nil)),
	}
	run := deleteRunner(cmd, flags, afterDelete)
	cmd.RunE = func(_ *cobra.Command, args []string) error {
		return run(resource, args[0])
	}
	return cmd
}

func deleteRunner(cmd *cobra.Command, flags *genericclioptions.ConfigFlags, afterDelete func(*cobra.Command, string) error) func(string, string) error {
	defaults := kubectldelete.NewDeleteCommandFlags("")
	cmdutil.AddDryRunFlag(cmd)
	cmd.Flags().BoolVar(defaults.Wait, "wait", *defaults.Wait, "wait for the resource to be gone, including its finalizers")
	cmd.Flags().DurationVar(defaults.Timeout, "timeout", *defaults.Timeout, "maximum time to wait for deletion")
	if cmd.Flags().Lookup("output") == nil {
		cmd.Flags().StringP("output", "o", "", "use name for resource/name output")
	}

	return func(resource, name string) error {
		// The root command parses flags for several verbs. Refuse misplaced
		// read flags (especially selectors) before sending a DELETE.
		local := cmd.LocalNonPersistentFlags()
		var flagErr error
		cmd.Flags().Visit(func(f *pflag.Flag) {
			if local.Lookup(f.Name) != nil {
				switch f.Name {
				case "dry-run", "wait", "timeout", "output":
				default:
					flagErr = fmt.Errorf("--%s is not supported by delete NAME", f.Name)
				}
			}
		})
		if flagErr != nil {
			return flagErr
		}
		streams := genericiooptions.IOStreams{In: cmd.InOrStdin(), Out: cmd.OutOrStdout(), ErrOut: cmd.ErrOrStderr()}
		o, err := defaults.ToOptions(nil, streams)
		if err != nil {
			return err
		}
		o.Output, err = cmd.Flags().GetString("output")
		if err != nil {
			return err
		}
		f := cmdutil.NewFactory(flags)
		// kubectl's resource/name form selects one object and does not expand
		// categories or comma-separated resource types into multiple deletes.
		if err := o.Complete(f, []string{resource + "/" + name}, cmd); err != nil {
			return err
		}
		if err := o.Validate(); err != nil {
			return err
		}
		if err := o.RunDelete(f); err != nil {
			return err
		}
		if afterDelete != nil && o.DryRunStrategy == cmdutil.DryRunNone {
			return afterDelete(cmd, name)
		}
		return nil
	}
}
