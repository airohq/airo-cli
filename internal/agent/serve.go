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
)

func newServeCommand(flags *genericclioptions.ConfigFlags) *cobra.Command {
	var name, temporaryName, program string
	var defaults supervisor.Options
	settings := flag.NewFlagSet("runner", flag.ContinueOnError)
	defaults.BindFlags(settings)
	cmd := &cobra.Command{
		Use: "serve [--name NAME | --agent NAME] [flags]", Short: "Run this agent's turns on this machine, starting its sandbox on demand",
		Example: "  airo agent serve --name demo --harness codex --codex-sandbox read-only\n  airo agent serve -a reviewer",
		Args: func(cmd *cobra.Command, args []string) error {
			if cmd.Flags().Changed("name") && temporaryName == "" {
				return errors.New("--name needs a temporary Agent name")
			}
			if len(args) > 0 && cmd.ArgsLenAtDash() != 0 {
				return errors.New("unexpected positional argument: use --harness and --sandbox, or pass helper flags after --")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
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
			args = append([]string(nil), args...)
			cmd.Flags().Visit(func(f *pflag.Flag) {
				if settings.Lookup(f.Name) != nil {
					name := f.Name
					if program != "" && name == "sandbox" {
						// Older standalone supervisors use the original flag name.
						name = "runner"
					}
					args = append(args, "--"+name+"="+f.Value.String())
				}
			})
			out := cmd.ErrOrStderr()
			if program == "" {
				settings.SetOutput(out)
				if err := settings.Parse(args); err != nil {
					return err
				}
				if settings.NArg() != 0 {
					return fmt.Errorf("unexpected runner argument %q", settings.Arg(0))
				}
				if name != "" && defaults.Harness == "" && defaults.ClaudePermissionMode == "" && defaults.CodexSandbox == "" {
					defaults.Harness, defaults.CodexSandbox = "codex", "read-only"
				}
				if _, err := defaults.Adapter(); err != nil {
					return err
				}
			}
			serve := func(ctx context.Context, agentName string, t tunnel.RunnerTunnel, workspace string) error {
				return serveRunner(ctx, t.Namespace, workspace, defaults, program, args, out, func(ctx context.Context, h http.Handler) error {
					if name == "" {
						fmt.Fprintf(out, "Created temporary agent %s\n", agentName)
					} else {
						fmt.Fprintf(out, "Serving agent %s/%s through tunnel %s. Waiting for turns.\n", ns, agentName, t.Name)
					}
					return t.ServeHTTP(ctx, out, cfg, h)
				})
			}
			if name == "" {
				return serveTemporary(ctx, c, ns, temporaryName, out, serve)
			}
			t, err := agentTunnel(ctx, c, ns, name)
			if err != nil {
				return err
			}
			return serve(ctx, name, t, t.UID)
		},
	}
	cmd.Flags().StringVarP(&name, "agent", "a", "", "existing remote Agent to serve (default: create a temporary Agent)")
	cmd.Flags().StringVar(&temporaryName, "name", "", "create a temporary Agent with this name (default: generate a name)")
	cmd.MarkFlagsMutuallyExclusive("name", "agent")
	cmd.Flags().BoolVarP(&defaults.Verbose, "verbose", "v", false, settings.Lookup("verbose").Usage)
	cmd.Flags().AddGoFlagSet(settings)
	cmd.Flags().Lookup("harness").Usage = "coding agent: claude or codex (serving an existing Agent defaults to read-only Codex)"
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

func serveRunner(ctx context.Context, ns, workspace string, opts supervisor.Options, program string, args []string, out io.Writer, serve serveTunnel) (err error) {
	var secret [32]byte
	rand.Read(secret[:])
	token := hex.EncodeToString(secret[:])
	identity := sha256.Sum256([]byte(workspace))
	name := fmt.Sprintf("airo-runner-%x", identity[:12])
	if program != "" {
		return withExternalSupervisor(ctx, ns, name, token, program, args, out, serve)
	}

	host, err := supervisor.New(name, token, opts, slog.New(slog.NewTextHandler(out, nil)))
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, host.Close()) }()
	return serve(ctx, runnerRequests(ns, token, host))
}

func withExternalSupervisor(ctx context.Context, ns, name, token, program string, args []string, out io.Writer, serve serveTunnel) error {
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
	err = serve(ctx, runnerRequests(ns, token, proxy))
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
