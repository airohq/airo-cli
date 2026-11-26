package main

import (
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/airohq/airo-cli/internal/generic"
)

// newRootFor returns the root command set to run args. A first word that
// is no command but names a kind an airo command owns runs that command:
// the short name a CRD gives, as airo ag list for airo agent list, and the
// kind's other names, such as agents.api.kagent.dev.
func newRootFor(args []string) *cobra.Command {
	args = expandKind(args)
	root := newRoot()
	root.SetArgs(args)
	return root
}

// expandKind replaces the first word of args with the command that owns the
// kind it names. Another root reads the flags, such as --context, so that
// the root that runs parses args only once; it is built first, because the
// root that runs sets how completions choose their scope.
func expandKind(args []string) []string {
	probe, flags := build()
	first := 0
	completing := len(args) > 0 && (args[0] == cobra.ShellCompRequestCmd || args[0] == cobra.ShellCompNoDescRequestCmd)
	if completing {
		first = 1
	}
	// Flags of the command the word names are no root's: skip them.
	probe.FParseErrWhitelist.UnknownFlags = true
	// Without the help flag, pflag fails on -h rather than skip it.
	probe.InitDefaultHelpFlag()
	if err := probe.ParseFlags(args[first:]); err != nil {
		return args
	}
	i := firstWord(probe.Flags(), args, first)
	// The word Tab completes is no kind yet.
	if i < 0 || (completing && i == len(args)-1) {
		return args
	}
	owner := generic.Owner(probe, flags, args[i], completing)
	if owner == nil {
		return args
	}
	return slices.Concat(args[:i], []string{owner.Name()}, args[i+1:])
}

// firstWord returns the index of the first argument from start that is no
// flag or flag value, or -1.
func firstWord(fs *pflag.FlagSet, args []string, start int) int {
	takesValue := func(f *pflag.Flag) bool { return f != nil && f.NoOptDefVal == "" }
	for i := start; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			return -1
		case strings.HasPrefix(a, "--"):
			if !strings.Contains(a, "=") && takesValue(fs.Lookup(a[2:])) {
				i++
			}
		case strings.HasPrefix(a, "-") && len(a) > 1:
			// -o yaml takes the next argument; -oyaml and -Av do not.
			if len(a) == 2 && takesValue(fs.ShorthandLookup(a[1:])) {
				i++
			}
		default:
			return i
		}
	}
	return -1
}
