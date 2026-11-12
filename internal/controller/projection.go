// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"fmt"
	"reflect"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	fleetv1alpha1 "go.datum.net/fleet/internal/api/session/v1alpha1"
	fleetv1 "go.datum.net/fleet/internal/api/v1alpha1"
)

// ProjectionReconciler writes only to the Karmada hub. Karmada carries the
// projection to the exact member cluster retained at admission and reflects
// cell-written status. This controller has no cell credential.
type ProjectionReconciler struct {
	client.Client
	FederationClient client.Client
	SystemNamespace  string
	HubNamespace     string
	Now              func() time.Time
	Freshness        time.Duration
}

func (r *ProjectionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var b fleetv1alpha1.MachineManagementSessionBinding
	if err := r.Get(ctx, req.NamespacedName, &b); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if req.Namespace != r.systemNamespace() {
		return ctrl.Result{}, nil
	}
	key := types.NamespacedName{Namespace: r.hubNamespace(), Name: b.Name}
	active := r.allocationStillActive(ctx, &b)
	if !active && b.Annotations[RevokedAtAnnotation] == "" {
		patch := client.MergeFrom(b.DeepCopy())
		if b.Annotations == nil {
			b.Annotations = map[string]string{}
		}
		b.Annotations[RevokedAtAnnotation] = r.now().UTC().Format(time.RFC3339Nano)
		if err := r.Patch(ctx, &b, patch); err != nil {
			return ctrl.Result{}, err
		}
	}

	var p fleetv1alpha1.ProjectedMachineManagementSession
	err := r.FederationClient.Get(ctx, key, &p)
	if apierrors.IsNotFound(err) {
		if b.Annotations[RevokedAtAnnotation] != "" || !active {
			return ctrl.Result{}, nil
		}
		p = *r.build(&b)
		if err := r.FederationClient.Create(ctx, &p); err != nil {
			return ctrl.Result{}, err
		}
		if err := r.ensurePolicy(ctx, &b); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: time.Second}, nil
	}
	if err != nil {
		return ctrl.Result{}, err
	}
	if !projectionMatches(&b, &p) {
		setNamedCondition(&b.Status.Conditions, fleetv1alpha1.ConditionReady, b.Generation, metav1.ConditionFalse, "ProjectionChanged", "The federated projection no longer matches the admitted session inputs.", r.now())
		return ctrl.Result{}, r.Status().Update(ctx, &b)
	}
	if err := r.ensurePolicy(ctx, &b); err != nil {
		return ctrl.Result{}, err
	}
	if b.Annotations[RevokedAtAnnotation] != "" || !active {
		return r.revokeProjection(ctx, &b, &p)
	}

	fresh := metav1.NewTime(r.now().Add(r.freshness()))
	if p.Spec.DesiredState != fleetv1alpha1.ProjectionActive || p.Spec.AuthorityFreshUntil.Before(&fresh) {
		patch := client.MergeFrom(p.DeepCopy())
		p.Spec.DesiredState = fleetv1alpha1.ProjectionActive
		p.Spec.AuthorityFreshUntil = fresh
		if err := r.FederationClient.Patch(ctx, &p, patch); err != nil {
			return ctrl.Result{}, err
		}
	}
	if !reflect.DeepEqual(b.Status.Connection, p.Status.Connection) || !reflect.DeepEqual(b.Status.Conditions, p.Status.Conditions) {
		b.Status.Connection = p.Status.Connection
		b.Status.Conditions = append([]metav1.Condition(nil), p.Status.Conditions...)
		for i := range b.Status.Conditions {
			b.Status.Conditions[i].ObservedGeneration = b.Generation
		}
		if err := r.Status().Update(ctx, &b); err != nil {
			return ctrl.Result{}, err
		}
	}
	if p.Status.Connection == nil {
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}
	return ctrl.Result{RequeueAfter: r.freshness() / 2}, nil
}

