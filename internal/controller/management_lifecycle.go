package controller

import (
	"context"
	"errors"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	sessionv1 "go.datum.net/fleet/internal/api/session/v1alpha1"
)

var ErrManagementActive = errors.New("management session teardown is not verified")

type ManagementSessionRecord struct {
	Binding            sessionv1.MachineManagementSessionBindingSpec `json:"binding"`
	Revoking           bool                                          `json:"revoking"`
	Completed          bool                                          `json:"completed"`
	ProjectionAdmitted bool                                          `json:"projectionAdmitted"`
}

type ManagementAccess struct {
	Scope             ClaimScope
	ConsumerProject   ProjectIdentity
	ProviderProject   ProjectIdentity
	NamespaceUID      types.UID
	MachineUID        types.UID
	CapacityNamespace string
	CapacityName      string
	CapacityUID       types.UID
	DeviceName        string
	MemberCluster     string
	LocationName      string
}

type ManagementAccessResolver interface {
	ResolveManagementAccess(context.Context, sessionv1.MachineManagementSessionBindingSpec) (ManagementAccess, error)
}

type SessionLifecycle interface {
	Recover(context.Context, *sessionv1.MachineManagementSession) (*sessionv1.MachineManagementSessionBindingSpec, error)
	Prepare(context.Context, sessionv1.MachineManagementSessionBindingSpec) (sessionv1.MachineManagementSessionBindingSpec, error)
	Acquire(context.Context, sessionv1.MachineManagementSessionBindingSpec) (sessionv1.MachineManagementSessionBindingSpec, error)
	Active(context.Context, *sessionv1.MachineManagementSessionBinding) bool
	Complete(context.Context, *sessionv1.MachineManagementSessionBinding) error
	BeginProjection(context.Context, *sessionv1.MachineManagementSessionBinding) error
	CancelUnprojected(context.Context, *sessionv1.MachineManagementSessionBinding) (bool, error)
}

func (a *LifecycleSessionAuthority) BeginProjection(ctx context.Context, binding *sessionv1.MachineManagementSessionBinding) error {
	if !a.Active(ctx, binding) || binding.Annotations[RevokedAtAnnotation] != "" {
		return ErrManagementActive
	}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		secret, asset, err := a.readBindingAsset(ctx, binding)
		if err != nil {
			return err
		}
		if !managementHeld(asset) || asset.State != AssetCommitted || asset.Management.Revoking || !equality.Semantic.DeepEqual(asset.Management.Binding, binding.Spec) {
			return ErrManagementActive
		}
		if asset.Management.ProjectionAdmitted {
			return nil
		}
		asset.Management.ProjectionAdmitted = true
		if err := encodeRecord(secret, asset, "asset"); err != nil {
			return err
		}
		return a.Coordinator.client.Update(ctx, secret)
	})
}

func (a *LifecycleSessionAuthority) CancelUnprojected(ctx context.Context, binding *sessionv1.MachineManagementSessionBinding) (bool, error) {
	if a == nil || a.Coordinator == nil || binding.Annotations[RevokedAtAnnotation] == "" {
		return false, ErrManagementActive
	}
	completed := false
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		secret, asset, err := a.readBindingAsset(ctx, binding)
		if err != nil {
			return err
		}
		if asset.Management == nil || !equality.Semantic.DeepEqual(asset.Management.Binding, binding.Spec) {
			return ErrRecordInvalid
		}
		if asset.Management.ProjectionAdmitted {
			return nil
		}
		asset.Management.Revoking = true
		if err := encodeRecord(secret, asset, "asset"); err != nil {
			return err
		}
		if err := a.Coordinator.client.Update(ctx, secret); err != nil {
			return err
		}
		completed = true
		return nil
	})
	return completed, err
}

