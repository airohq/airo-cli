package login

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/ktr0731/go-fuzzyfinder"
	"github.com/spf13/cobra"
	"golang.org/x/term"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/cli-runtime/pkg/printers"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// previousArg is the argument that switches back, as in cd - and kubectx -.
const previousArg = "-"

// NewContextCommand returns the context command.
func NewContextCommand(flags *genericclioptions.ConfigFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "context [NAME|-]",
		Aliases: []string{"contexts", "ctx"},
		Short:   "Switch between your sign-ins",
		Long: `Context makes another sign-in the current context of your kubeconfig, for
every later command of airo and kubectl. A sign-in is a context that airo
login made: one person at one platform.

Without a name it shows a picker. Type to filter, and press Enter to choose
or Esc to leave. "-" switches back to the context you were in before.

Contexts that do not sign in through the platform, such as a local
cluster's, are not listed and not switched to.`,
		Example: `  airo context                # choose from a picker
  airo context customer       # switch to the context customer
  airo context -              # switch back to the previous one
  airo context list
  airo ctx -                  # ctx is short for context`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := newPreviousStore()
			if err != nil {
				return err
			}
			target := ""
			if len(args) == 1 {
				target = args[0]
			}
			var ask func([]string, int) (string, error)
			if isTerminal(cmd.InOrStdin()) {
				ask = func(names []string, current int) (string, error) {
					return pickContext(cmd.Context(), names, current)
				}
			}
			return switchContext(cmd.OutOrStdout(), flags.ToRawKubeConfigLoader().ConfigAccess(), store, target, ask)
		},
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List your sign-ins",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return listContexts(cmd.OutOrStdout(), flags.ToRawKubeConfigLoader().ConfigAccess())
		},
	})
	return cmd
}

// switchContext makes target the kubeconfig's current context. An empty
// target is chosen with ask, which gets the names and the index of the
// current one, or -1. ask is nil when there is nobody to ask, and returns an
// empty name when the person chose none.
func switchContext(out io.Writer, access clientcmd.ConfigAccess, store previousStore, target string, ask func(names []string, current int) (string, error)) error {
	cfg, err := access.GetStartingConfig()
	if err != nil {
		return err
	}
	names := signIns(cfg)
	if len(names) == 0 {
		return errors.New("the kubeconfig has no sign-in: make one with airo login")
	}
	switch target {
	case "":
		if ask == nil {
			return fmt.Errorf("name the context to switch to: %s", strings.Join(names, ", "))
		}
		if target, err = ask(names, slices.Index(names, cfg.CurrentContext)); err != nil {
			return err
		}
		if target == "" {
			return nil
		}
	case previousArg:
		if target = store.get(); target == "" {
			return errors.New("this machine remembers no previous context: name one, or choose with airo context")
		}
	}
	if _, err := oidcOf(cfg, target); err != nil {
		return fmt.Errorf("%w: your sign-ins are %s", contextError(target, err), strings.Join(names, ", "))
	}
	if target == cfg.CurrentContext {
		fmt.Fprintf(out, "Already in context %s\n", target)
		return nil
	}

	previous := cfg.CurrentContext
	cfg.CurrentContext = target
	if err := clientcmd.ModifyConfig(access, *cfg, true); err != nil {
		return err
	}
	// Only a sign-in is remembered, because "-" switches to nothing else.
	if _, err := oidcOf(cfg, previous); err == nil {
		if err := store.put(previous); err != nil {
			return fmt.Errorf("switched to context %s, but could not remember %s for airo context -: %w", target, previous, err)
		}
	}
	fmt.Fprintf(out, "Switched to context %s\n", target)
	if ns := cfg.Contexts[target].Namespace; ns != "" {
		fmt.Fprintf(out, "  namespace: %s\n", ns)
	}
	return nil
}

// contextError says why the context name cannot be switched to.
func contextError(name string, err error) error {
	if errors.Is(err, errNoContext) {
		return err
	}
	return fmt.Errorf("context %q does not sign in through the platform", name)
}

// listContexts prints the sign-ins and marks the current one.
func listContexts(out io.Writer, access clientcmd.ConfigAccess) error {
	cfg, err := access.GetStartingConfig()
	if err != nil {
		return err
	}
	names := signIns(cfg)
	if len(names) == 0 {
		return errors.New("the kubeconfig has no sign-in: make one with airo login")
	}
	w := printers.GetNewTabWriter(out)
	fmt.Fprintln(w, "CURRENT\tNAME\tNAMESPACE")
	for _, name := range names {
		current := ""
		if name == cfg.CurrentContext {
			current = "*"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\n", current, name, cfg.Contexts[name].Namespace)
	}
	return w.Flush()
}

// signIns returns the names of the contexts that sign in through the
// platform, sorted.
func signIns(cfg *clientcmdapi.Config) []string {
	var names []string
	for name := range cfg.Contexts {
		if _, err := oidcOf(cfg, name); err == nil {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// pickContext shows the picker with the cursor on the current context. It
// returns an empty name when the person left without choosing.
func pickContext(ctx context.Context, names []string, current int) (string, error) {
	i, err := fuzzyfinder.Find(names,
		func(i int) string {
			if i == current {
				return names[i] + " (current)"
			}
			return names[i]
		},
		fuzzyfinder.WithContext(ctx),
		fuzzyfinder.WithPromptString("context> "),
		fuzzyfinder.WithPreselected(func(i int) bool { return i == current }),
	)
	if errors.Is(err, fuzzyfinder.ErrAbort) || ctx.Err() != nil {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("the picker did not open, name the context instead: %w", err)
	}
	return names[i], nil
}

// previousStore remembers the context the person switched away from, in the
// user's cache directory, for airo context -.
type previousStore struct {
	path string
}

func newPreviousStore() (previousStore, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return previousStore{}, err
	}
	return previousStore{path: filepath.Join(dir, "airo", "previous-context")}, nil
}

// get returns the previous context, or empty.
func (s previousStore) get() string {
	data, _ := os.ReadFile(s.path)
	return strings.TrimSpace(string(data))
}

func (s previousStore) put(name string) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(s.path, []byte(name+"\n"), 0o600)
}

// isTerminal reports whether a person types the input.
func isTerminal(in io.Reader) bool {
	f, ok := in.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}
