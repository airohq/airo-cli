package ui

import "github.com/spf13/cobra"

// AddCommandGroup adds commands to cmd under a heading of their own in its
// help, such as "Basic Commands". Groups show in the order they are added.
func AddCommandGroup(cmd *cobra.Command, id, title string, commands ...*cobra.Command) {
	if !cmd.ContainsGroup(id) {
		cmd.AddGroup(&cobra.Group{ID: id, Title: title + ":"})
	}
	for _, c := range commands {
		c.GroupID = id
	}
	cmd.AddCommand(commands...)
}
