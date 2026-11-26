package generic

import (
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/cli-runtime/pkg/genericclioptions"

	"github.com/airohq/airo-cli/internal/ui"
)

// servedKind is a kind the cluster serves, as discovery describes it.
type servedKind struct {
	gv  schema.GroupVersion
	api metav1.APIResource
}

func (r servedKind) groupKind() string {
	return schema.GroupKind{Group: r.gv.Group, Kind: r.api.Kind}.String()
}

// resources lists the kinds the cluster serves in their preferred versions,
// without subresources, from the cached discovery as kubectl's.
func resources(flags *genericclioptions.ConfigFlags) ([]servedKind, error) {
	dc, err := flags.ToDiscoveryClient()
	if err != nil {
		return nil, err
	}
	// A group that fails still leaves the others' kinds, as for kubectl.
	lists, err := dc.ServerPreferredResources()
	if len(lists) == 0 && err != nil {
		return nil, err
	}
	var all []servedKind
	for _, l := range lists {
		gv, err := schema.ParseGroupVersion(l.GroupVersion)
		if err != nil {
			continue
		}
		for _, r := range l.APIResources {
			if !strings.Contains(r.Name, "/") {
				all = append(all, servedKind{gv, r})
			}
		}
	}
	return all, nil
}

// owners maps each kind an airo command owns, as Kind.group, to the command.
func owners(root *cobra.Command) map[string]*cobra.Command {
	m := map[string]*cobra.Command{}
	for _, c := range root.Commands() {
		if k := c.Annotations[kindAnnotation]; k != "" {
			m[k] = c
		}
	}
	return m
}

// NewAPIResourcesCommand returns airo api-resources: kubectl's, with the airo
// command that owns each kind, so that people learn the short names.
func NewAPIResourcesCommand(flags *genericclioptions.ConfigFlags) *cobra.Command {
	return &cobra.Command{
		Use:     "api-resources",
		Short:   "List the kinds the cluster serves, with their short names",
		Long:    ui.Wrap(`List the kinds the cluster serves, as kubectl api-resources does. Any of a kind's names runs its airo command: the plural, the singular, a short name or NAME.GROUP. A kind without a command takes the verbs every kind has.`, 76),
		Example: "  airo api-resources",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			all, err := resources(flags)
			if err != nil {
				return err
			}
			slices.SortFunc(all, func(a, b servedKind) int {
				return strings.Compare(a.api.Name+"."+a.gv.Group, b.api.Name+"."+b.gv.Group)
			})
			own := owners(cmd.Root())
			rows := [][]string{{"NAME", "SHORTNAMES", "COMMAND", "APIVERSION", "IN PROJECT", "KIND"}}
			for _, r := range all {
				command := ""
				if c := own[r.groupKind()]; c != nil {
					command = "airo " + c.Name()
				}
				rows = append(rows, []string{r.api.Name, strings.Join(r.api.ShortNames, ","), command,
					r.gv.String(), strconv.FormatBool(r.api.Namespaced), r.api.Kind})
			}
			return ui.Table(cmd.OutOrStdout(), rows)
		},
	}
}

// clusterAliases adds to the help of a command that owns a kind the short
// names the cluster gives the kind. Help asks quietly, as a completion: it
// never signs in, and without an answer the help shows what airo knows.
func clusterAliases(cmd *cobra.Command, flags *genericclioptions.ConfigFlags) {
	kind := cmd.Annotations[kindAnnotation]
	if kind == "" || !PrepareCompletion(flags) {
		return
	}
	all, err := resources(flags)
	if err != nil {
		return
	}
	for _, r := range all {
		if r.groupKind() != kind {
			continue
		}
		for _, s := range r.api.ShortNames {
			if !cmd.HasAlias(s) && s != cmd.Name() {
				cmd.Aliases = append(cmd.Aliases, s)
			}
		}
	}
}
