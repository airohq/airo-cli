package generic

import (
	"context"
	"github.com/spf13/cobra"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/cli-runtime/pkg/genericiooptions"
	kubectlcreate "k8s.io/kubectl/pkg/cmd/create"
	cmdutil "k8s.io/kubectl/pkg/cmd/util"
)

// NewCreateCommand uses kubectl's manifest command for every kind served by
// the cluster. Resource-specific constructors belong to the noun commands.
func NewCreateCommand(flags *genericclioptions.ConfigFlags, checkProject func(context.Context, *genericclioptions.ConfigFlags) error) *cobra.Command {
	o := kubectlcreate.NewCreateOptions(genericiooptions.IOStreams{})
	cmd := &cobra.Command{
		Use:     "create -f FILE",
		Short:   "Create resources from a manifest",
		Example: "  airo create -f agent.yaml\n  cat resources.yaml | airo create -f -",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			o.IOStreams = genericiooptions.IOStreams{In: cmd.InOrStdin(), Out: cmd.OutOrStdout(), ErrOut: cmd.ErrOrStderr()}
			f := cmdutil.NewFactory(newManifestClient(cmd.Context(), flags, checkProject))
			if err := o.Complete(f, cmd, args); err != nil {
				return err
			}
			if err := o.Validate(); err != nil {
				return err
			}
			return o.RunCreate(f)
		},
	}
	cmdutil.AddFilenameOptionFlags(cmd, &o.FilenameOptions, "to create")
	cmdutil.AddValidateFlags(cmd)
	cmdutil.AddApplyAnnotationFlags(cmd)
	cmdutil.AddDryRunFlag(cmd)
	o.PrintFlags.AddFlags(cmd)
	return cmd
}
