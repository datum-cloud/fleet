package bootaddress

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
)

const namespace = "fleet-provider"

func claimer(t *testing.T, objects ...client.Object) (Claimer, client.Client) {
	t.Helper()
	return claimerWith(t, interceptor.Funcs{}, objects...)
}

// claimerWith builds a claimer whose client calls pass through funcs, so a
// test can make IPAM answer as it does.
func claimerWith(t *testing.T, funcs interceptor.Funcs, objects ...client.Object) (Claimer, client.Client) {
	t.Helper()
	scheme := runtime.NewScheme()
	scheme.AddKnownTypeWithName(GVK, &unstructured.Unstructured{})
	scheme.AddKnownTypeWithName(GVK.GroupVersion().WithKind("IPClaimList"), &unstructured.UnstructuredList{})
	scheme.AddKnownTypeWithName(AllocationGVK, &unstructured.Unstructured{})
	scheme.AddKnownTypeWithName(AllocationGVK.GroupVersion().WithKind("IPAllocationList"), &unstructured.UnstructuredList{})
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).WithInterceptorFuncs(funcs).Build()
	return Claimer{Client: c, Namespace: namespace, Class: "provisioning", Kind: "DeviceInstallation"}, c
}

// ipamStatus is an IPAM refusal as IPAM writes it: an HTTP code, a reason, and
// one cause naming IPAM's own refusal.
func ipamStatus(code int32, reason metav1.StatusReason, cause, pool string) error {
	return &apierrors.StatusError{ErrStatus: metav1.Status{
		Status: metav1.StatusFailure, Code: code, Reason: reason, Message: cause,
		Details: &metav1.StatusDetails{Name: pool, Causes: []metav1.StatusCause{{Type: metav1.CauseType(cause)}}},
	}}
}

func isClaim(object client.Object) bool {
	return object.GetObjectKind().GroupVersionKind().Kind == GVK.Kind
}

func bind(t *testing.T, c client.Client, name, phase, address string) *unstructured.Unstructured {
	t.Helper()
	claim := &unstructured.Unstructured{}
	claim.SetGroupVersionKind(GVK)
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: namespace, Name: name}, claim); err != nil {
		t.Fatal(err)
	}
	claim.Object["status"] = map[string]any{"phase": phase, "address": address}
	if err := c.Update(context.Background(), claim); err != nil {
		t.Fatal(err)
	}
	return claim
}

// TestClaimIsCreatedOnceAndBoundBeforeUse creates one claim per operation
// and returns its address only once IPAM bound it to one IPv4 host.
func TestClaimIsCreatedOnceAndBoundBeforeUse(t *testing.T) {
	ctx := context.Background()
	c, api := claimer(t)
	if _, err := c.Claim(ctx, "alloc-a", "alloc-a-install-1"); !errors.Is(err, ErrPending) {
		t.Fatalf("unbound claim: %v", err)
	}
	claim := bind(t, api, "alloc-a-install-1-boot", "Bound", "198.51.100.10")
	input, err := c.Claim(ctx, "alloc-a", "alloc-a-install-1")
	if err != nil || input.Address != "198.51.100.10" || input.AddressClaim.Name != "alloc-a-install-1-boot" {
		t.Fatalf("input = %+v, %v", input, err)
	}
	if spec, _, _ := unstructured.NestedMap(claim.Object, "spec"); spec["className"] != "provisioning" || spec["ipFamily"] != "IPv4" || spec["reclaimPolicy"] != "Delete" {
		t.Fatalf("spec = %v", spec)
	}
	bind(t, api, "alloc-a-install-1-boot", "Bound", "2001:db8::10")
	if _, err := c.Claim(ctx, "alloc-a", "alloc-a-install-1"); err == nil {
		t.Fatal("an IPv6 boot address was accepted for DHCPv4")
	}
	// Another requester's claim under the same name is a conflict.
	wipes := c
	wipes.Kind = "DeviceWipe"
	if _, err := wipes.Claim(ctx, "alloc-a", "alloc-a-install-1"); !errors.Is(err, ErrConflict) {
		t.Fatalf("foreign claim: %v", err)
	}
	if _, err := wipes.Release(ctx, "alloc-a-install-1"); !errors.Is(err, ErrConflict) {
		t.Fatalf("a requester released another's claim: %v", err)
	}
}

