package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// Run through the CLI so the tests cover flag parsing, client-go's cached
// config loader, resource requests and the next invocation's saved selection.
func TestTenantProjectsAcrossInvocations(t *testing.T) {
	const owner = "capsule.clastix.io/tenant"
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	discovery := resourceDiscovery()
	projects := map[string]string{"acme-web": "acme", "beta-default": "beta", "beta-api": "beta"}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") != "Bearer customer-token" {
			t.Errorf("changed identity for %s", r.URL.Path)
		}
		if _, pattern := discovery.Handler(r); pattern != "" {
			discovery.ServeHTTP(w, r)
			return
		}
		switch {
		case r.URL.Path == "/apis/authentication.k8s.io/v1/selfsubjectreviews":
			io.WriteString(w, `{"apiVersion":"authentication.k8s.io/v1","kind":"SelfSubjectReview","status":{"userInfo":{"username":"user","groups":["customer:acme","customer:beta"]}}}`)
		case strings.HasPrefix(r.URL.Path, "/apis/capsule.clastix.io/v1beta2/tenants/"):
			fmt.Fprintf(w, `{"apiVersion":"capsule.clastix.io/v1beta2","kind":"Tenant","metadata":{"name":%q}}`, path.Base(r.URL.Path))
		case r.URL.Path == "/api/v1/namespaces" && r.Method == "POST":
			var obj struct{ Metadata metav1.ObjectMeta }
			if err := json.NewDecoder(r.Body).Decode(&obj); err != nil {
				t.Error(err)
			}
			if obj.Metadata.Name != "new-first" || obj.Metadata.Labels[owner] != "new" {
				t.Errorf("created %+v", obj.Metadata)
			}
			projects[obj.Metadata.Name] = obj.Metadata.Labels[owner]
			w.WriteHeader(http.StatusCreated)
			fmt.Fprintf(w, `{"apiVersion":"v1","kind":"Namespace","metadata":{"name":%q}}`, obj.Metadata.Name)
		case r.URL.Path == "/api/v1/namespaces" && r.Method == "GET":
			tenant, ok := strings.CutPrefix(r.URL.Query().Get("labelSelector"), owner+"=")
			if !ok {
				t.Errorf("project list missing tenant selector: %s", r.URL.RawQuery)
			}
			items := []map[string]any{}
			for name, tname := range projects {
				if tenant == tname {
					items = append(items, map[string]any{"metadata": map[string]any{"name": name}})
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"apiVersion": "v1", "kind": "NamespaceList", "items": items})
		case strings.HasPrefix(r.URL.Path, "/api/v1/namespaces/") && r.Method == "GET":
			name := path.Base(r.URL.Path)
			tenant, ok := projects[name]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				io.WriteString(w, `{"apiVersion":"v1","kind":"Status","reason":"NotFound","code":404}`)
				return
			}
			fmt.Fprintf(w, `{"apiVersion":"v1","kind":"Namespace","metadata":{"name":%q,"labels":{%q:%q}}}`, name, owner, tenant)
		case strings.HasPrefix(r.URL.Path, testResourceAPI+"/namespaces/") && strings.HasSuffix(r.URL.Path, "/agents"):
			project := path.Base(path.Dir(r.URL.Path))
			fmt.Fprintf(w, `{"apiVersion":"example.test/v1","kind":"AgentList","items":[{"apiVersion":"example.test/v1","kind":"Agent","metadata":{"namespace":%q,"name":%q}}]}`, project, project+"-agent")
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	config := identityConfig(t, server.URL)
	cfg, err := clientcmd.LoadFromFile(config)
	if err != nil {
		t.Fatal(err)
	}
	cfg.AuthInfos["customer"].Exec = &clientcmdapi.ExecConfig{
		APIVersion: "client.authentication.k8s.io/v1", Command: "airo",
		Args:            []string{"credential", "--issuer=https://issuer.example", "--client-id=client"},
		InteractiveMode: clientcmdapi.NeverExecInteractiveMode,
	}
	if err := clientcmd.WriteToFile(*cfg, config); err != nil {
		t.Fatal(err)
	}
	cache := t.TempDir()
	nsFile := path.Join(cache, "namespace.yaml")
	agentFile := path.Join(cache, "agent.yaml")
	for file, manifest := range map[string]string{
		nsFile:    "apiVersion: v1\nkind: Namespace\nmetadata:\n  name: demo\n",
		agentFile: "apiVersion: example.test/v1\nkind: Agent\nmetadata:\n  name: demo\n",
	} {
		if err := os.WriteFile(file, []byte(manifest), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, tt := range []struct {
		command, want string
		writes, fails bool
	}{
		{"agent list --tenant acme -o name", "acme-web-agent", false, false},
		{"tenant acme", "Project: acme-web", true, false},
		{"tenant beta", "Project: beta-default", true, false},
		{"project beta-api", "Switched to project beta-api", true, false},
		{"tenant acme", "Project: acme-web", true, false},
		{"status --tenant beta", "Project:     beta-api", false, false},
		{"status", "Tenant:      acme", false, false},
		{"whoami", "Tenant:  acme, beta", false, false},
		{"agent list --tenant beta -o name", "beta-api-agent", false, false},
		{"agent list -o name", "acme-web-agent", false, false},
		{"--tenant beta --project beta-default agent list -o name", "beta-default-agent", false, false},
		{"--tenant beta --namespace beta-default agent list -o name", "beta-default-agent", false, false},
		{"agent list --context beta -o name", "beta-api-agent", false, false},
		{"agent list --tenant beta --project acme-web", "unavailable in tenant beta", false, true},
		{"project edit acme-web --tenant beta", "unavailable in tenant beta", false, true},
		{"project delete acme-web --tenant beta", "unavailable in tenant beta", false, true},
		{"project list --tenant beta -o name", "beta-api", false, false},
		{"project create new-first --tenant new", "airo tenant new --project new-first", false, false},
		{"tenant new", "Project: none", true, false},
		{"agent list", "has no project selected", false, true},
		{"agent skills search", "give the words to search for", false, true},
		{"create --dry-run=client --validate=false -o name -f " + nsFile, "namespace/demo", false, false},
		{"create --dry-run=client --validate=false -f " + agentFile, "has no project selected", false, true},
		{"project new-first", "Switched to project new-first", true, false},
		{"tenant beta", "Project: beta-api", true, false},
	} {
		t.Run(tt.command, func(t *testing.T) {
			before, err := os.ReadFile(config)
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			root := newRoot()
			root.SetOut(&out)
			root.SetErr(&out)
			root.SetArgs(append([]string{"--kubeconfig", config, "--cache-dir", cache}, strings.Fields(tt.command)...))
			err = root.Execute()
			if (err != nil) != tt.fails || !strings.Contains(out.String(), tt.want) {
				t.Fatalf("%s: output %q, error %v, want %q", tt.command, out.String(), err, tt.want)
			}
			after, err := os.ReadFile(config)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Equal(before, after) == tt.writes {
				t.Errorf("saved selection changed: %t, want %t", !bytes.Equal(before, after), tt.writes)
			}
		})
	}

	// Shell completion lists in the project a command would use, which
	// the kubeconfig alone decides: tenant beta is now selected.
	for _, tt := range []struct {
		words      string
		want, omit []string
	}{
		{"project", []string{"beta-api\n", "beta-default\n", "list\n"}, []string{"acme-web"}},
		{"--tenant acme project", []string{"acme-web\n"}, []string{"beta-api"}},
		{"agent list --project", []string{"beta-api\n", "beta-default\n"}, []string{"acme-web"}},
		{"agent describe", []string{"beta-api-agent\n"}, nil},
		{"agent describe --project beta-default", []string{"beta-default-agent\n"}, nil},
		{"agent describe beta-api-agent", nil, []string{"agent\n"}},
		// The root offers the kinds no command names, then their verbs.
		{"", []string{"agent\n", "namespaces\n", "secrets\n", "widgets\n"}, []string{"agents\n"}},
		{"widgets", []string{"describe\n", "list\n"}, nil},
	} {
		t.Run("complete "+tt.words, func(t *testing.T) {
			var out bytes.Buffer
			root := newRoot()
			root.SetOut(&out)
			root.SetErr(io.Discard)
			args := append([]string{"__completeNoDesc", "--kubeconfig", config, "--cache-dir", cache}, strings.Fields(tt.words)...)
			root.SetArgs(append(args, ""))
			if err := root.Execute(); err != nil {
				t.Fatal(err)
			}
			for _, want := range tt.want {
				if !strings.Contains(out.String(), want) {
					t.Errorf("missing %q in:\n%s", want, &out)
				}
			}
			for _, omit := range tt.omit {
				if strings.Contains(out.String(), omit) {
					t.Errorf("unexpected %q in:\n%s", omit, &out)
				}
			}
		})
	}
}
