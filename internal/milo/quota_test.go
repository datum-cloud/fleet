package milo

import (
	"context"
	"errors"
	"slices"
	"testing"

	quotav1alpha1 "go.miloapis.com/milo/pkg/apis/quota/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"go.datum.net/fleet/internal/allocation"
	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/tenancy"
)

func quotaClient(t *testing.T) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := quotav1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	// Like the API server, the fake assigns a UID on create.
	return fake.NewClientBuilder().WithScheme(scheme).WithInterceptorFuncs(interceptor.Funcs{Create: func(ctx context.Context, api client.WithWatch, object client.Object, options ...client.CreateOption) error {
		object.SetUID(types.UID("uid-" + object.GetName()))
		return api.Create(ctx, object, options...)
	}}).Build()
}

// consumers resolves every project to one incarnation and API.
func consumers(api client.Client, uid types.UID) tenancy.Resolver {
	return tenancy.ResolverFunc(func(_ context.Context, ref fleetv1.TypedReference) (tenancy.Consumer, error) {
		return tenancy.Consumer{Ref: ref, UID: uid, Client: api}, nil
	})
}

var machine = allocation.Machine{Consumer: tenancy.Project("consumer-example"), ConsumerUID: "00000000-0000-4000-8000-000000000501", Namespace: "default", Name: "allocation", UID: "00000000-0000-4000-8000-000000000601"}

// TestTheQuotaClaimIsNamedFromTheMachineAndGrantedOnce creates the claim
// once, retries find it by name, and only a granted claim reserves quota.
func TestTheQuotaClaimIsNamedFromTheMachineAndGrantedOnce(t *testing.T) {
	ctx := context.Background()
	api := quotaClient(t)
	quota := Quota{Consumers: consumers(api, machine.ConsumerUID)}
	if _, err := quota.Acquire(ctx, machine); !errors.Is(err, ErrQuotaPending) {
		t.Fatalf("first acquire = %v", err)
	}
	// A retry after a lost create response finds the same claim.
	if _, err := quota.Acquire(ctx, machine); !errors.Is(err, ErrQuotaPending) {
		t.Fatalf("retried acquire = %v", err)
	}
	var claims quotav1alpha1.ResourceClaimList
	if err := api.List(ctx, &claims); err != nil || len(claims.Items) != 1 || claims.Items[0].Name != ClaimName(machine) {
		t.Fatalf("claims = %+v, %v", claims.Items, err)
	}
	claim := &claims.Items[0]
	claim.Generation = 2
	claim.Status.ObservedGeneration = 2
	claim.Status.Conditions = []metav1.Condition{{Type: quotav1alpha1.ResourceClaimGranted, Status: metav1.ConditionTrue}}
	if err := api.Update(ctx, claim); err != nil {
		t.Fatal(err)
	}
	if name, err := quota.Acquire(ctx, machine); err != nil || name != ClaimName(machine) {
		t.Fatalf("granted acquire = %q, %v", name, err)
	}
	// Another Machine with the same UID in another project gets its own name.
	other := machine
	other.ConsumerUID = "00000000-0000-4000-8000-000000000502"
	if ClaimName(other) == ClaimName(machine) {
		t.Fatal("two projects share a quota claim name")
	}
	claim.Spec.Requests[0].Amount = 2
	if err := api.Update(ctx, claim); err != nil {
		t.Fatal(err)
	}
	if _, err := quota.Acquire(ctx, machine); !errors.Is(err, ErrQuotaInvalid) {
		t.Fatalf("altered claim = %v", err)
	}
}

// TestReleaseIsObservedNotAssumed deletes the claim, then removes Fleet's
// finalizer, and reports it released only once it is gone.
func TestReleaseIsObservedNotAssumed(t *testing.T) {
	ctx := context.Background()
	api := quotaClient(t)
	quota := Quota{Consumers: consumers(api, machine.ConsumerUID)}
	_, _ = quota.Acquire(ctx, machine)
	if err := quota.Release(ctx, machine); !errors.Is(err, ErrQuotaPending) {
		t.Fatalf("release requesting deletion = %v", err)
	}
	// The deleting claim is still held, and so still counted, until Fleet
	// removes its finalizer.
	claim := &quotav1alpha1.ResourceClaim{}
	if err := api.Get(ctx, client.ObjectKey{Namespace: machine.Namespace, Name: ClaimName(machine)}, claim); err != nil || claim.DeletionTimestamp == nil || !slices.Contains(claim.Finalizers, QuotaFinalizer) {
		t.Fatalf("claim after deletion request = %+v, %v", claim.ObjectMeta, err)
	}
	if err := quota.Release(ctx, machine); !errors.Is(err, ErrQuotaPending) {
		t.Fatalf("release removing the finalizer = %v", err)
	}
	if err := quota.Release(ctx, machine); err != nil {
		t.Fatalf("release after deletion = %v", err)
	}
}

