package agent

import (
	"strings"

	"github.com/spf13/cobra"
	"k8s.io/cli-runtime/pkg/genericclioptions"

	"github.com/airohq/airo-cli/internal/generic"
)

// completeNames lists the names of k in the selected project, for a shell
// completion.
func completeNames(flags *genericclioptions.ConfigFlags, k choice) func(string) []string {
	return generic.Names(flags, strings.ToLower(k.gvk.Kind)+"."+k.gvk.Group, nil)
}

// CompleteAgents lists the selected project's agents for a shell completion,
// as --agent takes them.
func CompleteAgents(flags *genericclioptions.ConfigFlags) func(string) []string {
	return completeNames(flags, agents)
}

// completeAgentRefs completes the flags that name what an Agent refers to.
func completeAgentRefs(cmd *cobra.Command, flags *genericclioptions.ConfigFlags) {
	generic.CompleteFlag(cmd, "model", completeNames(flags, models))
	generic.CompleteFlag(cmd, "harness", completeNames(flags, harnesses))
}
