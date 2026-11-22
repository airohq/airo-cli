package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/airohq/airo-cli/internal/expiry"
	"github.com/airohq/airo-cli/internal/tunnel"
)

var egressPolicyGVK = agentGVK.GroupVersion().WithKind("EgressPolicy")

func serveTemporary(ctx context.Context, c client.Client, ns, name string, out io.Writer, serve serveAgent) error {
	policies := &unstructured.UnstructuredList{}
	policies.SetGroupVersionKind(egressPolicyGVK.GroupVersion().WithKind("EgressPolicyList"))
	if err := c.List(ctx, policies, client.InNamespace(ns), client.MatchingLabels{tunnel.TemporaryAgentLabel: "true"}); err != nil {
		return err
	}
	if len(policies.Items) == 0 {
		return fmt.Errorf("project %s has no relay for temporary agents: ask a project administrator to configure a remote Harness and its EgressPolicy labelled %s=true, or connect to an existing Agent with airo agent connect NAME", ns, tunnel.TemporaryAgentLabel)
	}
	sort.Slice(policies.Items, func(i, j int) bool { return policies.Items[i].GetName() < policies.Items[j].GetName() })
	for _, policy := range policies.Items {
		relay := policy.GetName()
		available, err := tunnel.RunnerTunnelAvailable(ctx, c, ns, relay)
		if err != nil {
			return err
		}
		if !available {
			continue
		}
		h := &unstructured.Unstructured{}
		h.SetGroupVersionKind(harnessGVK)
		if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: relay}, h); err != nil {
			return fmt.Errorf("read temporary agent Harness %s/%s: %w", ns, relay, err)
		}
		origin, err := remoteOrigin(h)
		if err != nil {
			return err
		}
		err = withTemporaryAgent(ctx, c, ns, name, relay, out, func(ctx context.Context, a *unstructured.Unstructured) error {
			t, err := tunnel.CreateTemporaryRunnerTunnel(ctx, c, relay, origin, a)
			if err != nil {
				return err
			}
			workspace := newTemporaryWorkspace(a, h.GetUID(), c, out)
			return serve(ctx, "Created temporary agent "+a.GetName(), t, workspace)
		})
		// Retry only a clean reservation conflict. A joined cleanup error
		// means this attempt may have left an Agent behind.
		if err != tunnel.ErrTunnelInUse {
			return err
		}
	}
	return fmt.Errorf("all temporary agent tunnels in project %s are in use; stop the other server or wait for a relay to become available", ns)
}

func withTemporaryAgent(ctx context.Context, c client.Client, ns, name, relay string, out io.Writer, serve func(context.Context, *unstructured.Unstructured) error) (err error) {
	a := build(ns, name, options{harness: relay, prompt: "You are a helpful coding assistant."})
	a.Object["spec"].(map[string]any)["egressPolicyRefs"] = relayPolicies(relay)
	if name == "" {
		a.SetGenerateName("local-agent-")
	}
	a.SetAnnotations(map[string]string{expiry.Annotation: expiry.Deadline()})
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
	ctx, cancel := context.WithCancelCause(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		cancel(expiry.KeepAlive(ctx, out, c, a))
	}()
	defer func() { cancel(nil); <-done }()
	err = serve(ctx, a)
	if cause := context.Cause(ctx); cause != nil && !errors.Is(cause, context.Canceled) {
		return errors.Join(err, cause)
	}
	return err
}
