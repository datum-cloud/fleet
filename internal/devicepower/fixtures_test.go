package devicepower

import (
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	inventory "go.datum.net/fleet/internal/api/inventory/v1alpha1"
	metal "go.datum.net/fleet/internal/api/v1alpha1"
)

// Synthetic identifiers only.
const (
	project  = "example-provider-project"
	deviceNS = "default"
	siteNS   = "fleet-device-power"
	uuidA    = "00000000-0000-4000-8000-0000000000a1"
	uuidB    = "00000000-0000-4000-8000-0000000000b2"
	endpoint = "https://192.0.2.10:443"
)

var (
	t0   = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	keyA = strings.Repeat("a", 64)
)

// discoveredDevice is a Device that discovery read recently and matched.
func discoveredDevice(name string) *inventory.Device {
	at := metav1.NewTime(t0.Add(-10 * time.Minute))
	cond := func(kind, reason string) metav1.Condition {
		return metav1.Condition{Type: kind, Status: metav1.ConditionTrue, Reason: reason, ObservedGeneration: 1, LastTransitionTime: at}
	}
	return &inventory.Device{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: deviceNS, Generation: 1, UID: types.UID("device-" + name)},
		Spec: inventory.DeviceSpec{
			ClaimRef: inventory.LocalReference{Name: "claim-" + name},
			Identity: inventory.DeviceIdentity{Model: "Example 1U", SystemUUID: uuidA},
			BMC:      &inventory.BMC{Endpoint: endpoint},
		},
		Status: inventory.DeviceStatus{
			ObservedGeneration: 1,
			Discovery: &inventory.DeviceDiscovery{
				LastAttemptAt: &at, LastAttemptEndpoint: endpoint,
				TLS: &inventory.ObservedTLS{ObservedAt: at, Endpoint: endpoint, PublicKeySHA256: keyA},
				Inventory: &inventory.ObservedInventory{ObservedAt: at, Endpoint: endpoint, Systems: []inventory.ObservedSystem{
					{ID: "1", UUID: uuidA, Model: "Example 1U", PowerState: "Off", ResetTypes: []string{"On", "ForceOff", "ForceRestart"}},
				}},
			},
			Conditions: []metav1.Condition{
				cond(inventory.ConditionReachable, "ServiceRootRead"),
				cond(inventory.ConditionAuthenticated, "CredentialAccepted"),
				cond(inventory.ConditionDiscovered, "InventoryRead"),
				cond(inventory.ConditionIdentityMatched, "Matched"),
				cond(inventory.ConditionPublicKeyUnique, "Unique"),
			},
		},
	}
}

func operation(name, device string, action inventory.PowerAction) *inventory.DevicePowerOperation {
	return &inventory.DevicePowerOperation{
		TypeMeta:   metav1.TypeMeta{APIVersion: inventory.GroupVersion.String(), Kind: "DevicePowerOperation"},
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: deviceNS, UID: types.UID("00000000-0000-4000-8000-00000000" + uidSuffix(name)), Generation: 1, CreationTimestamp: metav1.NewTime(t0.Add(-time.Minute))},
		Spec: inventory.DevicePowerOperationSpec{DeviceRef: inventory.LocalReference{Name: device}, Action: action, ExpectedSystemUUID: uuidA,
			Requester: "staff@example.com", Reason: "synthetic test"},
	}
}

// uidSuffix makes a stable four-hex-digit suffix per name.
func uidSuffix(name string) string {
	var h uint16
	for _, r := range name {
		h = h*31 + uint16(r)
	}
	const hex = "0123456789abcdef"
	return string([]byte{hex[h>>12], hex[(h>>8)&15], hex[(h>>4)&15], hex[h&15]})
}

func capacity(name, device string) *metal.MachineCapacity {
	return &metal.MachineCapacity{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: deviceNS},
		Spec: metal.MachineCapacitySpec{DeviceRef: metal.LocalReference{Name: device}}}
}