func (r *ProjectionReconciler) allocationStillActive(ctx context.Context, b *fleetv1alpha1.MachineManagementSessionBinding) bool {
	var allocation fleetv1.MachineAllocation
	if err := r.Get(ctx, types.NamespacedName{Namespace: r.systemNamespace(), Name: b.Spec.AllocationName}, &allocation); err != nil {
		return false
	}
	if allocation.UID != b.Spec.AllocationUID || allocation.Status.Phase != "Active" ||
		allocation.Spec.MachineRef.Project != b.Spec.ConsumerProject ||
		allocation.Spec.MachineRef.Namespace != b.Spec.MachineNamespace || allocation.Spec.MachineRef.Name != b.Spec.MachineName ||
		allocation.Spec.MachineCapacityRef.Namespace != b.Spec.MachineCapacityNamespace || allocation.Spec.MachineCapacityRef.Name != b.Spec.MachineCapacityName ||
		allocation.Annotations[AllocationMachineUIDAnnotation] != string(b.Spec.MachineUID) ||
		allocation.Annotations[MemberClusterAnnotation] != b.Spec.MemberCluster ||
		allocation.Annotations[LocationNameAnnotation] != b.Spec.LocationName ||
		allocation.Annotations[ProviderProjectAnnotation] != b.Spec.ProviderProject {
		return false
	}
	var machine fleetv1.Machine
	if err := r.Get(ctx, types.NamespacedName{Namespace: b.Spec.MachineNamespace, Name: b.Spec.MachineName}, &machine); err != nil || machine.UID != b.Spec.MachineUID {
		return false
	}
	var capacity fleetv1.MachineCapacity
	if err := r.Get(ctx, types.NamespacedName{Namespace: b.Spec.MachineCapacityNamespace, Name: b.Spec.MachineCapacityName}, &capacity); err != nil {
		return false
	}
	return capacity.UID == b.Spec.MachineCapacityUID && capacity.Spec.DeviceRef.Name == b.Spec.DeviceName
}

func (r *ProjectionReconciler) revokeProjection(ctx context.Context, b *fleetv1alpha1.MachineManagementSessionBinding, p *fleetv1alpha1.ProjectedMachineManagementSession) (ctrl.Result, error) {
	if p.Spec.DesiredState != fleetv1alpha1.ProjectionRevoked {
		patch := client.MergeFrom(p.DeepCopy())
		p.Spec.DesiredState = fleetv1alpha1.ProjectionRevoked
		p.Spec.AuthorityFreshUntil = metav1.NewTime(r.now())
		if err := r.FederationClient.Patch(ctx, p, patch); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: time.Second}, nil
	}
	cleanup := apimeta.FindStatusCondition(p.Status.Conditions, "CleanupConfirmed")
	if cleanup == nil || cleanup.Status != metav1.ConditionTrue {
		return ctrl.Result{RequeueAfter: time.Second}, nil
	}
	if err := r.deletePolicy(ctx, b.Name); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.FederationClient.Delete(ctx, p); err != nil && !apierrors.IsNotFound(err) {
		return ctrl.Result{}, err
	}
	setNamedCondition(&b.Status.Conditions, "CleanupConfirmed", b.Generation, metav1.ConditionTrue, "LocationTeardownConfirmed", "The selected location gateway confirmed session teardown through Karmada status aggregation.", r.now())
	setNamedCondition(&b.Status.Conditions, fleetv1alpha1.ConditionReady, b.Generation, metav1.ConditionFalse, "Revoked", "The session authority has ended.", r.now())
	return ctrl.Result{}, r.Status().Update(ctx, b)
}

func (r *ProjectionReconciler) build(b *fleetv1alpha1.MachineManagementSessionBinding) *fleetv1alpha1.ProjectedMachineManagementSession {
	s := b.Spec
	return &fleetv1alpha1.ProjectedMachineManagementSession{
		TypeMeta:   metav1.TypeMeta{APIVersion: fleetv1alpha1.GroupVersion.String(), Kind: "ProjectedMachineManagementSession"},
		ObjectMeta: metav1.ObjectMeta{Name: b.Name, Namespace: r.hubNamespace(), Labels: map[string]string{MemberClusterLabel: s.MemberCluster}},
		Spec: fleetv1alpha1.ProjectedMachineManagementSessionSpec{
			DesiredState:       fleetv1alpha1.ProjectionActive,
			MachineCapacityRef: fleetv1alpha1.LocalReference{Name: s.MachineCapacityName},
			Protocol:           s.Protocol, AccessProfile: s.AccessProfile, ClientPublicKey: s.ClientPublicKey,
			ConnectBefore: s.ConnectBefore, ExpiresAt: s.ExpiresAt,
			AuthorityFreshUntil: metav1.NewTime(r.now().Add(r.freshness())),
		},
	}
}

