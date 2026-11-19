package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCreateManifest(t *testing.T) {
	const manifest = "apiVersion: example.test/v1\nkind: Widget\nmetadata:\n  name: sample\nspec:\n  score: 42\n"
	path := filepath.Join(t.TempDir(), "widget.yaml")
	if err := os.WriteFile(path, []byte(manifest), 0600); err != nil {
		t.Fatal(err)
	}
	discovery := resourceDiscovery()
	for _, mode := range []string{"create", "duplicate", "server dry run", "client dry run"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if _, pattern := discovery.Handler(r); pattern != "" {
					discovery.ServeHTTP(w, r)
					return
				}
				if r.URL.Path != testResourceAPI+"/namespaces/acme-web/widgets" || r.Method != "POST" || mode == "client dry run" {
					t.Errorf("unexpected request %s %s", r.Method, r.URL)
					http.NotFound(w, r)
					return
				}
				if (r.URL.Query().Get("dryRun") == "All") != (mode == "server dry run") {
					t.Errorf("unexpected dryRun query %s", r.URL)
				}
				if mode == "duplicate" {
					w.WriteHeader(http.StatusConflict)
					io.WriteString(w, `{"apiVersion":"v1","kind":"Status","status":"Failure","reason":"AlreadyExists","message":"widgets.example.test \"sample\" already exists","code":409}`)
					return
				}
				var obj map[string]any
				if err := json.NewDecoder(r.Body).Decode(&obj); err != nil {
					t.Error(err)
				}
				w.WriteHeader(http.StatusCreated)
				json.NewEncoder(w).Encode(obj)
			}))
			defer server.Close()
			args := []string{"--cache-dir", t.TempDir(), "create", "-f", path, "--validate=false", "-o", "name"}
			if strings.HasSuffix(mode, "dry run") {
				args = append(args, "--dry-run="+strings.Fields(mode)[0])
			}
			out, err := runIdentityCommand(t, identityConfig(t, server.URL), args...)
			if mode == "duplicate" {
				if err == nil || !strings.Contains(err.Error(), "already exists") {
					t.Fatalf("expected duplicate resource error, got %v", err)
				}
				return
			}
			if err != nil || strings.TrimSpace(out) != "widget.example.test/sample" {
				t.Fatalf("output %q, error %v", out, err)
			}
		})
	}
}

func TestCreateMissingInputInScript(t *testing.T) {
	for _, tt := range []struct {
		args []string
		want string
	}{
		{[]string{"tunnel", "create"}, "provide NAME"},
		{[]string{"agent", "create", "--template", "sample"}, "provide NAME"},
		{[]string{"agent", "create", "reviewer"}, "--prompt-file"},
		{[]string{"create"}, "-f"},
	} {
		root := newRoot()
		var out, stderr bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&stderr)
		root.SetIn(strings.NewReader(""))
		root.SetArgs(append([]string{"--kubeconfig", identityConfig(t, "https://unused.invalid")}, tt.args...))
		err := root.Execute()
		if err == nil || !strings.Contains(err.Error(), tt.want) || strings.Contains(stderr.String(), "? ") || out.Len() != 0 {
			t.Errorf("%v: output %q, stderr %q, error %v", tt.args, &out, &stderr, err)
		}
	}
}
