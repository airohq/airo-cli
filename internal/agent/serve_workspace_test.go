package agent

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/volume"
	"github.com/moby/moby/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type workspaceVolumes map[string]volume.Volume

func (e workspaceVolumes) Close() error { return nil }

func (e workspaceVolumes) VolumeCreate(_ context.Context, opts client.VolumeCreateOptions) (client.VolumeCreateResult, error) {
	if _, ok := e[opts.Name]; !ok {
		e[opts.Name] = volume.Volume{Name: opts.Name, Labels: opts.Labels}
	}
	return client.VolumeCreateResult{Volume: e[opts.Name]}, nil
}

func (e workspaceVolumes) VolumeInspect(_ context.Context, name string, _ client.VolumeInspectOptions) (client.VolumeInspectResult, error) {
	v, ok := e[name]
	if !ok {
		return client.VolumeInspectResult{}, errdefs.ErrNotFound
	}
	return client.VolumeInspectResult{Volume: v}, nil
}

func (e workspaceVolumes) VolumeList(context.Context, client.VolumeListOptions) (client.VolumeListResult, error) {
	var result client.VolumeListResult
	for _, v := range e {
		result.Items = append(result.Items, v)
	}
	return result, nil
}

func (e workspaceVolumes) VolumeRemove(_ context.Context, name string, _ client.VolumeRemoveOptions) (client.VolumeRemoveResult, error) {
	delete(e, name)
	return client.VolumeRemoveResult{}, nil
}

func TestServingWorkspaceLifetime(t *testing.T) {
	a := build("team", "demo", options{})
	a.SetUID("current")
	peer := build("team", "busy", options{})
	peer.SetUID("live")
	cluster := fake.NewClientBuilder().WithObjects(a, peer).Build()
	engine := workspaceVolumes{}
	workspace := func() *temporaryWorkspace {
		w := newTemporaryWorkspace(a, "scope", cluster, io.Discard)
		w.engine = engine
		return w
	}
	first := workspace()
	ctx := t.Context()
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	h := first.onTurn(next)
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/v1/info", nil))
	if len(engine) != 0 {
		t.Fatal("info allocated a workspace")
	}
	for _, v := range []struct{ name, scope, agent, uid string }{
		{"abandoned", "scope", "demo", "old"}, {"live", "scope", "busy", "live"}, {"unrelated", "another", "gone", "old"},
	} {
		engine[v.name] = volume.Volume{Name: v.name, Labels: map[string]string{workspaceOwner: v.name, workspaceScopeLabel: v.scope, workspaceAgent: v.agent, workspaceUID: v.uid}}
	}
	engine["legacy"] = volume.Volume{Name: "legacy"}
	for range 2 {
		resp := httptest.NewRecorder()
		h.ServeHTTP(resp, httptest.NewRequest("POST", "/v1/turn", nil))
		if resp.Code != http.StatusOK || engine[first.name].Name != first.name {
			t.Fatal("turn workspace", resp.Code)
		}
	}
	if _, ok := engine["abandoned"]; ok {
		t.Fatal("left expired workspace")
	}
	if err := first.close(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"unrelated", "live", "legacy"} {
		if _, ok := engine[name]; !ok {
			t.Fatalf("removed %s", name)
		}
	}
	if _, ok := engine[first.name]; ok {
		t.Fatal("workspace survived serve exit")
	}
	if err := first.prepare(ctx); err == nil {
		t.Fatal("reopened closed workspace")
	}
	second := workspace()
	if second.name == first.name {
		t.Fatal("reused workspace on second invocation")
	}
	engine[second.name] = volume.Volume{Name: second.name}
	if err := second.prepare(ctx); err == nil {
		t.Fatal("adopted foreign volume")
	}
	if err := second.close(); err != nil {
		t.Fatal(err)
	}
	if _, ok := engine[second.name]; !ok {
		t.Fatal("deleted foreign volume")
	}
}
