// Tenant contexts share the cluster and user of a sign-in. The namespace on
// each context is that tenant's remembered project.
package login

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/spf13/cobra"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/airohq/airo-cli/internal/generic"
)

const tenantLabel = "capsule.clastix.io/tenant"
const tenantExtension = "airohq.dev/tenant"

func storedTenant(c *clientcmdapi.Context) (string, error) {
	if c == nil || c.Extensions[tenantExtension] == nil {
		return "", nil
	}
	raw, ok := c.Extensions[tenantExtension].(*runtime.Unknown)
	var name string
	if !ok || json.Unmarshal(raw.Raw, &name) != nil || name == "" || len(validation.IsDNS1123Subdomain(name)) != 0 {
		return "", fmt.Errorf("invalid saved tenant in kubeconfig")
	}
	return name, nil
}

// tenantContext finds or prepares the context in memory. Only commands that
// persist a selection write this config back.
func tenantContext(cfg *clientcmdapi.Config, base, tenant string) (string, bool, error) {
	if problems := validation.IsDNS1123Subdomain(tenant); len(problems) > 0 {
		return "", false, fmt.Errorf("invalid tenant %q: %s", tenant, strings.Join(problems, ", "))
	}
	source := cfg.Contexts[base]
	if source == nil {
		return "", false, fmt.Errorf("select a sign-in with airo login")
	}
	for name, c := range cfg.Contexts {
		if c.Cluster == source.Cluster && c.AuthInfo == source.AuthInfo {
			saved, err := storedTenant(c)
			if err != nil {
				return "", false, err
			}
			if saved == tenant {
				return name, false, nil
			}
		}
	}
	name := url.PathEscape(source.Cluster) + "/" + url.PathEscape(source.AuthInfo) + "/" + tenant
	if cfg.Contexts[name] != nil {
		return "", false, fmt.Errorf("kubeconfig already contains an unrelated context named %q", name)
	}
	c := source.DeepCopy()
	c.Namespace = tenant + defaultProjectSuffix
	if c.Extensions == nil {
		c.Extensions = make(map[string]runtime.Object)
	}
	data, _ := json.Marshal(tenant)
	c.Extensions[tenantExtension] = &runtime.Unknown{Raw: data}
	cfg.Contexts[name] = c
	return name, true, nil
}

// Selection belongs to one CLI invocation.
type Selection struct {
	Tenant  string
	project string
}

func selectedContext(cfg *clientcmdapi.Config, flags *genericclioptions.ConfigFlags) string {
	if *flags.Context != "" {
		return *flags.Context
	}
	return cfg.CurrentContext
}

func tenantSelector(tenant string) string {
	if tenant == "" {
		return ""
	}
	return labels.Set{tenantLabel: tenant}.AsSelector().String()
}

func checkTenant(ctx context.Context, c dynamic.Interface, tenant string) error {
	_, err := c.Resource(tenantResource).Get(ctx, tenant, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("read tenant %s: %w", tenant, err)
	}
	return nil
}

// hasProject checks the platform's ownership label even when the caller has
// permission to use projects from more than one tenant.
func hasProject(ctx context.Context, c dynamic.Interface, tenant, project string) (bool, error) {
	if project == "" {
		return false, nil
	}
	ns, err := c.Resource(projectResource).Get(ctx, project, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read project %s: %w", project, err)
	}
	return ns.GetLabels()[tenantLabel] == tenant, nil
}

func requireProject(ctx context.Context, c dynamic.Interface, tenant, project string) error {
	ok, err := hasProject(ctx, c, tenant, project)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("project %q is unavailable in tenant %s: choose one with airo project list --tenant %s", project, tenant, tenant)
	}
	return nil
}

func (s *Selection) Resolve(cmd *cobra.Command, args []string, flags *genericclioptions.ConfigFlags) error {
	top := cmd
	for top.Parent() != nil && top.Parent() != cmd.Root() {
		top = top.Parent()
	}
	switch top.Name() {
	case "login", "credential", "context", "options", "version", "help", "completion", "__complete", "__completeNoDesc":
		return nil
	}
	// These nouns print usage without contacting the platform.
	if cmd == top && (top.Name() == "agent" || top.Name() == "session") {
		return nil
	}
	if cmd.CommandPath() == "airo agent skill search" || cmd.CommandPath() == "airo agent skill" {
		return nil
	}
	if cmd == cmd.Root() && len(args) <= 1 {
		return nil
	}
	cfg, base, created, explicit, err := s.fromKubeconfig(flags)
	if err != nil || s.Tenant == "" {
		return err
	}
	switch top.Name() {
	case "whoami", "tenant", "project", "api-resources":
		// Tenant and project operations can run before the first project exists.
		return nil
	}
	if created && *flags.Namespace == "" {
		c, err := selectionClient(flags)
		if err != nil {
			return err
		}
		s.project, err = firstProject(cmd.Context(), c, s.Tenant, cfg.Contexts[base], s.project)
		if err != nil {
			return err
		}
	}
	if top.Name() == "status" {
		return nil
	}
	all, _ := cmd.Flags().GetBool("all-namespaces")
	if all {
		if explicit {
			return fmt.Errorf("choose either a tenant or a cluster-wide list with -A")
		}
		return nil
	}
	*flags.Namespace = s.project
	if cmd == cmd.Root() {
		namespaced, err := generic.Namespaced(flags, args[0])
		if err != nil || !namespaced {
			return err
		}
	}
	if top.Name() == "apply" || top.Name() == "create" {
		// The manifest builder checks only the entries that need a project.
		return nil
	}
	return s.CheckProject(cmd.Context(), flags)
}

