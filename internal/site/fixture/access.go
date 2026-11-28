// Package fixture is how a site reaches synthetic Devices that have no BMC,
// such as the simulator's and the local VM's: an enrollment of each Device's
// identity, an access type that checks the admitted snapshot against it, and
// fixture discovery. It is never a production access type.
package fixture

import (
	"context"
	"strings"

	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	"go.datum.net/fleet/internal/custody"
	"go.datum.net/fleet/internal/site"
)

// Target is one enrolled synthetic Device: the identity its fixture Device
// records, the key digest its fixture discovery records, and its disks.
type Target struct {
	BMCEndpoint        string   `json:"bmcEndpoint"`
	SystemUUID         string   `json:"systemUUID"`
	BootMACAddress     string   `json:"bootMACAddress"`
	BMCPublicKeySHA256 string   `json:"bmcPublicKeySHA256"`
	Disks              []string `json:"disks"`
	// Hardware is what a simulated commissioning inspects. A target without
	// it cannot be commissioned.
	Hardware *ops.InspectedHardware `json:"hardware,omitempty"`
	// Firmware is the firmware a simulated commissioning reads.
	Firmware []ops.FirmwareBaseline `json:"firmware,omitempty"`
}

// Enrollment maps "<namespace>/<name>" to each Device the site serves.
type Enrollment map[string]Target

// Connection is what Access prepares: the enrolled target.
type Connection struct {
	Device string
	Target Target
}

// Access checks the admitted snapshot against the site's enrollment, as
// RedfishAccess does against a real BMC. A synthetic Device has no
// credential; its pin is the enrolled key digest.
type Access struct{ Enrollment Enrollment }

var _ site.Access = Access{}

func (a Access) Prepare(_ context.Context, assignment site.Assignment) (any, *custody.Refusal) {
	target, ok := a.Enrollment[assignment.Device]
	if !ok {
		return nil, &custody.Refusal{Reason: site.ReasonNotEnrolled, Message: "this site does not serve the Device"}
	}
	snapshot := assignment.Snapshot
	if !strings.EqualFold(target.SystemUUID, snapshot.SystemUUID) || target.BMCEndpoint != snapshot.BMCEndpoint || (snapshot.BootMACAddress != "" && !strings.EqualFold(target.BootMACAddress, snapshot.BootMACAddress)) {
		return nil, &custody.Refusal{Reason: custody.ReasonDeviceIdentityMismatch, Message: "the site's enrollment does not match the admitted snapshot"}
	}
	if target.BMCPublicKeySHA256 != snapshot.BMCPublicKeySHA256 {
		return nil, &custody.Refusal{Reason: site.ReasonPublicKeyMismatch, Message: "the enrolled key differs from the key discovery recorded at admission"}
	}
	return &Connection{Device: assignment.Device, Target: target}, nil
}
