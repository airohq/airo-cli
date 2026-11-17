package login

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	authenticationv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// NewStatusCommand returns the status command.
func NewStatusCommand(flags *genericclioptions.ConfigFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show your user, tenant, project, and connection status",
		Long: `Status shows your user and tenant as the Kubernetes API sees them, along
with the project, context, and server selected for this command. The project
is currently the Kubernetes namespace.

If the identity check fails, the selected project, context, and server are
still shown, and the command exits with an error. Connected means the API
answered the identity check; it does not check access to the project.`,
		Example: `  airo status
  airo status --context customer
  airo status -n acme-web`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return status(cmd.Context(), cmd.OutOrStdout(), flags)
		},
	}
}

// NewWhoamiCommand returns the whoami command.
func NewWhoamiCommand(flags *genericclioptions.ConfigFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "whoami",
		Short: "Show your user, tenant, and groups",
		Long: `Whoami asks the Kubernetes API for your authenticated user and groups,
like kubectl auth whoami. Tenant names come from customer:<tenant> groups.
All memberships are shown when there is more than one; none is shown when
there is no customer group, such as for a platform admin.`,
		Example: `  airo whoami
  airo whoami --context customer`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			loader, err := identityLoader(flags)
			if err != nil {
				return err
			}
			cfg, err := loader.ClientConfig()
			if err != nil {
				return err
			}
			user, err := currentUser(cmd.Context(), cfg)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "User:    %s\nTenant:  %s\nGroups:  %s\n",
				user.Username, namesOrNone(tenantsOf(user.Groups)), namesOrNone(user.Groups))
			return err
		},
	}
}

func status(ctx context.Context, out io.Writer, flags *genericclioptions.ConfigFlags) error {
	loader, err := identityLoader(flags)
	if err != nil {
		return err
	}
	raw, err := loader.RawConfig()
	if err != nil {
		return err
	}
	cfg, err := loader.ClientConfig()
	if err != nil {
		return err
	}
	project, _, err := loader.Namespace()
	if err != nil {
		return err
	}
	name := raw.CurrentContext
	if *flags.Context != "" {
		name = *flags.Context
	}
	if name == "" {
		name = "none"
	}

	username, tenant, connection := "unknown", "unknown", "Unverified"
	user, checkErr := currentUser(ctx, cfg)
	if checkErr == nil {
		username = user.Username
		tenant = namesOrNone(tenantsOf(user.Groups))
		connection = "Connected"
	}
	_, err = fmt.Fprintf(out, "User:        %s\nTenant:      %s\nProject:     %s\nContext:     %s\nServer:      %s\nConnection:  %s\n",
		username, tenant, project, name, cfg.Host, connection)
	return errors.Join(checkErr, err)
}

// identityLoader avoids client-go's localhost fallback when nothing has
// been selected. An explicit server also works without a saved context.
func identityLoader(flags *genericclioptions.ConfigFlags) (clientcmd.ClientConfig, error) {
	loader := flags.ToRawKubeConfigLoader()
	raw, err := loader.RawConfig()
	if err != nil {
		return nil, err
	}
	if raw.CurrentContext == "" && *flags.Context == "" && *flags.APIServer == "" {
		return nil, errors.New("the kubeconfig has no current context: sign in with airo login, or select one with --context")
	}
	return loader, nil
}

// currentUser asks the API server, so identity comes from the credentials
// actually sent, including exec plugins and kubectl's impersonation flags.
func currentUser(ctx context.Context, cfg *rest.Config) (authenticationv1.UserInfo, error) {
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return authenticationv1.UserInfo{}, err
	}
	me, err := cs.AuthenticationV1().SelfSubjectReviews().Create(ctx, &authenticationv1.SelfSubjectReview{}, metav1.CreateOptions{})
	if err != nil {
		return authenticationv1.UserInfo{}, fmt.Errorf("read your identity from the Kubernetes API: %w", err)
	}
	if me.Status.UserInfo.Username == "" {
		return authenticationv1.UserInfo{}, errors.New("the Kubernetes API returned no status.userInfo.username in its SelfSubjectReview")
	}
	return me.Status.UserInfo, nil
}

// tenantsOf reads the platform's customer groups, not the project name:
// project names need not start with the tenant, and a user can have several.
func tenantsOf(groups []string) []string {
	var names []string
	for _, group := range groups {
		if name, ok := strings.CutPrefix(group, customerGroupPrefix); ok && name != "" && !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	return names
}

func namesOrNone(names []string) string {
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, ", ")
}
