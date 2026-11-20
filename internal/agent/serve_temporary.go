package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/airohq/airo-cli/internal/tunnel"
)

var egressPolicyGVK = agentGVK.GroupVersion().WithKind("EgressPolicy")

type serveAgent func(context.Context, string, tunnel.RunnerTunnel, string) error

func serveTemporary(ctx context.Context, c client.Client, ns string, out io.Writer, serve serveAgent) error {
	policies := &unstructured.UnstructuredList{}
	policies.SetGroupVersionKind(egressPolicyGVK.GroupVersion().WithKind("EgressPolicyList"))
	if err := c.List(ctx, policies, client.InNamespace(ns), client.MatchingLabels{tunnel.TemporaryAgentLabel: "true"}); err != nil {
		return err
	}
	if len(policies.Items) == 0 {
		return fmt.Errorf("project %s has no relay for temporary agents: ask a project administrator to configure a remote Harness and its EgressPolicy labelled %s=true, or serve an existing agent with --agent", ns, tunnel.TemporaryAgentLabel)
	}
	sort.Slice(policies.Items, func(i, j int) bool { return policies.Items[i].GetName() < policies.Items[j].GetName() })
	for _, policy := range policies.Items {
		relay := policy.GetName()
		h := &unstructured.Unstructured{}
		h.SetGroupVersionKind(harnessGVK)
		if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: relay}, h); err != nil {
			return fmt.Errorf("read temporary agent Harness %s/%s: %w", ns, relay, err)
		}
		origin, err := remoteOrigin(h)
		if err != nil {
			return err
		}
		err = tunnel.WithTemporaryRunnerTunnel(ctx, out, c, ns, relay, origin, func(ctx context.Context, t tunnel.RunnerTunnel, owner metav1.OwnerReference) error {
			return withTemporaryAgent(ctx, c, ns, relay, owner, func(name string) error {
				// A stable per-relay workspace can resume across cold starts and
				// avoids leaving a new named volume after each invocation.
				return serve(ctx, name, t, string(h.GetUID()))
			})
		})
		if !errors.Is(err, tunnel.ErrTunnelInUse) {
			return err
		}
	}
	return fmt.Errorf("all temporary agent tunnels in project %s are in use; stop the other server or wait for its tunnel to expire", ns)
}

func withTemporaryAgent(ctx context.Context, c client.Client, ns, relay string, owner metav1.OwnerReference, serve func(string) error) (err error) {
	a := build(ns, "", options{harness: relay, prompt: "You are a helpful coding assistant."})
	a.SetGenerateName("local-agent-")
	a.SetOwnerReferences([]metav1.OwnerReference{owner})
	if err := unstructured.SetNestedSlice(a.Object, []any{map[string]any{"name": relay}}, "spec", "egressPolicyRefs"); err != nil {
		return err
	}
	if err := c.Create(ctx, a); err != nil {
		return fmt.Errorf("create temporary agent in project %s: %w", ns, err)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		uid := a.GetUID()
		if e := c.Delete(cleanup, a, client.Preconditions{UID: &uid}); e != nil && !apierrors.IsNotFound(e) {
			err = errors.Join(err, fmt.Errorf("delete temporary agent %s/%s: %w", ns, a.GetName(), e))
		}
	}()
	return serve(a.GetName())
}
