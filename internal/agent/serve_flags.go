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
	harnesses      map[string]ui.FlagGroup
}

var localSandboxes = map[string]localSandbox{
	"container": {
		group:          ui.FlagGroup{Heading: "Container Sandbox Flags", Names: []string{"harness", "idle-timeout", "image"}},
		defaultHarness: "codex",
		harnesses: map[string]ui.FlagGroup{
			"codex":  {Heading: "Codex Harness Flags", Names: []string{"codex-sandbox"}},
			"claude": {Heading: "Claude Harness Flags", Names: []string{"claude-permission-mode"}},
		},
	},
}

func bindLocalFlags(cmd *cobra.Command, opts *supervisor.Options) *flag.FlagSet {
	settings := flag.NewFlagSet("runner", flag.ContinueOnError)
	opts.BindFlags(settings)
	flags := cmd.Flags()
	flags.BoolVarP(&opts.Verbose, "verbose", "v", false, settings.Lookup("verbose").Usage)
	for _, name := range []string{"sandbox", "turn-timeout", "runner"} {
		flags.AddGoFlag(settings.Lookup(name))
	}
	_ = flags.MarkHidden("runner")
	bind := func(group ui.FlagGroup) {
		for _, name := range group.Names {
			flags.AddGoFlag(settings.Lookup(name))
			_ = flags.MarkHidden(name)
		}
	}
	for _, sandbox := range localSandboxes {
		bind(sandbox.group)
		for _, harness := range sandbox.harnesses {
			bind(harness)
		}
	}
	flags.Lookup("codex-sandbox").DefValue = "read-only"
	flags.Lookup("claude-permission-mode").DefValue = "plan"
	// The help command can render these defaults without parsing any flags.
	if _, err := selectLocalFlags(cmd, opts.Backend, opts.Harness); err != nil {
		panic(err)
	}
	cmd.Annotations["helpHint"] = "Pass --sandbox and/or --harness with --help to see flags for that combination."
	return settings
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
	group, ok := sandbox.harnesses[harness]
	if !ok {
		return "", fmt.Errorf("unknown harness %q for sandbox %q (available: %s)", harness, backend, names)
	}
	cmd.Flags().Lookup("harness").Usage = "coding agent (" + names + ")"
	cmd.Flags().Lookup("harness").DefValue = sandbox.defaultHarness
	ui.SetFlagGroups(cmd, sandbox.group, group)
	return harness, nil
}

func parseLocalFlags(cmd *cobra.Command, args []string, settings *flag.FlagSet, opts *supervisor.Options) ([]string, error) {
	flags := cmd.Flags()
	flags.AddFlagSet(cmd.InheritedFlags())
	if err := flags.Parse(args); err != nil {
		return nil, err
	}
	positionals := flags.Args()
	var runnerArgs []string
	if dash := flags.ArgsLenAtDash(); dash >= 0 {
		runnerArgs = append(runnerArgs, positionals[dash:]...)
		positionals = positionals[:dash]
	}
	// Explicit CLI flags take precedence over legacy settings after --.
	flags.Visit(func(f *pflag.Flag) {
		if settings.Lookup(f.Name) != nil {
			runnerArgs = append(runnerArgs, "--"+f.Name+"="+f.Value.String())
		}
	})
	settings.SetOutput(cmd.ErrOrStderr())
	if err := settings.Parse(runnerArgs); err != nil {
		return nil, err
	}
	if settings.NArg() != 0 {
		return nil, fmt.Errorf("unexpected runner argument %q", settings.Arg(0))
	}
	var err error
	opts.Harness, err = selectLocalFlags(cmd, opts.Backend, opts.Harness)
	if err != nil {
		return nil, err
	}
	var invalid error
	settings.Visit(func(f *flag.Flag) {
		if f.Name != "runner" && (flags.Lookup(f.Name) == nil || flags.Lookup(f.Name).Hidden) && invalid == nil {
			invalid = fmt.Errorf("unknown flag: --%s for sandbox %q and harness %q", f.Name, opts.Backend, opts.Harness)
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
		if opts.CodexSandbox == "" {
			opts.CodexSandbox = flags.Lookup("codex-sandbox").DefValue
		}
	case "claude":
		if opts.ClaudePermissionMode == "" {
			opts.ClaudePermissionMode = flags.Lookup("claude-permission-mode").DefValue
		}
	}
	_, err = opts.Adapter()
	return positionals, err
}
