package tunnel

import (
	"context"
	"errors"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/airohq/airo-cli/internal/expiry"
)

func TestTarget(t *testing.T) {
	for arg, want := range map[string]string{
		"3000":           "localhost:3000",
		"localhost:3000": "localhost:3000",
		"10.0.0.5:8080":  "10.0.0.5:8080",
	} {
		if got, _, err := target(arg); err != nil || got != want {
			t.Errorf("target(%q) = %q, %v; want %q", arg, got, err, want)
		}
	}
	for _, arg := range []string{"", "my-app", "99999", ":3000", "localhost:"} {
		if got, _, err := target(arg); err == nil {
			t.Errorf("target(%q) = %q, want an error", arg, got)
		}
	}
}

func TestDNSLabel(t *testing.T) {
	for in, want := range map[string]string{
		"sam.smith-3000":  "sam-smith-3000",
		"Sams_MacBook":    "sams-macbook",
		"DOMAIN\\me-3000": "domain-me-3000",
		"---":             "fallback",
	} {
		if got := dnsLabel(in, "fallback"); got != want {
			t.Errorf("dnsLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

func existing(public bool, callerAuth string) *unstructured.Unstructured {
	t := &unstructured.Unstructured{}
	t.SetGroupVersionKind(tunnelGVK)
	t.SetNamespace("team")
	t.SetName("my-app")
	t.Object["spec"] = map[string]any{"public": public, "callerAuth": callerAuth}
	return t
}

func TestEnsureCreatesAMissingTunnel(t *testing.T) {
	c := fake.NewClientBuilder().Build()
	yes := true
	got, created, err := ensure(context.Background(), c, "team", "my-app", spec{public: &yes, noAuth: &yes})
	if err != nil || !created {
		t.Fatalf("created = %v, err = %v", created, err)
	}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(got), got); err != nil {
		t.Fatal(err)
	}
	if public, _, _ := unstructured.NestedBool(got.Object, "spec", "public"); !public || str(got, "spec", "callerAuth") != "none" {
		t.Errorf("spec = %v, want public with callerAuth none", got.Object["spec"])
	}
}

func TestEnsureLeavesCallerAuthToTheAPIDefault(t *testing.T) {
	c := fake.NewClientBuilder().Build()
	got, _, err := ensure(context.Background(), c, "team", "my-app", spec{})
	if err != nil {
		t.Fatal(err)
	}
	if s, _, _ := unstructured.NestedMap(got.Object, "spec"); len(s) != 0 {
		t.Errorf("spec = %v, want empty", s)
	}
}

func TestEnsureUsesAnExistingTunnelAsItIs(t *testing.T) {
	c := fake.NewClientBuilder().WithObjects(existing(true, "none")).Build()
	if _, created, err := ensure(context.Background(), c, "team", "my-app", spec{}); err != nil || created {
		t.Fatalf("created = %v, err = %v", created, err)
	}
	yes := true
	if _, _, err := ensure(context.Background(), c, "team", "my-app", spec{public: &yes, noAuth: &yes}); err != nil {
		t.Fatalf("matching flags: %v", err)
	}
}

func TestEnsureRefusesToChangeAnExistingTunnel(t *testing.T) {
	c := fake.NewClientBuilder().WithObjects(existing(false, "kubernetes")).Build()
	yes := true
	for _, want := range []spec{{public: &yes}, {noAuth: &yes}} {
		if _, _, err := ensure(context.Background(), c, "team", "my-app", want); err == nil {
			t.Errorf("ensure(%+v) changed or accepted a private tunnel", want)
		}
	}
	got := existing(false, "")
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(got), got); err != nil {
		t.Fatal(err)
	}
	if public, _, _ := unstructured.NestedBool(got.Object, "spec", "public"); public {
		t.Error("the existing tunnel became public")
	}
}

// expiresIn is how far ahead the Tunnel's expiry in the cluster is.
func expiresIn(t *testing.T, c client.Client) time.Duration {
	t.Helper()
	got := existing(false, "")
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(got), got); err != nil {
		t.Fatal(err)
	}
	at, err := time.Parse(time.RFC3339, got.GetAnnotations()[expiry.Annotation])
	if err != nil {
		t.Fatalf("annotations = %v: %v", got.GetAnnotations(), err)
	}
	return time.Until(at)
}

func TestEnsureGivesANewTunnelAnExpiry(t *testing.T) {
	c := fake.NewClientBuilder().Build()
	if _, _, err := ensure(context.Background(), c, "team", "my-app", spec{}); err != nil {
		t.Fatal(err)
	}
	if in := expiresIn(t, c); in < 15*time.Minute-time.Minute || in > 15*time.Minute {
		t.Errorf("expires in %s, want about %s", in, 15*time.Minute)
	}
}

func TestEnsureGivesAnExistingTunnelNoExpiry(t *testing.T) {
	c := fake.NewClientBuilder().WithObjects(existing(false, "kubernetes")).Build()
	got, _, err := ensure(context.Background(), c, "team", "my-app", spec{})
	if err != nil {
		t.Fatal(err)
	}
	if a := got.GetAnnotations(); len(a) != 0 {
		t.Errorf("annotations = %v, want none", a)
	}
}

func TestExtendMovesTheExpiryForward(t *testing.T) {
	old := existing(false, "kubernetes")
	old.SetAnnotations(map[string]string{expiry.Annotation: time.Now().Add(time.Minute).UTC().Format(time.RFC3339)})
	c := fake.NewClientBuilder().WithObjects(old).Build()
	if err := expiry.Extend(context.Background(), c, old); err != nil {
		t.Fatal(err)
	}
	if in := expiresIn(t, c); in < 15*time.Minute-time.Minute {
		t.Errorf("expires in %s, want about %s", in, 15*time.Minute)
	}
}

func TestTemporaryRunnerDoesNotAdoptAnExistingTunnel(t *testing.T) {
	obj := existing(false, "kubernetes")
	obj.SetUID("another-owner")
	c := fake.NewClientBuilder().WithObjects(obj).Build()
	_, err := CreateTemporaryRunnerTunnel(t.Context(), c, obj.GetName(), "unused", obj)
	if !errors.Is(err, ErrTunnelInUse) {
		t.Fatal(err)
	}
	got := obj.DeepCopy()
	if err := c.Get(t.Context(), client.ObjectKeyFromObject(obj), got); err != nil {
		t.Fatal(err)
	}
	if got.GetUID() != obj.GetUID() || len(got.GetAnnotations()) != 0 {
		t.Fatal("existing Tunnel was changed")
	}
}

func TestTemporaryRunnerTunnelCreatedWithAgentOwner(t *testing.T) {
	agent := &unstructured.Unstructured{}
	agent.SetAPIVersion("api.kagent.dev/v1alpha3")
	agent.SetKind("Agent")
	agent.SetNamespace("team")
	agent.SetName("reviewer")
	agent.SetUID("agent-uid")
	// Inspect the initial create request. A later patch would leave a
	// crash window in which deleting the Agent could orphan its Tunnel.
	stop := errors.New("inspected create")
	c := fake.NewClientBuilder().WithInterceptorFuncs(interceptor.Funcs{
		Create: func(_ context.Context, _ client.WithWatch, obj client.Object, _ ...client.CreateOption) error {
			refs := obj.GetOwnerReferences()
			_, hasExpiry := obj.GetAnnotations()[expiry.Annotation]
			if obj.GetNamespace() != agent.GetNamespace() || len(refs) != 1 || refs[0].UID != agent.GetUID() ||
				refs[0].Name != agent.GetName() || refs[0].Kind != agent.GetKind() || refs[0].APIVersion != agent.GetAPIVersion() ||
				hasExpiry {
				t.Fatalf("unexpected initial Tunnel metadata: %+v", obj)
			}
			return stop
		},
	}).Build()
	_, err := CreateTemporaryRunnerTunnel(t.Context(), c, "relay", "unused", agent)
	if !errors.Is(err, stop) {
		t.Fatal(err)
	}
}

func TestPortForwardLeavesTemporaryAgentTunnelAlone(t *testing.T) {
	obj := existing(false, "kubernetes")
	obj.SetLabels(map[string]string{TemporaryAgentLabel: "true"})
	c := fake.NewClientBuilder().WithObjects(obj).Build()
	if _, _, err := ensure(t.Context(), c, obj.GetNamespace(), obj.GetName(), spec{}); err == nil {
		t.Fatal("port forward adopted a temporary agent Tunnel")
	}
}
