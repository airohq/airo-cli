// Package tunnel implements airo tunnel: serve a Tunnel with a local port.
package tunnel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"os/user"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/airohq/airo-cli/internal/expiry"
	"github.com/airohq/airo-cli/internal/generic"
	"github.com/airohq/airo-cli/internal/wizard"
)

// The kind behind airo tunnel, named only here: an Airo-owned kind is planned
// to replace it. The CLI reads it as unstructured and uses only the fields it
// needs, so it depends on the cluster's API and on no component's Go types.
var tunnelGVK = schema.GroupVersionKind{Group: "tunnels.airohq.dev", Version: "v1alpha1", Kind: "Tunnel"}

const callerAuthNone = "none"

// NewCommand returns the tunnel command.
func NewCommand(flags *genericclioptions.ConfigFlags) *cobra.Command {
	var (
		name   string
		public bool
		noAuth bool
	)
	cmd := &cobra.Command{
		Use:     "tunnel PORT|HOST:PORT",
		Aliases: []string{"tunnels"},
		Short:   "Give a local port a URL in the cluster, or on the internet with --public",
		Long: `Tunnel serves a Tunnel in your project with a local port until you stop it.

It creates the Tunnel when it does not exist, and deletes it again on exit.
If the command is killed, the cluster deletes the Tunnel within about 15
minutes. A Tunnel made with airo tunnel create is used as it is and stays.`,
		Example: `  airo tunnel 3000                       # reachable from the cluster, with a token
  airo tunnel 3000 --public --no-auth    # a URL on the internet for anyone, like ngrok
  airo tunnel 3000 --name my-app         # a name you choose: my-app.<project>.<domain>`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			to, port, err := target(args[0])
			if err != nil {
				return err
			}
			if name == "" {
				name = defaultName(port)
			}
			// A flag left out accepts whatever an existing Tunnel has.
			want := spec{}
			if cmd.Flags().Changed("public") {
				want.public = &public
			}
			if cmd.Flags().Changed("no-auth") {
				want.noAuth = &noAuth
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return run(ctx, cmd.OutOrStdout(), flags, name, to, want)
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "name of the Tunnel (default: <your username>-<port>)")
	cmd.Flags().BoolVar(&public, "public", false, "make the tunnel reachable from the internet too")
	cmd.Flags().BoolVar(&noAuth, "no-auth", false, "let callers in without a token")
	cmd.AddCommand(newCreateCommand(flags))
	cmd.AddCommand(generic.Commands(flags, cmd.Name())...)
	return cmd
}

// newCreateCommand returns tunnel create: a Tunnel that stays, with a name
// and a URL that other people and manifests can rely on.
func newCreateCommand(flags *genericclioptions.ConfigFlags) *cobra.Command {
	var public, noAuth bool
	cmd := &cobra.Command{
		Use:   "create [NAME]",
		Short: "Create a Tunnel that stays until you delete it",
		Long: `Create makes a Tunnel and leaves it in the project. Serve it with
airo tunnel PORT --name NAME, which leaves it in place on exit.
Omit NAME on a terminal to choose the name, visibility and authentication.`,
		Example: `  airo tunnel create my-app --public
  airo tunnel 3000 --name my-app`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ns, _, err := flags.ToRawKubeConfigLoader().Namespace()
			if err != nil {
				return err
			}
			w := wizard.New(cmd, "tunnel", ns)
			name, err := w.Name(args)
			if err != nil {
				return err
			}
			if len(args) == 0 {
				if !cmd.Flags().Changed("public") {
					public, err = w.Confirm("Make it reachable from the internet?", false)
					if err != nil {
						return err
					}
				}
				if !cmd.Flags().Changed("no-auth") {
					auth, err := w.Confirm("Require caller authentication?", true)
					if err != nil {
						return err
					}
					noAuth = !auth
				}
			}
			cfg, err := flags.ToRESTConfig()
			if err != nil {
				return err
			}
			c, err := client.New(cfg, client.Options{})
			if err != nil {
				return err
			}
			t := build(ns, name, spec{public: &public, noAuth: &noAuth})
			if err := c.Create(cmd.Context(), t); err != nil {
				return err
			}
			if err := waitReady(cmd.Context(), c, t); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Created tunnel %s/%s\n", ns, name)
			printURLs(cmd.OutOrStdout(), t)
			return nil
		},
	}
	cmd.Flags().BoolVar(&public, "public", false, "make the tunnel reachable from the internet too")
	cmd.Flags().BoolVar(&noAuth, "no-auth", false, "let callers in without a token")
	return cmd
}

