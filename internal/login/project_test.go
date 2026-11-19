package login

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/clientcmd"
)

// projectFixture is contextFixture with a cluster that lists the projects
// airo-web and airo-default, in that order, and a store for each context.
func projectFixture(t *testing.T) (clientcmd.ConfigAccess, func(string) (previousStore, error), projectLister) {
	t.Helper()
	access, _ := contextFixture(t)
	dir := t.TempDir()
	storeFor := func(context string) (previousStore, error) {
		return previousStore{path: filepath.Join(dir, base64.RawURLEncoding.EncodeToString([]byte(context)))}, nil
	}
	return access, storeFor, projectsOf("airo-web", "airo-default")
}

// projectsOf lists the names as Namespaces.
func projectsOf(names ...string) projectLister {
	return func(context.Context) (*unstructured.UnstructuredList, error) {
		list := &unstructured.UnstructuredList{}
		list.SetAPIVersion("v1")
		list.SetKind("NamespaceList")
		for _, name := range names {
			u := unstructured.Unstructured{}
			u.SetAPIVersion("v1")
			u.SetKind("Namespace")
			u.SetName(name)
			list.Items = append(list.Items, u)
		}
		return list, nil
	}
}

func projectOf(t *testing.T, access clientcmd.ConfigAccess, context string) string {
	t.Helper()
	cfg, err := access.GetStartingConfig()
	if err != nil {
		t.Fatal(err)
	}
	return cfg.Contexts[context].Namespace
}

func TestSwitchProjectAndBack(t *testing.T) {
	access, storeFor, list := projectFixture(t)
	for _, step := range []struct {
		target, want, out string
	}{
		{"airo-web", "airo-web", "Switched to project airo-web\n"},
		{"-", "airo-default", "Switched to project airo-default\n"},
		{"-", "airo-web", "Switched to project airo-web\n"},
		// Nothing changes, so "-" still leads back to airo-default.
		{"airo-web", "airo-web", "Already in project airo-web\n"},
		{"-", "airo-default", "Switched to project airo-default\n"},
	} {
		var out bytes.Buffer
		if err := switchProject(context.Background(), &out, access, "", storeFor, list, step.target, nil); err != nil {
			t.Fatalf("switch to %q: %v", step.target, err)
		}
		if got := projectOf(t, access, "airo"); got != step.want {
			t.Fatalf("after %q the project is %q, want %q", step.target, got, step.want)
		}
		if out.String() != step.out {
			t.Errorf("after %q the output is %q, want %q", step.target, out.String(), step.out)
		}
	}
	if got := currentContext(t, access); got != "airo" {
		t.Errorf("the context is %q, want it unchanged", got)
	}
}

// --context switches that context's project and remembers its own previous
// one, and the current context stays as it is.
func TestSwitchProjectOfAnotherContext(t *testing.T) {
	access, storeFor, _ := projectFixture(t)
	list := projectsOf("customer-default", "customer-web")
	var out bytes.Buffer
	if err := switchProject(context.Background(), &out, access, "customer", storeFor, list, "customer-web", nil); err != nil {
		t.Fatal(err)
	}
	if want := "Switched to project customer-web\n  context: customer\n"; out.String() != want {
		t.Errorf("the output is %q, want %q", out.String(), want)
	}
	if got := projectOf(t, access, "customer"); got != "customer-web" {
		t.Errorf("the project of customer is %q, want customer-web", got)
	}
	if got := projectOf(t, access, "airo"); got != "airo-default" {
		t.Errorf("the project of airo is %q, want it unchanged", got)
	}
	err := switchProject(context.Background(), &bytes.Buffer{}, access, "", storeFor, list, "-", nil)
	if err == nil || !strings.Contains(err.Error(), "remembers no previous project") {
		t.Errorf("airo project - in the context airo: error = %v, want none remembered", err)
	}
}

