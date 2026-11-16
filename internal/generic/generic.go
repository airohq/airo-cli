// Package generic implements the verbs every kind has: list, get and delete,
// for any kind the cluster serves, and apply for files. It asks the cluster
// what a kind is and how to show it, so a new kind needs no new airo.
package generic

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/cli-runtime/pkg/printers"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
)

// Verbs is the help line for a kind without a command of its own.
const Verbs = "list, get NAME or delete NAME"

// Options are the flags of list and get.
type Options struct {
	output        string
	allNamespaces bool
}

// AddFlags adds the flags of list and get to cmd.
func (o *Options) AddFlags(cmd *cobra.Command) {
	cmd.Flags().StringVarP(&o.output, "output", "o", "", "print the whole resource as yaml or json")
	cmd.Flags().BoolVarP(&o.allNamespaces, "all-namespaces", "A", false, "list across all namespaces")
}

// Run handles airo KIND VERB [NAME] for a kind that has no command of its
// own in this binary.
func Run(ctx context.Context, out io.Writer, flags *genericclioptions.ConfigFlags, o Options, args []string) error {
	kind := args[0]
	m, err := mapping(flags, kind)
	if err != nil {
		return err
	}
	if len(args) == 1 {
		return fmt.Errorf("airo %s needs a verb: %s", kind, Verbs)
	}
	verb, rest := args[1], args[2:]
	name := ""
	switch {
	case verb == "list" && len(rest) == 0:
	case (verb == "get" || verb == "delete") && len(rest) == 1:
		name = rest[0]
	case verb == "list" || verb == "get" || verb == "delete":
		return fmt.Errorf("usage: airo %s %s", kind, Verbs)
	default:
		return fmt.Errorf("airo %s has no verb %q: use %s", kind, verb, Verbs)
	}
	return do(ctx, out, flags, o, m, verb, name)
}

// Commands returns list, get and delete for a kind that has its own command,
// such as tunnel. kind is what a person would type, such as
// tunnel.tunnels.airohq.dev.
func Commands(flags *genericclioptions.ConfigFlags, kind string) []*cobra.Command {
	noun, _, _ := strings.Cut(kind, ".")
	verb := func(use, short string, args cobra.PositionalArgs) *cobra.Command {
		var o Options
		name, _, _ := strings.Cut(use, " ")
		cmd := &cobra.Command{
			Use:   use,
			Short: short,
			Args:  args,
			RunE: func(cmd *cobra.Command, args []string) error {
				m, err := mapping(flags, kind)
				if err != nil {
					return err
				}
				return do(cmd.Context(), cmd.OutOrStdout(), flags, o, m, name, strings.Join(args, ""))
			},
		}
		if name != "delete" {
			o.AddFlags(cmd)
		}
		return cmd
	}
	return []*cobra.Command{
		verb("list", "List the "+noun+"s of the project", cobra.NoArgs),
		verb("get NAME", "Show one "+noun, cobra.ExactArgs(1)),
		verb("delete NAME", "Delete one "+noun, cobra.ExactArgs(1)),
	}
}

func do(ctx context.Context, out io.Writer, flags *genericclioptions.ConfigFlags, o Options, m *meta.RESTMapping, verb, name string) error {
	err := doVerb(ctx, out, flags, o, m, verb, name)
	if name != "" && apierrors.IsNotFound(err) {
		return fmt.Errorf("no %s named %s", strings.ToLower(m.GroupVersionKind.Kind), name)
	}
	return err
}

func doVerb(ctx context.Context, out io.Writer, flags *genericclioptions.ConfigFlags, o Options, m *meta.RESTMapping, verb, name string) error {
	cfg, err := flags.ToRESTConfig()
	if err != nil {
		return err
	}
	ns := ""
	if m.Scope.Name() == meta.RESTScopeNameNamespace && !(verb == "list" && o.allNamespaces) {
		if ns, _, err = flags.ToRawKubeConfigLoader().Namespace(); err != nil {
			return err
		}
	}
	noun := strings.ToLower(m.GroupVersionKind.Kind)
	full := name
	if ns != "" {
		full = ns + "/" + name
	}

	if verb == "delete" {
		c, err := dynamic.NewForConfig(cfg)
		if err != nil {
			return err
		}
		if err := c.Resource(m.Resource).Namespace(ns).Delete(ctx, name, metav1.DeleteOptions{}); err != nil {
			return err
		}
		fmt.Fprintf(out, "Deleted %s %s\n", noun, full)
		return nil
	}

	if o.output != "" {
		return printObject(ctx, out, cfg, o.output, m, ns, name)
	}
	t, err := table(ctx, cfg, m, ns, name, o.allNamespaces)
	if err != nil {
		return err
	}
	if len(t.Rows) == 0 {
		if ns == "" {
			fmt.Fprintf(out, "No %ss\n", noun)
		} else {
			fmt.Fprintf(out, "No %ss in %s\n", noun, ns)
		}
		return nil
	}
	return printers.NewTablePrinter(printers.PrintOptions{WithNamespace: o.allNamespaces}).PrintObj(t, out)
}

