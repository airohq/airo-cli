package login

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/dynamic"

	"github.com/airohq/airo-cli/internal/generic"
)

var tenantResource = schema.GroupVersionResource{Group: "capsule.clastix.io", Version: "v1beta2", Resource: "tenants"}

// NewTenantCommand shows tenant-wide solutions and capabilities.
func NewTenantCommand(flags *genericclioptions.ConfigFlags, selection *Selection) *cobra.Command {
	var name string
	cmd := &cobra.Command{
		Use:     "tenant [NAME]",
		Aliases: []string{"tenants"},
		Short:   "Switch tenants or show the selected tenant’s solutions and capabilities",
		Example: "  airo tenant\n  airo tenant acme",
		Args:    cobra.MaximumNArgs(1),

		ValidArgsFunction: generic.FirstArg(completeTenants(flags)),
		RunE: func(cmd *cobra.Command, args []string) error {
			if name != "" {
				if len(args) != 0 {
					return fmt.Errorf("use --name to inspect a tenant or a positional name to switch tenants")
				}
				return showTenant(cmd.Context(), cmd.OutOrStdout(), flags, name)
			}
			if len(args) == 0 {
				return showTenant(cmd.Context(), cmd.OutOrStdout(), flags, selection.Tenant)
			}
			if cmd.Flags().Changed("tenant") && selection.Tenant != args[0] {
				return fmt.Errorf("choose one tenant: got %s and %s", args[0], selection.Tenant)
			}
			access := flags.ToRawKubeConfigLoader().ConfigAccess()
			cfg, err := access.GetStartingConfig()
			if err != nil {
				return err
			}
			edit := configEdit{access, cfg.DeepCopy()}
			base := selectedContext(cfg, flags)
			if _, err := oidcOf(cfg, base); err != nil {
				return contextError(base, err)
			}
			rc, err := flags.ToRESTConfig()
			if err != nil {
				return err
			}
			if err := selectTenant(cmd.Context(), cfg, base, args[0], *flags.Namespace, rc); err != nil {
				return err
			}
			if err := edit.save(cfg); err != nil {
				return err
			}
			writeSelection(cmd.OutOrStdout(), args[0], cfg.Contexts[cfg.CurrentContext].Namespace)
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "name of the tenant to inspect (defaults to the selected tenant)")
	generic.CompleteFlag(cmd, "name", completeTenants(flags))
	verbs := generic.Commands(flags, cmd.Name())
	for _, verb := range verbs {
		if verb.Name() == "list" {
			verb.Short = "List tenants you can see"
		}
	}
	generic.Own(cmd, schema.GroupKind{Group: tenantResource.Group, Kind: "Tenant"})
	generic.AddBasic(cmd, verbs...)
	return cmd
}

func showTenant(ctx context.Context, out io.Writer, flags *genericclioptions.ConfigFlags, name string) error {
	loader, err := identityLoader(flags)
	if err != nil {
		return err
	}
	cfg, err := loader.ClientConfig()
	if err != nil {
		return err
	}
	if name == "" {
		user, err := currentUser(ctx, cfg)
		if err != nil {
			return err
		}
		names := tenantsOf(user.Groups)
		switch len(names) {
		case 0:
			return fmt.Errorf("%s has no tenant membership: use 'airo tenant list' to see accessible tenants, then 'airo tenant --name NAME'", user.Username)
		case 1:
			name = names[0]
		default:
			return fmt.Errorf("you belong to several tenants (%s): choose one with --name NAME", strings.Join(names, ", "))
		}
	}
	client, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return err
	}
	tenant, err := client.Resource(tenantResource).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("read tenant %s: %w", name, err)
	}
	labels, _, err := unstructured.NestedStringMap(tenant.Object, "metadata", "labels")
	if err != nil {
		return fmt.Errorf("read tenant %s labels: %w", name, err)
	}

	if _, err := fmt.Fprintf(out, "Tenant: %s\n", name); err != nil {
		return err
	}
	if err := writeTenantLabels(out, labels, "solutions.airohq.dev/", "solution"); err != nil {
		return err
	}
	return writeTenantLabels(out, labels, "capabilities.airohq.dev/", "capability")
}

func writeTenantLabels(out io.Writer, labels map[string]string, prefix, kind string) error {
	var names []string
	for key := range labels {
		if name, ok := strings.CutPrefix(key, prefix); ok {
			names = append(names, name)
		}
	}
	if _, err := fmt.Fprintln(out); err != nil {
		return err
	}
	if len(names) == 0 {
		_, err := fmt.Fprintf(out, "No %s labels.\n", kind)
		return err
	}
	slices.Sort(names)
	table := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintf(table, "%s\tSTATE\n", strings.ToUpper(kind))
	for _, name := range names {
		state := "disabled"
		if labels[prefix+name] == "true" {
			state = "enabled"
		}
		fmt.Fprintf(table, "%s\t%s\n", name, state)
	}
	return table.Flush()
}
