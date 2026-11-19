package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

const testResourceAPI = "/apis/example.test/v1"

func resourceDiscovery() *http.ServeMux {
	discovery := http.NewServeMux()
	discovery.HandleFunc("/api", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"kind":"APIVersions","apiVersion":"v1","versions":["v1"]}`)
	})
	discovery.HandleFunc("/apis", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"kind":"APIGroupList","apiVersion":"v1","groups":[{"name":"example.test","versions":[{"groupVersion":"example.test/v1","version":"v1"}],"preferredVersion":{"groupVersion":"example.test/v1","version":"v1"}}]}`)
	})
	discovery.HandleFunc("/api/v1", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"kind":"APIResourceList","groupVersion":"v1","resources":[{"name":"namespaces","singularName":"namespace","kind":"Namespace","namespaced":false,"verbs":["get","list","delete"]},{"name":"secrets","singularName":"secret","kind":"Secret","namespaced":true,"verbs":["get","list","delete"]}]}`)
	})
	discovery.HandleFunc(testResourceAPI, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"kind":"APIResourceList","groupVersion":"example.test/v1","resources":[{"name":"widgets","singularName":"widget","kind":"Widget","namespaced":true,"shortNames":["wd"],"verbs":["get","list","delete"]},{"name":"agents","singularName":"agent","kind":"Agent","namespaced":true,"verbs":["get","list","delete"]}]}`)
	})

	return discovery
}

// The API adds a kind and its short name, serves a built-in Airo noun from
// another group, and supplies a column the CLI cannot know.
func TestResourceReads(t *testing.T) {
	const api = testResourceAPI
	const widgets = api + "/namespaces/acme-web/widgets"
	discovery := resourceDiscovery()
	for _, tt := range []struct {
		name string
		args []string
		path string
		want string
	}{
		{"filtered list", []string{"widgets", "list", "-l", "env=test", "--field-selector", "metadata.name=sample"}, widgets, "NAME SCORE sample 42"},
		{"singular get collection", []string{"widget", "get"}, widgets, "NAME SCORE sample 42"},
		{"short name across namespaces", []string{"wd", "list", "-A"}, api + "/widgets", "NAMESPACE NAME SCORE acme-web sample 42"},
		{"qualified named get", []string{"widgets.v1.example.test", "get", "sample", "-n", "other", "-o", "wide"}, api + "/namespaces/other/widgets/sample", "NAME SCORE DETAIL sample 42 extra"},
		{"built-in noun via discovery", []string{"agents", "get", "sample"}, api + "/namespaces/acme-web/agents/sample", "NAME SCORE sample 42"},
		{"project list as namespace", []string{"project", "list", "-A"}, "/api/v1/namespaces", "NAME SCORE sample 42"},
		{"project get as namespace", []string{"projects", "get", "sample"}, "/api/v1/namespaces/sample", "NAME SCORE sample 42"},
		{"plain object fallback", []string{"widget", "get", "sample"}, widgets + "/sample", "NAME AGE sample <unknown>"},
		{"json object", []string{"widgets.example.test", "get", "sample", "-o", "json"}, widgets + "/sample", ""},
		{"access denied", []string{"wd", "list"}, widgets, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Method != "GET" || r.Header.Get("Authorization") != "Bearer customer-token" {
					t.Errorf("unexpected method or identity for %s", r.URL.Path)
				}
				if _, pattern := discovery.Handler(r); pattern != "" {
					discovery.ServeHTTP(w, r)
					return
				}
				if r.URL.Path != tt.path {
					t.Errorf("got path %s, want %s", r.URL.Path, tt.path)
					http.NotFound(w, r)
					return
				}
				if tt.name == "filtered list" && (r.URL.Query().Get("labelSelector") != "env=test" || r.URL.Query().Get("fieldSelector") != "metadata.name=sample") {
					t.Errorf("unexpected selectors in %s", r.URL.RawQuery)
				}
				if tt.name == "access denied" {
					w.WriteHeader(http.StatusForbidden)
					io.WriteString(w, `{"apiVersion":"v1","kind":"Status","status":"Failure","reason":"Forbidden","code":403,"message":"cannot list widgets"}`)
					return
				}
				tableRequested := strings.Contains(r.Header.Get("Accept"), "as=Table")
				if tableRequested == (tt.name == "json object") {
					t.Errorf("unexpected Accept: %s", r.Header.Get("Accept"))
				}
				if tt.name == "plain object fallback" || tt.name == "json object" {
					io.WriteString(w, `{"apiVersion":"example.test/v1","kind":"Widget","metadata":{"name":"sample"},"spec":{"score":42}}`)
					return
				}
				table := metav1.Table{
					TypeMeta:          metav1.TypeMeta{APIVersion: "meta.k8s.io/v1", Kind: "Table"},
					ColumnDefinitions: []metav1.TableColumnDefinition{{Name: "Name", Type: "string", Format: "name"}, {Name: "Score", Type: "integer"}, {Name: "Detail", Type: "string", Priority: 1}},
					Rows:              []metav1.TableRow{{Cells: []any{"sample", 42, "extra"}, Object: runtime.RawExtension{Raw: []byte(`{"apiVersion":"meta.k8s.io/v1","kind":"PartialObjectMetadata","metadata":{"name":"sample","namespace":"acme-web"}}`)}}},
				}
				_ = json.NewEncoder(w).Encode(&table)
			}))
			defer server.Close()
			root := newRoot()
			var out, stderr bytes.Buffer
			root.SetOut(&out)
			root.SetErr(&stderr)
			root.SetArgs(append([]string{"--cache-dir", t.TempDir(), "--kubeconfig", identityConfig(t, server.URL)}, tt.args...))
			err := root.Execute()
			if tt.name == "access denied" {
				if err == nil || !strings.Contains(err.Error(), "cannot list widgets") || out.Len() != 0 {
					t.Fatalf("output %q, error %v", out.String(), err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tt.name == "json object" {
				var obj struct {
					Kind string
					Spec struct{ Score int }
				}
				if err := json.Unmarshal(out.Bytes(), &obj); err != nil || obj.Kind != "Widget" || obj.Spec.Score != 42 {
					t.Fatalf("unexpected JSON output: %s", &out)
				}
				return
			}
			if got := strings.Join(strings.Fields(out.String()), " "); got != tt.want {
				t.Errorf("got %q, want %q (stderr %q)", got, tt.want, stderr.String())
			}
		})
	}
}

