package session

import (
	"context"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"k8s.io/cli-runtime/pkg/genericclioptions"

	"github.com/airohq/airo-cli/internal/agent"
	"github.com/airohq/airo-cli/internal/generic"
)

// descriptionWidth is how much of a session's first prompt its completion
// shows.
const descriptionWidth = 50

// CompleteSessions completes the ID of one of the person's sessions, newest
// first, with its first prompt as the description that zsh and fish show.
// It asks the agent that --agent names. Without --agent it asks every agent
// of the project, or with oneAgent, for a command that talks to one agent,
// only the project's only agent: a session with one agent would not exist
// with another that --agent names later. Offering nothing makes the shell
// beep.
func CompleteSessions(flags *genericclioptions.ConfigFlags, oneAgent bool) cobra.CompletionFunc {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
		const directive = cobra.ShellCompDirectiveNoFileComp | cobra.ShellCompDirectiveKeepOrder
		if len(args) > 0 || !generic.PrepareCompletion(flags) {
			return nil, directive
		}
		ctx, cancel := context.WithTimeout(context.Background(), generic.CompletionTimeout)
		defer cancel()
		api, err := agent.Open(ctx, flags)
		if err != nil {
			return nil, directive
		}
		name, _ := cmd.Flags().GetString("agent")
		return sessionChoices(ctx, api, name, oneAgent, toComplete), directive
	}
}

// sessionChoices lists the sessions whose IDs start with prefix as
// completions: ID, a tab, and the description.
func sessionChoices(ctx context.Context, api *agent.API, name string, oneAgent bool, prefix string) []string {
	names, err := api.Agents(ctx, name)
	if err != nil || (name == "" && oneAgent && len(names) != 1) {
		return nil
	}
	turns := make([][]agent.Turn, len(names))
	// An agent that does not answer leaves the sessions of the rest.
	each(ctx, names, func(i int, n string) (err error) {
		turns[i], err = api.Turns(ctx, n, "", false)
		return err
	})
	var sessions []Session
	for i, n := range names {
		sessions = append(sessions, group(n, turns[i])...)
	}
	sort.SliceStable(sessions, func(i, j int) bool { return sessions[i].LastActive.After(sessions[j].LastActive) })
	var choices []string
	for _, s := range sessions {
		if !strings.HasPrefix(s.ID, prefix) {
			continue
		}
		description := oneLine(s.FirstPrompt, descriptionWidth)
		if len(names) > 1 {
			description = s.Agent + ": " + description
		}
		choices = append(choices, s.ID+"\t"+description)
	}
	return choices
}
