package allocation

import (
	"context"
	"errors"
	"fmt"
	"slices"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/bootaddress"
)

var (
	// ErrBootAttached means a boot attachment claim holds a boot NIC that
	// also carries the Machine's networks, so the network owner keeps the
	// port on the boot network and off the customer's networks.
	ErrBootAttached = errors.New("the boot NIC is on the boot network for an installation or wipe")
	// ErrBootAttachmentHeld means the claim holding the boot NIC belongs to
	// an operation custody holds, so it stays until the hold is settled.
	ErrBootAttachmentHeld = errors.New("the boot NIC stays on the boot network while its operation is held")
)

// attaches is a boot address source that moves boot NICs onto the boot
// network itself. Only such a source takes a shared port off the customer's
// networks.
type attaches interface {
	bootaddress.Source
	AttachesBootNIC()
}

// HandOver releases the boot claims of the allocation's ended, unheld
// operations while the allocation is active, under the same rules as
// release, so the network owner can move a shared boot NIC to the
// customer's networks. Fleet writes only claims; the network owner moves the
// port in one step once the boot attachment claim is gone. It reports
// whether no boot claim remains.
func (a *Allocator) HandOver(ctx context.Context, allocation *fleetv1.MachineAllocation) (bool, error) {
	if allocation.Status.Phase != fleetv1.AllocationActive {
		return false, ErrReleased
	}
	return a.releaseBootAddresses(ctx, allocation, false)
}

// BootNIC reports whether a boot attachment claim holds the allocation's boot
// NIC while it is one of the Machine's interfaces. It returns ErrBootAttached
// or ErrBootAttachmentHeld while one does, and nil otherwise, including for
// a source that does not attach boot NICs and for a dedicated boot NIC.
func (a *Allocator) BootNIC(ctx context.Context, allocation *fleetv1.MachineAllocation, interfaces []string) error {
	if _, ok := a.BootAddresses.(attaches); !ok || !slices.Contains(interfaces, allocation.Spec.BootInterface) {
		return nil
	}
	var holding error
	for _, source := range []bootaddress.Source{a.installClaims(), a.wipeClaims()} {
		operations, err := source.Operations(ctx, allocation.Name)
		if err != nil {
			return err
		}
		for _, operation := range operations {
			held, err := a.held(ctx, allocation, operation)
			if err != nil {
				return err
			}
			if held {
				return fmt.Errorf("%w: %s", ErrBootAttachmentHeld, operation)
			}
			holding = fmt.Errorf("%w: %s", ErrBootAttached, operation)
		}
	}
	return holding
}

// BootNICCondition is the Machine's NetworkReady condition while BootNIC
// reports a boot attachment, or nil when it reports none.
func BootNICCondition(machine *fleetv1.Machine, err error) *metav1.Condition {
	condition := &metav1.Condition{Type: "NetworkReady", Status: metav1.ConditionFalse, ObservedGeneration: machine.Generation, LastTransitionTime: metav1.Now()}
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrBootAttachmentHeld):
		condition.Reason, condition.Message = "RecoveryHold", "An installation or wipe with an unknown outcome is held, so its boot NIC stays on the boot network until the hold is settled."
	case errors.Is(err, ErrBootAttached):
		condition.Reason, condition.Message = "BootNetworkAttached", "The boot NIC is on the boot network for an installation or wipe."
	default:
		condition.Status, condition.Reason, condition.Message = metav1.ConditionUnknown, "NetworkUnavailable", "The boot attachment could not be read."
	}
	return condition
}
