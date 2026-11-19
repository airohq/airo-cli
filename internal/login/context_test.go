package login

import (
	"bytes"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// contextFixture writes a kubeconfig with the sign-ins airo and customer and
// a local cluster's context kind, with airo as the current one.
func contextFixture(t *testing.T) (clientcmd.ConfigAccess, previousStore) {
	t.Helper()
	dir := t.TempDir()
	cfg := clientcmdapi.NewConfig()
	for _, name := range []string{"customer", "airo"} {
		cfg.Clusters[name] = &clientcmdapi.Cluster{Server: "https://api.example.com"}
		cfg.AuthInfos[name] = execUser(oidc{issuer: "https://login.example.com/", clientID: "abc", session: name}, "")
		cfg.Contexts[name] = &clientcmdapi.Context{Cluster: name, AuthInfo: name, Namespace: name + "-default"}
	}
	cfg.Clusters["kind"] = &clientcmdapi.Cluster{Server: "https://127.0.0.1:6443"}
	cfg.AuthInfos["kind"] = &clientcmdapi.AuthInfo{Token: "t"}
	cfg.Contexts["kind"] = &clientcmdapi.Context{Cluster: "kind", AuthInfo: "kind"}
	cfg.CurrentContext = "airo"

	rules := &clientcmd.ClientConfigLoadingRules{ExplicitPath: filepath.Join(dir, "config")}
	if err := clientcmd.ModifyConfig(rules, *cfg, true); err != nil {
		t.Fatal(err)
	}
	return rules, previousStore{path: filepath.Join(dir, "cache", "previous-context")}
}

func currentContext(t *testing.T, access clientcmd.ConfigAccess) string {
	t.Helper()
	cfg, err := access.GetStartingConfig()
	if err != nil {
		t.Fatal(err)
	}
	return cfg.CurrentContext
}

func TestSwitchContextAndBack(t *testing.T) {
	access, store := contextFixture(t)
	for _, step := range []struct {
		target, want, out string
	}{
		{"customer", "customer", "Switched to context customer\n  namespace: customer-default\n"},
		{"-", "airo", "Switched to context airo\n  namespace: airo-default\n"},
		{"-", "customer", "Switched to context customer\n  namespace: customer-default\n"},
		// Nothing changes, so "-" still leads back to airo.
		{"customer", "customer", "Already in context customer\n"},
		{"-", "airo", "Switched to context airo\n  namespace: airo-default\n"},
	} {
		var out bytes.Buffer
		if err := switchContext(&out, access, store, step.target, nil); err != nil {
			t.Fatalf("switch to %q: %v", step.target, err)
		}
		if got := currentContext(t, access); got != step.want {
			t.Fatalf("after %q the context is %q, want %q", step.target, got, step.want)
		}
		if out.String() != step.out {
			t.Errorf("after %q the output is %q, want %q", step.target, out.String(), step.out)
		}
	}
}

func TestSwitchContextRefuses(t *testing.T) {
	for _, tc := range []struct {
		name, target, want string
	}{
		{"an unknown name", "nope", `the kubeconfig has no context "nope": your sign-ins are airo, customer`},
		{"a context of another cluster", "kind", `context "kind" does not sign in through the platform: your sign-ins are airo, customer`},
		{"back with nothing remembered", "-", "remembers no previous context"},
		{"no name and nobody to ask", "", "name the context to switch to: airo, customer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			access, store := contextFixture(t)
			err := switchContext(&bytes.Buffer{}, access, store, tc.target, nil)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to contain %q", err, tc.want)
			}
			if got := currentContext(t, access); got != "airo" {
				t.Errorf("the context is %q, want it unchanged", got)
			}
		})
	}
}

