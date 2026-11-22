package agent

import (
	"flag"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/airohq/kagent-remote-harness/runner/supervisor"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/airohq/airo-cli/internal/ui"
)

type localSandbox struct {
	group          ui.FlagGroup
	defaultHarness string
	harnesses      map[string]string
}

const (
	defaultCodexSandbox         = "read-only"
	defaultClaudePermissionMode = "plan"
)

var localSandboxes = map[string]localSandbox{
	"container": {
		group:          ui.FlagGroup{Heading: "Container Sandbox Flags", Names: []string{"harness", "idle-timeout", "image"}},
		defaultHarness: "codex",
		harnesses: map[string]string{
			"codex": `Codex Harness Args:
  Default: --sandbox ` + defaultCodexSandbox + `

  Override the default or pass additional arguments after --.
    -- --sandbox workspace-write`,
			"claude": `Claude Harness Args:
  Default: --permission-mode ` + defaultClaudePermissionMode + `

  Override the default or pass additional arguments after --.
    -- --permission-mode acceptEdits`,
		},
	},
}

func bindLocalFlags(cmd *cobra.Command, opts *supervisor.Options) {
	settings := flag.NewFlagSet("runner", flag.ContinueOnError)
	opts.BindFlags(settings)
	flags := cmd.Flags()
	flags.BoolVarP(&opts.Verbose, "verbose", "v", false, settings.Lookup("verbose").Usage)
	for _, name := range []string{"sandbox", "turn-timeout", "runner"} {
		flags.AddGoFlag(settings.Lookup(name))
	}
	_ = flags.MarkHidden("runner")
	for _, sandbox := range localSandboxes {
		for _, name := range sandbox.group.Names {
			flags.AddGoFlag(settings.Lookup(name))
			_ = flags.MarkHidden(name)
		}
	}
	// The help command can render these defaults without parsing any flags.
	if _, err := selectLocalFlags(cmd, opts.Backend, opts.Harness); err != nil {
		panic(err)
	}
	cmd.Annotations["helpHint"] = "Pass --sandbox and/or --harness with --help to see flags for that combination."
}

// selectLocalFlags applies the same scope to help and to explicit flag validation.
func selectLocalFlags(cmd *cobra.Command, backend, harness string) (string, error) {
	sandbox, ok := localSandboxes[backend]
	if !ok {
		names := slices.Sorted(maps.Keys(localSandboxes))
		return "", fmt.Errorf("unknown sandbox %q (available: %s)", backend, strings.Join(names, ", "))
	}
	if harness == "" {
		harness = sandbox.defaultHarness
	}
	names := strings.Join(slices.Sorted(maps.Keys(sandbox.harnesses)), ", ")
	help, ok := sandbox.harnesses[harness]
	if !ok {
		return "", fmt.Errorf("unknown harness %q for sandbox %q (available: %s)", harness, backend, names)
	}
	cmd.Flags().Lookup("harness").Usage = "coding agent (" + names + ")"
	cmd.Flags().Lookup("harness").DefValue = sandbox.defaultHarness
	ui.SetFlagGroups(cmd, sandbox.group)
	cmd.Annotations["harnessHelp"] = help
	return harness, nil
}

func parseLocalFlags(cmd *cobra.Command, args []string, opts *supervisor.Options) ([]string, error) {
	flags := cmd.Flags()
	flags.AddFlagSet(cmd.InheritedFlags())
	if err := flags.Parse(args); err != nil {
		return nil, err
	}
	positionals := flags.Args()
	if dash := flags.ArgsLenAtDash(); dash >= 0 {
		opts.HarnessArgs = positionals[dash:]
		positionals = positionals[:dash]
	}
	var err error
	opts.Harness, err = selectLocalFlags(cmd, opts.Backend, opts.Harness)
	if err != nil {
		return nil, err
	}
	var invalid error
	flags.Visit(func(f *pflag.Flag) {
		if invalid == nil && f.Name != "runner" && f.Hidden && cmd.LocalFlags().Lookup(f.Name) != nil {
			invalid = fmt.Errorf("unknown flag: --%s for sandbox %q", f.Name, opts.Backend)
		}
	})
	if invalid != nil {
		return nil, invalid
	}
	if help, _ := flags.GetBool("help"); help {
		return nil, pflag.ErrHelp
	}
	switch opts.Harness {
	case "codex":
		opts.CodexSandbox = defaultCodexSandbox
	case "claude":
		opts.ClaudePermissionMode = defaultClaudePermissionMode
	}
	_, err = opts.Adapter()
	return positionals, err
}