func TestSwitchProjectRefuses(t *testing.T) {
	forbidden := func(context.Context) (*unstructured.UnstructuredList, error) {
		return nil, errors.New("namespaces is forbidden")
	}
	for _, tc := range []struct {
		name, context, target string
		list                  projectLister
		want                  string
	}{
		{"an unknown name", "", "nope", nil, `you have no project "nope": your projects are airo-default, airo-web`},
		{"back with nothing remembered", "", "-", nil, "remembers no previous project"},
		{"no name and nobody to ask", "", "", nil, "name the project to switch to: airo-default, airo-web"},
		{"a context of another cluster", "kind", "airo-web", nil, `context "kind" does not sign in through the platform`},
		{"an unknown context", "nope", "airo-web", nil, `the kubeconfig has no context "nope"`},
		{"a cluster that lists no project", "", "airo-web", projectsOf(), "you have no project"},
		{"a cluster that refuses the list", "", "airo-web", forbidden, "namespaces is forbidden"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			access, storeFor, list := projectFixture(t)
			if tc.list != nil {
				list = tc.list
			}
			err := switchProject(context.Background(), &bytes.Buffer{}, access, tc.context, storeFor, list, tc.target, nil)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to contain %q", err, tc.want)
			}
			if got := projectOf(t, access, "airo"); got != "airo-default" {
				t.Errorf("the project is %q, want it unchanged", got)
			}
		})
	}
}

func TestSwitchProjectAsks(t *testing.T) {
	access, storeFor, list := projectFixture(t)
	ask := func(names []string, current int) (string, error) {
		if strings.Join(names, ",") != "airo-default,airo-web" || current != 0 {
			t.Errorf("asked with %v and current %d, want the projects with airo-default as the current one", names, current)
		}
		return "airo-web", nil
	}
	if err := switchProject(context.Background(), &bytes.Buffer{}, access, "", storeFor, list, "", ask); err != nil {
		t.Fatal(err)
	}
	if got := projectOf(t, access, "airo"); got != "airo-web" {
		t.Errorf("the project is %q, want airo-web", got)
	}

	// Leaving the picker changes nothing.
	var out bytes.Buffer
	none := func([]string, int) (string, error) { return "", nil }
	if err := switchProject(context.Background(), &out, access, "", storeFor, list, "", none); err != nil {
		t.Fatal(err)
	}
	if got := projectOf(t, access, "airo"); got != "airo-web" || out.Len() != 0 {
		t.Errorf("the project is %q and the output %q, want airo-web and none", got, out.String())
	}
}

var namespacesResource = schema.GroupResource{Resource: "namespaces"}

