package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestResourceDelete(t *testing.T) {
	const widgets = testResourceAPI + "/namespaces/acme-web/widgets/sample"
	discovery := resourceDiscovery()
	for _, tt := range []struct {
		name       string
		args       []string
		path       string
		dryRun     bool
		wait       bool
		denied     bool
		wantOutput string
		wantError  string
	}{
		{name: "short name and default wait", args: []string{"wd", "delete", "sample"}, path: widgets, wait: true, wantOutput: `widget.example.test "sample" deleted from acme-web namespace`},
		{name: "built-in noun via discovery", args: []string{"agents", "delete", "sample", "-n", "other", "--wait=false", "-o", "name"}, path: testResourceAPI + "/namespaces/other/agents/sample", wantOutput: "agent.example.test/sample"},
		{name: "qualified resource and server dry run", args: []string{"widgets.v1.example.test", "delete", "sample", "--dry-run=server"}, path: widgets, dryRun: true, wantOutput: "(server dry run)"},
		{name: "project dry run", args: []string{"project", "delete", "acme-web", "--dry-run=server"}, path: "/api/v1/namespaces/acme-web", dryRun: true, wantOutput: "(server dry run)"},
		{name: "cluster-scoped client dry run", args: []string{"namespaces", "delete", "sample", "--dry-run=client"}, wantOutput: `namespace "sample" deleted (dry run)`},
		{name: "denied", args: []string{"widgets", "delete", "sample"}, path: widgets, denied: true, wantError: "cannot delete widgets"},
		{name: "missing name", args: []string{"widgets", "delete"}, wantError: "delete NAME"},
		{name: "multiple resources", args: []string{"agents,widgets", "delete", "sample"}, wantError: "single resource and name"},
		{name: "selector with name", args: []string{"widgets", "delete", "sample", "-l", "env=test"}, wantError: "--selector"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var deletes, reads atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Header.Get("Authorization") != "Bearer customer-token" {
					t.Error("wrong credentials")
				}
				if _, pattern := discovery.Handler(r); pattern != "" && r.Method == http.MethodGet {
					discovery.ServeHTTP(w, r)
					return
				}
				if r.URL.Path != tt.path {
					t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
					http.NotFound(w, r)
					return
				}
				switch r.Method {
				case http.MethodDelete:
					deletes.Add(1)
					var opts metav1.DeleteOptions
					if err := json.NewDecoder(r.Body).Decode(&opts); err != nil {
						t.Error("invalid delete options: ", err)
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					if opts.PropagationPolicy == nil || *opts.PropagationPolicy != metav1.DeletePropagationBackground || opts.GracePeriodSeconds != nil || (strings.Join(opts.DryRun, ",") == "All") != tt.dryRun {
						t.Errorf("unexpected delete options: %+v", opts)
					}
					if tt.denied {
						w.WriteHeader(http.StatusForbidden)
						io.WriteString(w, `{"apiVersion":"v1","kind":"Status","status":"Failure","reason":"Forbidden","code":403,"message":"cannot delete widgets"}`)
						return
					}
					io.WriteString(w, `{"apiVersion":"v1","kind":"Status","status":"Success","details":{"uid":"original"}}`)
				case http.MethodGet:
					reads.Add(1)
					w.WriteHeader(http.StatusNotFound)
					io.WriteString(w, `{"apiVersion":"v1","kind":"Status","status":"Failure","reason":"NotFound","code":404}`)
				default:
					t.Errorf("unexpected method %s", r.Method)
					w.WriteHeader(http.StatusMethodNotAllowed)
				}
			}))
			defer server.Close()
			args := append([]string{"--cache-dir", t.TempDir()}, tt.args...)
			output, err := runIdentityCommand(t, identityConfig(t, server.URL), args...)
			if tt.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantError) || output != "" {
					t.Errorf("output %q, error %v; want %s", output, err, tt.wantError)
				}
			} else if err != nil || !strings.Contains(output, tt.wantOutput) {
				t.Errorf("output %q, error %v; want %q", output, err, tt.wantOutput)
			}
			wantDeletes := int32(0)
			if tt.path != "" {
				wantDeletes = 1
			}
			if deletes.Load() != wantDeletes || (reads.Load() > 0) != tt.wait {
				t.Errorf("sent %d deletes and %d reads", deletes.Load(), reads.Load())
			}
		})
	}
}
