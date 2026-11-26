package dryrun

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// command runs a command with --dry-run and -o and the args, and returns
// what its write did to the client c, with stdout and stderr.
func command(t *testing.T, c client.Client, write func(client.Client) error, args ...string) (string, string, error) {
	t.Helper()
	cmd := &cobra.Command{Use: "test", SilenceUsage: true, SilenceErrors: true}
	o := AddFlags(cmd)
	cmd.RunE = func(*cobra.Command, []string) error {
		w := o.Client(c)
		if err := write(w); err != nil {
			return err
		}
		if done, err := Finish(w); done || err != nil {
			return err
		}
		return nil
	}
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), errOut.String(), err
}

func role(name string) *rbacv1.Role {
	return &rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Namespace: "team", Name: name}}
}

func binding(name string) *rbacv1.RoleBinding {
	return &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Namespace: "team", Name: name},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: name},
	}
}

func createBoth(c client.Client) error {
	if err := c.Create(context.Background(), role("relay")); err != nil {
		return err
	}
	return c.Create(context.Background(), binding("relay"))
}

func exists(c client.Client, obj client.Object) bool {
	return c.Get(context.Background(), client.ObjectKeyFromObject(obj), obj) == nil
}

func TestDryRunKeepsNothing(t *testing.T) {
	for _, mode := range []string{"client", "server"} {
		t.Run(mode, func(t *testing.T) {
			c := fake.NewClientBuilder().Build()
			out, _, err := command(t, c, createBoth, "--dry-run="+mode)
			if err != nil {
				t.Fatal(err)
			}
			want := "role.rbac.authorization.k8s.io/relay created (" + strings.TrimPrefix(mode+" ", "client ") + "dry run)\n" +
				"rolebinding.rbac.authorization.k8s.io/relay created (" + strings.TrimPrefix(mode+" ", "client ") + "dry run)\n"
			if out != want {
				t.Errorf("output =\n%s\nwant\n%s", out, want)
			}
			if exists(c, role("relay")) || exists(c, binding("relay")) {
				t.Error("a dry run kept a resource")
			}
		})
	}

	// Without --dry-run the client is the command's own.
	c := fake.NewClientBuilder().Build()
	if out, _, err := command(t, c, createBoth); err != nil || out != "" || !exists(c, binding("relay")) {
		t.Errorf("a real run: output %q, error %v, or nothing created", out, err)
	}
}

// -o prints the resources with their kinds, one as itself and several as a
// List, in the order the command wrote them.
func TestDryRunPrintsTheResources(t *testing.T) {
	c := fake.NewClientBuilder().Build()
	out, _, err := command(t, c, createBoth, "--dry-run=client", "-o", "yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"apiVersion: v1\nitems:\n- apiVersion: rbac.authorization.k8s.io/v1\n  kind: Role\n", "  kind: RoleBinding\n", "kind: List\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Index(out, "kind: Role\n") > strings.Index(out, "kind: RoleBinding\n") {
		t.Errorf("the Role is not first:\n%s", out)
	}

	out, _, err = command(t, c, func(c client.Client) error {
		return c.Create(context.Background(), role("one"))
	}, "--dry-run=client", "-o", "json")
	if err != nil || !strings.HasPrefix(out, "{\n    \"apiVersion\": \"rbac.authorization.k8s.io/v1\",\n    \"kind\": \"Role\",") {
		t.Errorf("one resource as json = %s, %v", out, err)
	}
}

func TestDryRunFlagErrors(t *testing.T) {
	c := fake.NewClientBuilder().Build()
	for _, tt := range []struct {
		args []string
		want string
	}{
		{[]string{"-o", "yaml"}, "shows what a dry run would do"},
		{[]string{"--dry-run=server", "-o", "wide"}, "-o wide is not supported"},
		{[]string{"--dry-run=maybe"}, "Invalid dry-run value"},
	} {
		if _, _, err := command(t, c, createBoth, tt.args...); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%v: error %v, want %q", tt.args, err, tt.want)
		}
		if exists(c, role("relay")) {
			t.Fatalf("%v created a resource", tt.args)
		}
	}
}

// The API server cannot check a resource that refers to one this dry run
// made, as a RoleBinding its Role: the dry run says so and goes on.
func TestServerDryRunOfAResourceThatRefersToAnother(t *testing.T) {
	c := fake.NewClientBuilder().WithInterceptorFuncs(interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if _, ok := obj.(*rbacv1.RoleBinding); ok {
				return apierrors.NewNotFound(schema.GroupResource{Group: rbacv1.GroupName, Resource: "roles"}, obj.GetName())
			}
			return c.Create(ctx, obj, opts...)
		},
	}).Build()
	out, errOut, err := command(t, c, createBoth, "--dry-run=server")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "rolebinding.rbac.authorization.k8s.io/relay created (server dry run)") {
		t.Errorf("output = %s", out)
	}
	if !strings.Contains(errOut, "could not check rolebinding.rbac.authorization.k8s.io/relay") {
		t.Errorf("notes = %q", errOut)
	}

	// The first write's NotFound is the cluster's answer, not this dry run's.
	_, _, err = command(t, c, func(c client.Client) error {
		return c.Create(context.Background(), binding("alone"))
	}, "--dry-run=server")
	if !apierrors.IsNotFound(err) {
		t.Errorf("error = %v, want the cluster's NotFound", err)
	}
}
