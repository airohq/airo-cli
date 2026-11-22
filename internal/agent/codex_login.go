package agent

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/airohq/kagent-remote-harness/runner/supervisor"
	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
	"github.com/spf13/cobra"
)

// Login always runs the shipped Codex CLI. A custom worker image is never
// used for sign-in or given access to the saved subscription by default.
const loginImage = "ghcr.io/airohq/kagent-remote-harness/runner@sha256:5272af6ec215dc0df6429eb678ab37ee2cc4e9f9a76319b20f05b5997275b2d7"
const officialWorker = "ghcr.io/airohq/kagent-remote-harness/runner:latest"

// The engine itself scopes volumes. Include the host and user configuration
// path so two clients of a shared endpoint get separate login volumes.
func codexVolumeName() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	hostname, err := os.Hostname()
	if err != nil {
		return "", err
	}
	id := sha256.Sum256([]byte(hostname + "\x00" + dir))
	return fmt.Sprintf("airo-codex-login-%x", id[:12]), nil
}

func workerCodexLogin(opts supervisor.Options) (string, error) {
	if opts.Harness != "codex" || (opts.Image != officialWorker && opts.Image != loginImage) {
		return "", nil
	}
	if os.Getenv("OPENAI_API_KEY") != "" || os.Getenv("CODEX_API_KEY") != "" {
		return "", nil
	}
	return codexVolumeName()
}

func newAgentLoginCommand() *cobra.Command {
	return &cobra.Command{
		Use:       "login codex",
		Short:     "Sign in to ChatGPT for local Codex agents on this container engine",
		Long:      "Sign in once with a ChatGPT subscription. Codex prints a device code for your browser. Future local Codex workers on this engine can reuse the login.",
		ValidArgs: []string{"codex"},
		Args:      cobra.MatchAll(cobra.ExactArgs(1), cobra.OnlyValidArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			volume, err := codexVolumeName()
			if err != nil {
				return err
			}
			engine, err := client.New(client.FromEnv)
			if err != nil {
				return err
			}
			defer engine.Close()
			if err := loginCodex(ctx, engine, volume, cmd.ErrOrStderr()); err != nil {
				return err
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "Signed in to Codex for local agents. Credentials are stored in volume %s.\n", volume)
			return nil
		},
	}
}

// Keep login logs, configuration and all other CLI state in tmpfs. The
// volume contains only the auth record and the CLI's credential locks.
const codexLoginScript = `set -eu
umask 077
auth_dir=$1
mkdir -p "$HOME" "$CODEX_HOME" "$auth_dir"
codex -c 'cli_auth_credentials_store="file"' login --device-auth
test -s "$CODEX_HOME/auth.json"
# Device login clears any previous credential before asking for approval.
# Publish only the completed record, under the locks used by worker refreshes.
exec 9>>"$auth_dir/auth.lock"
flock -w 10 9
exec 8>>"$auth_dir/auth.json.lock"
flock -w 10 8
stage=$(mktemp "$auth_dir/.login.XXXXXX")
trap 'rm -f "$stage"' EXIT
cp "$CODEX_HOME/auth.json" "$stage"
mv -fT "$stage" "$auth_dir/auth.json"`

func loginCodex(ctx context.Context, e *client.Client, volume string, out io.Writer) (err error) {
	if _, err := e.ImageInspect(ctx, loginImage); errdefs.IsNotFound(err) {
		pull, err := e.ImagePull(ctx, loginImage, client.ImagePullOptions{})
		if err != nil {
			return err
		}
		if err := pull.Wait(ctx); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	v, err := e.VolumeCreate(ctx, client.VolumeCreateOptions{Name: volume, Labels: map[string]string{supervisor.CodexLoginLabel: volume}})
	if err != nil {
		return err
	}
	if v.Volume.Labels[supervisor.CodexLoginLabel] != volume {
		return fmt.Errorf("credential volume %s already belongs to another application", volume)
	}
	createCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	created, err := e.ContainerCreate(createCtx, client.ContainerCreateOptions{
		Config: &container.Config{
			Image: loginImage, User: "10001:10001", Entrypoint: []string{"/bin/sh", "-ec"}, Cmd: []string{codexLoginScript, "codex-login", "/work/auth"},
			Env: []string{"CODEX_HOME=/run/airo-login", "HOME=/tmp/home"}, AttachStdout: true, AttachStderr: true,
		},
		HostConfig: &container.HostConfig{
			ReadonlyRootfs: true, CapDrop: []string{"ALL"}, SecurityOpt: []string{"no-new-privileges"},
			LogConfig: container.LogConfig{Type: "none"}, Tmpfs: map[string]string{"/tmp": "rw,nosuid,nodev,mode=1777", "/run/airo-login": "rw,nosuid,nodev,uid=10001,gid=10001,mode=0700"},
			Mounts: []mount.Mount{{Type: mount.TypeVolume, Source: volume, Target: "/work"}},
		},
	})
	if err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, e2 := e.ContainerRemove(cleanup, created.ID, client.ContainerRemoveOptions{Force: true})
		if e2 != nil && !errdefs.IsNotFound(e2) {
			err = errors.Join(err, e2)
		}
	}()
	stream, err := e.ContainerAttach(ctx, created.ID, client.ContainerAttachOptions{Stream: true, Stdout: true, Stderr: true})
	if err != nil {
		return err
	}
	defer stream.Close()
	if _, err := e.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		return err
	}
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { stream.Close(); close(done) })
	_, err = stdcopy.StdCopy(out, out, stream.Reader)
	if !stop() {
		<-done
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return err
	}
	wait := e.ContainerWait(ctx, created.ID, client.ContainerWaitOptions{})
	select {
	case err := <-wait.Error:
		return err
	case result := <-wait.Result:
		if result.StatusCode != 0 {
			return fmt.Errorf("Codex subscription sign-in exited with status %d", result.StatusCode)
		}
		return nil
	}
}
