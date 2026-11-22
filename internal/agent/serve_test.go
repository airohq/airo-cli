package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/airohq/kagent-remote-harness/runner/supervisor"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/airohq/airo-cli/internal/expiry"
	"github.com/airohq/airo-cli/internal/tunnel"
)

func TestLocalAgentCommandArguments(t *testing.T) {
	for _, tc := range []struct {
		verb  string
		args  []string
		valid bool
	}{
		{"serve", nil, true},
		{"serve", []string{"--name", "demo", "--", "--idle-timeout=10s"}, true},
		{"serve", []string{"--name="}, false},
		{"serve", []string{"reviewer"}, false},
		{"serve", []string{"-a", "reviewer"}, false},
		{"serve", []string{"--agent", "reviewer"}, false},
		{"connect", []string{"reviewer"}, true},
		{"connect", []string{"-v", "reviewer", "--", "--idle-timeout=10s"}, true},
		{"connect", nil, false},
		{"connect", []string{""}, false},
		{"connect", []string{"one", "two"}, false},
		{"connect", []string{"--", "reviewer"}, false},
		{"connect", []string{"--name", "reviewer"}, false},
	} {
		t.Run(tc.verb+" "+strings.Join(tc.args, " "), func(t *testing.T) {
			cmd, _, err := NewCommand(nil).Find([]string{tc.verb})
			if err != nil || cmd.Name() != tc.verb {
				t.Fatalf("find %s: %v", tc.verb, err)
			}
			err = cmd.ParseFlags(tc.args)
			if err == nil {
				err = cmd.ValidateArgs(cmd.Flags().Args())
			}
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v: %v", tc.valid, err)
			}
		})
	}
}

