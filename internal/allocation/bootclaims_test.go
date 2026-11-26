package allocation

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	networkingv1 "go.datum.net/network-services-operator/api/v1alpha"

	inventory "go.datum.net/fleet/internal/api/inventory/v1alpha1"
	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/bootaddress"
	"go.datum.net/fleet/internal/custody"
	"go.datum.net/fleet/internal/network/nso"
)

// TestAFullBootClaimRecordRefusesTheNextClaim fills the allocation's boot
// claim record with NSO claims, the source that records. The next
// installation is refused with ErrBootClaimsFull, no claim is made for it,
// and every recorded entry is kept.
func TestAFullBootClaimRecordRefusesTheNextClaim(t *testing.T) {
	e, _ := nsoEnv(t)
	allocation, err := e.allocator.Allocate(e.ctx, e.request(1))
	if err != nil {
		t.Fatal(err)
	}
	for i := range MaxBootClaims {
		operation := fmt.Sprintf("%s-install-held-%d", allocation.Name, i)
		allocation.Status.BootClaims = append(allocation.Status.BootClaims, fleetv1.BootClaimRecord{
			OperationKind: "DeviceInstallation", Operation: operation, Claim: nso.BootReference(bootaddress.ClaimName(operation)),
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
	if err := e.admin.Get(e.ctx, client.ObjectKey{Namespace: e.allocator.Namespace, Name: bootaddress.ClaimName(name)}, &networkingv1.NetworkInterfaceClaim{}); !apierrors.IsNotFound(err) {
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
	e, _ := nsoEnv(t)
	allocation, err := e.allocator.Allocate(e.ctx, e.request(1))
	if err != nil {
		t.Fatal(err)
	}
	e.allocator.Provider = noStatus{e.admin}
	installation := e.installation(1, "debian-12", "")
	if _, err := e.allocator.Install(e.ctx, allocation, Installation{Installation: installation}); err == nil {
		t.Fatal("installed without recording its boot claim")
	}
	name := custody.InstallationName(allocation.Name, string(installation.UID))
	if err := e.admin.Get(e.ctx, client.ObjectKey{Namespace: e.allocator.Namespace, Name: bootaddress.ClaimName(name)}, &networkingv1.NetworkInterfaceClaim{}); !apierrors.IsNotFound(err) {
		t.Fatalf("a boot claim was made before its record: %v", err)
	}
}

// TestIPAMClaimsAreNotRecorded runs installations on one allocation through
// IPAM with the record already full of other entries. IPAM removes a claim
// and its address together, so its claims are never recorded, the record
// never refuses IPAM's work, and it is left as it was.
func TestIPAMClaimsAreNotRecorded(t *testing.T) {
	e, _ := nsoEnv(t)
	e.allocator.Provider = &ipam{Client: e.admin}
	e.allocator.BootAddresses = bootaddress.Claimer{Class: "provisioning"}
	allocation, err := e.allocator.Allocate(e.ctx, e.request(1))
	if err != nil {
		t.Fatal(err)
	}
	for i := range MaxBootClaims {
		operation := fmt.Sprintf("%s-install-other-%d", allocation.Name, i)
		allocation.Status.BootClaims = append(allocation.Status.BootClaims, fleetv1.BootClaimRecord{
			OperationKind: "DeviceInstallation", Operation: operation, Claim: nso.BootReference(bootaddress.ClaimName(operation)),
		})
	}
	if err := e.admin.Status().Update(e.ctx, allocation); err != nil {
		t.Fatal(err)
	}
	full := slices.Clone(allocation.Status.BootClaims)
	previous := ""
	for n, name := range []string{"debian-12", "debian-12-second", "debian-12-third"} {
		request := Installation{Installation: e.installation(n+1, name, previous), Current: previous}
		operation := e.installed(allocation, request)
		if claim := operation.Spec.BootNetwork.AddressClaim; claim.Kind != bootaddress.GVK.Kind {
			t.Fatalf("installation %d claimed %+v, want IPAM's", n, claim)
		}
		previous = name
	}
	stored := &fleetv1.MachineAllocation{}
	if err := e.admin.Get(e.ctx, client.ObjectKeyFromObject(allocation), stored); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(stored.Status.BootClaims, full) {
		t.Fatalf("IPAM installs changed the record: %d entries, want %d", len(stored.Status.BootClaims), len(full))
	}
}

// TestARecordWithNoClaimIsCleared records an NSO boot claim whose attach
// fails before any claim is made, here because inventory has no cable for
// the boot NIC. With neither a claim nor an operation, the record is
// verified and cleared by the next release pass instead of keeping it open.
func TestARecordWithNoClaimIsCleared(t *testing.T) {
	e, _ := nsoEnv(t)
	if err := e.admin.Delete(e.ctx, &inventory.Cable{ObjectMeta: metav1.ObjectMeta{Name: "cable-example-1", Namespace: namespace}}); err != nil {
		t.Fatal(err)
	}
	allocation, err := e.allocator.Allocate(e.ctx, e.request(1))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.allocator.Install(e.ctx, allocation, Installation{Installation: e.installation(1, "debian-12", "")}); !errors.Is(err, nso.ErrAttachmentUnknown) {
		t.Fatalf("install without a cable record: %v", err)
	}
	if len(allocation.Status.BootClaims) != 1 {
		t.Fatalf("record = %+v, want the failed attach recorded", allocation.Status.BootClaims)
	}
	if none, err := e.allocator.HandOver(e.ctx, allocation); err != nil || !none || len(allocation.Status.BootClaims) != 0 {
		t.Fatalf("hand over = none %v, %v, record %+v; want the empty record cleared", none, err, allocation.Status.BootClaims)
	}
}
