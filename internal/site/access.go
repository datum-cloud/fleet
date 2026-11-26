package site

import (
	"context"
	"errors"
	"strings"
	"time"

	"go.datum.net/fleet/internal/bmcdiscovery"
	"go.datum.net/fleet/internal/custody"
	"go.datum.net/fleet/internal/redfish"
)

// Access is the framework's check that it is safe to reach a Device's
// server, and the connection a kind then uses. It reads the credential, the
// enrollment and the server's identity, and changes nothing. Adding an access
// type, such as the local VM's, does not change the Kind contract.
type Access interface {
	Prepare(ctx context.Context, assignment Assignment) (any, *custody.Refusal)
}

// Credentials looks up a Device's BMC credential at the site.
type Credentials interface {
	Lookup(ctx context.Context, device string) (bmcdiscovery.Credential, bool, error)
}

// Targets is the site's enrollment: the Redfish System and boot NIC of each
// Device it serves. It is a site-local fact, checked against the snapshot.
type Targets interface {
	Target(device string) (redfish.Target, bool)
}

// RedfishConnection is what RedfishAccess prepares: the enrolled target, the
// System whose identity it verified, and a client pinned to the approved
// key. The framework checks identity only; each kind checks the capabilities
// it uses, such as boot targets or reset types, on System.
type RedfishConnection struct {
	Target redfish.Target
	System redfish.System
	BMC    *redfish.Client
}

// Observe reads the verified System's power and boot override.
func (c *RedfishConnection) Observe(ctx context.Context) (redfish.Power, error) {
	return c.BMC.Observe(ctx, c.System)
}

// Reset posts one ComputerSystem.Reset to the verified System.
func (c *RedfishConnection) Reset(ctx context.Context, kind string) error {
	return c.BMC.Reset(ctx, c.System, kind)
}

// SetBootOnce arms one boot override on the verified System.
func (c *RedfishConnection) SetBootOnce(ctx context.Context, target string) error {
	return c.BMC.SetBootOnce(ctx, c.System, target)
}

// RedfishAccess reaches a server through its BMC. It checks the site's
// enrollment against the admitted snapshot, reads the per-Device credential,
// requires the operator-approved pin to equal the key discovery recorded at
// admission, and reads the BMC only over a connection pinned to that key.
type RedfishAccess struct {
	Credentials Credentials
	Targets     Targets
	// Dial opens a client pinned to the approved key. Tests replace it.
	Dial func(endpoint, username, password, pin string) (*redfish.Client, error)
}

func (a RedfishAccess) Prepare(ctx context.Context, assignment Assignment) (any, *custody.Refusal) {
	target, ok := a.Targets.Target(assignment.Device)
	if !ok {
		return nil, &custody.Refusal{Reason: ReasonNotEnrolled, Message: "this site does not serve the Device"}
	}
	snapshot := assignment.Snapshot
	if !strings.EqualFold(target.SystemUUID, snapshot.SystemUUID) || target.Endpoint != snapshot.BMCEndpoint || (snapshot.BootMACAddress != "" && !strings.EqualFold(target.MAC, snapshot.BootMACAddress)) {
		return nil, &custody.Refusal{Reason: custody.ReasonDeviceIdentityMismatch, Message: "the site's enrollment does not match the admitted snapshot"}
	}
	credential, found, err := a.Credentials.Lookup(ctx, assignment.Device)
	switch {
	case err != nil || !found:
		return nil, &custody.Refusal{Reason: ReasonCredentialMissing, Message: "no usable BMC credential is on record for the Device"}
	case credential.Pin == "":
		return nil, &custody.Refusal{Reason: ReasonPinNotApproved, Message: "no operator has approved the BMC's key"}
	case credential.Pin != snapshot.BMCPublicKeySHA256:
		return nil, &custody.Refusal{Reason: ReasonPublicKeyMismatch, Message: "the approved key differs from the key discovery recorded at admission"}
	}
	dial := a.Dial
	if dial == nil {
		dial = func(endpoint, username, password, pin string) (*redfish.Client, error) {
			return redfish.NewPinnedClient(endpoint, username, password, pin, 30*time.Second)
		}
	}
	client, err := dial(target.Endpoint, credential.Username, credential.Password, credential.Pin)
	if err != nil {
		return nil, &custody.Refusal{Reason: ReasonBMCUnavailable, Message: "a pinned BMC client could not be built"}
	}
	system, err := client.Identify(ctx, target)
	switch {
	case errors.Is(err, redfish.ErrPinMismatch):
		return nil, &custody.Refusal{Reason: ReasonPublicKeyMismatch, Message: "the BMC presented a key other than the approved one"}
	case errors.Is(err, redfish.ErrIdentity):
		return nil, &custody.Refusal{Reason: custody.ReasonDeviceIdentityMismatch, Message: "the BMC reports another system"}
	case err != nil:
		return nil, &custody.Refusal{Reason: ReasonBMCUnavailable, Message: "the BMC could not be read"}
	}
	return &RedfishConnection{Target: target, System: system, BMC: client}, nil
}
