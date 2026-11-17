package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// These commands must ask the API as the selected identity, without needing
// discovery, namespace access, or a separate platform endpoint.
func identityServer(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/apis/authentication.k8s.io/v1/selfsubjectreviews" {
			t.Errorf("unexpected API request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read identity request: %v", err)
			return
		}
		_, kind, err := scheme.Codecs.UniversalDeserializer().Decode(body, nil, nil)
		if err != nil {
			t.Errorf("decode identity request: %v", err)
			return
		}
		if kind.Kind != "SelfSubjectReview" || kind.GroupVersion().String() != "authentication.k8s.io/v1" {
			t.Errorf("unexpected identity request: %v", kind)
		}
		username := "auth0:abc123"
		groups := []string{"customer:acme", "system:authenticated"}
		switch r.Header.Get("Authorization") {
		case "Bearer customer-token":
		case "Bearer admin-token":
			username, groups = "auth0:admin", []string{"platform-admins"}
		case "Bearer multi-token":
			groups = []string{"customer:acme", "customer:other", "customer:acme", "customer:", "system:authenticated"}
		case "Bearer empty-token":
			groups = nil
		default:
			t.Error("request did not use the selected kubeconfig credentials")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if as := r.Header.Get("Impersonate-User"); as != "" {
			username, groups = as, r.Header.Values("Impersonate-Group")
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"apiVersion": "authentication.k8s.io/v1", "kind": "SelfSubjectReview",
			"status": map[string]any{"userInfo": map[string]any{
				"username": username, "groups": groups, "futureField": "ignored",
			}},
		})
	}))
	t.Cleanup(server.Close)
	return server, &calls
}

func identityConfig(t *testing.T, server string) string {
	t.Helper()
	cfg := clientcmdapi.NewConfig()
	cfg.Clusters["platform"] = &clientcmdapi.Cluster{Server: server, InsecureSkipTLSVerify: true}
	for _, name := range []string{"customer", "admin", "multi", "empty"} {
		cfg.AuthInfos[name] = &clientcmdapi.AuthInfo{Token: name + "-token"}
		cfg.Contexts[name] = &clientcmdapi.Context{Cluster: "platform", AuthInfo: name}
	}
	cfg.Contexts["customer"].Namespace = "acme-web"
	cfg.CurrentContext = "customer"
	path := filepath.Join(t.TempDir(), "config")
	if err := clientcmd.WriteToFile(*cfg, path); err != nil {
		t.Fatal(err)
	}
	return path
}

func runIdentityCommand(t *testing.T, config string, args ...string) (string, error) {
	t.Helper()
	before, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	root := newRoot()
	var out, stderr bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&stderr)
	root.SetIn(strings.NewReader(""))
	root.SetArgs(append([]string{"--kubeconfig", config}, args...))
	err = root.Execute()
	if err == nil && stderr.Len() > 0 {
		t.Errorf("unexpected stderr: %s", &stderr)
	}
	after, readErr := os.ReadFile(config)
	if readErr != nil || !bytes.Equal(before, after) {
		t.Errorf("command changed kubeconfig: %v", readErr)
	}
	return out.String(), err
}

func outputFields(t *testing.T, output string) map[string]string {
	t.Helper()
	fields := make(map[string]string)
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			t.Fatalf("output is not a labelled field: %q", line)
		}
		if _, exists := fields[key]; exists {
			t.Fatalf("duplicate output field: %s", key)
		}
		fields[key] = strings.TrimSpace(value)
	}
	return fields
}

