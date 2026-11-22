package agent

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/airohq/kagent-remote-harness/runner/supervisor"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/airohq/airo-cli/internal/tunnel"
	"github.com/airohq/airo-cli/internal/ui"
)

func newServeCommand(flags *genericclioptions.ConfigFlags) *cobra.Command {
	var name string
	cmd := &cobra.Command{
		Use:     "serve [--name NAME]",
		Short:   "Run a temporary local Agent and delete it on exit",
		Long:    "Run a temporary Agent with a fresh local workspace. Ctrl-C deletes the Agent and its workspace. The container sandbox prompts for subscription sign-in on the first run.",
		Example: "  airo agent serve\n  airo agent serve --name demo",
		Args: func(cmd *cobra.Command, args []string) error {
			if cmd.Flags().Changed("name") && name == "" {
				return errors.New("--name needs a temporary Agent name")
			}
			return cobra.NoArgs(cmd, args)
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "temporary Agent name (default: generate a name)")
	return localRunnerCommand(cmd, flags, func(ctx context.Context, c client.Client, ns string, args []string, serve serveAgent) error {
		return serveTemporary(ctx, c, ns, name, cmd.ErrOrStderr(), serve)
	})
}

func newConnectCommand(flags *genericclioptions.ConfigFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "connect NAME",
		Short:   "Connect this machine to an existing Agent to run its turns",
		Long:    "Connect this machine to an Agent with a private relay, such as one created with airo agent create --local. Ctrl-C disconnects this machine. Run the command again to reconnect with the same workspace on this engine. The container sandbox prompts for subscription sign-in when needed.",
		Example: "  airo agent connect reviewer",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 || args[0] == "" {
				return errors.New("connect requires one existing Agent name")
			}
			return nil
		},
	}
	return localRunnerCommand(cmd, flags, func(ctx context.Context, c client.Client, ns string, names []string, serve serveAgent) error {
		name := names[0]
		t, err := agentTunnel(ctx, c, ns, name)
		if err != nil {
			return err
		}
		return serve(ctx, fmt.Sprintf("Serving agent %s/%s through tunnel %s. Waiting for turns.", ns, name, t.Name), t, nil)
	})
}

type serveAgent func(ctx context.Context, message string, target tunnel.RunnerTunnel, workspace *temporaryWorkspace) error

func localRunnerCommand(cmd *cobra.Command, flags *genericclioptions.ConfigFlags, start func(context.Context, client.Client, string, []string, serveAgent) error) *cobra.Command {
	var program string
	var defaults supervisor.Options
	settings := flag.NewFlagSet("runner", flag.ContinueOnError)
	defaults.BindFlags(settings)
	validate := cmd.Args
	cmd.Args = func(cmd *cobra.Command, args []string) error {
		if dash := cmd.ArgsLenAtDash(); dash >= 0 {
			args = args[:dash]
		}
		return validate(cmd, args)
	}
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		var helperArgs []string
		if dash := cmd.ArgsLenAtDash(); dash >= 0 {
			helperArgs = append(helperArgs, args[dash:]...)
			args = args[:dash]
		}
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
		// Keep the previous arguments after --. Explicit airo flags take
		// precedence and are also forwarded to a custom executable.
		cmd.Flags().Visit(func(f *pflag.Flag) {
			if settings.Lookup(f.Name) != nil {
				name := f.Name
				if program != "" && name == "sandbox" {
					// Older standalone supervisors use the original flag name.
					name = "runner"
				}
				helperArgs = append(helperArgs, "--"+name+"="+f.Value.String())
			}
		})
		out := cmd.ErrOrStderr()
		if program == "" {
			settings.SetOutput(out)
			if err := settings.Parse(helperArgs); err != nil {
				return err
			}
			if settings.NArg() != 0 {
				return fmt.Errorf("unexpected runner argument %q", settings.Arg(0))
			}
			if defaults.ClaudePermissionMode == "" && defaults.CodexSandbox == "" {
				switch defaults.Harness {
				case "", "codex":
					defaults.Harness, defaults.CodexSandbox = "codex", "read-only"
				case "claude":
					defaults.ClaudePermissionMode = "plan"
				}
			}
			if _, err := defaults.Adapter(); err != nil {
				return err
			}
			var terminal *os.File
			if ui.Interactive(cmd.InOrStdin(), out) {
				terminal, _ = cmd.InOrStdin().(*os.File)
			}
			if err := defaults.PrepareCredentials(ctx, "airo", terminal, out); err != nil {
				return fmt.Errorf("prepare %s credentials: %w", defaults.Harness, err)
			}
		}
		return start(ctx, c, ns, args, func(ctx context.Context, message string, t tunnel.RunnerTunnel, workspace *temporaryWorkspace) error {
			return serveRunner(ctx, t.Namespace, t.UID, workspace, defaults, program, helperArgs, out, func(ctx context.Context, h http.Handler) error {
				fmt.Fprintln(out, message)
				return t.ServeHTTP(ctx, out, cfg, h)
			})
		})
	}
	cmd.Flags().BoolVarP(&defaults.Verbose, "verbose", "v", false, settings.Lookup("verbose").Usage)
	cmd.Flags().AddGoFlagSet(settings)
	cmd.Flags().Lookup("harness").Usage = "coding agent: codex (default, read-only) or claude (plan mode by default)"
	_ = cmd.Flags().MarkHidden("runner")
	cmd.Flags().StringVar(&program, "runner-command", "", "use an external supervisor executable (default: embedded runner)")
	return cmd
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