func TestResourceDescribe(t *testing.T) {
	const api = testResourceAPI + "/namespaces/acme-web/"
	discovery := resourceDiscovery()
	for _, tt := range []struct {
		resource, path, object, want string
		events                       bool
	}{
		{"wd", api + "widgets/sample", `{"apiVersion":"example.test/v1","kind":"Widget","metadata":{"name":"sample","namespace":"acme-web","uid":"widget-1"},"spec":{"score":42}}`, "Score: 42", true},
		{"agents", api + "agents/sample", `{"apiVersion":"example.test/v1","kind":"Agent","metadata":{"name":"sample","namespace":"acme-web"},"status":{"phase":"Idle"}}`, "Phase: Idle", false},
		{"secret", "/api/v1/namespaces/acme-web/secrets/sample", `{"apiVersion":"v1","kind":"Secret","metadata":{"name":"sample","namespace":"acme-web"},"data":{"example":"dGVzdA=="}}`, "example: 4 bytes", false},
	} {
		t.Run(tt.resource, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer customer-token" {
					t.Errorf("unexpected method or credentials for %s", r.URL.Path)
				}
				if _, pattern := discovery.Handler(r); pattern != "" {
					discovery.ServeHTTP(w, r)
					return
				}
				switch r.URL.Path {
				case tt.path:
					io.WriteString(w, tt.object)
				case "/api/v1/namespaces/acme-web/events":
					if !tt.events || !strings.Contains(r.URL.Query().Get("fieldSelector"), "involvedObject.name=sample") {
						t.Errorf("unexpected event lookup: %s", r.URL)
					}
					io.WriteString(w, `{"apiVersion":"v1","kind":"EventList","items":[]}`)
				default:
					t.Errorf("unexpected request for %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			args := []string{"--cache-dir", t.TempDir(), tt.resource, "describe", "sample"}
			if !tt.events {
				args = append(args, "--show-events=false")
			}
			out, err := runIdentityCommand(t, identityConfig(t, server.URL), args...)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(strings.Join(strings.Fields(out), " "), tt.want) || strings.Contains(out, "Events:") != tt.events {
				t.Errorf("unexpected description:\n%s", out)
			}
		})
	}
}
