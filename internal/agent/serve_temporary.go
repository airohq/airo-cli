package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	utilrand "k8s.io/apimachinery/pkg/util/rand"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/airohq/airo-cli/internal/expiry"
)

var egressPolicyGVK = agentGVK.GroupVersion().WithKind("EgressPolicy")

// serveTemporary runs a local Agent that exists only while this command runs.
// The Agent owns its whole relay, as one made with agent create --local does,
// so deleting it, on Ctrl-C or by the janitor after its expiry, removes all.
func serveTemporary(ctx context.Context, c client.Client, ns, name, scope string, out io.Writer, serve serveAgent) error {
	substrate, err := localSubstrate(ctx, c, ns)
	if err != nil {
		return err
	}
	return withTemporaryAgent(ctx, c, ns, name, out, func(ctx context.Context, a *unstructured.Unstructured) error {
		if err := provisionLocalRelay(ctx, c, a, substrate); err != nil {
			return err
		}
		t, err := resolveAgentTunnel(ctx, c, a)
		if err != nil {
			return err
		}
		workspace := newTemporaryWorkspace(a, scope, c, out)
		return serve(ctx, "Created temporary agent "+a.GetName(), t, workspace)
	})
}

func withTemporaryAgent(ctx context.Context, c client.Client, ns, name string, out io.Writer, serve func(context.Context, *unstructured.Unstructured) error) (err error) {
	// The relay's resources share the Agent's name, so it is chosen here
	// rather than by the API server's generateName.
	if name == "" {
		name = "local-agent-" + utilrand.String(5)
	}
	a := build(ns, name, options{local: true, harness: name, prompt: "You are a helpful coding assistant."})
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
