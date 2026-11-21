package agent

import (
	"context"
	"errors"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/watch"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// An authoritative change to the selected Agent ends the local chat.
type agentChange string

func (e agentChange) Error() string { return string(e) }

func isAgentChange(err error) bool {
	var change agentChange
	return errors.As(err, &change)
}

func deletedAgent(a *unstructured.Unstructured) error {
	return agentChange(fmt.Sprintf("agent %s/%s was deleted", a.GetNamespace(), a.GetName()))
}

func replacedAgent(a *unstructured.Unstructured) error {
	return agentChange(fmt.Sprintf("agent %s/%s was replaced", a.GetNamespace(), a.GetName()))
}

func getChatAgent(ctx context.Context, c client.Reader, selected *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	a := &unstructured.Unstructured{}
	a.SetGroupVersionKind(agentGVK)
	err := c.Get(ctx, client.ObjectKeyFromObject(selected), a)
	if apierrors.IsNotFound(err) {
		return nil, deletedAgent(selected)
	}
	if err != nil {
		return nil, err
	}
	if selected.GetUID() != "" && a.GetUID() != selected.GetUID() {
		return nil, replacedAgent(selected)
	}
	return a, nil
}

func availableForChat(a *unstructured.Unstructured) error {
	ready, why, err := readiness(a)
	if err != nil || ready {
		return err
	}
	return agentChange(fmt.Sprintf("agent %s/%s is no longer ready: %s", a.GetNamespace(), a.GetName(), why))
}

func refreshChatAgent(ctx context.Context, c client.Reader, selected *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	a, err := getChatAgent(ctx, c, selected)
	if err != nil {
		return nil, err
	}
	return a, availableForChat(a)
}

// Start at the ready object's resourceVersion. After a closed or expired
// watch, read the object again before opening a new watch. Callers with only
// read access still detect changes by polling.
func watchChatAgent(ctx context.Context, c client.WithWatch, selected *unstructured.Unstructured) error {
	current := selected
	sel := fields.OneTermEqualSelector("metadata.name", selected.GetName())
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(agentGVK.GroupVersion().WithKind("AgentList"))
	poll := false
	for ctx.Err() == nil {
		if !poll && current != nil {
			opts := &client.ListOptions{
				Namespace:     selected.GetNamespace(),
				FieldSelector: sel,
				Raw:           &metav1.ListOptions{ResourceVersion: current.GetResourceVersion()},
			}
			w, err := c.Watch(ctx, list, opts)
			if err == nil {
				err = readAgentEvents(ctx, w, selected)
				w.Stop()
			}
			if isAgentChange(err) {
				return err
			}
			poll = apierrors.IsForbidden(err) || apierrors.IsMethodNotSupported(err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(readyPoll):
		}
		var err error
		current, err = refreshChatAgent(ctx, c, selected)
		if isAgentChange(err) {
			return err
		}
	}
	return nil
}

func readAgentEvents(ctx context.Context, w watch.Interface, selected *unstructured.Unstructured) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, open := <-w.ResultChan():
			if !open {
				return nil
			}
			if ev.Type == watch.Error {
				return apierrors.FromObject(ev.Object)
			}
			a, ok := ev.Object.(*unstructured.Unstructured)
			if !ok || a.GetName() != selected.GetName() || a.GetNamespace() != selected.GetNamespace() {
				continue
			}
			switch ev.Type {
			case watch.Deleted:
				return deletedAgent(selected)
			case watch.Added, watch.Modified:
				if a.GetUID() != selected.GetUID() {
					return replacedAgent(selected)
				}
				if err := availableForChat(a); err != nil {
					return err
				}
			}
		}
	}
}
