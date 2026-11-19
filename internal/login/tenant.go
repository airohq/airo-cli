package login

import (
	"context"
	"fmt"
	"io"
	"maps"
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

const featureLabelPrefix = "features.airohq.dev/"

// NewTenantCommand shows tenant-wide feature flags.
func NewTenantCommand(flags *genericclioptions.ConfigFlags) *cobra.Command {
	var name string
	cmd := &cobra.Command{
		Use:     "tenant",
		Aliases: []string{"tenants"},
		Short:   "Show your tenant and its feature flags",
		Example: "  airo tenant\n  airo tenant --tenant acme",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return showTenant(cmd.Context(), cmd.OutOrStdout(), flags, name)
		},
	}
	cmd.Flags().StringVar(&name, "tenant", "", "tenant to inspect (defaults to your tenant membership)")
	verbs := generic.Commands(flags, cmd.Name())
	for _, verb := range verbs {
		if verb.Name() == "list" {
			verb.Short = "List tenants you can see"
		}
	}
	cmd.AddCommand(verbs...)
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
			return fmt.Errorf("%s has no tenant membership: use 'airo tenant list' to see accessible tenants, then 'airo tenant --tenant NAME'", user.Username)
		case 1:
			name = names[0]
		default:
			return fmt.Errorf("you belong to several tenants (%s): choose one with --tenant NAME", strings.Join(names, ", "))
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

	features := make(map[string]bool)
	for label, value := range labels {
		if feature, ok := strings.CutPrefix(label, featureLabelPrefix); ok {
			features[feature] = value == "true"
		}
	}
	if _, err := fmt.Fprintf(out, "Tenant: %s\n\n", name); err != nil {
		return err
	}
	if len(features) == 0 {
		_, err := fmt.Fprintln(out, "No feature labels.")
		return err
	}
	table := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(table, "FEATURE\tSTATE")
	for _, feature := range slices.Sorted(maps.Keys(features)) {
		state := "disabled"
		if features[feature] {
			state = "enabled"
		}
		fmt.Fprintf(table, "%s\t%s\n", feature, state)
	}
	return table.Flush()
}
