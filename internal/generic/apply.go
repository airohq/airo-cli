package generic

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/cli-runtime/pkg/resource"
)

// fieldManager names airo as the owner of the fields it applies.
const fieldManager = "airo"

// NewApplyCommand returns the apply command.
func NewApplyCommand(flags *genericclioptions.ConfigFlags) *cobra.Command {
	var files []string
	cmd := &cobra.Command{
		Use:   "apply -f FILE",
		Short: "Create or update the resources in a file",
		Long: `Apply creates or updates the resources in YAML or JSON files, like
kubectl apply --server-side. A resource without a namespace goes to the
context's namespace.`,
		Example: `  airo apply -f agent.yaml
  airo apply -f manifests/         # every file in a directory
  cat agent.yaml | airo apply -f -`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ns, enforce, err := flags.ToRawKubeConfigLoader().Namespace()
			if err != nil {
				return err
			}
			r := resource.NewBuilder(flags).
				Unstructured().
				NamespaceParam(ns).DefaultNamespace().
				FilenameParam(enforce, &resource.FilenameOptions{Filenames: files}).
				Flatten().
				Do()
			if err := r.Err(); err != nil {
				return err
			}
			return r.Visit(func(info *resource.Info, err error) error {
				if err != nil {
					return err
				}
				data, err := runtime.Encode(unstructured.UnstructuredJSONScheme, info.Object)
				if err != nil {
					return err
				}
				// Server-side apply: the API server merges, and refuses a
				// field that another tool owns with a different value.
				_, err = resource.NewHelper(info.Client, info.Mapping).
					WithFieldManager(fieldManager).
					Patch(info.Namespace, info.Name, types.ApplyPatchType, data, &metav1.PatchOptions{})
				if err != nil {
					return fmt.Errorf("%s: %w", info.ObjectName(), err)
				}
				name := info.Name
				if info.Namespace != "" {
					name = info.Namespace + "/" + name
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Applied %s %s\n", strings.ToLower(info.Mapping.GroupVersionKind.Kind), name)
				return nil
			})
		},
	}
	cmd.Flags().StringSliceVarP(&files, "file", "f", nil, "a file, a directory, a URL, or - for standard input")
	_ = cmd.MarkFlagRequired("file")
	return cmd
}
