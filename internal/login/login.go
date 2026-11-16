// Package login implements airo login, and airo credential, the kubeconfig
// exec plugin that hands the signed-in person's token to kubectl and airo.
package login

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"
	authenticationv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/kubernetes"
	clientauthv1 "k8s.io/client-go/pkg/apis/clientauthentication/v1"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

const (
	execAPIVersion = "client.authentication.k8s.io/v1"
	// A customer's people are in the group customer:<tenant>, and the
	// tenant's first project is <tenant>-default.
	customerGroupPrefix  = "customer:"
	defaultProjectSuffix = "-default"
)

// NewCommand returns the login command.
func NewCommand(flags *genericclioptions.ConfigFlags) *cobra.Command {
	var file string
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Sign in to the platform",
		Long: `Login signs you in through the browser and makes the platform the current
context of your kubeconfig. The kubeconfig holds no credentials: airo keeps
the tokens in your cache directory and refreshes them.

The first time, pass the file you got from the platform's admin with -f.
After that, airo login signs you in again, for example to pick up a new role.`,
		Example: `  airo login -f airo.kubeconfig    # the first time
  airo login                       # sign in again
  airo login -n acme-web           # work in another project from now on`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return run(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), flags, file)
		},
	}
	cmd.Flags().StringVarP(&file, "file", "f", "", "the kubeconfig file from the platform's admin, for the first sign-in")
	return cmd
}

// NewCredentialCommand returns the exec plugin that airo login writes into
// the kubeconfig. People do not run it.
func NewCredentialCommand() *cobra.Command {
	var o oidc
	cmd := &cobra.Command{
		Use:    "credential",
		Short:  "Print the signed-in person's token for kubectl (kubeconfig exec plugin)",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// stdout is the credential, so the sign-in prompt goes to stderr.
			id, exp, err := o.token(cmd.Context(), cmd.ErrOrStderr(), false)
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
	_ = cmd.MarkFlagRequired("issuer")
	_ = cmd.MarkFlagRequired("client-id")
	return cmd
}

func run(ctx context.Context, out, prompt io.Writer, flags *genericclioptions.ConfigFlags, file string) error {
	access := flags.ToRawKubeConfigLoader().ConfigAccess()
	cfg, err := access.GetStartingConfig()
	if err != nil {
		return err
	}
	var (
		name string
		o    oidc
	)
	if file != "" {
		name, o, err = add(cfg, file)
	} else {
		name, o, err = existing(cfg, *flags.Context)
	}
	if err != nil {
		return err
	}
	cfg.CurrentContext = name
	if err := clientcmd.ModifyConfig(access, *cfg, true); err != nil {
		return err
	}

	id, _, err := o.token(ctx, prompt, true)
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
	rc.BearerToken = id
	cs, err := kubernetes.NewForConfig(rc)
	if err != nil {
		return err
	}
	me, err := cs.AuthenticationV1().SelfSubjectReviews().Create(ctx, &authenticationv1.SelfSubjectReview{}, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("signed in, but the platform refused the token: %w", err)
	}
	user := me.Status.UserInfo

	// A customer's first sign-in starts in the tenant's default project. A
	// namespace already in the context is the person's own choice and stays.
	// -n chooses the project to work in from now on.
	kctx := cfg.Contexts[name]
	ns := *flags.Namespace
	if ns == "" && kctx.Namespace == "" {
		ns = defaultProject(user.Groups)
	}
	if ns != "" && ns != kctx.Namespace {
		kctx.Namespace = ns
		if err := clientcmd.ModifyConfig(access, *cfg, true); err != nil {
			return err
		}
	}

	fmt.Fprintf(out, "Signed in as %s\n", user.Username)
	fmt.Fprintf(out, "  context:   %s\n", name)
	if kctx.Namespace != "" {
		fmt.Fprintf(out, "  namespace: %s\n", kctx.Namespace)
	} else {
		// Someone in no customer group, such as a platform admin.
		fmt.Fprintln(out, "  namespace: none, so commands use \"default\". Choose a project with airo login -n NAMESPACE")
	}
	fmt.Fprintf(out, "  groups:    %s\n", strings.Join(user.Groups, ", "))
	if _, err := exec.LookPath("airo"); err != nil {
		fmt.Fprintln(prompt, "airo is not on your PATH. The kubeconfig runs it by name, so add it there.")
	}
	return nil
}

// add copies the platform's context from the admin's file into cfg, under
// the same name, with airo as the user's exec plugin.
func add(cfg *clientcmdapi.Config, file string) (string, oidc, error) {
	src, err := clientcmd.LoadFromFile(file)
	if err != nil {
		return "", oidc{}, err
	}
	// Inline a certificate the file names by path.
	if err := clientcmdapi.FlattenConfig(src); err != nil {
		return "", oidc{}, err
	}
	name := src.CurrentContext
	if name == "" && len(src.Contexts) == 1 {
		for name = range src.Contexts {
		}
	}
	sctx := src.Contexts[name]
	if sctx == nil {
		return "", oidc{}, fmt.Errorf("%s has no current context", file)
	}
	cluster, user := src.Clusters[sctx.Cluster], src.AuthInfos[sctx.AuthInfo]
	if cluster == nil {
		return "", oidc{}, fmt.Errorf("%s: context %s has no cluster", file, name)
	}
	o, err := oidcFrom(user)
	if err != nil {
		return "", oidc{}, fmt.Errorf("%s: %w", file, err)
	}

	// Without the origin, the entries are written to the person's own
	// kubeconfig and not back to the admin's file.
	cluster = cluster.DeepCopy()
	cluster.LocationOfOrigin = ""
	kctx := &clientcmdapi.Context{Cluster: name, AuthInfo: name}
	if old := cfg.Contexts[name]; old != nil {
		kctx.Namespace = old.Namespace
		kctx.LocationOfOrigin = old.LocationOfOrigin
	}
	cfg.Clusters[name] = cluster
	cfg.AuthInfos[name] = execUser(o, "")
	cfg.Contexts[name] = kctx
	return name, o, nil
}

// existing finds the platform's context in cfg and makes airo its exec
// plugin, which also takes over an entry that ran kubelogin.
func existing(cfg *clientcmdapi.Config, name string) (string, oidc, error) {
	if name == "" {
		name = cfg.CurrentContext
	}
	hint := "pass the file from the platform's admin: airo login -f FILE"
	kctx := cfg.Contexts[name]
	if kctx == nil {
		if name == "" {
			return "", oidc{}, errors.New("the kubeconfig has no current context: " + hint)
		}
		return "", oidc{}, fmt.Errorf("the kubeconfig has no context %q: %s", name, hint)
	}
	user := cfg.AuthInfos[kctx.AuthInfo]
	o, err := oidcFrom(user)
	if err != nil {
		return "", oidc{}, fmt.Errorf("context %q: %w: %s", name, err, hint)
	}
	cfg.AuthInfos[kctx.AuthInfo] = execUser(o, user.LocationOfOrigin)
	return name, o, nil
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
			Args:            []string{"credential", "--issuer=" + o.issuer, "--client-id=" + o.clientID},
			InstallHint:     "Install airo: brew install airohq/tap/airo",
			InteractiveMode: clientcmdapi.IfAvailableExecInteractiveMode,
		},
	}
}

// defaultProject is the default project of the first customer group.
func defaultProject(groups []string) string {
	for _, g := range groups {
		if tenant, ok := strings.CutPrefix(g, customerGroupPrefix); ok && tenant != "" {
			return tenant + defaultProjectSuffix
		}
	}
	return ""
}
