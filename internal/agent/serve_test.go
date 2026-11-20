package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"strings"
	"testing"

	"github.com/airohq/kagent-remote-harness/runner/supervisor"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestAgentTunnel(t *testing.T) {
	const origin = "http://work.team.svc.cluster.local"
	a := resource(agentGVK, "reviewer", map[string]any{"harnessRef": map[string]any{"name": "work"}})
	h := resource(harnessGVK, "work", map[string]any{"env": []any{map[string]any{"name": "REMOTE_HARNESS_URL", "value": origin}}})
	for _, tc := range []struct {
		name  string
		spec  map[string]any
		valid bool
	}{
		{"private", map[string]any{}, true}, {"open", map[string]any{"callerAuth": "none"}, false},
		{"public", map[string]any{"public": true}, false}, {"unknown", map[string]any{"callerAuth": "future"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			obj := resource(schema.GroupVersionKind{Group: "tunnels.airohq.dev", Version: "v1alpha1", Kind: "Tunnel"}, "work", tc.spec)
			obj.SetUID("uid")
			obj.Object["status"] = map[string]any{"callerURL": origin, "clientURL": "https://work.example"}
			c := fake.NewClientBuilder().WithObjects(a, h, obj).Build()
			got, err := agentTunnel(context.Background(), c, "team", "reviewer")
			if tc.valid {
				if err != nil || got.UID != "uid" {
					t.Fatalf("target %+v: %v", got, err)
				}
			} else if err == nil {
				t.Fatal("accepted unsafe Tunnel")
			}
		})
	}
}

func TestRunnerProxyIdentity(t *testing.T) {
	calls := 0
	handler := runnerRequests("team", "local-secret", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Bearer local-secret" || r.Header.Get("Tunnel-Caller") != "" || r.Header.Get("Cookie") != "" {
			t.Fatal("unexpected headers", r.Header)
		}
		io.WriteString(w, "stream")
	}))
	for _, tc := range []struct {
		caller, path string
		want         int
	}{
		{"", "/v1/turn", 403}, {"substrate:actor/other/session", "/v1/turn", 403},
		{"substrate:actor/team/", "/v1/turn", 403}, {"substrate:actor/team/session", "/admin", 404},
		{"substrate:actor/team/session", "/v1/turn", 200},
	} {
		r := httptest.NewRequest("POST", tc.path, strings.NewReader("{}"))
		r.Header.Set("Tunnel-Caller", tc.caller)
		r.Header.Set("Authorization", "Bearer incoming")
		r.Header.Set("Cookie", "private")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("%s %s = %d", tc.caller, tc.path, w.Code)
		}
	}
	if calls != 1 {
		t.Fatalf("forwarded %d requests", calls)
	}
}

func TestEmbeddedRunnerStartsColdWithoutHelper(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv("DOCKER_HOST", "unix:///dev/null/absent-engine.sock")
	opts := supervisor.Options{Backend: "container", WorkerOptions: supervisor.WorkerOptions{Harness: "codex", CodexSandbox: "read-only", IdleTimeout: time.Minute, TurnTimeout: time.Minute}}
	err := serveRunner(t.Context(), "team", "test", opts, "", nil, io.Discard, func(_ context.Context, handler http.Handler) error {
		req := httptest.NewRequest(http.MethodGet, "/v1/info", nil)
		req.Header.Set("Tunnel-Caller", "substrate:actor/team/session")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != http.StatusOK || w.Header().Get("Remote-Harness-Protocol") != "1" {
			t.Fatalf("info: %d %s", w.Code, w.Body)
		}
		var result struct{ Harnesses []string }
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			return err
		}
		if len(result.Harnesses) != 1 || result.Harnesses[0] != "codex" {
			t.Fatalf("harnesses: %v", result.Harnesses)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestTemporaryAgentOwnsOnlyItsNewResources(t *testing.T) {
	owner := metav1.OwnerReference{APIVersion: "tunnels.airohq.dev/v1alpha1", Kind: "Tunnel", Name: "relay", UID: "tunnel-uid"}
	existing := build("team", "reviewer", options{harness: "relay"})
	c := fake.NewClientBuilder().WithObjects(existing).Build()
	stopped := errors.New("serve failed")
	var key client.ObjectKey
	err := withTemporaryAgent(t.Context(), c, "team", "relay", owner, func(name string) error {
		key = client.ObjectKey{Namespace: "team", Name: name}
		got := &unstructured.Unstructured{}
		got.SetGroupVersionKind(agentGVK)
		if err := c.Get(t.Context(), key, got); err != nil {
			t.Fatal(err)
		}
		refs := got.GetOwnerReferences()
		policies, _, _ := unstructured.NestedSlice(got.Object, "spec", "egressPolicyRefs")
		if !strings.HasPrefix(name, "local-agent-") || len(refs) != 1 || refs[0] != owner ||
			detailString(got.Object, "spec", "harnessRef", "name") != "relay" ||
			!reflect.DeepEqual(policies, []any{map[string]any{"name": "relay"}}) {
			t.Fatalf("unexpected temporary Agent: %+v", got)
		}
		return stopped
	})
	if !errors.Is(err, stopped) {
		t.Fatal(err)
	}
	check := existing.DeepCopy()
	if err := c.Get(t.Context(), key, check); !apierrors.IsNotFound(err) {
		t.Fatalf("temporary agent after cleanup: %v", err)
	}
	if err := c.Get(t.Context(), client.ObjectKeyFromObject(existing), check); err != nil {
		t.Fatal(err)
	}
}