func TestStatusAndWhoami(t *testing.T) {
	server, calls := identityServer(t)
	override, overrideCalls := identityServer(t)
	config := identityConfig(t, server.URL)
	for _, tt := range []struct {
		name    string
		args    []string
		user    string
		tenant  string
		groups  string
		project string
		context string
		server  string
	}{
		{name: "status", args: []string{"status"}, user: "auth0:abc123", tenant: "acme", project: "acme-web", context: "customer"},
		{name: "whoami", args: []string{"whoami"}, user: "auth0:abc123", tenant: "acme", groups: "customer:acme, system:authenticated"},
		{name: "project override", args: []string{"status", "-n", "other-team"}, user: "auth0:abc123", tenant: "acme", project: "other-team", context: "customer"},
		{name: "context override and default project", args: []string{"status", "--context", "admin"}, user: "auth0:admin", tenant: "none", project: "default", context: "admin"},
		{name: "admin identity", args: []string{"whoami", "--context", "admin"}, user: "auth0:admin", tenant: "none", groups: "platform-admins"},
		{name: "several tenants", args: []string{"whoami", "--context", "multi"}, user: "auth0:abc123", tenant: "acme, other", groups: "customer:acme, customer:other, customer:acme, customer:, system:authenticated"},
		{name: "no groups", args: []string{"whoami", "--context", "empty"}, user: "auth0:abc123", tenant: "none", groups: "none"},
		{name: "server override", args: []string{"status", "--server", override.URL}, user: "auth0:abc123", tenant: "acme", project: "acme-web", context: "customer", server: override.URL},
		{name: "whoami impersonation", args: []string{"whoami", "--as", "teammate", "--as-group", "customer:other", "--as-group", "readers"}, user: "teammate", tenant: "other", groups: "customer:other, readers"},
		{name: "status impersonation", args: []string{"status", "--as", "teammate", "--as-group", "customer:other"}, user: "teammate", tenant: "other", project: "acme-web", context: "customer"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			before := calls.Load() + overrideCalls.Load()
			output, err := runIdentityCommand(t, config, tt.args...)
			if err != nil {
				t.Fatal(err)
			}
			want := map[string]string{"User": tt.user, "Tenant": tt.tenant}
			if tt.args[0] == "status" {
				want["Project"], want["Context"], want["Connection"] = tt.project, tt.context, "Connected"
				want["Server"] = server.URL
				if tt.server != "" {
					want["Server"] = tt.server
				}
			} else {
				want["Groups"] = tt.groups
			}
			if got := outputFields(t, output); !reflect.DeepEqual(got, want) {
				t.Errorf("output = %v, want %v", got, want)
			}
			if got := calls.Load() + overrideCalls.Load() - before; got != 1 {
				t.Errorf("made %d API requests, want one identity check", got)
			}
		})
	}
	if overrideCalls.Load() != 1 {
		t.Error("--server did not select the requested API server")
	}
}

func TestIdentityFailures(t *testing.T) {
	for _, tt := range []struct {
		name string
		code int
		body string
		want string
	}{
		{"unauthorized", http.StatusUnauthorized, `{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"Unauthorized","message":"unauthorized","code":401}`, "unauthorized"},
		{"forbidden", http.StatusForbidden, `{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"Forbidden","message":"identity check forbidden","code":403}`, "forbidden"},
		{"missing username", http.StatusOK, `{"kind":"SelfSubjectReview","apiVersion":"authentication.k8s.io/v1","status":{"userInfo":{"groups":["customer:acme"]}}}`, "status.userInfo.username"},
		{"unavailable", 0, "", "connect"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.code)
				fmt.Fprint(w, tt.body)
			}))
			t.Cleanup(server.Close)
			if tt.code == 0 {
				server.Close()
			}
			config := identityConfig(t, server.URL)
			for _, command := range []string{"status", "whoami"} {
				output, err := runIdentityCommand(t, config, command)
				if err == nil || !strings.Contains(err.Error(), tt.want) {
					t.Fatalf("%s error = %v, want %q", command, err, tt.want)
				}
				if command == "whoami" {
					if output != "" {
						t.Errorf("whoami printed an unverified identity: %s", output)
					}
					continue
				}
				want := map[string]string{
					"User": "unknown", "Tenant": "unknown", "Project": "acme-web",
					"Context": "customer", "Server": server.URL, "Connection": "Unverified",
				}
				if got := outputFields(t, output); !reflect.DeepEqual(got, want) {
					t.Errorf("failed status output = %v, want %v", got, want)
				}
			}
		})
	}
}

func TestIdentityCommandValidation(t *testing.T) {
	server, calls := identityServer(t)
	config := identityConfig(t, server.URL)
	empty := filepath.Join(t.TempDir(), "config")
	if err := clientcmd.WriteToFile(*clientcmdapi.NewConfig(), empty); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"status", "whoami"} {
		for _, tt := range []struct {
			config string
			args   []string
		}{
			{config, []string{command, "extra"}},
			{config, []string{command, "--context", "missing"}},
			{empty, []string{command}},
		} {
			output, err := runIdentityCommand(t, tt.config, tt.args...)
			if err == nil || output != "" {
				t.Errorf("%v: output %q, error %v; want an error and no output", tt.args, output, err)
			}
		}
	}
	if calls.Load() != 0 {
		t.Error("invalid invocation contacted the API")
	}
}
