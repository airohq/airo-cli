package generic

import (
	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/cli-runtime/pkg/genericclioptions"
)

// kindAnnotation holds, on a command of the root, the kind it is the
// command of, as Kind.group.
const kindAnnotation = "airo.dev/kind"

// Own marks cmd as the command of a kind, so that any word the cluster maps
// to the kind, such as the short name its CRD gives, runs cmd. The CRD owns
// short names; airo hard-codes only those of what is no kind, such as ctx.
func Own(cmd *cobra.Command, kind schema.GroupKind) {
	if cmd.Annotations == nil {
		cmd.Annotations = map[string]string{}
	}
	cmd.Annotations[kindAnnotation] = kind.String()
}

// Owner returns the command of root that owns the kind word names in the
// cluster, or nil. It asks only when word is no command: kubectl's own
// shortcut expander takes the short names from cached discovery. In a
// completion, the request is quiet as every completion's is.
func Owner(root *cobra.Command, flags *genericclioptions.ConfigFlags, word string, completing bool) *cobra.Command {
	for _, c := range root.Commands() {
		if c.Name() == word || c.HasAlias(word) {
			return nil
		}
	}
	if completing {
		if !PrepareCompletion(flags) {
			return nil
		}
	} else if completionScope != nil && completionScope(flags) != nil {
		return nil
	}
	m, err := mapping(flags, word)
	if err != nil {
		return nil
	}
	return owners(root)[m.GroupVersionKind.GroupKind().String()]
}
