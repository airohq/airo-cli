package main

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// offersNothing reports whether f is no completion or the one that offers
// nothing, so that a test can tell it from a real one.
func offersNothing(f cobra.CompletionFunc) bool {
	return f == nil || reflect.ValueOf(f).Pointer() == reflect.ValueOf(cobra.NoFileCompletions).Pointer()
}

// Tab offers files only where a path goes: every other argument and flag
// completes or offers nothing, as kubectl's do.
func TestTabOffersFilesOnlyForPaths(t *testing.T) {
	var walk func(*cobra.Command)
	walk = func(c *cobra.Command) {
		if c.ValidArgsFunction == nil && len(c.ValidArgs) == 0 {
			t.Errorf("%s: Tab offers files for an argument", c.CommandPath())
		}
		c.LocalNonPersistentFlags().VisitAll(func(f *pflag.Flag) {
			if f.Value.Type() == "bool" {
				return
			}
			_, completes := c.GetFlagCompletionFunc(f.Name)
			if _, path := pathFlags[f.Name]; path != takesPath(f) || (!path && !completes) {
				t.Errorf("%s --%s: Tab offers files: %v, want %v", c.CommandPath(), f.Name, takesPath(f), path)
			}
		})
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(newRoot())

	for _, tt := range []struct {
		args []string
		want string
	}{
		{[]string{"agent", "create", ""}, ":4\n"},                       // a new name: nothing, no files
		{[]string{"agent", "create", "x", "--prompt", ""}, ":4\n"},      // free text
		{[]string{"agent", "create", "x", "--prompt-file", ""}, ":0\n"}, // a file
		{[]string{"agent", "serve", "--workspace", ""}, ":16\n"},        // a directory
		{[]string{"chat", ""}, ":4\n"},
		{[]string{"agent", "create", "--dry-run="}, "server\nclient\nnone\n:4\n"},
		// serve and connect parse their own flags, and complete them too.
		{[]string{"agent", "serve", "--dry-run="}, "--dry-run=server\n--dry-run=client\n--dry-run=none\n:4\n"},
		{[]string{"agent", "serve", "--sandbox", ""}, "container\n:4\n"},
		{[]string{"agent", "serve", "--name", ""}, ":4\n"},
		{[]string{"agent", "serve", "--work"}, "--workspace\n:4\n"},
		{[]string{"agent", "connect", "--workspace", ".", "--", ""}, ":4\n"},
	} {
		root := newRoot()
		var out bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&bytes.Buffer{})
		root.SetArgs(append([]string{cobra.ShellCompNoDescRequestCmd}, tt.args...))
		if err := root.Execute(); err != nil {
			t.Fatal(err)
		}
		if !strings.HasSuffix(out.String(), tt.want) {
			t.Errorf("airo %v<TAB> = %q, want the directive %q", tt.args, out.String(), tt.want)
		}
	}
}