// mapping asks the cluster which kind the word means. It takes what kubectl
// takes: tunnel, tunnels, a short name, or tunnels.tunnels.airohq.dev.
func mapping(flags *genericclioptions.ConfigFlags, kind string) (*meta.RESTMapping, error) {
	mapper, err := flags.ToRESTMapper()
	if err != nil {
		return nil, err
	}
	// The word is resource.version.group or resource.group: try both.
	gvr, gr := schema.ParseResourceArg(kind)
	var res schema.GroupVersionResource
	err = &meta.NoResourceMatchError{}
	if gvr != nil {
		res, err = mapper.ResourceFor(*gvr)
	}
	if err != nil {
		res, err = mapper.ResourceFor(gr.WithVersion(""))
	}
	if meta.IsNoMatchError(err) {
		return nil, fmt.Errorf("%q is not an airo command or a kind in the cluster", kind)
	}
	if err != nil {
		return nil, err
	}
	gvk, err := mapper.KindFor(res)
	if err != nil {
		return nil, err
	}
	return mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
}

// table asks the API server for the kind's own columns, the ones kubectl get
// prints, so airo knows nothing about the kind.
func table(ctx context.Context, cfg *rest.Config, m *meta.RESTMapping, ns, name string, withNamespace bool) (*metav1.Table, error) {
	c := rest.CopyConfig(cfg)
	gv := m.Resource.GroupVersion()
	c.GroupVersion = &gv
	c.APIPath = "/apis"
	if gv.Group == "" {
		c.APIPath = "/api"
	}
	c.NegotiatedSerializer = scheme.Codecs.WithoutConversion()
	rc, err := rest.RESTClientFor(c)
	if err != nil {
		return nil, err
	}
	req := rc.Get().Resource(m.Resource.Resource).
		SetHeader("Accept", "application/json;as=Table;v=v1;g=meta.k8s.io,application/json")
	if ns != "" {
		req = req.Namespace(ns)
	}
	if name != "" {
		req = req.Name(name)
	}
	raw, err := req.Do(ctx).Raw()
	if err != nil {
		return nil, err
	}
	t := &metav1.Table{}
	if err := json.Unmarshal(raw, t); err != nil {
		return nil, err
	}
	if len(t.ColumnDefinitions) == 0 {
		return nil, fmt.Errorf("the cluster gave no table for %s: use -o yaml", m.Resource.Resource)
	}
	// The printer reads the namespace from each row's object.
	for i := range t.Rows {
		if !withNamespace || len(t.Rows[i].Object.Raw) == 0 {
			continue
		}
		obj := &metav1.PartialObjectMetadata{}
		if err := json.Unmarshal(t.Rows[i].Object.Raw, obj); err == nil {
			t.Rows[i].Object.Object = obj
		}
	}
	return t, nil
}

func printObject(ctx context.Context, out io.Writer, cfg *rest.Config, format string, m *meta.RESTMapping, ns, name string) error {
	var p printers.ResourcePrinter
	switch format {
	case "yaml":
		p = &printers.YAMLPrinter{}
	case "json":
		p = &printers.JSONPrinter{}
	default:
		return fmt.Errorf("-o %s is not supported: use yaml or json", format)
	}
	c, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return err
	}
	r := c.Resource(m.Resource).Namespace(ns)
	var obj runtime.Object
	if name != "" {
		u, err := r.Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		u.SetManagedFields(nil)
		obj = u
	} else {
		list, err := r.List(ctx, metav1.ListOptions{})
		if err != nil {
			return err
		}
		for i := range list.Items {
			list.Items[i].SetManagedFields(nil)
		}
		obj = list
	}
	return p.PrintObj(obj, out)
}
