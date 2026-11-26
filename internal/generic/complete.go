package generic

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/metadata"
	"k8s.io/client-go/rest"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// Shell completion follows kubectl's (k8s.io/kubectl/pkg/util/completion):
// kinds come from the cluster's cached discovery, names from a list as the
// kubeconfig's identity, and what the kubeconfig holds from the kubeconfig.
// Unlike kubectl, a completion caps each request and never starts a sign-in,
// so Tab cannot hang or open a browser. A completion that fails offers
// nothing: the shell shows no candidates, and running the command explains.

// CompletionTimeout caps each request a completion makes.
const CompletionTimeout = 5 * time.Second

// NoSignInFlag is the exec plugin's flag that makes it fail rather than
// start a sign-in. A completion adds it to airo credential.
const NoSignInFlag = "--no-sign-in"

// completionScope chooses the context and project a completion lists in,
// from the kubeconfig alone: cobra runs no PersistentPreRunE for a
// completion. As kubectl's SetFactoryForCompletion, it is set once at start,
// because the selection belongs to a package that imports this one.
var completionScope func(*genericclioptions.ConfigFlags) error

// SetCompletionScope sets how a completion chooses its context and project.
func SetCompletionScope(scope func(*genericclioptions.ConfigFlags) error) {
	completionScope = scope
}

// PrepareCompletion readies flags for a completion's requests. It reports
// false when the completion should offer nothing.
func PrepareCompletion(flags *genericclioptions.ConfigFlags) bool {
	if completionScope != nil && completionScope(flags) != nil {
		return false
	}
	flags.WrapConfigFn = quietConfig
	return true
}

// quietConfig keeps a completion's requests short and silent.
func quietConfig(c *rest.Config) *rest.Config {
	c.Timeout = CompletionTimeout
	if e := c.ExecProvider; e != nil {
		e = e.DeepCopy()
		e.InteractiveMode = clientcmdapi.NeverExecInteractiveMode
		if e.Command == "airo" && !slices.Contains(e.Args, NoSignInFlag) {
			e.Args = append(e.Args, NoSignInFlag)
		}
		c.ExecProvider = e
	}
	return c
}

// Names lists the names of a kind that start with a prefix: in the selected
// project when the kind is namespaced. selector, when not nil, gives a label
// selector after the completion's scope is chosen.
func Names(flags *genericclioptions.ConfigFlags, kind string, selector func() string) func(prefix string) []string {
	return func(prefix string) []string {
		if !PrepareCompletion(flags) {
			return nil
		}
		m, err := mapping(flags, kind)
		if err != nil {
			return nil
		}
		rc, err := flags.ToRESTConfig()
		if err != nil {
			return nil
		}
		c, err := metadata.NewForConfig(rc)
		if err != nil {
			return nil
		}
		var r metadata.ResourceInterface = c.Resource(m.Resource)
		if m.Scope.Name() == meta.RESTScopeNameNamespace {
			ns, _, err := flags.ToRawKubeConfigLoader().Namespace()
			if err != nil {
				return nil
			}
			r = c.Resource(m.Resource).Namespace(ns)
		}
		opts := metav1.ListOptions{}
		if selector != nil {
			opts.LabelSelector = selector()
		}
		ctx, cancel := context.WithTimeout(context.Background(), CompletionTimeout)
		defer cancel()
		list, err := r.List(ctx, opts)
		if err != nil {
			return nil
		}
		var names []string
		for _, item := range list.Items {
			names = append(names, item.Name)
		}
		return WithPrefix(names, prefix)
	}
}

// WithPrefix returns the words that start with prefix, sorted.
func WithPrefix(words []string, prefix string) []string {
	var out []string
	for _, w := range words {
		if strings.HasPrefix(w, prefix) {
			out = append(out, w)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// FirstArg completes a command's first argument from list, and nothing after.
func FirstArg(list func(prefix string) []string) cobra.CompletionFunc {
	return func(_ *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
		if len(args) > 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		return list(toComplete), cobra.ShellCompDirectiveNoFileComp
	}
}

// FlagValue completes a flag's value from list.
func FlagValue(list func(prefix string) []string) cobra.CompletionFunc {
	return func(_ *cobra.Command, _ []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
		return list(toComplete), cobra.ShellCompDirectiveNoFileComp
	}
}

// CompleteFlag registers list for the flag name on cmd. A flag the command
// does not have is a programming error, caught by the tests.
func CompleteFlag(cmd *cobra.Command, name string, list func(prefix string) []string) {
	if err := cmd.RegisterFlagCompletionFunc(name, FlagValue(list)); err != nil {
		panic(err)
	}
}

// verbs are the verbs every kind has, as the root command takes them.
var verbs = []string{"list", "get", "describe", "edit", "delete"}

// completeKindWords completes airo KIND VERB NAME for kinds without a
// command of their own: the kinds, then the verbs, then the names.
func completeKindWords(flags *genericclioptions.ConfigFlags) cobra.CompletionFunc {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
		switch {
		case len(args) == 0:
			return kinds(cmd, flags, toComplete), cobra.ShellCompDirectiveNoFileComp
		case len(args) == 1:
			return WithPrefix(verbs, toComplete), cobra.ShellCompDirectiveNoFileComp
		case len(args) == 2 && args[1] != "list":
			return Names(flags, args[0], nil)(toComplete), cobra.ShellCompDirectiveNoFileComp
		}
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
}

// kinds lists the plural names of the kinds the cluster lists, as kubectl
// api-resources does, without the ones an airo command already names. A
// plural that two groups serve is offered as resource.group.
func kinds(root *cobra.Command, flags *genericclioptions.ConfigFlags, prefix string) []string {
	if !PrepareCompletion(flags) {
		return nil
	}
	dc, err := flags.ToDiscoveryClient()
	if err != nil {
		return nil
	}
	// Discovery is cached on disk, as for kubectl. A group that fails still
	// leaves the others' kinds.
	lists, _ := dc.ServerPreferredResources()
	taken := map[string]bool{}
	for _, c := range root.Commands() {
		taken[c.Name()] = true
		for _, a := range c.Aliases {
			taken[a] = true
		}
	}
	groups := map[string][]string{}
	for _, l := range lists {
		gv, err := schema.ParseGroupVersion(l.GroupVersion)
		if err != nil {
			continue
		}
		for _, r := range l.APIResources {
			if strings.Contains(r.Name, "/") || !slices.Contains(r.Verbs, "list") ||
				taken[r.Name] || taken[r.SingularName] || taken[strings.ToLower(r.Kind)] {
				continue
			}
			groups[r.Name] = append(groups[r.Name], gv.Group)
		}
	}
	var words []string
	for name, gs := range groups {
		if len(gs) == 1 {
			words = append(words, name)
			continue
		}
		for _, g := range gs {
			words = append(words, strings.TrimSuffix(name+"."+g, "."))
		}
	}
	return WithPrefix(words, prefix)
}
