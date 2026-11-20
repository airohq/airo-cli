package agent

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
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

	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/airohq/airo-cli/internal/tunnel"
)

func newServeCommand(flags *genericclioptions.ConfigFlags) *cobra.Command {
	var name, program string
	cmd := &cobra.Command{
		Use: "serve --agent NAME -- [RUNNER FLAGS]", Short: "Run this agent's turns on this machine, starting its runner on demand",
		Example: "  airo agent serve -a reviewer -- --harness codex --codex-sandbox workspace-write",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 || cmd.ArgsLenAtDash() != 0 {
				return errors.New("pass runner settings after --, for example -- --harness codex --codex-sandbox workspace-write")
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
			t, err := agentTunnel(ctx, c, ns, name)
			if err != nil {
				return err
			}
			out := cmd.ErrOrStderr()
			return withSupervisor(ctx, t, program, args, out, func(ctx context.Context, h http.Handler) error {
				fmt.Fprintf(out, "Serving agent %s/%s through tunnel %s. Waiting for turns.\n", ns, name, t.Name)
				return t.ServeHTTP(ctx, out, cfg, h)
			})
		},
	}
	cmd.Flags().StringVarP(&name, "agent", "a", "", "agent whose remote Harness points at the runner Tunnel (required)")
	cmd.Flags().StringVar(&program, "runner-command", "remote-harness", "local executable implementing the supervisor protocol")
	_ = cmd.MarkFlagRequired("agent")
	return cmd
}

func agentTunnel(ctx context.Context, c client.Reader, ns, name string) (tunnel.RunnerTunnel, error) {
	var zero tunnel.RunnerTunnel
	a := &unstructured.Unstructured{}
	a.SetGroupVersionKind(agentGVK)
	if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, a); err != nil {
		return zero, err
	}
	ref := detailString(a.Object, "spec", "harnessRef", "name")
	if ref == "" {
		return zero, fmt.Errorf("agent %s has no spec.harnessRef.name", name)
	}
	h := &unstructured.Unstructured{}
	h.SetGroupVersionKind(harnessGVK)
	if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: ref}, h); err != nil {
		return zero, err
	}
	env, _, err := unstructured.NestedSlice(h.Object, "spec", "env")
	if err != nil {
		return zero, err
	}
	for _, entry := range env {
		item, _ := entry.(map[string]any)
		if item["name"] == "REMOTE_HARNESS_URL" {
			if origin := detailString(item, "value"); origin != "" {
				return tunnel.FindRunnerTunnel(ctx, c, ns, origin)
			}
		}
	}
	return zero, fmt.Errorf("harness %s needs REMOTE_HARNESS_URL set to its Tunnel's status.callerURL in spec.env", ref)
}

// Remote harness protocol v1. Only requests authenticated by the Tunnel
// gateway as project actors can be translated to the local runner identity.
func runnerProxy(ns, token string, tr http.RoundTripper) http.Handler {
	proxy := &httputil.ReverseProxy{Transport: tr, FlushInterval: -1, Rewrite: func(p *httputil.ProxyRequest) {
		p.Out.URL.Scheme = "http"
		p.Out.URL.Host = "runner"
		p.Out.Host = "runner"
		p.Out.Header = make(http.Header)
		p.Out.Header.Set("Authorization", "Bearer "+token)
		p.Out.Header.Set("Content-Type", "application/json")
	}}
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
		proxy.ServeHTTP(w, r)
	})
}

func withSupervisor(ctx context.Context, t tunnel.RunnerTunnel, program string, args []string, out io.Writer, serve func(context.Context, http.Handler) error) error {
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
	var secret [32]byte
	rand.Read(secret[:])
	token := hex.EncodeToString(secret[:])
	identity := sha256.Sum256([]byte(t.UID))
	args = append(append([]string{"supervise"}, args...), "--socket", socket, "--name", fmt.Sprintf("airo-runner-%x", identity[:12]))
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
	err = serve(ctx, runnerProxy(t.Namespace, token, tr))
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
