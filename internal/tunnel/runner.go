package tunnel

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/andydunstall/piko/client"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/rest"
	kube "sigs.k8s.io/controller-runtime/pkg/client"
)

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
		public, _, err := unstructured.NestedBool(t.Object, "spec", "public")
		auth, _, authErr := unstructured.NestedString(t.Object, "spec", "callerAuth")
		if err != nil || authErr != nil || public || (auth != "" && auth != "kubernetes") {
			return RunnerTunnel{}, fmt.Errorf("runner tunnel %s/%s must be private with Kubernetes caller authentication", ns, t.GetName())
		}
		if result.Name != "" {
			return RunnerTunnel{}, fmt.Errorf("multiple tunnels in %s advertise %s", ns, origin)
		}
		result = RunnerTunnel{Name: t.GetName(), Namespace: ns, UID: string(t.GetUID()), ClientURL: str(t, "status", "clientURL")}
		u, err := url.Parse(result.ClientURL)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || result.UID == "" {
			return RunnerTunnel{}, fmt.Errorf("tunnel %s/%s needs a UID and an HTTPS status.clientURL", ns, t.GetName())
		}
	}
	if result.Name == "" {
		return result, fmt.Errorf("no tunnel in project %s advertises the Harness URL %s as status.callerURL", ns, origin)
	}
	return result, nil
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
