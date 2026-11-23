package generic

import (
	"context"
	"sync"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/cli-runtime/pkg/genericclioptions"
)

// A manifest can contain both namespaced and cluster-scoped resources.
// Check the selected project only when the builder discovers a namespaced
// kind, without parsing the input twice (it may be stdin).
type manifestClient struct {
	genericclioptions.RESTClientGetter
	checkProject func() error
}

func newManifestClient(ctx context.Context, flags *genericclioptions.ConfigFlags, check func(context.Context, *genericclioptions.ConfigFlags) error) manifestClient {
	return manifestClient{flags, sync.OnceValue(func() error { return check(ctx, flags) })}
}

func (c manifestClient) ToRESTMapper() (meta.RESTMapper, error) {
	mapper, err := c.RESTClientGetter.ToRESTMapper()
	if err != nil {
		return nil, err
	}
	return manifestMapper{mapper, c.checkProject}, nil
}

type manifestMapper struct {
	meta.RESTMapper
	checkProject func() error
}

func (m manifestMapper) RESTMapping(kind schema.GroupKind, versions ...string) (*meta.RESTMapping, error) {
	mapping, err := m.RESTMapper.RESTMapping(kind, versions...)
	if err == nil && mapping.Scope.Name() == meta.RESTScopeNameNamespace {
		err = m.checkProject()
	}
	return mapping, err
}