// TestTheClaimIsHeldUntilFleetReleasesIt keeps the quota of a claim that
// someone else deletes, and restores a finalizer removed from a live claim.
func TestTheClaimIsHeldUntilFleetReleasesIt(t *testing.T) {
	ctx := context.Background()
	api := quotaClient(t)
	quota := Quota{Consumers: consumers(api, machine.ConsumerUID)}
	_, _ = quota.Acquire(ctx, machine)
	key := client.ObjectKey{Namespace: machine.Namespace, Name: ClaimName(machine)}
	claim := &quotav1alpha1.ResourceClaim{}
	if err := api.Get(ctx, key, claim); err != nil || !slices.Contains(claim.Finalizers, QuotaFinalizer) {
		t.Fatalf("created claim = %+v, %v", claim.ObjectMeta, err)
	}

	// A finalizer removed from a live claim is restored on the next acquire.
	claim.Finalizers = nil
	if err := api.Update(ctx, claim); err != nil {
		t.Fatal(err)
	}
	if _, err := quota.Acquire(ctx, machine); !errors.Is(err, ErrQuotaPending) {
		t.Fatalf("acquire restoring the finalizer = %v", err)
	}
	if err := api.Get(ctx, key, claim); err != nil || !slices.Contains(claim.Finalizers, QuotaFinalizer) {
		t.Fatalf("restored claim = %+v, %v", claim.ObjectMeta, err)
	}

	// A delete by someone else, such as a namespace purge, leaves the claim
	// held and counted. Acquire reports it unknown rather than granted.
	if err := api.Delete(ctx, claim); err != nil {
		t.Fatal(err)
	}
	if err := api.Get(ctx, key, claim); err != nil || claim.DeletionTimestamp == nil {
		t.Fatalf("claim deleted by another actor = %+v, %v", claim.ObjectMeta, err)
	}
	if _, err := quota.Acquire(ctx, machine); !errors.Is(err, ErrQuotaUnknown) {
		t.Fatalf("acquire of a deleting claim = %v", err)
	}
	// Release finishes what the other actor started.
	if err := quota.Release(ctx, machine); !errors.Is(err, ErrQuotaPending) {
		t.Fatalf("release of a deleting claim = %v", err)
	}
	if err := quota.Release(ctx, machine); err != nil {
		t.Fatalf("release after the finalizer = %v", err)
	}
}

// TestQuotaServesOnlyTheMachinesProjectIncarnation never claims quota in a
// project recreated under the Machine's project name, or for a consumer that
// is not a Milo Project, and finds nothing left to release in a recreated
// project.
func TestQuotaServesOnlyTheMachinesProjectIncarnation(t *testing.T) {
	ctx := context.Background()
	api := quotaClient(t)
	recreated := Quota{Consumers: consumers(api, "00000000-0000-4000-8000-000000000509")}
	if _, err := recreated.Acquire(ctx, machine); !errors.Is(err, tenancy.ErrConsumerGone) {
		t.Fatalf("acquire in a recreated project = %v", err)
	}
	if err := recreated.Release(ctx, machine); err != nil {
		t.Fatalf("release in a recreated project = %v", err)
	}
	namespaced := machine
	namespaced.Consumer = tenancy.Namespace("consumer-example")
	if _, err := (Quota{Consumers: consumers(api, machine.ConsumerUID)}).Acquire(ctx, namespaced); !errors.Is(err, tenancy.ErrUnsupportedConsumer) {
		t.Fatalf("acquire for a Namespace consumer = %v", err)
	}
	var claims quotav1alpha1.ResourceClaimList
	if err := api.List(ctx, &claims); err != nil || len(claims.Items) != 0 {
		t.Fatalf("claims = %+v, %v", claims.Items, err)
	}
}

// TestAnotherFinalizerKeepsTheClaimCounted removes only Fleet's finalizer.
// A claim another actor still holds exists, so Milo still counts it, and
// release stays pending until that actor lets go.
func TestAnotherFinalizerKeepsTheClaimCounted(t *testing.T) {
	ctx := context.Background()
	api := quotaClient(t)
	quota := Quota{Consumers: consumers(api, machine.ConsumerUID)}
	_, _ = quota.Acquire(ctx, machine)
	key := client.ObjectKey{Namespace: machine.Namespace, Name: ClaimName(machine)}
	claim := &quotav1alpha1.ResourceClaim{}
	if err := api.Get(ctx, key, claim); err != nil {
		t.Fatal(err)
	}
	claim.Finalizers = append(claim.Finalizers, "example.com/other")
	if err := api.Update(ctx, claim); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := quota.Release(ctx, machine); !errors.Is(err, ErrQuotaPending) {
			t.Fatalf("release while another finalizer holds the claim = %v", err)
		}
	}
	if err := api.Get(ctx, key, claim); err != nil || !slices.Equal(claim.Finalizers, []string{"example.com/other"}) {
		t.Fatalf("claim = %+v, %v", claim.ObjectMeta, err)
	}
	claim.Finalizers = nil
	if err := api.Update(ctx, claim); err != nil {
		t.Fatal(err)
	}
	if err := quota.Release(ctx, machine); err != nil {
		t.Fatalf("release after the other finalizer = %v", err)
	}
}
