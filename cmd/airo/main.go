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
	root := &cobra.Command{
		Use:   "airo [resource] [verb] [options]",
		Short: "Work with the Airo platform from your machine",
		Long: `Work with the Airo platform from your machine.

Use a resource's singular or plural name. Every resource kind in the cluster
supports list, get and delete.`,
		Example: `  airo login                     # See 'airo login -h' for first-time setup
  airo project list              # List the namespaces you can access
  airo agent list                # List agents in the current project
  airo agent get my-agent        # Inspect an agent from the list
  airo chat                      # Chat with the default agent`,
		Version:      buildVersion(),
		SilenceUsage: true,
	}
	flags.AddFlags(root.PersistentFlags())
	generic.AddResourceHandler(root, flags)
	root.SetUsageTemplate(usageTemplate)
	addGroup := func(id, title string, commands ...*cobra.Command) {
		root.AddGroup(&cobra.Group{ID: id, Title: title})
		for _, cmd := range commands {
			cmd.GroupID = id
		}
		root.AddCommand(commands...)
	}
	addGroup("resource", "Resource Commands:",
		agent.NewCommand(flags),
		session.NewCommand(flags),
		login.NewTenantCommand(flags),
		tunnel.NewCommand(flags),
	)
	addGroup("auth", "Authentication Commands:",
		login.NewCommand(flags),
		login.NewWhoamiCommand(flags),
	)
	addGroup("config", "Configuration Commands:",
		login.NewContextCommand(flags),
		login.NewProjectCommand(flags),
		login.NewStatusCommand(flags),
	)
	addGroup("advanced", "Advanced Commands:", generic.NewApplyCommand(flags))
	root.SetCompletionCommandGroupID("advanced")
	addGroup("other", "Other Commands:",
		agent.NewChatCommand(flags),
		newOptionsCommand(),
		&cobra.Command{
			Use:   "version",
			Short: "Print airo's version",
			Args:  cobra.NoArgs,
			Run: func(cmd *cobra.Command, _ []string) {
				fmt.Fprintln(cmd.OutOrStdout(), "airo version "+buildVersion())
			},
		},
	)
	root.SetHelpCommandGroupID("other")
	root.AddCommand(login.NewCredentialCommand())
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
