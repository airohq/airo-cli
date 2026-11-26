// Package login implements airo login, airo context and airo project, which
// write the kubeconfig, and airo credential, the kubeconfig
// exec plugin that hands the signed-in person's token to kubectl and airo.
package login

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/dynamic"
	clientauthv1 "k8s.io/client-go/pkg/apis/clientauthentication/v1"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/airohq/airo-cli/internal/generic"
	"github.com/airohq/airo-cli/internal/picker"
	"github.com/airohq/airo-cli/internal/ui"
)

const (
	execAPIVersion = "client.authentication.k8s.io/v1"
	// A customer's people are in the group customer:<tenant>, and the
	// tenant's first project is <tenant>-default.
	customerGroupPrefix  = "customer:"
	defaultProjectSuffix = "-default"
	// defaultContext is the context of a first sign-in.
	defaultContext = "airo"
)

// platform is where to sign in, from the flags. An empty field means the
// value already in the kubeconfig.
type platform struct {
	server   string
	ca       []byte
	issuer   string
	clientID string
}

// NewCommand returns the login command.
func NewCommand(flags *genericclioptions.ConfigFlags, selection *Selection) *cobra.Command {
	var (
		p      platform
		caData string
	)
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Sign in to the platform",
		Long: `Login signs you in through the browser and makes the platform the current
context of your kubeconfig. The kubeconfig holds no credentials: airo keeps
the tokens in your cache directory and refreshes them.

The first time, pass where the platform is: --server, --issuer, --client-id
and the API server's certificate authority. The platform's admin gives you
the values. They stay in the kubeconfig, so after that airo login signs you
in again, for example to pick up a new role.

Choose a tenant with --tenant and a project with --project. Switching tenants
later reuses this sign-in and restores each tenant's project.`,
		Example: `  # the first time
  airo login --server https://api.example.com:6443 --certificate-authority ca.crt \
    --issuer https://login.example.com/ --client-id abc123

  airo login                       # sign in again
  airo login --project acme-web    # work in another project from now on
  airo login --tenant acme         # choose a tenant when signing in`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var err error
			p.server = *flags.APIServer
			if p.ca, err = readCA(*flags.CAFile, caData); err != nil {
				return err
			}
			return run(cmd.Context(), cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr(), flags, p, selection.Tenant)
		},
	}
	cmd.Flags().StringVar(&p.issuer, "issuer", "", "the OIDC issuer's URL, for the first sign-in")
	cmd.Flags().StringVar(&p.clientID, "client-id", "", "the OIDC client ID, for the first sign-in")
	cmd.Flags().StringVar(&caData, "certificate-authority-data", "", caDataUsage)
	return cmd
}

const caDataUsage = "the API server's certificate authority as base64, in place of --certificate-authority's file"

// readCA returns the certificate authority from --certificate-authority's
// file or --certificate-authority-data's base64, or nil when neither is set.
func readCA(file, data string) ([]byte, error) {
	switch {
	case data != "" && file != "":
		return nil, errors.New("pass --certificate-authority or --certificate-authority-data, not both")
	case data != "":
		ca, err := base64.StdEncoding.DecodeString(data)
		if err != nil {
			return nil, fmt.Errorf("--certificate-authority-data is not base64: %w", err)
		}
		return ca, nil
	case file != "":
		return os.ReadFile(file)
	}
	return nil, nil
}

// NewCredentialCommand returns the exec plugin that airo login writes into
// the kubeconfig. People do not run it.
func NewCredentialCommand() *cobra.Command {
	var o oidc
	var noSignIn bool
	cmd := &cobra.Command{
		Use:    "credential",
		Short:  "Print the signed-in person's token for kubectl (kubeconfig exec plugin)",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// stdout is the credential, so the sign-in prompt goes to stderr.
			ctx, prompt := cmd.Context(), cmd.ErrOrStderr()
			if noSignIn {
				// A shell completion waits for this: refresh, but briefly.
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, generic.CompletionTimeout)
				defer cancel()
				prompt = nil
			}
			id, exp, err := o.token(ctx, prompt)
			if err != nil {
				return err
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(clientauthv1.ExecCredential{
				TypeMeta: metav1.TypeMeta{APIVersion: execAPIVersion, Kind: "ExecCredential"},
				Status: &clientauthv1.ExecCredentialStatus{
					Token:               id,
					ExpirationTimestamp: &metav1.Time{Time: exp},
				},
			})
		},
	}
	cmd.Flags().StringVar(&o.issuer, "issuer", "", "the OIDC issuer's URL")
	cmd.Flags().StringVar(&o.clientID, "client-id", "", "the OIDC client ID")
	cmd.Flags().StringVar(&o.session, "session", "", "the sign-in whose tokens to use: the context's name")
	cmd.Flags().BoolVar(&noSignIn, strings.TrimPrefix(generic.NoSignInFlag, "--"), false, "fail instead of signing in, as a shell completion needs")
	_ = cmd.MarkFlagRequired("issuer")
	_ = cmd.MarkFlagRequired("client-id")
	return cmd
}

