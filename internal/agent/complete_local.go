package agent

import (
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// completeParsedFlags completes a command that parses its own flags, as
// serve and connect do: cobra then leaves the whole line to the arguments'
// completion. It completes flag names, a flag's value as the flag's own
// completion does, and the positional arguments with positional. After --
// come the harness's own arguments, which offer nothing.
func completeParsedFlags(positional cobra.CompletionFunc) cobra.CompletionFunc {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
		if slices.Contains(args, "--") {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		fs := cmd.Flags()
		takesValue := func(f *pflag.Flag) bool { return f != nil && f.NoOptDefVal == "" }
		// --flag=value, or the value after --flag.
		if name, value, ok := strings.Cut(strings.TrimPrefix(toComplete, "--"), "="); ok && strings.HasPrefix(toComplete, "--") {
			if f := fs.Lookup(name); f != nil {
				comps, d := flagValue(cmd, f, args, value)
				for i := range comps {
					comps[i] = "--" + name + "=" + comps[i]
				}
				return comps, d
			}
		}
		if n := len(args); n > 0 && strings.HasPrefix(args[n-1], "--") && !strings.Contains(args[n-1], "=") {
			if f := fs.Lookup(args[n-1][2:]); takesValue(f) {
				return flagValue(cmd, f, args, toComplete)
			}
		}
		if strings.HasPrefix(toComplete, "-") {
			var names []cobra.Completion
			fs.VisitAll(func(f *pflag.Flag) {
				if !f.Hidden && strings.HasPrefix("--"+f.Name, toComplete) {
					names = append(names, "--"+f.Name)
				}
			})
			return names, cobra.ShellCompDirectiveNoFileComp
		}
		var words []string
		for i := 0; i < len(args); i++ {
			if a := args[i]; strings.HasPrefix(a, "-") {
				if !strings.Contains(a, "=") && takesValue(fs.Lookup(strings.TrimLeft(a, "-"))) {
					i++
				}
				continue
			}
			words = append(words, args[i])
		}
		return positional(cmd, words, toComplete)
	}
}

// flagValue completes the value of f as cobra would: its completion, or
// the files or directories it is marked to take.
func flagValue(cmd *cobra.Command, f *pflag.Flag, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	if fn, ok := cmd.GetFlagCompletionFunc(f.Name); ok {
		return fn(cmd, args, toComplete)
	}
	if _, ok := f.Annotations[cobra.BashCompSubdirsInDir]; ok {
		return nil, cobra.ShellCompDirectiveFilterDirs
	}
	if _, ok := f.Annotations[cobra.BashCompFilenameExt]; ok {
		return nil, cobra.ShellCompDirectiveDefault
	}
	return nil, cobra.ShellCompDirectiveNoFileComp
}
