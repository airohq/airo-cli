package agent

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func resource(gvk schema.GroupVersionKind, name string, spec map[string]any) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(gvk)
	u.SetNamespace("team")
	u.SetName(name)
	u.Object["spec"] = spec
	return u
}

func harness(name, runtime string) *unstructured.Unstructured {
	return resource(harnessGVK, name, map[string]any{runtime: map[string]any{}})
}

func model(name string) *unstructured.Unstructured {
	return resource(modelConfigGVK, name, map[string]any{"model": name})
}

// created reads the Agent my-agent back from the cluster.
func created(t *testing.T, c client.Client) *unstructured.Unstructured {
	t.Helper()
	a := resource(agentGVK, "my-agent", nil)
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(a), a); err != nil {
		t.Fatal(err)
	}
	return a
}

// run creates my-agent without waiting long for a controller the fake
// cluster does not have, and returns the error of the create itself.
func run(t *testing.T, c client.Client, o options) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := create(context.Background(), &out, c, "team", "my-agent", o, 10*time.Millisecond)
	if err != nil && strings.Contains(err.Error(), "has no status") {
		err = nil
	}
	return out.String(), err
}

func TestCreateTakesTheProjectsOnlyHarnessAndModel(t *testing.T) {
	c := fake.NewClientBuilder().WithObjects(harness("main", "claude"), model("sonnet")).Build()
	out, err := run(t, c, options{prompt: "You review.", description: "Reviews"})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"harnessRef": map[string]any{"name": "main"},
		"template": map[string]any{"inline": map[string]any{
			"systemPrompt": "You review.",
			"description":  "Reviews",
			"modelConfig":  map[string]any{"name": "sonnet"},
		}},
	}
	if got := created(t, c).Object["spec"]; !equal(got, want) {
		t.Errorf("spec = %v, want %v", got, want)
	}
	for _, line := range []string{"Created agent team/my-agent", "main (claude)", "sonnet"} {
		if !strings.Contains(out, line) {
			t.Errorf("output %q lacks %q", out, line)
		}
	}
}

func TestCreateUsesTheNamedHarnessAndModel(t *testing.T) {
	c := fake.NewClientBuilder().WithObjects(
		harness("one", "claude"), harness("two", "kagent"), model("sonnet"), model("haiku"),
	).Build()
	if _, err := run(t, c, options{harness: "two", model: "haiku"}); err != nil {
		t.Fatal(err)
	}
	a := created(t, c)
	if got, _, _ := unstructured.NestedString(a.Object, "spec", "harnessRef", "name"); got != "two" {
		t.Errorf("harness = %q, want two", got)
	}
	if got, _, _ := unstructured.NestedString(a.Object, "spec", "template", "inline", "modelConfig", "name"); got != "haiku" {
		t.Errorf("model = %q, want haiku", got)
	}
}

func TestCreateNeverGuessesBetweenSeveral(t *testing.T) {
	c := fake.NewClientBuilder().WithObjects(
		harness("one", "claude"), harness("two", "byo"), model("sonnet"), model("haiku"),
	).Build()
	for flag, o := range map[string]options{
		"--harness": {model: "sonnet"},
		"--model":   {harness: "one"},
	} {
		_, err := run(t, c, o)
		if err == nil || !strings.Contains(err.Error(), flag) {
			t.Errorf("create(%+v) = %v, want an error that names %s", o, err, flag)
		}
	}
	// The choices are in the message, with what each Harness runs.
	_, err := run(t, c, options{model: "sonnet"})
	if !strings.Contains(err.Error(), "one (claude), two (byo)") {
		t.Errorf("error %q lacks the choices", err)
	}
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(agentGVK.GroupVersion().WithKind("AgentList"))
	if err := c.List(context.Background(), list); err != nil || len(list.Items) != 0 {
		t.Errorf("agents = %d, err = %v; want none created", len(list.Items), err)
	}
}

// marked returns u as the project's default of its kind.
func marked(u *unstructured.Unstructured, annotation string) *unstructured.Unstructured {
	u.SetAnnotations(map[string]string{annotation: "true"})
	return u
}

func TestCreateTakesTheDefaultAmongSeveral(t *testing.T) {
	c := fake.NewClientBuilder().WithObjects(
		harness("byo", "byo"), marked(harness("claude", "claude"), harnesses.marker),
		model("haiku"), marked(model("sonnet"), models.marker),
	).Build()
	if _, err := run(t, c, options{prompt: "You review."}); err != nil {
		t.Fatal(err)
	}
	a := created(t, c)
	if got, _, _ := unstructured.NestedString(a.Object, "spec", "harnessRef", "name"); got != "claude" {
		t.Errorf("harness = %q, want claude", got)
	}
	if got, _, _ := unstructured.NestedString(a.Object, "spec", "template", "inline", "modelConfig", "name"); got != "sonnet" {
		t.Errorf("model = %q, want sonnet", got)
	}
}

func TestCreateTakesNoneOfTwoDefaults(t *testing.T) {
	c := fake.NewClientBuilder().WithObjects(
		marked(harness("one", "claude"), harnesses.marker), marked(harness("two", "claude"), harnesses.marker), model("sonnet"),
	).Build()
	if _, err := run(t, c, options{}); err == nil || !strings.Contains(err.Error(), "no single default") {
		t.Errorf("create = %v, want an error for two defaults", err)
	}
}

func TestCreateRefusesANameTheProjectLacks(t *testing.T) {
	c := fake.NewClientBuilder().WithObjects(harness("main", "claude"), model("sonnet")).Build()
	for _, o := range []options{{harness: "other"}, {model: "other"}} {
		_, err := run(t, c, o)
		if err == nil || !strings.Contains(err.Error(), "named other") {
			t.Errorf("create(%+v) = %v, want an error for the missing name", o, err)
		}
	}
}

