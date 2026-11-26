package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
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
				"Configuration Commands:", "  project ", "  status ", "  tenant ",
				"Advanced Commands:", "  apply ", "  completion ",
				"Other Commands:", "  chat ", "  help ", "  options ", "  version ", "airo options"},
			// Like kubectl's, the root's help lists no flags.
			[]string{"--kubeconfig", "modelconfig", "credential", "airo [command]\n", "Flags:", "--output", "-h, --help"},
		},
		{
			[]string{"agents", "list", "-h"},
			[]string{"airo agent list", "--all-namespaces", "--output", "airo options"},
			[]string{"--kubeconfig"},
		},
		{
			[]string{"agent", "serve", "-h"},
			[]string{"Basic Flags:", "--help", "--name", "--sandbox", "--turn-timeout", "-v, --verbose",
				"Container Sandbox Flags:", "--harness", "--idle-timeout", "--image", "--workspace",
				"Codex Harness Args:", "Default: --sandbox danger-full-access", "Codex's own sandbox is off", "-- -c model_reasoning_effort=high", "airo options", "Pass --sandbox and/or --harness with --help"},
			[]string{"\nFlags:\n", "--runner", "--kubeconfig", "Claude Harness Args:", "--permission-mode bypassPermissions"},
		},
		{
			[]string{"agent", "connect", "-h", "--harness=claude", "--sandbox", "container"},
			[]string{"Basic Flags:", "--sandbox", "--verbose", "Container Sandbox Flags:", "--harness", "--workspace",
				"Claude Harness Args:", "Default: --permission-mode bypassPermissions", "Claude asks no permissions", "-- --permission-mode plan", "airo options", "Pass --sandbox and/or --harness with --help"},
			[]string{"--name", "--runner", "--kubeconfig", "Codex Harness Args:", "--sandbox danger-full-access"},
		},
		{
			[]string{"agent", "serve", "--sandbox", "container", "--harness", "claude", "--help"},
			[]string{"Basic Flags:", "Container Sandbox Flags:", "Claude Harness Args:", "--permission-mode bypassPermissions"},
			[]string{"Codex Harness Args:", "--sandbox danger-full-access"},
		},
		{
			[]string{"agent", "connect", "--help"},
			[]string{"Basic Flags:", "Container Sandbox Flags:", "Codex Harness Args:", "--sandbox danger-full-access"},
			[]string{"Claude Harness Args:", "--permission-mode bypassPermissions"},
		},
		// The verbs every kind has come first, then the noun's own groups.
		{
			[]string{"agent", "-h"},
			[]string{"Basic Commands:", "  delete ", "  describe ", "  edit ", "  get ", "  list ",
				"Agent Commands:", "  create ", "  skill ", "  update ",
				"Local Run Commands:", "  connect ", "  serve "},
			[]string{"Available Commands:"},
		},
		{
			[]string{"tunnel", "-h"},
			[]string{"Basic Commands:", "  delete ", "  list ", "Tunnel Commands:", "  create "},
			[]string{"Available Commands:"},
		},
		{
			[]string{"project", "-h"},
			[]string{"Basic Commands:", "  delete ", "Delete a project", "  edit ", "  list ", "Project Commands:", "  create "},
			[]string{"Available Commands:"},
		},
		{
			[]string{"tenant", "-h"},
			[]string{"Basic Commands:", "  delete ", "  list "},
			[]string{"Available Commands:"},
		},
		// A noun that is not a kind has no basic verbs.
		{
			[]string{"session", "-h"},
			[]string{"Available Commands:", "  cancel ", "  list ", "  view "},
			[]string{"Basic Commands:"},
		},
		{
			[]string{"options"},
			[]string{"--kubeconfig", "--project", "--tenant"},
			[]string{"--output", "--all-namespaces", "--namespace", "--context"},
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

// Every command that takes the name of something that exists, and every flag
// that names an Agent, a model, a harness or a selection, completes it on Tab.
func TestNamesComplete(t *testing.T) {
	var walk func(*cobra.Command)
	walk = func(cmd *cobra.Command) {
		if words := strings.Fields(cmd.Use); len(words) > 1 && cmd.Name() != "create" && cmd.ValidArgsFunction == nil {
			// NAME, [NAME] and [NAME|-] take a name, [--name NAME] does not.
			switch strings.Trim(words[1], "[]|-") {
			case "NAME", "SKILL", "SESSION":
				t.Errorf("%s takes a %s but does not complete it", cmd.CommandPath(), words[1])
			}
		}
		cmd.Flags().VisitAll(func(f *pflag.Flag) {
			switch f.Name {
			case "agent", "model", "harness", "sandbox", "project", "tenant", "context", "resume":
				if _, ok := cmd.GetFlagCompletionFunc(f.Name); !ok {
					t.Errorf("%s --%s does not complete", cmd.CommandPath(), f.Name)
				}
			}
		})
		for _, child := range cmd.Commands() {
			walk(child)
		}
	}
	walk(newRoot())
}

// The help shows only the commands of a group once a noun has groups, so
// a verb added without one would be missing from it.
func TestGroupedNounsGroupEveryVerb(t *testing.T) {
	var walk func(*cobra.Command)
	walk = func(cmd *cobra.Command) {
		for _, child := range cmd.Commands() {
			if len(cmd.Groups()) > 0 && child.IsAvailableCommand() && child.GroupID == "" {
				t.Errorf("%s has groups, but %s is in none: the help would leave it out", cmd.CommandPath(), child.Name())
			}
			walk(child)
		}
	}
	walk(newRoot())
}

// Commands behind a Tenant capability name it at the top of their help, and
// basic commands name none (DESIGN.md#capabilities-and-solutions).
func TestHelpNamesRequiredCapabilities(t *testing.T) {
	want := map[string]string{
		"airo agent":               "agents",
		"airo agent create":        "agents",
		"airo agent update":        "agents",
		"airo agent skill list":    "agents",
		"airo agent skill add":     "agents",
		"airo agent skill upgrade": "agents",
		"airo agent skill remove":  "agents",
		"airo agent serve":         "agents, tunnels, local-agents",
		"airo agent connect":       "agents, tunnels, local-agents",
		"airo chat":                "agents",
		"airo session":             "agents",
		"airo session list":        "agents",
		"airo session view":        "agents",
		"airo session cancel":      "agents",
		"airo tunnel":              "tunnels",
		"airo tunnel create":       "tunnels",
	}
	var walk func(*cobra.Command)
	walk = func(cmd *cobra.Command) {
		got := ""
		if line, _, _ := strings.Cut(cmd.Long, "\n"); strings.HasPrefix(line, "Required capabilities: ") {
			got = strings.TrimPrefix(line, "Required capabilities: ")
		}
		if got != want[cmd.CommandPath()] {
			t.Errorf("%s requires %q in its help, want %q", cmd.CommandPath(), got, want[cmd.CommandPath()])
		}
		for _, child := range cmd.Commands() {
			walk(child)
		}
	}
	walk(newRoot())
}

func TestLocalAgentFlagErrors(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"serve", "--claude-permission-mode=plan"}, "unknown flag: --claude-permission-mode"},
		{[]string{"connect", "reviewer", "--codex-sandbox=", "--harness", "claude"}, "unknown flag: --codex-sandbox"},
		{[]string{"serve", "--help", "--claude-permission-mode=plan"}, "unknown flag: --claude-permission-mode"},
		{[]string{"serve", "--harness=claude", "--", "--output-format=text"}, "--output-format is managed"},
		{[]string{"connect", "--sandbox=other", "--help"}, `unknown sandbox "other" (available: container)`},
		{[]string{"serve", "--harness=other", "--help"}, `unknown harness "other" for sandbox "container" (available: claude, codex)`},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			root := newRoot()
			root.SilenceErrors = true
			root.SetArgs(append([]string{"agent"}, tc.args...))
			if err := root.Execute(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %q", err, tc.want)
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
