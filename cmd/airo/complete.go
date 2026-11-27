package main

import (
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// pathFlags take a path: Tab offers files, or directories when true.
var pathFlags = map[string]bool{
	"filename":    false,
	"file":        false,
	"prompt-file": false,
	"template":    false, // kubectl's: a template, or a file that holds one
	"kustomize":   true,
	"workspace":   true,
}

// completeNoFiles makes Tab offer no files where no file goes: cobra offers
// the directory's files for an argument or a flag without a completion, as
// for a new name in agent create NAME, and kubectl offers nothing there. An
// argument or flag that completes keeps its completion; one that takes a
// path offers files or directories. kubectl's global flags, such as
// --kubeconfig, keep cobra's default, as in kubectl.
func completeNoFiles(c *cobra.Command) {
	if c.ValidArgsFunction == nil && len(c.ValidArgs) == 0 {
		c.ValidArgsFunction = cobra.NoFileCompletions
	}
	c.LocalNonPersistentFlags().VisitAll(func(f *pflag.Flag) {
		if f.Value.Type() == "bool" || takesPath(f) {
			return
		}
		if _, ok := c.GetFlagCompletionFunc(f.Name); ok {
			return
		}
		var err error
		if dir, ok := pathFlags[f.Name]; ok && dir {
			err = c.MarkFlagDirname(f.Name)
		} else if ok {
			err = c.MarkFlagFilename(f.Name)
		} else if f.Name == "dry-run" {
			err = c.RegisterFlagCompletionFunc(f.Name, cobra.FixedCompletions([]cobra.Completion{"server", "client", "none"}, cobra.ShellCompDirectiveNoFileComp))
		} else {
			err = c.RegisterFlagCompletionFunc(f.Name, cobra.NoFileCompletions)
		}
		if err != nil {
			panic(err)
		}
	})
	for _, sub := range c.Commands() {
		completeNoFiles(sub)
	}
}

// takesPath reports whether f is marked to complete files or directories.
func takesPath(f *pflag.Flag) bool {
	_, file := f.Annotations[cobra.BashCompFilenameExt]
	_, dir := f.Annotations[cobra.BashCompSubdirsInDir]
	return file || dir
}