func TestCreateNeedsAHarness(t *testing.T) {
	c := fake.NewClientBuilder().WithObjects(model("sonnet")).Build()
	if _, err := run(t, c, options{}); err == nil || !strings.Contains(err.Error(), "no harness") {
		t.Errorf("create = %v, want an error for a project without a harness", err)
	}
}

func TestCreateFromATemplateSetsNoModel(t *testing.T) {
	c := fake.NewClientBuilder().WithObjects(
		harness("main", "claude"), model("sonnet"), model("haiku"), resource(templateGVK, "reviewer", nil),
	).Build()
	if _, err := run(t, c, options{template: "missing"}); err == nil || !strings.Contains(err.Error(), "it has reviewer") {
		t.Errorf("create = %v, want an error for a template the project lacks", err)
	}
	if _, err := run(t, c, options{template: "reviewer"}); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"ref": map[string]any{"name": "reviewer"}}
	if got, _, _ := unstructured.NestedMap(created(t, c).Object, "spec", "template"); !equal(got, want) {
		t.Errorf("template = %v, want %v", got, want)
	}
}

func TestCreateLeavesTheModelOutForAHarnessWithItsOwnImage(t *testing.T) {
	c := fake.NewClientBuilder().WithObjects(harness("own", "byo"), model("sonnet"), model("haiku")).Build()
	if _, err := run(t, c, options{prompt: "You review."}); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := unstructured.NestedMap(created(t, c).Object, "spec", "template", "inline", "modelConfig"); found {
		t.Error("the agent has a model, want none")
	}
	// A model the person names is set all the same.
	c = fake.NewClientBuilder().WithObjects(harness("own", "byo"), model("sonnet"), model("haiku")).Build()
	if _, err := run(t, c, options{model: "haiku"}); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := unstructured.NestedString(created(t, c).Object, "spec", "template", "inline", "modelConfig", "name"); got != "haiku" {
		t.Errorf("model = %q, want haiku", got)
	}
}

func TestCreateRefusesAnExistingAgent(t *testing.T) {
	old := resource(agentGVK, "my-agent", map[string]any{"harnessRef": map[string]any{"name": "theirs"}})
	c := fake.NewClientBuilder().WithObjects(old, harness("main", "claude"), model("sonnet")).Build()
	if _, err := run(t, c, options{}); err == nil || !strings.Contains(err.Error(), "exists") {
		t.Errorf("create = %v, want an error for an agent that exists", err)
	}
	if got, _, _ := unstructured.NestedString(created(t, c).Object, "spec", "harnessRef", "name"); got != "theirs" {
		t.Errorf("harness = %q: the existing agent changed", got)
	}
}

// withConditions returns the Agent my-agent with the status a controller
// would give it.
func withConditions(conditions ...any) *unstructured.Unstructured {
	a := resource(agentGVK, "my-agent", nil)
	a.Object["status"] = map[string]any{"conditions": conditions}
	return a
}

func condition(kind, status, message string) map[string]any {
	return map[string]any{"type": kind, "status": status, "message": message}
}

func TestWaitReady(t *testing.T) {
	for name, tc := range map[string]struct {
		agent *unstructured.Unstructured
		want  string
	}{
		"ready":            {withConditions(condition("Accepted", "True", ""), condition("Ready", "True", "")), ""},
		"refused":          {withConditions(condition("Compatible", "False", "Claude does not support provider OpenAI"), condition("Ready", "False", "blocked")), "cannot run: Claude does not support provider OpenAI"},
		"not ready":        {withConditions(condition("Accepted", "True", ""), condition("Ready", "False", "building the snapshot")), "is not ready after 10ms: building the snapshot"},
		"stale refusal":    {withConditions(map[string]any{"type": "Compatible", "status": "False", "message": "old", "observedGeneration": int64(7)}, condition("Ready", "False", "blocked")), "is not ready after"},
		"no status":        {resource(agentGVK, "my-agent", nil), "has no status"},
		"no Ready, no gap": {withConditions(condition("Accepted", "True", "")), "has no Ready condition"},
	} {
		c := fake.NewClientBuilder().WithObjects(tc.agent).Build()
		err := waitReady(context.Background(), c, resource(agentGVK, "my-agent", nil), 10*time.Millisecond)
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%s: waitReady = %v, want nil", name, err)
		case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
			t.Errorf("%s: waitReady = %v, want an error with %q", name, err, tc.want)
		}
	}
}

func TestReadPrompt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prompt.md")
	if err := os.WriteFile(path, []byte("You review.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := readPrompt(nil, path); err != nil || got != "You review.\n" {
		t.Errorf("readPrompt(file) = %q, %v", got, err)
	}
	if got, err := readPrompt(strings.NewReader("From a pipe."), "-"); err != nil || got != "From a pipe." {
		t.Errorf("readPrompt(-) = %q, %v", got, err)
	}
	if _, err := readPrompt(strings.NewReader(" \n"), "-"); err == nil {
		t.Error("readPrompt of an empty prompt = nil, want an error")
	}
	if _, err := readPrompt(nil, filepath.Join(t.TempDir(), "missing.md")); err == nil {
		t.Error("readPrompt of a missing file = nil, want an error")
	}
}

func equal(got, want any) bool {
	g, _ := got.(map[string]any)
	w, _ := want.(map[string]any)
	a := &unstructured.Unstructured{Object: map[string]any{"v": g}}
	b := &unstructured.Unstructured{Object: map[string]any{"v": w}}
	x, _ := a.MarshalJSON()
	y, _ := b.MarshalJSON()
	return bytes.Equal(x, y)
}