func run(ctx context.Context, in io.Reader, out, prompt io.Writer, flags *genericclioptions.ConfigFlags, p platform, tenant string) error {
	access := flags.ToRawKubeConfigLoader().ConfigAccess()
	cfg, err := access.GetStartingConfig()
	if err != nil {
		return err
	}
	edit := configEdit{access, cfg.DeepCopy()}
	name, o, err := prepare(cfg, *flags.Context, p)
	if err != nil {
		return err
	}
	cfg.CurrentContext = name

	token, _, err := o.authenticate(ctx, prompt, "")
	if err != nil {
		return err
	}

	// Ask the API server who the token is. The token is passed directly, so
	// this works before airo is on the PATH for the exec plugin.
	rc, err := clientcmd.NewNonInteractiveClientConfig(*cfg, name, &clientcmd.ConfigOverrides{}, access).ClientConfig()
	if err != nil {
		return err
	}
	rc.ExecProvider = nil
	rc.BearerToken = token.IDToken
	user, err := currentUser(ctx, rc)
	if err != nil {
		return fmt.Errorf("signed in, but could not verify your identity: %w", err)
	}

	saved, err := storedTenant(cfg.Contexts[name])
	if err != nil {
		return err
	}
	if tenant == "" {
		memberships := tenantsOf(user.Groups)
		if saved != "" && !slices.Contains(memberships, saved) {
			// Admin access may come from grants outside customer groups.
			c, err := dynamic.NewForConfig(rc)
			if err != nil {
				return err
			}
			err = checkTenant(ctx, c, saved)
			switch {
			case err == nil:
				tenant = saved
			case apierrors.IsForbidden(err), apierrors.IsNotFound(err):
			default:
				return err
			}
		}
		if tenant == "" {
			if !ui.IsTerminal(out) {
				in = nil
			}
			tenant, err = chooseLoginTenant(ctx, in, prompt, saved, memberships)
			if err != nil {
				return err
			}
		}
	}
	if tenant != "" {
		if err := selectTenant(ctx, cfg, name, tenant, *flags.Namespace, rc); err != nil {
			return err
		}
	} else {
		if saved != "" {
			// The last tenant was removed. Return to this account's sign-in.
			source := cfg.Contexts[name]
			for candidate, c := range cfg.Contexts {
				if c.Cluster == source.Cluster && c.AuthInfo == source.AuthInfo && c.Extensions[tenantExtension] == nil {
					name = candidate
					break
				}
			}
			if name == cfg.CurrentContext {
				return errors.New("previous tenant unavailable: select a tenant with airo login --tenant NAME")
			}
			cfg.CurrentContext = name
		}
		if *flags.Namespace != "" {
			cfg.Contexts[name].Namespace = *flags.Namespace
		}
	}
	path, err := o.cachePath()
	if err != nil {
		return err
	}
	if err := save(path, token); err != nil {
		return err
	}
	if err := edit.save(cfg); err != nil {
		return err
	}
	fmt.Fprintf(out, "Signed in as %s\n", user.Username)
	writeSelection(out, tenant, cfg.Contexts[cfg.CurrentContext].Namespace)
	fmt.Fprintf(out, "  groups:    %s\n", strings.Join(user.Groups, ", "))
	if _, err := exec.LookPath("airo"); err != nil {
		fmt.Fprintln(prompt, "airo is not on your PATH. The kubeconfig runs it by name, so add it there.")
	}
	return nil
}

func chooseLoginTenant(ctx context.Context, in io.Reader, prompt io.Writer, saved string, memberships []string) (string, error) {
	switch {
	case slices.Contains(memberships, saved):
		return saved, nil
	case len(memberships) == 0:
		return "", nil
	case len(memberships) == 1:
		return memberships[0], nil
	case !ui.Interactive(in, prompt):
		return "", fmt.Errorf("choose a tenant with --tenant NAME: %s", strings.Join(memberships, ", "))
	}
	tenant, err := picker.One(ctx, in, prompt, "tenant", memberships, -1)
	if err == nil && tenant == "" {
		err = errors.New("tenant selection canceled")
	}
	return tenant, err
}