func (a *LifecycleSessionAuthority) Recover(ctx context.Context, session *sessionv1.MachineManagementSession) (*sessionv1.MachineManagementSessionBindingSpec, error) {
	if a == nil || a.Coordinator == nil {
		return nil, ErrUnsupported
	}
	var records corev1.SecretList
	if err := a.Coordinator.client.List(ctx, &records, client.InNamespace(a.Coordinator.namespace), client.MatchingLabels{recordTypeLabel: "asset"}); err != nil {
		return nil, err
	}
	var retained *sessionv1.MachineManagementSessionBindingSpec
	for index := range records.Items {
		asset, err := decodeAssetSecret(&records.Items[index])
		if err != nil {
			return nil, err
		}
		if asset.Management == nil || asset.Management.Binding.SessionUID != session.UID {
			continue
		}
		binding := asset.Management.Binding
		if retained != nil || binding.SessionNamespace != session.Namespace || binding.SessionName != session.Name || binding.MachineName != session.Spec.MachineRef.Name || binding.RequestingPrincipal != session.Annotations[PrincipalAnnotation] || binding.ClientPublicKey != session.Spec.ClientPublicKey || binding.Protocol != session.Spec.Protocol || binding.AccessProfile != session.Spec.AccessProfile {
			return nil, ErrRecordInvalid
		}
		retained = &binding
	}
	return retained, nil
}

func (a *LifecycleSessionAuthority) Prepare(ctx context.Context, binding sessionv1.MachineManagementSessionBindingSpec) (sessionv1.MachineManagementSessionBindingSpec, error) {
	asset, access, err := a.resolve(ctx, binding)
	if err != nil {
		return binding, err
	}
	binding.ConsumerProject, binding.ProviderProject = access.ConsumerProject.RouteName, access.ProviderProject.RouteName
	binding.AllocationName, binding.AllocationUID = assetRecordName(asset.AssetID), types.UID(asset.BindingID)
	binding.MachineCapacityNamespace, binding.MachineCapacityName, binding.MachineCapacityUID = asset.MachineNamespace, asset.MachineName, asset.MachineUID
	binding.DeviceName, binding.LocationName, binding.MemberCluster = asset.DeviceName, asset.LocationName, access.MemberCluster
	return binding, nil
}

type LifecycleSessionAuthority struct {
	Coordinator *Coordinator
	Resolver    ManagementAccessResolver
}

func (a *LifecycleSessionAuthority) resolve(ctx context.Context, binding sessionv1.MachineManagementSessionBindingSpec) (*AssetRecord, ManagementAccess, error) {
	if a == nil || a.Coordinator == nil || a.Resolver == nil || binding.RequestingPrincipal == "" || binding.MachineUID == "" {
		return nil, ManagementAccess{}, ErrUnsupported
	}
	access, err := a.Resolver.ResolveManagementAccess(ctx, binding)
	if err != nil {
		return nil, access, err
	}
	if !access.Scope.Valid() || !access.ConsumerProject.Valid() || !access.ProviderProject.Valid() || access.MemberCluster == "" || access.LocationName == "" || access.Scope.ProjectUID != access.ConsumerProject.UID || access.Scope.AuthorityID != access.ConsumerProject.AuthorityID {
		return nil, access, ErrRecordInvalid
	}
	custody, _, err := a.Coordinator.getCustodyScoped(ctx, access.Scope, binding.MachineUID)
	if err != nil {
		return nil, access, err
	}
	if custody.State != CustodyBound || custody.ClaimName != binding.MachineName || custody.ClaimNamespace != binding.MachineNamespace || custody.NamespaceUID != access.NamespaceUID || access.MachineUID != binding.MachineUID {
		return nil, access, ErrRecordInvalid
	}
	asset, _, err := a.Coordinator.getAsset(ctx, custody.AssetID)
	if err != nil {
		return nil, access, err
	}
	if asset.SchemaVersion != assetRecordSchemaVersion {
		return nil, access, ErrRecordInvalid
	}
	if custody.ConsumerProject != asset.ConsumerProject || custody.ProviderProject != asset.ProviderProject || custody.Authority != asset.Authority || custody.ExternalHold != asset.ExternalHold {
		return nil, access, ErrRecordInvalid
	}
	if asset.ClaimScope != access.Scope || asset.ClaimUID != binding.MachineUID || asset.ClaimName != binding.MachineName || asset.ClaimNamespace != binding.MachineNamespace || asset.State != AssetCommitted || asset.DeviceName == "" || asset.DeviceName != access.DeviceName || asset.MachineNamespace != access.CapacityNamespace || asset.MachineName != access.CapacityName || asset.MachineUID != access.CapacityUID || asset.LocationName != access.LocationName || asset.ActiveOperationID != "" {
		return nil, access, ErrRecordInvalid
	}
	if asset.ConsumerProject.Valid() && (asset.ConsumerProject != access.ConsumerProject || asset.ProviderProject != access.ProviderProject) {
		return nil, access, ErrRecordInvalid
	}
	return asset, access, nil
}

