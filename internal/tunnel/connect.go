package tunnel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/andydunstall/piko/client"
	"k8s.io/client-go/rest"

	"github.com/airohq/airo-cli/internal/bearer"
)

// Tunnel client protocol v1: what the tunnel server and its clients agree on.
// The values are copied on purpose. The CLI depends on the platform's API and
// this wire protocol, never on the operator's code, and a released binary
// pins the protocol anyway.
const (
	// The path of a client's connection is
	// /_tunnel/v1/machines/<machine>/piko/v1/upstream/<pool|direct>. The Piko
	// client appends everything from /piko.
	clientPathPrefix = "/_tunnel/v1/machines/"
	// A client listens on pool to take any request, and on direct to take the
	// requests addressed to it with the Tunnel-Client header.
	endpointPool   = "pool"
	endpointDirect = "direct"
)

// tokenRefresh is how often connect reconnects with a fresh token.
const tokenRefresh = 10 * time.Minute

// connect serves the Tunnel with the service at to until ctx is done. It
// connects out to the Tunnel's client URL as the kubeconfig's identity.
func connect(ctx context.Context, out io.Writer, cfg *rest.Config, ns, name, clientURL, to string) error {
	host, _ := os.Hostname()
	host, _, _ = strings.Cut(host, ".")
	base, err := url.Parse(strings.TrimSuffix(clientURL, "/") + clientPathPrefix + dnsLabel(host, "machine"))
	if err != nil {
		return err
	}
	// The connection carries your Kubernetes token, which anyone who sees
	// unencrypted traffic could reuse.
	if base.Scheme != "https" {
		return fmt.Errorf("tunnel %s/%s uses %s, which would send your Kubernetes token unencrypted", ns, name, clientURL)
	}

	// Connect again with a fresh token on each refresh, because the token
	// from an exec plugin expires and the Piko client reconnects with the one
	// it started with; the new connections start before the old ones close.
	// The Piko client gives up when the server refuses it, such as after
	// losing access, so a stopped forwarder also reconnects, with backoff.
	var current []*client.Forwarder
	closeAll := func(fs []*client.Forwarder) {
		for _, f := range fs {
			_ = f.Close()
		}
	}
	defer func() { closeAll(current) }()
	backoff := time.Second
	for {
		next, err := listen(ctx, cfg, base, to)
		if ctx.Err() != nil {
			closeAll(next)
			return nil
		}
		if err != nil {
			// The server's refusal says why, such as a missing connect verb.
			fmt.Fprintf(out, "Not connected: %v. Retrying in %s.\n", err, backoff)
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(backoff):
			}
			backoff = min(backoff*2, 30*time.Second)
			continue
		}
		closeAll(current)
		current = next

		stopped := make(chan error, len(next))
		for _, f := range next {
			go func() { stopped <- f.Wait() }()
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(tokenRefresh):
			backoff = time.Second
		case err := <-stopped:
			if ctx.Err() != nil {
				return nil
			}
			if err == nil {
				err = errors.New("the connection closed")
			}
			fmt.Fprintf(out, "Disconnected: %v. Reconnecting.\n", err)
			closeAll(current)
			current = nil
		}
	}
}

// listen connects the pool and direct endpoints with a fresh token.
func listen(ctx context.Context, cfg *rest.Config, base *url.URL, to string) ([]*client.Forwarder, error) {
	tok, err := bearer.Token(cfg)
	if err != nil {
		return nil, err
	}
	up := &client.Upstream{URL: base, Token: tok}
	var fs []*client.Forwarder
	for _, ep := range []string{endpointPool, endpointDirect} {
		f, err := up.ListenAndForward(ctx, ep, to)
		if err != nil {
			for _, f := range fs {
				_ = f.Close()
			}
			return nil, err
		}
		fs = append(fs, f)
	}
	return fs, nil
}
