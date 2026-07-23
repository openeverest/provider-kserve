package common

import (
	"context"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// FieldOwner is the server-side apply field manager used by this provider.
// It scopes ownership of the fields the provider declares so that fields
// defaulted or managed by KServe's controllers and webhooks are left intact.
const FieldOwner = ProviderName

// Apply reconciles a desired object using server-side apply.
//
// The provider and KServe's own controllers/webhooks both write to the same
// resources (e.g. the webhook defaults spec fields the provider never sets).
// A full-object read-then-Update overwrites those defaults on every reconcile,
// which fights the KServe controller and produces "the object has been
// modified" conflicts in a hot loop.
//
// Server-side apply avoids both problems: it does not use resourceVersion (so
// concurrent KServe writes never conflict) and it only manages the fields this
// provider declares, leaving KServe-owned fields untouched. Re-applying the
// same intent is idempotent, so the stored object stays stable.
func Apply(ctx context.Context, cl client.Client, owner client.Object, obj client.Object) error {
	if err := controllerutil.SetControllerReference(owner, obj, cl.Scheme()); err != nil {
		return fmt.Errorf("failed to set owner reference: %w", err)
	}

	// Server-side apply requires the GVK to be populated on the object.
	gvks, _, err := cl.Scheme().ObjectKinds(obj)
	if err != nil {
		return fmt.Errorf("failed to determine GVK for %T: %w", obj, err)
	}
	obj.GetObjectKind().SetGroupVersionKind(gvks[0])

	// An apply patch must not carry a resourceVersion or managed fields.
	obj.SetResourceVersion("")
	obj.SetManagedFields(nil)

	return cl.Patch(ctx, obj, client.Apply,
		client.FieldOwner(FieldOwner),
		client.ForceOwnership,
	)
}
