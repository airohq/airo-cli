package agent

import (
	"context"
	"fmt"
	"io"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/watch"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Capsule creates the project endpoint, RoleBinding and default Agent
// asynchronously. Preserve immediate diagnostics for established projects.
func chatProject(ctx context.Context, c client.Client, ns string, out io.Writer) (*corev1.ConfigMap, error) {
	var deadline time.Time
	for {
		cm := &corev1.ConfigMap{}
		err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: endpointConfigMap}, cm)
		if err == nil {
			agentsInProject, listErr := list(ctx, c, ns, agents)
			if len(agentsInProject) > 0 {
				return cm, nil
			}
			if listErr != nil && !apierrors.IsForbidden(listErr) {
				return nil, listErr
			}
			// pick gives the usual diagnostic for an empty project.
		} else if !apierrors.IsNotFound(err) && !apierrors.IsForbidden(err) {
			return nil, err
		}

		if deadline.IsZero() {
			var project corev1.Namespace
			if lookupErr := c.Get(ctx, client.ObjectKey{Name: ns}, &project); lookupErr == nil {
				deadline = project.CreationTimestamp.Add(3 * time.Minute)
			}
		}
		if !time.Now().Before(deadline) {
			if apierrors.IsNotFound(err) {
				return nil, fmt.Errorf("project %s has no agent endpoint", ns)
			}
			return cm, err
		}
		if out != nil {
			fmt.Fprintf(out, "Waiting for project %s to provision its agents.\n", ns)
			out = nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(readyPoll):
		}
	}
}

// The project's marker can appear between the initial list and the watch.
// Resume from the list's version. A closed or expired watch starts a new list.
func waitProjectCredentials(ctx context.Context, c client.WithWatch, ns, name string, out io.Writer) error {
	field := fields.OneTermEqualSelector("metadata.name", name)
	announced := false
	for {
		items := &corev1.ConfigMapList{}
		opts := &client.ListOptions{Namespace: ns, FieldSelector: field}
		if err := c.List(ctx, items, opts); err != nil {
			return err
		}
		for _, item := range items.Items {
			if item.Name == name && item.DeletionTimestamp == nil {
				return nil
			}
		}
		if !announced {
			fmt.Fprintf(out, "Waiting for agent credentials in project %s.\n", ns)
			announced = true
		}
		opts.Raw = &metav1.ListOptions{ResourceVersion: items.ResourceVersion}
		w, err := c.Watch(ctx, &corev1.ConfigMapList{}, opts)
		if apierrors.IsResourceExpired(err) {
			continue
		}
		if err != nil {
			return err
		}
		ready, err := watchMarker(ctx, w, ns, name)
		w.Stop()
		if ready || err != nil {
			return err
		}
	}
}

func watchMarker(ctx context.Context, w watch.Interface, ns, name string) (bool, error) {
	for {
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case event, ok := <-w.ResultChan():
			if !ok {
				return false, nil
			}
			if event.Type == watch.Error {
				err := apierrors.FromObject(event.Object)
				if apierrors.IsResourceExpired(err) {
					return false, nil
				}
				return false, err
			}
			cm, ok := event.Object.(*corev1.ConfigMap)
			if ok && (event.Type == watch.Added || event.Type == watch.Modified) && cm.Namespace == ns && cm.Name == name && cm.DeletionTimestamp == nil {
				return true, nil
			}
		}
	}
}
