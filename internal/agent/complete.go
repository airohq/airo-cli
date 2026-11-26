package agent

import (
	"context"
	"strings"

	"github.com/spf13/cobra"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"sigs.k8s.io/controller-runtime/pkg/client"

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

// completeSkills completes a skill of the Agent that --agent names, or of the
// project's only Agent. With several Agents and no --agent it offers nothing,
// and the shell beeps: a skill of one agent would not exist in another one
// that --agent names later.
func completeSkills(flags *genericclioptions.ConfigFlags) cobra.CompletionFunc {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
		if len(args) > 0 || !generic.PrepareCompletion(flags) {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		c, ns, err := connect(flags)
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		ctx, cancel := context.WithTimeout(context.Background(), generic.CompletionTimeout)
		defer cancel()
		name, _ := cmd.Flags().GetString("agent")
		return skillChoices(ctx, c, ns, name, toComplete), cobra.ShellCompDirectiveNoFileComp
	}
}

// skillChoices lists the skills of the Agent name, or of the project's only
// Agent when name is empty, that start with prefix.
func skillChoices(ctx context.Context, c client.Client, ns, name, prefix string) []string {
	if name == "" {
		names, err := agentNames(ctx, c, ns)
		if err != nil || len(names) != 1 {
			return nil
		}
		name = names[0]
	}
	a, err := getAgent(ctx, c, ns, name)
	if err != nil {
		return nil
	}
	entries, err := inlineSkills(a)
	if err != nil {
		return nil
	}
	var skills []string
	for _, entry := range entries {
		skills = append(skills, entryName(entry))
	}
	return generic.WithPrefix(skills, prefix)
}

// completeAgentRefs completes the flags that name what an Agent refers to.
func completeAgentRefs(cmd *cobra.Command, flags *genericclioptions.ConfigFlags) {
	generic.CompleteFlag(cmd, "model", completeNames(flags, models))
	generic.CompleteFlag(cmd, "harness", completeNames(flags, harnesses))
}
