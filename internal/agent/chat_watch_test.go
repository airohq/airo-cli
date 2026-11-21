package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/watch"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestChatWatchChanges(t *testing.T) {
	ready := reviewerWith([3]string{"Ready", "True", ""})
	ready.SetUID("original")
	terminating := ready.DeepCopy()
	now := metav1.Now()
	terminating.SetDeletionTimestamp(&now)
	replaced := ready.DeepCopy()
	replaced.SetUID("new-agent")
	unready := reviewerWith([3]string{"Ready", "False", "updating runtime"})
	unready.SetUID(ready.GetUID())
	for _, tc := range []struct {
		name   string
		event  watch.EventType
		object *unstructured.Unstructured
		want   string
	}{
		{"deleted", watch.Deleted, ready, "was deleted"},
		{"terminating", watch.Modified, terminating, "was deleted"},
		{"replaced", watch.Added, replaced, "was replaced"},
		{"unready", watch.Modified, unready, "no longer ready: updating runtime"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stream := watch.NewRaceFreeFake()
			defer stream.Stop()
			other := ready.DeepCopy()
			other.SetName("other")
			stream.Delete(other)
			stream.Modify(ready)
			stream.Action(tc.event, tc.object)
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			if err := readAgentEvents(ctx, stream, ready); !isAgentChange(err) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("chat ended with %v", err)
			}
		})
	}
}

func TestChatWatchRecoversAfterDisconnect(t *testing.T) {
	a := reviewerWith([3]string{"Ready", "True", ""})
	a.SetUID("original")
	connected := make(chan struct{})
	stream := watch.NewRaceFreeFake()
	c := fake.NewClientBuilder().WithObjects(a).WithInterceptorFuncs(interceptor.Funcs{
		Watch: func(_ context.Context, _ client.WithWatch, _ client.ObjectList, opts ...client.ListOption) (watch.Interface, error) {
			o := (&client.ListOptions{}).ApplyOptions(opts)
			raw := o.AsListOptions()
			if o.Namespace != "team" || raw.FieldSelector != "metadata.name=reviewer" || raw.ResourceVersion != a.GetResourceVersion() {
				t.Errorf("watch options: %v %+v", o.Namespace, raw)
			}
			close(connected)
			return stream, nil
		},
	}).Build()
	ctx, cancel := context.WithTimeout(t.Context(), 6*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- watchChatAgent(ctx, c, a) }()
	select {
	case <-connected:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	// Miss the deletion on the stream. The reconnect must read the object
	// before watching again, even when the name was immediately reused.
	if err := c.Delete(ctx, a); err != nil {
		t.Fatal(err)
	}
	replacement := a.DeepCopy()
	replacement.SetUID("replacement")
	replacement.SetResourceVersion("")
	if err := c.Create(ctx, replacement); err != nil {
		t.Fatal(err)
	}
	stream.Stop()
	if err := <-done; !isAgentChange(err) || !strings.Contains(err.Error(), "was replaced") {
		t.Fatalf("watch ended with %v", err)
	}
}

func TestDeletedAgentStopsSend(t *testing.T) {
	s, requests, _, notes := talk(t, stream())
	selected := reviewerWith([3]string{"Ready", "True", ""})
	c := fake.NewClientBuilder().Build()
	ctx, cancel := context.WithCancelCause(t.Context())
	defer cancel(nil)
	s.beforeTurn = func(ctx context.Context) error {
		_, err := refreshChatAgent(ctx, c, selected)
		if isAgentChange(err) {
			cancel(err)
		}
		return err
	}
	s.id = "old-session"
	if err := s.once(ctx, "hello"); !isAgentChange(err) {
		t.Fatal(err)
	}
	if rpc, _ := requests.seen(); len(rpc) != 0 || notes.Len() != 0 {
		t.Fatalf("unexpected RPC or closing hint: %v %q", rpc, notes)
	}
}

func TestChatWatchDeniedStillDetectsDeletion(t *testing.T) {
	a := reviewerWith([3]string{"Ready", "True", ""})
	c := fake.NewClientBuilder().WithInterceptorFuncs(interceptor.Funcs{
		Watch: func(context.Context, client.WithWatch, client.ObjectList, ...client.ListOption) (watch.Interface, error) {
			return nil, apierrors.NewForbidden(agentGVK.GroupVersion().WithResource("agents").GroupResource(), a.GetName(), errors.New("watch denied"))
		},
	}).Build()
	ctx, cancel := context.WithTimeout(t.Context(), 6*time.Second)
	defer cancel()
	if err := watchChatAgent(ctx, c, a); !isAgentChange(err) || !strings.Contains(err.Error(), "was deleted") {
		t.Fatal(err)
	}
}