func (a *LifecycleSessionAuthority) Acquire(ctx context.Context, proposed sessionv1.MachineManagementSessionBindingSpec) (sessionv1.MachineManagementSessionBindingSpec, error) {
	proposed.ConnectBefore = metav1.NewTime(proposed.ConnectBefore.UTC().Truncate(time.Second))
	proposed.ExpiresAt = metav1.NewTime(proposed.ExpiresAt.UTC().Truncate(time.Second))
	var retained sessionv1.MachineManagementSessionBindingSpec
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		asset, access, err := a.resolve(ctx, proposed)
		if err != nil {
			return err
		}
		current, secret, err := a.Coordinator.getAsset(ctx, asset.AssetID)
		if err != nil {
			return err
		}
		if !equality.Semantic.DeepEqual(current, asset) {
			return ErrOperationActive
		}
		binding := proposed
		binding.ConsumerProject, binding.ProviderProject = access.ConsumerProject.RouteName, access.ProviderProject.RouteName
		binding.AllocationName, binding.AllocationUID = secret.Name, types.UID(asset.BindingID)
		binding.MachineCapacityNamespace, binding.MachineCapacityName, binding.MachineCapacityUID = asset.MachineNamespace, asset.MachineName, asset.MachineUID
		binding.DeviceName, binding.LocationName, binding.MemberCluster = asset.DeviceName, asset.LocationName, access.MemberCluster
		if asset.Management != nil && asset.Management.Binding.SessionUID == binding.SessionUID {
			previous := asset.Management.Binding
			binding.ConnectBefore, binding.ExpiresAt = previous.ConnectBefore, previous.ExpiresAt
			binding.SessionCorrelationID, binding.OperationCorrelationID = previous.SessionCorrelationID, previous.OperationCorrelationID
			if !equality.Semantic.DeepEqual(previous, binding) || asset.Management.Completed {
				return ErrRecordInvalid
			}
			retained = previous
			return nil
		}
		if managementHeld(asset) {
			return ErrManagementActive
		}
		asset.Management = &ManagementSessionRecord{Binding: binding}
		if err := validateManagementRecord(asset); err != nil {
			return err
		}
		if err := encodeRecord(secret, asset, "asset"); err != nil {
			return err
		}
		if err := a.Coordinator.client.Update(ctx, secret); err != nil {
			return err
		}
		retained = binding
		return nil
	})
	return retained, err
}

func (a *LifecycleSessionAuthority) Active(ctx context.Context, binding *sessionv1.MachineManagementSessionBinding) bool {
	asset, access, err := a.resolve(ctx, binding.Spec)
	return err == nil && managementHeld(asset) && !asset.Management.Revoking && equality.Semantic.DeepEqual(asset.Management.Binding, binding.Spec) && access.MemberCluster == binding.Spec.MemberCluster && access.ConsumerProject.RouteName == binding.Spec.ConsumerProject && access.ProviderProject.RouteName == binding.Spec.ProviderProject
}

func (a *LifecycleSessionAuthority) Complete(ctx context.Context, binding *sessionv1.MachineManagementSessionBinding) error {
	if a == nil || a.Coordinator == nil || binding.Annotations[RevokedAtAnnotation] == "" {
		return ErrManagementActive
	}
	condition := apimeta.FindStatusCondition(binding.Status.Conditions, "CleanupConfirmed")
	if condition == nil || condition.Status != metav1.ConditionTrue || condition.ObservedGeneration != binding.Generation {
		return ErrManagementActive
	}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		return a.complete(ctx, binding)
	})
}

