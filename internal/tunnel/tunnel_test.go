package tunnel

import (
	"context"
	"testing"

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