func TestCreateProject(t *testing.T) {
	var created string
	var out bytes.Buffer
	create := func(_ context.Context, name string) error { created = name; return nil }
	// The cluster lists the project on its second look.
	looks := 0
	list := func(ctx context.Context) (*unstructured.UnstructuredList, error) {
		looks++
		if looks < 2 {
			return projectsOf("airo-default")(ctx)
		}
		return projectsOf("airo-default", "airo-web")(ctx)
	}
	if err := createProject(context.Background(), &out, "airo-web", create, list, time.Minute); err != nil {
		t.Fatal(err)
	}
	if created != "airo-web" || looks != 2 {
		t.Errorf("created %q after %d looks, want airo-web and 2", created, looks)
	}
	if want := "Created project airo-web\n  work in it: airo project airo-web\n"; out.String() != want {
		t.Errorf("the output is %q, want %q", out.String(), want)
	}

	// A cluster that does not list it in time.
	out.Reset()
	if err := createProject(context.Background(), &out, "airo-web", create, projectsOf("airo-default"), 0); err != nil {
		t.Fatal(err)
	}
	if want := "Created project airo-web\n  the platform does not list it yet: in a moment, work in it with airo project airo-web\n"; out.String() != want {
		t.Errorf("the output is %q, want %q", out.String(), want)
	}

	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"a name that exists", apierrors.NewAlreadyExists(namespacesResource, "airo-web"), "named airo-web exists already"},
		// The platform's admission says why, such as a name without the
		// tenant's prefix or a full quota.
		{"a name the platform refuses", apierrors.NewForbidden(namespacesResource, "x", errors.New("must start with 'airo-'")), "must start with 'airo-'"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			create := func(context.Context, string) error { return tc.err }
			err := createProject(context.Background(), &bytes.Buffer{}, "airo-web", create, projectsOf(), 0)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

func TestSwitchAfterProjectDeletion(t *testing.T) {
	ctx := context.Background()
	defaultIs := func(project string) func(context.Context) (string, error) {
		return func(context.Context) (string, error) { return project, nil }
	}
	noDefault := defaultIs("")
	// The cluster lists airo-web, airo-default and airo-test; it still does
	// right after a delete, while the namespace terminates.
	list := projectsOf("airo-web", "airo-default", "airo-test")

	t.Run("another project", func(t *testing.T) {
		access, storeFor, _ := projectFixture(t)
		var out bytes.Buffer
		if err := switchAfterProjectDeletion(ctx, &out, access, "", "airo-web", storeFor, list, noDefault); err != nil {
			t.Fatal(err)
		}
		if out.Len() != 0 || projectOf(t, access, "airo") != "airo-default" {
			t.Errorf("said %q, project %q; want no switch from airo-default", out.String(), projectOf(t, access, "airo"))
		}
	})

	t.Run("the current one goes back to the previous", func(t *testing.T) {
		access, storeFor, _ := projectFixture(t)
		if err := switchProject(ctx, &bytes.Buffer{}, access, "", storeFor, list, "airo-test", nil); err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if err := switchAfterProjectDeletion(ctx, &out, access, "", "airo-test", storeFor, list, defaultIs("airo-web")); err != nil {
			t.Fatal(err)
		}
		if want := "Switched to project airo-default\n"; out.String() != want {
			t.Errorf("the output is %q, want %q", out.String(), want)
		}
		if got := projectOf(t, access, "airo"); got != "airo-default" {
			t.Errorf("the project is %q, want airo-default", got)
		}
		// "-" does not lead back to the deleted project.
		err := switchProject(ctx, &bytes.Buffer{}, access, "", storeFor, list, "-", nil)
		if err == nil || !strings.Contains(err.Error(), "remembers no previous project") {
			t.Errorf("airo project - after the delete: %v, want nothing remembered", err)
		}
	})

	t.Run("the current one without a previous goes to the default", func(t *testing.T) {
		access, storeFor, _ := projectFixture(t)
		var out bytes.Buffer
		if err := switchAfterProjectDeletion(ctx, &out, access, "", "airo-default", storeFor, list, defaultIs("airo-web")); err != nil {
			t.Fatal(err)
		}
		if want := "Switched to project airo-web\n"; out.String() != want {
			t.Errorf("the output is %q, want %q", out.String(), want)
		}
	})

	for name, fallback := range map[string]func(context.Context) (string, error){
		"no default":                          noDefault,
		"the default is the deleted one":      defaultIs("airo-default"),
		"a default the cluster does not list": defaultIs("airo-gone"),
		"the identity cannot be read": func(context.Context) (string, error) {
			return "", errors.New("unauthorized")
		},
	} {
		t.Run("the current one, and "+name, func(t *testing.T) {
			access, storeFor, _ := projectFixture(t)
			var out bytes.Buffer
			if err := switchAfterProjectDeletion(ctx, &out, access, "", "airo-default", storeFor, list, fallback); err != nil {
				t.Fatal(err)
			}
			if want := "  your commands still work in it: choose another with airo project\n"; out.String() != want {
				t.Errorf("the output is %q, want %q", out.String(), want)
			}
			if got := projectOf(t, access, "airo"); got != "airo-default" {
				t.Errorf("the project is %q, want it unchanged", got)
			}
		})
	}

	t.Run("the project of another context", func(t *testing.T) {
		access, storeFor, _ := projectFixture(t)
		var out bytes.Buffer
		customers := projectsOf("customer-default", "customer-web")
		if err := switchAfterProjectDeletion(ctx, &out, access, "customer", "customer-default", storeFor, customers, defaultIs("customer-web")); err != nil {
			t.Fatal(err)
		}
		if got := projectOf(t, access, "customer"); got != "customer-web" {
			t.Errorf("the project of customer is %q, want customer-web", got)
		}
		if got := projectOf(t, access, "airo"); got != "airo-default" {
			t.Errorf("the project of airo is %q, want it unchanged", got)
		}
	})

}