// prepare makes the context name in cfg a sign-in to the platform, with airo
// as its exec plugin, and returns the context's name. The platform's values
// come from p, then from the context itself, and for a new context from the
// one the person is signed in to now. An entry that ran kubelogin is taken
// over.
func prepare(cfg *clientcmdapi.Config, name string, p platform) (string, oidc, error) {
	// The context to sign in to again, or to copy a new one from: the
	// current one when it is the platform's, and not, say, a local cluster's.
	base := cfg.CurrentContext
	if _, err := oidcOf(cfg, base); err != nil {
		base = defaultContext
	}
	if name == "" {
		name = base
	}
	kctx := cfg.Contexts[name]
	if kctx != nil {
		base = name
	}
	o, err := oidcOf(cfg, base)
	if kctx != nil && err != nil {
		// Someone else's entry, such as a local cluster's: leave it alone.
		return "", oidc{}, fmt.Errorf("context %q: %w: choose a different sign-in", name, err)
	}
	if p.issuer != "" {
		o.issuer = p.issuer
	}
	if p.clientID != "" {
		o.clientID = p.clientID
	}

	from := cfg.Contexts[base]
	if kctx == nil {
		// A second sign-in shares the cluster's entry. Its own address or
		// certificate authority gets its own entry.
		kctx = &clientcmdapi.Context{Cluster: name, AuthInfo: name}
		if from != nil && p.server == "" && p.ca == nil {
			kctx.Cluster = from.Cluster
		}
		cfg.Contexts[name] = kctx
	}
	cluster := cfg.Clusters[kctx.Cluster]
	if cluster == nil {
		cluster = clientcmdapi.NewCluster()
		if from != nil && cfg.Clusters[from.Cluster] != nil {
			cluster = cfg.Clusters[from.Cluster].DeepCopy()
			cluster.LocationOfOrigin = ""
		}
		cfg.Clusters[kctx.Cluster] = cluster
	}
	if p.server != "" {
		cluster.Server = p.server
	}
	if p.ca != nil {
		cluster.CertificateAuthority, cluster.CertificateAuthorityData = "", p.ca
	}

	var missing []string
	if cluster.Server == "" {
		missing = append(missing, "--server")
	}
	if o.issuer == "" {
		missing = append(missing, "--issuer")
	}
	if o.clientID == "" {
		missing = append(missing, "--client-id")
	}
	if missing != nil {
		return "", oidc{}, fmt.Errorf("the kubeconfig does not say where the platform is: pass %s, with the values from the platform's admin", strings.Join(missing, ", "))
	}

	// The context's name keeps this sign-in's tokens apart from another
	// context's at the same platform.
	if o.session == "" || kctx.AuthInfo == name {
		o.session = kctx.AuthInfo
	}
	origin := ""
	if user := cfg.AuthInfos[kctx.AuthInfo]; user != nil {
		origin = user.LocationOfOrigin
	}
	cfg.AuthInfos[kctx.AuthInfo] = execUser(o, origin)
	return name, o, nil
}

// errNoContext is oidcOf's error for a name the kubeconfig does not have.
var errNoContext = errors.New("the kubeconfig has no context")

// oidcOf reads the sign-in of the context name.
func oidcOf(cfg *clientcmdapi.Config, name string) (oidc, error) {
	kctx := cfg.Contexts[name]
	if kctx == nil {
		return oidc{}, fmt.Errorf("%w %q", errNoContext, name)
	}
	return oidcFrom(cfg.AuthInfos[kctx.AuthInfo])
}

// oidcFrom reads the issuer and client ID from a user's exec plugin: airo
// credential, or kubelogin (kubectl oidc-login) as the platform's admins
// hand it out.
func oidcFrom(user *clientcmdapi.AuthInfo) (oidc, error) {
	var o oidc
	if user != nil && user.Exec != nil {
		for _, arg := range user.Exec.Args {
			key, value, _ := strings.Cut(arg, "=")
			switch key {
			case "--issuer", "--oidc-issuer-url":
				o.issuer = value
			case "--client-id", "--oidc-client-id":
				o.clientID = value
			case "--session":
				o.session = value
			}
		}
	}
	if o.issuer == "" || o.clientID == "" {
		return oidc{}, errors.New("the user does not sign in through the platform")
	}
	return o, nil
}

func execUser(o oidc, origin string) *clientcmdapi.AuthInfo {
	return &clientcmdapi.AuthInfo{
		LocationOfOrigin: origin,
		Exec: &clientcmdapi.ExecConfig{
			APIVersion:      execAPIVersion,
			Command:         "airo",
			Args:            []string{"credential", "--issuer=" + o.issuer, "--client-id=" + o.clientID, "--session=" + o.session},
			InstallHint:     "Install airo: brew install airohq/tap/airo",
			InteractiveMode: clientcmdapi.IfAvailableExecInteractiveMode,
		},
	}
}

// defaultProject is the default project of the first customer group.
func defaultProject(groups []string) string {
	if tenants := tenantsOf(groups); len(tenants) > 0 {
		return tenants[0] + defaultProjectSuffix
	}
	return ""
}

func writeSelection(out io.Writer, tenant, project string) {
	if tenant == "" {
		tenant = "none"
	}
	if project == "" {
		project = "none (choose with airo project)"
	}
	fmt.Fprintf(out, "Tenant:  %s\nProject: %s\n", tenant, project)
}
