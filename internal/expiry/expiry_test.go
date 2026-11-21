package expiry

import (
	"context"
	"io"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestKeepAliveRenewsAgentUntilDeleted(t *testing.T) {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(schema.GroupVersionKind{Group: "example.org", Version: "v1", Kind: "Agent"})
	obj.SetNamespace("project")
	obj.SetName("demo")
	obj.SetUID("original")
	obj.SetAnnotations(map[string]string{Annotation: time.Now().UTC().Format(time.RFC3339)})
	c := fake.NewClientBuilder().WithObjects(obj).Build()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- keepAlive(ctx, io.Discard, c, time.Millisecond, time.Millisecond, obj) }()
	for {
		got := obj.DeepCopy()
		if err := c.Get(ctx, client.ObjectKeyFromObject(obj), got); err != nil {
			t.Fatal(err)
		}
		at, err := time.Parse(time.RFC3339, got.GetAnnotations()[Annotation])
		if err != nil {
			t.Fatal(err)
		}
		if time.Until(at) > lifetime-time.Minute {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if err := c.Delete(ctx, obj); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err == nil {
		t.Fatal("continued after the Agent was deleted")
	}
}