// TestVerifyRequiresTheExactBoundIncarnation refuses a missing claim,
// a claim recreated after its operation, another address, and an unbound or
// deleting claim.
func TestVerifyRequiresTheExactBoundIncarnation(t *testing.T) {
	ctx := context.Background()
	c, api := claimer(t)
	if _, err := c.Claim(ctx, "alloc-a", "alloc-a-install-1"); !errors.Is(err, ErrPending) {
		t.Fatal(err)
	}
	claim := bind(t, api, "alloc-a-install-1-boot", "Bound", "198.51.100.10")
	created := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	stamp := func(at time.Time) {
		t.Helper()
		claim.SetCreationTimestamp(metav1.NewTime(at))
		if err := api.Update(ctx, claim); err != nil {
			t.Fatal(err)
		}
	}
	stamp(created)
	good := ops.BootNetworkInput{AddressClaim: Reference(claim.GetName()), Address: "198.51.100.10"}
	operation := &ops.DeviceInstallation{}
	operation.Name, operation.Namespace = "alloc-a-install-1", namespace
	operation.CreationTimestamp = metav1.NewTime(created.Add(time.Second))
	if err := Verify(ctx, api, operation, "DeviceInstallation", good); err != nil {
		t.Fatal(err)
	}
	// The claim must be the operation's own: by name and by its labels.
	other := &ops.DeviceInstallation{}
	other.Name, other.Namespace = "alloc-a-install-2", namespace
	if err := Verify(ctx, api, other, "DeviceInstallation", good); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("another operation's claim verified: %v", err)
	}
	if err := Verify(ctx, api, operation, "DeviceWipe", good); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("another kind's claim verified: %v", err)
	}
	for name, input := range map[string]ops.BootNetworkInput{
		"other name":     {AddressClaim: Reference("other-boot"), Address: good.Address},
		"other address":  {AddressClaim: good.AddressClaim, Address: "198.51.100.11"},
		"not an address": {AddressClaim: good.AddressClaim, Address: "198.51.100.10/24"},
	} {
		if err := Verify(ctx, api, operation, "DeviceInstallation", input); !errors.Is(err, ErrUnavailable) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// The claim deleted and recreated under its name after the operation
	// was created is not the claim the operation was built from, even with
	// the same address.
	stamp(created.Add(2 * time.Second))
	if err := Verify(ctx, api, operation, "DeviceInstallation", good); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("a claim recreated after its operation verified: %v", err)
	}
	stamp(created)
	unstamped := &ops.DeviceInstallation{}
	unstamped.Name, unstamped.Namespace = operation.Name, operation.Namespace
	if err := Verify(ctx, api, unstamped, "DeviceInstallation", good); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("an operation with no creation time verified: %v", err)
	}
	claim = bind(t, api, claim.GetName(), "Releasing", "198.51.100.10")
	if err := Verify(ctx, api, operation, "DeviceInstallation", good); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("unbound claim verified: %v", err)
	}
	if err := Verify(ctx, nil, operation, "DeviceInstallation", good); !errors.Is(err, ErrUnavailable) {
		t.Fatal("no reader verified")
	}
	if gone, err := c.Release(ctx, "alloc-a-install-1"); err != nil || !gone {
		t.Fatalf("release = %v, %v", gone, err)
	}
	if gone, err := c.Release(ctx, "alloc-a-install-1"); err != nil || !gone {
		t.Fatalf("second release = %v, %v", gone, err)
	}
}

// TestClaimFollowsIPAMsCreateContract finds the claim a racing create made,
// which IPAM reports as a ClaimExists conflict, and reports exhaustion and
// other refusals without leaving a claim behind.
func TestClaimFollowsIPAMsCreateContract(t *testing.T) {
	ctx := context.Background()
	var refusal error
	raced := false
	c, api := claimerWith(t, interceptor.Funcs{Create: func(ctx context.Context, api client.WithWatch, object client.Object, opts ...client.CreateOption) error {
		if !isClaim(object) {
			return api.Create(ctx, object, opts...)
		}
		if refusal != nil {
			return refusal
		}
		if raced {
			// Another reconcile created the claim first.
			if err := api.Create(ctx, object.DeepCopyObject().(client.Object), opts...); err != nil {
				return err
			}
			return ipamStatus(http.StatusConflict, metav1.StatusReasonConflict, "ClaimExists", "")
		}
		return api.Create(ctx, object, opts...)
	}})
	raced = true
	if _, err := c.Claim(ctx, "alloc-a", "alloc-a-install-1"); !errors.Is(err, ErrPending) {
		t.Fatalf("a create that lost the race: %v", err)
	}
	bind(t, api, "alloc-a-install-1-boot", "Bound", "198.51.100.10")
	if input, err := c.Claim(ctx, "alloc-a", "alloc-a-install-1"); err != nil || input.Address != "198.51.100.10" {
		t.Fatalf("input = %+v, %v", input, err)
	}
	raced = false
	for name, tc := range map[string]struct {
		err  error
		want error
		text string
	}{
		"exhausted pool":   {ipamStatus(http.StatusInsufficientStorage, "InsufficientStorage", "Exhausted", "lab-provisioning"), ErrExhausted, "pool lab-provisioning is full"},
		"exhausted parent": {ipamStatus(http.StatusInsufficientStorage, "InsufficientStorage", "Exhausted", ""), ErrExhausted, ""},
		"unknown class":    {ipamStatus(http.StatusBadRequest, metav1.StatusReasonBadRequest, "ClassNotFound", ""), ErrRefused, "ClassNotFound"},
		"retained name":    {ipamStatus(http.StatusConflict, metav1.StatusReasonConflict, "AllocationRetained", ""), ErrRefused, "AllocationRetained"},
	} {
		refusal = tc.err
		operation := "alloc-a-install-" + strings.ReplaceAll(name, " ", "-")
		_, err := c.Claim(ctx, "alloc-a", operation)
		if !errors.Is(err, tc.want) || !strings.Contains(err.Error(), tc.text) {
			t.Errorf("%s: %v", name, err)
		}
		claim := &unstructured.Unstructured{}
		claim.SetGroupVersionKind(GVK)
		if err := api.Get(ctx, client.ObjectKey{Namespace: namespace, Name: ClaimName(operation)}, claim); !apierrors.IsNotFound(err) {
			t.Errorf("%s: a refused claim exists: %v", name, err)
		}
	}
	// An outage is neither a refusal nor an exhaustion.
	refusal = apierrors.NewServiceUnavailable("ipam is down")
	if _, err := c.Claim(ctx, "alloc-a", "alloc-a-install-down"); err == nil || errors.Is(err, ErrExhausted) || errors.Is(err, ErrRefused) {
		t.Fatalf("an outage: %v", err)
	}
}

