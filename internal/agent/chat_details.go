package agent

import (
	"context"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// These are the Agent's configured settings, not a claim about a resumed
// session's pinned revision. Missing or unreadable metadata must not stop chat.
type chatDetails struct {
	agent, harness, model string
}

func readChatDetails(ctx context.Context, c client.Reader, ns, name string) chatDetails {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	details := chatDetails{agent: name, harness: "unknown", model: "unknown"}
	read := func(gvk schema.GroupVersionKind, namespace, name string) (*unstructured.Unstructured, error) {
		obj := &unstructured.Unstructured{}
		obj.SetGroupVersionKind(gvk)
		err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, obj)
		return obj, err
	}
	a, err := read(agentGVK, ns, name)
	if err != nil {
		return details
	}
	if harness := detailString(a.Object, "spec", "harnessRef", "name"); harness != "" {
		details.harness = harness
	}
	inline, found, err := unstructured.NestedMap(a.Object, "spec", "template", "inline")
	if err != nil {
		return details
	}
	if !found {
		ref := detailString(a.Object, "spec", "template", "ref", "name")
		if ref == "" {
			return details
		}
		template, err := read(templateGVK, ns, ref)
		if err != nil {
			return details
		}
		inline, found, err = unstructured.NestedMap(template.Object, "spec")
		if err != nil || !found {
			return details
		}
	}
	ref, found, err := unstructured.NestedMap(inline, "modelConfig")
	if err != nil {
		return details
	}
	if !found {
		details.model = "not configured"
		return details
	}
	modelName := detailString(ref, "name")
	if modelName == "" {
		return details
	}
	modelNS, _, err := unstructured.NestedString(ref, "namespace")
	if err != nil {
		return details
	}
	modelNS = strings.TrimSpace(modelNS)
	if modelNS == "" {
		modelNS = ns
	}
	refName := modelName
	if modelNS != ns {
		refName = modelNS + "/" + modelName
	}
	details.model = "unknown (" + refName + ")"
	model, err := read(modelConfigGVK, modelNS, modelName)
	if err == nil {
		if name := detailString(model.Object, "spec", "model"); name != "" {
			details.model = name
		}
	}
	return details
}

func detailString(obj map[string]any, fields ...string) string {
	value, _, _ := unstructured.NestedString(obj, fields...)
	return strings.TrimSpace(value)
}