func serveRunner(ctx context.Context, ns, tunnelUID string, workspace *temporaryWorkspace, opts supervisor.Options, program string, args []string, out io.Writer, serve serveTunnel) (err error) {
	var secret [32]byte
	rand.Read(secret[:])
	token := hex.EncodeToString(secret[:])
	identity := sha256.Sum256([]byte(tunnelUID))
	name := fmt.Sprintf("airo-runner-%x", identity[:12])
	wrap := func(handler http.Handler) http.Handler {
		if workspace != nil {
			handler = workspace.onTurn(handler)
		}
		return runnerRequests(ns, token, handler)
	}
	if workspace != nil {
		name = workspace.name
		defer func() { err = errors.Join(err, workspace.close()) }()
	}
	if program != "" {
		return withExternalSupervisor(ctx, name, token, program, args, out, wrap, serve)
	}

	host, err := supervisor.New(name, token, opts, slog.New(slog.NewTextHandler(out, nil)))
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, host.Close()) }()
	return serve(ctx, wrap(host))
}

func withExternalSupervisor(ctx context.Context, name, token, program string, args []string, out io.Writer, wrap func(http.Handler) http.Handler, serve serveTunnel) error {
	path, err := exec.LookPath(program)
	if err != nil {
		return fmt.Errorf("find runner helper %q (install a helper with the supervise command): %w", program, err)
	}
	dir, err := os.MkdirTemp("", "airo-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	socket := filepath.Join(dir, "r")
	args = append(append([]string{"supervise"}, args...), "--socket", socket, "--name", name)
	helper := exec.Command(path, args...)
	helper.Env = append(os.Environ(), "REMOTE_HARNESS_TOKEN="+token, "REMOTE_HARNESS_PARENT_PIPE=1")
	helper.Stdout = out
	helper.Stderr = out
	pipe, err := helper.StdinPipe()
	if err != nil {
		return err
	}
	if err := helper.Start(); err != nil {
		pipe.Close()
		return err
	}
	ctx, cancel := context.WithCancelCause(ctx)
	done := make(chan struct{})
	go func() {
		err := helper.Wait()
		if err == nil {
			err = errors.New("supervisor exited")
		}
		cancel(fmt.Errorf("remote-harness: %w", err))
		close(done)
	}()
	defer func() {
		cancel(nil)
		pipe.Close()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			_ = helper.Process.Kill()
			<-done
		}
	}()
	tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	defer tr.CloseIdleConnections()
	if err := waitSupervisor(ctx, tr, token); err != nil {
		return err
	}
	proxy := &httputil.ReverseProxy{Transport: tr, FlushInterval: -1, Rewrite: func(p *httputil.ProxyRequest) {
		p.Out.URL.Scheme = "http"
		p.Out.URL.Host = "runner"
		p.Out.Host = "runner"
	}}
	err = serve(ctx, wrap(proxy))
	if cause := context.Cause(ctx); cause != nil && !errors.Is(cause, context.Canceled) {
		return cause
	}
	return err
}

func waitSupervisor(ctx context.Context, tr http.RoundTripper, token string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	for {
		req, _ := http.NewRequestWithContext(ctx, "GET", "http://runner/v1/info", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := tr.RoundTrip(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK || resp.Header.Get("Remote-Harness-Protocol") != "1" {
				return errors.New("remote-harness must serve protocol v1")
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("start remote-harness: %w", context.Cause(ctx))
		case <-time.After(25 * time.Millisecond):
		}
	}
}
