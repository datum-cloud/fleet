package sessionfixture

import (
	"context"
	"errors"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/controller"
)

type Fixture struct {
	Client           client.Client
	Coordinator      *controller.Coordinator
	PrivateNamespace string
}

func (f Fixture) Allocate(ctx context.Context, machineKey, capacityKey client.ObjectKey, principal, member string) (*controller.AssetRecord, error) {
	if principal == "" || member == "" {
		return nil, errors.New("explicit synthetic principal and member cluster are required")
	}
	var claim fleetv1.Machine
	var capacity fleetv1.MachineCapacity
	var consumer, provider corev1.Namespace
	for _, item := range []struct {
		key client.ObjectKey
		obj client.Object
	}{{machineKey, &claim}, {capacityKey, &capacity}, {client.ObjectKey{Name: machineKey.Namespace}, &consumer}, {client.ObjectKey{Name: capacityKey.Namespace}, &provider}} {
		if err := f.Client.Get(ctx, item.key, item.obj); err != nil {
			return nil, err
		}
		if item.obj.GetUID() == "" || item.obj.GetDeletionTimestamp() != nil {
			return nil, controller.ErrRecordInvalid
		}
	}
	if claim.Spec.LocationRef.Name == "" || claim.Spec.MachineClassRef.Name == "" || claim.Spec.MachineProfileRef.Name == "" || claim.Spec.PowerState != "Off" || capacity.Spec.DeviceRef.Name == "" || capacity.Spec.AllocationPolicy != "Enabled" || len(claim.Spec.NetworkInterfaces) != 0 || claim.Spec.InitialInstallationRef != nil {
		return nil, errors.New("synthetic fixture requires a location, class, profile, device, enabled capacity and powered-off networkless claim without installation")
	}
	grant := Grant{Principal: principal, MachineNamespace: claim.Namespace, MachineName: claim.Name, MachineGeneration: claim.Generation, CapacityGeneration: capacity.Generation, Access: controller.ManagementAccess{
		Scope: controller.LocalClaimScope(consumer.UID), NamespaceUID: consumer.UID, MachineUID: claim.UID,
		ConsumerProject:   controller.ProjectIdentity{AuthorityID: "local", RouteName: consumer.Name, UID: consumer.UID},
		ProviderProject:   controller.ProjectIdentity{AuthorityID: "local", RouteName: provider.Name, UID: provider.UID},
		CapacityNamespace: capacity.Namespace, CapacityName: capacity.Name, CapacityUID: capacity.UID, DeviceName: capacity.Spec.DeviceRef.Name,
		LocationName: claim.Spec.LocationRef.Name, MemberCluster: member,
	}}
	if err := saveGrant(ctx, f.Client, f.PrivateNamespace, grant); err != nil {
		return nil, err
	}
	admission := controller.AdmissionAuthority{PrincipalUID: principal, DecisionID: "synthetic-session-fixture"}
	if err := f.Coordinator.EnsureClaimCustody(ctx, &claim, consumer.UID, admission); err != nil {
		return nil, err
	}
	if !controllerutil.ContainsFinalizer(&claim, controller.MachineFinalizer) {
		controllerutil.AddFinalizer(&claim, controller.MachineFinalizer)
		if err := f.Client.Update(ctx, &claim); err != nil {
			return nil, err
		}
	}
	capacity.Status.Conditions = nil
	for _, condition := range []string{"Prepared", "Healthy", "Available"} {
		capacity.Status.Conditions = append(capacity.Status.Conditions, metav1.Condition{Type: condition, Status: metav1.ConditionTrue, ObservedGeneration: capacity.Generation, LastTransitionTime: metav1.Now(), Reason: "SyntheticFixture"})
	}
	capacity.Status.Hardware.CPU.Architecture = "amd64"
	class := &fleetv1.MachineClass{ObjectMeta: metav1.ObjectMeta{Name: claim.Spec.MachineClassRef.Name, UID: "synthetic-class"}, Spec: fleetv1.MachineClassSpec{Capacity: &fleetv1.CapacityPolicy{MachineCapacities: fleetv1.MachineSelector{Namespace: capacity.Namespace}}}}
	profile := &fleetv1.MachineProfile{ObjectMeta: metav1.ObjectMeta{Name: claim.Spec.MachineProfileRef.Name, UID: "synthetic-profile"}, Spec: fleetv1.MachineProfileSpec{CPU: fleetv1.CPURequirement{Architecture: "amd64"}}}
	return f.Coordinator.Allocate(ctx, controller.AllocationRequest{Claim: &claim, Class: class, Profile: profile, MachineCapacities: []fleetv1.MachineCapacity{capacity}, NamespaceUID: consumer.UID, Admission: admission, EvidenceObservedAt: map[types.UID]time.Time{capacity.UID: time.Now()}})
}

func (f Fixture) Release(ctx context.Context, machineKey client.ObjectKey, uid types.UID) (controller.ReleaseResult, error) {
	if uid == "" {
		return controller.ReleaseResult{}, errors.New("exact --machine-uid required for release")
	}
	result, err := f.Coordinator.Release(ctx, uid)
	if err != nil {
		return result, err
	}
	if !result.SafeToRemoveFinalizer {
		return result, errors.New("release is not verified; retain custody and retry")
	}
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var claim fleetv1.Machine
		if err := f.Client.Get(ctx, machineKey, &claim); err != nil {
			return client.IgnoreNotFound(err)
		}
		if claim.UID != uid || !controllerutil.ContainsFinalizer(&claim, controller.MachineFinalizer) {
			return nil
		}
		controllerutil.RemoveFinalizer(&claim, controller.MachineFinalizer)
		return f.Client.Update(ctx, &claim)
	})
	return result, err
}