func TestSwitchContextAsks(t *testing.T) {
	access, store := contextFixture(t)
	ask := func(names []string, current int) (string, error) {
		if strings.Join(names, ",") != "airo,customer" || current != 0 {
			t.Errorf("asked with %v and current %d, want the sign-ins with airo as the current one", names, current)
		}
		return "customer", nil
	}
	if err := switchContext(&bytes.Buffer{}, access, store, "", ask); err != nil {
		t.Fatal(err)
	}
	if got := currentContext(t, access); got != "customer" {
		t.Errorf("the context is %q, want customer", got)
	}

	// Leaving the picker changes nothing.
	var out bytes.Buffer
	none := func([]string, int) (string, error) { return "", nil }
	if err := switchContext(&out, access, store, "", none); err != nil {
		t.Fatal(err)
	}
	if got := currentContext(t, access); got != "customer" || out.Len() != 0 {
		t.Errorf("the context is %q and the output %q, want customer and none", got, out.String())
	}
}

// Coming from a local cluster's context works, and it is not remembered:
// "-" could not switch to it.
func TestSwitchContextFromAnotherCluster(t *testing.T) {
	access, store := contextFixture(t)
	cfg, err := access.GetStartingConfig()
	if err != nil {
		t.Fatal(err)
	}
	cfg.CurrentContext = "kind"
	if err := clientcmd.ModifyConfig(access, *cfg, true); err != nil {
		t.Fatal(err)
	}
	if err := switchContext(&bytes.Buffer{}, access, store, "airo", nil); err != nil {
		t.Fatal(err)
	}
	if got := currentContext(t, access); got != "airo" {
		t.Errorf("the context is %q, want airo", got)
	}
	if got := store.get(); got != "" {
		t.Errorf("the previous context is %q, want none", got)
	}
}

func TestListContexts(t *testing.T) {
	access, _ := contextFixture(t)
	var out bytes.Buffer
	if err := listContexts(&out, access); err != nil {
		t.Fatal(err)
	}
	want := "CURRENT   NAME       NAMESPACE\n" +
		"*         airo       airo-default\n" +
		"          customer   customer-default\n"
	if out.String() != want {
		t.Errorf("list =\n%s\nwant\n%s", out.String(), want)
	}
}

func TestSetContext(t *testing.T) {
	access, _ := contextFixture(t)
	var out bytes.Buffer
	if err := setContext(&out, access, "", platform{server: "https://api.new.example"}, "acme-web"); err != nil {
		t.Fatal(err)
	}
	if want := "Set context airo\n  server:    https://api.new.example\n  namespace: acme-web\n"; out.String() != want {
		t.Errorf("the output is %q, want %q", out.String(), want)
	}
	cfg, err := access.GetStartingConfig()
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Clusters["airo"].Server; got != "https://api.new.example" {
		t.Errorf("the server is %q", got)
	}
	if got := cfg.Contexts["airo"].Namespace; got != "acme-web" {
		t.Errorf("the namespace is %q", got)
	}
	if got := cfg.Clusters["customer"].Server; got != "https://api.example.com" {
		t.Errorf("another sign-in's server changed to %q", got)
	}
	if o, err := oidcOf(cfg, "airo"); err != nil || o.issuer != "https://login.example.com/" || o.session != "airo" {
		t.Errorf("the sign-in changed: %+v, %v", o, err)
	}

	// A named context, and the values of the sign-in itself.
	if err := setContext(&out, access, "customer", platform{ca: []byte("ca"), issuer: "https://new.example/"}, ""); err != nil {
		t.Fatal(err)
	}
	if cfg, err = access.GetStartingConfig(); err != nil {
		t.Fatal(err)
	}
	if got := string(cfg.Clusters["customer"].CertificateAuthorityData); got != "ca" {
		t.Errorf("the certificate authority is %q", got)
	}
	if o, err := oidcOf(cfg, "customer"); err != nil || o.issuer != "https://new.example/" || o.clientID != "abc" || o.session != "customer" {
		t.Errorf("the sign-in is %+v, %v", o, err)
	}
	if cfg.CurrentContext != "airo" {
		t.Errorf("the current context changed to %q", cfg.CurrentContext)
	}
}