// TestReleaseIsVerifiedAndAnUnknownOutcomeKeepsTheAddressHeld returns true
// only when the claim is gone and no allocation names it. A delete that
// fails, or whose response is lost, returns false, and a later call settles
// it.
func TestReleaseIsVerifiedAndAnUnknownOutcomeKeepsTheAddressHeld(t *testing.T) {
	ctx := context.Background()
	mode := ""
	c, api := claimerWith(t, interceptor.Funcs{Delete: func(ctx context.Context, api client.WithWatch, object client.Object, opts ...client.DeleteOption) error {
		switch mode {
		case "accepted":
			// A delete that succeeds while the claim is still there is not a
			// release.
			return nil
		case "fails":
			// IPAM could not release, and the claim stays Releasing.
			return apierrors.NewInternalError(errors.New("release allocation after 3 attempts"))
		case "lost":
			// IPAM released it, but the response never arrived.
			if err := api.Delete(ctx, object, opts...); err != nil {
				return err
			}
			return apierrors.NewTimeoutError("the response was lost", 1)
		}
		return api.Delete(ctx, object, opts...)
	}})
	claim := func(operation string) {
		t.Helper()
		if _, err := c.Claim(ctx, "alloc-a", operation); !errors.Is(err, ErrPending) {
			t.Fatal(err)
		}
		bind(t, api, ClaimName(operation), "Bound", "198.51.100.10")
	}
	exists := func(operation string) bool {
		t.Helper()
		claim := &unstructured.Unstructured{}
		claim.SetGroupVersionKind(GVK)
		err := api.Get(ctx, client.ObjectKey{Namespace: namespace, Name: ClaimName(operation)}, claim)
		if err != nil && !apierrors.IsNotFound(err) {
			t.Fatal(err)
		}
		return err == nil
	}

	claim("alloc-a-install-1")
	mode = "fails"
	if gone, err := c.Release(ctx, "alloc-a-install-1"); err == nil || gone || !exists("alloc-a-install-1") {
		t.Fatalf("a failed release = %v, %v", gone, err)
	}
	mode = "accepted"
	if gone, err := c.Release(ctx, "alloc-a-install-1"); err != nil || gone || !exists("alloc-a-install-1") {
		t.Fatalf("a release that left the claim = %v, %v", gone, err)
	}
	mode = ""
	if gone, err := c.Release(ctx, "alloc-a-install-1"); err != nil || !gone {
		t.Fatalf("the retried release = %v, %v", gone, err)
	}

	claim("alloc-a-install-2")
	mode = "lost"
	if gone, err := c.Release(ctx, "alloc-a-install-2"); err == nil || gone {
		t.Fatalf("a release with a lost response = %v, %v", gone, err)
	}
	mode = ""
	if gone, err := c.Release(ctx, "alloc-a-install-2"); err != nil || !gone {
		t.Fatalf("the release read after a lost response = %v, %v", gone, err)
	}

	// The claim is gone, but IPAM still records an allocation for it.
	allocation := &unstructured.Unstructured{}
	allocation.SetGroupVersionKind(AllocationGVK)
	allocation.SetNamespace(namespace)
	allocation.SetName("alloc-0123456789abcdef")
	allocation.Object["spec"] = map[string]any{"claimRef": map[string]any{"name": ClaimName("alloc-a-install-3")}}
	if err := api.Create(ctx, allocation); err != nil {
		t.Fatal(err)
	}
	if gone, err := c.Release(ctx, "alloc-a-install-3"); !errors.Is(err, ErrReleaseUnverified) || gone {
		t.Fatalf("a release with a remaining allocation = %v, %v", gone, err)
	}
}

