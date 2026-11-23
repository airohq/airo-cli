package agent

import (
	"context"
	"errors"
	"fmt"
	"time"

	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/airohq/airo-cli/internal/tunnel"
)

// This in-cluster image forwards remote harness protocol v1. The sandbox
// image selected by agent connect runs separately on the serving machine.
const relayImage = "ghcr.io/airohq/kagent-remote-harness@sha256:d94b644f9d87dfdecbba3090775a1b65a22d632db160971f4a1e1c7c7de0a882"

func localSubstrate(ctx context.Context, c client.Client, ns string) (map[string]any, error) {
	items, err := list(ctx, c, ns, harnesses)
	if err != nil {
		return nil, err
	}
	h := newestDefault(items, harnesses.marker)
	if len(items) == 1 {
		h = &items[0]
	}
	if h == nil {
		return nil, fmt.Errorf("project %s needs a default Harness to supply the local relay's worker pool and snapshot location", ns)
	}
	pool := detailString(h.Object, "spec", "substrate", "workerPoolRef", "name")
	location := detailString(h.Object, "spec", "substrate", "snapshotPolicy", "location")
	if pool == "" || location == "" {
		return nil, fmt.Errorf("Harness %s/%s needs spec.substrate.workerPoolRef.name and spec.substrate.snapshotPolicy.location for a local relay", ns, h.GetName())
	}
	return map[string]any{"workerPoolRef": map[string]any{"name": pool}, "snapshotPolicy": map[string]any{"location": location}}, nil
}

func provisionLocalRelay(ctx context.Context, c client.Client, a *unstructured.Unstructured, substrate map[string]any) (err error) {
	// Every child carries the Agent UID in its initial CREATE request. A
	// rollback therefore needs a single guarded delete, even on a collision.
	defer func() {
		if err == nil {
			return
		}
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		uid := a.GetUID()
		if e := c.Delete(cleanup, a, client.Preconditions{UID: &uid}); e != nil && !apierrors.IsNotFound(e) {
			err = errors.Join(err, fmt.Errorf("clean up Agent %s: %w", client.ObjectKeyFromObject(a), e))
		}
	}()
	origin, err := tunnel.CreateLocalAgentTunnel(ctx, c, a)
	if err != nil {
		return err
	}
	name, ns := a.GetName(), a.GetNamespace()
	owner := metav1.NewControllerRef(a, agentGVK)
	owner.BlockOwnerDeletion = nil
	// The project admission policy reserves this role name for a single
	// Tunnel call grant. It cannot resolve to an unrelated existing Role.
	roleName := "airo-local-call-" + string(a.GetUID())
	policy := &unstructured.Unstructured{}
	policy.SetGroupVersionKind(egressPolicyGVK)
	policy.Object["spec"] = map[string]any{"destinations": []any{map[string]any{
		"origin": origin, "authentication": map[string]any{
			"header": "Authorization", "prefix": "Bearer ", "actorJwt": map[string]any{"audiences": []any{origin}},
		},
	}}}
	h := &unstructured.Unstructured{}
	h.SetGroupVersionKind(harnessGVK)
	h.Object["spec"] = map[string]any{
		"byo": map[string]any{}, "substrate": substrate,
		"workload": map[string]any{"image": relayImage, "command": []any{"/harness"}},
		"env":      []any{map[string]any{"name": "REMOTE_HARNESS_URL", "value": origin}},
	}
	role := &rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: roleName}, Rules: []rbacv1.PolicyRule{tunnel.CallRule(name)}}
	binding := &rbacv1.RoleBinding{
		RoleRef:  rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: roleName},
		Subjects: []rbacv1.Subject{{Kind: rbacv1.GroupKind, APIGroup: rbacv1.GroupName, Name: "substrate:atespace:" + ns}},
	}
	for _, obj := range []client.Object{policy, role, binding, h} {
		if obj.GetName() == "" {
			obj.SetName(name)
		}
		obj.SetNamespace(ns)
		obj.SetOwnerReferences([]metav1.OwnerReference{*owner})
		if err := c.Create(ctx, obj); err != nil {
			kind, _ := c.GroupVersionKindFor(obj)
			return fmt.Errorf("create %s %s/%s for local Agent: %w", kind.Kind, ns, obj.GetName(), err)
		}
	}
	return nil
}