// build returns a new Tunnel with only the fields the flags turn on, so the
// API's defaults decide the rest.
func build(ns, name string, want spec) *unstructured.Unstructured {
	t := &unstructured.Unstructured{}
	t.SetGroupVersionKind(tunnelGVK)
	t.SetNamespace(ns)
	t.SetName(name)
	s := map[string]any{}
	if want.public != nil && *want.public {
		s["public"] = true
	}
	if want.noAuth != nil && *want.noAuth {
		s["callerAuth"] = callerAuthNone
	}
	t.Object["spec"] = s
	return t
}

// printURLs prints where callers reach the Tunnel.
func printURLs(out io.Writer, t *unstructured.Unstructured) {
	if u := str(t, "status", "publicCallerURL"); u != "" {
		fmt.Fprintf(out, "  public:  %s\n", u)
	}
	fmt.Fprintf(out, "  cluster: %s\n", str(t, "status", "callerURL"))
	if str(t, "spec", "callerAuth") == callerAuthNone {
		fmt.Fprintln(out, "  callers: anyone who reaches the URL")
	} else {
		fmt.Fprintln(out, "  callers: need a Kubernetes token and the call verb on the Tunnel")
	}
}

// spec is what the flags ask of the Tunnel. Nil means the flag was not given.
type spec struct {
	public *bool
	noAuth *bool
}

func run(ctx context.Context, out io.Writer, flags *genericclioptions.ConfigFlags, name, to string, want spec) error {
	cfg, err := flags.ToRESTConfig()
	if err != nil {
		return err
	}
	ns, _, err := flags.ToRawKubeConfigLoader().Namespace()
	if err != nil {
		return err
	}
	c, err := client.New(cfg, client.Options{})
	if err != nil {
		return err
	}

	t, created, err := ensure(ctx, c, ns, name, want)
	if err != nil {
		return err
	}
	// An existing Tunnel with an expiry is one a killed airo tunnel left
	// behind. This command serves it as its own: without a refresh the
	// cluster would delete it while it is in use.
	uid := t.GetUID()
	temporary := created || t.GetAnnotations()[expiry.Annotation] != ""
	if temporary && !created {
		if err := expiry.Extend(ctx, c, t); err != nil {
			return fmt.Errorf("extend tunnel %s/%s: %w", ns, name, err)
		}
	}
	if temporary {
		defer func() {
			// ctx is done by now, so the delete gets its own.
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := c.Delete(ctx, t, client.Preconditions{UID: &uid}); err != nil && !apierrors.IsNotFound(err) {
				fmt.Fprintf(out, "Could not delete tunnel %s/%s: %v\n", ns, name, err)
				return
			}
			fmt.Fprintf(out, "Deleted tunnel %s/%s\n", ns, name)
		}()
	}
	if err := waitReady(ctx, c, t); err != nil {
		return err
	}

	if t.GetUID() != uid {
		return fmt.Errorf("tunnel %s/%s was replaced", ns, name)
	}

	fmt.Fprintf(out, "Tunnel %s/%s forwards to %s\n", ns, name, to)
	printURLs(out, t)

	if temporary {
		// A Tunnel that is gone ends the command, with the reason as its error.
		var cancel context.CancelCauseFunc
		ctx, cancel = context.WithCancelCause(ctx)
		defer cancel(nil)
		go func() { cancel(expiry.KeepAlive(ctx, out, c, t)) }()
	}
	if err := connect(ctx, out, cfg, ns, name, str(t, "status", "clientURL"), to); err != nil {
		return err
	}
	if cause := context.Cause(ctx); cause != nil && !errors.Is(cause, context.Canceled) {
		return cause
	}
	return nil
}