func projectionMatches(b *fleetv1alpha1.MachineManagementSessionBinding, p *fleetv1alpha1.ProjectedMachineManagementSession) bool {
	s, x := b.Spec, p.Spec
	return p.Labels[MemberClusterLabel] == s.MemberCluster &&
		x.MachineCapacityRef.Name == s.MachineCapacityName && x.Protocol == s.Protocol && x.AccessProfile == s.AccessProfile &&
		x.ClientPublicKey == s.ClientPublicKey && x.ConnectBefore.Equal(&s.ConnectBefore) && x.ExpiresAt.Equal(&s.ExpiresAt)
}

func (r *ProjectionReconciler) ensurePolicy(ctx context.Context, b *fleetv1alpha1.MachineManagementSessionBinding) error {
	policy := propagationPolicy(r.hubNamespace(), b.Name, b.Spec.MemberCluster)
	var existing unstructured.Unstructured
	existing.SetAPIVersion("policy.karmada.io/v1alpha1")
	existing.SetKind("PropagationPolicy")
	err := r.FederationClient.Get(ctx, types.NamespacedName{Namespace: r.hubNamespace(), Name: policy.GetName()}, &existing)
	if apierrors.IsNotFound(err) {
		return r.FederationClient.Create(ctx, policy)
	}
	if err != nil {
		return err
	}
	clusters, _, _ := unstructured.NestedStringSlice(existing.Object, "spec", "placement", "clusterAffinity", "clusterNames")
	if len(clusters) != 1 || clusters[0] != b.Spec.MemberCluster {
		return fmt.Errorf("propagation policy %s target changed from admitted member %q", existing.GetName(), b.Spec.MemberCluster)
	}
	return nil
}

func propagationPolicy(namespace, sessionName, member string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "policy.karmada.io/v1alpha1", "kind": "PropagationPolicy",
		"metadata": map[string]any{"name": "mms-" + sessionName, "namespace": namespace, "labels": map[string]any{SessionUIDLabel: sessionName}},
		"spec": map[string]any{
			"resourceSelectors": []any{map[string]any{"apiVersion": fleetv1alpha1.GroupVersion.String(), "kind": "ProjectedMachineManagementSession", "name": sessionName, "namespace": namespace}},
			"placement":         map[string]any{"clusterAffinity": map[string]any{"clusterNames": []any{member}}},
		},
	}}
}

func (r *ProjectionReconciler) deletePolicy(ctx context.Context, sessionName string) error {
	p := propagationPolicy(r.hubNamespace(), sessionName, "")
	if err := r.FederationClient.Delete(ctx, p); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

func (r *ProjectionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.FederationClient == nil {
		return fmt.Errorf("federation client is required")
	}
	r.Client = mgr.GetClient()
	return ctrl.NewControllerManagedBy(mgr).
		For(&fleetv1alpha1.MachineManagementSessionBinding{}).
		Watches(&fleetv1.MachineAllocation{}, handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
			a := obj.(*fleetv1.MachineAllocation)
			var list fleetv1alpha1.MachineManagementSessionBindingList
			if err := r.List(ctx, &list, client.InNamespace(r.systemNamespace()), client.MatchingLabels{AllocationNameLabel: a.Name}); err != nil {
				return nil
			}
			out := make([]reconcile.Request, 0, len(list.Items))
			for i := range list.Items {
				out = append(out, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: list.Items[i].Namespace, Name: list.Items[i].Name}})
			}
			return out
		})).Complete(r)
}

func (r *ProjectionReconciler) systemNamespace() string {
	if r.SystemNamespace != "" {
		return r.SystemNamespace
	}
	return SystemNamespace
}
func (r *ProjectionReconciler) hubNamespace() string {
	if r.HubNamespace != "" {
		return r.HubNamespace
	}
	return LocationNamespace
}
func (r *ProjectionReconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}
func (r *ProjectionReconciler) freshness() time.Duration {
	if r.Freshness > 0 {
		return r.Freshness
	}
	return DefaultAuthorityFreshness
}
