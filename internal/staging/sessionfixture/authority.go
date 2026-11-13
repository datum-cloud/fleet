package sessionfixture

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	sessionv1 "go.datum.net/fleet/internal/api/session/v1alpha1"
	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
	"go.datum.net/fleet/internal/controller"
	"go.datum.net/fleet/internal/executor"
	"go.datum.net/fleet/internal/simulator"
)

type Grant struct {
	Principal          string                      `json:"principal"`
	MachineNamespace   string                      `json:"machineNamespace"`
	MachineName        string                      `json:"machineName"`
	MachineGeneration  int64                       `json:"machineGeneration"`
	CapacityGeneration int64                       `json:"capacityGeneration"`
	Access             controller.ManagementAccess `json:"access"`
}

type Resolver struct {
	Client           client.Client
	PrivateNamespace string
}

func grantName(uid types.UID) string {
	digest := sha256.Sum256([]byte(uid))
	return "synthetic-session-" + hex.EncodeToString(digest[:16])
}

func (r Resolver) ResolveManagementAccess(ctx context.Context, binding sessionv1.MachineManagementSessionBindingSpec) (controller.ManagementAccess, error) {
	var secret corev1.Secret
	if err := r.Client.Get(ctx, client.ObjectKey{Namespace: r.PrivateNamespace, Name: grantName(binding.MachineUID)}, &secret); err != nil {
		return controller.ManagementAccess{}, err
	}
	var grant Grant
	if err := json.Unmarshal(secret.Data["grant.json"], &grant); err != nil {
		return controller.ManagementAccess{}, err
	}
	access := grant.Access
	if secret.Immutable == nil || !*secret.Immutable || secret.DeletionTimestamp != nil || grant.Principal == "" || grant.Principal != binding.RequestingPrincipal || grant.MachineNamespace != binding.MachineNamespace || grant.MachineName != binding.MachineName || access.MachineUID != binding.MachineUID || binding.Protocol != "Redfish" || binding.AccessProfile != "Provisioning" {
		return controller.ManagementAccess{}, controller.ErrUnsupported
	}
	var claim fleetv1.Machine
	var capacity fleetv1.MachineCapacity
	var consumer, provider corev1.Namespace
	for _, item := range []struct {
		key client.ObjectKey
		obj client.Object
	}{
		{client.ObjectKey{Namespace: grant.MachineNamespace, Name: grant.MachineName}, &claim},
		{client.ObjectKey{Namespace: access.CapacityNamespace, Name: access.CapacityName}, &capacity},
		{client.ObjectKey{Name: grant.MachineNamespace}, &consumer},
		{client.ObjectKey{Name: access.CapacityNamespace}, &provider},
	} {
		if err := r.Client.Get(ctx, item.key, item.obj); err != nil {
			return controller.ManagementAccess{}, err
		}
		if item.obj.GetDeletionTimestamp() != nil {
			return controller.ManagementAccess{}, controller.ErrRecordInvalid
		}
	}
	if claim.UID != access.MachineUID || claim.Generation != grant.MachineGeneration || consumer.UID != access.NamespaceUID || provider.UID != access.ProviderProject.UID || capacity.UID != access.CapacityUID || capacity.Generation != grant.CapacityGeneration || capacity.Spec.DeviceRef.Name != access.DeviceName || claim.Spec.LocationRef.Name != access.LocationName || capacity.Spec.AllocationPolicy != "Enabled" {
		return controller.ManagementAccess{}, controller.ErrRecordInvalid
	}
	return access, nil
}

func NewAuthority(kube client.Client, namespace string) (*controller.LifecycleSessionAuthority, error) {
	coordinator, err := controller.NewCoordinator(kube, namespace, unavailableExecutor{}, controller.LocalDigestImageResolver{}, nil, time.Minute)
	if err != nil {
		return nil, err
	}
	return &controller.LifecycleSessionAuthority{Coordinator: coordinator, Resolver: Resolver{Client: kube, PrivateNamespace: namespace}}, nil
}

type unavailableExecutor struct{}

func (unavailableExecutor) Execute(context.Context, simulator.Request) (simulator.OperationResult, error) {
	return simulator.OperationResult{}, executor.ErrOutcomeUnknown
}

func (unavailableExecutor) Lookup(context.Context, string) (simulator.OperationResult, bool, error) {
	return simulator.OperationResult{}, false, executor.ErrOutcomeUnknown
}

func saveGrant(ctx context.Context, kube client.Client, namespace string, grant Grant) error {
	data, err := json.Marshal(grant)
	if err != nil {
		return err
	}
	immutable := true
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: grantName(grant.Access.MachineUID)}, Immutable: &immutable, Data: map[string][]byte{"grant.json": data}}
	var existing corev1.Secret
	err = kube.Get(ctx, client.ObjectKeyFromObject(secret), &existing)
	if client.IgnoreNotFound(err) != nil {
		return err
	}
	if err == nil {
		if existing.Immutable == nil || !*existing.Immutable || string(existing.Data["grant.json"]) != string(data) || existing.DeletionTimestamp != nil {
			return errors.New("synthetic grant differs from retained immutable inputs")
		}
		return nil
	}
	return kube.Create(ctx, secret)
}
