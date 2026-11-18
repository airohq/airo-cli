package agent

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestChatDetailsResolveConfiguredModel(t *testing.T) {
	actual := model("default-model")
	actual.Object["spec"] = map[string]any{"model": "claude-sonnet-4-5"}
	shared := actual.DeepCopy()
	shared.SetNamespace("shared")
	template := resource(templateGVK, "review-template", map[string]any{
		"modelConfig": map[string]any{"name": "default-model", "namespace": "shared"},
	})
	for _, tc := range []struct {
		name    string
		objects []client.Object
		want    chatDetails
	}{
		{"inline", []client.Object{build("team", "reviewer", options{harness: "claude", model: "default-model"}), actual}, chatDetails{"reviewer", "claude", "claude-sonnet-4-5"}},
		{"template and namespace", []client.Object{build("team", "reviewer", options{harness: "claude", template: "review-template"}), template, shared}, chatDetails{"reviewer", "claude", "claude-sonnet-4-5"}},
		{"no configured model", []client.Object{build("team", "reviewer", options{harness: "custom"})}, chatDetails{"reviewer", "custom", "not configured"}},
		{"missing model", []client.Object{build("team", "reviewer", options{harness: "claude", model: "default-model"})}, chatDetails{"reviewer", "claude", "unknown (default-model)"}},
		{"missing template", []client.Object{build("team", "reviewer", options{harness: "claude", template: "missing"})}, chatDetails{"reviewer", "claude", "unknown"}},
		{"missing agent", nil, chatDetails{"reviewer", "unknown", "unknown"}},
		{"unknown schema", []client.Object{agentNamed("reviewer", false)}, chatDetails{"reviewer", "unknown", "unknown"}},
		{"malformed reference", []client.Object{resource(agentGVK, "reviewer", map[string]any{
			"harnessRef": map[string]any{"name": "claude"},
			"template":   map[string]any{"inline": map[string]any{"modelConfig": "unexpected"}},
		})}, chatDetails{"reviewer", "claude", "unknown"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := fake.NewClientBuilder().WithObjects(tc.objects...).Build()
			if got := readChatDetails(context.Background(), c, "team", "reviewer"); got != tc.want {
				t.Fatalf("details = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestChatDetailsTolerateLookupFailure(t *testing.T) {
	a := build("team", "reviewer", options{harness: "claude", model: "default-model"})
	c := fake.NewClientBuilder().WithObjects(a).Build()
	reader := unavailableModelReader{Reader: c}
	got := readChatDetails(context.Background(), reader, "team", "reviewer")
	if got != (chatDetails{"reviewer", "claude", "unknown (default-model)"}) {
		t.Fatalf("unreadable model lost the known agent details: %+v", got)
	}
}

type unavailableModelReader struct{ client.Reader }

func (r unavailableModelReader) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if obj.(*unstructured.Unstructured).GroupVersionKind() == modelConfigGVK {
		return context.DeadlineExceeded
	}
	return r.Reader.Get(ctx, key, obj, opts...)
}