// fromKubeconfig chooses the sign-in's context, tenant and project from the
// kubeconfig alone. It leaves the tenant empty when none is selected, and
// reports whether the tenant's context is new and whether --tenant chose it.
func (s *Selection) fromKubeconfig(flags *genericclioptions.ConfigFlags) (cfg *clientcmdapi.Config, base string, created, explicit bool, err error) {
	cfg, err = flags.ToRawKubeConfigLoader().ConfigAccess().GetStartingConfig()
	if err != nil {
		return nil, "", false, false, err
	}
	base = selectedContext(cfg, flags)
	// Pin the sign-in for the entire request, including when another
	// terminal switches the kubeconfig's current context mid-command.
	*flags.Context = base
	explicit = s.Tenant != ""
	if !explicit {
		s.Tenant, err = storedTenant(cfg.Contexts[base])
		if err != nil || s.Tenant == "" {
			return cfg, base, false, false, err
		}
	}
	name, created, err := tenantContext(cfg, base, s.Tenant)
	if err != nil {
		return nil, "", false, false, err
	}
	if !created {
		*flags.Context = name
	}
	s.project = cfg.Contexts[name].Namespace
	if *flags.Namespace != "" {
		s.project = *flags.Namespace
	}
	return cfg, base, created, explicit, nil
}

// CompletionScope selects the context and project for a shell completion
// from the kubeconfig alone, without the checks Resolve asks the cluster for.
func (s *Selection) CompletionScope(flags *genericclioptions.ConfigFlags) error {
	if _, _, _, _, err := s.fromKubeconfig(flags); err != nil || s.Tenant == "" {
		return err
	}
	*flags.Namespace = s.project
	return nil
}

// CheckProject verifies the effective namespace before a project-scoped operation.
func (s *Selection) CheckProject(ctx context.Context, flags *genericclioptions.ConfigFlags) error {
	if s.Tenant == "" {
		return nil
	}
	if s.project == "" {
		return fmt.Errorf("tenant %s has no project selected: choose one with airo project", s.Tenant)
	}
	c, err := selectionClient(flags)
	if err != nil {
		return err
	}
	return requireProject(ctx, c, s.Tenant, s.project)
}

func selectionClient(flags *genericclioptions.ConfigFlags) (dynamic.Interface, error) {
	rc, err := flags.ToRESTConfig()
	if err != nil {
		return nil, err
	}
	return dynamic.NewForConfig(rc)
}

// selectTenant verifies the tenant and project before saving either. A missing
// remembered project leaves tenant-level operations available.
func selectTenant(ctx context.Context, cfg *clientcmdapi.Config, base, tenant, project string, rc *rest.Config) error {
	name, created, err := tenantContext(cfg, base, tenant)
	if err != nil {
		return err
	}
	c, err := dynamic.NewForConfig(rc)
	if err != nil {
		return err
	}
	if err := checkTenant(ctx, c, tenant); err != nil {
		return err
	}
	kctx := cfg.Contexts[name]
	if project != "" {
		if err := requireProject(ctx, c, tenant, project); err != nil {
			return err
		}
		kctx.Namespace = project
	} else {
		var previous *clientcmdapi.Context
		if created {
			previous = cfg.Contexts[base]
		}
		kctx.Namespace, err = firstProject(ctx, c, tenant, previous, kctx.Namespace)
		if err != nil {
			return err
		}
	}
	cfg.CurrentContext = name
	return nil
}

// Use an existing sign-in's project when first adopting tenant contexts.
func firstProject(ctx context.Context, c dynamic.Interface, tenant string, previous *clientcmdapi.Context, project string) (string, error) {
	candidates := []string{project}
	if previous != nil && previous.Extensions[tenantExtension] == nil && previous.Namespace != "" && previous.Namespace != project {
		candidates = append([]string{previous.Namespace}, candidates...)
	}
	for _, candidate := range candidates {
		ok, err := hasProject(ctx, c, tenant, candidate)
		if err != nil && !apierrors.IsForbidden(err) {
			return "", err
		}
		if ok {
			return candidate, nil
		}
	}
	return "", nil
}

// configEdit keeps the original snapshot for client-go's locked diff/write.
// A switch or browser login must not undo an unrelated edit from another terminal.
type configEdit struct {
	clientcmd.ConfigAccess
	original *clientcmdapi.Config
}

func (e configEdit) GetStartingConfig() (*clientcmdapi.Config, error) { return e.original, nil }

func (e configEdit) save(cfg *clientcmdapi.Config) error {
	return clientcmd.ModifyConfig(e, *cfg, true)
}