func TestLocalCommandsRequireCredentialsBeforeClusterAccess(t *testing.T) {
	for _, key := range []string{"OPENAI_API_KEY", "CODEX_API_KEY", "ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN"} {
		t.Setenv(key, "")
	}
	engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/_ping" {
			w.Header().Set("API-Version", "1.54")
			return
		}
		if r.Method != "GET" || !strings.Contains(r.URL.Path, "/volumes/airo-") {
			t.Errorf("unexpected Engine call: %s %s", r.Method, r.URL.Path)
		}
		http.NotFound(w, r)
	}))
	defer engine.Close()
	t.Setenv("DOCKER_HOST", "tcp://"+engine.Listener.Addr().String())
	t.Setenv("DOCKER_API_VERSION", "1.54")
	t.Setenv("DOCKER_TLS_VERIFY", "")
	t.Setenv("DOCKER_CERT_PATH", "")
	kube := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("reached cluster before login: %s", r.URL)
		http.NotFound(w, r)
	}))
	defer kube.Close()
	for _, tc := range []struct {
		args []string
		key  string
	}{
		{[]string{"serve"}, "CODEX_API_KEY"},
		{[]string{"connect", "reviewer", "--harness", "claude"}, "CLAUDE_CODE_OAUTH_TOKEN"},
	} {
		cmd := root()
		cmd.SetIn(strings.NewReader(""))
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		cmd.SetArgs(append([]string{"--server", kube.URL, "-n", "test", "agent"}, tc.args...))
		err := cmd.Execute()
		if err == nil || !strings.Contains(err.Error(), tc.key) || !strings.Contains(err.Error(), "terminal") {
			t.Fatalf("%v: %v", tc.args, err)
		}
	}
}

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
	err := serveRunner(t.Context(), "team", "test", nil, opts, "", nil, io.Discard, func(_ context.Context, handler http.Handler) error {
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
	existing := build("team", "reviewer", options{harness: "relay"})
	c := fake.NewClientBuilder().WithObjects(existing).Build()
	stopped := errors.New("serve failed")
	var key client.ObjectKey
	err := withTemporaryAgent(t.Context(), c, "team", "", "relay", io.Discard, func(_ context.Context, a *unstructured.Unstructured) error {
		name := a.GetName()
		key = client.ObjectKey{Namespace: "team", Name: name}
		got := &unstructured.Unstructured{}
		got.SetGroupVersionKind(agentGVK)
		if err := c.Get(t.Context(), key, got); err != nil {
			t.Fatal(err)
		}
		refs := got.GetOwnerReferences()
		policies, _, _ := unstructured.NestedSlice(got.Object, "spec", "egressPolicyRefs")
		if !strings.HasPrefix(name, "local-agent-") || len(refs) != 0 || got.GetAnnotations()[expiry.Annotation] == "" ||
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

func TestNamedTemporaryAgentCanReuseReleasedName(t *testing.T) {
	ctx := t.Context()
	c := fake.NewClientBuilder().Build()
	for range 2 {
		err := withTemporaryAgent(ctx, c, "team", "demo", "relay", io.Discard, func(_ context.Context, a *unstructured.Unstructured) error {
			name := a.GetName()
			if name != "demo" {
				t.Fatalf("got name %q", name)
			}
			err := withTemporaryAgent(ctx, c, "team", name, "relay", io.Discard, func(context.Context, *unstructured.Unstructured) error {
				t.Fatal("served an existing Agent")
				return nil
			})
			if !apierrors.IsAlreadyExists(err) {
				t.Fatalf("expected name collision: %v", err)
			}
			a = resource(agentGVK, name, nil)
			if err := c.Get(ctx, client.ObjectKeyFromObject(a), a); err != nil || len(a.GetOwnerReferences()) != 0 {
				t.Fatalf("original Agent after collision: %v, %v", a, err)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestTemporaryServeInterruptDeletesOnlyAgent(t *testing.T) {
	ctx, interrupt := context.WithCancel(t.Context())
	defer interrupt()
	h := resource(harnessGVK, "relay", map[string]any{"env": []any{
		map[string]any{"name": "REMOTE_HARNESS_URL", "value": "http://relay.team.svc.cluster.local"},
	}})
	policy := resource(egressPolicyGVK, "relay", nil)
	policy.SetLabels(map[string]string{tunnel.TemporaryAgentLabel: "true"})
	var child client.Object
	c := fake.NewClientBuilder().WithObjects(h, policy).WithInterceptorFuncs(interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			err := c.Create(ctx, obj, opts...)
			if err == nil && obj.GetObjectKind().GroupVersionKind().Kind == "Tunnel" {
				child = obj.DeepCopyObject().(client.Object)
				interrupt()
			}
			return err
		},
	}).Build()
	if err := serveTemporary(ctx, c, "team", "reviewer", io.Discard, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("interrupt returned %v", err)
	}
	a := resource(agentGVK, "reviewer", nil)
	if err := c.Get(t.Context(), client.ObjectKeyFromObject(a), a); !apierrors.IsNotFound(err) {
		t.Fatalf("Agent after interrupt: %v", err)
	}
	// The fake client has no garbage collector. The dependent stays here
	// unless the CLI explicitly deletes it.
	if err := c.Get(t.Context(), client.ObjectKeyFromObject(child), child); err != nil {
		t.Fatalf("expected the Tunnel to be left for garbage collection: %v", err)
	}
}

func TestCreateLocalAgentWithoutPreparedRelay(t *testing.T) {
	c := fake.NewClientBuilder().Build()
	_, err := run(t, c, options{local: true, prompt: "You review."})
	if err == nil || !strings.Contains(err.Error(), "default Harness") {
		t.Fatalf("expected the missing worker pool settings to be reported: %v", err)
	}
	cmd := newCreateCommand(nil)
	if cmd.Flags().Lookup("local") == nil || cmd.Flags().Lookup("relay") != nil {
		t.Fatal("expected only the local serving flag")
	}
}

func TestLocalAgentReconnectsOnlyToItsOwnedRelay(t *testing.T) {
	const origin = "http://relay.team.svc.cluster.local"
	name := "reviewer"
	a := build("team", "reviewer", options{local: true, harness: name, prompt: "You review."})
	a.SetUID("agent-uid")
	h := resource(harnessGVK, name, map[string]any{"env": []any{map[string]any{"name": "REMOTE_HARNESS_URL", "value": origin}}})
	policy := resource(egressPolicyGVK, name, nil)
	tun := resource(schema.GroupVersionKind{Group: "tunnels.airohq.dev", Version: "v1alpha1", Kind: "Tunnel"}, name, map[string]any{})
	tun.SetUID("tunnel-uid")
	tun.Object["status"] = map[string]any{"callerURL": origin, "clientURL": "https://relay.example"}
	for _, obj := range []*unstructured.Unstructured{h, policy, tun} {
		obj.SetOwnerReferences([]metav1.OwnerReference{*metav1.NewControllerRef(a, agentGVK)})
	}
	c := fake.NewClientBuilder().WithObjects(a, h, policy, tun).Build()
	for range 2 {
		target, err := agentTunnel(t.Context(), c, "team", a.GetName())
		if err != nil || target.UID != "tunnel-uid" {
			t.Fatalf("reconnect: %+v, %v", target, err)
		}
	}
	tun.SetOwnerReferences(nil)
	c = fake.NewClientBuilder().WithObjects(a, h, policy, tun).Build()
	if _, err := agentTunnel(t.Context(), c, "team", a.GetName()); err == nil {
		t.Fatal("connected to a Tunnel belonging to another owner")
	}
}

func TestLocalCreateOwnsEachChildAndRollsBackOnFailure(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		t.Run(fmt.Sprintf("conflict=%t", conflict), func(t *testing.T) {
			base := harness("base", "claude")
			base.Object["spec"].(map[string]any)["substrate"] = map[string]any{
				"workerPoolRef": map[string]any{"name": "pool"}, "snapshotPolicy": map[string]any{"location": "gs://snapshots/team"},
			}
			const name = "my-agent"
			var kinds []string
			c := fake.NewClientBuilder().WithObjects(base).WithInterceptorFuncs(interceptor.Funcs{
				Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
					gvk, err := c.GroupVersionKindFor(obj)
					if err != nil {
						return err
					}
					kinds = append(kinds, gvk.Kind)
					switch gvk.Kind {
					case "Agent":
						obj.SetUID("original-agent")
						a := obj.(*unstructured.Unstructured)
						if a.GetLabels()[localAgentLabel] != "true" || a.GetAnnotations()[expiry.Annotation] != "" ||
							detailString(a.Object, "spec", "harnessRef", "name") != name {
							t.Fatalf("unexpected Agent: %v", a)
						}
						a.Object["status"] = map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": "True"}}}
					case "Tunnel":
						obj.SetUID("original-tunnel")
						obj.(*unstructured.Unstructured).Object["status"] = map[string]any{
							"callerURL": "http://my-agent.team.svc.cluster.local", "clientURL": "https://my-agent.example",
							"conditions": []any{map[string]any{"type": "Ready", "status": "True"}},
						}
					case "EgressPolicy":
						if conflict {
							return apierrors.NewAlreadyExists(gvk.GroupVersion().WithResource("egresspolicies").GroupResource(), name)
						}
					}
					if gvk.Kind != "Agent" {
						refs := obj.GetOwnerReferences()
						if len(refs) != 1 || refs[0].Name != name || refs[0].UID != "original-agent" || obj.GetNamespace() != "team" {
							t.Fatalf("child %s has unexpected metadata: %+v", gvk.Kind, obj)
						}
					}
					return c.Create(ctx, obj, opts...)
				},
			}).Build()
			_, err := run(t, c, options{local: true, prompt: "Review."})
			if conflict {
				if !apierrors.IsAlreadyExists(err) {
					t.Fatalf("unexpected error %v", err)
				}
				a := resource(agentGVK, name, nil)
				if err := c.Get(t.Context(), client.ObjectKeyFromObject(a), a); !apierrors.IsNotFound(err) {
					t.Fatalf("Agent after rollback: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(kinds, []string{"Agent", "Tunnel", "EgressPolicy", "Role", "RoleBinding", "Harness"}) {
				t.Fatalf("created %v", kinds)
			}
		})
	}
}
