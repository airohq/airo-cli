package agent

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/airohq/kagent-remote-harness/runner/supervisor"
	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/airohq/airo-cli/internal/dryrun"
	"github.com/airohq/airo-cli/internal/generic"
	"github.com/airohq/airo-cli/internal/tunnel"
	"github.com/airohq/airo-cli/internal/ui"
)

func newServeCommand(flags *genericclioptions.ConfigFlags) *cobra.Command {
	var name string
	cmd := &cobra.Command{
		Use:     "serve [--name NAME] [flags] [-- HARNESS_ARGS...]",
		Short:   "Run a temporary local Agent and delete it on exit",
		Long:    "Run a temporary Agent with a fresh workspace or share a host directory with --workspace. Ctrl-C deletes the Agent and its temporary volume. Files in a shared directory stay on the host. The container sandbox prompts for subscription sign-in on the first run.",
		Example: "  airo agent serve\n  airo agent serve --name demo\n  airo agent serve --workspace .",
		Args: func(cmd *cobra.Command, args []string) error {
			if cmd.Flags().Changed("name") {
				if name == "" {
					return errors.New("--name needs a temporary Agent name")
				}
				if problems := validation.IsDNS1123Label(name); len(problems) != 0 {
					return fmt.Errorf("a temporary Agent shares its Tunnel name, which must be a DNS label: %s", strings.Join(problems, ", "))
				}
			}
			return cobra.NoArgs(cmd, args)
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "temporary Agent name (default: generate a name)")
	dry := dryrun.AddFlags(cmd)
	return localRunnerCommand(cmd, flags, func(ctx context.Context, c client.Client, ns, scope string, args []string, serve serveAgent) error {
		return serveTemporary(ctx, dry.Client(c), ns, name, scope, cmd.ErrOrStderr(), serve)
	})
}

func newConnectCommand(flags *genericclioptions.ConfigFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "connect NAME [flags] [-- HARNESS_ARGS...]",
		Short:   "Connect this machine to an existing Agent to run its turns",
		Long:    "Connect this machine to an Agent with a private relay, such as one created with airo agent create --local. Ctrl-C disconnects this machine. Run the command again to reconnect with the same state on this engine. To share a host directory, pass --workspace on each connection. The container sandbox prompts for subscription sign-in when needed.",
		Example: "  airo agent connect reviewer",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 || args[0] == "" {
				return errors.New("connect requires one existing Agent name")
			}
			return nil
		},
		ValidArgsFunction: generic.FirstArg(CompleteAgents(flags)),
	}
	return localRunnerCommand(cmd, flags, func(ctx context.Context, c client.Client, ns, _ string, names []string, serve serveAgent) error {
		name := names[0]
		t, err := agentTunnel(ctx, c, ns, name)
		if err != nil {
			return err
		}
		return serve(ctx, fmt.Sprintf("Serving agent %s/%s through tunnel %s. Waiting for turns.", ns, name, t.Name), t, nil)
	})
}

type serveAgent func(ctx context.Context, message string, target tunnel.RunnerTunnel, workspace *temporaryWorkspace) error

func localRunnerCommand(cmd *cobra.Command, flags *genericclioptions.ConfigFlags, start func(ctx context.Context, c client.Client, ns, scope string, args []string, serve serveAgent) error) *cobra.Command {
	var defaults supervisor.Options
	validate := cmd.Args
	bindLocalFlags(cmd, &defaults)
	// Cobra normally handles --help before argument validation. Parse here so
	// help and execution both use the final sandbox and harness selection.
	cmd.DisableFlagParsing = true
	var positionals []string
	cmd.Args = func(cmd *cobra.Command, args []string) error {
		var err error
		positionals, err = parseLocalFlags(cmd, args, &defaults)
		if err != nil {
			return err
		}
		return validate(cmd, positionals)
	}
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
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
		out := cmd.ErrOrStderr()
		var terminal *os.File
		if ui.Interactive(cmd.InOrStdin(), out) {
			terminal, _ = cmd.InOrStdin().(*os.File)
		}
		// A dry run serves nothing, so it needs no credentials.
		if !dryrun.Requested(cmd) {
			if err := defaults.PrepareCredentials(ctx, "airo", terminal, out); err != nil {
				return fmt.Errorf("prepare %s credentials: %w", defaults.Harness, err)
			}
		}
		return start(ctx, c, ns, workspaceScope(cfg.Host, ns), positionals, func(ctx context.Context, message string, t tunnel.RunnerTunnel, workspace *temporaryWorkspace) error {
			return serveRunner(ctx, t.Namespace, t.UID, workspace, defaults, out, func(ctx context.Context, h http.Handler) error {
				fmt.Fprintln(out, message)
				return t.ServeHTTP(ctx, out, cfg, h)
			})
		})
	}
	return cmd
}

