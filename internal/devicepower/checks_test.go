package devicepower

import (
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	inventory "go.datum.net/fleet/internal/api/inventory/v1alpha1"
	metal "go.datum.net/fleet/internal/api/v1alpha1"
)

func TestCheckRefusesEachUnsafeRequest(t *testing.T) {
	base := func() Acceptance {
		return Acceptance{Operation: operation("op", "device-a", inventory.PowerForceRestart), Device: discoveredDevice("device-a"),
			DiscoveryMaxAge: time.Hour, Now: t0}
	}
	if r := Check(base()); r != nil {
		t.Fatalf("a safe request was refused: %+v", r)
	}
	for _, tc := range []struct {
		want   string
		mutate func(*Acceptance)
	}{
		{inventory.ReasonExpired, func(a *Acceptance) { a.Operation.CreationTimestamp = metav1.NewTime(t0.Add(-6 * time.Minute)) }},
		{inventory.ReasonDeviceNotFound, func(a *Acceptance) { a.Device = nil }},
		{inventory.ReasonDeviceRetired, func(a *Acceptance) { a.Device.Spec.Lifecycle = inventory.DeviceRetired }},
		{inventory.ReasonCapacityBacked, func(a *Acceptance) { a.Capacities = []metal.MachineCapacity{*capacity("cap", "device-a")} }},
		{inventory.ReasonOperationInProgress, func(a *Acceptance) {
			other := operation("other", "device-a", inventory.PowerOn)
			meta.SetStatusCondition(&other.Status.Conditions, metav1.Condition{Type: inventory.ConditionPowerAccepted, Status: metav1.ConditionTrue, Reason: "Accepted"})
			a.Others = []inventory.DevicePowerOperation{*other}
		}},
		{inventory.ReasonSystemUUIDNotRecorded, func(a *Acceptance) { a.Device.Spec.Identity.SystemUUID = "" }},
		{inventory.ReasonSystemUUIDMismatch, func(a *Acceptance) { a.Operation.Spec.ExpectedSystemUUID = uuidB }},
		{inventory.ReasonDiscoveryNotCurrent, func(a *Acceptance) { a.Device.Spec.BMC = nil }},
		{inventory.ReasonDiscoveryNotCurrent, func(a *Acceptance) {
			a.Now = t0.Add(2 * time.Hour)
			a.Operation.CreationTimestamp = metav1.NewTime(a.Now)
		}},
		{inventory.ReasonDiscoveryNotCurrent, func(a *Acceptance) { a.Device.Status.Discovery.Inventory.Endpoint = "https://192.0.2.11:443" }},
		{inventory.ReasonDiscoveryNotCurrent, func(a *Acceptance) { a.Device.Generation = 2 }},
		{inventory.ReasonDiscoveryNotCurrent, func(a *Acceptance) {
			meta.SetStatusCondition(&a.Device.Status.Conditions, metav1.Condition{Type: inventory.ConditionIdentityMatched, Status: metav1.ConditionFalse, Reason: "SystemUUIDMismatch", ObservedGeneration: 1})
		}},
		{inventory.ReasonDiscoveryNotCurrent, func(a *Acceptance) {
			meta.RemoveStatusCondition(&a.Device.Status.Conditions, inventory.ConditionPublicKeyUnique)
		}},
		{inventory.ReasonSystemNotObserved, func(a *Acceptance) {
			inv := a.Device.Status.Discovery.Inventory
			inv.Systems = append(inv.Systems, inv.Systems[0])
		}},
		{inventory.ReasonSystemNotObserved, func(a *Acceptance) { a.Device.Status.Discovery.Inventory.Systems[0].UUID = uuidB }},
		{inventory.ReasonSharedPublicKey, func(a *Acceptance) {
			meta.SetStatusCondition(&a.Device.Status.Conditions, metav1.Condition{Type: inventory.ConditionPublicKeyUnique, Status: metav1.ConditionFalse, Reason: "SharedPublicKey", ObservedGeneration: 1})
		}},
		{inventory.ReasonResetTypeNotOffered, func(a *Acceptance) { a.Operation.Spec.Action = inventory.PowerCycle }},
		{inventory.ReasonResetTypeNotOffered, func(a *Acceptance) { a.Device.Status.Discovery.Inventory.Systems[0].ResetTypes = nil }},
	} {
		t.Run(tc.want, func(t *testing.T) {
			a := base()
			tc.mutate(&a)
			if r := Check(a); r == nil || r.Reason != tc.want {
				t.Fatalf("refusal = %+v, want %s", r, tc.want)
			}
		})
	}
}

func TestCheckIgnoresFinishedAndOtherDevicesOperations(t *testing.T) {
	a := Acceptance{Operation: operation("op", "device-a", inventory.PowerOn), Device: discoveredDevice("device-a"), DiscoveryMaxAge: time.Hour, Now: t0,
		Capacities: []metal.MachineCapacity{*capacity("cap", "device-b")}}
	done := operation("done", "device-a", inventory.PowerOn)
	meta.SetStatusCondition(&done.Status.Conditions, metav1.Condition{Type: inventory.ConditionPowerAccepted, Status: metav1.ConditionTrue, Reason: "Accepted"})
	done.Status.Result = inventory.PowerSucceeded
	elsewhere := operation("elsewhere", "device-b", inventory.PowerOn)
	meta.SetStatusCondition(&elsewhere.Status.Conditions, metav1.Condition{Type: inventory.ConditionPowerAccepted, Status: metav1.ConditionTrue, Reason: "Accepted"})
	a.Others = []inventory.DevicePowerOperation{*done, *elsewhere, *a.Operation}
	if r := Check(a); r != nil {
		t.Fatalf("refused: %+v", r)
	}
}

func TestNamesAreDerivedAndBounded(t *testing.T) {
	if name, ok := CredentialSecretName("default", "device-a"); !ok || name != "bmc.default.device-a" {
		t.Fatalf("credential name = %q %v", name, ok)
	}
	long := make([]byte, 250)
	for i := range long {
		long[i] = 'a'
	}
	if _, ok := CredentialSecretName("default", string(long)); ok {
		t.Fatal("an over-long credential name was accepted")
	}
	if a, b := LockName(project, "a-b", "c"), LockName(project, "a", "b-c"); a == b || len(a) > 63 {
		t.Fatalf("lock names %q %q", a, b)
	}
}
