// Command airo is the CLI of the Airo platform. Each command reads and writes
// Kubernetes resources as the kubeconfig's identity, like kubectl, and holds
// no state of its own. DESIGN.md has the grammar.
package main

import (
	"fmt"
	"os"
	"runtime/debug"

	"github.com/spf13/cobra"
	"k8s.io/cli-runtime/pkg/genericclioptions"

	"github.com/airohq/airo-cli/internal/agent"
	"github.com/airohq/airo-cli/internal/generic"
	"github.com/airohq/airo-cli/internal/login"
	"github.com/airohq/airo-cli/internal/session"
	"github.com/airohq/airo-cli/internal/tunnel"
)

// version is set at build time for a release (.goreleaser.yaml).
var version string

func main() {
	if err := newRoot().Execute(); err != nil {
		os.Exit(1)
	}
}

// newRoot returns the airo command with every command below it.
func newRoot() *cobra.Command {
	// kubectl's flags, such as -n, --context and --kubeconfig.
	flags := genericclioptions.NewConfigFlags(true)
	var o generic.Options
	root := &cobra.Command{
		Use:   "airo",
		Short: "Work with the Airo platform from your machine",
		Long: `Work with the Airo platform from your machine.

Commands are airo NOUN [VERB]. A noun works in the singular and the plural:
airo agent list and airo agents list are the same. Every kind of resource in
the cluster is a noun with the verbs ` + generic.Verbs + `,
also the kinds not listed below:

  airo modelconfig list
  airo modelconfig get my-model -o yaml
  airo modelconfig delete my-model`,
		Version:      buildVersion(),
		SilenceUsage: true,
		// A first word that is no command below is a kind in the cluster.
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return cmd.Help()
			}
			return generic.Run(cmd.Context(), cmd.OutOrStdout(), flags, o, args)
		},
	}
	flags.AddFlags(root.PersistentFlags())
	o.AddFlags(root)
	root.AddCommand(
		login.NewCommand(flags),
		login.NewContextCommand(flags),
		login.NewCredentialCommand(),
		generic.NewApplyCommand(flags),
		agent.NewCommand(flags),
		session.NewCommand(flags),
		tunnel.NewCommand(flags),
		&cobra.Command{
			Use:   "version",
			Short: "Print airo's version",
			Args:  cobra.NoArgs,
			Run: func(cmd *cobra.Command, _ []string) {
				fmt.Fprintln(cmd.OutOrStdout(), "airo version "+buildVersion())
			},
		},
	)
	return root
}

// buildVersion is the release's version, or what go install recorded: the
// module version, such as the pseudo-version of a commit on main.
func buildVersion() string {
	if version != "" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" {
		return info.Main.Version
	}
	return "dev"
}
