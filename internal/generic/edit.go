package generic

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/cli-runtime/pkg/genericiooptions"
	cmdutil "k8s.io/kubectl/pkg/cmd/util"
	"k8s.io/kubectl/pkg/cmd/util/editor"
)

// editFieldManager names airo as the owner of the fields an edit changes,
// as kubectl edit names itself kubectl-edit.
const editFieldManager = "airo-edit"

// NewEditCommand edits one named resource in the person's editor.
func NewEditCommand(flags *genericclioptions.ConfigFlags, resource string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "edit NAME",
		Short: "Edit one named resource in your editor",
		Long:  editLong,
		Example: fmt.Sprintf(`  airo %[1]s edit NAME
  airo %[1]s edit NAME -o json
  EDITOR=nano airo %[1]s edit NAME`, resource),
		Args: cobra.ExactArgs(1),

		ValidArgsFunction: FirstArg(Names(flags, resource, nil)),
	}
	run := editRunner(cmd, flags)
	cmd.RunE = func(_ *cobra.Command, args []string) error {
		return run(resource, args[0])
	}
	return cmd
}

const editLong = `Edit opens the resource as YAML in the editor that KUBE_EDITOR or EDITOR
names, or vi, and saves the changes when the editor closes, as kubectl edit
does. Closing the editor without changes changes nothing. When the cluster
refuses a change, the editor opens again with the reason at the top. If the
resource changed meanwhile, the changes stay in a file to apply later.`

// editRunner runs kubectl's own edit, so that airo edits as kubectl does.
func editRunner(cmd *cobra.Command, flags *genericclioptions.ConfigFlags) func(string, string) error {
	o := editor.NewEditOptions(editor.NormalEditMode, genericiooptions.IOStreams{})
	cmdutil.AddValidateFlags(cmd)
	cmd.Flags().Lookup("validate").Usage = "check the changed resource against its schema: strict (or true), warn, or ignore (or false)"
	cmd.Flags().BoolVar(&o.OutputPatch, "output-patch", false, "print the patch when the resource changed")
	cmd.Flags().BoolVar(&o.WindowsLineEndings, "windows-line-endings", o.WindowsLineEndings, "edit with Windows line endings; the default is the platform's")
	cmd.Flags().StringVar(&o.FieldManager, "field-manager", editFieldManager, "name of the manager that owns the changed fields")
	if cmd.Flags().Lookup("output") == nil {
		cmd.Flags().StringP("output", "o", "", "edit as yaml or json")
	}

	return func(resource, name string) error {
		// The root command parses flags for several verbs. Refuse misplaced
		// read flags, such as selectors, before changing anything.
		local := cmd.LocalNonPersistentFlags()
		var flagErr error
		cmd.Flags().Visit(func(f *pflag.Flag) {
			if local.Lookup(f.Name) != nil {
				switch f.Name {
				case "output", "validate", "output-patch", "windows-line-endings", "field-manager":
				default:
					flagErr = fmt.Errorf("--%s is not supported by edit NAME", f.Name)
				}
			}
		})
		if flagErr != nil {
			return flagErr
		}
		output, err := cmd.Flags().GetString("output")
		if err != nil {
			return err
		}
		switch output {
		case "", "yaml", "json":
		default:
			return fmt.Errorf("edit takes -o yaml or -o json, not %q", output)
		}
		o.PrintFlags.OutputFormat = &output
		o.IOStreams = genericiooptions.IOStreams{In: cmd.InOrStdin(), Out: cmd.OutOrStdout(), ErrOut: cmd.ErrOrStderr()}
		// kubectl's resource/name form selects exactly one object.
		if err := o.Complete(cmdutil.NewFactory(flags), []string{resource + "/" + name}, cmd); err != nil {
			return err
		}
		if err := o.Validate(); err != nil {
			return err
		}
		return o.Run()
	}
}