func TestSetContextSaysWhoSharesTheCluster(t *testing.T) {
	access, _ := contextFixture(t)
	cfg, err := access.GetStartingConfig()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Contexts["customer"].Cluster = "airo"
	if err := clientcmd.ModifyConfig(access, *cfg, true); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := setContext(&out, access, "airo", platform{server: "https://api.new.example"}, ""); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "serves customer") {
		t.Errorf("the output does not name the other sign-in: %q", out.String())
	}
}

func TestSetContextRefuses(t *testing.T) {
	for _, tc := range []struct {
		name, target string
		p            platform
		want         string
	}{
		{"nothing to set", "", platform{}, "nothing to set"},
		{"an address that is none", "", platform{server: "api.example.com"}, "is not an address"},
		{"an unknown name", "nope", platform{server: "https://a.example"}, `the kubeconfig has no context "nope": your sign-ins are airo, customer`},
		{"a context of another cluster", "kind", platform{server: "https://a.example"}, `context "kind" does not sign in through the platform`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			access, _ := contextFixture(t)
			err := setContext(&bytes.Buffer{}, access, tc.target, tc.p, "")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("the error is %v, want it to contain %q", err, tc.want)
			}
			cfg, err := access.GetStartingConfig()
			if err != nil {
				t.Fatal(err)
			}
			if got := cfg.Clusters["kind"].Server; got != "https://127.0.0.1:6443" {
				t.Errorf("the other cluster's server changed to %q", got)
			}
		})
	}
}

func TestContextDelete(t *testing.T) {
	for _, tt := range []struct {
		name      string
		args      []string
		deleted   string
		active    bool
		wantError string
	}{
		{name: "inactive", args: []string{"contexts", "delete", "customer"}, deleted: "customer"},
		{name: "active", args: []string{"ctx", "delete", "airo"}, deleted: "airo", active: true},
		{name: "not a sign-in", args: []string{"context", "delete", "kind"}, wantError: "does not sign in through the platform"},
		{name: "unknown context", args: []string{"context", "delete", "missing"}, wantError: "has no context"},
		{name: "missing name", args: []string{"context", "delete"}, wantError: "accepts 1 arg"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			access, _ := contextFixture(t)
			before, err := access.GetStartingConfig()
			if err != nil {
				t.Fatal(err)
			}
			// A second context uses the same cluster and credentials.
			before.Contexts["shared"] = before.Contexts["airo"].DeepCopy()
			if err := clientcmd.ModifyConfig(access, *before, true); err != nil {
				t.Fatal(err)
			}
			flags := genericclioptions.NewConfigFlags(true)
			root := &cobra.Command{Use: "airo", SilenceErrors: true, SilenceUsage: true}
			flags.AddFlags(root.PersistentFlags())
			root.AddCommand(NewContextCommand(flags))
			var out, stderr bytes.Buffer
			root.SetOut(&out)
			root.SetErr(&stderr)
			root.SetArgs(append([]string{"--kubeconfig", access.GetExplicitFile()}, tt.args...))
			err = root.Execute()
			if tt.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantError) || out.Len() != 0 {
					t.Fatalf("output %q, error %v", out.String(), err)
				}
			} else if err != nil || out.String() != "Deleted context "+tt.deleted+"\n" || (stderr.Len() > 0) != tt.active {
				t.Fatalf("output %q, stderr %q, error %v", out.String(), stderr.String(), err)
			}
			want := before.DeepCopy()
			if tt.deleted != "" {
				delete(want.Contexts, tt.deleted)
			}
			if tt.active {
				want.CurrentContext = ""
			}
			after, err := access.GetStartingConfig()
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(after, want) {
				t.Fatal("unexpected change to kubeconfig entries or current context")
			}
		})
	}
}
