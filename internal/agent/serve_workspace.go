package agent

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/client"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	kube "sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	workspaceOwner = "airohq.dev/temporary-workspace"
	workspaceRelay = "airohq.dev/relay-uid"
	workspaceAgent = "airohq.dev/agent-name"
	workspaceUID   = "airohq.dev/agent-uid"
)

// A serving process owns one volume across all its turns and idle worker restarts.
// It allocates the volume on the first turn.
type temporaryWorkspace struct {
	name, relay, namespace, agent, uid string
	cluster                            kube.Reader
	out                                io.Writer

	mu            sync.Mutex
	engine        volumeEngine
	ready, closed bool
}

type volumeEngine interface {
	io.Closer
	VolumeList(context.Context, client.VolumeListOptions) (client.VolumeListResult, error)
	VolumeCreate(context.Context, client.VolumeCreateOptions) (client.VolumeCreateResult, error)
	VolumeInspect(context.Context, string, client.VolumeInspectOptions) (client.VolumeInspectResult, error)
	VolumeRemove(context.Context, string, client.VolumeRemoveOptions) (client.VolumeRemoveResult, error)
}

func newTemporaryWorkspace(a *unstructured.Unstructured, relay types.UID, cluster kube.Reader, out io.Writer) *temporaryWorkspace {
	return &temporaryWorkspace{
		name: "airo-serve-" + rand.Text(), relay: string(relay), namespace: a.GetNamespace(),
		agent: a.GetName(), uid: string(a.GetUID()), cluster: cluster, out: out,
	}
}

func (s *temporaryWorkspace) onTurn(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/turn" {
			if err := s.prepare(r.Context()); err != nil {
				http.Error(w, "prepare temporary workspace: "+err.Error(), http.StatusServiceUnavailable)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *temporaryWorkspace) prepare(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("serving session has stopped")
	}
	if s.ready {
		return nil
	}
	if s.engine == nil {
		e, err := client.New(client.FromEnv)
		if err != nil {
			return err
		}
		s.engine = e
	}
	s.reap(ctx)
	labels := map[string]string{workspaceOwner: s.name, workspaceRelay: s.relay, workspaceAgent: s.agent, workspaceUID: s.uid}
	result, err := s.engine.VolumeCreate(ctx, client.VolumeCreateOptions{Name: s.name, Labels: labels})
	if err != nil {
		return err
	}
	if result.Volume.Labels[workspaceOwner] != s.name || result.Volume.Labels[workspaceUID] != s.uid {
		return fmt.Errorf("volume %s belongs to another session", s.name)
	}
	s.ready = true
	return nil
}

// A hard kill can leave a volume. Before reusing this relay, check its old
// volumes against their Agent UIDs. An idle but still live session keeps its files.
func (s *temporaryWorkspace) reap(ctx context.Context) {
	if s.relay == "" {
		return
	}
	result, err := s.engine.VolumeList(ctx, client.VolumeListOptions{Filters: make(client.Filters).Add("label", workspaceRelay+"="+s.relay)})
	if err != nil {
		fmt.Fprintf(s.out, "Checking for expired workspaces: %v\n", err)
		return
	}
	for _, v := range result.Items {
		owner := v.Labels[workspaceUID]
		agent := v.Labels[workspaceAgent]
		if owner == "" || owner == s.uid || agent == "" || v.Labels[workspaceOwner] != v.Name || v.Labels[workspaceRelay] != s.relay {
			continue
		}
		a := &unstructured.Unstructured{}
		a.SetGroupVersionKind(agentGVK)
		err := s.cluster.Get(ctx, kube.ObjectKey{Namespace: s.namespace, Name: agent}, a)
		if err != nil && !apierrors.IsNotFound(err) || err == nil && string(a.GetUID()) == owner {
			continue
		}
		_, err = s.engine.VolumeRemove(ctx, v.Name, client.VolumeRemoveOptions{})
		if err != nil && !errdefs.IsNotFound(err) {
			fmt.Fprintf(s.out, "Removing expired workspace %s: %v\n", v.Name, err)
		}
	}
}

func (s *temporaryWorkspace) close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	if s.engine == nil {
		return nil
	}
	defer s.engine.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// Inspect even when CREATE lost its response. Never remove a collision.
	v, err := s.engine.VolumeInspect(ctx, s.name, client.VolumeInspectOptions{})
	if err == nil && v.Volume.Labels[workspaceOwner] == s.name && v.Volume.Labels[workspaceUID] == s.uid {
		_, err = s.engine.VolumeRemove(ctx, s.name, client.VolumeRemoveOptions{})
	}
	if err != nil && !errdefs.IsNotFound(err) {
		return fmt.Errorf("remove temporary workspace %s: %w", s.name, err)
	}
	return nil
}