func (a *LifecycleSessionAuthority) readBindingAsset(ctx context.Context, binding *sessionv1.MachineManagementSessionBinding) (*corev1.Secret, *AssetRecord, error) {
	var secret corev1.Secret
	if err := a.Coordinator.client.Get(ctx, client.ObjectKey{Namespace: a.Coordinator.namespace, Name: binding.Spec.AllocationName}, &secret); err != nil {
		return nil, nil, err
	}
	asset, err := decodeAssetSecret(&secret)
	if err != nil {
		return nil, nil, err
	}
	if secret.Name != assetRecordName(asset.AssetID) {
		return nil, nil, ErrRecordInvalid
	}
	return &secret, asset, nil
}

func (a *LifecycleSessionAuthority) complete(ctx context.Context, binding *sessionv1.MachineManagementSessionBinding) error {
	secret, asset, err := a.readBindingAsset(ctx, binding)
	if err != nil {
		return err
	}
	if string(binding.Spec.AllocationUID) != asset.BindingID || asset.Management != nil && asset.Management.Binding.SessionUID != binding.Spec.SessionUID {
		return nil
	}
	if asset.Management == nil || !equality.Semantic.DeepEqual(asset.Management.Binding, binding.Spec) {
		return ErrRecordInvalid
	}
	if asset.Management.Completed {
		return nil
	}
	asset.Management.Revoking, asset.Management.Completed = true, true
	if err := encodeRecord(secret, asset, "asset"); err != nil {
		return err
	}
	return a.Coordinator.client.Update(ctx, secret)
}

func managementHeld(asset *AssetRecord) bool {
	return asset.Management != nil && !asset.Management.Completed
}

func validateManagementRecord(asset *AssetRecord) error {
	record := asset.Management
	binding := record.Binding
	if binding.SessionUID == "" || binding.SessionName == "" || binding.SessionNamespace != asset.ClaimNamespace || binding.RequestingPrincipal == "" || binding.ClientPublicKey == "" || binding.Protocol != "Redfish" || binding.AccessProfile != "Provisioning" || binding.ConsumerProject == "" || binding.ProviderProject == "" || binding.MemberCluster == "" || binding.LocationName == "" || binding.LocationName != asset.LocationName || binding.DeviceName == "" || binding.DeviceName != asset.DeviceName || binding.AllocationName != assetRecordName(asset.AssetID) || string(binding.AllocationUID) != asset.BindingID || binding.MachineUID != asset.ClaimUID || binding.MachineName != asset.ClaimName || binding.MachineNamespace != asset.ClaimNamespace || binding.MachineCapacityUID != asset.MachineUID || binding.MachineCapacityName != asset.MachineName || binding.MachineCapacityNamespace != asset.MachineNamespace || binding.ConnectBefore.IsZero() || binding.ExpiresAt.IsZero() || binding.ConnectBefore.After(binding.ExpiresAt.Time) || binding.SessionCorrelationID == "" || binding.OperationCorrelationID == "" {
		return ErrRecordInvalid
	}
	if record.Completed && !record.Revoking || managementHeld(asset) && (asset.ActiveOperationID != "" || asset.State != AssetCommitted && asset.State != AssetReleasing) || asset.State == AssetReleasing && !record.Revoking {
		return ErrRecordInvalid
	}
	return nil
}

func (c *Coordinator) revokeManagement(ctx context.Context, assetID string, claimUID types.UID, releasing bool) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		asset, secret, err := c.getAsset(ctx, assetID)
		if err != nil {
			return err
		}
		if asset.ClaimUID != claimUID || !managementHeld(asset) || asset.ActiveOperationID != "" || asset.State != AssetCommitted && asset.State != AssetReleasing {
			return ErrRecordInvalid
		}
		asset.Management.Revoking = true
		if releasing {
			asset.State = AssetReleasing
		}
		if err := encodeRecord(secret, asset, "asset"); err != nil {
			return err
		}
		return c.client.Update(ctx, secret)
	})
}
