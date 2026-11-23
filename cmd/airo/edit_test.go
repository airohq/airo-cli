package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// TestResourceEdit runs kubectl's edit against a fake cluster, with an
// "editor" that is a script changing the file as a person would.
func TestResourceEdit(t *testing.T) {
	const widget = testResourceAPI + "/namespaces/acme-web/widgets/sample"
	discovery := resourceDiscovery()
	for _, tt := range []struct {
		name       string
		args       []string
		script     string // the editor, as a shell command on the file "$1"
		wantPatch  string
		wantOutput string
		wantError  string
	}{
		{name: "changed", args: []string{"widgets", "edit", "sample"}, script: `sed -i.bak 's/color: red/color: blue/' "$1"`,
			wantPatch: `"color":"blue"`, wantOutput: "widget.example.test/sample edited"},
		{name: "built-in noun", args: []string{"agent", "edit", "sample"}, script: `sed -i.bak 's/color: red/color: blue/' "$1"`,
			wantPatch: `"color":"blue"`, wantOutput: "edited"},
		{name: "as json", args: []string{"widgets", "edit", "sample", "-o", "json"}, script: `sed -i.bak 's/"red"/"blue"/' "$1"`,
			wantPatch: `"color":"blue"`, wantOutput: `"color": "blue"`},
		{name: "unchanged", args: []string{"widgets", "edit", "sample"}, script: `true`, wantOutput: "Edit cancelled, no changes made."},
		{name: "selector", args: []string{"widgets", "edit", "sample", "-l", "env=test"}, wantError: "--selector"},
		{name: "table output", args: []string{"widgets", "edit", "sample", "-o", "wide"}, wantError: "-o yaml or -o json"},
		{name: "missing name", args: []string{"widgets", "edit"}, wantError: "edit NAME"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			editor := filepath.Join(dir, "editor.sh")
			seen := filepath.Join(dir, "seen")
			if err := os.WriteFile(editor, []byte("#!/bin/sh\ncat \"$1\" >> "+seen+"\n"+tt.script+"\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("KUBE_EDITOR", editor)
			path := widget
			if tt.args[0] == "agent" {
				path = testResourceAPI + "/namespaces/acme-web/agents/sample"
			}
			var patches atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if _, pattern := discovery.Handler(r); pattern != "" && r.Method == http.MethodGet {
					discovery.ServeHTTP(w, r)
					return
				}
				if r.URL.Path != path {
					t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
					http.NotFound(w, r)
					return
				}
				kind := "Widget"
				if tt.args[0] == "agent" {
					kind = "Agent"
				}
				object := `{"apiVersion":"example.test/v1","kind":"` + kind + `","metadata":{"name":"sample","namespace":"acme-web","resourceVersion":"1"},"spec":{"color":"%s"}}`
				switch r.Method {
				case http.MethodGet:
					io.WriteString(w, strings.Replace(object, "%s", "red", 1))
				case http.MethodPatch:
					patches.Add(1)
					body, _ := io.ReadAll(r.Body)
					if !strings.Contains(string(body), tt.wantPatch) || r.URL.Query().Get("fieldManager") != "airo-edit" {
						t.Errorf("patch %s with %s, want %s by airo-edit", body, r.URL.RawQuery, tt.wantPatch)
					}
					io.WriteString(w, strings.Replace(object, "%s", "blue", 1))
				default:
					t.Errorf("unexpected method %s", r.Method)
				}
			}))
			defer server.Close()
			// kubectl's validation reads the cluster's schemas, which this
			// fake does not serve; it is kubectl's own and tested there.
			args := append([]string{"--cache-dir", t.TempDir(), "-n", "acme-web"}, tt.args...)
			if tt.wantError == "" {
				args = append(args, "--validate=false")
			}
			// kubectl says on stderr that nothing changed, so read both.
			root := newRoot()
			var out bytes.Buffer
			root.SetOut(&out)
			root.SetErr(&out)
			root.SetIn(strings.NewReader(""))
			root.SetArgs(append([]string{"--kubeconfig", identityConfig(t, server.URL)}, args...))
			err := root.Execute()
			output := out.String()
			if tt.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantError) {
					t.Errorf("output %q, error %v; want %s", output, err, tt.wantError)
				}
				return
			}
			if err != nil || !strings.Contains(output, tt.wantOutput) {
				opened, _ := os.ReadFile(seen)
				t.Errorf("output %q, error %v; want %q. The editor opened:\n%s", output, err, tt.wantOutput, opened)
			}
			if want := int32(0); tt.wantPatch != "" {
				want = 1
				if patches.Load() != want {
					t.Errorf("sent %d patches, want %d", patches.Load(), want)
				}
			} else if patches.Load() != 0 {
				t.Errorf("sent %d patches, want none", patches.Load())
			}
		})
	}
}
