package allocation

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/bootaddress"
	"go.datum.net/fleet/internal/custody"
)

// TestAFullBootClaimRecordRefusesTheNextClaim fills the allocation's boot
// claim record. The next installation is refused with ErrBootClaimsFull, no
// claim is made for it, and every recorded entry is kept.
func TestAFullBootClaimRecordRefusesTheNextClaim(t *testing.T) {
	e := startEnv(t)
	e.allocator.Provider = &ipam{Client: e.admin}
	e.allocator.BootAddresses = bootaddress.Claimer{Class: "provisioning"}
	e.server(1)
	allocation, err := e.allocator.Allocate(e.ctx, e.request(1))
	if err != nil {
		t.Fatal(err)
	}
	for i := range MaxBootClaims {
		operation := fmt.Sprintf("%s-install-held-%d", allocation.Name, i)
		allocation.Status.BootClaims = append(allocation.Status.BootClaims, fleetv1.BootClaimRecord{
			OperationKind: "DeviceInstallation", Operation: operation, Claim: bootaddress.Reference(bootaddress.ClaimName(operation)),
		})
	}
	if err := e.admin.Status().Update(e.ctx, allocation); err != nil {
		t.Fatal(err)
	}
	full := slices.Clone(allocation.Status.BootClaims)

	installation := e.installation(1, "debian-12", "")
	if _, err := e.allocator.Install(e.ctx, allocation, Installation{Installation: installation}); !errors.Is(err, ErrBootClaimsFull) {
		t.Fatalf("install with a full record: %v", err)
	}
	name := custody.InstallationName(allocation.Name, string(installation.UID))
	claim := &unstructured.Unstructured{}
	claim.SetGroupVersionKind(bootaddress.GVK)
	if err := e.admin.Get(e.ctx, client.ObjectKey{Namespace: e.allocator.Namespace, Name: bootaddress.ClaimName(name)}, claim); !apierrors.IsNotFound(err) {
		t.Fatalf("a boot claim was made with a full record: %v", err)
	}
	if err := e.admin.Get(e.ctx, client.ObjectKey{Namespace: e.allocator.Namespace, Name: name}, &ops.DeviceInstallation{}); !apierrors.IsNotFound(err) {
		t.Fatalf("an installation was created with a full record: %v", err)
	}
	stored := &fleetv1.MachineAllocation{}
	if err := e.admin.Get(e.ctx, client.ObjectKeyFromObject(allocation), stored); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(stored.Status.BootClaims, full) {
		t.Fatalf("the record changed: %d entries, want the %d unverified ones", len(stored.Status.BootClaims), len(full))
	}
}

// noStatus fails every status write, as a crash before the write would.
type noStatus struct{ client.Client }

func (c noStatus) Status() client.SubResourceWriter { return failingStatus{c.Client.Status()} }

type failingStatus struct{ client.SubResourceWriter }

func (failingStatus) Update(context.Context, client.Object, ...client.SubResourceUpdateOption) error {
	return errors.New("the status write did not happen")
}

// TestTheRecordIsWrittenBeforeTheClaim makes no boot claim when the record
// cannot be written, so a crash between the two never leaves a claim the
// record does not name.
func TestTheRecordIsWrittenBeforeTheClaim(t *testing.T) {
	e := startEnv(t)
	e.allocator.BootAddresses = bootaddress.Claimer{Class: "provisioning"}
	e.server(1)
	allocation, err := e.allocator.Allocate(e.ctx, e.request(1))
	if err != nil {
		t.Fatal(err)
	}
	e.allocator.Provider = noStatus{&ipam{Client: e.admin}}
	installation := e.installation(1, "debian-12", "")
	if _, err := e.allocator.Install(e.ctx, allocation, Installation{Installation: installation}); err == nil {
		t.Fatal("installed without recording its boot claim")
	}
	name := custody.InstallationName(allocation.Name, string(installation.UID))
	claim := &unstructured.Unstructured{}
	claim.SetGroupVersionKind(bootaddress.GVK)
	if err := e.admin.Get(e.ctx, client.ObjectKey{Namespace: e.allocator.Namespace, Name: bootaddress.ClaimName(name)}, claim); !apierrors.IsNotFound(err) {
		t.Fatalf("a boot claim was made before its record: %v", err)
	}
}