// workspaceScope names one project on one cluster, so a temporary workspace
// left by a killed process is checked only against the cluster that knows it.
func workspaceScope(host, ns string) string {
	sum := sha256.Sum256([]byte(host + "\x00" + ns))
	return hex.EncodeToString(sum[:8])
}

func agentTunnel(ctx context.Context, c client.Reader, ns, name string) (tunnel.RunnerTunnel, error) {
	var zero tunnel.RunnerTunnel
	a := &unstructured.Unstructured{}
	a.SetGroupVersionKind(agentGVK)
	if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, a); err != nil {
		return zero, err
	}
	return resolveAgentTunnel(ctx, c, a)
}

func resolveAgentTunnel(ctx context.Context, c client.Reader, a *unstructured.Unstructured) (tunnel.RunnerTunnel, error) {
	var zero tunnel.RunnerTunnel
	name, ns := a.GetName(), a.GetNamespace()
	ref := detailString(a.Object, "spec", "harnessRef", "name")
	if ref == "" {
		return zero, fmt.Errorf("agent %s has no spec.harnessRef.name", name)
	}
	h := &unstructured.Unstructured{}
	h.SetGroupVersionKind(harnessGVK)
	if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: ref}, h); err != nil {
		return zero, err
	}
	local := a.GetLabels()[localAgentLabel] == "true"
	if local {
		if ref != name || !metav1.IsControlledBy(h, a) {
			return zero, fmt.Errorf("Harness %s/%s is not the relay owned by Agent %s", ns, ref, name)
		}
		policy := &unstructured.Unstructured{}
		policy.SetGroupVersionKind(egressPolicyGVK)
		if err := c.Get(ctx, client.ObjectKeyFromObject(h), policy); err != nil {
			return zero, err
		}
		if !metav1.IsControlledBy(policy, a) {
			return zero, fmt.Errorf("EgressPolicy %s/%s is not owned by Agent %s", ns, ref, name)
		}
	}
	origin, err := remoteOrigin(h)
	if err != nil {
		return zero, err
	}
	if local {
		return tunnel.OwnedRunnerTunnel(ctx, c, ref, origin, a)
	}
	return tunnel.FindRunnerTunnel(ctx, c, ns, origin)
}

func remoteOrigin(h *unstructured.Unstructured) (string, error) {
	env, _, err := unstructured.NestedSlice(h.Object, "spec", "env")
	if err != nil {
		return "", err
	}
	for _, entry := range env {
		item, _ := entry.(map[string]any)
		if item["name"] == "REMOTE_HARNESS_URL" {
			if origin := detailString(item, "value"); origin != "" {
				return origin, nil
			}
		}
	}
	return "", fmt.Errorf("harness %s needs REMOTE_HARNESS_URL set to its Tunnel's status.callerURL in spec.env", h.GetName())
}

// Remote harness protocol v1. Only requests authenticated by the Tunnel
// gateway as project actors can be translated to the local runner identity.
func runnerRequests(ns, token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		session, ok := strings.CutPrefix(r.Header.Get("Tunnel-Caller"), "substrate:actor/"+ns+"/")
		if !ok || session == "" || strings.Contains(session, "/") {
			http.Error(w, "a verified project actor is required", http.StatusForbidden)
			return
		}
		if !(r.Method == "POST" && r.URL.Path == "/v1/turn" || r.Method == "GET" && r.URL.Path == "/v1/info") {
			http.NotFound(w, r)
			return
		}
		out := r.Clone(r.Context())
		out.Header = make(http.Header)
		out.Header.Set("Content-Type", "application/json")
		out.Header.Set("Authorization", "Bearer "+token)
		next.ServeHTTP(w, out)
	})
}

type serveTunnel func(context.Context, http.Handler) error

func serveRunner(ctx context.Context, ns, tunnelUID string, workspace *temporaryWorkspace, opts supervisor.Options, out io.Writer, serve serveTunnel) (err error) {
	var secret [32]byte
	rand.Read(secret[:])
	token := hex.EncodeToString(secret[:])
	identity := sha256.Sum256([]byte(tunnelUID))
	name := fmt.Sprintf("airo-runner-%x", identity[:12])
	if workspace != nil {
		name = workspace.name
		defer func() { err = errors.Join(err, workspace.close()) }()
	}

	host, err := supervisor.New(name, token, opts, slog.New(slog.NewTextHandler(out, nil)))
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, host.Close()) }()
	var handler http.Handler = host
	if workspace != nil {
		handler = workspace.onTurn(handler)
	}
	return serve(ctx, runnerRequests(ns, token, handler))
}
