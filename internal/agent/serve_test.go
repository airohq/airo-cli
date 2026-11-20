package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/airohq/airo-cli/internal/tunnel"

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
	target := tunnel.RunnerTunnel{Namespace: "team", UID: "test"}
	err := serveRunner(t.Context(), target, "", []string{"--harness", "codex", "--codex-sandbox", "read-only"}, io.Discard, func(_ context.Context, handler http.Handler) error {
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
