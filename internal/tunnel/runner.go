package tunnel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/andydunstall/piko/client"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/rest"
	kube "sigs.k8s.io/controller-runtime/pkg/client"
)

// TemporaryAgentLabel marks a reserved relay policy and its short-lived Tunnel.
const TemporaryAgentLabel = "airohq.dev/temporary-agent"

// RunnerTunnel is the existing private, authenticated Tunnel named by a
// remote Harness's URL. Serving it makes no resource changes.
type RunnerTunnel struct {
	Name, Namespace, UID, ClientURL string
}

func FindRunnerTunnel(ctx context.Context, c kube.Reader, ns, origin string) (RunnerTunnel, error) {
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(tunnelGVK.GroupVersion().WithKind(tunnelGVK.Kind + "List"))
	if err := c.List(ctx, list, kube.InNamespace(ns)); err != nil {
		return RunnerTunnel{}, err
	}
	var result RunnerTunnel
	for i := range list.Items {
		t := &list.Items[i]
		caller := str(t, "status", "callerURL")
		if caller == "" || strings.TrimSuffix(caller, "/") != strings.TrimSuffix(origin, "/") {
			continue
		}
		if t.GetLabels()[TemporaryAgentLabel] == "true" {
			return RunnerTunnel{}, fmt.Errorf("temporary agent tunnel %s/%s is served by the command that created it", ns, t.GetName())
		}
		if result.Name != "" {
			return RunnerTunnel{}, fmt.Errorf("multiple tunnels in %s advertise %s", ns, origin)
		}
		var err error
		result, err = readRunnerTunnel(t)
		if err != nil {
			return RunnerTunnel{}, err
		}
	}
	if result.Name == "" {
		return result, fmt.Errorf("no tunnel in project %s advertises the Harness URL %s as status.callerURL", ns, origin)
	}
	return result, nil
}

func readRunnerTunnel(t *unstructured.Unstructured) (RunnerTunnel, error) {
	ns := t.GetNamespace()
	public, _, err := unstructured.NestedBool(t.Object, "spec", "public")
	auth, _, authErr := unstructured.NestedString(t.Object, "spec", "callerAuth")
	if err != nil || authErr != nil || public || (auth != "" && auth != "kubernetes") {
		return RunnerTunnel{}, fmt.Errorf("runner tunnel %s/%s must be private with Kubernetes caller authentication", ns, t.GetName())
	}
	result := RunnerTunnel{Name: t.GetName(), Namespace: ns, UID: string(t.GetUID()), ClientURL: str(t, "status", "clientURL")}
	u, err := url.Parse(result.ClientURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || result.UID == "" {
		return RunnerTunnel{}, fmt.Errorf("tunnel %s/%s needs a UID and an HTTPS status.clientURL", ns, t.GetName())
	}
	return result, nil
}

// ErrTunnelInUse means another invocation or a persistent Tunnel owns this name.
var ErrTunnelInUse = errors.New("tunnel already exists")

// WithTemporaryRunnerTunnel creates a private expiring Tunnel. Dependents
// created by run must carry owner as a Kubernetes owner reference.
func WithTemporaryRunnerTunnel(ctx context.Context, out io.Writer, c kube.Client, ns, name, origin string, run func(context.Context, RunnerTunnel, metav1.OwnerReference) error) (err error) {
	t := build(ns, name, spec{})
	t.SetLabels(map[string]string{TemporaryAgentLabel: "true"})
	t.SetAnnotations(map[string]string{expiresAnnotation: expiry(time.Now())})
	if err := c.Create(ctx, t); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return ErrTunnelInUse
		}
		return fmt.Errorf("create temporary tunnel %s/%s: %w", ns, name, err)
	}
	uid := t.GetUID()
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if e := c.Delete(cleanup, t, kube.Preconditions{UID: &uid}); e != nil && !apierrors.IsNotFound(e) {
			err = errors.Join(err, fmt.Errorf("delete temporary tunnel %s/%s: %w", ns, name, e))
		}
	}()
	if err := waitReady(ctx, c, t); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if t.GetUID() != uid || str(t, "status", "callerURL") != origin {
		return fmt.Errorf("temporary tunnel %s/%s no longer matches its reserved Harness URL %s", ns, name, origin)
	}
	target, err := readRunnerTunnel(t)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancelCause(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		cancel(keepAlive(ctx, out, c, ns, name, uid, refreshCheck, refreshEvery))
	}()
	defer func() { cancel(nil); <-done }()
	err = run(ctx, target, metav1.OwnerReference{APIVersion: tunnelGVK.GroupVersion().String(), Kind: tunnelGVK.Kind, Name: name, UID: uid})
	if cause := context.Cause(ctx); cause != nil && !errors.Is(cause, context.Canceled) {
		return errors.Join(err, cause)
	}
	return err
}

type httpConnection struct {
	client.Listener
	done chan error
}

func (c *httpConnection) Wait() error { return <-c.done }

// ServeHTTP admits connections from Piko directly. A local TCP listener
// would let other local processes impersonate the Tunnel gateway.
func (t RunnerTunnel) ServeHTTP(ctx context.Context, out io.Writer, cfg *rest.Config, h http.Handler) error {
	return maintain(ctx, out, cfg, t.Namespace, t.Name, t.ClientURL, func(ctx context.Context, up *client.Upstream, ep string) (connection, error) {
		ln, err := up.Listen(ctx, ep)
		if err != nil {
			return nil, err
		}
		srv := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second, BaseContext: func(net.Listener) context.Context { return ctx }}
		conn := &httpConnection{Listener: ln, done: make(chan error, 1)}
		go func() {
			conn.done <- srv.Serve(ln)
			// Close() retires the listener on credential rotation. Let an
			// established turn finish before releasing its underlying stream.
			_ = srv.Shutdown(ctx)
			_ = ln.Shutdown()
		}()
		return conn, nil
	})
}
