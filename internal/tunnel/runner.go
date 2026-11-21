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
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/rest"
	kube "sigs.k8s.io/controller-runtime/pkg/client"
)

// TemporaryAgentLabel marks a reserved relay policy and its short-lived Tunnel.
const TemporaryAgentLabel = "airohq.dev/temporary-agent"

// CallRule grants access to the caller endpoint of one project Tunnel.
func CallRule(name string) rbacv1.PolicyRule {
	return rbacv1.PolicyRule{APIGroups: []string{tunnelGVK.Group}, Resources: []string{"tunnels"}, ResourceNames: []string{name}, Verbs: []string{"call"}}
}

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

// OwnedRunnerTunnel resolves the dedicated, durable Tunnel for a local Agent.
func OwnedRunnerTunnel(ctx context.Context, c kube.Reader, name, origin string, a *unstructured.Unstructured) (RunnerTunnel, error) {
	t := build(a.GetNamespace(), name, spec{})
	if err := c.Get(ctx, kube.ObjectKeyFromObject(t), t); err != nil {
		return RunnerTunnel{}, err
	}
	if !metav1.IsControlledBy(t, a) || str(t, "status", "callerURL") != origin {
		return RunnerTunnel{}, fmt.Errorf("tunnel %s is not the Agent's dedicated relay", kube.ObjectKeyFromObject(t))
	}
	return readRunnerTunnel(t)
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

// RunnerTunnelAvailable skips occupied relays before publishing a temporary
// Agent. The subsequent Tunnel create arbitrates concurrent claims.
func RunnerTunnelAvailable(ctx context.Context, c kube.Reader, ns, name string) (bool, error) {
	t := build(ns, name, spec{})
	err := c.Get(ctx, kube.ObjectKeyFromObject(t), t)
	if apierrors.IsNotFound(err) {
		return true, nil
	}
	return false, err
}

// CreateTemporaryRunnerTunnel creates the Agent's dependent Tunnel and waits for its URLs.
func CreateTemporaryRunnerTunnel(ctx context.Context, c kube.Client, name, origin string, agent *unstructured.Unstructured) (RunnerTunnel, error) {
	t, err := createAgentTunnel(ctx, c, agent, name, true)
	if apierrors.IsAlreadyExists(err) {
		return RunnerTunnel{}, ErrTunnelInUse
	}
	if err != nil {
		return RunnerTunnel{}, err
	}
	if str(t, "status", "callerURL") != origin {
		return RunnerTunnel{}, fmt.Errorf("temporary tunnel %s does not match its reserved Harness URL %s", kube.ObjectKeyFromObject(t), origin)
	}
	return readRunnerTunnel(t)
}

// CreateLocalAgentTunnel returns the private origin for the Agent's relay policy.
func CreateLocalAgentTunnel(ctx context.Context, c kube.Client, agent *unstructured.Unstructured) (string, error) {
	t, err := createAgentTunnel(ctx, c, agent, agent.GetName(), false)
	if err != nil {
		return "", err
	}
	if _, err := readRunnerTunnel(t); err != nil {
		return "", err
	}
	origin := str(t, "status", "callerURL")
	u, err := url.Parse(origin)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("tunnel %s needs a valid status.callerURL origin", kube.ObjectKeyFromObject(t))
	}
	return origin, nil
}

func createAgentTunnel(ctx context.Context, c kube.Client, agent *unstructured.Unstructured, name string, temporary bool) (*unstructured.Unstructured, error) {
	t := build(agent.GetNamespace(), name, spec{})
	if temporary {
		t.SetLabels(map[string]string{TemporaryAgentLabel: "true"})
	}
	owner := metav1.NewControllerRef(agent, agent.GroupVersionKind())
	owner.BlockOwnerDeletion = nil
	t.SetOwnerReferences([]metav1.OwnerReference{*owner})
	if err := c.Create(ctx, t); err != nil {
		return nil, fmt.Errorf("create tunnel %s: %w", kube.ObjectKeyFromObject(t), err)
	}
	uid := t.GetUID()
	if err := waitReady(ctx, c, t); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if t.GetUID() != uid || !metav1.IsControlledBy(t, agent) {
		return nil, fmt.Errorf("tunnel %s was replaced", kube.ObjectKeyFromObject(t))
	}
	return t, nil
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