// TestVerifyWaitsWhenIPAMCannotBeRead tells an outage, which admission
// waits out, from a missing claim, which it refuses.
func TestVerifyWaitsWhenIPAMCannotBeRead(t *testing.T) {
	ctx := context.Background()
	down := false
	_, api := claimerWith(t, interceptor.Funcs{Get: func(ctx context.Context, api client.WithWatch, key client.ObjectKey, object client.Object, opts ...client.GetOption) error {
		if down && isClaim(object) {
			return apierrors.NewServiceUnavailable("ipam is down")
		}
		return api.Get(ctx, key, object, opts...)
	}})
	operation := &ops.DeviceInstallation{}
	operation.Name, operation.Namespace = "alloc-a-install-1", namespace
	operation.CreationTimestamp = metav1.NewTime(time.Now().Add(time.Hour))
	input := ops.BootNetworkInput{AddressClaim: Reference(ClaimName(operation.Name)), Address: "198.51.100.10"}
	if err := Verify(ctx, api, operation, "DeviceInstallation", input); !errors.Is(err, ErrUnavailable) || errors.Is(err, ErrUnreachable) {
		t.Fatalf("a missing claim: %v", err)
	}
	down = true
	if err := Verify(ctx, api, operation, "DeviceInstallation", input); !errors.Is(err, ErrUnreachable) || errors.Is(err, ErrUnavailable) {
		t.Fatalf("an outage: %v", err)
	}
}

// TestARecreatedMachineInheritsNoClaim gives a recreated Machine, whose
// allocation is named from its new identity, a claim of its own, and leaves
// the earlier allocation's claim to that allocation's release.
func TestARecreatedMachineInheritsNoClaim(t *testing.T) {
	ctx := context.Background()
	c, api := claimer(t)
	if _, err := c.Claim(ctx, "alloc-old", "alloc-old-install-1"); !errors.Is(err, ErrPending) {
		t.Fatal(err)
	}
	bind(t, api, "alloc-old-install-1-boot", "Bound", "198.51.100.10")
	if _, err := c.Claim(ctx, "alloc-new", "alloc-new-install-1"); !errors.Is(err, ErrPending) {
		t.Fatalf("the recreated Machine's claim: %v", err)
	}
	if operations, err := c.Operations(ctx, "alloc-new"); err != nil || len(operations) != 1 || operations[0] != "alloc-new-install-1" {
		t.Fatalf("operations = %v, %v", operations, err)
	}
	// Reusing the earlier operation's name from the new allocation is a
	// conflict, not an inherited address.
	if _, err := c.Claim(ctx, "alloc-new", "alloc-old-install-1"); !errors.Is(err, ErrConflict) {
		t.Fatalf("the earlier claim was inherited: %v", err)
	}
}

// TestARetainingClaimIsNeverUsed refuses a claim IPAM stored with any reclaim
// policy but Delete, whose address would outlive the claim, both to its
// requester and at admission.
func TestARetainingClaimIsNeverUsed(t *testing.T) {
	ctx := context.Background()
	c, api := claimer(t)
	if _, err := c.Claim(ctx, "alloc-a", "alloc-a-install-1"); !errors.Is(err, ErrPending) {
		t.Fatal(err)
	}
	claim := bind(t, api, "alloc-a-install-1-boot", "Bound", "198.51.100.10")
	created := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	claim.SetCreationTimestamp(metav1.NewTime(created))
	if err := api.Update(ctx, claim); err != nil {
		t.Fatal(err)
	}
	operation := &ops.DeviceInstallation{}
	operation.Name, operation.Namespace = "alloc-a-install-1", namespace
	operation.CreationTimestamp = metav1.NewTime(created.Add(time.Second))
	input := ops.BootNetworkInput{AddressClaim: Reference(claim.GetName()), Address: "198.51.100.10"}
	if err := Verify(ctx, api, operation, "DeviceInstallation", input); err != nil {
		t.Fatal(err)
	}
	if err := unstructured.SetNestedField(claim.Object, "Retain", "spec", "reclaimPolicy"); err != nil {
		t.Fatal(err)
	}
	if err := api.Update(ctx, claim); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Claim(ctx, "alloc-a", "alloc-a-install-1"); !errors.Is(err, ErrConflict) {
		t.Fatalf("a retaining claim was used: %v", err)
	}
	if err := Verify(ctx, api, operation, "DeviceInstallation", input); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("a retaining claim verified: %v", err)
	}
}
