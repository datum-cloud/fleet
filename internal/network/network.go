// Package network attaches an allocated server's interfaces to its
// customer's networks. Fleet names each interface's physical attachment
// point from inventory and asks the network owner to attach it; it never
// configures a switch.
package network

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	inventory "go.datum.net/fleet/internal/api/inventory/v1alpha1"
	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/bootnet"
)

var (
	// ErrUnsupported means no physical attachment is available.
	ErrUnsupported = errors.New("physical network attachment is not available")
	// ErrUnauthorized means the Machine's creator may not use a Network,
	// or the check could not be made.
	ErrUnauthorized = errors.New("network use is not authorized")
	// ErrNotReady means a Network is missing, not ready, or does not carry
	// a requested address family.
	ErrNotReady = errors.New("the network is not ready for this request")
	// ErrAttachmentUnknown means inventory cannot say where an interface is
	// cabled.
	ErrAttachmentUnknown = errors.New("the interface's attachment point is unknown")
	// ErrConflict means an interface claim under Fleet's name is not the one
	// Fleet created.
	ErrConflict = errors.New("an interface claim conflicts with the allocation's record")
)

// LocationAuthority is the exact Location lifetime and service availability
// a request was admitted against.
type LocationAuthority struct {
	Name     string    `json:"name"`
	UID      types.UID `json:"uid"`
	Revision string    `json:"revision"`
}

func (a LocationAuthority) Valid() bool {
	return a.Name != "" && a.UID != "" && a.Revision != ""
}

// Locate returns where a Device interface meets the network: its MAC from
// Device inventory, and the switch and port at the other end of the one
// cable record that lands on it. Cable endpoints name the interface as the
// Device's component.
func Locate(ctx context.Context, reader client.Reader, device *inventory.Device, iface string) (fleetv1.AttachmentPoint, error) {
	var mac string
	for _, candidate := range device.Spec.Interfaces {
		if candidate.Name == iface {
			mac = candidate.MACAddress
		}
	}
	normalized, ok := bootnet.NormalizeMAC(strings.ReplaceAll(mac, "-", ":"))
	if !ok {
		return fleetv1.AttachmentPoint{}, fmt.Errorf("%w: the Device records no MAC for %q", ErrAttachmentUnknown, iface)
	}
	var cables inventory.CableList
	if err := reader.List(ctx, &cables, client.InNamespace(device.Namespace)); err != nil {
		return fleetv1.AttachmentPoint{}, err
	}
	var found []fleetv1.AttachmentPoint
	for _, cable := range cables.Items {
		for _, lane := range cable.Spec.Lanes {
			for _, ends := range [][2]inventory.CableEndpoint{{lane.A, lane.B}, {lane.B, lane.A}} {
				here, there := ends[0], ends[1]
				if here.DeviceRef != nil && here.DeviceRef.Name == device.Name && here.ComponentID == iface && there.DeviceRef != nil && there.ComponentID != "" {
					found = append(found, fleetv1.AttachmentPoint{MACAddress: normalized, Switch: there.DeviceRef.Name, Port: there.ComponentID})
				}
			}
		}
	}
	if len(found) != 1 {
		return fleetv1.AttachmentPoint{}, fmt.Errorf("%w: %d cable records land on %q", ErrAttachmentUnknown, len(found), iface)
	}
	return found[0], nil
}
