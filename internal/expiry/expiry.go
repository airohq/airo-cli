// Package expiry renews the deadline the platform janitor uses for temporary resources.
package expiry

import (
	"context"
	"fmt"
	"io"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const Annotation = "airohq.dev/expires"
const lifetime = 15 * time.Minute

// Deadline returns the expiry to put on a resource at creation or renewal.
func Deadline() string {
	return time.Now().Add(lifetime).UTC().Format(time.RFC3339)
}

// Extend patches by UID so a restarted client cannot renew a replacement.
func Extend(ctx context.Context, c client.Client, obj *unstructured.Unstructured) error {
	target := &unstructured.Unstructured{}
	target.SetGroupVersionKind(obj.GroupVersionKind())
	target.SetNamespace(obj.GetNamespace())
	target.SetName(obj.GetName())
	patch := fmt.Sprintf(`{"metadata":{"uid":%q,"annotations":{%q:%q}}}`, obj.GetUID(), Annotation, Deadline())
	return c.Patch(ctx, target, client.RawPatch(types.MergePatchType, []byte(patch)))
}

// KeepAlive renews every five minutes. Check the wall clock more frequently
// because a monotonic timer may stand still while a laptop sleeps.
func KeepAlive(ctx context.Context, out io.Writer, c client.Client, obj *unstructured.Unstructured) error {
	return keepAlive(ctx, out, c, 30*time.Second, 5*time.Minute, obj)
}

func keepAlive(ctx context.Context, out io.Writer, c client.Client, check, interval time.Duration, obj *unstructured.Unstructured) error {
	tick := time.NewTicker(check)
	defer tick.Stop()
	last := time.Now().Round(0)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
		if time.Now().Round(0).Sub(last) < interval {
			continue
		}
		err := Extend(ctx, c, obj)
		switch {
		case ctx.Err() != nil:
			return nil
		case apierrors.IsNotFound(err), apierrors.IsConflict(err), apierrors.IsInvalid(err):
			return fmt.Errorf("%s %s is gone: it expired, was deleted or was replaced. Run the command again", obj.GetKind(), client.ObjectKeyFromObject(obj))
		case err != nil:
			fmt.Fprintf(out, "Could not extend %s %s: %v. Trying again.\n", obj.GetKind(), client.ObjectKeyFromObject(obj), err)
		default:
			last = time.Now().Round(0)
		}
	}
}
