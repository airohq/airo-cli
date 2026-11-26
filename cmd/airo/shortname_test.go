package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spf13/pflag"
)

// A short name from a CRD runs the airo command that owns the kind, as do
// the kind's other names; a short name of a kind without one stays generic.
func TestShortNamesRunTheOwningCommand(t *testing.T) {
	discovery := http.NewServeMux()
	discovery.Handle("/api", resourceDiscovery())
	discovery.Handle("/api/v1", resourceDiscovery())
	discovery.Handle(testResourceAPI, resourceDiscovery())
	discovery.HandleFunc("/apis/tunnels.airohq.dev/v1alpha1", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"kind":"APIResourceList","groupVersion":"tunnels.airohq.dev/v1alpha1","resources":[{"name":"tunnels","singularName":"tunnel","kind":"Tunnel","namespaced":true,"shortNames":["tun"],"verbs":["get","list","delete"]}]}`)
	})
	discovery.HandleFunc("/apis", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"kind":"APIGroupList","apiVersion":"v1","groups":[`+
			`{"name":"example.test","versions":[{"groupVersion":"example.test/v1","version":"v1"}],"preferredVersion":{"groupVersion":"example.test/v1","version":"v1"}},`+
			`{"name":"tunnels.airohq.dev","versions":[{"groupVersion":"tunnels.airohq.dev/v1alpha1","version":"v1alpha1"}],"preferredVersion":{"groupVersion":"tunnels.airohq.dev/v1alpha1","version":"v1alpha1"}}]}`)
	})
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if _, pattern := discovery.Handler(r); pattern != "" {
			discovery.ServeHTTP(w, r)
			return
		}
		t.Errorf("unexpected %s %s: these read only discovery", r.Method, r.URL.Path)
		http.NotFound(w, r)
	}))
	defer server.Close()
	config := identityConfig(t, server.URL)
	for _, tt := range []struct {
		name string
		args []string
		want []string
		not  string
	}{
		{"short name", []string{"tun", "-h"}, []string{"airo tunnel PORT|HOST:PORT", "Tunnel Commands:"}, "is a kind the cluster serves"},
		{"short name and verb", []string{"tun", "create", "--help"}, []string{"airo tunnel create"}, "is a kind the cluster serves"},
		{"qualified name", []string{"tunnels.tunnels.airohq.dev", "-h"}, []string{"airo tunnel PORT|HOST:PORT"}, "is a kind the cluster serves"},
		{"flags first", []string{"--project", "tun", "tun", "-h"}, []string{"airo tunnel PORT|HOST:PORT"}, "is a kind the cluster serves"},
		{"kind without a command", []string{"wd", "-h"}, []string{"Widget is a kind the cluster serves"}, "Tunnel Commands:"},
		{"completion after a short name", []string{"__completeNoDesc", "tun", "cr"}, []string{"create\n"}, "is a kind"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			args := append([]string{"--kubeconfig", config, "--cache-dir", t.TempDir()}, tt.args...)
			if strings.HasPrefix(tt.args[0], "__complete") {
				args = append([]string{tt.args[0], "--kubeconfig", config, "--cache-dir", t.TempDir()}, tt.args[1:]...)
			}
			root := newRootFor(args)
			var out bytes.Buffer
			root.SetOut(&out)
			root.SetErr(&out)
			if err := root.Execute(); err != nil {
				t.Fatal(err)
			}
			for _, want := range tt.want {
				if !strings.Contains(out.String(), want) {
					t.Errorf("output lacks %q:\n%s", want, &out)
				}
			}
			if strings.Contains(out.String(), tt.not) {
				t.Errorf("output has %q:\n%s", tt.not, &out)
			}
		})
	}
}

func TestFirstWord(t *testing.T) {
	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	fs.StringP("output", "o", "", "")
	fs.BoolP("all-namespaces", "A", false, "")
	for _, tt := range []struct {
		args []string
		want int
	}{
		{[]string{"ag", "list"}, 0},
		{[]string{"-o", "yaml", "ag"}, 2},
		{[]string{"--output", "yaml", "ag"}, 2},
		{[]string{"--output=yaml", "-A", "ag"}, 2},
		{[]string{"-oyaml", "ag"}, 1},
		{[]string{"--unknown", "ag"}, 1},
		{[]string{"-A"}, -1},
		{[]string{"--", "ag"}, -1},
	} {
		if got := firstWord(fs, tt.args, 0); got != tt.want {
			t.Errorf("firstWord(%v) = %d, want %d", tt.args, got, tt.want)
		}
	}
}
