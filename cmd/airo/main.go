// Command airo is the CLI of the Airo platform. Each command reads and writes
// Kubernetes resources as the kubeconfig's identity, like kubectl, and holds
// no state of its own. DESIGN.md has the grammar.
package main

import (
	"fmt"
	"os"
	"runtime/debug"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"k8s.io/cli-runtime/pkg/genericclioptions"

	"github.com/airohq/airo-cli/internal/agent"
	"github.com/airohq/airo-cli/internal/generic"
	"github.com/airohq/airo-cli/internal/login"
	"github.com/airohq/airo-cli/internal/session"
	"github.com/airohq/airo-cli/internal/tunnel"
	"github.com/airohq/airo-cli/internal/ui"
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
	// Scope is resolved before constructing the resource client.
	flags := genericclioptions.NewConfigFlags(false)
	root := &cobra.Command{
		Use:   "airo [resource] [verb] [options]",
		Short: "Work with the Airo platform from your machine",
		Long: `Work with the Airo platform from your machine.

Use a resource's singular or plural name. Every resource kind in the cluster
supports list, get, describe, edit and delete.`,
		Example: `  airo login                     # See 'airo login -h' for first-time setup
  airo project list              # List projects in your tenant
  airo agent list                # List agents in the current project
  airo agent get my-agent        # Inspect an agent from the list
  airo chat                      # Chat with the default agent`,
		Version:      buildVersion(),
		SilenceUsage: true,
	}
	var selection login.Selection
	root.SetGlobalNormalizationFunc(func(_ *pflag.FlagSet, name string) pflag.NormalizedName {
		if name == "namespace" {
			name = "project"
		}
		return pflag.NormalizedName(name)
	})
	flags.AddFlags(root.PersistentFlags())
	root.PersistentFlags().Lookup("project").Usage = "project for this command"
	root.PersistentFlags().StringVar(&selection.Tenant, "tenant", "", "tenant for this command")
	_ = root.PersistentFlags().MarkHidden("context")
	root.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		return selection.Resolve(cmd, args, flags)
	}
	generic.AddResourceHandler(root, flags)
	root.SetUsageTemplate(usageTemplate)
	addGroup := func(id, title string, commands ...*cobra.Command) {
		ui.AddCommandGroup(root, id, title, commands...)
	}
	addGroup("resource", "Resource Commands",
		agent.NewCommand(flags),
		session.NewCommand(flags),
		tunnel.NewCommand(flags),
	)
	addGroup("auth", "Authentication Commands",
		login.NewCommand(flags, &selection),
		login.NewWhoamiCommand(flags),
	)
	addGroup("config", "Configuration Commands",
		login.NewTenantCommand(flags, &selection),
		login.NewContextCommand(flags),
		login.NewProjectCommand(flags, &selection),
		login.NewStatusCommand(flags, &selection),
	)
	addGroup("advanced", "Advanced Commands", generic.NewApplyCommand(flags, selection.CheckProject), generic.NewCreateCommand(flags, selection.CheckProject))
	root.SetCompletionCommandGroupID("advanced")
	addGroup("other", "Other Commands",
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
