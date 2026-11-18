package main

import "github.com/spf13/cobra"

// The root groups commands and shows the resource grammar. Subcommands keep
// their own usage, aliases and flags. Global flags live under airo options.
const usageTemplate = `Usage:{{if .HasParent}}{{if .Runnable}}
  {{.UseLine}}{{end}}{{if .HasAvailableSubCommands}}
  {{.CommandPath}} [command]{{end}}{{else}}
  {{.Use}}{{end}}{{with .Aliases}}

Aliases:
  {{$.NameAndAliases}}{{end}}{{with .Example}}

Examples:
{{.}}{{end}}{{if .HasAvailableSubCommands}}{{if .Groups}}{{range .Groups}}{{$group := .}}

{{.Title}}{{range $.Commands}}{{if and (eq .GroupID $group.ID) (or .IsAvailableCommand (eq .Name "help"))}}
  {{rpad .Name .NamePadding}} {{.Short}}{{end}}{{end}}{{end}}{{else}}

Available Commands:{{range .Commands}}{{if .IsAvailableCommand}}
  {{rpad .Name .NamePadding}} {{.Short}}{{end}}{{end}}{{end}}{{end}}{{$flags := .LocalFlags}}{{if not .HasParent}}{{$flags = .LocalNonPersistentFlags}}{{end}}{{if $flags.HasAvailableFlags}}

Flags:
{{$flags.FlagUsages | trimTrailingWhitespaces}}{{end}}

{{if .HasAvailableSubCommands}}Use "{{.CommandPath}} [command] --help" for more information about a command.
{{end}}Use "airo options" to see global command-line options.
`

func newOptionsCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "options",
		Short: "Show global command-line options",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	cmd.SetHelpTemplate(`Global options for all airo commands:

{{.InheritedFlags.FlagUsages}}`)
	return cmd
}