// ensure returns the Tunnel to serve and whether it created it. It never
// changes an existing Tunnel: that one may be someone else's, and --public or
// --no-auth on it would expose their service.
func ensure(ctx context.Context, c client.Client, ns, name string, want spec) (*unstructured.Unstructured, bool, error) {
	t := &unstructured.Unstructured{}
	t.SetGroupVersionKind(tunnelGVK)
	err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, t)
	if apierrors.IsNotFound(err) {
		t = build(ns, name, want)
		// From the moment it exists, so a command killed right after this
		// call leaves nothing behind for long.
		t.SetAnnotations(map[string]string{expiry.Annotation: expiry.Deadline()})
		if err := c.Create(ctx, t); err != nil {
			return nil, false, fmt.Errorf("create tunnel %s/%s: %w", ns, name, err)
		}
		return t, true, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("tunnel %s/%s: %w", ns, name, err)
	}
	if t.GetLabels()[TemporaryAgentLabel] == "true" {
		return nil, false, fmt.Errorf("tunnel %s/%s is serving a temporary agent: choose another tunnel name", ns, name)
	}
	isPublic, _, _ := unstructured.NestedBool(t.Object, "spec", "public")
	isNoAuth := str(t, "spec", "callerAuth") == callerAuthNone
	if (want.public != nil && *want.public != isPublic) || (want.noAuth != nil && *want.noAuth != isNoAuth) {
		return nil, false, fmt.Errorf("tunnel %s/%s exists with --public=%t and --no-auth=%t: "+
			"leave the flags out to use it as it is, pick another --name, or delete it with airo tunnel delete %s",
			ns, name, isPublic, isNoAuth, name)
	}
	return t, false, nil
}

// waitReady reads the Tunnel into t until the operator has given it its URLs.
func waitReady(ctx context.Context, c client.Client, t *unstructured.Unstructured) error {
	var (
		reason string
		ready  bool
	)
	err := wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, 30*time.Second, true, func(ctx context.Context) (bool, error) {
		if err := c.Get(ctx, client.ObjectKeyFromObject(t), t); err != nil {
			return false, err
		}
		conditions, _, _ := unstructured.NestedSlice(t.Object, "status", "conditions")
		for _, item := range conditions {
			cond, _ := item.(map[string]any)
			if cond["type"] != "Ready" {
				continue
			}
			reason, _ = cond["message"].(string)
			ready = cond["status"] == "True"
			return ready && str(t, "status", "clientURL") != "", nil
		}
		return false, nil
	})
	if ctx.Err() != nil {
		return nil
	}
	if errors.Is(err, context.DeadlineExceeded) {
		// A cluster whose Tunnels differ from what this version knows.
		if ready {
			return fmt.Errorf("tunnel %s/%s is ready but has no status.clientURL: this airo may be too old for the cluster, update it",
				t.GetNamespace(), t.GetName())
		}
		if reason == "" {
			reason = "the tunnel operator has not answered; are tunnels turned on in this project?"
		}
		return fmt.Errorf("tunnel %s/%s is not ready: %s", t.GetNamespace(), t.GetName(), reason)
	}
	return err
}

func str(t *unstructured.Unstructured, fields ...string) string {
	s, _, _ := unstructured.NestedString(t.Object, fields...)
	return s
}

// target turns the argument into the address to forward to and its port.
func target(arg string) (to, port string, err error) {
	if _, err := strconv.ParseUint(arg, 10, 16); err == nil {
		return "localhost:" + arg, arg, nil
	}
	host, port, err := net.SplitHostPort(arg)
	if err != nil || host == "" || port == "" {
		return "", "", fmt.Errorf("%q is not a port or host:port, such as 3000 or localhost:3000", arg)
	}
	return arg, port, nil
}

// defaultName is <username>-<port>: the same URL on every run, and not a name
// a teammate in the same project gets by default. Two clients on one Tunnel
// share its requests.
func defaultName(port string) string {
	name := "tunnel"
	if u, err := user.Current(); err == nil {
		name = u.Username
	}
	return dnsLabel(name+"-"+port, "tunnel-"+port)
}

var notLabelChars = regexp.MustCompile(`[^a-z0-9]+`)

// dnsLabel makes s a DNS label, which the names of Tunnels and machines are.
func dnsLabel(s, fallback string) string {
	s = notLabelChars.ReplaceAllString(strings.ToLower(s), "-")
	if len(s) > 63 {
		s = s[:63]
	}
	if s = strings.Trim(s, "-"); s == "" {
		return fallback
	}
	return s
}
