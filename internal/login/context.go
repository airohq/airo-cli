package login

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/cli-runtime/pkg/printers"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/airohq/airo-cli/internal/picker"
	"github.com/airohq/airo-cli/internal/ui"
)

// previousArg is the argument that switches back, as in cd - and kubectx -.
const previousArg = "-"

// NewContextCommand returns the context command.
func NewContextCommand(flags *genericclioptions.ConfigFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "context [NAME|-]",
		Hidden:  true,
		Aliases: []string{"contexts", "ctx"},
		Short:   "Switch between your sign-ins",
		Long: `Context makes another sign-in the current context of your kubeconfig, for
every later command of airo and kubectl. A sign-in is a context that airo
login made: one person at one platform.

Without a name it offers an inline choice. Use the arrow keys or type to
filter, then press Enter. Plain terminals show numbered choices. "-" switches back to the context you were in before.

Contexts that do not sign in through the platform, such as a local
cluster's, are not listed and not switched to.`,
		Example: `  airo context                # choose from a picker
  airo context customer       # switch to the context customer
  airo context -              # switch back to the previous one
  airo context list
  airo context set --server https://api.example.com
  airo ctx -               # ctx is short for context`,
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
			if ui.Interactive(cmd.InOrStdin(), cmd.OutOrStdout()) && ui.IsTerminal(cmd.ErrOrStderr()) {
				ask = func(names []string, current int) (string, error) {
					return picker.One(cmd.Context(), cmd.InOrStdin(), cmd.ErrOrStderr(), "context", names, current)
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
	cmd.AddCommand(&cobra.Command{
		Use:     "delete NAME",
		Short:   "Remove a sign-in context from your kubeconfig",
		Long:    "Remove the named Airo context. If it is current, clear the selection. Its cluster and user entries stay in the kubeconfig.",
		Example: "  airo context delete customer",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			access := flags.ToRawKubeConfigLoader().ConfigAccess()
			cfg, err := access.GetStartingConfig()
			if err != nil {
				return err
			}
			name := args[0]
			if _, err := oidcOf(cfg, name); err != nil {
				return contextError(name, err)
			}
			delete(cfg.Contexts, name)
			current := cfg.CurrentContext == name
			if current {
				cfg.CurrentContext = ""
			}
			if err := clientcmd.ModifyConfig(access, *cfg, true); err != nil {
				return err
			}
			if current {
				fmt.Fprintln(cmd.ErrOrStderr(), "Removed the active context. Use 'airo context NAME' to select another sign-in.")
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Deleted context %s\n", name)
			return err
		},
	})
	cmd.AddCommand(newContextSetCommand(flags))
	return cmd
}

// newContextSetCommand returns airo context set.
func newContextSetCommand(flags *genericclioptions.ConfigFlags) *cobra.Command {
	var (
		p      platform
		caData string
	)
	cmd := &cobra.Command{
		Use:   "set [NAME]",
		Short: "Change what a sign-in holds, such as the platform's address",
		Long: `Set changes values of a sign-in in your kubeconfig, for every later command
of airo and kubectl, without signing in again. It is airo's form of kubectl
config set-context and set-cluster.

Without a name it changes the current context. Only the flags you pass are
changed. Contexts that do not sign in through the platform are left alone,
and a new sign-in is made with airo login.`,
		Example: `  airo context set --server https://api.example.com
  airo context set -n acme-web                # work in another project from now on
  airo context set customer --certificate-authority ca.crt
  airo context set --issuer https://login.example.com/ --client-id abc123`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := *flags.Context
			if len(args) == 1 {
				if name != "" && name != args[0] {
					return fmt.Errorf("name one context: got %s and the selected sign-in %s", args[0], name)
				}
				name = args[0]
			}
			var err error
			p.server = *flags.APIServer
			if p.ca, err = readCA(*flags.CAFile, caData); err != nil {
				return err
			}
			return setContext(cmd.OutOrStdout(), flags.ToRawKubeConfigLoader().ConfigAccess(), name, p, *flags.Namespace)
		},
	}
	cmd.Flags().StringVar(&p.issuer, "issuer", "", "the OIDC issuer's URL")
	cmd.Flags().StringVar(&p.clientID, "client-id", "", "the OIDC client ID")
	cmd.Flags().StringVar(&caData, "certificate-authority-data", "", caDataUsage)
	return cmd
}

// setContext writes the values of p that are set, and namespace, into the
// sign-in name, or into the current context when name is empty.
func setContext(out io.Writer, access clientcmd.ConfigAccess, name string, p platform, namespace string) error {
	if p.server == "" && p.ca == nil && p.issuer == "" && p.clientID == "" && namespace == "" {
		return errors.New("nothing to set: pass --server, --certificate-authority, --issuer, --client-id or -n")
	}
	if p.server != "" {
		if u, err := url.Parse(p.server); err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
			return fmt.Errorf("--server %q is not an address such as https://api.example.com", p.server)
		}
	}
	cfg, err := access.GetStartingConfig()
	if err != nil {
		return err
	}
	names := signIns(cfg)
	if len(names) == 0 {
		return errors.New("the kubeconfig has no sign-in: make one with airo login")
	}
	if name == "" {
		name = cfg.CurrentContext
	}
	o, err := oidcOf(cfg, name)
	if err != nil {
		return fmt.Errorf("%w: your sign-ins are %s", contextError(name, err), strings.Join(names, ", "))
	}
	kctx := cfg.Contexts[name]

	var shared []string
	if p.server != "" || p.ca != nil {
		cluster := cfg.Clusters[kctx.Cluster]
		if cluster == nil {
			return fmt.Errorf("context %q names the cluster %q, which the kubeconfig does not have: sign in again with airo login --server", name, kctx.Cluster)
		}
		if p.server != "" {
			cluster.Server = p.server
		}
		if p.ca != nil {
			cluster.CertificateAuthority, cluster.CertificateAuthorityData = "", p.ca
		}
		// Sign-ins at the same platform share the cluster's entry.
		for _, other := range names {
			if other != name && cfg.Contexts[other].Cluster == kctx.Cluster {
				shared = append(shared, other)
			}
		}
	}
	if p.issuer != "" || p.clientID != "" {
		if p.issuer != "" {
			o.issuer = p.issuer
		}
		if p.clientID != "" {
			o.clientID = p.clientID
		}
		// As airo login does, this takes over an entry that ran kubelogin.
		if o.session == "" {
			o.session = kctx.AuthInfo
		}
		cfg.AuthInfos[kctx.AuthInfo] = execUser(o, cfg.AuthInfos[kctx.AuthInfo].LocationOfOrigin)
	}
	if namespace != "" {
		kctx.Namespace = namespace
	}
	if err := clientcmd.ModifyConfig(access, *cfg, true); err != nil {
		return err
	}

	fmt.Fprintf(out, "Set context %s\n", name)
	if p.server != "" {
		fmt.Fprintf(out, "  server:    %s\n", p.server)
	}
	if p.ca != nil {
		fmt.Fprintln(out, "  certificate authority: replaced")
	}
	if p.issuer != "" {
		fmt.Fprintf(out, "  issuer:    %s\n", p.issuer)
	}
	if p.clientID != "" {
		fmt.Fprintf(out, "  client ID: %s\n", p.clientID)
	}
	if namespace != "" {
		fmt.Fprintf(out, "  namespace: %s\n", namespace)
	}
	if len(shared) > 0 {
		fmt.Fprintf(out, "The same platform's entry serves %s, so the address changed there too\n", strings.Join(shared, ", "))
	}
	return nil
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
