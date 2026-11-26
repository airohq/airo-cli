package login

import (
	"github.com/spf13/cobra"
	"k8s.io/cli-runtime/pkg/genericclioptions"

	"github.com/airohq/airo-cli/internal/generic"
)

// SetUpCompletion makes shell completion choose its context and project as a
// command does, and completes the flags every command has: --project,
// --tenant and --context.
func SetUpCompletion(root *cobra.Command, flags *genericclioptions.ConfigFlags, selection *Selection) {
	generic.SetCompletionScope(selection.CompletionScope)
	generic.CompleteFlag(root, "project", completeProjects(flags, selection))
	generic.CompleteFlag(root, "tenant", completeTenants(flags))
	generic.CompleteFlag(root, "context", completeContexts(flags))
}

// completeProjects lists the projects of the selected tenant, as airo project
// list does.
func completeProjects(flags *genericclioptions.ConfigFlags, selection *Selection) func(string) []string {
	return generic.Names(flags, projectResource.GroupResource().String(), func() string {
		return tenantSelector(selection.Tenant)
	})
}

func completeTenants(flags *genericclioptions.ConfigFlags) func(string) []string {
	return generic.Names(flags, tenantResource.GroupResource().String(), nil)
}

// completeContexts lists the sign-ins in the kubeconfig, as airo context list
// does. It needs no network.
func completeContexts(flags *genericclioptions.ConfigFlags) func(string) []string {
	return func(prefix string) []string {
		cfg, err := flags.ToRawKubeConfigLoader().ConfigAccess().GetStartingConfig()
		if err != nil {
			return nil
		}
		return generic.WithPrefix(signIns(cfg), prefix)
	}
}
