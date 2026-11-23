package generic

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/cli-runtime/pkg/genericclioptions"

	"github.com/airohq/airo-cli/internal/ui"
)

// kindCommand is the help of a kind without a command of its own, such as
// configmap: the verbs every kind has, as airo agent shows its verbs. It
// asks the cluster what the word means, as running a verb does.
func kindCommand(flags *genericclioptions.ConfigFlags, word string) (*cobra.Command, error) {
	m, err := mapping(flags, word)
	if err != nil {
		return nil, err
	}
	where := "in the current project"
	if m.Scope.Name() != meta.RESTScopeNameNamespace {
		where = "in the cluster"
	}
	gvk := m.GroupVersionKind
	version := gvk.GroupVersion().String()
	cmd := &cobra.Command{
		Use:   word,
		Short: fmt.Sprintf("Work with %s %s", m.Resource.Resource, where),
		Long: fmt.Sprintf("%s is a kind the cluster serves (%s %s). airo has no commands of its own for it, so it offers the verbs every kind has: list and get the %s %s, or describe, edit or delete one by name.",
			gvk.Kind, version, m.Resource.Resource, m.Resource.Resource, where),
		Example: fmt.Sprintf(`  airo %[1]s list
  airo %[1]s get NAME -o yaml
  airo %[1]s describe NAME
  airo %[1]s edit NAME
  airo %[1]s delete NAME`, word),
	}
	if alias := strings.ToLower(gvk.Kind); alias != word {
		cmd.Aliases = append(cmd.Aliases, alias)
	}
	if plural := m.Resource.Resource; plural != word && !strings.EqualFold(plural, gvk.Kind) {
		cmd.Aliases = append(cmd.Aliases, plural)
	}
	cmd.Long = ui.Wrap(cmd.Long, 76)
	cmd.AddCommand(Commands(flags, word)...)
	return cmd, nil
}

// showKindHelp shows the help of the kind that args name, or of its verb
// when args name one too, as a child of root so that its usage reads
// airo KIND. It reports false when the first word is no kind.
func showKindHelp(root *cobra.Command, flags *genericclioptions.ConfigFlags, args []string) (bool, error) {
	kind, err := kindCommand(flags, args[0])
	if err != nil {
		return false, err
	}
	root.AddCommand(kind)
	if len(args) > 1 {
		for _, verb := range kind.Commands() {
			if verb.Name() == args[1] {
				return true, verb.Help()
			}
		}
	}
	return true, kind.Help()
}
