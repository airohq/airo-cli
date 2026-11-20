package tunnel

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
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
	at, err := time.Parse(time.RFC3339, got.GetAnnotations()[expiresAnnotation])
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
	if in := expiresIn(t, c); in < expiresAfter-time.Minute || in > expiresAfter {
		t.Errorf("expires in %s, want about %s", in, expiresAfter)
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
	old.SetAnnotations(map[string]string{expiresAnnotation: time.Now().Add(time.Minute).UTC().Format(time.RFC3339)})
	c := fake.NewClientBuilder().WithObjects(old).Build()
	if err := extend(context.Background(), c, "team", "my-app", old.GetUID()); err != nil {
		t.Fatal(err)
	}
	if in := expiresIn(t, c); in < expiresAfter-time.Minute {
		t.Errorf("expires in %s, want about %s", in, expiresAfter)
	}
}

func TestKeepAliveExtendsUntilStopped(t *testing.T) {
	old := existing(false, "kubernetes")
	old.SetAnnotations(map[string]string{expiresAnnotation: time.Now().Add(time.Minute).UTC().Format(time.RFC3339)})
	c := fake.NewClientBuilder().WithObjects(old).Build()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- keepAlive(ctx, io.Discard, c, "team", "my-app", "", time.Millisecond, time.Millisecond)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for expiresIn(t, c) < expiresAfter-time.Minute {
		if time.Now().After(deadline) {
			t.Fatal("the expiry did not move forward")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Errorf("keepAlive after cancel = %v, want nil", err)
	}
}

func TestKeepAliveStopsWhenTheTunnelIsGone(t *testing.T) {
	c := fake.NewClientBuilder().Build()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := keepAlive(ctx, io.Discard, c, "team", "my-app", "", time.Millisecond, time.Millisecond); err == nil {
		t.Error("keepAlive = nil, want an error for a Tunnel that is gone")
	}
}

func TestTemporaryRunnerDoesNotAdoptAnExistingTunnel(t *testing.T) {
	obj := existing(false, "kubernetes")
	obj.SetUID("another-owner")
	c := fake.NewClientBuilder().WithObjects(obj).Build()
	err := WithTemporaryRunnerTunnel(t.Context(), io.Discard, c, obj.GetNamespace(), obj.GetName(), "unused", nil)
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

func TestPortForwardLeavesTemporaryAgentTunnelAlone(t *testing.T) {
	obj := existing(false, "kubernetes")
	obj.SetAnnotations(map[string]string{expiresAnnotation: expiry(time.Now())})
	obj.SetLabels(map[string]string{TemporaryAgentLabel: "true"})
	c := fake.NewClientBuilder().WithObjects(obj).Build()
	if _, _, err := ensure(t.Context(), c, obj.GetNamespace(), obj.GetName(), spec{}); err == nil {
		t.Fatal("port forward adopted a temporary agent Tunnel")
	}
}
