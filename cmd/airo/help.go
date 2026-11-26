package main

import (
	"github.com/spf13/cobra"

	"github.com/airohq/airo-cli/internal/ui"
)

func init() {
	cobra.AddTemplateFunc("flagSections", ui.FlagSections)
}

// The root groups commands and shows the resource grammar, and no flags, as
// kubectl's does: its own flags are the read and write flags of airo KIND
// VERB, which that verb's help shows. Subcommands keep their own usage,
// aliases and flags. Global flags live under airo options.
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
  {{rpad .Name .NamePadding}} {{.Short}}{{end}}{{end}}{{end}}{{end}}{{$flags := .LocalFlags}}{{if and .HasParent $flags.HasAvailableFlags}}{{range flagSections . $flags}}

{{.Name}}:
{{.FlagUsagesWrapped 80 | trimTrailingWhitespaces}}{{else}}

Flags:
{{$flags.FlagUsages | trimTrailingWhitespaces}}{{end}}{{end}}{{with index .Annotations "harnessHelp"}}

{{.}}{{end}}

{{if .HasAvailableSubCommands}}Use "{{.CommandPath}} [command] --help" for more information about a command.
{{end}}Use "airo options" to see global command-line options.{{with index .Annotations "helpHint"}}
{{.}}{{end}}
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
