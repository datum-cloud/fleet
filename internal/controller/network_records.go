package controller

import (
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	fleetnetwork "go.datum.net/fleet/internal/network"
)

type NetworkRecord struct {
	Plan            fleetnetwork.Plan                      `json:"plan"`
	Observation     fleetnetwork.Observation               `json:"observation,omitempty"`
	Lifecycle       []fleetnetwork.InterfaceLifecycleState `json:"lifecycle,omitempty"`
	ReleaseRevision string                                 `json:"releaseRevision,omitempty"`
}

func validateNetworkRecord(record *NetworkRecord) error {
	if record == nil {
		return nil
	}
	if !record.Plan.Valid() {
		return ErrRecordInvalid
	}
	if len(record.Lifecycle) != 0 && len(record.Lifecycle) != len(record.Plan.Interfaces) {
		return ErrRecordInvalid
	}
	planned := map[string]bool{}
	for _, iface := range record.Plan.Interfaces {
		planned[iface.Name] = true
	}
	seenObservations := map[string]bool{}
	for _, observation := range record.Observation.Interfaces {
		if !planned[observation.Name] || seenObservations[observation.Name] {
			return ErrRecordInvalid
		}
		seenObservations[observation.Name] = true
	}
	seen := map[string]bool{}
	for _, state := range record.Lifecycle {
		if !planned[state.Name] || seen[state.Name] || !state.CreateAdmitted || state.GuardToken == "" || state.DeleteCompleted && !state.DeleteAdmitted || (state.RetainedInterfaceName == "") != (state.RetainedInterfaceUID == "") || state.NoBindingVerified && state.RetainedInterfaceUID != "" {
			return ErrRecordInvalid
		}
		seen[state.Name] = true
	}
	if record.ReleaseRevision != "" {
		if len(record.Lifecycle) != len(record.Plan.Interfaces) {
			return ErrRecordInvalid
		}
		for _, state := range record.Lifecycle {
			if !state.DeleteCompleted {
				return ErrRecordInvalid
			}
		}
	}
	return nil
}

// ProjectNetworkStatus publishes only consumer-safe NSO facts. Retained UIDs,
// claim names, binding metadata, and raw provider messages remain private.
func ProjectNetworkStatus(record *NetworkRecord, now time.Time, maxEvidenceAge time.Duration) (*fleetv1.NetworkStatus, metav1.Condition) {
	if record == nil {
		return nil, metav1.Condition{Type: "NetworkReady", Status: metav1.ConditionTrue, Reason: "NoAttachmentsRequired", Message: "The claim requests no network attachments."}
	}
	status := &fleetv1.NetworkStatus{Revision: record.Observation.Revision}
	observed := map[string]fleetnetwork.InterfaceObservation{}
	for _, iface := range record.Observation.Interfaces {
		observed[iface.Name] = iface
	}
	for _, planned := range record.Plan.Interfaces {
		projected := fleetv1.NetworkInterfaceStatus{Name: planned.Name, Network: fleetv1.NetworkReference{APIGroup: "networking.datumapis.com", Kind: "Network", Namespace: planned.NetworkNamespace, Name: planned.NetworkName}}
		if iface, ok := observed[planned.Name]; ok {
			projected.MTU = iface.MTU
			for _, address := range iface.Addresses {
				if address.Class == "" {
					projected.Addresses = append(projected.Addresses, fleetv1.AddressAssignment{Address: address.Address, Family: address.Family, Gateway: address.Gateway, Primary: address.Primary})
				} else {
					projected.ExternalAddresses = append(projected.ExternalAddresses, fleetv1.ExternalAddressAssignment{Address: address.Address, Family: address.Family, ClassRef: fleetv1.LocalReference{Name: address.Class}})
				}
			}
			for _, condition := range iface.Conditions {
				message := "Networking has not verified this requirement."
				if condition.Status == metav1.ConditionTrue {
					message = "Networking verified this requirement."
				}
				projected.Conditions = append(projected.Conditions, metav1.Condition{Type: condition.Type, Status: condition.Status, Reason: normalizedNetworkReason(condition), Message: message, ObservedGeneration: condition.ObservedGeneration})
			}
		}
		status.Interfaces = append(status.Interfaces, projected)
	}
	current := maxEvidenceAge > 0 && !record.Observation.ObservedAt.IsZero() && !record.Observation.ObservedAt.After(now) && now.Sub(record.Observation.ObservedAt) <= maxEvidenceAge
	if current && record.Observation.Programmed(record.Plan) {
		return status, metav1.Condition{Type: "NetworkReady", Status: metav1.ConditionTrue, Reason: "AttachmentsVerified", Message: "Networking verified every requested interface."}
	}
	return status, metav1.Condition{Type: "NetworkReady", Status: metav1.ConditionUnknown, Reason: "NetworkPending", Message: "Waiting for current network allocation, preparation, and programming evidence."}
}

func normalizedNetworkReason(condition fleetnetwork.Condition) string {
	if condition.Status == metav1.ConditionTrue {
		return "Verified"
	}
	switch condition.Reason {
	case "NetworkNotAvailableInLocation", "AddressFamilyNotCarried", "AddressFamiliesUnknown", "NetworkContextTerminating", "SubnetTerminating":
		return condition.Reason
	default:
		return "NetworkPending"
	}
}
