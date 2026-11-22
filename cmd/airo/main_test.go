package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestHelpAndGlobalOptions(t *testing.T) {
	for _, tt := range []struct {
		args []string
		want []string // in display order
		omit []string
	}{
		{
			[]string{"-h"},
			[]string{"Usage:\n  airo [resource] [verb] [options]\n", "  airo login", "  airo project list", "  airo agent list",
				"Resource Commands:", "  agent ", "  session ", "  tunnel ",
				"Authentication Commands:", "  login ", "  whoami ",
				"Configuration Commands:", "  context ", "  project ", "  status ",
				"Advanced Commands:", "  apply ", "  completion ",
				"Other Commands:", "  chat ", "  help ", "  options ", "  version ", "airo options"},
			[]string{"--kubeconfig", "modelconfig", "credential", "airo [command]\n"},
		},
		{
			[]string{"agents", "list", "-h"},
			[]string{"airo agent list", "--all-namespaces", "--output", "airo options"},
			[]string{"--kubeconfig"},
		},
		{
			[]string{"agent", "serve", "-h"},
			[]string{"Basic Flags:", "--help", "--name", "--turn-timeout", "-v, --verbose",
				"Sandbox Flags:", "--idle-timeout", "--image", "--sandbox",
				"Harness Flags:", "--claude-permission-mode", "--codex-sandbox", "--harness", "airo options"},
			[]string{"\nFlags:\n", "--runner", "--kubeconfig"},
		},
		{
			[]string{"agent", "connect", "-h"},
			[]string{"Basic Flags:", "--verbose", "Sandbox Flags:", "--sandbox",
				"Harness Flags:", "--harness", "airo options"},
			[]string{"\nFlags:\n", "--name", "--runner", "--kubeconfig"},
		},
		{
			[]string{"options"},
			[]string{"--context", "--kubeconfig", "--namespace"},
			[]string{"--output", "--all-namespaces"},
		},
	} {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			root := newRoot()
			var out bytes.Buffer
			root.SetOut(&out)
			root.SetArgs(tt.args)
			if err := root.Execute(); err != nil {
				t.Fatal(err)
			}
			rest := out.String()
			for _, want := range tt.want {
				var found bool
				_, rest, found = strings.Cut(rest, want)
				if !found {
					t.Fatalf("missing or out of order %q in:\n%s", want, &out)
				}
			}
			for _, omit := range tt.omit {
				if strings.Contains(out.String(), omit) {
					t.Errorf("unexpected %q in:\n%s", omit, &out)
				}
			}
		})
	}
}

func TestAgentWithoutVerbShowsHelp(t *testing.T) {
	for _, noun := range []string{"agent", "agents"} {
		root := newRoot()
		var out bytes.Buffer
		root.SetOut(&out)
		root.SetArgs([]string{noun})
		if err := root.Execute(); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "airo agent [command]") || !strings.Contains(out.String(), "airo chat") {
			t.Errorf("%s help: %s", noun, &out)
		}
	}
}

func TestAgentRejectsUnknownVerbs(t *testing.T) {
	for _, tt := range []struct {
		args []string
		want string
	}{
		{[]string{"agents", "unknown", "assistant"}, "airo chat"},
		{[]string{"agent", "fix", "the", "build"}, "airo chat"},
	} {
		root := newRoot()
		root.SetArgs(tt.args)
		root.SilenceErrors = true
		if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "unknown agent verb") || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%v: got %v, want unknown verb with hint %q", tt.args, err, tt.want)
		}
	}
}

// Every noun takes its plural (DESIGN.md), also a noun inside another, such
// as airo agent skill. A noun is a command with verbs below it.
func TestNounsTakeThePlural(t *testing.T) {
	root := newRoot()
	nouns := 0
	var check func(parent *cobra.Command, path []string)
	check = func(parent *cobra.Command, path []string) {
		for _, c := range parent.Commands() {
			if !c.HasSubCommands() {
				continue
			}
			nouns++
			plural := c.Name() + "s"
			if !c.HasAlias(plural) {
				t.Errorf("%s %s has no alias %s", parent.CommandPath(), c.Name(), plural)
				continue
			}
			args := append(append([]string{}, path...), plural)
			got, _, err := root.Find(append(args, "list"))
			if err != nil {
				t.Errorf("airo %v list: %v", args, err)
			} else if got.Parent() != c || got.Name() != "list" {
				t.Errorf("airo %v list ran %q, want the list of %s", args, got.CommandPath(), c.Name())
			}
			check(c, append(append([]string{}, path...), c.Name()))
		}
	}
	check(root, nil)
	if nouns == 0 {
		t.Fatal("found no noun to check")
	}
}
